package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func completeMatchCheck(t *testing.T, h *harness, selectKey string) (store.Opportunity, store.CheckView) {
	t.Helper()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := h.db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := h.db.CreateOpportunity(ctx, owner, store.OpportunityInput{
		CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/1", OriginalText: "Build Go services.",
		Stage: "new", WorkPattern: "hybrid",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID,
		store.OwnerDecisionInput{RequestKey: selectKey, ExpectedOpportunityRevision: opportunity.Revision,
			ExpectedDecisionRevision: 0, Decision: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	started, _, err := h.db.StartJobCheck(ctx, owner, opportunity.ID,
		store.CheckStartInput{RequestKey: selectKey + "-check", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	var capture store.SourceCapture
	url := "https://harbour.example/jobs/1"
	err = h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		sum := sha256.Sum256([]byte(url))
		var err error
		capture, err = store.InsertSourceCapture(ctx, db, store.SourceCaptureInput{
			ContentSHA256: hex.EncodeToString(sum[:]), ArtifactRef: "artifact/test",
			ByteLength: 2048, MediaType: "text/html", OriginalURL: url,
			Provenance: researchcontract.ProvenanceFetchedResponse, Completeness: store.CaptureComplete,
			Executor: researchcontract.ExecutorIdentity{Backend: "test-shell"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := h.db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: selectKey + "-round",
		Intent: "Check one selected role", Outcome: "process_input", ProfileVersion: prefs.Version,
		Deadline: time.Now().Add(time.Hour),
		Scope: store.RoundScope{Operations: []string{store.RoundCodexTurn, store.RoundCheckSave},
			Resources: []string{"campaign:active", "opportunity:" + opportunity.ID},
			Delegates: []string{"codex-check"}},
		Limits: store.RoundAllowance{Requests: 8, Items: 8, Tools: 8, Turns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.ActivateRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	workflow, err := h.db.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent := store.Actor{Kind: "agent", ID: "codex-check"}
	turn, _, err := h.db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{
		RequestKey: selectKey + "-turn", Operation: store.RoundCodexTurn,
		ResourceID: "opportunity:" + opportunity.ID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := h.db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	span := func(start, end int) store.CheckSourceSpan {
		return store.CheckSourceSpan{CaptureID: capture.ID, Start: start, End: end}
	}
	_, _, err = h.db.ApplyRoundMutation(ctx, agent, round.ID, store.RoundMutationInput{
		RequestKey: selectKey + "-save", Operation: store.RoundCheckSave,
		ResourceID: "opportunity:" + opportunity.ID, ExpectedRevision: workflow.Revision,
		Capability: capability,
		CheckSave: &store.CheckSaveInput{OpportunityID: opportunity.ID, CheckID: started.ID,
			RequestedDocuments: []store.RequestedDocumentInput{}, Gaps: []store.CheckGapInput{},
			Vacancy: store.CheckVacancyInput{CaptureIDs: []string{capture.ID},
				Completeness: store.CaptureComplete, SourceURL: url, RetrievedAt: "2026-09-24T11:05:00Z"},
			Route: store.CheckRouteInput{Kind: store.CheckRouteDirect,
				DestinationText: "jobs@example.invalid", Judgment: store.CheckRouteJudgmentApplication,
				SourceExcerpt: "Send your CV", ObservedAt: "2026-09-24T11:30:00Z"},
			Questions: []store.CheckQuestionInput{
				{Text: "Why do you want this role?", Required: store.CheckRequired,
					SourceSpan: span(150, 180), SourceExcerpt: "Why do you want this role?"},
				{Text: "Hybrid?", Required: store.CheckRequiredUnknown,
					SourceSpan: span(300, 335), SourceExcerpt: "Hybrid?"},
			}}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := h.db.GetJobCheck(ctx, opportunity.ID, started.ID)
	if err != nil || view.Status != store.CheckStatusChecked {
		t.Fatalf("completing save: %+v %v", view, err)
	}
	if _, _, err := h.db.StopRound(ctx, owner, round.ID); err != nil {
		t.Fatalf("stop save round: %v", err)
	}
	paused, err := h.db.PauseStoppedRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatalf("pause save round: %v", err)
	}
	check, err := h.db.BeginRoundReconciliation(ctx, round.ID, turn.ID, paused.Generation)
	if err != nil {
		t.Fatalf("begin reconcile: %v", err)
	}
	if _, err := h.db.FinishRoundReconciliation(ctx, check, store.AttemptObservedSuccess, json.RawMessage(`{"status":"confirmed"}`)); err != nil {
		t.Fatalf("finish reconcile: %v", err)
	}
	if _, err := h.db.ResumeRound(ctx, owner, round.ID, paused.Generation); err != nil {
		t.Fatalf("resume save round: %v", err)
	}
	if _, err := h.db.FinishRound(ctx, owner, round.ID, store.RoundCompleted, "check_saved", "job_check", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("finish save round: %v", err)
	}
	return opportunity, view
}

func TestAnswerMatchEndpointsDeterministic(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity, check := completeMatchCheck(t, h, "match-select-1")

	response := h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/answers/match/current", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("match before run: %d", response.Code)
	}
	body := `{"requestKey":"match-run-1","expectedCheckId":"` + check.ID + `","expectedQuestionSetSha256":"` + check.QuestionSetSHA256 + `"}`
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/answers/match", body, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("deterministic match: %d %s", response.Code, response.Body.String())
	}
	var matched struct {
		Status  string `json:"status"`
		Matches []struct {
			QuestionID string `json:"questionId"`
			Choice     struct {
				NoneFits bool `json:"noneFits"`
			} `json:"choice"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &matched); err != nil {
		t.Fatal(err)
	}
	if matched.Status != "unmatched" || len(matched.Matches) != 2 ||
		!matched.Matches[0].Choice.NoneFits || !matched.Matches[1].Choice.NoneFits {
		t.Fatalf("none-fits view: %+v", matched)
	}
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/answers/match", body, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("match replay: %d", response.Code)
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/answers/match/current", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("match read: %d", response.Code)
	}
	stale := `{"requestKey":"match-run-2","expectedCheckId":"` + check.ID + `","expectedQuestionSetSha256":"wrong"}`
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/answers/match", stale, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("stale pins: %d", response.Code)
	}
	var conflict struct {
		Error struct {
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error.Details["reason"] != "outdated_check" {
		t.Fatalf("conflict reason: %+v", conflict)
	}
	other := createCheckedOpportunity(t, h, "match-select-2")
	response = h.request("POST", "/api/v1/opportunities/"+other.ID+"/answers/match",
		`{"requestKey":"match-run-3","expectedCheckId":"none","expectedQuestionSetSha256":"none"}`, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("unstarted check: %d", response.Code)
	}
}
