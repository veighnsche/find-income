package agency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type readyRecommendationPackSources struct{}

func (readyRecommendationPackSources) LoadPackSources(context.Context) ([]applicationpacks.Source, error) {
	return []applicationpacks.Source{{ID: "cv", Approved: true}, {ID: "review", Approved: true}, {ID: "history", Approved: true}}, nil
}

func recommendationFixture(t *testing.T, requests int64, ownerSelected bool) (*store.Store, store.Round, store.Opportunity, report) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Fixture Employer", Website: "https://fixture.example/careers"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", Stage: "new", WorkPattern: "remote", LocationText: "Brussels",
		SourceURL: "https://jobs.lever.co/fixture/platform", OriginalText: "Build Go services in Brussels with PostgreSQL."})
	if err != nil {
		t.Fatal(err)
	}
	if ownerSelected {
		if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{RequestKey: "select-platform", ExpectedOpportunityRevision: opportunity.Revision, Decision: "selected"}); err != nil {
			t.Fatal(err)
		}
	}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "recommendation-fixture", Intent: "Find and prepare sourced platform work", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active"}, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: requests, Items: 1, Tools: 1, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail := report{AssessedSources: []assessedSource{{OpportunityID: opportunity.ID, SourceOpeningID: "saved-source", EvidenceSummary: "assessment unavailable in isolated recommendation fixture"}}, Opportunities: 1}
	return db, round, opportunity, detail
}

func recommendationDecisions(t *testing.T, db *store.Store, wantedCapability string) (jevservice.Service, *atomic.Int32, func()) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var wire struct {
			State struct {
				Kind    string `json:"kind"`
				Sources []struct {
					ID      string `json:"id"`
					Excerpt string `json:"excerpt"`
				} `json:"sources"`
				Candidates []struct {
					ID           string `json:"id"`
					CapabilityID string `json:"capability_id"`
				} `json:"candidates"`
			} `json:"state"`
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil || wire.State.Kind != string(jev.DecisionNextOutcome) {
			t.Errorf("invalid saved recommendation request")
			return
		}
		foundProfile, foundSelection := false, false
		for _, source := range wire.State.Sources {
			if strings.HasPrefix(source.ID, "profile:") && strings.Contains(source.Excerpt, "roleCriteria") {
				foundProfile = true
			}
			if strings.HasPrefix(source.ID, "opportunity:") && strings.Contains(source.Excerpt, "owner decision=selected") {
				foundSelection = true
			}
		}
		if !foundProfile || wantedCapability == "home_prepare" && !foundSelection {
			t.Errorf("profile or attributed owner selection missing: %+v", wire.State.Sources)
			return
		}
		chosen := "__unresolved__"
		for _, candidate := range wire.State.Candidates {
			if candidate.CapabilityID == wantedCapability {
				chosen = candidate.ID
			}
		}
		probabilities := map[string]float64{}
		for option := range wire.Questions["selected_candidate"].Criteria {
			probabilities[option] = 0
		}
		if _, ok := probabilities[chosen]; !ok {
			t.Errorf("requested capability %s absent", wantedCapability)
			return
		}
		probabilities[chosen] = 1
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"selected_candidate": map[string]any{"type": "choice", "choice": chosen, "confidence": 0.8, "probabilities": probabilities}}, "usage": map[string]any{"input_tokens": 5, "output_tokens": 1}})
	}))
	t.Setenv(jev.CredentialEnvironmentVariable, "fixture-only")
	cfg := jev.DefaultConfig()
	cfg.Enabled, cfg.Endpoint, cfg.Timeout, cfg.MaxAttempts = true, server.URL+"/v1/systemone", time.Second, 1
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return jevservice.Service{Store: db, Client: client}, calls, server.Close
}

