package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type museStubExecutor struct{}

func (museStubExecutor) Execute(context.Context, researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
	return researchcontract.ExecuteOutput{}, nil
}

type museStubCaptures struct{}

func (museStubCaptures) ResolveReceipt(context.Context, string) (researchcontract.ExecutionReceipt, error) {
	return researchcontract.ExecutionReceipt{}, nil
}

func (museStubCaptures) OpenCapture(context.Context, string) (researchcontract.Capture, io.ReadCloser, error) {
	return researchcontract.Capture{}, nil, nil
}

type museStubAssessor struct{}

func (museStubAssessor) Assess(context.Context, researchcontract.AssessInput) (researchcontract.Assessment, error) {
	return researchcontract.Assessment{}, nil
}

type museStubTransport struct{}

func (museStubTransport) Run(_ context.Context, _ musecode.SessionSpec, _ musecode.SessionInput, _ musecode.Cursor, _ musecode.EventSink) error {
	return nil
}

func museWiredHarness(t *testing.T, facts musecode.Facts) *harness {
	t.Helper()
	return museWiredHarnessWithTransport(t, facts, museStubTransport{})
}

func museWiredHarnessWithTransport(t *testing.T, facts musecode.Facts, transport musecode.Transport) *harness {
	t.Helper()
	h := newHarness(t)
	service, err := musewire.NewService(musewire.Deps{
		Facts: facts, Bounds: musecode.DefaultBounds(), Transport: transport,
		Cursors: musewire.StoreCursors{DB: h.db}, DB: h.db,
		Actor:    store.Actor{Kind: "administrator", ID: "owner"},
		Executor: museStubExecutor{}, Captures: museStubCaptures{}, Assessor: museStubAssessor{},
		Workspaces: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Muse: service})
	return h
}

func readyFacts() musecode.Facts {
	return musecode.Facts{CLIPath: "/fixture/muse", CLIReportVersion: "1.4.0",
		EffectiveModel: "fixture-model", SubscriptionLaneProved: true,
		SessionProtocolProved: true, WorkspaceIsolatedProved: true}
}

func TestMuseReadinessHonesty(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	response := h.request("GET", "/api/v1/muse/readiness?tier=contributor", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("unwired readiness: %d", response.Code)
	}
	var unwired struct {
		State string `json:"state"`
		Code  string `json:"code"`
		Tier  string `json:"tier"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &unwired); err != nil {
		t.Fatal(err)
	}
	if unwired.State != "needs-setup" || unwired.Code != musecode.CodeNotConfigured || unwired.Tier != "contributor" {
		t.Fatalf("unwired readiness = %+v, want needs-setup/not-configured", unwired)
	}
	response = h.request("GET", "/api/v1/muse/readiness?tier=ultra", "", cookie, "", "", "")
	if response.Code != 400 {
		t.Fatalf("bad tier: %d, want 400", response.Code)
	}
	response = h.request("GET", "/api/v1/muse/readiness?tier=contributor", "", nil, "", "", "")
	if response.Code != 401 {
		t.Fatalf("unauthenticated readiness: %d, want 401", response.Code)
	}

	wired := museWiredHarness(t, readyFacts())
	cookie, _ = wired.login()
	response = wired.request("GET", "/api/v1/muse/readiness?tier=contributor", "", cookie, "", "", "")
	var ready struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || ready.State != "ready" || ready.Code != musecode.CodeReady {
		t.Fatalf("ready readiness = %d %+v, want ready", response.Code, ready)
	}

	lane := museWiredHarness(t, musecode.Facts{CLIPath: "/fixture/muse", CLIReportVersion: "1.4.0"})
	cookie, _ = lane.login()
	response = lane.request("GET", "/api/v1/muse/readiness?tier=standard", "", cookie, "", "", "")
	var blocked struct {
		State string `json:"state"`
		Code  string `json:"code"`
		Tier  string `json:"tier"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &blocked); err != nil {
		t.Fatal(err)
	}
	if blocked.State != "unavailable" || blocked.Code != musecode.CodeLaneUnverified || blocked.Tier != "standard" {
		t.Fatalf("lane readiness = %+v, want unavailable/lane-unverified", blocked)
	}

	disabled := museWiredHarnessWithTransport(t, readyFacts(), musecode.UnavailableTransport{})
	cookie, _ = disabled.login()
	response = disabled.request("GET", "/api/v1/muse/readiness?tier=contributor", "", cookie, "", "", "")
	var transportBlocked struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &transportBlocked); err != nil {
		t.Fatal(err)
	}
	if transportBlocked.State != "unavailable" || transportBlocked.Code != musecode.CodeProtocolUnverified {
		t.Fatalf("disabled transport readiness = %+v, want unavailable/protocol-unverified", transportBlocked)
	}
}

