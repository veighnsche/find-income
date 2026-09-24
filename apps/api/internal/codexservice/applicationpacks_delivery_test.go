package codexservice

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type routeResumeWorker struct{ launches int }

func (*routeResumeWorker) CheckRound(context.Context, string) error { return nil }
func (w *routeResumeWorker) LaunchRound(context.Context, store.Round) error {
	w.launches++
	return nil
}
func (*routeResumeWorker) CancelRound(string) {}

type capturedRouteEvaluator func(jev.Request) jev.Result

func (f capturedRouteEvaluator) Evaluate(_ context.Context, request jev.Request) (jev.Result, error) {
	return f(request), nil
}

func TestCommittedPackResumeUsesCapturedRouteWithoutAnotherProviderCall(t *testing.T) {
	testCapturedRouteResume(t, true)
}

func TestIncompleteCapturedRouteStaysPausedWithoutChargedChecks(t *testing.T) {
	testCapturedRouteResume(t, false)
}

func testCapturedRouteResume(t *testing.T, complete bool) {
	t.Helper()
	ctx := context.Background()
	s, db := testService(t, testConfig())
	owner := store.Actor{Kind: "administrator", ID: "route-owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Harbour"})
	if err != nil {
		t.Fatal(err)
	}
	opening := "Build Go services. Send your application to jobs@harbour.example."
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Backend Engineer",
		Kind: "employment", SourceURL: "https://harbour.example/jobs/1", OriginalText: opening, Stage: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{
		RequestKey: "select-route-role", ExpectedOpportunityRevision: opportunity.Revision, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.WriteAudited(ctx, owner, func(tx *sql.Tx) (store.Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO opportunity_routes
		 (id,opportunity_id,kind,destination_text,source_kind,source_excerpt,observed_at,revision,created_at,updated_at)
		 VALUES('captured-route',?,'direct','jobs@harbour.example','posting',?, ?,1,?,?)`, opportunity.ID,
			"Send your application to jobs@harbour.example.", now, now, now)
		return store.Change{Operation: "fixture.route", EntityKind: "opportunity_route", EntityID: "captured-route"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := db.ListOpportunityRoutes(ctx, opportunity.ID)
	if err != nil || len(routes) != 1 {
		t.Fatalf("route: %+v %v", routes, err)
	}
	route := routes[0]
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "opportunity:" + opportunity.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "captured-route-round", Intent: "Prepare the selected pack",
		Outcome: "prepare", ProfileVersion: profile.Version, Scope: store.RoundScope{Resources: []string{resource, "campaign:active"},
			Operations: []string{store.RoundCodexTurn, store.RoundPrepareApplicationPack, store.RoundJevRequest}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 3, Items: 1, Tools: 4, Turns: 1}, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if round, err = db.ActivateRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "one-pack-turn",
		Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundThread(ctx, round.ID, turn.ID, turn.Generation, "route-fixture-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "route-fixture-thread", "route-fixture-turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.ObserveRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "route-fixture-thread", "route-fixture-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"role": map[string]any{"opportunityId": opportunity.ID,
		"opportunityRevision": opportunity.Revision, "profileRevision": profile.Version, "title": opportunity.Title,
		"company": company.Name, "sourceUrl": opportunity.SourceURL, "description": opportunity.OriginalText},
		"draft": map[string]any{"materialUnknowns": []string{}}})
	source, pdf := []byte("= CV"), append([]byte("%PDF-1.7\n"), make([]byte, 110)...)
	packed, _ := json.Marshal(struct{ Manifest, Source, PDF []byte }{manifest, source, pdf})
	packSHA := sha256.Sum256(packed)
	packResult, _, err := db.ApplyRoundMutation(ctx, agent, round.ID, store.RoundMutationInput{RequestKey: "one-pack",
		Operation: store.RoundPrepareApplicationPack, ResourceID: resource, ExpectedRevision: opportunity.Revision, Capability: capability,
		ApplicationPack: &store.ApplicationPackMutationInput{OpportunityID: opportunity.ID, ExpectedOpportunityRevision: opportunity.Revision,
			ExpectedProfileRevision: profile.Version, ContentSHA256: hex.EncodeToString(packSHA[:]), ManifestJSON: manifest, TypstSource: source, PDF: pdf}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, agent, round.ID, turn.ID, true, json.RawMessage(`{"prepared":true}`), ""); err != nil {
		t.Fatal(err)
	}
	input := jev.DeliveryRouteInput{OpportunityID: opportunity.ID, RouteID: route.ID, Title: opportunity.Title,
		SourceURL: opportunity.SourceURL, OriginalText: opportunity.OriginalText,
		SourceSHA256: store.DeliverySourceHash(opportunity.Title, opportunity.SourceURL, opportunity.OriginalText),
		RouteKind:    route.Kind, Destination: route.DestinationText, RouteSourceKind: route.SourceKind, RouteSourceRef: route.SourceRef,
		RouteExcerpt: route.SourceExcerpt, RouteSHA256: store.DeliveryRouteHash(route), MaxReportedTokens: 2500}
	raw := []byte(`{"model":"synthetic","answers":{"delivery_route":{"type":"choice","choice":"application_mailbox","probabilities":{"application_mailbox":1,"other_contact":0,"unresolved":0},"confidence":1}},"usage":{"input_tokens":12,"output_tokens":4}}`)
	result, err := jev.AssessDeliveryRoute(ctx, capturedRouteEvaluator(func(_ jev.Request) jev.Result {
		return jev.Result{RequestedModel: "synthetic", ReturnedModel: "synthetic", RawResponse: raw,
			Usage: jev.Usage{InputTokens: 12, OutputTokens: 4}, Answers: map[string]jev.Answer{
				"delivery_route": {Type: "choice", Choice: &jev.ChoiceAnswer{Choice: "application_mailbox",
					Probabilities: map[string]float64{"application_mailbox": 1, "other_contact": 0, "unresolved": 0}, Confidence: 1}}}}
	}), input)
	if err != nil {
		t.Fatal(err)
	}
	cost, _ := store.RoundOperationCost(store.RoundJevRequest)
	stage, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "original-pack-key/delivery-route/" + route.ID + "/0",
		Operation: store.RoundJevRequest, ResourceID: resource, Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, stage.ID); err != nil {
		t.Fatal(err)
	}
	logical := []byte(result.RequestSnapshot)
	logicalSHA := sha256.Sum256(logical)
	attempt, err := db.BeginJevAttempt(ctx, store.JevAttemptStart{RoundID: round.ID, RoundAttemptID: stage.ID, Purpose: "delivery_route",
		InputSHA256: hex.EncodeToString(logicalSHA[:]), SourceRefsJSON: []byte(`[]`), CandidateSetJSON: []byte(`[]`),
		ProfileVersion: profile.Version, RubricVersion: "delivery-route-v1", RequestedModel: "synthetic", LogicalRequestJSON: logical,
		TransportRequestBytes: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	inTokens, outTokens := int64(12), int64(4)
	if complete {
		if _, err := db.FinishJevAttempt(ctx, store.JevAttemptFinish{ID: attempt.ID, Status: "succeeded", ReturnedModel: "synthetic",
			RawResponseBytes: raw, InputTokens: &inTokens, OutputTokens: &outTokens}); err != nil {
			t.Fatal(err)
		}
	}
	stopping, _, err := db.StopRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := db.PauseStoppedRound(ctx, owner, stopping.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := paused.Used
	worker := &routeResumeWorker{}
	coordinator := &rounds.Service{Store: db, Readiness: worker, Worker: worker, Reconciler: s}
	if !complete {
		for i := 0; i < 2; i++ {
			if _, err := coordinator.Resume(ctx, owner, paused.ID); !errors.Is(err, store.ErrUncertain) {
				t.Fatalf("incomplete capture resumed: %v", err)
			}
			latest, err := db.Round(ctx, round.ID)
			if err != nil || latest.Used != before || latest.State != store.RoundPaused || worker.launches != 0 {
				t.Fatalf("futile resume charged or launched work: %+v err=%v", latest, err)
			}
		}
		return
	}
	resumed, err := coordinator.Resume(ctx, owner, paused.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Used != before || worker.launches != 1 {
		t.Fatalf("local recovery charged or relaunched unexpectedly: before=%+v after=%+v launches=%d", before, resumed.Used, worker.launches)
	}
	status, err := s.CompletePackDeliveryRoute(ctx, round.ID, packResult.EntityID)
	if err != nil || status != "application_mailbox" {
		t.Fatalf("captured route not recovered: status=%s err=%v", status, err)
	}
	after, err := db.Round(ctx, round.ID)
	if err != nil || after.Used != before {
		t.Fatalf("completion made a new request or turn: before=%+v after=%+v err=%v", before, after.Used, err)
	}
	assessment, err := db.CurrentDeliveryRouteAssessment(ctx, route, opportunity)
	if err != nil || assessment.Choice != "application_mailbox" || assessment.JevAttemptID != attempt.ID {
		t.Fatalf("captured verdict not linked: %+v %v", assessment, err)
	}
	completed, err := db.JevAttempt(ctx, attempt.ID)
	if err != nil || completed.InputTokens == nil || *completed.InputTokens != 12 || completed.OutputTokens == nil || *completed.OutputTokens != 4 ||
		string(completed.RawResponseBytes) != string(raw) {
		t.Fatalf("original provider evidence changed: %+v %v", completed, err)
	}
}