func TestHomeRecommendationJevChoiceChangesExactSupportedAction(t *testing.T) {
	for _, wanted := range []struct{ capability, action, kind string }{
		{"home_discover", "discover", "campaign"},
		{"home_prepare", "prepare", "opportunity"},
	} {
		t.Run(wanted.action, func(t *testing.T) {
			db, round, opportunity, detail := recommendationFixture(t, 2, true)
			decisions, calls, closeServer := recommendationDecisions(t, db, wanted.capability)
			defer closeServer()
			engine := &Engine{Store: db, Decisions: decisions, Runtime: &inertRuntime{}, PackSources: readyRecommendationPackSources{}}
			advice := engine.computeHomeRecommendation(context.Background(), round, detail)
			if advice.Status != "selected" || advice.Action != wanted.action || advice.Target == nil || advice.Target.Kind != wanted.kind || advice.DecisionAttemptID == "" || advice.DecisionInputSHA256 == "" || advice.Reason == "" || calls.Load() != 1 {
				t.Fatalf("Jev action not retained: %+v calls=%d", advice, calls.Load())
			}
			if wanted.action == "prepare" && (advice.Target.ID != opportunity.ID || advice.Target.Revision != opportunity.Revision || advice.Target.OwnerDecisionRevision == 0) {
				t.Fatalf("prepare target lost owner currentness: %+v", advice.Target)
			}
			current, err := db.Round(context.Background(), round.ID)
			if err != nil || current.Used.Requests != 1 {
				t.Fatalf("recommendation did not consume exactly one in-round request: %+v %v", current.Used, err)
			}
		})
	}
}

func TestHomeRecommendationReusesSavedChoiceAfterStopBeforeReport(t *testing.T) {
	ctx := context.Background()
	db, round, _, detail := recommendationFixture(t, 1, true)
	base, calls, closeServer := recommendationDecisions(t, db, "home_prepare")
	defer closeServer()
	interrupted := &interruptDiscoveryDecision{base: base, trigger: func(input jev.DecisionInput) bool {
		return len(input.Candidates) > 0 && input.Candidates[0].CapabilityID == "home_discover"
	}}
	interrupted.stop = func(_ context.Context, roundID string) {
		if _, _, err := db.StopRound(context.Background(), round.Actor, roundID); err != nil {
			t.Errorf("stop recommendation: %v", err)
			return
		}
		if _, err := db.PauseStoppedRound(context.Background(), round.Actor, roundID); err != nil {
			t.Errorf("pause recommendation: %v", err)
		}
	}
	engine := &Engine{Store: db, Decisions: interrupted, Runtime: &inertRuntime{}, PackSources: readyRecommendationPackSources{}}
	first := engine.computeHomeRecommendation(ctx, round, detail)
	if first.Status != "selected" || calls.Load() != 1 {
		t.Fatalf("choice before stop not captured: %+v calls=%d", first, calls.Load())
	}
	paused, err := db.Round(ctx, round.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("round not paused: %+v %v", paused, err)
	}
	if _, err := db.ResumeRound(ctx, round.Actor, round.ID, paused.Generation); err != nil {
		t.Fatal(err)
	}
	resumed, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := engine.computeHomeRecommendation(ctx, resumed, detail)
	if second.Status != "selected" || second.Action != "prepare" || second.DecisionAttemptID != first.DecisionAttemptID || second.DecisionInputSHA256 != first.DecisionInputSHA256 || calls.Load() != 1 || interrupted.calls.Load() != 1 {
		t.Fatalf("saved recommendation was repeated or lost: first=%+v second=%+v calls=%d", first, second, calls.Load())
	}
	detail.Recommendation = second
	engine.finish(ctx, round.ID, round.Actor, "sourced_opportunity_assessed", true, detail)
	finished, err := db.Round(ctx, round.ID)
	if err != nil || finished.State != store.RoundCompleted || !strings.Contains(string(finished.Report), `"action":"prepare"`) {
		t.Fatalf("saved recommendation not published with source report: %+v %v", finished, err)
	}
}

