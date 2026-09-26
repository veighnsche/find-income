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

func answerDraftTestRequest() materialprep.AnswerDraftRequest {
	return materialprep.AnswerDraftRequest{
		OpportunityID: "opp-test", OpportunityTitle: "Go engineer", CompanyName: "Harbour",
		CheckID:     "check-test",
		Description: "Build Go services for the scheduling platform.",
		Requirements: []materialprep.ArtifactRequirement{
			{Statement: "Weekend availability is required.", Excerpt: "Weekend availability is required for this rota."},
		},
		Questions: []materialprep.DraftQuestion{
			{ID: "q-motivation", Text: "Why do you want this role?"},
			{ID: "q-shipped", Text: "Describe a Go service you shipped."},
		},
		CareerSources: []applicationpacks.Source{
			{ID: "cv", Name: "CV", SHA256: strings.Repeat("a", 64), Approved: true,
				Body: "Six years of Go platform work in Amsterdam."},
		},
	}
}

func TestDraftRequestedAnswersGrounds(t *testing.T) {
	ctx := context.Background()
	runner := &fakeStandardRunner{fn: func(input musecode.StandardInput) ([]string, error) {
		return []string{`{"drafts":[
		  {"questionId":"q-motivation","lines":[
		    {"text":"Six years of Go platform work.","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work in Amsterdam."}]}]},
		  {"questionId":"q-shipped","lines":[
		    {"text":"Go services for the scheduling platform.","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work in Amsterdam."}]}]}
		]}`}, nil
	}}
	outcome, err := (&materialprep.StandardDrafter{Runner: runner}).DraftRequestedAnswers(ctx, answerDraftTestRequest())
	if err != nil || len(outcome.Drafts) != 2 || len(outcome.Held) != 0 || runner.calls != 1 {
		t.Fatalf("drafts: %+v calls=%d err=%v", outcome, runner.calls, err)
	}
	input := runner.inputs[0]
	if input.Purpose != materialprep.StandardDraftPurpose || input.BundleRef != "check-test" || len(input.Targets) != 2 {
		t.Fatalf("input: %+v", input)
	}
	for _, want := range []string{"q-motivation", "Why do you want this role?", "Six years of Go platform work."} {
		if !strings.Contains(input.Context["prompt"], want) && want != "Six years of Go platform work." {
			t.Fatalf("prompt misses %q", want)
		}
	}
	if outcome.Drafts[0].QuestionID != "q-motivation" || outcome.Drafts[0].Text != "Six years of Go platform work." {
		t.Fatalf("motivation draft: %+v", outcome.Drafts[0])
	}
}

func TestDraftRequestedAnswersRejectsUngrounded(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{
		"out of scope":   `{"drafts":[{"questionId":"q-nope","lines":[{"text":"x","citations":[{"sourceId":"cv","excerpt":"y"}]}]}]}`,
		"duplicate":      `{"drafts":[{"questionId":"q-motivation","lines":[{"text":"x","citations":[{"sourceId":"cv","excerpt":"y"}]}]},{"questionId":"q-motivation","lines":[{"text":"z","citations":[{"sourceId":"cv","excerpt":"y"}]}]}]}`,
		"malformed":      `{"drafts":[}`,
		"trailing":       `{"drafts":[]} trailing`,
		"smuggled field": `{"drafts":[{"questionId":"q-motivation","lines":[],"extra":1}]}`,
	}
	for name, message := range cases {
		runner := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) {
			return []string{message}, nil
		}}
		if _, err := (&materialprep.StandardDrafter{Runner: runner}).DraftRequestedAnswers(ctx, answerDraftTestRequest()); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := (&materialprep.StandardDrafter{}).DraftRequestedAnswers(ctx, answerDraftTestRequest()); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("nil runner: %v", err)
	}
	empty := answerDraftTestRequest()
	empty.Questions = nil
	runner := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) { return []string{`{"drafts":[]}`}, nil }}
	if _, err := (&materialprep.StandardDrafter{Runner: runner}).DraftRequestedAnswers(ctx, empty); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty scope: %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("empty scope spent a turn")
	}
}

