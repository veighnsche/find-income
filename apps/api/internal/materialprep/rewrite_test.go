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

func rewriteTestRequest() materialprep.ArtifactRewriteRequest {
	return materialprep.ArtifactRewriteRequest{
		OpportunityID: "opp-test", OpportunityTitle: "Go engineer", CompanyName: "Harbour",
		CheckID: "check-test", RouteKind: "direct", RouteJudgment: "application_route",
		RouteDestination: "jobs@example.invalid",
		Documents:        []materialprep.ArtifactDocument{{Label: "CV", Required: true}},
		Items: []materialprep.RewriteItem{
			{Type: "cv", ExpectedVersion: 1, CurrentContent: "Go engineer, six years."},
		},
		Instruction: "Tighten the wording.",
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

func TestRewriteArtifactsGroundsAndFences(t *testing.T) {
	ctx := context.Background()
	runner := &fakeStandardRunner{fn: func(input musecode.StandardInput) ([]string, error) {
		return []string{`{"artifacts":[
		  {"type":"cv","content":"Go platform engineer, six years of Go platform work.","facts":["cv"],"answers":[]}
		]}`}, nil
	}}
	outcome, err := (&materialprep.StandardDrafter{Runner: runner}).RewriteArtifacts(ctx, rewriteTestRequest())
	if err != nil || len(outcome.Drafts) != 1 || len(outcome.Held) != 0 || runner.calls != 1 {
		t.Fatalf("outcome: %+v calls=%d err=%v", outcome, runner.calls, err)
	}
	input := runner.inputs[0]
	if input.Purpose != materialprep.StandardRewritePurpose || input.BundleRef != "check-test" ||
		len(input.Targets) != 1 || input.Targets[0] != "cv" {
		t.Fatalf("input: %+v", input)
	}
	prompt := input.Context["prompt"]
	for _, want := range []string{"Go engineer, six years.", "Tighten the wording.", "Six years of Go platform work."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt misses %q", want)
		}
	}
	// The owner instruction is trusted turn input: its own words ground.
	runner = &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) {
		return []string{`{"artifacts":[
		  {"type":"cv","content":"Go platform engineer who mentors juniors.","facts":["cv"],"answers":[]}
		]}`}, nil
	}}
	request := rewriteTestRequest()
	request.Instruction = "Mention that I mentor juniors."
	outcome, err = (&materialprep.StandardDrafter{Runner: runner}).RewriteArtifacts(ctx, request)
	if err != nil || len(outcome.Drafts) != 1 {
		t.Fatalf("instruction-grounded: %+v err=%v", outcome, err)
	}
	// Without the instruction the same sentence holds: the cited
	// career body never mentions mentoring.
	request.Instruction = ""
	outcome, err = (&materialprep.StandardDrafter{Runner: runner}).RewriteArtifacts(ctx, request)
	if err != nil || len(outcome.Drafts) != 0 || len(outcome.Held) != 1 {
		t.Fatalf("unmentored: %+v err=%v", outcome, err)
	}
	// Out-of-scope rewrites fail the turn.
	runner = &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) {
		return []string{`{"artifacts":[{"type":"email_body","content":"x","facts":[],"answers":[]}]}`}, nil
	}}
	if _, err := (&materialprep.StandardDrafter{Runner: runner}).RewriteArtifacts(ctx, rewriteTestRequest()); err == nil {
		t.Fatal("out-of-scope rewrite accepted")
	}
	empty := rewriteTestRequest()
	empty.Items = nil
	if _, err := (&materialprep.StandardDrafter{Runner: runner}).RewriteArtifacts(ctx, empty); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty scope: %v", err)
	}
}

type stubArtifactRewriter struct {
	calls    int
	requests []materialprep.ArtifactRewriteRequest
	fn       func(materialprep.ArtifactRewriteRequest) (materialprep.DraftOutcome, error)
}

func (f *stubArtifactRewriter) RewriteArtifacts(_ context.Context, req materialprep.ArtifactRewriteRequest) (materialprep.DraftOutcome, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.fn == nil {
		return materialprep.DraftOutcome{}, nil
	}
	return f.fn(req)
}

