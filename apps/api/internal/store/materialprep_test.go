package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

type materialFixture struct {
	store       *Store
	opportunity Opportunity
	check       CheckView
}

func materialCheckSave(opportunityID, checkID, captureID string) CheckSaveInput {
	questions := []CheckQuestionInput{
		{Text: "Why do you want this role?", Required: CheckRequired, Kind: CheckQuestionFreeText,
			SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 150, End: 180},
			SourceExcerpt: "Why do you want this role?"},
		{Text: "Describe a Go service you shipped.", Required: CheckRequired, Kind: CheckQuestionFreeText,
			SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 400, End: 436},
			SourceExcerpt: "Describe a Go service you shipped."},
		{Text: "Anything else to share?", Required: CheckOptional, Kind: CheckQuestionFreeText,
			SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 500, End: 525},
			SourceExcerpt: "Anything else to share?"},
		{Text: "Are you willing to work hybrid?", Required: CheckRequiredUnknown, Kind: CheckQuestionFreeText,
			SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 600, End: 635},
			SourceExcerpt: "Are you willing to work hybrid?"},
	}
	return CheckSaveInput{
		OpportunityID: opportunityID, CheckID: checkID,
		Vacancy: CheckVacancyInput{CaptureIDs: []string{captureID},
			Completeness: CaptureComplete, SourceURL: "https://harbour.example/jobs/1",
			RetrievedAt: "2026-09-24T11:05:00Z"},
		RequestedDocuments: []RequestedDocumentInput{{Label: "CV", Required: true,
			SourceExcerpt: "Send your CV to jobs@example.invalid",
			SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 214, End: 260}}},
		Route: CheckRouteInput{Kind: CheckRouteDirect, DestinationText: "jobs@example.invalid",
			Judgment: CheckRouteJudgmentApplication, SourceExcerpt: "Send your CV to jobs@example.invalid",
			ObservedAt: "2026-09-24T11:30:00Z"},
		Gaps: []CheckGapInput{{Description: "Weekly hours are not stated in the vacancy text.",
			Consequential: true, Kind: CheckGapMissingFact}},
		Questions: questions,
		Activity: []CheckActivityInput{
			{Kind: "vacancy.opened", Outcome: string(researchcontract.OutcomeOK), CaptureID: captureID},
			{Kind: "questions.extracted", Payload: json.RawMessage(`{"count":4}`)},
		},
	}
}

