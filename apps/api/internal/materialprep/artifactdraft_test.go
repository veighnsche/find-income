package materialprep_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func artifactTestRequest() materialprep.ArtifactDraftRequest {
	return materialprep.ArtifactDraftRequest{
		OpportunityID: "opp-test", OpportunityTitle: "Go engineer", CompanyName: "Harbour",
		CheckID: "check-test", RouteKind: "direct", RouteJudgment: "application_route",
		RouteDestination: "jobs@example.invalid",
		Documents:        []materialprep.ArtifactDocument{{Label: "CV", Required: true}},
		Targets: []materialprep.ArtifactTarget{
			{Type: "cv", Basis: `requested document "CV"`},
			{Type: "email_subject", Basis: `route destination "jobs@example.invalid"`},
			{Type: "email_body", Basis: `route destination "jobs@example.invalid"`},
		},
		Answered: []materialprep.AnsweredFact{
			{QuestionID: "q-motivation", Question: "Why?", Text: "I like Go.", AnswerVersion: 1},
		},
		CareerSources: []applicationpacks.Source{
			{ID: "cv", Name: "CV", SHA256: strings.Repeat("a", 64), Approved: true,
				Body: "Six years of Go platform work."},
		},
		Profile: store.Preferences{Version: 3, PreferredLocation: "Amsterdam",
			AllowRemote: true, SalaryCurrency: "EUR", Timezone: "Europe/Amsterdam"},
	}
}

func TestDraftArtifactsGroundsAndFences(t *testing.T) {
	ctx := context.Background()
	runner := &fakeStandardRunner{fn: func(input musecode.StandardInput) ([]string, error) {
		return []string{`{"artifacts":[
		  {"type":"cv","content":"Go engineer, six years.","facts":["cv"],"answers":["q-motivation"]},
		  {"type":"email_subject","content":"Application: Go engineer","facts":[],"answers":[]},
		  {"type":"email_body","content":"Dear Harbour, please find my CV.","facts":["cv"],"answers":[]}
		]}`}, nil
	}}
	drafts, err := (&materialprep.StandardDrafter{Runner: runner}).DraftArtifacts(ctx, artifactTestRequest())
	if err != nil || len(drafts) != 3 || runner.calls != 1 {
		t.Fatalf("drafts: %+v calls=%d err=%v", drafts, runner.calls, err)
	}
	input := runner.inputs[0]
	if input.Purpose != materialprep.StandardArtifactPurpose || input.BundleRef != "check-test" ||
		len(input.Targets) != 3 {
		t.Fatalf("input: %+v", input)
	}
	prompt := input.Context["prompt"]
	for _, want := range []string{"jobs@example.invalid", "CV", "I like Go.", "Six years of Go platform work."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt misses %q", want)
		}
	}
	if drafts[0].Type != "cv" || len(drafts[0].FactIDs) != 1 || len(drafts[0].AnswerIDs) != 1 {
		t.Fatalf("cv draft: %+v", drafts[0])
	}
}

func TestDraftArtifactsRejectsUngrounded(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{
		"out of scope":   `{"artifacts":[{"type":"cover_letter","content":"x","facts":[],"answers":[]}]}`,
		"duplicate":      `{"artifacts":[{"type":"cv","content":"x","facts":[],"answers":[]},{"type":"cv","content":"y","facts":[],"answers":[]}]}`,
		"unknown fact":   `{"artifacts":[{"type":"cv","content":"x","facts":["nope"],"answers":[]}]}`,
		"unknown answer": `{"artifacts":[{"type":"cv","content":"x","facts":[],"answers":["q-nope"]}]}`,
		"long subject":   `{"artifacts":[{"type":"email_subject","content":"` + strings.Repeat("s", 501) + `","facts":[],"answers":[]}]}`,
		"empty content":  `{"artifacts":[{"type":"cv","content":"  ","facts":[],"answers":[]}]}`,
		"malformed":      `{"artifacts":[}`,
		"trailing":       `{"artifacts":[]} trailing`,
		"smuggled field": `{"artifacts":[{"type":"cv","content":"x","facts":[],"answers":[],"extra":1}]}`,
	}
	for name, message := range cases {
		runner := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) {
			return []string{message}, nil
		}}
		if _, err := (&materialprep.StandardDrafter{Runner: runner}).DraftArtifacts(ctx, artifactTestRequest()); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := (&materialprep.StandardDrafter{}).DraftArtifacts(ctx, artifactTestRequest()); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("nil runner: %v", err)
	}
	empty := artifactTestRequest()
	empty.Targets = nil
	runner := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) { return []string{`{"artifacts":[]}`}, nil }}
	if _, err := (&materialprep.StandardDrafter{Runner: runner}).DraftArtifacts(ctx, empty); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty scope: %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("empty scope spent a turn")
	}
	subset := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) {
		return []string{`{"artifacts":[{"type":"cv","content":"Go engineer.","facts":["cv"],"answers":[]}]}`}, nil
	}}
	drafts, err := (&materialprep.StandardDrafter{Runner: subset}).DraftArtifacts(ctx, artifactTestRequest())
	if err != nil || len(drafts) != 1 {
		t.Fatalf("subset: %+v err=%v", drafts, err)
	}
}

type stubArtifactDrafter struct {
	calls    int
	requests []materialprep.ArtifactDraftRequest
	fn       func(materialprep.ArtifactDraftRequest) ([]materialprep.ArtifactDraft, error)
}

