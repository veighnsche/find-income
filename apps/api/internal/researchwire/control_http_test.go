package researchwire

// HTTP stop/resume routing for research runs (T27 regression): the shared
// rounds routes must reach the run supervisor, whose worker is the only one
// that launches research outcomes. The legacy rounds worker rejects
// research_run at readiness, so falling through to it breaks browser Resume
// (HTTP 500) and skips stop journaling. This test pins the real stack behind
// the real HTTP handler: stop fences + journals, resume rotates the
// generation on remaining allowance, and an unwired control degrades to an
// honest 503 instead of half-built behavior.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestHTTPStopResumeResearchRun(t *testing.T) {
	h := newHarness(t)
	h.commissionSupervisor(t)
	authSvc := auth.NewService(h.db)
	const password = "t27-control-password"
	if err := authSvc.SetupAdministrator(h.ctx, []byte(password)); err != nil {
		t.Fatal(err)
	}
	const origin = "http://127.0.0.1:9"
	serve := func(control httpapi.ResearchRunControl) *httptest.Server {
		t.Helper()
		server := httptest.NewServer(httpapi.NewHandler(h.db, authSvc, httpapi.Options{
			AllowedOrigins: []string{origin},
			// No legacy worker: research control must not need it.
			Rounds:          &rounds.Service{Store: h.db},
			Research:        h.stack.Research,
			ResearchControl: control,
		}))
		t.Cleanup(server.Close)
		return server
	}
	login := func(server *httptest.Server) (cookie *http.Cookie, csrf string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/login",
			strings.NewReader(`{"password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(resp.Cookies()) != 1 {
			t.Fatalf("login cookies: %d", len(resp.Cookies()))
		}
		cookie = resp.Cookies()[0]
		sreq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/auth/session", nil)
		sreq.Header.Set("Accept", "application/json")
		sreq.AddCookie(cookie)
		sresp, err := server.Client().Do(sreq)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(sresp.Body)
		sresp.Body.Close()
		if sresp.StatusCode != http.StatusOK {
			t.Fatalf("session: %d %s", sresp.StatusCode, raw)
		}
		var session struct {
			CSRFToken string `json:"csrfToken"`
		}
		if err := json.Unmarshal(raw, &session); err != nil {
			t.Fatal(err)
		}
		return cookie, session.CSRFToken
	}
	server := serve(h.stack.Supervisor)
	cookie, csrf := login(server)
	post := func(path string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", csrf)
		req.AddCookie(cookie)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	// One settled dispatch before the stop so the fence has real work behind it.
	h.fetch(t, "control-fetch-1", h.board.URL+"/roles/1")

	status, raw := post("/rounds/" + h.runID + "/stop")
	if status != http.StatusOK {
		t.Fatalf("HTTP stop: %d %s", status, raw)
	}
	var stopped struct {
		State      string `json:"state"`
		Generation int64  `json:"generation"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(raw, &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.State != "paused" || stopped.StopReason == "" {
		t.Fatalf("stopped round: %+v", stopped)
	}
	if stopped.Generation == h.gen {
		t.Fatal("HTTP stop did not rotate the control generation")
	}
	events, _, err := h.stack.Journal.List(h.ctx, h.runID, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	seenStop := false
	for _, e := range events {
		if e.Kind == "run.stopped" {
			seenStop = true
		}
	}
	if !seenStop {
		t.Fatal("HTTP stop journaled no run.stopped event")
	}
	stale, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: h.gen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: h.board.URL + "/roles/1-cross",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "control-fetch-stale",
	})
	if err == nil && (stale.Outcome == researchcontract.OutcomeOK || stale.Outcome == researchcontract.OutcomeReused) {
		t.Fatalf("stale generation dispatched after HTTP stop: %+v", stale)
	}

	status, raw = post("/rounds/" + h.runID + "/resume")
	if status != http.StatusOK {
		t.Fatalf("HTTP resume: %d %s", status, raw)
	}
	var resumed struct {
		State      string `json:"state"`
		Generation int64  `json:"generation"`
	}
	if err := json.Unmarshal(raw, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.State != "running" || resumed.Generation == stopped.Generation {
		t.Fatalf("resumed round: %+v", resumed)
	}
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: round.Generation, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: h.board.URL + "/roles/1-cross",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "control-fetch-after-resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Outcome != researchcontract.OutcomeOK && after.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("post-resume dispatch: %+v", after)
	}

	// Unwired control degrades honestly: a handler without the supervisor
	// answers 503 on the same research stop route.
	plain := serve(nil)
	pcookie, pcsrf := login(plain)
	preq, err := http.NewRequest(http.MethodPost, plain.URL+"/api/v1/rounds/"+h.runID+"/stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	preq.Header.Set("Accept", "application/json")
	preq.Header.Set("Origin", origin)
	preq.Header.Set("X-CSRF-Token", pcsrf)
	preq.AddCookie(pcookie)
	presp, err := plain.Client().Do(preq)
	if err != nil {
		t.Fatal(err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unwired research stop: %d, want 503", presp.StatusCode)
	}
}

// Stopping a muse discovery run over HTTP fences the round and the live
// Contributor session behind it; resuming revives the round and
// re-conducts the session from its durable cursor. The stop-time
// assessment is fenced with the round, so the stopped report carries the
// save with an honest gap; the resumed run judges it once revived and
// completes the same bounded run.
func TestHTTPStopResumeMuseDiscoveryRun(t *testing.T) {
	h := newHarness(t)
	h.commission(t)
	// Wait until the session sits inside the transport: stopping earlier
	// would fence a run the supervisor has not started conducting yet.
	select {
	case <-h.gate.started:
	case <-time.After(10 * time.Second):
		t.Fatal("discovery session never entered the transport")
	}
	if _, err := h.db.AuthorReasonCatalog(h.ctx, h.owner, store.ReasonCatalogInput{
		ProfileVersion: h.profile,
		Rubric:         "Match senior support roles; hybrid or remote.",
		Positive:       []store.ReasonChoice{{ID: "hybrid-ok", Label: "Hybrid friendly", Detail: "Listing offers hybrid or remote work."}},
		Negative:       []store.ReasonChoice{{ID: "oncall-heavy", Label: "Heavy on-call", Detail: "Listing requires heavy on-call."}},
		MissingInformation: []store.ReasonChoice{
			{ID: "pay-unknown", Label: "Pay unstated", Detail: "Listing states no base pay."},
		},
	}); err != nil {
		t.Fatal(err)
	}
	dispatched, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: h.gen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: h.board.URL + "/roles/1",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "muse-resume-fetch-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatched.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("dispatch: %+v", dispatched)
	}
	toolServer, ok := h.stack.Muse.ServerForRun("t23-run-1")
	if !ok {
		t.Fatal("live tool server unavailable during the session")
	}
	mcpServer := httptest.NewServer(toolServer.Handler())
	t.Cleanup(mcpServer.Close)
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "t27-muse", Version: "1"}, nil)
	mcpSession, err := mcpClient.Connect(h.ctx, &mcp.StreamableClientTransport{
		Endpoint: mcpServer.URL, HTTPClient: mcpServer.Client(),
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mcpSession.Close() })
	saved, err := mcpSession.CallTool(h.ctx, &mcp.CallToolParams{Name: "public_save_vacancy",
		Arguments: map[string]any{
			"page_url": h.board.URL + "/roles/1", "employer_name": "Contoso Support",
			"title": "Support Engineer", "location_text": "Berlin", "receipt": dispatched.Receipt.ID,
		}})
	if err != nil || saved.IsError {
		t.Fatalf("save vacancy: %+v %v", saved, err)
	}
	payload, ok := saved.StructuredContent.(map[string]any)
	if !ok || payload["outcome"] != "ok" {
		t.Fatalf("save vacancy: %+v", saved.StructuredContent)
	}
	vacancy, ok := payload["vacancy"].(map[string]any)
	if !ok {
		t.Fatalf("save vacancy: no vacancy in %+v", payload)
	}
	ref, _ := vacancy["vacancy_ref"].(string)
	if ref == "" {
		t.Fatalf("save vacancy: no ref in %+v", payload)
	}
	h.gate.emitted = make(chan struct{})
	h.gate.saves = []string{ref}
	h.gate.after = func(runCtx context.Context) {
		<-runCtx.Done()
	}
	h.gate.Release()
	select {
	case <-h.gate.emitted:
	case <-time.After(10 * time.Second):
		t.Fatal("session never emitted the save")
	}
	authSvc := auth.NewService(h.db)
	const password = "t27-muse-control-password"
	if err := authSvc.SetupAdministrator(h.ctx, []byte(password)); err != nil {
		t.Fatal(err)
	}
	const origin = "http://127.0.0.1:9"
	server := httptest.NewServer(httpapi.NewHandler(h.db, authSvc, httpapi.Options{
		AllowedOrigins:  []string{origin},
		Rounds:          &rounds.Service{Store: h.db},
		Research:        h.stack.Research,
		ResearchControl: h.stack.Supervisor,
		Muse:            h.stack.Muse,
	}))
	t.Cleanup(server.Close)
	loginReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/login",
		strings.NewReader(`{"password":"`+password+`"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Origin", origin)
	loginResp, err := server.Client().Do(loginReq)
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()
	if len(loginResp.Cookies()) != 1 {
		t.Fatalf("login cookies: %d", len(loginResp.Cookies()))
	}
	cookie := loginResp.Cookies()[0]
	sessionReq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/auth/session", nil)
	sessionReq.Header.Set("Accept", "application/json")
	sessionReq.AddCookie(cookie)
	sessionResp, err := server.Client().Do(sessionReq)
	if err != nil {
		t.Fatal(err)
	}
	sessionRaw, _ := io.ReadAll(sessionResp.Body)
	sessionResp.Body.Close()
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(sessionRaw, &session); err != nil {
		t.Fatal(err)
	}
	post := func(path string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		req.AddCookie(cookie)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	status, raw := post("/rounds/" + h.runID + "/stop")
	if status != http.StatusOK {
		t.Fatalf("HTTP stop: %d %s", status, raw)
	}
	var stopped struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.State != "paused" {
		t.Fatalf("stopped round: %+v", stopped)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		row, err := h.db.LoadMuseRunReport(context.Background(), "t23-run-1")
		if err == nil {
			if row.Outcome != string(musecode.OutcomeStopped) {
				t.Fatalf("muse outcome = %q, want stopped", row.Outcome)
			}
			if len(row.SavedRefs) != 1 || row.SavedRefs[0] != ref {
				t.Fatalf("stopped saves = %v, want [%s]", row.SavedRefs, ref)
			}
			if len(row.ClassifyErrors) != 1 || !strings.Contains(row.ClassifyErrors[0], "resume before new work") {
				t.Fatalf("stopped gaps = %v, want the fence gap", row.ClassifyErrors)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no stopped muse report: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	page, err := h.db.ListRunFindings(h.ctx, h.runID, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("stop-time findings = %d, want 0 (assessment fenced)", len(page.Items))
	}
	h.gate.after = nil
	h.gate.saves = nil
	status, raw = post("/rounds/" + h.runID + "/resume")
	if status != http.StatusOK {
		t.Fatalf("HTTP resume: %d %s", status, raw)
	}
	var resumed struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.State != "running" {
		t.Fatalf("resumed round: %+v", resumed)
	}
	deadline = time.Now().Add(60 * time.Second)
	for {
		row, err := h.db.LoadMuseRunReport(context.Background(), "t23-run-1")
		if err == nil && row.Outcome == string(musecode.OutcomeCompleted) {
			if len(row.SavedRefs) != 1 || row.SavedRefs[0] != ref {
				t.Fatalf("resumed saves = %v, want [%s]", row.SavedRefs, ref)
			}
			if len(row.ClassifyErrors) != 0 {
				t.Fatalf("resumed gaps = %v, want none", row.ClassifyErrors)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no completed muse report: %+v %v", row, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	page, err = h.db.ListRunFindings(h.ctx, h.runID, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].VacancyRef != ref {
		t.Fatalf("resumed findings = %+v, want the one judged save", page.Items)
	}
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if round.State != store.RoundCompleted {
		t.Fatalf("resumed round state = %q, want completed", round.State)
	}
	// A completed run never replays: the terminal round refuses resume.
	if status, raw := post("/rounds/" + h.runID + "/resume"); status != http.StatusBadRequest {
		t.Fatalf("completed resume: %d %s, want 400", status, raw)
	}
}