func setupMaterialAnswered(t *testing.T, key string) materialFixture {
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
		materialCheckSave(opportunity.ID, started.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	status, err := s.CurrentJobCheck(ctx, opportunity.ID)
	if err != nil || status.Status != CheckStatusChecked || len(status.Check.Questions) != 4 {
		t.Fatalf("fixture check: %+v %v", status, err)
	}
	workflow, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil || workflow.Stage != RoleStageChecked {
		t.Fatalf("fixture workflow: %+v %v", workflow, err)
	}
	workflow, err = s.AdvanceRoleWorkflow(ctx, opportunity.ID, workflow.Revision, RoleStageAnswering, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceRoleWorkflow(ctx, opportunity.ID, workflow.Revision, RoleStageAnswered, ""); err != nil {
		t.Fatal(err)
	}
	return materialFixture{store: s, opportunity: opportunity, check: *status.Check}
}

func materialPackFor(t *testing.T, f materialFixture, mutate func(map[string]any)) ApplicationPackMutationInput {
	t.Helper()
	ctx := context.Background()
	workflow, err := f.store.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := f.store.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answers := make([]map[string]any, 0, len(f.check.Questions))
	for _, question := range f.check.Questions {
		answers = append(answers, map[string]any{"questionId": question.ID})
	}
	manifest := map[string]any{
		"role": map[string]any{"opportunityId": f.opportunity.ID,
			"opportunityRevision": workflow.OpportunityRev, "profileRevision": preferences.Version},
		"material": map[string]any{"checkId": f.check.ID,
			"questionSetSha256": f.check.QuestionSetSHA256,
			"origin":            MaterialOriginPrepared, "answers": answers},
	}
	if mutate != nil {
		mutate(manifest)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	typst := []byte(`= Application`)
	pdf := append([]byte("%PDF-1.4 fixture\n"), bytes.Repeat([]byte("0"), 200)...)
	return ApplicationPackMutationInput{OpportunityID: f.opportunity.ID,
		ExpectedOpportunityRevision: workflow.OpportunityRev, ExpectedProfileRevision: preferences.Version,
		ManifestJSON: manifestJSON, TypstSource: typst, PDF: pdf,
		ContentSHA256: applicationPackContentHash(manifestJSON, typst, pdf)}
}

func materialPrepareInput(t *testing.T, f materialFixture, key string) MaterialPrepareInput {
	t.Helper()
	workflow, err := f.store.RoleWorkflow(context.Background(), f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	return MaterialPrepareInput{RequestKey: key, ExpectedCheckID: f.check.ID,
		ExpectedQuestionSetSHA256: f.check.QuestionSetSHA256,
		ExpectedWorkflowRevision:  workflow.Revision,
		Pack:                      materialPackFor(t, f, nil)}
}

func saveFixtureAnswer(t *testing.T, f materialFixture, ordinal int, text string) QuestionAnswerValue {
	t.Helper()
	value, err := f.store.SaveAnswerValue(context.Background(), ownerActor(), f.opportunity.ID,
		f.check.Questions[ordinal].ID, AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestMaterialPrepareAnsweredRole(t *testing.T) {
	f := setupMaterialAnswered(t, "prep-ok")
	ctx := context.Background()
	first := saveFixtureAnswer(t, f, 0, "I want this role for its Go platform work.")
	second := saveFixtureAnswer(t, f, 1, "I shipped a billing service in Go.")
	view, created, err := f.store.PrepareOpportunityMaterials(ctx, ownerActor(), f.opportunity.ID,
		materialPrepareInput(t, f, "prep-ok-key"))
	if err != nil || !created {
		t.Fatalf("prepare: %+v %v", view, err)
	}
	if view.Status != MaterialStatusPrepared || view.Current == nil || view.Current.Version != 1 {
		t.Fatalf("prepared status: %+v", view)
	}
	current := view.Current
	if current.PackID == "" || current.OpportunityID != f.opportunity.ID || current.CheckID != f.check.ID ||
		current.QuestionSetSHA256 != f.check.QuestionSetSHA256 || current.CreatedAt == "" ||
		current.CreatedBy != ownerActor() || current.Provenance.Origin != MaterialOriginPrepared ||
		current.Provenance.RewriteOf != nil || len(current.Provenance.SourceShas) == 0 {
		t.Fatalf("version pins: %+v", current)
	}
	if !current.Readiness.Ready || len(current.Readiness.MissingRequired) != 0 || len(current.Readiness.Held) != 0 {
		t.Fatalf("readiness: %+v", current.Readiness)
	}
	if len(current.Answers) != 4 {
		t.Fatalf("answer coverage: %+v", current.Answers)
	}
	for i, ref := range current.Answers {
		if ref.QuestionID != f.check.Questions[i].ID ||
			ref.QuestionTextSHA256 != f.check.Questions[i].TextSHA256 {
			t.Fatalf("ref order/pins at %d: %+v", i, ref)
		}
	}
	if current.Answers[0].AnswerVersion != first.Version || current.Answers[0].TextSHA256 != first.TextSHA256 ||
		current.Answers[1].AnswerVersion != second.Version || current.Answers[1].TextSHA256 != second.TextSHA256 {
		t.Fatalf("carried refs: %+v", current.Answers)
	}
	if current.Answers[2].AnswerVersion != 0 || current.Answers[2].TextSHA256 != materialEmptyTextSHA ||
		current.Answers[3].AnswerVersion != 0 || current.Answers[3].TextSHA256 != materialEmptyTextSHA {
		t.Fatalf("optional/unknown refs stay blank: %+v", current.Answers)
	}
	workflow, err := f.store.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil || workflow.Stage != RoleStagePrepared {
		t.Fatalf("workflow after prepare: %+v %v", workflow, err)
	}
	pack, err := f.store.ApplicationPack(ctx, current.PackID)
	if err != nil || pack.OpportunityID != f.opportunity.ID {
		t.Fatalf("linked pack: %+v %v", pack, err)
	}
	read, err := f.store.CurrentOpportunityMaterials(ctx, f.opportunity.ID)
	if err != nil || read.Status != MaterialStatusPrepared || read.Current.Version != 1 {
		t.Fatalf("current re-read: %+v %v", read, err)
	}
	one, err := f.store.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil || one.PackID != current.PackID {
		t.Fatalf("version read: %+v %v", one, err)
	}
	if _, err := f.store.OpportunityMaterialVersion(ctx, f.opportunity.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}
}

func TestMaterialPrepareHeldWhenRequiredUnset(t *testing.T) {
	f := setupMaterialAnswered(t, "prep-held")
	ctx := context.Background()
	saveFixtureAnswer(t, f, 0, "I want this role for its Go platform work.")
	heldID := f.check.Questions[1].ID
	view, created, err := f.store.PrepareOpportunityMaterials(ctx, ownerActor(), f.opportunity.ID,
		materialPrepareInput(t, f, "prep-held-key"))
	if err != nil || !created {
		t.Fatalf("prepare: %+v %v", view, err)
	}
	if view.Status != MaterialStatusHeld || view.Current.Readiness.Ready {
		t.Fatalf("held status: %+v", view)
	}
	readiness := view.Current.Readiness
	if len(readiness.MissingRequired) != 1 || readiness.MissingRequired[0] != heldID ||
		len(readiness.Held) != 1 || readiness.Held[0] != heldID {
		t.Fatalf("held readiness: %+v", readiness)
	}
	if ref := view.Current.Answers[1]; ref.AnswerVersion != 0 || ref.TextSHA256 != materialEmptyTextSHA {
		t.Fatalf("held ref: %+v", ref)
	}
	answers, err := f.store.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil || len(answers.Values) != 1 {
		t.Fatalf("prepare wrote no owner answers: %+v %v", answers, err)
	}
}

func TestMaterialPrepareDraftsRequiredOnly(t *testing.T) {
	f := setupMaterialAnswered(t, "prep-draft")
	ctx := context.Background()
	saveFixtureAnswer(t, f, 0, "I want this role for its Go platform work.")
	input := materialPrepareInput(t, f, "prep-draft-key")
	draftText := "I shipped a billing service in Go last year."
	input.Drafts = []MaterialDraft{{QuestionID: f.check.Questions[1].ID,
		Text: draftText, TextSHA256: materialTextSHA(draftText)}}
	view, created, err := f.store.PrepareOpportunityMaterials(ctx, ownerActor(), f.opportunity.ID, input)
	if err != nil || !created || view.Status != MaterialStatusPrepared {
		t.Fatalf("drafted prepare: %+v %v", view, err)
	}
	ref := view.Current.Answers[1]
	if ref.AnswerVersion != 0 || ref.TextSHA256 != materialTextSHA(draftText) {
		t.Fatalf("drafted ref: %+v", ref)
	}
	answers, err := f.store.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil || len(answers.Values) != 1 {
		t.Fatalf("drafts never enter owner answer state: %+v %v", answers, err)
	}
}

func TestMaterialPrepareDraftGuards(t *testing.T) {
	f := setupMaterialAnswered(t, "prep-guard")
	saveFixtureAnswer(t, f, 0, "I want this role for its Go platform work.")
	draft := func(id, text string) MaterialDraft {
		return MaterialDraft{QuestionID: id, Text: text, TextSHA256: materialTextSHA(text)}
	}
	cases := map[string]func(*MaterialPrepareInput){
		"answered": func(input *MaterialPrepareInput) {
			input.Drafts = []MaterialDraft{draft(f.check.Questions[0].ID, "Override attempt.")}
		},
		"optional": func(input *MaterialPrepareInput) {
			input.Drafts = []MaterialDraft{draft(f.check.Questions[2].ID, "Optional draft.")}
		},
		"unknown": func(input *MaterialPrepareInput) {
			input.Drafts = []MaterialDraft{draft(f.check.Questions[3].ID, "Unknown draft.")}
		},
		"missing question": func(input *MaterialPrepareInput) {
			input.Drafts = []MaterialDraft{draft("no-such-question", "Nowhere draft.")}
		},
		"duplicate": func(input *MaterialPrepareInput) {
			one := draft(f.check.Questions[1].ID, "First.")
			input.Drafts = []MaterialDraft{one, one}
		},
		"sha mismatch": func(input *MaterialPrepareInput) {
			bad := draft(f.check.Questions[1].ID, "Mismatch.")
			bad.TextSHA256 = materialTextSHA("Other text.")
			input.Drafts = []MaterialDraft{bad}
		},
	}
	for name, mutate := range cases {
		input := materialPrepareInput(t, f, "prep-guard-"+name)
		mutate(&input)
		if _, _, err := f.store.PrepareOpportunityMaterials(context.Background(), ownerActor(),
			f.opportunity.ID, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	blanked := saveFixtureAnswer(t, f, 1, "")
	if blanked.State != AnswerValueStateBlank {
		t.Fatalf("blank fixture: %+v", blanked)
	}
	input := materialPrepareInput(t, f, "prep-guard-blank")
	input.Drafts = []MaterialDraft{draft(f.check.Questions[1].ID, "Blank override.")}
	if _, _, err := f.store.PrepareOpportunityMaterials(context.Background(), ownerActor(),
		f.opportunity.ID, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank wins over drafting: %v", err)
	}
	input = materialPrepareInput(t, f, "prep-guard-blank-ok")
	view, _, err := f.store.PrepareOpportunityMaterials(context.Background(), ownerActor(), f.opportunity.ID, input)
	if err != nil || view.Status != MaterialStatusHeld {
		t.Fatalf("blank required prepare: %+v %v", view, err)
	}
	if len(view.Current.Readiness.MissingRequired) != 1 || len(view.Current.Readiness.Held) != 0 {
		t.Fatalf("blank is missing, not held: %+v", view.Current.Readiness)
	}
}

func TestMaterialPreparePinFences(t *testing.T) {
	f := setupMaterialAnswered(t, "prep-fence")
	ctx := context.Background()
	cases := map[string]func(*MaterialPrepareInput){
		"check id":      func(input *MaterialPrepareInput) { input.ExpectedCheckID = "other-check" },
		"question set":  func(input *MaterialPrepareInput) { input.ExpectedQuestionSetSHA256 = strings.Repeat("0", 64) },
		"workflow rev":  func(input *MaterialPrepareInput) { input.ExpectedWorkflowRevision++ },
		"pack role":     func(input *MaterialPrepareInput) { input.Pack.OpportunityID = "other-role" },
		"empty key":     func(input *MaterialPrepareInput) { input.RequestKey = "" },
		"bad sha shape": func(input *MaterialPrepareInput) { input.ExpectedQuestionSetSHA256 = "short" },
		"source sha":    func(input *MaterialPrepareInput) { input.SourceShas = []string{"short"} },
	}
	for name, mutate := range cases {
		input := materialPrepareInput(t, f, "prep-fence-"+name)
		mutate(&input)
		want := ErrConflict
		if name == "pack role" || name == "empty key" || name == "bad sha shape" || name == "source sha" {
			want = ErrInvalid
		}
		if _, _, err := f.store.PrepareOpportunityMaterials(ctx, ownerActor(), f.opportunity.ID, input); !errors.Is(err, want) {
			t.Fatalf("%s: want %v got %v", name, want, err)
		}
	}
	input := materialPrepareInput(t, f, "prep-fence-manifest")
	input.Pack = materialPackFor(t, f, func(manifest map[string]any) { delete(manifest, "material") })
	if _, _, err := f.store.PrepareOpportunityMaterials(ctx, ownerActor(), f.opportunity.ID, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("manifest without material section: %v", err)
	}
	input = materialPrepareInput(t, f, "prep-fence-count")
	input.Pack = materialPackFor(t, f, func(manifest map[string]any) {
		manifest["material"].(map[string]any)["answers"] = []map[string]any{{"questionId": "only-one"}}
	})
	if _, _, err := f.store.PrepareOpportunityMaterials(ctx, ownerActor(), f.opportunity.ID, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("manifest answer count: %v", err)
	}
}

func TestMaterialPrepareRequiresAnsweredStage(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "stage-select")
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "stage-check", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/9")
	round, capability := startCheckRound(t, s, "stage-round",
		[]string{RoundCodexTurn, RoundCheckSave, RoundJevRequest}, opportunity.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, "stage-save", started.WorkflowRevision,
		materialCheckSave(opportunity.ID, started.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	status, err := s.CurrentJobCheck(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil || workflow.Stage != RoleStageChecked {
		t.Fatalf("fixture stage: %+v %v", workflow, err)
	}
	preferences, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{
		"role": map[string]any{"opportunityId": opportunity.ID,
			"opportunityRevision": workflow.OpportunityRev, "profileRevision": preferences.Version},
		"material": map[string]any{"checkId": status.Check.ID,
			"questionSetSha256": status.Check.QuestionSetSHA256, "origin": MaterialOriginPrepared,
			"answers": []map[string]any{{"questionId": "a"}, {"questionId": "b"},
				{"questionId": "c"}, {"questionId": "d"}}}})
	typst := []byte(`= Application`)
	pdf := append([]byte("%PDF-1.4 fixture\n"), bytes.Repeat([]byte("0"), 200)...)
	input := MaterialPrepareInput{RequestKey: "stage-key", ExpectedCheckID: status.Check.ID,
		ExpectedQuestionSetSHA256: status.Check.QuestionSetSHA256,
		ExpectedWorkflowRevision:  workflow.Revision,
		Pack: ApplicationPackMutationInput{OpportunityID: opportunity.ID,
			ExpectedOpportunityRevision: workflow.OpportunityRev, ExpectedProfileRevision: preferences.Version,
			ManifestJSON: manifest, TypstSource: typst, PDF: pdf,
			ContentSHA256: applicationPackContentHash(manifest, typst, pdf)}}
	if _, _, err := s.PrepareOpportunityMaterials(ctx, ownerActor(), opportunity.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("checked stage: %v", err)
	}
}

func TestMaterialPrepareIdempotentReplay(t *testing.T) {
	f := setupMaterialAnswered(t, "prep-replay")
	ctx := context.Background()
	owner := ownerActor()
	saveFixtureAnswer(t, f, 0, "I want this role for its Go platform work.")
	input := materialPrepareInput(t, f, "prep-replay-key")
	first, created, err := f.store.PrepareOpportunityMaterials(ctx, owner, f.opportunity.ID, input)
	if err != nil || !created {
		t.Fatalf("first prepare: %+v %v", first, err)
	}
	replayInput := materialPrepareInput(t, f, "prep-replay-key")
	second, created, err := f.store.PrepareOpportunityMaterials(ctx, owner, f.opportunity.ID, replayInput)
	if err != nil || created {
		t.Fatalf("replay: %+v %v", second, err)
	}
	if second.Current.Version != first.Current.Version || second.Current.PackID != first.Current.PackID ||
		second.Status != first.Status {
		t.Fatalf("replay diverged: %+v vs %+v", second, first)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, f.check.Questions[0].ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: "Changed text."}); err != nil {
		t.Fatal(err)
	}
	changed := materialPrepareInput(t, f, "prep-replay-key")
	if _, _, err := f.store.PrepareOpportunityMaterials(ctx, owner, f.opportunity.ID, changed); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("reused key with changed answers: %v", err)
	}
	next, created, err := f.store.PrepareOpportunityMaterials(ctx, owner, f.opportunity.ID,
		materialPrepareInput(t, f, "prep-replay-key-2"))
	if err != nil || !created || next.Current.Version != 2 {
		t.Fatalf("second version: %+v %v", next, err)
	}
	one, err := f.store.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil || one.PackID != first.Current.PackID || one.Answers[0].TextSHA256 == next.Current.Answers[0].TextSHA256 {
		t.Fatalf("v1 mutated: %+v %v", one, err)
	}
}

func TestMaterialPrepareUnselectedRole(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	input := MaterialPrepareInput{RequestKey: "nope", ExpectedCheckID: "check",
		ExpectedQuestionSetSHA256: strings.Repeat("0", 64)}
	if _, _, err := s.PrepareOpportunityMaterials(ctx, ownerActor(), opportunity.ID, input); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected prepare: %v", err)
	}
	if _, err := s.CurrentOpportunityMaterials(ctx, opportunity.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected read: %v", err)
	}
	selected := selectFixtureOpportunity(t, s, company.ID, "fresh-select")
	view, err := s.CurrentOpportunityMaterials(ctx, selected.ID)
	if err != nil || view.Status != MaterialStatusNotPrepared || view.Current != nil {
		t.Fatalf("fresh role: %+v %v", view, err)
	}
}

func TestMaterialReadOutdatedAfterProfileMove(t *testing.T) {
	f := setupMaterialAnswered(t, "prep-stale")
	ctx := context.Background()
	owner := ownerActor()
	saveFixtureAnswer(t, f, 0, "I want this role for its Go platform work.")
	saveFixtureAnswer(t, f, 1, "I shipped a billing service in Go.")
	view, _, err := f.store.PrepareOpportunityMaterials(ctx, owner, f.opportunity.ID,
		materialPrepareInput(t, f, "prep-stale-key"))
	if err != nil || view.Status != MaterialStatusPrepared {
		t.Fatalf("prepare: %+v %v", view, err)
	}
	preferences, err := f.store.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preferences.PreferredLocation = "Rotterdam"
	if _, _, err := f.store.UpdatePreferences(ctx, preferences.Version, preferences, owner); err != nil {
		t.Fatal(err)
	}
	read, err := f.store.CurrentOpportunityMaterials(ctx, f.opportunity.ID)
	if err != nil || read.Status != MaterialStatusOutdated || read.Current.Version != 1 {
		t.Fatalf("stale read: %+v %v", read, err)
	}
	if !read.Current.Readiness.Ready {
		t.Fatalf("stale read recomputed readiness: %+v", read.Current.Readiness)
	}
}
