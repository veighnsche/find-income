package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestOwnerReplacesPreferencesWithVersionAndExactHours(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	current := h.request("GET", "/api/v1/preferences", "", cookie, "", "", "")
	if current.Code != http.StatusOK {
		t.Fatalf("read preferences: %d %s", current.Code, current.Body.String())
	}
	var before struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal(current.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"expectedVersion":     before.Version,
		"preferredLocation":   "",
		"allowRemote":         true,
		"allowHybrid":         false,
		"targetHours":         "37.5",
		"minMonthlyBaseCents": 620000,
		"salaryCurrency":      "USD",
		"timezone":            "Europe/Brussels",
		"roleCriteria": []map[string]string{
			{"id": "api-platform", "label": "API platform work", "description": "Own API design and operations", "kind": "role", "mode": "require"},
			{"id": "database-design", "label": "Database design", "description": "Prefer schema ownership", "kind": "responsibility", "mode": "prefer"},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if response := h.request("PUT", "/api/v1/preferences", string(body), cookie, "", "", origin); response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF was accepted: %d %s", response.Code, response.Body.String())
	}
	updated := h.request("PUT", "/api/v1/preferences", string(body), cookie, "", csrf, origin)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updated.Code, updated.Body.String())
	}
	var saved struct {
		Preferences struct {
			Version           int64  `json:"version"`
			TargetHours       string `json:"targetHours"`
			PreferredLocation string `json:"preferredLocation"`
			RoleCriteria      []struct {
				ID             string `json:"id"`
				DefinitionHash string `json:"definitionHash"`
			} `json:"roleCriteria"`
		} `json:"preferences"`
		ChangeID string `json:"changeId"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Preferences.Version != before.Version+1 || saved.Preferences.TargetHours != "37.5" ||
		saved.Preferences.PreferredLocation != "" || len(saved.Preferences.RoleCriteria) != 2 ||
		len(saved.Preferences.RoleCriteria[0].DefinitionHash) != 64 || saved.ChangeID == "" {
		t.Fatalf("saved complete policy incorrectly: %s", updated.Body.String())
	}
	stale := h.request("PUT", "/api/v1/preferences", string(body), cookie, "", csrf, origin)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale version: %d %s", stale.Code, stale.Body.String())
	}
	previous, err := h.db.PreferenceVersion(context.Background(), before.Version)
	if err != nil || previous.Version != before.Version {
		t.Fatalf("historical preferences lost: %+v %v", previous, err)
	}
	invalid := map[string]any{}
	for key, value := range request {
		invalid[key] = value
	}
	invalid["expectedVersion"] = saved.Preferences.Version
	invalid["targetHours"] = "37.555"
	invalidBody, _ := json.Marshal(invalid)
	if response := h.request("PUT", "/api/v1/preferences", string(invalidBody), cookie, "", csrf, origin); response.Code != http.StatusBadRequest {
		t.Fatalf("fractional precision accepted: %d %s", response.Code, response.Body.String())
	}
	owner, err := h.service.SessionPrincipal(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := h.service.CreateAgent(context.Background(), owner, "reader", []string{"preferences:read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if response := h.request("PUT", "/api/v1/preferences", string(body), nil, token, "", origin); response.Code != http.StatusForbidden {
		t.Fatalf("agent preference write accepted: %d %s", response.Code, response.Body.String())
	}
}