func TestMuseReadsServeDurableRows(t *testing.T) {
	h := museWiredHarness(t, readyFacts())
	cookie, _ := h.login()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	prefs, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := h.db.StartRound(ctx, store.Actor{Kind: "administrator", ID: "owner"}, store.StartRoundInput{
		RequestKey: "muse-test", Intent: "test", Outcome: "pending", ProfileVersion: prefs.Version,
		Scope: store.RoundScope{Operations: []string{"muse_discover"}}, Limits: store.RoundAllowance{Requests: 1},
		Deadline: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.SaveMuseCursor(ctx, store.MuseCursor{RunRef: "run-1", Tier: "contributor",
		LastSavedReceipt: "vac-0001", SavedCount: 1, SavedRefs: []string{"vac-0001"}, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SaveMuseRunReport(ctx, store.MuseRunReport{RunRef: "run-1", RoundID: round.ID, Tier: "contributor",
		Outcome: "completed", SavedRefs: []string{"vac-0001"}, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	response := h.request("GET", "/api/v1/muse/checkpoints?runRef=run-1", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("checkpoints: %d %s", response.Code, response.Body.String())
	}
	var checkpoints []struct {
		RunRef     string   `json:"runRef"`
		SavedCount int      `json:"savedCount"`
		SavedRefs  []string `json:"savedRefs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &checkpoints); err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 2 || checkpoints[0].SavedCount != 1 {
		t.Fatalf("checkpoints = %+v, want cursor + terminal", checkpoints)
	}
	response = h.request("GET", "/api/v1/muse/report?runRef=run-1", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("report: %d %s", response.Code, response.Body.String())
	}
	var report struct {
		Outcome    string   `json:"outcome"`
		SavedRefs  []string `json:"savedRefs"`
		NextAction string   `json:"nextAction"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "completed" || len(report.SavedRefs) != 1 || report.NextAction == "" {
		t.Fatalf("report = %+v, want completed with action", report)
	}
	response = h.request("GET", "/api/v1/muse/report?runRef=absent", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("absent report: %d, want 404", response.Code)
	}
	response = h.request("GET", "/api/v1/muse/checkpoints", "", cookie, "", "", "")
	if response.Code != 400 {
		t.Fatalf("missing runRef: %d, want 400", response.Code)
	}
	response = h.request("GET", "/api/v1/muse/commissions", "", cookie, "", "", "")
	var commissions struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &commissions); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || commissions.Count != 0 {
		t.Fatalf("commissions = %d %+v, want 0", response.Code, commissions)
	}
	if r := h.request("GET", "/api/v1/muse/report?runRef=run-1", "", nil, "", "", ""); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated report: %d, want 401", r.Code)
	}

	plain := newHarness(t)
	cookie, _ = plain.login()
	if r := plain.request("GET", "/api/v1/muse/report?runRef=run-1", "", cookie, "", "", ""); r.Code != 404 {
		t.Fatalf("unwired report: %d, want 404", r.Code)
	}
}
