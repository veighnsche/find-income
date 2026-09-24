package httpapi

import (
	"context"
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
