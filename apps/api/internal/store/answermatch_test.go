package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

type answerMatchFixture struct {
	store       *Store
	opportunity Opportunity
	check       CheckView
	round       Round
	answers     []SavedAnswer
}

func setupAnswerMatch(t *testing.T, key string) answerMatchFixture {
	t.Helper()
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, key+"-select")
	started, _, err := s.StartJobCheck(ctx, owner, opportunity.ID,
		CheckStartInput{RequestKey: key + "-check", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, key+"-round",
		[]string{RoundCodexTurn, RoundCheckSave, RoundJevRequest}, opportunity.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, key+"-save", started.WorkflowRevision,
		checkSaveFixture(opportunity.ID, started.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	remote, _, err := s.CreateSavedAnswer(ctx, owner, SavedAnswerCreateInput{
		RequestKey: key + "-remote", Text: "I work remotely from Example City.",
		ScopeTags: []string{"remote", "work_pattern"}, ContextNote: "Remote work answer."})
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := s.CreateSavedAnswer(ctx, owner, SavedAnswerCreateInput{
		RequestKey: key + "-start", Text: "I can start on the first of next month.",
		ScopeTags: []string{"start_date"}, ContextNote: "Start date answer."})
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.CurrentJobCheck(ctx, opportunity.ID)
	if err != nil || status.Status != CheckStatusChecked || len(status.Check.Questions) != 2 {
		t.Fatalf("fixture check: %+v %v", status, err)
	}
	return answerMatchFixture{store: s, opportunity: opportunity, check: *status.Check,
		round: round, answers: []SavedAnswer{remote, start}}
}

func answerMatchOffered(answer SavedAnswer) jev.AnswerMatchCandidate {
	version := answer.Versions[len(answer.Versions)-1]
	excerpt := version.Text
	if len(excerpt) > 2000 {
		excerpt = excerpt[:2000]
	}
	return jev.AnswerMatchCandidate{AnswerID: answer.ID, AnswerVersion: version.Version,
		TextSHA256: version.TextSHA256, ScopeTags: append([]string(nil), answer.ScopeTags...),
		ContextNote: answer.ContextNote, Excerpt: excerpt}
}

func answerMatchBatch(check CheckView, catalog string, questions []CheckQuestionView, offered map[string][]jev.AnswerMatchCandidate) jev.AnswerMatchInput {
	input := jev.AnswerMatchInput{CheckID: check.ID, QuestionSetSHA256: check.QuestionSetSHA256,
		AnswerCatalogDigest: catalog, MaxReportedTokens: 1000}
	for _, question := range questions {
		input.Questions = append(input.Questions, jev.AnswerMatchQuestion{
			QuestionID: question.ID, Text: question.Text, Required: question.Required,
			Kind: question.Kind, TextSHA256: question.TextSHA256, Candidates: offered[question.ID]})
	}
	return input
}

type answerMatchFake struct {
	t       *testing.T
	choices map[string]string // question key -> choice
	calls   int
}

func (f *answerMatchFake) Evaluate(_ context.Context, request jev.Request) (jev.Result, error) {
	f.calls++
	answers := map[string]jev.Answer{}
	for id, question := range request.Questions {
		options := question.(jev.ChoiceQuestion).Criteria
		choice, ok := f.choices[id]
		if !ok {
			f.t.Fatalf("fake has no choice for %s", id)
		}
		probabilities := map[string]float64{}
		for option := range options {
			probabilities[option] = 0
		}
		probabilities[choice] = 1
		answers[id] = jev.Answer{Type: "choice",
			Choice: &jev.ChoiceAnswer{Choice: choice, Probabilities: probabilities, Confidence: 0.8}}
	}
	return jev.Result{RequestedModel: "jev-test-1", ReturnedModel: "jev-test-1",
		Usage: jev.Usage{InputTokens: 12, OutputTokens: 4}, Answers: answers}, nil
}

// answerMatchRaw rebuilds provider bytes for the canonical judged order so the
// captured exchange replays exactly what the fake evaluator returned.
func answerMatchRaw(t *testing.T, input jev.AnswerMatchInput, choices map[string]string) []byte {
	t.Helper()
	ordered := append([]jev.AnswerMatchQuestion(nil), input.Questions...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].QuestionID < ordered[j].QuestionID })
	answers := map[string]any{}
	position := 0
	for _, question := range ordered {
		if len(question.Candidates) == 0 {
			continue
		}
		choice, ok := choices[question.QuestionID]
		if !ok {
			t.Fatalf("no raw choice for %s", question.QuestionID)
		}
		probabilities := map[string]float64{jev.AnswerMatchNoneFits: 0}
		for _, candidate := range question.Candidates {
			probabilities[candidate.AnswerID] = 0
		}
		probabilities[choice] = 1
		answers["match_"+itoaTest(position)] = map[string]any{"type": "choice", "choice": choice,
			"probabilities": probabilities, "confidence": 0.8}
		position++
	}
	raw, err := json.Marshal(map[string]any{"model": "jev-test-1", "answers": answers,
		"usage": map[string]any{"input_tokens": 12, "output_tokens": 4}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func recordAnswerMatchAttempt(t *testing.T, s *Store, round Round, opportunityID, key string, logical, raw []byte) string {
	t.Helper()
	ctx := context.Background()
	owner := ownerActor()
	attempt, _, err := s.ReserveRoundAttempt(ctx, owner, round.ID, RoundAttemptInput{
		RequestKey: key, Operation: RoundJevRequest, ResourceID: "opportunity:" + opportunityID,
		Cost: RoundAllowance{Requests: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, round.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(logical)
	refs, _ := json.Marshal(map[string]string{"check_id": "check"})
	candidates, _ := json.Marshal([]string{"answer", jev.AnswerMatchNoneFits})
	exchange, err := s.BeginJevAttempt(ctx, JevAttemptStart{RoundID: round.ID,
		RoundAttemptID: attempt.ID, Purpose: jev.AnswerMatchPurpose, InputSHA256: hex.EncodeToString(sum[:]),
		SourceRefsJSON: refs, CandidateSetJSON: candidates, ProfileVersion: round.ProfileVersion,
		RubricVersion: jev.AnswerMatchRubricVersion, RequestedModel: "jev-test-1",
		LogicalRequestJSON: logical, TransportRequestBytes: []byte(`{"test":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishJevAttempt(ctx, JevAttemptFinish{ID: exchange.ID, Status: "succeeded",
		ReturnedModel: "jev-test-1", RawResponseBytes: raw}); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]string{"jevAttemptId": exchange.ID})
	if _, err := s.FinishRoundAttempt(ctx, owner, round.ID, attempt.ID, true, result, ""); err != nil {
		t.Fatal(err)
	}
	return exchange.ID
}

// judgeAnswerMatchBatch judges one batch through the fake evaluator and records
// its captured exchange. Choices are keyed by question id.
func judgeAnswerMatchBatch(t *testing.T, f answerMatchFixture, attemptKey string,
	input jev.AnswerMatchInput, choices map[string]string) (jev.AnswerMatchResult, string) {
	t.Helper()
	ctx := context.Background()
	orderedIDs := []string{}
	for _, question := range input.Questions {
		orderedIDs = append(orderedIDs, question.QuestionID)
	}
	sort.Strings(orderedIDs)
	byKey := map[string]string{}
	position := 0
	for _, id := range orderedIDs {
		for _, question := range input.Questions {
			if question.QuestionID != id || len(question.Candidates) == 0 {
				continue
			}
			byKey["match_"+itoaTest(position)] = choices[id]
			position++
		}
	}
	fake := &answerMatchFake{t: t, choices: byKey}
	result, err := jev.MatchAnswers(ctx, fake, input)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Fatalf("one charged call per batch, got %d", fake.calls)
	}
	raw := answerMatchRaw(t, input, choices)
	attemptID := recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID, attemptKey, result.RequestSnapshot, raw)
	return result, attemptID
}

// runAnswerMatchBatch judges one batch through the fake evaluator, records its
// captured exchange, and persists it. Choices are keyed by question id.
func runAnswerMatchBatch(t *testing.T, f answerMatchFixture, requestKey, attemptKey string,
	input jev.AnswerMatchInput, choices map[string]string) (AnswerMatchView, bool) {
	t.Helper()
	result, attemptID := judgeAnswerMatchBatch(t, f, attemptKey, input, choices)
	view, created, err := f.store.SaveAnswerMatch(context.Background(), ownerActor(), f.round.ID, attemptID, requestKey, input, result)
	if err != nil {
		t.Fatal(err)
	}
	return view, created
}

func answerMatchCatalog(t *testing.T, s *Store) string {
	t.Helper()
	digest, err := s.AnswerCatalogDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("catalog digest: %q", digest)
	}
	return digest
}

func TestAnswerMatchSaveAndReadMatched(t *testing.T) {
	f := setupAnswerMatch(t, "match")
	catalog := answerMatchCatalog(t, f.store)
	remote, start := answerMatchOffered(f.answers[0]), answerMatchOffered(f.answers[1])
	questions := f.check.Questions
	input := answerMatchBatch(f.check, catalog, questions, map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote, start},
		questions[1].ID: {remote, start},
	})
	view, created := runAnswerMatchBatch(t, f, "match-key", "match-attempt-0", input,
		map[string]string{questions[0].ID: remote.AnswerID, questions[1].ID: jev.AnswerMatchNoneFits})
	if !created || view.Status != AnswerMatchStatusMatched || len(view.Matches) != 2 {
		t.Fatalf("save: created=%v %+v", created, view)
	}
	if view.CheckID != f.check.ID || view.QuestionSetSHA256 != f.check.QuestionSetSHA256 ||
		view.AnswerCatalogDigest != catalog || view.CatalogMatchedAt == "" {
		t.Fatalf("view pins: %+v", view)
	}
	first, second := view.Matches[0], view.Matches[1]
	if first.QuestionID != questions[0].ID || first.Choice.NoneFits || first.Deterministic ||
		first.Choice.AnswerID != remote.AnswerID || first.Choice.AnswerVersion != remote.AnswerVersion ||
		first.Choice.AnswerTextSHA256 != remote.TextSHA256 {
		t.Fatalf("matched choice: %+v", first)
	}
	wantHash, err := jev.AnswerCandidateSetHash([]jev.AnswerMatchCandidate{remote, start})
	if err != nil || first.CandidateSetHash != wantHash {
		t.Fatalf("candidate hash: %s %v", first.CandidateSetHash, err)
	}
	if first.JevAttemptID == "" || first.Model != "jev-test-1" || first.Confidence != 0.8 || first.MatchedAt == "" {
		t.Fatalf("match provenance: %+v", first)
	}
	if second.QuestionID != questions[1].ID || !second.Choice.NoneFits || second.Deterministic ||
		second.Choice.AnswerID != "" || second.JevAttemptID == "" {
		t.Fatalf("none_fits stays unresolved with provenance: %+v", second)
	}
	read, err := f.store.CurrentAnswerMatch(context.Background(), f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	rawView, _ := json.Marshal(view)
	rawRead, _ := json.Marshal(read)
	if string(rawView) != string(rawRead) {
		t.Fatalf("re-read differs:\n%s\n%s", rawView, rawRead)
	}
}

func TestAnswerMatchNoneFitsRowShape(t *testing.T) {
	f := setupAnswerMatch(t, "nonefits")
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	input := answerMatchBatch(f.check, catalog, questions[:1], map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote},
	})
	runAnswerMatchBatch(t, f, "nonefits-key", "nonefits-attempt-0", input,
		map[string]string{questions[0].ID: jev.AnswerMatchNoneFits})
	var answerID, answerSHA, attempt sql.NullString
	var version sql.NullInt64
	var noneFits int
	err := f.store.db.QueryRowContext(context.Background(), `SELECT answer_id,answer_version,
	  answer_text_sha256,none_fits,jev_attempt_id FROM answer_matches`).Scan(
		&answerID, &version, &answerSHA, &noneFits, &attempt)
	if err != nil {
		t.Fatal(err)
	}
	if answerID.Valid || version.Valid || answerSHA.Valid || noneFits != 1 || !attempt.Valid {
		t.Fatalf("none_fits row: %+v %+v %+v %d %+v", answerID, version, answerSHA, noneFits, attempt)
	}
}

func TestAnswerMatchCandidateVerification(t *testing.T) {
	f := setupAnswerMatch(t, "verify")
	ctx := context.Background()
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	build := func() (jev.AnswerMatchInput, jev.AnswerMatchResult, map[string]string) {
		input := answerMatchBatch(f.check, catalog, questions[:1], map[string][]jev.AnswerMatchCandidate{
			questions[0].ID: {remote},
		})
		choices := map[string]string{questions[0].ID: remote.AnswerID}
		fake := &answerMatchFake{t: t, choices: map[string]string{"match_0": remote.AnswerID}}
		result, err := jev.MatchAnswers(ctx, fake, input)
		if err != nil {
			t.Fatal(err)
		}
		return input, result, choices
	}
	save := func(key string, input jev.AnswerMatchInput, result jev.AnswerMatchResult, choices map[string]string, attemptID string) error {
		_, _, err := f.store.SaveAnswerMatch(ctx, ownerActor(), f.round.ID, attemptID, key, input, result)
		return err
	}
	t.Run("invented choice rejected", func(t *testing.T) {
		input, result, choices := build()
		result.Selections[0].AnswerID = "invented"
		raw := answerMatchRaw(t, input, choices)
		attemptID := recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID, "verify-invented", result.RequestSnapshot, raw)
		if err := save("verify-invented", input, result, choices, attemptID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invented choice: %v", err)
		}
	})
	t.Run("wrong excerpt rejected", func(t *testing.T) {
		input, result, choices := build()
		input.Questions[0].Candidates[0].Excerpt = "Invented wording nowhere in the approved text."
		raw := answerMatchRaw(t, input, choices)
		attemptID := recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID, "verify-excerpt", result.RequestSnapshot, raw)
		if err := save("verify-excerpt", input, result, choices, attemptID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("wrong excerpt: %v", err)
		}
	})
	t.Run("unknown version rejected", func(t *testing.T) {
		input, _, _ := build()
		input.Questions[0].Candidates[0].AnswerVersion = 99
		fake := &answerMatchFake{t: t, choices: map[string]string{"match_0": remote.AnswerID}}
		result, err := jev.MatchAnswers(ctx, fake, input)
		if err != nil {
			t.Fatal(err)
		}
		raw := answerMatchRaw(t, input, map[string]string{questions[0].ID: remote.AnswerID})
		attemptID := recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID, "verify-version", result.RequestSnapshot, raw)
		if err := save("verify-version", input, result, nil, attemptID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unknown version: %v", err)
		}
	})
	t.Run("tampered exchange rejected", func(t *testing.T) {
		input, result, _ := build()
		other := answerMatchBatch(f.check, catalog, questions[:1], map[string][]jev.AnswerMatchCandidate{
			questions[0].ID: {remote},
		})
		otherFake := &answerMatchFake{t: t, choices: map[string]string{"match_0": jev.AnswerMatchNoneFits}}
		otherResult, err := jev.MatchAnswers(ctx, otherFake, other)
		if err != nil {
			t.Fatal(err)
		}
		otherRaw := answerMatchRaw(t, other, map[string]string{questions[0].ID: jev.AnswerMatchNoneFits})
		attemptID := recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID, "verify-swap", otherResult.RequestSnapshot, otherRaw)
		if err := save("verify-swap", input, result, nil, attemptID); !errors.Is(err, ErrFenced) {
			t.Fatalf("swapped exchange: %v", err)
		}
	})
}

func TestAnswerMatchStalenessGuards(t *testing.T) {
	f := setupAnswerMatch(t, "stale")
	ctx := context.Background()
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	build := func() (jev.AnswerMatchInput, jev.AnswerMatchResult, map[string]string) {
		input := answerMatchBatch(f.check, catalog, questions[:1], map[string][]jev.AnswerMatchCandidate{
			questions[0].ID: {remote},
		})
		choices := map[string]string{questions[0].ID: remote.AnswerID}
		fake := &answerMatchFake{t: t, choices: map[string]string{"match_0": remote.AnswerID}}
		result, err := jev.MatchAnswers(ctx, fake, input)
		if err != nil {
			t.Fatal(err)
		}
		return input, result, choices
	}
	record := func(key string, input jev.AnswerMatchInput, result jev.AnswerMatchResult, choices map[string]string) string {
		return recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID, key,
			result.RequestSnapshot, answerMatchRaw(t, input, choices))
	}
	t.Run("outdated check rejected", func(t *testing.T) {
		input, result, choices := build()
		attemptID := record("stale-check-attempt", input, result, choices)
		// A recheck opens only once the basis moved; until then the start
		// converges to the current check.
		if _, err := f.store.db.ExecContext(ctx, `UPDATE opportunities SET revision=revision+1 WHERE id=?`,
			f.opportunity.ID); err != nil {
			t.Fatal(err)
		}
		workflow, err := f.store.RoleWorkflow(ctx, f.opportunity.ID)
		if err != nil {
			t.Fatal(err)
		}
		recheck, created, err := f.store.StartJobCheck(ctx, ownerActor(), f.opportunity.ID,
			CheckStartInput{RequestKey: "stale-recheck", ExpectedOpportunityRevision: f.opportunity.Revision + 1, ExpectedWorkflowRevision: workflow.Revision})
		if err != nil || !created {
			t.Fatalf("recheck: %+v %v", recheck, err)
		}
		if _, _, err := f.store.SaveAnswerMatch(ctx, ownerActor(), f.round.ID, attemptID, "stale-check", input, result); !errors.Is(err, ErrConflict) {
			t.Fatalf("outdated check: %v", err)
		}
	})
	t.Run("moved basis rejected", func(t *testing.T) {
		g := setupAnswerMatch(t, "stalebasis")
		gcatalog := answerMatchCatalog(t, g.store)
		gremote := answerMatchOffered(g.answers[0])
		gquestions := g.check.Questions
		ginput := answerMatchBatch(g.check, gcatalog, gquestions[:1], map[string][]jev.AnswerMatchCandidate{
			gquestions[0].ID: {gremote},
		})
		gchoices := map[string]string{gquestions[0].ID: gremote.AnswerID}
		gfake := &answerMatchFake{t: t, choices: map[string]string{"match_0": gremote.AnswerID}}
		gresult, err := jev.MatchAnswers(ctx, gfake, ginput)
		if err != nil {
			t.Fatal(err)
		}
		gattempt := recordAnswerMatchAttempt(t, g.store, g.round, g.opportunity.ID,
			"stale-basis-attempt", gresult.RequestSnapshot, answerMatchRaw(t, ginput, gchoices))
		// Simulate a concurrent opportunity edit moving the revision the
		// completed check pinned.
		if _, err := g.store.db.ExecContext(ctx, `UPDATE opportunities SET revision=revision+1 WHERE id=?`,
			g.opportunity.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := g.store.SaveAnswerMatch(ctx, ownerActor(), g.round.ID, gattempt, "stale-basis", ginput, gresult); !errors.Is(err, ErrConflict) {
			t.Fatalf("moved basis: %v", err)
		}
	})
}

func TestAnswerMatchCatalogPinning(t *testing.T) {
	f := setupAnswerMatch(t, "pin")
	ctx := context.Background()
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	input := answerMatchBatch(f.check, catalog, questions, map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote},
		questions[1].ID: {remote},
	})
	runAnswerMatchBatch(t, f, "pin-key", "pin-attempt-0", input,
		map[string]string{questions[0].ID: remote.AnswerID, questions[1].ID: remote.AnswerID})
	updated, _, err := f.store.ApproveSavedAnswerVersion(ctx, ownerActor(), f.answers[0].ID,
		SavedAnswerVersionCreateInput{RequestKey: "pin-v2", ExpectedVersion: 1, Text: "I work remotely from Example City, full time."})
	if err != nil {
		t.Fatal(err)
	}
	_ = updated
	view, err := f.store.CurrentAnswerMatch(ctx, f.opportunity.ID)
	if err != nil || view.Status != AnswerMatchStatusOutdated || len(view.Matches) != 2 {
		t.Fatalf("catalog drift must outdated: %+v %v", view, err)
	}
	if view.AnswerCatalogDigest != catalog {
		t.Fatalf("outdated view keeps its pins: %+v", view)
	}
	fresh := answerMatchCatalog(t, f.store)
	if fresh == catalog {
		t.Fatal("catalog digest did not move after approval")
	}
	freshRemote := remote
	freshRemote.AnswerVersion = 2
	freshRemote.TextSHA256 = updated.Versions[1].TextSHA256
	freshRemote.Excerpt = updated.Versions[1].Text
	freshInput := answerMatchBatch(f.check, fresh, questions, map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {freshRemote},
		questions[1].ID: {freshRemote},
	})
	freshView, created := runAnswerMatchBatch(t, f, "pin-key-2", "pin-attempt-1", freshInput,
		map[string]string{questions[0].ID: freshRemote.AnswerID, questions[1].ID: freshRemote.AnswerID})
	if !created || freshView.Status != AnswerMatchStatusMatched || freshView.AnswerCatalogDigest != fresh {
		t.Fatalf("re-match on fresh catalog: %+v", freshView)
	}
}

func TestAnswerMatchPartialBatchesAndIdempotency(t *testing.T) {
	f := setupAnswerMatch(t, "idem")
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	first := answerMatchBatch(f.check, catalog, questions[:1], map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote},
	})
	result, attemptID := judgeAnswerMatchBatch(t, f, "idem-attempt-0", first,
		map[string]string{questions[0].ID: remote.AnswerID})
	save := func(key string) (AnswerMatchView, bool) {
		t.Helper()
		view, created, err := f.store.SaveAnswerMatch(context.Background(), ownerActor(), f.round.ID, attemptID, key, first, result)
		if err != nil {
			t.Fatal(err)
		}
		return view, created
	}
	view, created := save("idem-key")
	if !created || view.Status != AnswerMatchStatusPartial || len(view.Matches) != 1 {
		t.Fatalf("first batch: created=%v %+v", created, view)
	}
	replay, created := save("idem-key")
	if created {
		t.Fatalf("replay must not create: %+v", replay)
	}
	rawFirst, _ := json.Marshal(view)
	rawReplay, _ := json.Marshal(replay)
	if string(rawFirst) != string(rawReplay) {
		t.Fatalf("replay differs:\n%s\n%s", rawFirst, rawReplay)
	}
	second := answerMatchBatch(f.check, catalog, questions[1:], map[string][]jev.AnswerMatchCandidate{
		questions[1].ID: {remote},
	})
	merged, created := runAnswerMatchBatch(t, f, "idem-key", "idem-attempt-2", second,
		map[string]string{questions[1].ID: jev.AnswerMatchNoneFits})
	if !created || merged.Status != AnswerMatchStatusMatched || len(merged.Matches) != 2 {
		t.Fatalf("merged batches: created=%v %+v", created, merged)
	}
	// Same question, different outcome, same key: conflict, never silent swap.
	changed := &answerMatchFake{t: t, choices: map[string]string{"match_0": jev.AnswerMatchNoneFits}}
	changedResult, err := jev.MatchAnswers(context.Background(), changed, first)
	if err != nil {
		t.Fatal(err)
	}
	changedRaw := answerMatchRaw(t, first, map[string]string{questions[0].ID: jev.AnswerMatchNoneFits})
	changedAttempt := recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID,
		"idem-attempt-3", changedResult.RequestSnapshot, changedRaw)
	if _, _, err := f.store.SaveAnswerMatch(context.Background(), ownerActor(), f.round.ID,
		changedAttempt, "idem-key", first, changedResult); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed outcome: %v", err)
	}
}

func TestAnswerMatchSameKeyNewCatalogConflicts(t *testing.T) {
	f := setupAnswerMatch(t, "keyreuse")
	ctx := context.Background()
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	input := answerMatchBatch(f.check, catalog, questions[:1], map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote},
	})
	runAnswerMatchBatch(t, f, "reuse-key", "reuse-attempt-0", input,
		map[string]string{questions[0].ID: remote.AnswerID})
	updated, _, err := f.store.ApproveSavedAnswerVersion(ctx, ownerActor(), f.answers[0].ID,
		SavedAnswerVersionCreateInput{RequestKey: "reuse-v2", ExpectedVersion: 1, Text: "I work remotely from Example City, full time."})
	if err != nil {
		t.Fatal(err)
	}
	fresh := answerMatchCatalog(t, f.store)
	freshRemote := remote
	freshRemote.AnswerVersion = 2
	freshRemote.TextSHA256 = updated.Versions[1].TextSHA256
	freshRemote.Excerpt = updated.Versions[1].Text
	freshInput := answerMatchBatch(f.check, fresh, questions[1:], map[string][]jev.AnswerMatchCandidate{
		questions[1].ID: {freshRemote},
	})
	fake := &answerMatchFake{t: t, choices: map[string]string{"match_0": freshRemote.AnswerID}}
	freshResult, err := jev.MatchAnswers(ctx, fake, freshInput)
	if err != nil {
		t.Fatal(err)
	}
	raw := answerMatchRaw(t, freshInput, map[string]string{questions[1].ID: freshRemote.AnswerID})
	attemptID := recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID,
		"reuse-attempt-1", freshResult.RequestSnapshot, raw)
	if _, _, err := f.store.SaveAnswerMatch(ctx, ownerActor(), f.round.ID,
		attemptID, "reuse-key", freshInput, freshResult); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("reused key with new catalog: %v", err)
	}
}

func TestAnswerMatchDeterministicNeedsNoAttempt(t *testing.T) {
	f := setupAnswerMatch(t, "determ")
	ctx := context.Background()
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	input := answerMatchBatch(f.check, catalog, questions, map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote},
		// questions[1] has no offered candidates: deterministic none_fits.
	})
	fake := &answerMatchFake{t: t, choices: map[string]string{"match_0": remote.AnswerID}}
	result, err := jev.MatchAnswers(ctx, fake, input)
	if err != nil || fake.calls != 1 {
		t.Fatalf("mixed batch: %+v %v", result, err)
	}
	raw := answerMatchRaw(t, input, map[string]string{questions[0].ID: remote.AnswerID})
	attemptID := recordAnswerMatchAttempt(t, f.store, f.round, f.opportunity.ID,
		"determ-attempt-0", result.RequestSnapshot, raw)
	view, created, err := f.store.SaveAnswerMatch(ctx, ownerActor(), f.round.ID, attemptID, "determ-key", input, result)
	if err != nil || !created || view.Status != AnswerMatchStatusMatched {
		t.Fatalf("mixed save: %+v %v", view, err)
	}
	var deterministic int
	var attempt sql.NullString
	var confidence float64
	err = f.store.db.QueryRowContext(ctx, `SELECT deterministic,jev_attempt_id,confidence
	  FROM answer_matches WHERE question_id=?`, questions[1].ID).Scan(&deterministic, &attempt, &confidence)
	if err != nil {
		t.Fatal(err)
	}
	if deterministic != 1 || attempt.Valid || confidence != 0 {
		t.Fatalf("deterministic row: %d %v %v", deterministic, attempt, confidence)
	}
	if !view.Matches[1].Deterministic || view.Matches[1].JevAttemptID != "" || !view.Matches[1].Choice.NoneFits {
		t.Fatalf("deterministic view: %+v", view.Matches[1])
	}
	// A fully deterministic batch persists with no attempt at all.
	empty := answerMatchBatch(f.check, catalog, questions[:1], nil)
	emptyResult, err := jev.MatchAnswers(ctx, nil, empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.SaveAnswerMatch(ctx, ownerActor(), f.round.ID, "", "determ-key-2", empty, emptyResult); err != nil {
		t.Fatalf("deterministic-only save: %v", err)
	}
	if _, _, err := f.store.SaveAnswerMatch(ctx, ownerActor(), f.round.ID, attemptID, "determ-key-3", empty, emptyResult); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deterministic batch with attempt: %v", err)
	}
	if _, _, err := f.store.SaveAnswerMatch(ctx, ownerActor(), f.round.ID, "", "determ-key-4", input, result); !errors.Is(err, ErrInvalid) {
		t.Fatalf("judged batch without attempt: %v", err)
	}
}

func TestAnswerMatchUnusableStaysUnresolved(t *testing.T) {
	f := setupAnswerMatch(t, "unusable")
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	first := answerMatchBatch(f.check, catalog, questions[:1], map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote},
	})
	runAnswerMatchBatch(t, f, "unusable-key", "unusable-attempt-0", first,
		map[string]string{questions[0].ID: remote.AnswerID})
	second := answerMatchBatch(f.check, catalog, questions[1:], map[string][]jev.AnswerMatchCandidate{
		questions[1].ID: {remote},
	})
	fake := &answerMatchFake{t: t, choices: map[string]string{"match_0": remote.AnswerID}}
	result, err := jev.MatchAnswers(context.Background(), fake, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.SaveAnswerMatch(context.Background(), ownerActor(), f.round.ID,
		"missing-attempt", "unusable-key", second, result); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unusable exchange: %v", err)
	}
	view, err := f.store.CurrentAnswerMatch(context.Background(), f.opportunity.ID)
	if err != nil || view.Status != AnswerMatchStatusPartial || len(view.Matches) != 1 {
		t.Fatalf("failed batch must persist nothing: %+v %v", view, err)
	}
}

func TestAnswerMatchReadGuards(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CurrentAnswerMatch(ctx, opportunity.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected read: %v", err)
	}
	selected := selectFixtureOpportunity(t, s, company.ID, "readguard-select")
	if _, err := s.CurrentAnswerMatch(ctx, selected.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no check read: %v", err)
	}
	started, _, err := s.StartJobCheck(ctx, ownerActor(), selected.ID,
		CheckStartInput{RequestKey: "readguard-check", ExpectedOpportunityRevision: selected.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_ = started
	if _, err := s.CurrentAnswerMatch(ctx, selected.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no run read: %v", err)
	}
}

func TestAnswerMatchAllNoneFitsReadsUnmatched(t *testing.T) {
	f := setupAnswerMatch(t, "unmatched")
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	input := answerMatchBatch(f.check, catalog, questions, map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote},
		questions[1].ID: {remote},
	})
	view, _ := runAnswerMatchBatch(t, f, "unmatched-key", "unmatched-attempt-0", input,
		map[string]string{questions[0].ID: jev.AnswerMatchNoneFits, questions[1].ID: jev.AnswerMatchNoneFits})
	if view.Status != AnswerMatchStatusUnmatched || len(view.Matches) != 2 {
		t.Fatalf("all none_fits: %+v", view)
	}
}

func TestAnswerMatchPendingCheckRejected(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "pending-select")
	started, _, err := s.StartJobCheck(ctx, owner, opportunity.ID,
		CheckStartInput{RequestKey: "pending-check", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	round, _ := startCheckRound(t, s, "pending-round", []string{RoundCodexTurn, RoundJevRequest}, opportunity.ID)
	answer, _, err := s.CreateSavedAnswer(ctx, owner, SavedAnswerCreateInput{
		RequestKey: "pending-answer", Text: "I work remotely from Example City.", ScopeTags: []string{"remote"}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.AnswerCatalogDigest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	version := answer.Versions[0]
	input := jev.AnswerMatchInput{CheckID: started.ID, QuestionSetSHA256: strings.Repeat("0", 64),
		AnswerCatalogDigest: catalog, MaxReportedTokens: 1000,
		Questions: []jev.AnswerMatchQuestion{{QuestionID: "q-pending", Text: "Are you willing to work remotely?",
			Required: "required", Kind: "free_text", TextSHA256: strings.Repeat("1", 64),
			Candidates: []jev.AnswerMatchCandidate{{AnswerID: answer.ID, AnswerVersion: version.Version,
				TextSHA256: version.TextSHA256, ScopeTags: answer.ScopeTags, Excerpt: version.Text}}}}}
	fake := &answerMatchFake{t: t, choices: map[string]string{"match_0": answer.ID}}
	result, err := jev.MatchAnswers(ctx, fake, input)
	if err != nil {
		t.Fatal(err)
	}
	raw := answerMatchRaw(t, input, map[string]string{"q-pending": answer.ID})
	attemptID := recordAnswerMatchAttempt(t, s, round, opportunity.ID, "pending-attempt", result.RequestSnapshot, raw)
	if _, _, err := s.SaveAnswerMatch(ctx, owner, round.ID, attemptID, "pending-key", input, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending check: %v", err)
	}
}

// TestAnswerMatchZeroLLMCalls proves the store match path cannot reach Codex:
// answermatch.go imports stdlib plus the Jev judgment package only, and the
// save entry takes a captured Jev exchange rather than any Codex handle.
func TestAnswerMatchZeroLLMCalls(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "answermatch.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(strings.ToLower(path), "codex") {
			t.Fatalf("match path must not import %s", path)
		}
		if strings.Contains(path, ".") && !strings.HasSuffix(path, "/internal/jev") {
			t.Fatalf("match path must not import %s", path)
		}
	}
	var _ func(context.Context, Actor, string, string, string, jev.AnswerMatchInput, jev.AnswerMatchResult) (AnswerMatchView, bool, error) = (*Store)(nil).SaveAnswerMatch
}
