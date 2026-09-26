package agency

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// memoryClarificationStore is a test-only ClarificationStore pinning the
// I-side semantics: idempotent opens on request key, resolve-once
// answers, job-scoped lists.
type memoryClarificationStore struct {
	mu    sync.Mutex
	seq   int
	items map[string]Clarification
	byKey map[string]string
	byJob map[string][]string
}

func newMemoryClarificationStore() *memoryClarificationStore {
	return &memoryClarificationStore{items: map[string]Clarification{},
		byKey: map[string]string{}, byJob: map[string][]string{}}
}

func (s *memoryClarificationStore) OpenClarification(_ context.Context, _ store.Actor, opportunityID string, input ClarificationOpenInput) (Clarification, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := opportunityID + "\x00" + input.RequestKey
	if id, ok := s.byKey[key]; ok {
		existing := s.items[id]
		if existing.CheckID != input.CheckID || existing.Prompt != input.Prompt ||
			len(existing.AffectedWork) != len(input.AffectedWork) {
			return Clarification{}, false, store.ErrRoundIdempotencyConflict
		}
		return existing, false, nil
	}
	s.seq++
	item := Clarification{ID: fmt.Sprintf("clar-%d", s.seq), OpportunityID: opportunityID,
		CheckID: input.CheckID, Origin: QuestionOriginOwnerClarification,
		Requirement: input.Requirement, Prompt: input.Prompt,
		AffectedWork: append([]ClarificationWorkRef(nil), input.AffectedWork...),
		Status:       ClarificationOpen, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	s.items[item.ID] = item
	s.byKey[key] = item.ID
	s.byJob[opportunityID] = append(s.byJob[opportunityID], item.ID)
	return item, true, nil
}

func (s *memoryClarificationStore) AnswerClarification(_ context.Context, actor store.Actor, id string, input ClarificationAnswer) (Clarification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return Clarification{}, store.ErrNotFound
	}
	if item.Status == ClarificationAnswered {
		if item.Answer == input.Text {
			return item, nil
		}
		return Clarification{}, &ClarificationError{Reason: ClarifyAlreadyAnswered, ID: id,
			Err: fmt.Errorf("clarification already answered")}
	}
	item.Status = ClarificationAnswered
	item.Answer = input.Text
	item.AnsweredAt = time.Now().UTC().Format(time.RFC3339)
	item.AnsweredBy = actor
	s.items[id] = item
	return item, nil
}

func (s *memoryClarificationStore) GetClarification(_ context.Context, id string) (Clarification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return Clarification{}, store.ErrNotFound
	}
	return item, nil
}

func (s *memoryClarificationStore) ListClarifications(_ context.Context, opportunityID string) ([]Clarification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Clarification{}
	for _, id := range s.byJob[opportunityID] {
		out = append(out, s.items[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func clarificationOpenFixture() ClarificationOpenInput {
	return ClarificationOpenInput{RequestKey: "clar-req-1", CheckID: "check-1",
		Requirement: ClarificationRequirement{Statement: "must hold a forklift certificate",
			CaptureID: "cap-1", SpanStart: 10, SpanEnd: 46},
		Prompt:       "Do you hold a forklift certificate? If so, which one and since when?",
		AffectedWork: []ClarificationWorkRef{{Kind: "artifact", ID: "art-cv"}, {Kind: "artifact", ID: "art-letter"}}}
}

func TestClarificationOpenAnswerResolveOnce(t *testing.T) {
	ctx := context.Background()
	db := newMemoryClarificationStore()
	actor := store.Actor{Kind: "owner", ID: "owner"}
	opened, created, err := OpenClarification(ctx, db, actor, "opp-1", clarificationOpenFixture())
	if err != nil || !created {
		t.Fatalf("open=%+v err=%v, want a created clarification", opened, err)
	}
	if opened.Origin != QuestionOriginOwnerClarification || opened.Status != ClarificationOpen {
		t.Fatalf("opened=%+v, want an open owner clarification", opened)
	}
	replayed, created, err := OpenClarification(ctx, db, actor, "opp-1", clarificationOpenFixture())
	if err != nil || created || replayed.ID != opened.ID {
		t.Fatalf("replay=%+v created=%v err=%v, want idempotent open", replayed, created, err)
	}
	answered, err := AnswerClarification(ctx, db, actor, opened.ID,
		ClarificationAnswer{RequestKey: "ans-req-1", Text: "Yes, reach-truck certificate since 2021."})
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != ClarificationAnswered || answered.Answer != "Yes, reach-truck certificate since 2021." {
		t.Fatalf("answered=%+v, want the exact owner text saved", answered)
	}
	if _, err := AnswerClarification(ctx, db, actor, opened.ID,
		ClarificationAnswer{RequestKey: "ans-req-2", Text: "Different answer."}); err == nil {
		t.Fatal("a second answer must conflict instead of forking truth")
	}
}

func TestClarificationValidation(t *testing.T) {
	ctx := context.Background()
	db := newMemoryClarificationStore()
	actor := store.Actor{Kind: "owner", ID: "owner"}
	bad := clarificationOpenFixture()
	bad.Requirement.CaptureID = ""
	if _, _, err := OpenClarification(ctx, db, actor, "opp-1", bad); err == nil {
		t.Fatal("unsourced requirement must fail")
	}
	bad = clarificationOpenFixture()
	bad.AffectedWork = nil
	if _, _, err := OpenClarification(ctx, db, actor, "opp-1", bad); err == nil {
		t.Fatal("missing affected work must fail")
	}
	opened, _, err := OpenClarification(ctx, db, actor, "opp-1", clarificationOpenFixture())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AnswerClarification(ctx, db, actor, opened.ID, ClarificationAnswer{RequestKey: "k"}); err == nil {
		t.Fatal("empty answer text must fail")
	}
}

func TestClarificationJobScopeAndResume(t *testing.T) {
	ctx := context.Background()
	db := newMemoryClarificationStore()
	actor := store.Actor{Kind: "owner", ID: "owner"}
	first, _, err := OpenClarification(ctx, db, actor, "opp-1", clarificationOpenFixture())
	if err != nil {
		t.Fatal(err)
	}
	second := clarificationOpenFixture()
	second.RequestKey = "clar-req-2"
	second.Prompt = "What is your notice period?"
	second.AffectedWork = []ClarificationWorkRef{{Kind: "artifact", ID: "art-email"}}
	other, _, err := OpenClarification(ctx, db, actor, "opp-1", second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AnswerClarification(ctx, db, actor, first.ID,
		ClarificationAnswer{RequestKey: "ans-1", Text: "Reach-truck since 2021."}); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifiedJobContext(ctx, db, "opp-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || verified[0].ID != first.ID {
		t.Fatalf("verified=%v, want only the answered clarification", verified)
	}
	scope := ResumeScope(verified[0])
	if len(scope) != 2 {
		t.Fatalf("scope=%v, want the two affected artifacts", scope)
	}
	stillOpen, err := db.GetClarification(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ResumeScope(stillOpen) != nil {
		t.Fatal("open clarifications resume nothing")
	}
	foreign, err := VerifiedJobContext(ctx, db, "opp-2")
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign=%v err=%v, want job-scoped isolation", foreign, err)
	}
}
