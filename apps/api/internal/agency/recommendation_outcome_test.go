package agency

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/deliveryservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/interviewprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
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

func TestDeliveryAdapterRunsOneBoundedChargedDecision(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "delivery-owner"}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "delivery-advice", Intent: "Review saved submission states", Outcome: "deliver", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{InputRefs: []string{"delivery_review:bounded-review"}, Resources: []string{"campaign:active"}, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	decisions, calls, closeServer := recommendationDecisions(t, db, "home_review_result")
	defer closeServer()
	engine := &Engine{Store: db, Decisions: decisions}
	data := engine.RecommendDelivery(ctx, round, deliveryservice.DeliveryAdviceFacts{ReviewID: "bounded-review", Recorded: 2, Failed: 1})
	var advice homeRecommendation
	if json.Unmarshal(data, &advice) != nil || advice.Status != "selected" || advice.Action != "review_result" || calls.Load() != 1 {
		t.Fatalf("bounded delivery choice unavailable: %s calls=%d", data, calls.Load())
	}
	attempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil || len(attempts) != 1 || !bytes.Contains(attempts[0].LogicalRequestJSON, []byte("bounded-review")) ||
		bytes.Contains(attempts[0].LogicalRequestJSON, []byte("recipient")) || bytes.Contains(attempts[0].LogicalRequestJSON, []byte("messageBody")) {
		t.Fatalf("delivery decision evidence exceeded bounded facts: %+v %v", attempts, err)
	}
	charged, err := db.Round(ctx, round.ID)
	if err != nil || charged.Used.Requests != 1 {
		t.Fatalf("delivery decision did not consume one authorized request: %+v %v", charged, err)
	}
}