func TestHomeRecommendationExcludesUnselectedOrStalePrepareTarget(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "unselected", true: "stale"}[stale], func(t *testing.T) {
			db, round, opportunity, detail := recommendationFixture(t, 2, stale)
			if stale {
				name := "Updated Platform Engineer"
				if _, _, err := db.PatchOpportunity(context.Background(), round.Actor, opportunity.ID, store.OpportunityPatch{ExpectedRevision: opportunity.Revision, Title: &name}); err != nil {
					t.Fatal(err)
				}
			}
			decisions, calls, closeServer := recommendationDecisions(t, db, "home_discover")
			defer closeServer()
			engine := &Engine{Store: db, Decisions: decisions, Runtime: &inertRuntime{}, PackSources: readyRecommendationPackSources{}}
			advice := engine.computeHomeRecommendation(context.Background(), round, detail)
			if stale {
				if advice.Status != "selected" || advice.Action != "discover" {
					t.Fatalf("stale owner selection influenced action: %+v", advice)
				}
			} else if advice.Status != "selected" || advice.Action != "discover" {
				t.Fatalf("unselected advice: %+v", advice)
			}
			if calls.Load() != 1 {
				t.Fatalf("decision calls=%d", calls.Load())
			}
			attempts, err := db.JevAttemptsForRound(context.Background(), round.ID)
			if err != nil || len(attempts) != 1 || strings.Contains(string(attempts[0].CandidateSetJSON), "home_prepare") {
				t.Fatalf("unselected/stale prepare option sent to Jev: %+v %v", attempts, err)
			}
		})
	}
}

func TestHomeRecommendationPreservesPriorOwnerDecisionForAssessedRole(t *testing.T) {
	for _, scenario := range []struct {
		name, decision, binding string
		patch                   bool
	}{
		{name: "dismissed", decision: "dismissed", binding: "current"},
		{name: "older_selection", decision: "selected", binding: "stale", patch: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			db, round, opportunity, detail := recommendationFixture(t, 2, false)
			ownerDecision, _, err := db.SetOwnerOpportunityDecision(ctx, round.Actor, opportunity.ID, store.OwnerDecisionInput{
				RequestKey: "prior-" + scenario.name, ExpectedOpportunityRevision: opportunity.Revision, Decision: scenario.decision})
			if err != nil {
				t.Fatal(err)
			}
			if scenario.patch {
				newTitle := "Revised Platform Engineer"
				if _, _, err := db.PatchOpportunity(ctx, round.Actor, opportunity.ID, store.OpportunityPatch{
					ExpectedRevision: opportunity.Revision, Title: &newTitle}); err != nil {
					t.Fatal(err)
				}
			}
			decisions, calls, closeServer := recommendationDecisions(t, db, "home_discover")
			defer closeServer()
			engine := &Engine{Store: db, Decisions: decisions, Runtime: &inertRuntime{}, PackSources: readyRecommendationPackSources{}}
			detail.Recommendation = engine.computeHomeRecommendation(ctx, round, detail)
			if detail.Recommendation.Status != "selected" || detail.Recommendation.Action != "discover" || calls.Load() != 1 {
				t.Fatalf("prior decision blocked supported advice: %+v calls=%d", detail.Recommendation, calls.Load())
			}
			var opportunityRef *recommendationSourceRef
			for i := range detail.Recommendation.SourceRefs {
				ref := &detail.Recommendation.SourceRefs[i]
				if ref.Kind == "current_opportunity_state" && ref.ID == "opportunity:"+opportunity.ID {
					opportunityRef = ref
				}
			}
			if opportunityRef == nil || opportunityRef.OwnerDecisionRevision != ownerDecision.Revision {
				t.Fatalf("prior owner decision missing from saved reference: %+v", detail.Recommendation.SourceRefs)
			}
			attempts, err := db.JevAttemptsForRound(ctx, round.ID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("decision attempt unavailable: %+v %v", attempts, err)
			}
			var sources []jev.DecisionSource
			if err := json.Unmarshal(attempts[0].SourceRefsJSON, &sources); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, source := range sources {
				if source.ID == "opportunity:"+opportunity.ID &&
					strings.Contains(source.Excerpt, "owner decision="+scenario.decision) &&
					strings.Contains(source.Excerpt, "binding="+scenario.binding) {
					found = true
				}
			}
			if !found || strings.Contains(string(attempts[0].CandidateSetJSON), "home_prepare") {
				t.Fatalf("prior decision attribution or prepare exclusion failed: sources=%+v candidates=%s", sources, attempts[0].CandidateSetJSON)
			}
			engine.finish(ctx, round.ID, round.Actor, "sourced_opportunity_assessed", true, detail)
			completed, err := db.Round(ctx, round.ID)
			if err != nil {
				t.Fatal(err)
			}
			if verdict := ReadHomeRecommendationCurrentness(ctx, db, completed); verdict.Status != "current" {
				t.Fatalf("unchanged prior owner decision staled saved advice: %+v", verdict)
			}
		})
	}
}

