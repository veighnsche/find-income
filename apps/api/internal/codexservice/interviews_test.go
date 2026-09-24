package codexservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/interviewprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func interviewTestSource() applicationpacks.Source {
	body := "Personal project: a Go service manages local environments. This is personal project work, not paid employment."
	h := sha256.Sum256([]byte(body))
	return applicationpacks.Source{ID: "career", Name: "Approved career source", SHA256: hex.EncodeToString(h[:]), Approved: true, Body: body}
}

func interviewTestDraft() interviewprep.Draft {
	cite := func(id, excerpt string) applicationpacks.Citation {
		return applicationpacks.Citation{SourceID: id, Excerpt: excerpt}
	}
	return interviewprep.Draft{Focus: []interviewprep.FocusAlternative{{ID: "go-service", Why: interviewprep.CitedText{Text: "Discuss a personal Go service project against the role's Go duties.", Citations: []applicationpacks.Citation{cite("role", "Maintain Go services"), cite("career", "Personal project: a Go service manages local environments.")}}}}, Questions: []interviewprep.Question{{Text: "How does the team review service changes?", Why: interviewprep.CitedText{Text: "The role includes a small engineering team.", Citations: []applicationpacks.Citation{cite("role", "small engineering team")}}}}, Examples: []interviewprep.ExampleOutline{{Title: "Local environment service", ExperienceKind: "personal_project", ContextBasis: cite("career", "not paid employment"), Situation: interviewprep.CitedText{Text: "A personal environment project needed service support.", Citations: []applicationpacks.Citation{cite("career", "Personal project: a Go service manages local environments.")}}, Action: interviewprep.CitedText{Text: "Built a Go service for local environments.", Citations: []applicationpacks.Citation{cite("career", "a Go service manages local environments")}}, UnknownResult: "No measured outcome is present in the approved source."}}, Unknowns: []string{"The supplied invitation has no interview time, venue or interviewer names."}}
}