func (f *stubArtifactDrafter) DraftArtifacts(_ context.Context, req materialprep.ArtifactDraftRequest) ([]materialprep.ArtifactDraft, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.fn == nil {
		return nil, nil
	}
	return f.fn(req)
}

func artifactEntry(set store.ArtifactReadinessSet, artifactType string) store.ArtifactReadinessEntry {
	for _, entry := range set.Entries {
		if entry.Type == artifactType {
			return entry
		}
	}
	return store.ArtifactReadinessEntry{}
}

func TestDraftOpportunityArtifactsCommitsSubset(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "artifact-draft")
	f.svc.Artifacts = &stubArtifactDrafter{fn: func(req materialprep.ArtifactDraftRequest) ([]materialprep.ArtifactDraft, error) {
		if len(req.Targets) != 3 {
			t.Fatalf("targets: %+v", req.Targets)
		}
		return []materialprep.ArtifactDraft{{Type: "cv", Content: "Go engineer, six years.",
			FactIDs: []string{careerEvidenceID}}}, nil
	}}
	checkID, questionSet, workflowRev := prepPins(t, f)
	set, created, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"draft-1", checkID, questionSet, workflowRev)
	if err != nil || !created {
		t.Fatalf("draft: created=%v err=%v", created, err)
	}
	cv := artifactEntry(set, store.ArtifactCV)
	if !cv.Required || cv.State != store.ArtifactStateReady || cv.Current == nil || cv.Current.Version != 1 {
		t.Fatalf("cv: %+v", cv)
	}
	if len(cv.Current.Basis.FactIDs) != 1 || cv.Current.Basis.FactIDs[0] != careerEvidenceID {
		t.Fatalf("cv basis: %+v", cv.Current.Basis)
	}
	subject := artifactEntry(set, store.ArtifactEmailSubject)
	if !subject.Required || subject.State != store.ArtifactStateHeld {
		t.Fatalf("omitted subject must stay held: %+v", subject)
	}
	letter := artifactEntry(set, store.ArtifactCoverLetter)
	if letter.Required || letter.State != store.ArtifactStateNotRequired {
		t.Fatalf("letter: %+v", letter)
	}
	// A second draft with the remaining types commits them without
	// touching the stored CV.
	f.svc.Artifacts = &stubArtifactDrafter{fn: func(req materialprep.ArtifactDraftRequest) ([]materialprep.ArtifactDraft, error) {
		if len(req.Targets) != 2 {
			t.Fatalf("second targets: %+v", req.Targets)
		}
		return []materialprep.ArtifactDraft{
			{Type: "email_subject", Content: "Application: Backend Engineer"},
			{Type: "email_body", Content: "Dear Harbour, please find my CV attached."},
		}, nil
	}}
	set, created, err = f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"draft-2", checkID, questionSet, workflowRev)
	if err != nil || !created {
		t.Fatalf("second draft: created=%v err=%v", created, err)
	}
	if entry := artifactEntry(set, store.ArtifactEmailBody); entry.State != store.ArtifactStateReady {
		t.Fatalf("body: %+v", entry)
	}
	if entry := artifactEntry(set, store.ArtifactCV); entry.Current.Version != 1 {
		t.Fatalf("cv touched: %+v", entry)
	}
}

func TestDraftOpportunityArtifactsJournalsActivity(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "artifact-journal")
	f.svc.Artifacts = &stubArtifactDrafter{fn: func(materialprep.ArtifactDraftRequest) ([]materialprep.ArtifactDraft, error) {
		return []materialprep.ArtifactDraft{{Type: "cv", Content: "Go engineer.",
			FactIDs: []string{careerEvidenceID}}}, nil
	}}
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"journal-1", checkID, questionSet, workflowRev); err != nil {
		t.Fatal(err)
	}
	events, _, err := f.db.ListPrepareActivity(ctx, f.opportunity.ID, "", 25)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	want := []string{store.PrepareTurnStarted, store.PrepareArtifactDone,
		store.PrepareArtifactHeld, store.PrepareArtifactHeld, store.PrepareCompleted}
	if len(kinds) != len(want) {
		t.Fatalf("kinds: %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds: %v", kinds)
		}
	}
	if !strings.Contains(string(events[0].Payload), careerEvidenceID) {
		t.Fatalf("turn payload: %s", events[0].Payload)
	}
	if !strings.Contains(string(events[1].Payload), `"type":"cv"`) {
		t.Fatalf("saved payload: %s", events[1].Payload)
	}
	last := string(events[len(events)-1].Payload)
	if !strings.Contains(last, `"drafted":["cv"]`) || !strings.Contains(last, "email_subject") {
		t.Fatalf("completed payload: %s", last)
	}
}

func TestDraftOpportunityArtifactsGuards(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "artifact-guard")
	stub := &stubArtifactDrafter{}
	f.svc.Artifacts = stub
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		" bad", checkID, questionSet, workflowRev); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("request key: %v", err)
	}
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"drift", "missing", questionSet, workflowRev); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("pin drift: %v", err)
	}
	if stub.calls != 0 {
		t.Fatalf("guard spent %d turns", stub.calls)
	}
	f.svc.Artifacts = nil
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"unavail", checkID, questionSet, workflowRev); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("nil artifacts: %v", err)
	}
}