func TestHomeRecommendationNoAllowancePreservesUsefulReport(t *testing.T) {
	db, round, _, detail := recommendationFixture(t, 1, false)
	ctx := context.Background()
	actor := round.Actor
	cost, _ := store.RoundOperationCost(store.RoundJevRequest)
	attempt, _, err := db.ReserveRoundAttempt(ctx, actor, round.ID, store.RoundAttemptInput{RequestKey: "prior-work", Operation: store.RoundJevRequest, ResourceID: "campaign:active", Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, actor, round.ID, attempt.ID, true, json.RawMessage(`{"work":true}`), ""); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Store: db}
	detail.Recommendation = engine.computeHomeRecommendation(ctx, round, detail)
	engine.finish(ctx, round.ID, actor, "sourced_opportunity_assessed", true, detail)
	finished, err := db.Round(ctx, round.ID)
	if err != nil || finished.State != store.RoundCompleted || detail.Recommendation.Status != "unavailable" || detail.Recommendation.Code != "recommendation_allowance_or_scope_unavailable" || !strings.Contains(string(finished.Report), `"assessedSources"`) {
		t.Fatalf("useful result lost when advice unavailable: %+v report=%s err=%v", detail.Recommendation, finished.Report, err)
	}
}

func TestHomeRecommendationAbstentionPreservesUsefulReport(t *testing.T) {
	ctx := context.Background()
	db, round, _, detail := recommendationFixture(t, 2, false)
	decisions, calls, closeServer := recommendationDecisions(t, db, "no_supported_choice")
	defer closeServer()
	engine := &Engine{Store: db, Decisions: decisions}
	detail.Recommendation = engine.computeHomeRecommendation(ctx, round, detail)
	if detail.Recommendation.Status != "unresolved" || detail.Recommendation.Code != "jev_abstained" || calls.Load() != 1 {
		t.Fatalf("abstention was not recorded: %+v calls=%d", detail.Recommendation, calls.Load())
	}
	engine.finish(ctx, round.ID, round.Actor, "sourced_opportunity_assessed", true, detail)
	finished, err := db.Round(ctx, round.ID)
	if err != nil || finished.State != store.RoundCompleted || !strings.Contains(string(finished.Report), `"assessedSources"`) || !strings.Contains(string(finished.Report), `"status":"unresolved"`) {
		t.Fatalf("assessment lost after abstention: %+v %v", finished, err)
	}
}

func TestHomeRecommendationBoundsSelectedRoleInventoryBeforeJev(t *testing.T) {
	ctx := context.Background()
	db, round, first, detail := recommendationFixture(t, 2, true)
	for i := 0; i < 8; i++ {
		opportunity, _, err := db.CreateOpportunity(ctx, round.Actor, store.OpportunityInput{CompanyID: first.CompanyID,
			Title: fmt.Sprintf("Platform Engineer %d", i), Kind: "employment", Stage: "new", WorkPattern: "remote", LocationText: "Brussels",
			SourceURL: fmt.Sprintf("https://jobs.lever.co/fixture/role-%d", i), OriginalText: "Build Go services in Brussels."})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.SetOwnerOpportunityDecision(ctx, round.Actor, opportunity.ID, store.OwnerDecisionInput{
			RequestKey: fmt.Sprintf("select-%d", i), ExpectedOpportunityRevision: opportunity.Revision, Decision: "selected"}); err != nil {
			t.Fatal(err)
		}
	}
	engine := &Engine{Store: db}
	advice := engine.computeHomeRecommendation(ctx, round, detail)
	if advice.Status != "unavailable" || advice.Code != "recommendation_context_unbounded" || !advice.OmittedOpportunities {
		t.Fatalf("unbounded selected roles silently dropped: %+v", advice)
	}
	if _, err := db.RoundAttemptForRequest(ctx, round.ID, homeRecommendationRequestKey+"/0"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unbounded recommendation charged Jev: %v", err)
	}
}

