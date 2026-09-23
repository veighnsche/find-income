package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestRoundRecommendationCurrentnessWrapperOnlyChangesResponseCopy(t *testing.T) {
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
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "currentness-wrapper", Intent: "Review saved source work", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active"}, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := json.RawMessage(`{"code":"sourced_opportunity_assessed","recommendation":{"status":"unavailable","code":"no_assessed_source","profileVersion":1}}`)
	if _, err := db.FinishRound(ctx, owner, round.ID, store.RoundCompleted, "sourced_opportunity_assessed", "partial", saved); err != nil {
		t.Fatal(err)
	}
	round, err = db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{database: db}
	view := h.roundWithRecommendationCurrentness(ctx, round)
	var response struct {
		RecommendationCurrentness struct {
			Status    string `json:"status"`
			Code      string `json:"code"`
			CheckedAt string `json:"checkedAt"`
		} `json:"recommendationCurrentness"`
	}
	if json.Unmarshal(view.Report, &response) != nil || response.RecommendationCurrentness.Status != "unavailable" || response.RecommendationCurrentness.Code != "recommendation_not_selected" || response.RecommendationCurrentness.CheckedAt == "" {
		t.Fatalf("transient verdict absent: %s", view.Report)
	}
	again, err := db.Round(ctx, round.ID)
	if err != nil || !bytes.Equal(again.Report, saved) || !bytes.Equal(round.Report, saved) {
		t.Fatalf("currentness read changed saved report: %s %v", again.Report, err)
	}
}

func TestLatestCompletedDiscoveryRoundReadRoute(t *testing.T) {
	ctx := context.Background()
	h := newRecordHTTP(t)
	status, body := h.do(http.MethodGet, "/rounds/latest-completed?outcome=discover", "", "", "", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, body)
	owner := h.login()
	status, body = h.owner(http.MethodGet, "/rounds/latest-completed?outcome=discover")
	requireStatus(t, status, http.StatusOK, body)
	if string(bytes.TrimSpace(body)) != "null" {
		t.Fatalf("empty latest completed response = %s", body)
	}
	for _, path := range []string{"/rounds/latest-completed", "/rounds/latest-completed?outcome=prepare", "/rounds/latest-completed?outcome=discover&outcome=discover"} {
		status, body = h.owner(http.MethodGet, path)
		requireStatus(t, status, http.StatusBadRequest, body)
	}
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := h.db.StartRound(ctx, owner.Actor(), store.StartRoundInput{RequestKey: "latest-discovery", Intent: "Read saved work", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active"}, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.ActivateRound(ctx, owner.Actor(), round.ID); err != nil {
		t.Fatal(err)
	}
	status, body = h.owner(http.MethodGet, "/rounds/latest-completed?outcome=discover")
	requireStatus(t, status, http.StatusOK, body)
	if string(bytes.TrimSpace(body)) != "null" {
		t.Fatalf("active round exposed as completed: %s", body)
	}
	saved := json.RawMessage(`{"code":"sourced_opportunity_assessed","recommendation":{"status":"unavailable","code":"no_assessed_source","profileVersion":1}}`)
	if _, err := h.db.FinishRound(ctx, owner.Actor(), round.ID, store.RoundCompleted, "sourced_opportunity_assessed", "partial", saved); err != nil {
		t.Fatal(err)
	}
	status, body = h.owner(http.MethodGet, "/rounds/latest-completed?outcome=discover")
	requireStatus(t, status, http.StatusOK, body)
	var view struct {
		ID     string `json:"id"`
		Report struct {
			RecommendationCurrentness struct {
				Status string `json:"status"`
				Code   string `json:"code"`
			} `json:"recommendationCurrentness"`
		} `json:"report"`
	}
	if json.Unmarshal(body, &view) != nil || view.ID != round.ID || view.Report.RecommendationCurrentness.Status != "unavailable" || view.Report.RecommendationCurrentness.Code != "recommendation_not_selected" {
		t.Fatalf("latest completed response lacked currentness: %s", body)
	}
	stored, err := h.db.Round(ctx, round.ID)
	if err != nil || !bytes.Equal(stored.Report, saved) {
		t.Fatalf("read route mutated saved round: %s %v", stored.Report, err)
	}
}
