package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type fakeResearchService struct {
	mutations int
	err       error
}

func (f *fakeResearchService) CommissionResearch(_ context.Context, in CommissionResearchInput) (CommissionResearchOutput, error) {
	f.mutations++
	if f.err != nil {
		return CommissionResearchOutput{}, f.err
	}
	created := in.IdempotencyKey != "replay-key"
	return CommissionResearchOutput{View: fakeRunView("run-1"), Created: created}, nil
}

func (f *fakeResearchService) SteerResearch(_ context.Context, in SteerResearchInput) (generated.SteeringMessage, error) {
	f.mutations++
	if f.err != nil {
		return generated.SteeringMessage{}, f.err
	}
	return generated.SteeringMessage{MessageId: "msg-1", Revision: 1, Body: in.Body, Ack: "pending"}, nil
}

func (f *fakeResearchService) ResearchRun(_ context.Context, _ store.Actor, runID string) (generated.ResearchRunView, error) {
	if f.err != nil {
		return generated.ResearchRunView{}, f.err
	}
	return fakeRunView(runID), nil
}

func (f *fakeResearchService) ResearchActivity(_ context.Context, _ store.Actor, _ string, _ string, _ int) (generated.ResearchActivityPage, error) {
	if f.err != nil {
		return generated.ResearchActivityPage{}, f.err
	}
	return generated.ResearchActivityPage{Events: []generated.ResearchActivityEvent{
		{EventId: "ev-1", At: time.Now().UTC(), Kind: "claim", Summary: "claimed"},
	}}, nil
}

func (f *fakeResearchService) ResearchCapture(_ context.Context, _ store.Actor, captureID string) (generated.ResearchCaptureView, error) {
	if f.err != nil {
		return generated.ResearchCaptureView{}, f.err
	}
	return generated.ResearchCaptureView{CaptureId: captureID, ObservedUrl: "https://example.com/", RetrievedAt: time.Now().UTC(), Status: "200", MediaType: "text/html", ContentHash: "sha256:abc"}, nil
}

func (f *fakeResearchService) ResearchIdentity(_ context.Context, _ store.Actor, _, subjectID string) (generated.ResearchIdentityView, error) {
	if f.err != nil {
		return generated.ResearchIdentityView{}, f.err
	}
	return generated.ResearchIdentityView{SubjectId: subjectID, Candidates: []generated.ResearchIdentityCandidate{}, Decision: "new", Basis: "no candidates"}, nil
}

func (f *fakeResearchService) ResearchReport(_ context.Context, _ store.Actor, runID string) (generated.ResearchReportView, error) {
	if f.err != nil {
		return generated.ResearchReportView{}, f.err
	}
	return generated.ResearchReportView{RunId: runID, Outcomes: []string{}, Searched: []string{}, Reused: []string{}, Uncertainty: []string{}}, nil
}

func fakeRunView(runID string) generated.ResearchRunView {
	return generated.ResearchRunView{
		RunId: runID, State: "running",
		Allowance:      generated.ResearchAllowance{TimeMs: 900000, MaxActions: 60, MaxJev: 12, MaxTurns: 8, MaxConcurrent: 2},
		Investigations: []generated.ResearchInvestigation{},
		SavedIds:       []string{},
	}
}

func newResearchHTTP(t *testing.T, svc ResearchService) *recordHTTP {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := auth.NewService(db)
	if err := service.SetupAdministrator(context.Background(), []byte(password)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(db, service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: db}, Research: svc}))
	t.Cleanup(func() { server.Close(); _ = db.Close() })
	return &recordHTTP{t: t, client: server.Client(), server: server, db: db, service: service}
}

func TestResearchEndpointsContract(t *testing.T) {
	fake := &fakeResearchService{}
	h := newResearchHTTP(t, fake)
	h.login()

	status, body := h.owner(http.MethodPost, "/research/runs", `{"briefText":"find work","idempotencyKey":"k1"}`)
	requireStatus(t, status, http.StatusCreated, body)
	if decodeObject(t, body)["runId"] != "run-1" {
		t.Fatalf("commission view: %s", body)
	}
	status, body = h.owner(http.MethodPost, "/research/runs", `{"briefText":"find work","idempotencyKey":"replay-key"}`)
	requireStatus(t, status, http.StatusOK, body)

	status, body = h.owner(http.MethodPost, "/research/runs", `{"briefText":"`+strings.Repeat("x", 20001)+`"}`)
	requireStatus(t, status, http.StatusBadRequest, body)
	status, body = h.owner(http.MethodPost, "/research/runs", `{"allowance":{"timeMs":0,"maxActions":1,"maxJev":0,"maxTurns":1,"maxConcurrent":1}}`)
	requireStatus(t, status, http.StatusBadRequest, body)

	status, body = h.owner(http.MethodPost, "/research/runs/run-1/steer", `{"body":"focus remote"}`)
	requireStatus(t, status, http.StatusOK, body)
	if decodeObject(t, body)["ack"] != "pending" {
		t.Fatalf("steer ack: %s", body)
	}
	status, body = h.owner(http.MethodPost, "/research/runs/run-1/steer", `{"body":"  "}`)
	requireStatus(t, status, http.StatusBadRequest, body)

	mutations := fake.mutations
	for _, path := range []string{"/research/runs/run-1", "/research/runs/run-1/activity?limit=10", "/research/runs/run-1/report", "/research/captures/cap-1", "/research/identity?subjectKind=vacancy&subjectId=opp-1"} {
		status, body = h.owner(http.MethodGet, path)
		requireStatus(t, status, http.StatusOK, body)
	}
	if fake.mutations != mutations {
		t.Fatal("reads must be side-effect-free against the service")
	}

	status, body = h.owner(http.MethodGet, "/research/runs/run-1/activity?limit=101")
	requireStatus(t, status, http.StatusBadRequest, body)
	status, body = h.owner(http.MethodGet, "/research/identity?subjectKind=company&subjectId=x")
	requireStatus(t, status, http.StatusBadRequest, body)
	status, body = h.owner(http.MethodGet, "/research/identity?subjectKind=vacancy")
	requireStatus(t, status, http.StatusBadRequest, body)
}

func TestResearchErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{researchcontract.NewError(researchcontract.OutcomeInvalid, "", "bad"), http.StatusBadRequest},
		{researchcontract.NewError(researchcontract.OutcomeNotFound, "", "gone"), http.StatusNotFound},
		{researchcontract.NewError(researchcontract.OutcomeForbidden, "permission", "no"), http.StatusForbidden},
		{researchcontract.NewError(researchcontract.OutcomeBudgetExhausted, "", "empty"), http.StatusTooManyRequests},
		{researchcontract.NewError(researchcontract.OutcomeStale, "", "moved"), http.StatusConflict},
		{researchcontract.NewError(researchcontract.OutcomeUncertain, "", "unknown"), http.StatusConflict},
		{researchcontract.NewError(researchcontract.OutcomeStopped, "", "stopped"), http.StatusConflict},
		{store.ErrNotFound, http.StatusNotFound},
		{store.ErrFenced, http.StatusForbidden},
	}
	for _, c := range cases {
		fake := &fakeResearchService{err: c.err}
		h := newResearchHTTP(t, fake)
		h.login()
		status, body := h.owner(http.MethodGet, "/research/runs/run-1")
		requireStatus(t, status, c.want, body)
	}
}

// D1/C2: the first explicit commission authors the reason catalog for the
// current saved goal version; a second commission reuses it, and a new
// goal version authors exactly one new catalog. Passive reads never
// author: the unauthored-version read below stays 404 until commissioned.
func TestCommissionAuthorsCatalogForCurrentBrief(t *testing.T) {
	fake := &fakeResearchService{}
	h := newResearchHTTP(t, fake)
	h.login()
	ctx := context.Background()

	status, body := h.owner(http.MethodPost, "/research/runs", `{"briefText":"backend roles","idempotencyKey":"d1-first"}`)
	requireStatus(t, status, http.StatusCreated, body)
	first, err := h.db.ReasonCatalog(ctx, 1)
	if err != nil {
		t.Fatalf("first commission authored no catalog: %v", err)
	}
	if len(first.Positive) == 0 || len(first.Negative) == 0 {
		t.Fatalf("commissioned catalog has no reason polarities: %+v", first)
	}

	status, body = h.owner(http.MethodPost, "/research/runs", `{"briefText":"backend roles","idempotencyKey":"d1-second"}`)
	requireStatus(t, status, http.StatusCreated, body)
	reused, err := h.db.ReasonCatalog(ctx, 1)
	if err != nil || reused.CatalogVersion != first.CatalogVersion {
		t.Fatalf("second commission forked the catalog: %+v err=%v", reused, err)
	}

	prefs, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefs.RoleCriteria = append(prefs.RoleCriteria, store.RoleCriterion{
		ID: "go-services", Label: "Go services", Description: "Go service ownership.",
		Kind: "technology", Mode: "prefer",
	})
	updated, _, err := h.db.UpdatePreferences(ctx, prefs.Version, prefs,
		store.Actor{Kind: "administrator", ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.owner(http.MethodGet, "/research/briefs/2/catalog")
	requireStatus(t, status, http.StatusNotFound, body)
	status, body = h.owner(http.MethodPost, "/research/runs", `{"briefText":"backend roles","idempotencyKey":"d1-third"}`)
	requireStatus(t, status, http.StatusCreated, body)
	second, err := h.db.ReasonCatalog(ctx, updated.Version)
	if err != nil || second.CatalogVersion == first.CatalogVersion {
		t.Fatalf("new goal version authored no new catalog: %+v err=%v", second, err)
	}
}

// recoveryResearchService is a ResearchService double that also serves the
// optional run-recovery surface with scripted pages.
type recoveryResearchService struct {
	*fakeResearchService
	page      RunHistoryPage
	latest    RunHistoryItem
	latestErr error
	lists     int
	latests   int
}

func (f *recoveryResearchService) ListResearchRuns(_ context.Context, _ store.Actor, opts RunHistoryOptions) (RunHistoryPage, error) {
	f.lists++
	if len(opts.States) == 1 && opts.States[0] == "bogus" {
		return RunHistoryPage{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "state", "unknown run state")
	}
	return f.page, nil
}

func (f *recoveryResearchService) LatestTerminalResearchRun(_ context.Context, _ store.Actor, _ string) (RunHistoryItem, error) {
	f.latests++
	if f.latestErr != nil {
		return RunHistoryItem{}, f.latestErr
	}
	return f.latest, nil
}

// D3/C1: the recovery handlers serve the relevant-run lookup and latest
// terminal run for server-backed restoration (route registration stays
// I-owned, so these call the handler methods directly). Reads are
// authenticated and side-effect-free; doubles predating the recovery
// surface report honest unavailable instead of breaking.
func TestRecoveryHandlersServeHistory(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	fake := &recoveryResearchService{fakeResearchService: &fakeResearchService{},
		page: RunHistoryPage{Items: []RunHistoryItem{
			{RunID: "run-paused", RequestKey: "d3-a", Intent: "find", Outcome: "research_run",
				State: "paused", CreatedAt: "2026-09-26T10:00:00Z", UpdatedAt: "2026-09-26T10:05:00Z"},
			{RunID: "run-failed", RequestKey: "d3-b", Intent: "find", Outcome: "research_run",
				State: "failed", StopReason: "contributor_error",
				CreatedAt: "2026-09-26T09:00:00Z", UpdatedAt: "2026-09-26T09:05:00Z",
				CompletedAt: "2026-09-26T09:05:00Z"},
		}, NextCursor: "cursor-1"},
		latest: RunHistoryItem{RunID: "run-failed", Outcome: "research_run", State: "failed"}}
	handler := &Handler{database: h.db, auth: h.service, research: fake}

	call := func(method func(http.ResponseWriter, *http.Request), target string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.RemoteAddr = "127.0.0.1:12345"
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		method(response, request)
		return response
	}

	response := call(handler.listResearchRuns, "/api/v1/research/runs?limit=2&state=paused&state=failed", cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("list runs: got %d %s, want 200", response.Code, response.Body.String())
	}
	var page RunHistoryPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].RunID != "run-paused" || page.NextCursor != "cursor-1" {
		t.Fatalf("recovery page: %+v", page)
	}
	if fake.lists != 1 {
		t.Fatalf("list served %d service calls, want 1", fake.lists)
	}

	response = call(handler.getLatestTerminalResearchRun, "/api/v1/research/runs/latest-terminal?outcome=research_run", cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("latest terminal: got %d %s, want 200", response.Code, response.Body.String())
	}
	var latest RunHistoryItem
	if err := json.Unmarshal(response.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if latest.RunID != "run-failed" || latest.State != "failed" {
		t.Fatalf("latest terminal: %+v", latest)
	}

	fake.latestErr = researchcontract.NewError(researchcontract.OutcomeNotFound, "run", "no terminal run recorded")
	response = call(handler.getLatestTerminalResearchRun, "/api/v1/research/runs/latest-terminal", cookie)
	if response.Code != http.StatusNotFound {
		t.Fatalf("empty terminal history: got %d, want 404", response.Code)
	}
	fake.latestErr = nil

	if response := call(handler.listResearchRuns, "/api/v1/research/runs?limit=500", cookie); response.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: got %d, want 400", response.Code)
	}
	if response := call(handler.listResearchRuns, "/api/v1/research/runs?state=bogus", cookie); response.Code != http.StatusBadRequest {
		t.Fatalf("bogus state: got %d, want 400", response.Code)
	}
	if response := call(handler.listResearchRuns, "/api/v1/research/runs", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: got %d, want 401", response.Code)
	}
	if response := call(handler.getLatestTerminalResearchRun, "/api/v1/research/runs/latest-terminal", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated latest: got %d, want 401", response.Code)
	}

	legacy := &Handler{database: h.db, auth: h.service, research: &fakeResearchService{}}
	if response := call(legacy.listResearchRuns, "/api/v1/research/runs", cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy double list: got %d, want honest 503", response.Code)
	}
	if response := call(legacy.getLatestTerminalResearchRun, "/api/v1/research/runs/latest-terminal", cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy double latest: got %d, want honest 503", response.Code)
	}
	unwired := &Handler{database: h.db, auth: h.service}
	if response := call(unwired.listResearchRuns, "/api/v1/research/runs", cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired list: got %d, want 503", response.Code)
	}
}

func TestResearchRequiresOwnerAndConnectedService(t *testing.T) {
	fake := &fakeResearchService{}
	h := newResearchHTTP(t, fake)
	status, _ := h.do(http.MethodGet, "/research/runs/run-1", "", "", "", "", nil)
	if status == http.StatusOK {
		t.Fatal("unauthenticated read allowed")
	}
	unwired := newResearchHTTP(t, nil)
	unwired.login()
	status, body := unwired.owner(http.MethodGet, "/research/runs/run-1")
	requireStatus(t, status, http.StatusServiceUnavailable, body)
	status, body = unwired.owner(http.MethodPost, "/research/runs", `{"briefText":"x"}`)
	requireStatus(t, status, http.StatusServiceUnavailable, body)
}