func TestHomeRecommendationCurrentnessIsReadOnlyAndDetectsChangedTarget(t *testing.T) {
	ctx := context.Background()
	db, round, opportunity, detail := recommendationFixture(t, 2, true)
	decisions, _, closeServer := recommendationDecisions(t, db, "home_prepare")
	defer closeServer()
	engine := &Engine{Store: db, Decisions: decisions, Runtime: &inertRuntime{}, PackSources: readyRecommendationPackSources{}}
	detail.Recommendation = engine.computeHomeRecommendation(ctx, round, detail)
	if detail.Recommendation.Status != "selected" {
		t.Fatalf("advice unavailable: %+v", detail.Recommendation)
	}
	engine.finish(ctx, round.ID, round.Actor, "sourced_opportunity_assessed", true, detail)
	completed, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeReport := append([]byte(nil), completed.Report...)
	beforeUsed := completed.Used
	current := ReadHomeRecommendationCurrentness(ctx, db, completed)
	if current.Status != "current" || current.Code != "verified" || current.CheckedAt == "" {
		t.Fatalf("fresh advice not verified: %+v round=%+v advice=%+v", current, completed, detail.Recommendation)
	}
	again, err := db.Round(ctx, round.ID)
	if err != nil || !bytes.Equal(again.Report, beforeReport) || again.Used != beforeUsed {
		t.Fatalf("currentness read mutated saved round: %+v %v", again, err)
	}
	newTitle := "Changed Platform Engineer"
	if _, _, err := db.PatchOpportunity(ctx, round.Actor, opportunity.ID, store.OpportunityPatch{ExpectedRevision: opportunity.Revision, Title: &newTitle}); err != nil {
		t.Fatal(err)
	}
	stale := ReadHomeRecommendationCurrentness(ctx, db, completed)
	if stale.Status != "stale" || stale.Code != "opportunity_changed" {
		t.Fatalf("changed role still presented as current: %+v", stale)
	}
}

func TestHomeRecommendationCurrentnessDetectsNewOwnerSelection(t *testing.T) {
	ctx := context.Background()
	db, round, first, detail := recommendationFixture(t, 2, true)
	decisions, _, closeServer := recommendationDecisions(t, db, "home_prepare")
	defer closeServer()
	engine := &Engine{Store: db, Decisions: decisions, Runtime: &inertRuntime{}, PackSources: readyRecommendationPackSources{}}
	detail.Recommendation = engine.computeHomeRecommendation(ctx, round, detail)
	if detail.Recommendation.Status != "selected" {
		t.Fatalf("advice unavailable: %+v", detail.Recommendation)
	}
	engine.finish(ctx, round.ID, round.Actor, "sourced_opportunity_assessed", true, detail)
	completed, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := db.CreateOpportunity(ctx, round.Actor, store.OpportunityInput{CompanyID: first.CompanyID,
		Title: "Site Reliability Engineer", Kind: "employment", Stage: "new", WorkPattern: "remote", LocationText: "Brussels",
		SourceURL: "https://jobs.lever.co/fixture/sre", OriginalText: "Operate Go services in Brussels."})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, round.Actor, other.ID, store.OwnerDecisionInput{
		RequestKey: "select-sre", ExpectedOpportunityRevision: other.Revision, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	if verdict := ReadHomeRecommendationCurrentness(ctx, db, completed); verdict.Status != "stale" || verdict.Code != "selected_roles_changed" {
		t.Fatalf("new current owner selection did not stale saved choice: %+v", verdict)
	}
}