func seedRewriteCV(t *testing.T, f prepFixture) {
	t.Helper()
	ctx := context.Background()
	checkID, questionSet, workflowRev := prepPins(t, f)
	f.svc.Artifacts = &stubArtifactDrafter{fn: func(materialprep.ArtifactDraftRequest) (materialprep.DraftOutcome, error) {
		return materialprep.DraftOutcome{Drafts: []materialprep.ArtifactDraft{{Type: "cv",
			Content: "Go engineer, six years.", FactIDs: []string{careerEvidenceID}}}}, nil
	}}
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"rewrite-seed", checkID, questionSet, workflowRev); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteOpportunityArtifactsVersions(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "rewrite-op")
	seedRewriteCV(t, f)
	rewrite := &stubArtifactRewriter{fn: func(req materialprep.ArtifactRewriteRequest) (materialprep.DraftOutcome, error) {
		if len(req.Items) != 1 || req.Items[0].Type != "cv" || req.Instruction != "Tighten." {
			t.Fatalf("request: %+v", req)
		}
		// First call rewrites v1; the same-key replay recovers
		// against the committed v2 current text.
		if content := req.Items[0].CurrentContent; content != "Go engineer, six years." &&
			content != "Go engineer; six years." {
			t.Fatalf("current content: %q", content)
		}
		return materialprep.DraftOutcome{Drafts: []materialprep.ArtifactDraft{{Type: "cv",
			Content: "Go engineer; six years.", FactIDs: []string{careerEvidenceID}}}}, nil
	}}
	f.svc.Rewrites = rewrite
	set, created, err := f.svc.RewriteOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"rewrite-1", materialprep.RewriteInput{
			Items:       []materialprep.RewriteItem{{Type: "cv", ExpectedVersion: 1}},
			Instruction: "Tighten."})
	if err != nil || !created || rewrite.calls != 1 {
		t.Fatalf("rewrite: created=%v calls=%d err=%v", created, rewrite.calls, err)
	}
	cv := artifactEntry(set, store.ArtifactCV)
	if cv.State != store.ArtifactStateReady || cv.Current == nil || cv.Current.Version != 2 ||
		cv.Current.Content != "Go engineer; six years." {
		t.Fatalf("cv: %+v", cv)
	}
	if cv.Current.Basis.CheckID == "" || len(cv.Current.Basis.FactSHA256) != 1 {
		t.Fatalf("rewrite basis pins: %+v", cv.Current.Basis)
	}
	if entry := artifactEntry(set, store.ArtifactEmailSubject); entry.State != store.ArtifactStateHeld {
		t.Fatalf("unrelated item touched: %+v", entry)
	}
	// Same-key replay recovers the accepted rewrite without a new turn.
	set, created, err = f.svc.RewriteOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"rewrite-1", materialprep.RewriteInput{
			Items:       []materialprep.RewriteItem{{Type: "cv", ExpectedVersion: 1}},
			Instruction: "Tighten."})
	if err != nil || created {
		t.Fatalf("replay: created=%v err=%v", created, err)
	}
	if entry := artifactEntry(set, store.ArtifactCV); entry.Current.Version != 2 {
		t.Fatalf("replay version: %+v", entry)
	}
	// A fenced write against the moved version conflicts.
	if _, _, err := f.svc.RewriteOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"rewrite-2", materialprep.RewriteInput{
			Items: []materialprep.RewriteItem{{Type: "cv", ExpectedVersion: 1}}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale fence: %v", err)
	}
}

func TestRewriteOpportunityArtifactsGuards(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "rewrite-guard")
	seedRewriteCV(t, f)
	f.svc.Rewrites = &stubArtifactRewriter{}
	rewrite := func(key string, input materialprep.RewriteInput) error {
		_, _, err := f.svc.RewriteOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID, key, input)
		return err
	}
	if err := rewrite("ok", materialprep.RewriteInput{
		Items: []materialprep.RewriteItem{{Type: "cv", ExpectedVersion: 1}}}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := rewrite("unstored", materialprep.RewriteInput{
		Items: []materialprep.RewriteItem{{Type: "email_body", ExpectedVersion: 1}}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("unstored item: %v", err)
	}
	if err := rewrite("badtype", materialprep.RewriteInput{
		Items: []materialprep.RewriteItem{{Type: "form_values", ExpectedVersion: 1}}}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("form values: %v", err)
	}
	if err := rewrite("badkey ", materialprep.RewriteInput{
		Items: []materialprep.RewriteItem{{Type: "cv", ExpectedVersion: 2}}}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("request key: %v", err)
	}
	f.svc.Rewrites = nil
	if err := rewrite("unavail", materialprep.RewriteInput{
		Items: []materialprep.RewriteItem{{Type: "cv", ExpectedVersion: 2}}}); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("nil rewrites: %v", err)
	}
}

func TestRewriteOpportunityArtifactsRefusesStale(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "rewrite-stale")
	questionID := f.check.Questions[0].ID
	if _, err := f.db.SaveAnswerValue(ctx, testOwner(), f.opportunity.ID, questionID,
		store.AnswerValueSaveInput{Text: "I like Go."}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.db.SaveOpportunityArtifact(ctx, testOwner(), f.opportunity.ID, store.ArtifactSaveInput{
		RequestKey: "stale-cv", Type: store.ArtifactCV, Content: "cv v1",
		Basis: store.ArtifactBasis{AnswerRefs: []store.ArtifactAnswerRef{{QuestionID: questionID, AnswerVersion: 1}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SaveAnswerValue(ctx, testOwner(), f.opportunity.ID, questionID,
		store.AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: "I love Go."}); err != nil {
		t.Fatal(err)
	}
	stub := &stubArtifactRewriter{}
	f.svc.Rewrites = stub
	if _, _, err := f.svc.RewriteOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"rewrite-stale", materialprep.RewriteInput{
			Items: []materialprep.RewriteItem{{Type: "cv", ExpectedVersion: 1}}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale rewrite: %v", err)
	}
	if stub.calls != 0 {
		t.Fatalf("stale rewrite spent %d turns", stub.calls)
	}
}
