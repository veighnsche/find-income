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

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
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
// Contributor session behind it; resuming answers 409 because stopped
// discovery runs never resume.
func TestHTTPStopFencesMuseSession(t *testing.T) {
	h := newHarness(t)
	h.commission(t)
	// Wait until the session sits inside the transport: stopping earlier
	// would fence a run the supervisor has not started conducting yet.
	select {
	case <-h.gate.started:
	case <-time.After(10 * time.Second):
		t.Fatal("discovery session never entered the transport")
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
	if status, raw := post("/rounds/" + h.runID + "/resume"); status != http.StatusConflict {
		t.Fatalf("HTTP resume: %d %s, want 409", status, raw)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		report, err := h.stack.Muse.Report(context.Background(), "t23-run-1")
		if err == nil {
			if report.Outcome != musecode.OutcomeStopped {
				t.Fatalf("muse outcome = %q, want stopped", report.Outcome)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no terminal muse report: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
