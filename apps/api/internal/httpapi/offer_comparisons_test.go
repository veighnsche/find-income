package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type offerHTTPWorker struct{}

func (offerHTTPWorker) CheckRound(context.Context, string) error       { return nil }
func (offerHTTPWorker) LaunchRound(context.Context, store.Round) error { return nil }
func (offerHTTPWorker) CancelRound(string)                             {}

func TestCompareOffersRouteUsesOwnerCSRFAndExactIntakeReplay(t *testing.T) {
	h := newRecordHTTP(t)
	h.login()
	h.server.Close()
	worker := offerHTTPWorker{}
	h.server = httptest.NewServer(NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: h.db, Readiness: worker, Worker: worker}}))
	t.Cleanup(h.server.Close)
	body := `{"requestKey":"comparison-click","offers":["Fixture Labs offers a role. Clarify weekly hours."],"prioritiesText":"I value predictable hours."}`
	status, data := h.do(http.MethodPost, "/rounds/compare-offers", body, "", "", origin, h.cookie)
	requireStatus(t, status, http.StatusForbidden, data)
	status, data = h.owner(http.MethodPost, "/rounds/compare-offers", body)
	requireStatus(t, status, http.StatusCreated, data)
	first := decodeObject(t, data)
	if first["intakeId"] == "" || first["round"].(map[string]any)["outcome"] != "compare_offers" {
		t.Fatalf("response: %s", data)
	}
	status, data = h.owner(http.MethodPost, "/rounds/compare-offers", body)
	requireStatus(t, status, http.StatusOK, data)
	if decodeObject(t, data)["intakeId"] != first["intakeId"] {
		t.Fatal("retry changed intake")
	}
	status, data = h.owner(http.MethodPost, "/rounds/compare-offers", `{"requestKey":"comparison-click","offers":["Different text"]}`)
	requireStatus(t, status, http.StatusConflict, data)
	roundID := first["round"].(map[string]any)["id"].(string)
	status, data = h.owner(http.MethodGet, "/offer-comparisons/by-round/"+roundID)
	requireStatus(t, status, http.StatusNotFound, data)
	status, data = h.do(http.MethodGet, "/offer-comparisons/by-round/"+roundID, "", "", "", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, data)
}
