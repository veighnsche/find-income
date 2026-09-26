package agency

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func outcomeAdviceRound(t *testing.T, requests int64, campaignScope bool) (*store.Store, store.Round) {
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
	resources := []string{"profile:current"}
	if campaignScope {
		resources = append(resources, "campaign:active")
	}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "input-advice", Intent: "Apply supplied owner context", Outcome: "process_input", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: resources, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: requests, Items: 1, Tools: 1, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	return db, round
}

func inputAdviceFacts(round store.Round) outcomeRecommendationFacts {
	return outcomeRecommendationFacts{Outcome: "process_input", Code: "input_applied", ResultID: round.ID, AppliedChanges: 1, UnresolvedCount: 1}
}

func TestInputOutcomeSavesOneChargedAdviceAndPureCurrentness(t *testing.T) {
	ctx := context.Background()
	db, round := outcomeAdviceRound(t, 1, true)
	decisions, calls, closeServer := recommendationDecisions(t, db, "home_review_result")
	defer closeServer()
	engine := &Engine{Store: db, Decisions: decisions}
	detail := inputReport{Code: "input_applied", AppliedChanges: []store.RoundHistoryEvent{{Operation: store.RoundCorrectPreferences}}, Unresolved: []string{"PRIVATE_OWNER_TEXT_NEVER_TO_JEV"}}
	engine.finishInput(ctx, round.ID, round.Actor, detail)
	finished, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saved inputReport
	if json.Unmarshal(finished.Report, &saved) != nil || finished.State != store.RoundCompleted || saved.Recommendation == nil ||
		saved.Recommendation.Status != "selected" || saved.Recommendation.Action != "review_result" || len(saved.AppliedChanges) != 1 || len(saved.Unresolved) != 1 || calls.Load() != 1 || finished.Used.Requests != 1 {
		t.Fatalf("input result or bounded advice lost: report=%s used=%+v calls=%d", finished.Report, finished.Used, calls.Load())
	}
	attempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil || len(attempts) != 1 || bytes.Contains(attempts[0].LogicalRequestJSON, []byte("PRIVATE_OWNER_TEXT_NEVER_TO_JEV")) {
		t.Fatalf("private input text entered advice request: %+v %v", attempts, err)
	}
	before := append([]byte(nil), finished.Report...)
	for i := 0; i < 2; i++ {
		verdict := ReadHomeRecommendationCurrentness(ctx, db, finished)
		if verdict.Status != "current" || verdict.Code != "verified" || calls.Load() != 1 {
			t.Fatalf("idle currentness dispatched work or rejected saved advice: %+v calls=%d", verdict, calls.Load())
		}
	}
	again, err := db.Round(ctx, round.ID)
	if err != nil || !bytes.Equal(again.Report, before) || again.Used != finished.Used {
		t.Fatalf("read changed saved outcome: %s %v", again.Report, err)
	}
}

func TestInputOutcomeMissingScopeOrAllowanceKeepsUsefulResult(t *testing.T) {
	for _, scenario := range []struct {
		name, code    string
		requests      int64
		campaignScope bool
	}{
		{name: "no_scope", code: "recommendation_allowance_or_scope_unavailable", requests: 1},
		{name: "no_allowance", code: "recommendation_allowance_or_scope_unavailable", campaignScope: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, round := outcomeAdviceRound(t, scenario.requests, scenario.campaignScope)
			engine := &Engine{Store: db}
			engine.finishInput(context.Background(), round.ID, round.Actor, inputReport{Code: "input_applied", AppliedChanges: []store.RoundHistoryEvent{{Operation: store.RoundCorrectPreferences}}})
			finished, err := db.Round(context.Background(), round.ID)
			var saved inputReport
			if err != nil || json.Unmarshal(finished.Report, &saved) != nil || len(saved.AppliedChanges) != 1 || saved.Recommendation == nil || saved.Recommendation.Status != "unavailable" || saved.Recommendation.Code != scenario.code {
				t.Fatalf("useful input result lost: %s %v", finished.Report, err)
			}
			if _, err := db.RoundAttemptForRequest(context.Background(), round.ID, homeRecommendationRequestKey+"/0"); err != store.ErrNotFound {
				t.Fatalf("unscoped or unbudgeted advice charged Jev: %v", err)
			}
		})
	}
}

type uncapturedOutcomeDecision struct{}

func (uncapturedOutcomeDecision) RunDecision(_ context.Context, _ jevservice.Binding, input jev.DecisionInput) (jev.DecisionResult, error) {
	for _, candidate := range input.Candidates {
		if candidate.CapabilityID == "home_review_result" {
			return jev.DecisionResult{Disposition: jev.DecisionSelected, SelectedID: candidate.ID}, nil
		}
	}
	return jev.DecisionResult{}, store.ErrInvalid
}
func (uncapturedOutcomeDecision) RunScreening(context.Context, jevservice.Binding, jev.ScreeningInput) (jev.ScreeningResult, error) {
	return jev.ScreeningResult{}, store.ErrInvalid
}
func (uncapturedOutcomeDecision) RunOrganisation(context.Context, jevservice.Binding, jev.OrganisationInput) (jev.OrganisationResult, error) {
	return jev.OrganisationResult{}, store.ErrInvalid
}

