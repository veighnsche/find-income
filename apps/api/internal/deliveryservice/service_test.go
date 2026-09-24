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

	"github.com/veighnsche/find-income-dashboard/api/internal/delivery"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type routeEvaluator func(jev.Request) (jev.Result, error)

func (f routeEvaluator) Evaluate(_ context.Context, request jev.Request) (jev.Result, error) {
	return f(request)
}

func recordSyntheticRouteAssessment(t *testing.T, db *store.Store, owner store.Actor, opportunityID, routeID string, resumeBeforeSave bool) {
	t.Helper()
	ctx := context.Background()
	opportunity, err := db.Opportunity(ctx, opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := db.ListOpportunityRoutes(ctx, opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	var route store.OpportunityRoute
	for _, candidate := range routes {
		if candidate.ID == routeID {
			route = candidate
		}
	}
	if route.ID == "" {
		t.Fatal("route fixture missing")
	}
	input := jev.DeliveryRouteInput{OpportunityID: opportunityID, RouteID: routeID, Title: opportunity.Title,
		SourceURL: opportunity.SourceURL, OriginalText: opportunity.OriginalText,
		SourceSHA256: store.DeliverySourceHash(opportunity.Title, opportunity.SourceURL, opportunity.OriginalText),
		RouteKind:    route.Kind, Destination: route.DestinationText, RouteSourceKind: route.SourceKind,
		RouteSourceRef: route.SourceRef, RouteExcerpt: route.SourceExcerpt, RouteSHA256: store.DeliveryRouteHash(route), MaxReportedTokens: 2500}
	raw := []byte(`{"model":"synthetic","answers":{"delivery_route":{"type":"choice","choice":"application_mailbox","probabilities":{"application_mailbox":1,"other_contact":0,"unresolved":0},"confidence":1}},"usage":{"input_tokens":12,"output_tokens":4}}`)
	result, err := jev.AssessDeliveryRoute(ctx, routeEvaluator(func(_ jev.Request) (jev.Result, error) {
		return jev.Result{RequestedModel: "synthetic", ReturnedModel: "synthetic", RawResponse: raw,
			Usage: jev.Usage{InputTokens: 12, OutputTokens: 4}, Answers: map[string]jev.Answer{
				"delivery_route": {Type: "choice", Choice: &jev.ChoiceAnswer{Choice: "application_mailbox",
					Probabilities: map[string]float64{"application_mailbox": 1, "other_contact": 0, "unresolved": 0}, Confidence: 1}},
			}}, nil
	}), input)
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "route-assess-" + routeID + "-" + input.SourceSHA256,
		Intent: "Prepare the source-linked application pack and route evidence", Outcome: "prepare", ProfileVersion: 1,
		Scope: store.RoundScope{Resources: []string{"opportunity:" + opportunityID}, Delegates: []string{"codex-runner"},
			Operations: []string{store.RoundPrepareApplicationPack, store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 2}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	cost, _ := store.RoundOperationCost(store.RoundJevRequest)
	stage, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "route-jev-" + routeID,
		Operation: store.RoundJevRequest, ResourceID: "opportunity:" + opportunityID, Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, round.ID, stage.ID); err != nil {
		t.Fatal(err)
	}
	logical := []byte(result.RequestSnapshot)
	logicalSHA := sha256.Sum256(logical)
	attempt, err := db.BeginJevAttempt(ctx, store.JevAttemptStart{RoundID: round.ID, RoundAttemptID: stage.ID,
		Purpose: "delivery_route", InputSHA256: hex.EncodeToString(logicalSHA[:]), SourceRefsJSON: []byte(`[]`),
		CandidateSetJSON: []byte(`[]`), ProfileVersion: 1, RubricVersion: "delivery-route-v1", RequestedModel: "synthetic",
		LogicalRequestJSON: logical, TransportRequestBytes: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	inputTokens, outputTokens := int64(12), int64(4)
	if _, err = db.FinishJevAttempt(ctx, store.JevAttemptFinish{ID: attempt.ID, Status: "succeeded", ReturnedModel: "synthetic",
		RawResponseBytes: raw, InputTokens: &inputTokens, OutputTokens: &outputTokens}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.FinishRoundAttempt(ctx, agent, round.ID, stage.ID, true, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	if resumeBeforeSave {
		stopping, _, err := db.StopRound(ctx, owner, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		paused, err := db.PauseStoppedRound(ctx, owner, stopping.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ResumeRound(ctx, owner, paused.ID, paused.Generation); err != nil {
			t.Fatal(err)
		}
	}
	tampered := result
	tampered.Choice = "other_contact"
	if _, err = db.SaveDeliveryRouteAssessment(ctx, agent, round.ID, attempt.ID, input, tampered); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("caller-edited choice persisted: %v", err)
	}
	changedInput := input
	changedInput.Title += " changed"
	if _, err = db.SaveDeliveryRouteAssessment(ctx, agent, round.ID, attempt.ID, changedInput, result); err == nil {
		t.Fatal("caller-edited input persisted")
	}
	if _, err = db.SaveDeliveryRouteAssessment(ctx, agent, round.ID, attempt.ID, input, result); err != nil {
		t.Fatal(err)
	}
	if _, err = db.FinishRound(ctx, owner, round.ID, store.RoundCompleted, "prepared", "pack_saved", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}

type fakeSender struct {
	calls int
	fn    func(context.Context) delivery.Outcome
}

type fakeNextActionAdvisor struct {
	calls    int
	facts    DeliveryAdviceFacts
	response json.RawMessage
}

func (f *fakeNextActionAdvisor) RecommendDelivery(_ context.Context, _ store.Round, facts DeliveryAdviceFacts) json.RawMessage {
	f.calls++
	f.facts = facts
	if len(f.response) != 0 {
		return f.response
	}
	return json.RawMessage(`{"status":"selected","action":"review_delivery"}`)
}

func (f *fakeSender) Send(ctx context.Context, _ delivery.Material, _ string) delivery.Outcome {
	f.calls++
	return f.fn(ctx)
}

func deliveryFixture(t *testing.T, resumeRouteBeforeSave ...bool) (*store.Store, string, store.Actor, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	opening := "Build Go services. Apply by email to applications@harbour.example."
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/1", OriginalText: opening, Notes: "Private note", Stage: "new",
		WorkPattern: "hybrid", LocationText: "Amsterdam", PostedOn: "2026-09-20", DeadlineOn: "2026-10-20",
		Compensation: store.AdvertisedCompensation{Currency: "EUR", Period: "month", Basis: "base"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{
		RequestKey: "select", ExpectedOpportunityRevision: opportunity.Revision, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"role": map[string]any{"destination": "applications@harbour.example",
		"title": opportunity.Title, "company": company.Name, "sourceUrl": opportunity.SourceURL,
		"description": opportunity.OriginalText, "opportunityId": opportunity.ID,
		"opportunityRevision": opportunity.Revision, "profileRevision": 1},
		"draft": map[string]any{"cover": []any{map[string]any{"text": "I would like to apply for the Backend Engineer role."}}, "materialUnknowns": []string{}}})
	source := []byte("= CV\n")
	pdf := append([]byte("%PDF-1.7\n"), make([]byte, 110)...)
	packed, _ := json.Marshal(struct{ Manifest, Source, PDF []byte }{manifest, source, pdf})
	hash := sha256.Sum256(packed)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.WriteAudited(ctx, owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO application_packs(id,opportunity_id,opportunity_revision,profile_revision,version,content_sha256,manifest_json,typst_source,pdf,created_at)
		 VALUES('pack-1',?,?,1,1,?,?,?,?,?)`, opportunity.ID, opportunity.Revision, hex.EncodeToString(hash[:]), string(manifest), source, pdf, now)
		if err != nil {
			return store.Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO opportunity_routes(id,opportunity_id,kind,destination_text,source_kind,source_excerpt,observed_at,revision,created_at,updated_at)
		 VALUES('route-1',?,'direct','applications@harbour.example','application_instruction',?, ?,1,?,?)`, opportunity.ID, "Apply by email to applications@harbour.example.", now, now, now)
		return store.Change{Operation: "fixture.delivery", EntityKind: "application_pack", EntityID: "pack-1"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	recordSyntheticRouteAssessment(t, db, owner, opportunity.ID, "route-1", len(resumeRouteBeforeSave) == 1 && resumeRouteBeforeSave[0])
	return db, dir, owner, opportunity.ID
}

func prepareApproved(t *testing.T, db *store.Store, owner store.Actor) (store.DeliveryReview, *Service) {
	t.Helper()
	svc := &Service{Store: db, From: "owner@example.org"}
	review, err := svc.PrepareReview(context.Background(), owner, "review-pack-1", []string{"pack-1"})
	if err != nil || len(review.Items) != 1 || !review.Items[0].Current || review.Items[0].RouteExcerpt == "" {
		t.Fatalf("prepare review: %+v %v", review, err)
	}
	if _, err := svc.ApproveReview(context.Background(), owner, review.ID, "bad"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("wrong digest approved: %v", err)
	}
	review, err = svc.ApproveReview(context.Background(), owner, review.ID, review.MaterialSHA256)
	if err != nil || review.ApprovedSHA256 != review.MaterialSHA256 {
		t.Fatalf("approve: %+v %v", review, err)
	}
	return review, svc
}

func addSecondDeliveryPack(t *testing.T, db *store.Store, owner store.Actor) {
	t.Helper()
	ctx := context.Background()
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Delta Works"})
	if err != nil {
		t.Fatal(err)
	}
	opening := "Build reliable APIs. Send your application to hiring@delta.example."
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "API Engineer", Kind: "employment",
		SourceURL: "https://delta.example/jobs/2", OriginalText: opening, Stage: "new", WorkPattern: "remote", LocationText: "Brussels",
		PostedOn: "2026-09-20", DeadlineOn: "2026-10-20", Compensation: store.AdvertisedCompensation{Currency: "EUR", Period: "month", Basis: "base"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{
		RequestKey: "select-second", ExpectedOpportunityRevision: opportunity.Revision, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"role": map[string]any{"destination": "hiring@delta.example",
		"title": opportunity.Title, "company": company.Name, "sourceUrl": opportunity.SourceURL,
		"description": opportunity.OriginalText, "opportunityId": opportunity.ID,
		"opportunityRevision": opportunity.Revision, "profileRevision": 1},
		"draft": map[string]any{"cover": []any{map[string]any{"text": "I would like to apply for the API Engineer role."}}, "materialUnknowns": []string{}}})
	source := []byte("= CV\n")
	pdf := append([]byte("%PDF-1.7\n"), make([]byte, 110)...)
	packed, _ := json.Marshal(struct{ Manifest, Source, PDF []byte }{manifest, source, pdf})
	hash := sha256.Sum256(packed)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.WriteAudited(ctx, owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO application_packs(id,opportunity_id,opportunity_revision,profile_revision,version,content_sha256,manifest_json,typst_source,pdf,created_at)
		 VALUES('pack-2',?,?,1,1,?,?,?,?,?)`, opportunity.ID, opportunity.Revision, hex.EncodeToString(hash[:]), string(manifest), source, pdf, now)
		if err != nil {
			return store.Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO opportunity_routes(id,opportunity_id,kind,destination_text,source_kind,source_excerpt,observed_at,revision,created_at,updated_at)
		 VALUES('route-2',?,'direct','hiring@delta.example','application_instruction',?, ?,1,?,?)`, opportunity.ID, "Send your application to hiring@delta.example.", now, now, now)
		return store.Change{Operation: "fixture.delivery", EntityKind: "application_pack", EntityID: "pack-2"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	recordSyntheticRouteAssessment(t, db, owner, opportunity.ID, "route-2", false)
}

func TestBoundedExactBatchRecordsPerItemOutcomes(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	addSecondDeliveryPack(t, db, owner)
	svc := &Service{Store: db, From: "owner@example.org"}
	if _, err := svc.PrepareReview(context.Background(), owner, "review-too-many", []string{"pack-1", "pack-2", "pack-1", "pack-2"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unbounded batch accepted: %v", err)
	}
	review, err := svc.PrepareReview(context.Background(), owner, "review-batch", []string{"pack-1", "pack-2"})
	if err != nil || len(review.Items) != 2 {
		t.Fatalf("batch prepare: %+v %v", review, err)
	}
	review, err = svc.ApproveReview(context.Background(), owner, review.ID, review.MaterialSHA256)
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	sender.fn = func(_ context.Context) delivery.Outcome {
		if sender.calls == 1 {
			return delivery.Outcome{State: delivery.AcceptedBySMTP, Stage: "data_reply", SMTPCode: 250}
		}
		return delivery.Outcome{State: delivery.RejectedBeforeData, Stage: "recipient", SMTPCode: 550}
	}
	svc.Sender = sender
	result, err := svc.SendReview(context.Background(), owner, review.ID)
	if err != nil || sender.calls != 2 || result.Round.Used.Items != 2 {
		t.Fatalf("batch send: %+v err=%v calls=%d", result, err, sender.calls)
	}
	states := map[string]int{}
	for _, item := range result.Review.Items {
		states[item.State]++
	}
	if states["accepted_by_smtp"] != 1 || states["failed"] != 1 || states["verified_receipt"] != 0 {
		t.Fatalf("per-item outcomes collapsed or receipt invented: %+v", states)
	}
}

func TestPrepareReviewRequestKeyRecoversExactReview(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	svc := &Service{Store: db, From: "owner@example.org"}
	first, err := svc.PrepareReview(context.Background(), owner, "recover-review", []string{"pack-1"})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := svc.PrepareReview(context.Background(), owner, "recover-review", []string{"pack-1"})
	if err != nil || retry.ID != first.ID || retry.MaterialSHA256 != first.MaterialSHA256 || retry.Items[0].MessageID != first.Items[0].MessageID {
		t.Fatalf("lost response created different material: first=%+v retry=%+v err=%v", first, retry, err)
	}
	if _, err := svc.PrepareReview(context.Background(), owner, "recover-review", []string{"another-pack"}); !errors.Is(err, store.ErrRoundIdempotencyConflict) {
		t.Fatalf("request key reused for different packs: %v", err)
	}
}

func TestChangedRoleBeforeReviewCannotBlessOldPackWithFreshRouteAssessment(t *testing.T) {
	ctx := context.Background()
	db, _, owner, opportunityID := deliveryFixture(t)
	defer db.Close()
	old, err := db.Opportunity(ctx, opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	title := "Sales Director"
	changed, _, err := db.PatchOpportunity(ctx, owner, opportunityID, store.OpportunityPatch{ExpectedRevision: old.Revision, Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunityID, store.OwnerDecisionInput{
		RequestKey: "select-changed-role", ExpectedOpportunityRevision: changed.Revision,
		ExpectedDecisionRevision: 1, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	recordSyntheticRouteAssessment(t, db, owner, opportunityID, "route-1", false)
	svc := &Service{Store: db, From: "owner@example.org", Sender: &fakeSender{fn: func(context.Context) delivery.Outcome {
		return delivery.Outcome{State: delivery.AcceptedBySMTP}
	}}}
	if _, err := svc.PrepareReview(ctx, owner, "changed-role-review", []string{"pack-1"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old pack became reviewable under fresh route judgment: %v", err)
	}
	if _, err := db.DeliveryReviewByRequest(ctx, owner, "changed-role-review", []string{"pack-1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale review persisted: %v", err)
	}
}

func TestCosmeticOpportunityEditRetainsPackReview(t *testing.T) {
	ctx := context.Background()
	db, _, owner, opportunityID := deliveryFixture(t)
	defer db.Close()
	old, err := db.Opportunity(ctx, opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	notes := "Updated private notes only"
	changed, _, err := db.PatchOpportunity(ctx, owner, opportunityID, store.OpportunityPatch{ExpectedRevision: old.Revision, Notes: &notes})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunityID, store.OwnerDecisionInput{
		RequestKey: "select-notes-edit", ExpectedOpportunityRevision: changed.Revision,
		ExpectedDecisionRevision: 1, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: db, From: "owner@example.org"}
	review, err := svc.PrepareReview(ctx, owner, "notes-review", []string{"pack-1"})
	if err != nil || len(review.Items) != 1 || !review.Items[0].Current {
		t.Fatalf("cosmetic edit invalidated pack: %+v %v", review, err)
	}
}

func TestPriorGenerationCapturedRouteCanCompleteAfterResume(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t, true)
	defer db.Close()
	svc := &Service{Store: db, From: "owner@example.org"}
	review, err := svc.PrepareReview(context.Background(), owner, "resumed-route-review", []string{"pack-1"})
	if err != nil || len(review.Items) != 1 || !review.Items[0].Current {
		t.Fatalf("captured route lost after resume: %+v %v", review, err)
	}
	opportunity, err := db.Opportunity(context.Background(), review.Items[0].OpportunityID)
	if err != nil {
		t.Fatal(err)
	}
	roundKey := "route-assess-route-1-" + store.DeliverySourceHash(opportunity.Title, opportunity.SourceURL, opportunity.OriginalText)
	assessments, err := db.JevAttemptsForRound(context.Background(), reviewRouteRoundID(t, db, roundKey, owner))
	if err != nil || len(assessments) != 1 || assessments[0].InputTokens == nil || *assessments[0].InputTokens != 12 ||
		assessments[0].OutputTokens == nil || *assessments[0].OutputTokens != 4 || len(assessments[0].RawResponseBytes) == 0 {
		t.Fatalf("captured Jev evidence changed: %+v %v", assessments, err)
	}
}

func reviewRouteRoundID(t *testing.T, db *store.Store, key string, owner store.Actor) string {
	t.Helper()
	round, err := db.RoundByRequest(context.Background(), owner, key)
	if err != nil {
		t.Fatal(err)
	}
	return round.ID
}

func TestExactReviewDoubleClickAndSubmissionIsNotReceipt(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	review, svc := prepareApproved(t, db, owner)
	sender := &fakeSender{fn: func(_ context.Context) delivery.Outcome {
		return delivery.Outcome{State: delivery.AcceptedBySMTP, Stage: "data_reply", SMTPCode: 250}
	}}
	svc.Sender = sender
	advisor := &fakeNextActionAdvisor{}
	svc.Advisor = advisor
	result, err := svc.SendReview(context.Background(), owner, review.ID)
	if err != nil || sender.calls != 1 || result.Review.Items[0].State != "accepted_by_smtp" || result.Round.State != store.RoundCompleted {
		t.Fatalf("send result: %+v err=%v calls=%d", result, err, sender.calls)
	}
	if advisor.calls != 1 || advisor.facts != (DeliveryAdviceFacts{ReviewID: review.ID, Recorded: 1}) ||
		result.Round.Limits.Requests != 2 || result.Round.Scope.Resources[len(result.Round.Scope.Resources)-1] != "campaign:active" {
		t.Fatalf("delivery advice scope or bounded facts: round=%+v calls=%d facts=%+v", result.Round, advisor.calls, advisor.facts)
	}
	var report struct {
		EmployerReceiptVerified bool            `json:"employerReceiptVerified"`
		Recommendation          json.RawMessage `json:"recommendation"`
	}
	if json.Unmarshal(result.Round.Report, &report) != nil || report.EmployerReceiptVerified || len(report.Recommendation) == 0 {
		t.Fatalf("submission report lost truth or advice: %s", result.Round.Report)
	}
	result, err = svc.SendReview(context.Background(), owner, review.ID)
	if err != nil || sender.calls != 1 || advisor.calls != 1 || result.Review.Items[0].State != "accepted_by_smtp" {
		t.Fatalf("double click resent or repeated advice: %+v err=%v sender=%d advice=%d", result, err, sender.calls, advisor.calls)
	}
	svc.Sender = nil
	result, err = svc.SendReview(context.Background(), owner, review.ID)
	if err != nil || result.Review.Items[0].State != "accepted_by_smtp" || sender.calls != 1 {
		t.Fatalf("existing outcome hidden after sender config loss: %+v err=%v calls=%d", result, err, sender.calls)
	}
}

func TestUnavailableAdvicePreservesRecordedDeliveryResult(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	review, svc := prepareApproved(t, db, owner)
	sender := &fakeSender{fn: func(context.Context) delivery.Outcome {
		return delivery.Outcome{State: delivery.AcceptedBySMTP, Stage: "data_reply", SMTPCode: 250}
	}}
	advisor := &fakeNextActionAdvisor{response: json.RawMessage(`{"status":"unavailable","code":"recommendation_provider_unavailable"}`)}
	svc.Sender, svc.Advisor = sender, advisor
	result, err := svc.SendReview(context.Background(), owner, review.ID)
	if err != nil || sender.calls != 1 || advisor.calls != 1 || result.Round.State != store.RoundCompleted || result.Review.Items[0].State != "accepted_by_smtp" {
		t.Fatalf("saved submission lost with unavailable advice: %+v %v", result, err)
	}
	var report struct {
		EmployerReceiptVerified bool            `json:"employerReceiptVerified"`
		Recommendation          json.RawMessage `json:"recommendation"`
	}
	if json.Unmarshal(result.Round.Report, &report) != nil || report.EmployerReceiptVerified || string(report.Recommendation) != string(advisor.response) {
		t.Fatalf("unavailable advice changed delivery truth: %s", result.Round.Report)
	}
}

func TestSendRequestLostBeforeArrivalStartsOnlyOnReplay(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	review, svc := prepareApproved(t, db, owner)
	if _, err := svc.SendReview(context.Background(), owner, review.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unconfigured first request commissioned send: %v", err)
	}
	sender := &fakeSender{fn: func(context.Context) delivery.Outcome {
		return delivery.Outcome{State: delivery.AcceptedBySMTP, Stage: "data_reply", SMTPCode: 250}
	}}
	svc.Sender = sender
	result, err := svc.SendReview(context.Background(), owner, review.ID)
	if err != nil || sender.calls != 1 || result.Review.Items[0].State != "accepted_by_smtp" {
		t.Fatalf("first reaching replay did not send once: %+v err=%v calls=%d", result, err, sender.calls)
	}
}

func TestChangedAttachmentAndRouteBlockApprovalOrSend(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	svc := &Service{Store: db, From: "owner@example.org"}
	review, err := svc.PrepareReview(context.Background(), owner, "review-pack-1", []string{"pack-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.WriteAudited(context.Background(), owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.Exec(`UPDATE application_packs SET pdf=pdf||'changed' WHERE id='pack-1'`)
		return store.Change{Operation: "fixture.change_pdf", EntityKind: "application_pack", EntityID: "pack-1"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveReview(context.Background(), owner, review.ID, review.MaterialSHA256); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed attachment accepted: %v", err)
	}
}

func TestChangedRouteBlocksSendButCosmeticRouteRevisionDoesNot(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	review, svc := prepareApproved(t, db, owner)
	ctx := context.Background()
	_, err := db.WriteAudited(ctx, owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.Exec(`UPDATE opportunity_routes SET revision=revision+1,updated_at='2026-09-23T21:00:00Z' WHERE id='route-1'`)
		return store.Change{Operation: "fixture.cosmetic_route", EntityKind: "opportunity_route", EntityID: "route-1"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	stillCurrent, err := db.DeliveryReview(ctx, review.ID)
	if err != nil || !stillCurrent.Items[0].Current {
		t.Fatalf("cosmetic route revision invalidated approval: %+v %v", stillCurrent, err)
	}
	_, err = db.WriteAudited(ctx, owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.Exec(`UPDATE opportunity_routes SET destination_text='other@harbour.example',revision=revision+1 WHERE id='route-1'`)
		return store.Change{Operation: "fixture.destination_change", EntityKind: "opportunity_route", EntityID: "route-1"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{fn: func(_ context.Context) delivery.Outcome { return delivery.Outcome{State: delivery.AcceptedBySMTP} }}
	svc.Sender = sender
	result, err := svc.SendReview(ctx, owner, review.ID)
	if err != nil || sender.calls != 0 || result.Round.State != store.RoundFailed || result.Review.Items[0].Current {
		t.Fatalf("changed route reached sender: %+v err=%v calls=%d", result, err, sender.calls)
	}
}

func TestStopDuringSubmissionRetainsLateOutcomeWithoutResend(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	review, svc := prepareApproved(t, db, owner)
	ctx := context.Background()
	sender := &fakeSender{fn: func(sendCtx context.Context) delivery.Outcome {
		round, err := db.ActiveRound(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.StopRound(ctx, owner, round.ID); err != nil {
			t.Fatal(err)
		}
		if sendCtx.Err() == nil {
			t.Fatal("Stop did not cancel in-flight SMTP context")
		}
		return delivery.Outcome{State: delivery.AcceptedBySMTP, Stage: "data_reply", SMTPCode: 250}
	}}
	svc.Sender = sender
	result, err := svc.SendReview(ctx, owner, review.ID)
	if err != nil || sender.calls != 1 || result.Review.Items[0].State != "accepted_by_smtp" || result.Round.State != store.RoundPaused {
		t.Fatalf("late submission outcome lost: %+v err=%v", result, err)
	}
	if _, err := svc.SendReview(ctx, owner, review.ID); err != nil || sender.calls != 1 {
		t.Fatalf("stopped submission resent: %v calls=%d", err, sender.calls)
	}
}

func TestUnknownDATAOutcomeSurvivesRestartWithoutResend(t *testing.T) {
	db, dir, owner, _ := deliveryFixture(t)
	review, svc := prepareApproved(t, db, owner)
	sender := &fakeSender{fn: func(_ context.Context) delivery.Outcome {
		return delivery.Outcome{State: delivery.Uncertain, Stage: "data_reply"}
	}}
	svc.Sender = sender
	result, err := svc.SendReview(context.Background(), owner, review.ID)
	if err != nil || result.Review.Items[0].State != "uncertain" || result.Round.State != store.RoundPaused || sender.calls != 1 {
		t.Fatalf("uncertain result: %+v err=%v", result, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc.Store = db
	result, err = svc.SendReview(context.Background(), owner, review.ID)
	if err != nil || result.Review.Items[0].State != "uncertain" || sender.calls != 1 {
		t.Fatalf("restart resent uncertain DATA: %+v err=%v calls=%d", result, err, sender.calls)
	}
}

func TestRestartAfterCommittedIntentCannotResend(t *testing.T) {
	db, dir, owner, _ := deliveryFixture(t)
	review, svc := prepareApproved(t, db, owner)
	item := review.Items[0]
	ctx := context.Background()
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "delivery:" + review.ID,
		Intent: "Deliver only the exact approved application review " + review.ID, Outcome: "deliver", ProfileVersion: item.ProfileRevision,
		Scope: store.RoundScope{InputRefs: []string{"delivery_review:" + review.ID}, Resources: []string{"delivery:" + item.ID},
			Operations: []string{store.RoundDeliverApplication}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1}, Deadline: time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := db.ReserveRoundAttempt(ctx, owner, round.ID, store.RoundAttemptInput{RequestKey: item.ID,
		Operation: store.RoundDeliverApplication, ResourceID: "delivery:" + item.ID,
		Cost: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimDeliveryItem(ctx, owner, review.ID, item.ID, round.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	latest, err := db.DeliveryReview(ctx, review.ID)
	if err != nil || latest.Items[0].State != "uncertain" {
		t.Fatalf("interrupted intent not uncertain after restart: %+v %v", latest, err)
	}
	sender := &fakeSender{fn: func(_ context.Context) delivery.Outcome { return delivery.Outcome{State: delivery.AcceptedBySMTP} }}
	svc.Store, svc.Sender = db, sender
	if _, err := svc.SendReview(ctx, owner, review.ID); err != nil || sender.calls != 0 {
		t.Fatalf("restart resent committed intent: %v calls=%d", err, sender.calls)
	}
}

func TestStopBeforeDispatchDoesNotCallSender(t *testing.T) {
	db, _, owner, _ := deliveryFixture(t)
	defer db.Close()
	review, svc := prepareApproved(t, db, owner)
	item := review.Items[0]
	ctx := context.Background()
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "delivery:" + review.ID,
		Intent: "Deliver only the exact approved application review " + review.ID, Outcome: "deliver", ProfileVersion: item.ProfileRevision,
		Scope: store.RoundScope{InputRefs: []string{"delivery_review:" + review.ID}, Resources: []string{"delivery:" + item.ID},
			Operations: []string{store.RoundDeliverApplication}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1}, Deadline: time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActivateRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StopRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{fn: func(_ context.Context) delivery.Outcome { return delivery.Outcome{State: delivery.AcceptedBySMTP} }}
	svc.Sender = sender
	result, err := svc.SendReview(ctx, owner, review.ID)
	if err != nil || sender.calls != 0 || result.Round.State != store.RoundPaused || result.Review.Items[0].State != "prepared" {
		t.Fatalf("stopped round dispatched SMTP: %+v err=%v calls=%d", result, err, sender.calls)
	}
}