func TestDeliveryAdapterWithoutDecisionProviderReturnsUnavailableWithoutCharge(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "delivery-owner"}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "delivery-offline-advice", Intent: "Review saved submission states", Outcome: "deliver", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{InputRefs: []string{"delivery_review:bounded-review"}, Resources: []string{"campaign:active"}, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	data := (&Engine{Store: db}).RecommendDelivery(ctx, round, deliveryservice.DeliveryAdviceFacts{ReviewID: "bounded-review", Recorded: 1})
	var advice homeRecommendation
	if json.Unmarshal(data, &advice) != nil || advice.Status != "unavailable" || advice.Code != "recommendation_provider_unavailable" {
		t.Fatalf("missing provider panicked or selected without capture: %s", data)
	}
	if _, err := db.RoundAttemptForRequest(ctx, round.ID, homeRecommendationRequestKey+"/0"); err != store.ErrNotFound {
		t.Fatalf("disabled provider charged a request: %v", err)
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

func TestInterviewPrepareSavesBoundedAdviceWithoutSendingOwnerContext(t *testing.T) {
	ctx := context.Background()
	db, owner, interview, _ := agencyInterviewFixture(t)
	runtime := &interviewFixtureRuntime{db: db, commission: interview}
	decisions, calls, closeServer := recommendationDecisions(t, db, "home_review_interview")
	defer closeServer()
	engine := &Engine{Store: db, Runtime: runtime, InterviewSources: interviewFixtureSources{}, InterviewFocus: interviewUnavailableFocus{}, Decisions: decisions, Context: ctx}
	input := agencyInterviewRound(interview)
	input.Limits.Requests++
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	round, _, err := service.Start(ctx, owner, input)
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := engine.WaitRoundStopped(wait, round.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saved interviewOutcome
	if json.Unmarshal(finished.Report, &saved) != nil || !saved.BriefSaved || saved.Recommendation == nil || saved.Recommendation.Status != "selected" ||
		saved.Recommendation.Action != "review_interview" || calls.Load() != 1 {
		t.Fatalf("sourced interview brief or advice missing: %s calls=%d", finished.Report, calls.Load())
	}
	attempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.RoundAttemptID == saved.Recommendation.DecisionAttemptID && bytes.Contains(attempt.LogicalRequestJSON, []byte(interview.Context)) {
			t.Fatalf("owner invitation leaked into next-action request")
		}
	}
	if verdict := ReadHomeRecommendationCurrentness(ctx, db, finished); verdict.Status != "current" {
		t.Fatalf("fresh interview advice unavailable: %+v", verdict)
	}
}

func TestInterviewDebriefSavesBoundedAdviceWithoutSendingOwnerNotes(t *testing.T) {
	ctx := context.Background()
	db, owner, interview, _ := agencyInterviewFixture(t)
	runtime := &interviewFixtureRuntime{db: db, commission: interview}
	preparer := &Engine{Store: db, Runtime: runtime, InterviewSources: interviewFixtureSources{}, InterviewFocus: interviewUnavailableFocus{}, Context: ctx}
	service := &rounds.Service{Store: db, Readiness: preparer, Worker: preparer}
	preparation, _, err := service.Start(ctx, owner, agencyInterviewRound(interview))
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := preparer.WaitRoundStopped(wait, preparation.ID); err != nil {
		t.Fatal(err)
	}
	notes := "PRIVATE_OWNER_NOTES: We discussed the Go service."
	debrief, _, err := db.CommissionInterviewDebrief(ctx, owner, store.InterviewDebriefCommissionInput{RequestKey: "bounded-debrief", InterviewID: interview.ID, Notes: notes})
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "bounded-debrief-round", Intent: "Save cited owner debrief", Outcome: "interview_debrief", ProfileVersion: interview.ProfileVersion,
		Scope: store.RoundScope{InputRefs: []string{"debrief:" + debrief.ID}, Resources: []string{"interview:" + interview.ID, "campaign:active"},
			Operations: []string{store.RoundCodexTurn, store.RoundInterviewDebriefSave, store.RoundJevRequest}, Delegates: []string{"codex-runner"}},
		Limits: store.RoundAllowance{Requests: 2, Items: 1, Tools: 2, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindInterviewRound(ctx, owner, debrief.ID, round.ID, true); err != nil {
		t.Fatal(err)
	}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "debrief-turn", Operation: store.RoundCodexTurn,
		ResourceID: "interview:" + interview.ID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
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
	prepared, err := interviewprep.ValidateDebrief(interviewprep.DebriefInput{InterviewID: interview.ID, OwnerNotes: notes,
		Observations: []interviewprep.DebriefObservation{{Kind: "discussed", Detail: interviewprep.CitedText{Text: "The owner discussed the Go service.",
			Citations: []applicationpacks.Citation{interviewprep.OwnerNoteCitation("We discussed the Go service.")}}}}, Unknowns: []string{"No employer decision was supplied."}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(prepared)
	if _, _, err := db.ApplyRoundMutation(ctx, agent, round.ID, store.RoundMutationInput{RequestKey: "save-debrief", Operation: store.RoundInterviewDebriefSave,
		ResourceID: "interview:" + interview.ID, ExpectedRevision: 1, InterviewDebrief: &store.InterviewDebriefMutation{
			DebriefID: debrief.ID, InterviewID: interview.ID, DebriefJSON: encoded}, Capability: capability}); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundThread(ctx, round.ID, turn.ID, turn.Generation, "debrief-advice-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "debrief-advice-thread", "debrief-advice-turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.ObserveRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "debrief-advice-thread", "debrief-advice-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, agent, round.ID, turn.ID, true, json.RawMessage(`{"saved":true}`), ""); err != nil {
		t.Fatal(err)
	}
	decisions, calls, closeServer := recommendationDecisions(t, db, "home_review_debrief")
	defer closeServer()
	engine := &Engine{Store: db, Decisions: decisions}
	engine.finishInterview(ctx, round, interviewOutcome{Code: "debrief_saved", InterviewID: interview.ID, DebriefID: debrief.ID, BriefSaved: true,
		Unknowns: []string{"No employer decision was supplied."}})
	finished, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saved interviewOutcome
	if json.Unmarshal(finished.Report, &saved) != nil || saved.Recommendation == nil || saved.Recommendation.Status != "selected" || saved.Recommendation.Action != "review_debrief" || calls.Load() != 1 {
		t.Fatalf("debrief result or bounded advice missing: %s calls=%d", finished.Report, calls.Load())
	}
	attempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.RoundAttemptID == saved.Recommendation.DecisionAttemptID && bytes.Contains(attempt.LogicalRequestJSON, []byte(notes)) {
			t.Fatal("owner debrief notes entered next-action request")
		}
	}
	if verdict := ReadHomeRecommendationCurrentness(ctx, db, finished); verdict.Status != "current" {
		t.Fatalf("saved debrief advice is not current: %+v", verdict)
	}
}