func TestInputOutcomeMissingJevCaptureNeverPublishesSelectedAdvice(t *testing.T) {
	db, round := outcomeAdviceRound(t, 1, true)
	engine := &Engine{Store: db, Decisions: uncapturedOutcomeDecision{}}
	engine.finishInput(context.Background(), round.ID, round.Actor, inputReport{Code: "input_applied", AppliedChanges: []store.RoundHistoryEvent{{Operation: store.RoundCorrectPreferences}}})
	finished, err := db.Round(context.Background(), round.ID)
	var saved inputReport
	if err != nil || json.Unmarshal(finished.Report, &saved) != nil || saved.Recommendation == nil || saved.Recommendation.Status != "unavailable" || !strings.Contains(string(finished.Report), "appliedChanges") {
		t.Fatalf("missing raw capture was promoted or result lost: %s %v", finished.Report, err)
	}
}

func TestInputOutcomeSavedAdviceStalesWhenProfileChanges(t *testing.T) {
	ctx := context.Background()
	db, round := outcomeAdviceRound(t, 1, true)
	decisions, _, closeServer := recommendationDecisions(t, db, "home_review_result")
	defer closeServer()
	engine := &Engine{Store: db, Decisions: decisions}
	engine.finishInput(ctx, round.ID, round.Actor, inputReport{Code: "input_applied", AppliedChanges: []store.RoundHistoryEvent{{Operation: store.RoundCorrectPreferences}}})
	finished, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.UpdatePreferences(ctx, profile.Version, profile, round.Actor); err != nil {
		t.Fatal(err)
	}
	if verdict := ReadHomeRecommendationCurrentness(ctx, db, finished); verdict.Status != "stale" || verdict.Code != "profile_changed" {
		t.Fatalf("changed profile left selected advice live: %+v", verdict)
	}
}

func TestInputOutcomeReplaysCapturedChoiceAfterStopWithoutAnotherRequest(t *testing.T) {
	ctx := context.Background()
	db, round := outcomeAdviceRound(t, 1, true)
	base, calls, closeServer := recommendationDecisions(t, db, "home_review_result")
	defer closeServer()
	interrupted := &interruptRecommendationDecision{base: base, trigger: func(input jev.DecisionInput) bool {
		return len(input.Candidates) > 0 && input.Candidates[0].CapabilityID == "home_review_result"
	}}
	interrupted.stop = func(_ context.Context, roundID string) {
		if _, _, err := db.StopRound(context.Background(), round.Actor, roundID); err != nil {
			t.Errorf("stop advice: %v", err)
			return
		}
		if _, err := db.PauseStoppedRound(context.Background(), round.Actor, roundID); err != nil {
			t.Errorf("pause advice: %v", err)
		}
	}
	engine := &Engine{Store: db, Decisions: interrupted}
	facts := inputAdviceFacts(round)
	first := engine.computeOutcomeRecommendation(ctx, round, facts)
	if first.Status != "selected" || first.Action != "review_result" || calls.Load() != 1 {
		t.Fatalf("captured choice missing before Stop: %+v calls=%d", first, calls.Load())
	}
	paused, err := db.Round(ctx, round.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("advice round did not pause: %+v %v", paused, err)
	}
	if _, err := db.ResumeRound(ctx, round.Actor, round.ID, paused.Generation); err != nil {
		t.Fatal(err)
	}
	resumed, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := engine.computeOutcomeRecommendation(ctx, resumed, facts)
	if second.Status != "selected" || second.Action != "review_result" || second.DecisionAttemptID != first.DecisionAttemptID || second.DecisionInputSHA256 != first.DecisionInputSHA256 || calls.Load() != 1 {
		t.Fatalf("saved choice repeated or lost: first=%+v second=%+v calls=%d", first, second, calls.Load())
	}
	detail := inputReport{Code: "input_applied", AppliedChanges: []store.RoundHistoryEvent{{Operation: store.RoundCorrectPreferences}}, Unresolved: []string{"still needs owner detail"}, Recommendation: second}
	encoded, _ := json.Marshal(detail)
	finished, err := db.FinishRound(ctx, round.Actor, round.ID, store.RoundCompleted, detail.Code, "partial", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if verdict := ReadHomeRecommendationCurrentness(ctx, db, finished); verdict.Status != "current" {
		t.Fatalf("replayed result became stale after completion: %+v", verdict)
	}
}