func interviewRoundFixture(t *testing.T) (*Service, *store.Store, store.Round, string, store.Interview) {
	t.Helper()
	ctx := context.Background()
	s, db := testService(t, testConfig())
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Fixture Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", SourceURL: "https://example.test/role", OriginalText: "Maintain Go services with a small engineering team.", Stage: "new"})
	if err != nil {
		t.Fatal(err)
	}
	interview, _, err := db.CommissionInterview(ctx, owner, store.InterviewCommissionInput{RequestKey: "interview-commission", OpportunityID: opportunity.ID, Context: "The owner supplied an invitation about Go services; no schedule was included."})
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "interview-round", Intent: "Prepare one sourced interview brief", Outcome: "interview_prepare", ProfileVersion: interview.ProfileVersion, Deadline: time.Now().Add(time.Hour), Scope: store.RoundScope{InputRefs: []string{"interview:" + interview.ID}, Resources: []string{"opportunity:" + opportunity.ID, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundInterviewBriefSave, store.RoundJevRequest}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: 4, Items: 1, Tools: 4, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = db.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindInterviewRound(ctx, owner, interview.ID, r.ID, false); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "interview:" + interview.ID, Operation: store.RoundCodexTurn, ResourceID: "opportunity:" + opportunity.ID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, db, r, capability, interview
}

func TestInterviewToolSavesBriefOnlyAndFencesStaleContext(t *testing.T) {
	ctx := context.Background()
	s, db, r, capability, interview := interviewRoundFixture(t)
	if err := s.ConfigureInterviews(InterviewRuntimeConfig{LoadSources: func(context.Context) ([]applicationpacks.Source, error) {
		return []applicationpacks.Source{interviewTestSource()}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	args := interviewPrepareArgs{RoundID: r.ID, Capability: capability, RequestKey: "prepare", InterviewID: interview.ID, Draft: interviewTestDraft()}
	first, err := s.interviewPrepareTool(ctx, args)
	if err != nil || first["created"] != true {
		t.Fatalf("brief save: %+v %v", first, err)
	}
	saved, err := db.Interview(ctx, interview.ID)
	if err != nil || len(saved.Brief) == 0 || len(saved.Focus) != 0 {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	attempts, err := db.JevAttemptsForRound(ctx, r.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("tool called Jev: %+v %v", attempts, err)
	}
	second, err := s.interviewPrepareTool(ctx, args)
	if err != nil || second["created"] != false {
		t.Fatalf("replay: %+v %v", second, err)
	}
	changed := "Different role text."
	if _, _, err := db.PatchOpportunity(ctx, store.Actor{Kind: "administrator", ID: "owner"}, interview.OpportunityID, store.OpportunityPatch{ExpectedRevision: 1, OriginalText: &changed}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.interviewPrepareTool(ctx, args); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale role accepted: %v", err)
	}
}

func TestInterviewDebriefToolSavesOnlyOwnerReportedCitations(t *testing.T) {
	ctx := context.Background()
	s, db, r, capability, interview := interviewRoundFixture(t)
	if err := s.ConfigureInterviews(InterviewRuntimeConfig{LoadSources: func(context.Context) ([]applicationpacks.Source, error) {
		return []applicationpacks.Source{interviewTestSource()}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.interviewPrepareTool(ctx, interviewPrepareArgs{RoundID: r.ID, Capability: capability, RequestKey: "prepare", InterviewID: interview.ID, Draft: interviewTestDraft()}); err != nil {
		t.Fatal(err)
	}
	turn, err := db.RoundAttemptForRequest(ctx, r.ID, "interview:"+interview.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if err := db.BindRoundThread(ctx, r.ID, turn.ID, turn.Generation, "interview-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, r.ID, turn.ID, turn.Generation, "interview-thread", "interview-turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.ObserveRoundTurn(ctx, r.ID, turn.ID, turn.Generation, "interview-thread", "interview-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, agent, r.ID, turn.ID, true, json.RawMessage(`{"saved":true}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRound(ctx, owner, r.ID, store.RoundCompleted, "interview_prepared", "complete", json.RawMessage(`{"saved":true}`)); err != nil {
		t.Fatal(err)
	}
	notes := "We discussed the personal Go service. I could not answer the deployment question."
	d, _, err := db.CommissionInterviewDebrief(ctx, owner, store.InterviewDebriefCommissionInput{RequestKey: "debrief-one", InterviewID: interview.ID, Notes: notes})
	if err != nil {
		t.Fatal(err)
	}
	debriefRound, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "debrief-round", Intent: "Record owner notes", Outcome: "interview_debrief", ProfileVersion: interview.ProfileVersion, Deadline: time.Now().Add(time.Hour), Scope: store.RoundScope{InputRefs: []string{"debrief:" + d.ID}, Resources: []string{"interview:" + interview.ID, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundInterviewDebriefSave}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	debriefRound, err = db.ActivateRound(ctx, owner, debriefRound.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindInterviewRound(ctx, owner, d.ID, debriefRound.ID, true); err != nil {
		t.Fatal(err)
	}
	debriefTurn, _, err := db.ReserveRoundAttempt(ctx, agent, debriefRound.ID, store.RoundAttemptInput{RequestKey: "debrief-turn", Operation: store.RoundCodexTurn, ResourceID: "interview:" + interview.ID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, debriefRound.ID, debriefTurn.ID); err != nil {
		t.Fatal(err)
	}
	debriefCapability, err := db.IssueRoundToolCapability(ctx, debriefRound.ID, debriefTurn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	args := interviewDebriefArgs{RoundID: debriefRound.ID, Capability: debriefCapability, RequestKey: "save-debrief", DebriefID: d.ID, Observations: []interviewprep.DebriefObservation{{Kind: "discussed", Detail: interviewprep.CitedText{Text: "The owner reports discussing a personal Go service.", Citations: []applicationpacks.Citation{interviewprep.OwnerNoteCitation("We discussed the personal Go service.")}}}, {Kind: "unclear", Detail: interviewprep.CitedText{Text: "The owner reports not answering a deployment question.", Citations: []applicationpacks.Citation{interviewprep.OwnerNoteCitation("I could not answer the deployment question.")}}}}, Unknowns: []string{"Employer assessment and next steps were not supplied."}}
	if _, err := s.interviewDebriefTool(ctx, args); err != nil {
		t.Fatal(err)
	}
	saved, err := db.InterviewDebrief(ctx, d.ID)
	if err != nil || len(saved.Debrief) == 0 {
		t.Fatalf("debrief not saved: %+v %v", saved, err)
	}
	if _, err := s.interviewDebriefTool(ctx, args); err != nil {
		t.Fatalf("same debrief replay: %v", err)
	}
	if _, _, err := db.StopRound(ctx, owner, debriefRound.ID); err != nil {
		t.Fatal(err)
	}
	args.RequestKey = "after-stop"
	if _, err := s.interviewDebriefTool(ctx, args); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stopped round wrote debrief: %v", err)
	}
}

type interviewRecoveryReady struct{}

func (interviewRecoveryReady) CheckRound(context.Context, string) error { return nil }

type interviewRecoveryWorker struct{ launches int }

func (w *interviewRecoveryWorker) LaunchRound(context.Context, store.Round) error {
	w.launches++
	return nil
}
func (*interviewRecoveryWorker) CancelRound(string) {}

type interviewCaptureEvaluator struct{ logical *[]byte }

func (e interviewCaptureEvaluator) Evaluate(_ context.Context, request jev.Request) (jev.Result, error) {
	*e.logical, _ = json.Marshal(struct {
		State     any                     `json:"state"`
		Questions map[string]jev.Question `json:"questions"`
	}{request.State, request.Questions})
	return jev.Result{}, errors.New("capture request only")
}

type interviewRecoveryReconciler struct {
	service      *Service
	db           *store.Store
	turn         store.RoundAttempt
	terminal     bool
	observations int
}

func (r *interviewRecoveryReconciler) RecoverLocalDispatch(ctx context.Context, roundID, attemptID string, generation int64) (bool, bool, error) {
	return r.service.RecoverLocalDispatch(ctx, roundID, attemptID, generation)
}
func (r *interviewRecoveryReconciler) ObserveDispatch(ctx context.Context, attemptID string) (rounds.Observation, error) {
	if attemptID != r.turn.ID {
		return rounds.Observation{}, errors.New("unexpected remote observation for Jev")
	}
	r.observations++
	state, status := store.AttemptUncertain, "unknown"
	if r.terminal {
		state, status = store.AttemptObservedFailure, "interrupted"
	}
	evidence, _ := json.Marshal(map[string]string{"threadId": "interview-thread", "turnId": "interview-turn", "status": status})
	if err := r.db.ObserveRoundTurn(ctx, r.turn.RoundID, r.turn.ID, r.turn.Generation, "interview-thread", "interview-turn", status, evidence); err != nil {
		return rounds.Observation{}, err
	}
	return rounds.Observation{State: state, Evidence: evidence}, nil
}

func makeCapturedInterviewFocus(t *testing.T, db *store.Store, r store.Round, interview store.Interview, brief interviewprep.Brief, complete bool) store.RoundAttempt {
	t.Helper()
	ctx := context.Background()
	input, err := brief.FocusInput(2500)
	if err != nil {
		t.Fatal(err)
	}
	var logical []byte
	_, err = jev.SelectInterviewFocus(ctx, interviewCaptureEvaluator{logical: &logical}, input)
	if err == nil || len(logical) == 0 {
		t.Fatalf("logical request: %v", err)
	}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	attempt, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "interview-focus:" + interview.ID + "/0", Operation: store.RoundJevRequest, ResourceID: "opportunity:" + interview.OpportunityID, Cost: store.RoundAllowance{Requests: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, r.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(logical)
	refs, _ := json.Marshal(input.Context)
	candidates, _ := json.Marshal(input.Candidates)
	capture, err := db.BeginJevAttempt(ctx, store.JevAttemptStart{RoundID: r.ID, RoundAttemptID: attempt.ID, StepIndex: 0, Purpose: "interview_focus", InputSHA256: hex.EncodeToString(digest[:]), SourceRefsJSON: refs, CandidateSetJSON: candidates, ProfileVersion: r.ProfileVersion, RubricVersion: "interview-focus-v1", RequestedModel: "jev-1.13.0", LogicalRequestJSON: logical, TransportRequestBytes: []byte(`{"synthetic":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		raw := []byte(`{"model":"jev-1.13.0","answers":{"interview_focus":{"type":"choice","choice":"go-service","probabilities":{"go-service":0.9,"__unresolved__":0.1},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`)
		in, out := int64(10), int64(2)
		if _, err := db.FinishJevAttempt(ctx, store.JevAttemptFinish{ID: capture.ID, Status: "succeeded", ReturnedModel: "jev-1.13.0", RawResponseBytes: raw, InputTokens: &in, OutputTokens: &out}); err != nil {
			t.Fatal(err)
		}
	}
	return attempt
}

func TestInterviewResumeKeepsUnknownParentAndRecoversOnlyExactJevCapture(t *testing.T) {
	ctx := context.Background()
	s, db, r, capability, interview := interviewRoundFixture(t)
	if err := s.ConfigureInterviews(InterviewRuntimeConfig{LoadSources: func(context.Context) ([]applicationpacks.Source, error) {
		return []applicationpacks.Source{interviewTestSource()}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	args := interviewPrepareArgs{RoundID: r.ID, Capability: capability, RequestKey: "prepare", InterviewID: interview.ID, Draft: interviewTestDraft()}
	if _, err := s.interviewPrepareTool(ctx, args); err != nil {
		t.Fatal(err)
	}
	saved, _ := db.Interview(ctx, interview.ID)
	var brief interviewprep.Brief
	if err := json.Unmarshal(saved.Brief, &brief); err != nil {
		t.Fatal(err)
	}
	focusAttempt := makeCapturedInterviewFocus(t, db, r, interview, brief, true)
	turn, err := db.RoundAttemptForRequest(ctx, r.ID, "interview:"+interview.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundThread(ctx, r.ID, turn.ID, turn.Generation, "interview-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, r.ID, turn.ID, turn.Generation, "interview-thread", "interview-turn"); err != nil {
		t.Fatal(err)
	}
	worker := &interviewRecoveryWorker{}
	reconciler := &interviewRecoveryReconciler{service: s, db: db, turn: turn}
	roundsService := &rounds.Service{Store: db, Readiness: interviewRecoveryReady{}, Reconciler: reconciler, Worker: worker}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	paused, err := roundsService.Stop(ctx, owner, r.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("stop: %+v %v", paused, err)
	}
	if _, err := db.VerifyRoundToolCapability(ctx, capability, r.ID); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("old tool capability: %v", err)
	}
	if _, err := roundsService.Resume(ctx, owner, r.ID); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("unknown parent resumed: %v", err)
	}
	current, _ := db.Round(ctx, r.ID)
	turnNow, _ := db.RoundAttempt(ctx, turn.ID)
	focusNow, _ := db.RoundAttempt(ctx, focusAttempt.ID)
	visible, _ := db.OwnerInterview(ctx, owner.ID, interview.ID)
	if current.State != store.RoundPaused || turnNow.State != store.AttemptUncertain || focusNow.State != store.AttemptObservedSuccess || len(visible.Brief) == 0 || len(visible.Focus) != 0 || worker.launches != 0 {
		t.Fatalf("paused=%+v turn=%+v focus=%+v visible=%+v launches=%d", current, turnNow, focusNow, visible, worker.launches)
	}
	reconciler.terminal = true
	resumed, err := roundsService.Resume(ctx, owner, r.ID)
	if err != nil || resumed.State != store.RoundRunning || worker.launches != 1 || reconciler.observations != 2 {
		t.Fatalf("terminal resume=%+v err=%v launches=%d observations=%d", resumed, err, worker.launches, reconciler.observations)
	}
	terminal, _ := db.RoundAttempt(ctx, turn.ID)
	if terminal.State != store.AttemptObservedFailure {
		t.Fatalf("terminal observation lost: %+v", terminal)
	}
}

func TestInterviewIncompleteCaptureStaysPausedWithoutJevReconciliationCharge(t *testing.T) {
	ctx := context.Background()
	s, db, r, capability, interview := interviewRoundFixture(t)
	if err := s.ConfigureInterviews(InterviewRuntimeConfig{LoadSources: func(context.Context) ([]applicationpacks.Source, error) {
		return []applicationpacks.Source{interviewTestSource()}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.interviewPrepareTool(ctx, interviewPrepareArgs{RoundID: r.ID, Capability: capability, RequestKey: "prepare", InterviewID: interview.ID, Draft: interviewTestDraft()}); err != nil {
		t.Fatal(err)
	}
	saved, _ := db.Interview(ctx, interview.ID)
	var brief interviewprep.Brief
	if err := json.Unmarshal(saved.Brief, &brief); err != nil {
		t.Fatal(err)
	}
	focus := makeCapturedInterviewFocus(t, db, r, interview, brief, false)
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if _, _, err := db.StopRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PauseStoppedRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := db.Round(ctx, r.ID)
	for i := 0; i < 2; i++ {
		handled, resolved, err := s.RecoverLocalDispatch(ctx, r.ID, focus.ID, before.Generation)
		if err != nil || !handled || resolved {
			t.Fatalf("capture %d handled=%v resolved=%v err=%v", i, handled, resolved, err)
		}
	}
	after, _ := db.Round(ctx, r.ID)
	if after.State != store.RoundPaused || after.Used != before.Used {
		t.Fatalf("unbounded recovery: before=%+v after=%+v", before, after)
	}
}

func TestInterviewPostTurnCapturedJevResumesWithoutProviderReplay(t *testing.T) {
	ctx := context.Background()
	s, db, r, capability, interview := interviewRoundFixture(t)
	if err := s.ConfigureInterviews(InterviewRuntimeConfig{LoadSources: func(context.Context) ([]applicationpacks.Source, error) {
		return []applicationpacks.Source{interviewTestSource()}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.interviewPrepareTool(ctx, interviewPrepareArgs{RoundID: r.ID, Capability: capability, RequestKey: "prepare", InterviewID: interview.ID, Draft: interviewTestDraft()}); err != nil {
		t.Fatal(err)
	}
	turn, err := db.RoundAttemptForRequest(ctx, r.ID, "interview:"+interview.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundThread(ctx, r.ID, turn.ID, turn.Generation, "settled-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, r.ID, turn.ID, turn.Generation, "settled-thread", "settled-turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.ObserveRoundTurn(ctx, r.ID, turn.ID, turn.Generation, "settled-thread", "settled-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, turn.ID, true, json.RawMessage(`{"saved":true}`), ""); err != nil {
		t.Fatal(err)
	}
	saved, err := db.Interview(ctx, interview.ID)
	if err != nil {
		t.Fatal(err)
	}
	var brief interviewprep.Brief
	if err := json.Unmarshal(saved.Brief, &brief); err != nil {
		t.Fatal(err)
	}
	focus := makeCapturedInterviewFocus(t, db, r, interview, brief, true)
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	worker := &interviewRecoveryWorker{}
	service := &rounds.Service{Store: db, Readiness: interviewRecoveryReady{}, Reconciler: s, Worker: worker}
	paused, err := service.Stop(ctx, owner, r.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("stop=%+v err=%v", paused, err)
	}
	resumed, err := service.Resume(ctx, owner, r.ID)
	if err != nil || resumed.State != store.RoundRunning || resumed.Used != paused.Used || worker.launches != 1 {
		t.Fatalf("resume=%+v err=%v launches=%d", resumed, err, worker.launches)
	}
	turnNow, err := db.RoundAttempt(ctx, turn.ID)
	if err != nil || turnNow.State != store.AttemptSucceeded {
		t.Fatalf("parent turn=%+v err=%v", turnNow, err)
	}
	focusNow, err := db.RoundAttempt(ctx, focus.ID)
	if err != nil || focusNow.State != store.AttemptObservedSuccess {
		t.Fatalf("captured Jev=%+v err=%v", focusNow, err)
	}
	captures, err := db.JevAttemptsForRound(ctx, r.ID)
	if err != nil || len(captures) != 1 {
		t.Fatalf("captures=%+v err=%v", captures, err)
	}
	input, err := brief.FocusInput(2500)
	if err != nil {
		t.Fatal(err)
	}
	choice, err := jev.RecoverCapturedInterviewFocus(input, captures[0].LogicalRequestJSON, captures[0].RawResponseBytes, captures[0].RequestedModel)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := brief.BindFocusSelection(choice, 2500)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(prepared)
	if _, err := db.ApplyInterviewFocus(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, r.Generation, interview.ID, captures[0].ID, encoded); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("old generation applied focus: %v", err)
	}
}

func TestInterviewCompleteProductDoesNotSettleUnknownParentTurn(t *testing.T) {
	ctx := context.Background()
	s, db, r, capability, interview := interviewRoundFixture(t)
	if err := s.ConfigureInterviews(InterviewRuntimeConfig{LoadSources: func(context.Context) ([]applicationpacks.Source, error) {
		return []applicationpacks.Source{interviewTestSource()}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.interviewPrepareTool(ctx, interviewPrepareArgs{RoundID: r.ID, Capability: capability, RequestKey: "prepare", InterviewID: interview.ID, Draft: interviewTestDraft()}); err != nil {
		t.Fatal(err)
	}
	saved, _ := db.Interview(ctx, interview.ID)
	var brief interviewprep.Brief
	if err := json.Unmarshal(saved.Brief, &brief); err != nil {
		t.Fatal(err)
	}
	focusAttempt := makeCapturedInterviewFocus(t, db, r, interview, brief, true)
	captures, err := db.JevAttemptsForRound(ctx, r.ID)
	if err != nil || len(captures) != 1 {
		t.Fatalf("captures=%+v err=%v", captures, err)
	}
	if _, err := db.FinishRoundAttempt(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, focusAttempt.ID, true, json.RawMessage(`{"jevAttemptId":"saved"}`), ""); err != nil {
		t.Fatal(err)
	}
	input, err := brief.FocusInput(2500)
	if err != nil {
		t.Fatal(err)
	}
	choice, err := jev.RecoverCapturedInterviewFocus(input, captures[0].LogicalRequestJSON, captures[0].RawResponseBytes, captures[0].RequestedModel)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := brief.BindFocusSelection(choice, 2500)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(prepared)
	if _, err := db.ApplyInterviewFocus(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, r.Generation, interview.ID, captures[0].ID, encoded); err != nil {
		t.Fatal(err)
	}
	turn, err := db.RoundAttemptForRequest(ctx, r.ID, "interview:"+interview.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundThread(ctx, r.ID, turn.ID, turn.Generation, "interview-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, r.ID, turn.ID, turn.Generation, "interview-thread", "interview-turn"); err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	worker := &interviewRecoveryWorker{}
	reconciler := &interviewRecoveryReconciler{service: s, db: db, turn: turn}
	service := &rounds.Service{Store: db, Readiness: interviewRecoveryReady{}, Reconciler: reconciler, Worker: worker}
	if _, err := service.Stop(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(ctx, owner, r.ID); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("saved product cleared parent uncertainty: %v", err)
	}
	active, err := db.ActiveRound(ctx)
	visible, readErr := db.OwnerInterview(ctx, owner.ID, interview.ID)
	parent, attemptErr := db.RoundAttempt(ctx, turn.ID)
	if err != nil || active.ID != r.ID || readErr != nil || len(visible.Focus) == 0 || attemptErr != nil || parent.State != store.AttemptUncertain || worker.launches != 0 {
		t.Fatalf("active=%+v err=%v visible=%+v readErr=%v parent=%+v attemptErr=%v launches=%d", active, err, visible, readErr, parent, attemptErr, worker.launches)
	}
}