func TestDraftRequestedAnswersHoldsUnsupported(t *testing.T) {
	ctx := context.Background()
	newRunner := func(message string) *fakeStandardRunner {
		return &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) {
			return []string{message}, nil
		}}
	}
	// Every failure below holds the question instead of inventing text:
	// unknown source, empty excerpt, uncited lines, invented claims, and
	// questions the turn omits.
	outcome, err := (&materialprep.StandardDrafter{Runner: newRunner(`{"drafts":[
	  {"questionId":"q-motivation","lines":[{"text":"Six years of Go.","citations":[{"sourceId":"nope","excerpt":"x"}]}]},
	  {"questionId":"q-shipped","lines":[{"text":"Fluent in underwater basket weaving.","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work in Amsterdam."}]}]}
	]}`)}).DraftRequestedAnswers(ctx, answerDraftTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Drafts) != 0 || len(outcome.Held) != 2 {
		t.Fatalf("holds: %+v", outcome)
	}
	if outcome.Held[1].MissingFact == "" {
		t.Fatalf("invented claim held without naming the gap: %+v", outcome.Held[1])
	}
	omitted, err := (&materialprep.StandardDrafter{Runner: newRunner(`{"drafts":[
	  {"questionId":"q-motivation","lines":[{"text":"Six years of Go platform work.","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work in Amsterdam."}]}]}
	]}`)}).DraftRequestedAnswers(ctx, answerDraftTestRequest())
	if err != nil || len(omitted.Drafts) != 1 || len(omitted.Held) != 1 || omitted.Held[0].QuestionID != "q-shipped" {
		t.Fatalf("omitted: %+v err=%v", omitted, err)
	}
}

type stubAnswerDrafter struct {
	materialprep.ArtifactDrafter
	calls   int
	outcome materialprep.RequestedAnswerOutcome
}

func (f *stubAnswerDrafter) DraftRequestedAnswers(_ context.Context, _ materialprep.AnswerDraftRequest) (materialprep.RequestedAnswerOutcome, error) {
	f.calls++
	return f.outcome, nil
}

func (f *stubAnswerDrafter) DraftArtifacts(_ context.Context, _ materialprep.ArtifactDraftRequest) (materialprep.DraftOutcome, error) {
	return materialprep.DraftOutcome{Drafts: []materialprep.ArtifactDraft{}, Held: []materialprep.ArtifactHold{}}, nil
}

func TestDraftOpportunityArtifactsFillsRequestedDrafts(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "requested-drafts")
	flagged := f.check.Questions[0].ID
	otherRequired := f.check.Questions[1].ID
	if _, err := f.db.SaveAnswerValue(ctx, testOwner(), f.opportunity.ID, flagged,
		store.AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: "", DraftRequested: true}); err != nil {
		t.Fatal(err)
	}
	drafter := &stubAnswerDrafter{outcome: materialprep.RequestedAnswerOutcome{
		Drafts: []materialprep.RequestedAnswerDraft{{QuestionID: flagged, Text: "Scheduling platforms, six years."}},
	}}
	f.svc.Artifacts = drafter
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"draft-req-1", checkID, questionSet, workflowRev); err != nil {
		t.Fatal(err)
	}
	if drafter.calls != 1 {
		t.Fatalf("answer turns = %d, want 1", drafter.calls)
	}
	answers, err := f.db.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	var filled, other *store.QuestionAnswerValue
	for i, value := range answers.Values {
		if value.QuestionID == flagged {
			filled = &answers.Values[i]
		}
		if value.QuestionID == otherRequired {
			other = &answers.Values[i]
		}
	}
	if filled == nil || filled.Text != "Scheduling platforms, six years." ||
		filled.State != store.AnswerValueStateAnswered || filled.DraftRequested ||
		filled.Provenance.Origin != store.AnswerValueOriginStandardDraft {
		t.Fatalf("filled: %+v", filled)
	}
	if other != nil {
		t.Fatalf("unflagged required must stay untouched: %+v", other)
	}
}

func TestDraftOpportunityArtifactsSkipsWithoutFlags(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "requested-drafts-none")
	drafter := &stubAnswerDrafter{outcome: materialprep.RequestedAnswerOutcome{}}
	f.svc.Artifacts = drafter
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"draft-req-2", checkID, questionSet, workflowRev); err != nil {
		t.Fatal(err)
	}
	if drafter.calls != 0 {
		t.Fatalf("answer turns = %d, want none without flags", drafter.calls)
	}
}
