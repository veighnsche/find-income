package deliveryservice

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// materialBindFixture is one selected role with a checked check (one
// required plus one optional question), saved answers, a committed ready
// material version, and one assessed email route. Everything is built
// through exported store APIs.
type materialBindFixture struct {
	db          *store.Store
	owner       store.Actor
	opportunity store.Opportunity
	check       store.CheckView
	packID      string
}

func materialBindPackHash(manifest, source, pdf []byte) string {
	encoded, _ := json.Marshal(struct {
		Manifest []byte
		Source   []byte
		PDF      []byte
	}{manifest, source, pdf})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func setupMaterialBindRole(t *testing.T, db *store.Store, owner store.Actor, key, companyName, title, destination string, answerRequired bool) materialBindFixture {
	t.Helper()
	ctx := context.Background()
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: companyName})
	if err != nil {
		t.Fatal(err)
	}
	opening := "Build Go services. Apply by email to " + destination + "."
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: title, Kind: "employment",
		SourceURL: "https://harbour.example/jobs/" + key, OriginalText: opening, Stage: "new",
		WorkPattern: "hybrid", LocationText: "Amsterdam", PostedOn: "2026-09-20", DeadlineOn: "2026-10-20",
		Compensation: store.AdvertisedCompensation{Currency: "EUR", Period: "month", Basis: "base"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{
		RequestKey: key + "-select", ExpectedOpportunityRevision: opportunity.Revision, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	routeID := "route-" + key
	excerpt := "Apply by email to " + destination + "."
	if _, err := db.WriteAudited(ctx, owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO opportunity_routes(id,opportunity_id,kind,destination_text,source_kind,source_excerpt,observed_at,revision,created_at,updated_at)
		 VALUES(?,?,'direct',?,'application_instruction',?,?,1,?,?)`, routeID, opportunity.ID, destination, excerpt, now, now, now)
		return store.Change{Operation: "fixture.delivery", EntityKind: "opportunity_route", EntityID: routeID}, err
	}); err != nil {
		t.Fatal(err)
	}
	recordSyntheticRouteAssessment(t, db, owner, opportunity.ID, routeID, false)

	started, _, err := db.StartJobCheck(ctx, owner, opportunity.ID,
		store.CheckStartInput{RequestKey: key + "-check", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	var capture store.SourceCapture
	sum := sha256.Sum256([]byte("capture-" + key))
	captureSHA := hex.EncodeToString(sum[:])
	if err := db.ResearchWrite(ctx, func(rdb store.ResearchDB) error {
		capture, err = store.InsertSourceCapture(ctx, rdb, store.SourceCaptureInput{
			ContentSHA256: captureSHA, ArtifactRef: "artifact/" + captureSHA[:16], ByteLength: 2048,
			MediaType: "text/html", OriginalURL: "https://harbour.example/jobs/" + key,
			Provenance: researchcontract.ProvenanceFetchedResponse, Completeness: store.CaptureComplete,
			Executor: researchcontract.ExecutorIdentity{Backend: "test-shell"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	preferences, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agent := store.Actor{Kind: "agent", ID: "codex-check"}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: key + "-round",
		Intent: "Check one selected role", Outcome: "process_input", ProfileVersion: preferences.Version,
		Deadline: time.Now().Add(time.Hour),
		Scope: store.RoundScope{Operations: []string{store.RoundCodexTurn, store.RoundCheckSave, store.RoundJevRequest},
			Resources: []string{"campaign:active", "opportunity:" + opportunity.ID}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 8, Items: 8, Tools: 8, Turns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if round, err = db.ActivateRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{
		RequestKey: key + "-turn", Operation: store.RoundCodexTurn,
		ResourceID: "opportunity:" + opportunity.ID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	save := store.CheckSaveInput{
		OpportunityID: opportunity.ID, CheckID: started.ID,
		Vacancy: store.CheckVacancyInput{CaptureIDs: []string{capture.ID},
			Completeness: store.CaptureComplete, SourceURL: "https://harbour.example/jobs/" + key,
			RetrievedAt: "2026-09-24T11:05:00Z"},
		RequestedDocuments: []store.RequestedDocumentInput{{Label: "CV", Required: true,
			SourceExcerpt: excerpt,
			SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 214, End: 260}}},
		Route: store.CheckRouteInput{Kind: store.CheckRouteDirect, DestinationText: destination,
			Judgment: store.CheckRouteJudgmentApplication, SourceExcerpt: excerpt,
			ObservedAt: "2026-09-24T11:30:00Z"},
		Gaps: []store.CheckGapInput{{Description: "Weekly hours are not stated in the vacancy text.",
			Consequential: true, Kind: store.CheckGapMissingFact}},
		Questions: []store.CheckQuestionInput{
			{Text: "Why do you want this role?", Required: store.CheckRequired, Kind: store.CheckQuestionFreeText,
				SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 150, End: 180},
				SourceExcerpt: "Why do you want this role?"},
			{Text: "Anything else to share?", Required: store.CheckOptional, Kind: store.CheckQuestionFreeText,
				SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 500, End: 525},
				SourceExcerpt: "Anything else to share?"},
		},
		Activity: []store.CheckActivityInput{
			{Kind: "vacancy.opened", Outcome: string(researchcontract.OutcomeOK), CaptureID: capture.ID},
			{Kind: "questions.extracted", Payload: json.RawMessage(`{"count":2}`)},
		},
	}
	if _, _, err := db.ApplyRoundMutation(ctx, agent, round.ID, store.RoundMutationInput{
		RequestKey: key + "-save", Operation: store.RoundCheckSave,
		ResourceID: "opportunity:" + opportunity.ID, ExpectedRevision: started.WorkflowRevision,
		CheckSave: &save, Capability: capability}); err != nil {
		t.Fatal(err)
	}
	status, err := db.CurrentJobCheck(ctx, opportunity.ID)
	if err != nil || status.Status != store.CheckStatusChecked || len(status.Check.Questions) != 2 {
		t.Fatalf("fixture check: %+v %v", status, err)
	}
	workflow, err := db.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow, err = db.AdvanceRoleWorkflow(ctx, opportunity.ID, workflow.Revision, store.RoleStageAnswering, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AdvanceRoleWorkflow(ctx, opportunity.ID, workflow.Revision, store.RoleStageAnswered, ""); err != nil {
		t.Fatal(err)
	}
	if answerRequired {
		if _, err := db.SaveAnswerValue(ctx, owner, opportunity.ID, status.Check.Questions[0].ID,
			store.AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: "I want this role for its Go platform work."}); err != nil {
			t.Fatal(err)
		}
	}
	workflow, err = db.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferences, err = db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answers := make([]map[string]any, 0, len(status.Check.Questions))
	for _, question := range status.Check.Questions {
		answers = append(answers, map[string]any{"questionId": question.ID})
	}
	manifest, _ := json.Marshal(map[string]any{
		"role": map[string]any{"opportunityId": opportunity.ID,
			"opportunityRevision": workflow.OpportunityRev, "profileRevision": preferences.Version,
			"title": title, "company": companyName,
			"sourceUrl": "https://harbour.example/jobs/" + key, "description": opening,
			"destination": destination},
		"draft": map[string]any{
			"cover":            []any{map[string]any{"text": "I would like to apply for the " + title + " role."}},
			"materialUnknowns": materialBindUnknowns(answerRequired, status.Check.Questions[0].ID)},
		"material": map[string]any{"checkId": status.Check.ID,
			"questionSetSha256": status.Check.QuestionSetSHA256,
			"origin":            store.MaterialOriginPrepared, "answers": answers},
	})
	typst := []byte("= Application\n")
	pdf := append([]byte("%PDF-1.4 fixture\n"), make([]byte, 200)...)
	view, created, err := db.PrepareOpportunityMaterials(ctx, owner, opportunity.ID, store.MaterialPrepareInput{
		RequestKey: key + "-material", ExpectedCheckID: status.Check.ID,
		ExpectedQuestionSetSHA256: status.Check.QuestionSetSHA256,
		ExpectedWorkflowRevision:  workflow.Revision,
		Pack: store.ApplicationPackMutationInput{OpportunityID: opportunity.ID,
			ExpectedOpportunityRevision: workflow.OpportunityRev, ExpectedProfileRevision: preferences.Version,
			ManifestJSON: manifest, TypstSource: typst, PDF: pdf,
			ContentSHA256: materialBindPackHash(manifest, typst, pdf)}})
	if err != nil || !created || view.Current == nil {
		t.Fatalf("fixture material: %+v %v", view, err)
	}
	return materialBindFixture{db: db, owner: owner, opportunity: opportunity, check: *status.Check, packID: view.Current.PackID}
}

// materialBindUnknowns mirrors the production preparation services: one
// materialUnknowns entry per held or missing required question.
func materialBindUnknowns(answerRequired bool, requiredQuestionID string) []string {
	if answerRequired {
		return []string{}
	}
	return []string{requiredQuestionID}
}

func openMaterialBindStore(t *testing.T) (*store.Store, store.Actor) {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, store.Actor{Kind: "administrator", ID: "owner"}
}

func TestPrepareAndApproveBoundMaterialVersion(t *testing.T) {
	db, owner := openMaterialBindStore(t)
	f := setupMaterialBindRole(t, db, owner, "bound", "Harbour Systems", "Backend Engineer", "applications@harbour.example", true)
	svc := &Service{Store: db, From: "owner@example.org"}

	review, err := svc.PrepareMaterialReview(context.Background(), owner, "review-bound", []string{f.packID})
	if err != nil || len(review.Items) != 1 || review.Items[0].PackID != f.packID || !review.Items[0].Current {
		t.Fatalf("bound prepare: %+v %v", review, err)
	}
	if _, err := svc.ApproveMaterialReview(context.Background(), owner, review.ID, review.MaterialSHA256); err != nil {
		t.Fatalf("bound approve: %v", err)
	}
	approved, err := svc.ApproveMaterialReview(context.Background(), owner, review.ID, review.MaterialSHA256)
	if err != nil || approved.ApprovedSHA256 != review.MaterialSHA256 {
		t.Fatalf("bound approve replay: %+v %v", approved, err)
	}
	if err := svc.VerifyReviewMaterials(context.Background(), approved); err != nil {
		t.Fatalf("bound verify: %v", err)
	}
}

func TestPrepareMaterialReviewRejectsHeld(t *testing.T) {
	db, owner := openMaterialBindStore(t)
	f := setupMaterialBindRole(t, db, owner, "held", "Harbour Systems", "Backend Engineer", "applications@harbour.example", false)
	svc := &Service{Store: db, From: "owner@example.org"}

	status, err := db.CurrentOpportunityMaterials(context.Background(), f.opportunity.ID)
	if err != nil || status.Status != store.MaterialStatusHeld {
		t.Fatalf("fixture held: %+v %v", status, err)
	}
	if _, err := svc.PrepareMaterialReview(context.Background(), owner, "review-held", []string{f.packID}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("held material accepted: %v", err)
	}
	// The pre-existing pack gate agrees: the held manifest carries unknowns.
	if _, err := svc.PrepareReview(context.Background(), owner, "review-held-legacy", []string{f.packID}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("legacy gate missed held unknowns: %v", err)
	}
}

func TestApproveMaterialReviewRejectsChangedAnswers(t *testing.T) {
	db, owner := openMaterialBindStore(t)
	f := setupMaterialBindRole(t, db, owner, "edited", "Harbour Systems", "Backend Engineer", "applications@harbour.example", true)
	svc := &Service{Store: db, From: "owner@example.org"}
	ctx := context.Background()

	bound, err := svc.PrepareMaterialReview(ctx, owner, "review-edited", []string{f.packID})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := svc.PrepareReview(ctx, owner, "review-edited-legacy", []string{f.packID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveAnswerValue(ctx, owner, f.opportunity.ID, f.check.Questions[0].ID,
		store.AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: "Changed after the review was prepared."}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveMaterialReview(ctx, owner, bound.ID, bound.MaterialSHA256); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed answers approved: %v", err)
	}
	if err := svc.VerifyReviewMaterials(ctx, bound); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("send pre-check missed changed answers: %v", err)
	}
	// Without the F2 binding the same change is invisible: the legacy
	// pack/route checks still pass, which is the gap this task closes.
	if _, err := svc.ApproveReview(ctx, owner, legacy.ID, legacy.MaterialSHA256); err != nil {
		t.Fatalf("legacy control unexpectedly rejected: %v", err)
	}
}

func TestMaterialBindingRejectsStaleVersionAfterEdit(t *testing.T) {
	db, owner := openMaterialBindStore(t)
	f := setupMaterialBindRole(t, db, owner, "stale", "Harbour Systems", "Backend Engineer", "applications@harbour.example", true)
	svc := &Service{Store: db, From: "owner@example.org"}
	ctx := context.Background()

	review, err := svc.PrepareMaterialReview(ctx, owner, "review-stale", []string{f.packID})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := db.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opportunity, err := db.Opportunity(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	const editedText = "Edited application text, inspected by the owner."
	answers := make([]map[string]any, 0, len(f.check.Questions))
	for _, question := range f.check.Questions {
		answers = append(answers, map[string]any{"questionId": question.ID})
	}
	manifest, _ := json.Marshal(map[string]any{
		"role": map[string]any{"opportunityId": f.opportunity.ID,
			"opportunityRevision": workflow.OpportunityRev, "profileRevision": preferences.Version,
			"title": opportunity.Title, "company": "Harbour Systems",
			"sourceUrl": opportunity.SourceURL, "description": opportunity.OriginalText,
			"destination": "applications@harbour.example"},
		"draft": map[string]any{
			"cover":            []any{map[string]any{"text": editedText}},
			"materialUnknowns": []string{}},
		"material": map[string]any{"checkId": f.check.ID,
			"questionSetSha256": f.check.QuestionSetSHA256,
			"origin":            store.MaterialOriginDirectEdit, "text": editedText, "answers": answers},
	})
	typst := []byte("= Application edited\n")
	pdf := append([]byte("%PDF-1.4 fixture\n"), make([]byte, 200)...)
	edited, created, err := db.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, store.MaterialEditInput{
		RequestKey: "stale-edit", ExpectedVersion: 1, Text: editedText,
		Pack: store.ApplicationPackMutationInput{OpportunityID: f.opportunity.ID,
			ExpectedOpportunityRevision: workflow.OpportunityRev, ExpectedProfileRevision: preferences.Version,
			ManifestJSON: manifest, TypstSource: typst, PDF: pdf,
			ContentSHA256: materialBindPackHash(manifest, typst, pdf)}})
	if err != nil || !created || edited.Version != 2 || edited.PackID == f.packID {
		t.Fatalf("fixture edit: %+v %v", edited, err)
	}
	if _, err := svc.ApproveMaterialReview(ctx, owner, review.ID, review.MaterialSHA256); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("superseded version approved: %v", err)
	}
	if _, err := svc.PrepareMaterialReview(ctx, owner, "review-stale-retry", []string{f.packID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("superseded pack re-prepared: %v", err)
	}
	fresh, err := svc.PrepareMaterialReview(ctx, owner, "review-stale-fresh", []string{edited.PackID})
	if err != nil || len(fresh.Items) != 1 || fresh.Items[0].PackID != edited.PackID {
		t.Fatalf("fresh version rejected: %+v %v", fresh, err)
	}
	if _, err := svc.ApproveMaterialReview(ctx, owner, fresh.ID, fresh.MaterialSHA256); err != nil {
		t.Fatalf("fresh version not approved: %v", err)
	}
}

func TestPrepareMaterialReviewRejectsStaleProfile(t *testing.T) {
	db, owner := openMaterialBindStore(t)
	f := setupMaterialBindRole(t, db, owner, "profile", "Harbour Systems", "Backend Engineer", "applications@harbour.example", true)
	svc := &Service{Store: db, From: "owner@example.org"}
	ctx := context.Background()

	review, err := svc.PrepareMaterialReview(ctx, owner, "review-profile", []string{f.packID})
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.UpdatePreferences(ctx, preferences.Version, preferences, owner); err != nil {
		t.Fatal(err)
	}
	status, err := db.CurrentOpportunityMaterials(ctx, f.opportunity.ID)
	if err != nil || status.Status != store.MaterialStatusOutdated {
		t.Fatalf("fixture outdated: %+v %v", status, err)
	}
	if _, err := svc.ApproveMaterialReview(ctx, owner, review.ID, review.MaterialSHA256); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("outdated material approved: %v", err)
	}
}

func TestPrepareMaterialReviewPreservesIdempotency(t *testing.T) {
	db, owner := openMaterialBindStore(t)
	f := setupMaterialBindRole(t, db, owner, "idem", "Harbour Systems", "Backend Engineer", "applications@harbour.example", true)
	svc := &Service{Store: db, From: "owner@example.org"}
	ctx := context.Background()

	first, err := svc.PrepareMaterialReview(ctx, owner, "review-idem", []string{f.packID})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := svc.PrepareMaterialReview(ctx, owner, "review-idem", []string{f.packID})
	if err != nil || retry.ID != first.ID || retry.MaterialSHA256 != first.MaterialSHA256 ||
		retry.Items[0].MessageID != first.Items[0].MessageID {
		t.Fatalf("lost response created different material: first=%+v retry=%+v err=%v", first, retry, err)
	}
	// A new version under the same key conflicts: the edited pack is current
	// and ready, so the binding passes and the stored request-key probe
	// rejects the changed material.
	edited := editMaterialBindText(t, db, owner, f, "idem-edit", "Idempotency edit.")
	if _, err := svc.PrepareMaterialReview(ctx, owner, "review-idem", []string{edited.PackID}); !errors.Is(err, store.ErrRoundIdempotencyConflict) {
		t.Fatalf("request key reused for different material: %v", err)
	}
}

func editMaterialBindText(t *testing.T, db *store.Store, owner store.Actor, f materialBindFixture, key, text string) store.MaterialVersionView {
	t.Helper()
	ctx := context.Background()
	workflow, err := db.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opportunity, err := db.Opportunity(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	answers := make([]map[string]any, 0, len(f.check.Questions))
	for _, question := range f.check.Questions {
		answers = append(answers, map[string]any{"questionId": question.ID})
	}
	manifest, _ := json.Marshal(map[string]any{
		"role": map[string]any{"opportunityId": f.opportunity.ID,
			"opportunityRevision": workflow.OpportunityRev, "profileRevision": preferences.Version,
			"title": opportunity.Title, "company": "Harbour Systems",
			"sourceUrl": opportunity.SourceURL, "description": opportunity.OriginalText,
			"destination": "applications@harbour.example"},
		"draft": map[string]any{
			"cover":            []any{map[string]any{"text": text}},
			"materialUnknowns": []string{}},
		"material": map[string]any{"checkId": f.check.ID,
			"questionSetSha256": f.check.QuestionSetSHA256,
			"origin":            store.MaterialOriginDirectEdit, "text": text, "answers": answers},
	})
	typst := []byte("= Application edited\n")
	pdf := append([]byte("%PDF-1.4 fixture\n"), make([]byte, 200)...)
	edited, created, err := db.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, store.MaterialEditInput{
		RequestKey: key, ExpectedVersion: 1, Text: text,
		Pack: store.ApplicationPackMutationInput{OpportunityID: f.opportunity.ID,
			ExpectedOpportunityRevision: workflow.OpportunityRev, ExpectedProfileRevision: preferences.Version,
			ManifestJSON: manifest, TypstSource: typst, PDF: pdf,
			ContentSHA256: materialBindPackHash(manifest, typst, pdf)}})
	if err != nil || !created {
		t.Fatalf("fixture edit: %+v %v", edited, err)
	}
	return edited
}

func TestMaterialStatusVerdicts(t *testing.T) {
	cases := []struct {
		status string
		want   error
	}{
		{store.MaterialStatusPrepared, nil},
		{store.MaterialStatusHeld, store.ErrInvalid},
		{store.MaterialStatusNotPrepared, store.ErrInvalid},
		{store.MaterialStatusOutdated, store.ErrConflict},
		{store.MaterialStatusPreparing, store.ErrConflict},
		{"", store.ErrConflict},
	}
	for _, tc := range cases {
		if err := materialStatusErr(tc.status); !errors.Is(err, tc.want) {
			t.Errorf("status %q: got %v want %v", tc.status, err, tc.want)
		}
	}
	if !errors.Is(materialReadErr(store.ErrRoleNotSelected), store.ErrFenced) {
		t.Error("unselected role did not fence")
	}
	if !errors.Is(materialReadErr(store.ErrNotFound), store.ErrConflict) {
		t.Error("vanished role did not read as stale")
	}
}

func TestPackMaterialUnknowns(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     []string
		wantErr  bool
	}{
		{"empty", `{"draft":{"materialUnknowns":[]}}`, []string{}, false},
		{"missing key", `{"draft":{"cover":[]}}`, nil, false},
		{"held", `{"draft":{"materialUnknowns":["q1"]}}`, []string{"q1"}, false},
		{"missing draft", `{"role":{}}`, nil, true},
		{"invalid json", `{`, nil, true},
	}
	for _, tc := range cases {
		got, err := packMaterialUnknowns([]byte(tc.manifest))
		if tc.wantErr != (err != nil) {
			t.Errorf("%s: err=%v", tc.name, err)
			continue
		}
		if !tc.wantErr && len(got) != len(tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckMaterialAnswerRefs(t *testing.T) {
	answered := store.QuestionAnswerValue{QuestionID: "q1", Version: 1,
		State: store.AnswerValueStateAnswered, TextSHA256: "sha-answered"}
	blank := store.QuestionAnswerValue{QuestionID: "q2", Version: 2, State: store.AnswerValueStateBlank}
	answeredRef := store.MaterialAnswerRef{QuestionID: "q1", AnswerVersion: 1, TextSHA256: "sha-answered"}
	blankRef := store.MaterialAnswerRef{QuestionID: "q2", AnswerVersion: 2, TextSHA256: materialBlankTextSHA}
	unsetRef := store.MaterialAnswerRef{QuestionID: "q3", TextSHA256: materialBlankTextSHA}
	draftRef := store.MaterialAnswerRef{QuestionID: "q4", TextSHA256: "sha-draft"}

	cases := []struct {
		name   string
		refs   []store.MaterialAnswerRef
		values []store.QuestionAnswerValue
		want   error
	}{
		{"exact", []store.MaterialAnswerRef{answeredRef, blankRef, unsetRef, draftRef},
			[]store.QuestionAnswerValue{answered, blank}, nil},
		{"answered text changed", []store.MaterialAnswerRef{answeredRef},
			[]store.QuestionAnswerValue{{QuestionID: "q1", Version: 2,
				State: store.AnswerValueStateAnswered, TextSHA256: "sha-new"}}, store.ErrConflict},
		{"answered sha drifted", []store.MaterialAnswerRef{answeredRef},
			[]store.QuestionAnswerValue{{QuestionID: "q1", Version: 1,
				State: store.AnswerValueStateAnswered, TextSHA256: "sha-new"}}, store.ErrConflict},
		{"answered blanked", []store.MaterialAnswerRef{answeredRef},
			[]store.QuestionAnswerValue{{QuestionID: "q1", Version: 2, State: store.AnswerValueStateBlank}}, store.ErrConflict},
		{"blank answered", []store.MaterialAnswerRef{blankRef},
			[]store.QuestionAnswerValue{{QuestionID: "q2", Version: 3,
				State: store.AnswerValueStateAnswered, TextSHA256: "sha-x"}}, store.ErrConflict},
		{"blank reblanked", []store.MaterialAnswerRef{blankRef},
			[]store.QuestionAnswerValue{{QuestionID: "q2", Version: 3, State: store.AnswerValueStateBlank}}, store.ErrConflict},
		{"unset answered", []store.MaterialAnswerRef{unsetRef},
			[]store.QuestionAnswerValue{{QuestionID: "q3", Version: 1,
				State: store.AnswerValueStateAnswered, TextSHA256: "sha-x"}}, store.ErrConflict},
		{"draft answered", []store.MaterialAnswerRef{draftRef},
			[]store.QuestionAnswerValue{{QuestionID: "q4", Version: 1, State: store.AnswerValueStateBlank}}, store.ErrConflict},
		{"value deleted", []store.MaterialAnswerRef{answeredRef}, nil, store.ErrConflict},
		{"unpinned value", []store.MaterialAnswerRef{answeredRef},
			[]store.QuestionAnswerValue{answered,
				{QuestionID: "q9", Version: 1, State: store.AnswerValueStateBlank}}, store.ErrConflict},
	}
	for _, tc := range cases {
		if err := checkMaterialAnswerRefs(tc.refs, tc.values); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
}
