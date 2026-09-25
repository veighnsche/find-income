package materialprep_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// fakeStandardRunner is the provider-disabled Standard fixture: it records
// inputs and replays scripted model texts. No live model, Jev, network,
// send, or secrets are touched.
type fakeStandardRunner struct {
	calls  int
	inputs []musecode.StandardInput
	fn     func(musecode.StandardInput) ([]string, error)
}

func (f *fakeStandardRunner) RunStandard(_ context.Context, input musecode.StandardInput) (materialprep.StandardResult, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	messages, err := f.fn(input)
	if err != nil {
		return materialprep.StandardResult{}, err
	}
	return materialprep.StandardResult{Messages: messages}, nil
}

func standardTestRequest() materialprep.DraftRequest {
	return materialprep.DraftRequest{
		OpportunityID: "opp-test", OpportunityTitle: "Go engineer", CompanyName: "Harbour",
		CheckID: "check-test",
		RequiredUnset: []materialprep.DraftQuestion{
			{ID: "q-motivation", Text: "Why do you want this role?"},
			{ID: "q-go", Text: "Describe a Go service you shipped."},
		},
		Answered: []materialprep.AnsweredFact{
			{QuestionID: "q-extra", Question: "Anything else?", Text: "I mentor juniors.", AnswerVersion: 1},
		},
		SavedAnswers: []materialprep.SavedAnswerFact{
			{AnswerID: "ans-1", Version: 2, Text: "I led platform work at Acme.",
				ScopeTags: []string{"leadership"}, ContextNote: "Staff-level scope"},
		},
		CareerSources: []applicationpacks.Source{
			{ID: "cv", Name: "CV", SHA256: strings.Repeat("a", 64), Approved: true,
				Body: "Six years of Go platform work. Led Acme migration."},
			{ID: "github", Name: "Evidence", SHA256: strings.Repeat("b", 64), Approved: true,
				Body: "Shipped queue worker handling 1M jobs daily."},
		},
		Profile: store.Preferences{Version: 3, PreferredLocation: "Amsterdam",
			AllowRemote: true, SalaryCurrency: "EUR", Timezone: "Europe/Amsterdam"},
	}
}

func TestStandardDrafterDraftsFromVerifiedFacts(t *testing.T) {
	fake := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) {
		return []string{`{"drafts":[
{"questionId":"q-motivation","lines":[
{"text":"I want this role for its Go platform work.","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work"}]}]},
{"questionId":"q-go","lines":[
{"text":"I shipped a queue worker.","citations":[{"sourceId":"github","excerpt":"Shipped queue worker handling 1M jobs daily"}]},
{"text":"It followed my platform experience.","citations":[{"sourceId":"cv","excerpt":"Led Acme migration"}]}]}]}`}, nil
	}}
	drafter := &materialprep.StandardDrafter{Runner: fake}
	drafts, err := drafter.DraftRequiredAnswers(context.Background(), standardTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 2 || drafts[0].QuestionID != "q-motivation" || len(drafts[1].Lines) != 2 {
		t.Fatalf("drafts: %+v", drafts)
	}
	if fake.calls != 1 {
		t.Fatalf("Standard calls: %d, want exactly 1", fake.calls)
	}
	input := fake.inputs[0]
	if input.Purpose != materialprep.StandardDraftPurpose {
		t.Fatalf("purpose: %q", input.Purpose)
	}
	if input.BundleRef != "check-test" {
		t.Fatalf("bundle: %q", input.BundleRef)
	}
	if len(input.Targets) != 2 || input.Targets[0] != "q-motivation" || input.Targets[1] != "q-go" {
		t.Fatalf("targets: %v", input.Targets)
	}
	prompt := input.Context["prompt"]
	for _, want := range []string{"Why do you want this role?", "Describe a Go service you shipped.",
		"I mentor juniors.", "I led platform work at Acme.", "Six years of Go platform work.",
		"Shipped queue worker handling 1M jobs daily.", "Amsterdam", "q-motivation", "q-go"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Standard input misses verified fact %q", want)
		}
	}
	if strings.Contains(prompt, "jobs@example.invalid") {
		t.Fatal("Standard input carries employer contact handles")
	}
}

