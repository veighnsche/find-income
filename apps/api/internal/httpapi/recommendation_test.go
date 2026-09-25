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
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "currentness-wrapper", Intent: "Review saved source work", Outcome: "process_input", ProfileVersion: profile.Version,
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

func TestLatestCompletedPrepareReadIsOwnerAndOutcomeScoped(t *testing.T) {
	ctx := context.Background()
	h := newRecordHTTP(t)
	path := "/rounds/latest-completed?outcome=prepare"
	status, body := h.do(http.MethodGet, path, "", "", "", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, body)
	owner := h.login()
	status, body = h.owner(http.MethodGet, path)
	requireStatus(t, status, http.StatusOK, body)
	if string(bytes.TrimSpace(body)) != "null" {
		t.Fatalf("empty prepare history: %s", body)
	}
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finish := func(actor store.Actor, key, outcome string) store.Round {
		t.Helper()
		round, _, err := h.db.StartRound(ctx, actor, store.StartRoundInput{
			RequestKey: key, Intent: "Read a saved commissioned result", Outcome: outcome, ProfileVersion: profile.Version,
			Scope:  store.RoundScope{Resources: []string{"campaign:active"}, Operations: []string{store.RoundJevRequest}},
			Limits: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1, Turns: 1}, Deadline: time.Now().Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.ActivateRound(ctx, actor, round.ID); err != nil {
			t.Fatal(err)
		}
		status, body := h.owner(http.MethodGet, path)
		requireStatus(t, status, http.StatusOK, body)
		var activeView *roundResponse
		if json.Unmarshal(body, &activeView) != nil || activeView != nil && activeView.ID == round.ID {
			t.Fatalf("active round returned as complete: %s", body)
		}
		round, err = h.db.FinishRound(ctx, actor, round.ID, store.RoundCompleted, "saved", "partial", json.RawMessage(`{"code":"saved","packId":"saved-pack"}`))
		if err != nil {
			t.Fatal(err)
		}
		return round
	}
	prepared := finish(owner.Actor(), "owner-prepare", "prepare")
	processed := finish(owner.Actor(), "owner-processed", "process_input")
	finish(store.Actor{Kind: "administrator", ID: "different-owner"}, "foreign-prepare", "prepare")
	for _, expected := range []store.Round{prepared, processed} {
		status, body = h.owner(http.MethodGet, "/rounds/latest-completed?outcome="+expected.Outcome)
		requireStatus(t, status, http.StatusOK, body)
		var view roundResponse
		if json.Unmarshal(body, &view) != nil || view.ID != expected.ID || view.Outcome != expected.Outcome || view.RequestKey != expected.RequestKey {
			t.Fatalf("wrong owner/outcome result: %s", body)
		}
		if expected.Outcome == "prepare" {
			var projected struct {
				Code                      string `json:"code"`
				PackID                    string `json:"packId"`
				RecommendationCurrentness struct {
					Status string `json:"status"`
				} `json:"recommendationCurrentness"`
			}
			if json.Unmarshal(view.Report, &projected) != nil || projected.Code != "saved" || projected.PackID != "saved-pack" || projected.RecommendationCurrentness.Status != "unavailable" {
				t.Fatalf("prepare report projection lost result fields: %s", body)
			}
		}
		stored, err := h.db.Round(ctx, expected.ID)
		if err != nil || !bytes.Equal(stored.Report, expected.Report) || stored.Used != expected.Used || stored.Revision != expected.Revision {
			t.Fatalf("read changed stored report, allowance or revision: %+v %v", stored, err)
		}
	}
	status, body = h.owner(http.MethodGet, "/rounds/latest-completed?outcome=all")
	requireStatus(t, status, http.StatusOK, body)
	if decodeObject(t, body)["id"] != processed.ID {
		t.Fatalf("latest all-outcome owner result was not recovered: %s", body)
	}
	status, body = h.owner(http.MethodGet, path+"&outcome=process_input")
	requireStatus(t, status, http.StatusBadRequest, body)
	for _, removed := range []string{"compare_offers", "interview_prepare", "interview_debrief", "process_replies"} {
		status, body = h.owner(http.MethodGet, "/rounds/latest-completed?outcome="+removed)
		requireStatus(t, status, http.StatusBadRequest, body)
	}
}