func TestStandardDrafterOutputValidation(t *testing.T) {
	cases := map[string]struct {
		messages []string
		wantErr  bool
		want     int
	}{
		"out of scope":      {[]string{`{"drafts":[{"questionId":"q-else","lines":[{"text":"x","citations":[{"sourceId":"cv","excerpt":"Six years of Go platform work"}]}]}]}`}, true, 0},
		"inexact excerpt":   {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"Claim.","citations":[{"sourceId":"cv","excerpt":"ten years of Rust"}]}]}]}`}, true, 0},
		"malformed":         {[]string{`{"drafts":[`}, true, 0},
		"subset omits held": {[]string{`{"drafts":[{"questionId":"q-go","lines":[{"text":"Shipped it.","citations":[{"sourceId":"github","excerpt":"Shipped queue worker handling 1M jobs daily"}]}]}]}`}, false, 1},
		"empty drafts":      {[]string{`{"drafts":[]}`}, false, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) { return tc.messages, nil }}
			drafter := &materialprep.StandardDrafter{Runner: fake}
			drafts, err := drafter.DraftRequiredAnswers(context.Background(), standardTestRequest())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted: %+v", drafts)
				}
				if fake.calls != 1 {
					t.Fatalf("calls: %d, want 1 (no retry)", fake.calls)
				}
				return
			}
			if err != nil || len(drafts) != tc.want {
				t.Fatalf("drafts: %+v %v", drafts, err)
			}
		})
	}
}

func TestStandardDrafterRequestGuards(t *testing.T) {
	ctx := context.Background()
	var nilDrafter *materialprep.StandardDrafter
	if _, err := nilDrafter.DraftRequiredAnswers(ctx, standardTestRequest()); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("nil drafter: %v", err)
	}
	if _, err := (&materialprep.StandardDrafter{}).DraftRequiredAnswers(ctx, standardTestRequest()); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("nil runner: %v", err)
	}
	fake := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) { return []string{`{"drafts":[]}`}, nil }}
	drafter := &materialprep.StandardDrafter{Runner: fake}
	empty := standardTestRequest()
	empty.RequiredUnset = nil
	if _, err := drafter.DraftRequiredAnswers(ctx, empty); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty scope: %v", err)
	}
	dup := standardTestRequest()
	dup.RequiredUnset = append(dup.RequiredUnset, dup.RequiredUnset[0])
	if _, err := drafter.DraftRequiredAnswers(ctx, dup); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("duplicate scope: %v", err)
	}
	unapproved := standardTestRequest()
	unapproved.CareerSources[0].Approved = false
	if _, err := drafter.DraftRequiredAnswers(ctx, unapproved); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unapproved source: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("guard failures spent Standard turns: %d", fake.calls)
	}
	boom := errors.New("synthetic Standard failure")
	failing := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) { return nil, boom }}
	if _, err := (&materialprep.StandardDrafter{Runner: failing}).DraftRequiredAnswers(ctx, standardTestRequest()); !errors.Is(err, boom) {
		t.Fatalf("runner error: %v", err)
	}
	if failing.calls != 1 {
		t.Fatalf("error path calls: %d, want 1 (no retry)", failing.calls)
	}
}

func TestStandardInputOnlyNoDiscoveryRoute(t *testing.T) {
	// By construction the adapter's runner accepts ONLY StandardInput
	// (TierStandard, private-only). This test proves the captured inputs
	// carry verified facts only, with no discovery/checks/Answer routing.
	fake := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) { return []string{`{"drafts":[]}`}, nil }}
	if _, err := (&materialprep.StandardDrafter{Runner: fake}).DraftRequiredAnswers(context.Background(), standardTestRequest()); err != nil {
		t.Fatal(err)
	}
	input := fake.inputs[0]
	if input.Purpose != materialprep.StandardDraftPurpose {
		t.Fatalf("purpose: %q", input.Purpose)
	}
	// Context carries exactly one verified-fact prompt; no discovery,
	// check, or Answer keys exist.
	if len(input.Context) != 1 || input.Context["prompt"] == "" {
		t.Fatalf("context keys: %v", input.Context)
	}
	for _, forbidden := range []string{"criteria", "vacancy", "discovery", "check_route", "answer_match", "jev"} {
		for key, value := range input.Context {
			if strings.Contains(strings.ToLower(key), forbidden) || strings.Contains(value, "public_search") {
				t.Fatalf("Standard input carries %q route in %q", forbidden, key)
			}
		}
	}
	// The Standard adapter code must construct StandardInput only: no
	// PublicInput, Contributor tier, public tools, or research/answer
	// packages in non-comment code (comments may name them to forbid them).
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	standardPath := filepath.Join(filepath.Dir(file), "standard.go")
	raw, err := os.ReadFile(standardPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "StandardInput") || !strings.Contains(body, "StandardRunner") {
		t.Fatal("standard.go lacks the Standard adapter")
	}
	var code strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		code.WriteString(line + "\n")
	}
	codeBody := code.String()
	for _, forbidden := range []string{"PublicInput", "TierContributor", "ContributorTool", "public_search", "public_fetch", "publicresearch", "researchwire", "researchexecute", "researchmemory", "jevservice", "codexservice", "httpapi"} {
		if strings.Contains(codeBody, forbidden) {
			t.Fatalf("standard.go code references forbidden %q", forbidden)
		}
	}
}

func TestStandardPrepareGroundedBlankHeld(t *testing.T) {
	f := setupPrep(t, "std-prep")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	target := f.check.Questions[1]
	fake := &fakeStandardRunner{fn: func(input musecode.StandardInput) ([]string, error) {
		if input.Purpose != materialprep.StandardDraftPurpose {
			return nil, errors.New("wrong Standard purpose")
		}
		if len(input.Targets) != 1 || input.Targets[0] != target.ID {
			return nil, errors.New("wrong Standard targets")
		}
		return []string{`{"drafts":[{"questionId":"` + target.ID + `","lines":[{"text":"I shipped a billing service in Go last year.","citations":[{"sourceId":"` + careerEvidenceID + `","excerpt":"` + careerExcerptMain + `"}]}]}]}`}, nil
	}}
	f.svc.Draft = &materialprep.StandardDrafter{Runner: fake}
	checkID, questionSet, workflowRev := prepPins(t, f)
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"std-prep-key", checkID, questionSet, workflowRev)
	if err != nil || !created || view.Status != store.MaterialStatusPrepared {
		t.Fatalf("Standard prepare: %+v %v", view, err)
	}
	if fake.calls != 1 {
		t.Fatalf("Standard turns=%d, want exactly 1", fake.calls)
	}
	_, manifest := readCommittedManifest(t, f, view.Current.PackID)
	if manifest.Material.Answers[0].State != "answered" {
		t.Fatalf("Q0 state: %+v", manifest.Material.Answers[0])
	}
	if manifest.Material.Answers[1].State != "drafted" || manifest.Material.Answers[1].Text == "" {
		t.Fatalf("Q1 grounded draft: %+v", manifest.Material.Answers[1])
	}
	// Optional and required-unknown questions stay blank, never drafted.
	if manifest.Material.Answers[2].State != "blank" || manifest.Material.Answers[2].Text != "" {
		t.Fatalf("optional blank: %+v", manifest.Material.Answers[2])
	}
	if manifest.Material.Answers[3].State != "blank" || manifest.Material.Answers[3].Text != "" {
		t.Fatalf("unknown blank: %+v", manifest.Material.Answers[3])
	}
	if len(manifest.Draft.MaterialUnknowns) != 0 {
		t.Fatalf("unknowns: %v", manifest.Draft.MaterialUnknowns)
	}
}

func TestStandardPrepareHeldWhenOmits(t *testing.T) {
	f := setupPrep(t, "std-held")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	heldID := f.check.Questions[1].ID
	fake := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) { return []string{`{"drafts":[]}`}, nil }}
	f.svc.Draft = &materialprep.StandardDrafter{Runner: fake}
	checkID, questionSet, workflowRev := prepPins(t, f)
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"std-held-key", checkID, questionSet, workflowRev)
	if err != nil || !created || view.Status != store.MaterialStatusHeld {
		t.Fatalf("held prepare: %+v %v", view, err)
	}
	if fake.calls != 1 {
		t.Fatalf("Standard turns=%d, want 1", fake.calls)
	}
	if view.Current.Readiness.Ready || len(view.Current.Readiness.Held) != 1 || view.Current.Readiness.Held[0] != heldID {
		t.Fatalf("held readiness: %+v", view.Current.Readiness)
	}
	_, manifest := readCommittedManifest(t, f, view.Current.PackID)
	if len(manifest.Draft.MaterialUnknowns) != 1 {
		t.Fatalf("held unknowns: %v", manifest.Draft.MaterialUnknowns)
	}
	if manifest.Material.Answers[1].State != "held" || manifest.Material.Answers[1].Text != "" {
		t.Fatalf("held entry: %+v", manifest.Material.Answers[1])
	}
}

func TestStandardEditZeroModelCalls(t *testing.T) {
	f := setupPrep(t, "std-edit")
	ctx := context.Background()
	prepareHeldV1(t, f, "std-edit-prep")
	fake := &fakeStandardRunner{fn: func(musecode.StandardInput) ([]string, error) { return []string{`{"drafts":[]}`}, nil }}
	f.svc.Draft = &materialprep.StandardDrafter{Runner: fake}
	f.svc.Relevance = nil
	text := "Exact owner edit — byte-identical, no model."
	view, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "std-edit-1", 1, text)
	if err != nil || !created {
		t.Fatalf("edit: created=%v err=%v", created, err)
	}
	if fake.calls != 0 {
		t.Fatalf("edit spent Standard turns: %d", fake.calls)
	}
	pack, err := f.db.ApplicationPack(ctx, view.PackID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pack.ManifestJSON), text) {
		t.Fatal("edit text not byte-exact in manifest")
	}
}

func TestStandardRewriteNewVersionInvalidatesApproval(t *testing.T) {
	ctx := context.Background()
	f, _ := setupRewrite(t, "std-rewrite-approval")
	v1, err := f.db.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := f.db.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.AdvanceRoleWorkflow(ctx, f.opportunity.ID, workflow.Revision, store.RoleStageReviewing, ""); err != nil {
		t.Fatal(err)
	}
	view, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "std-rewrite-approval-1", 1, "Tighten.")
	if err != nil || !created {
		t.Fatalf("rewrite: created=%v err=%v", created, err)
	}
	if view.Version != 2 || view.Provenance.Origin != store.MaterialOriginRewrite {
		t.Fatalf("rewrite version: %+v", view.Provenance)
	}
	if view.Provenance.RewriteOf == nil || *view.Provenance.RewriteOf != 1 {
		t.Fatalf("rewrite linkage: %+v", view.Provenance)
	}
	if view.PackID == v1.PackID {
		t.Fatal("rewrite reused the prior pack id; prior approval would still bind")
	}
	after, err := f.db.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Stage != store.RoleStagePrepared {
		t.Fatalf("rewrite stage: %q, want prepared (fresh review required)", after.Stage)
	}
	// Prior version bytes are immutable.
	again, err := f.db.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil || again.PackID != v1.PackID {
		t.Fatalf("v1 changed: %+v %v", again, err)
	}
}
