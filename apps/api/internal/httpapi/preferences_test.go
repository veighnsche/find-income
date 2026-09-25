package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func preferencesBody(t *testing.T, version int64) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"expectedVersion":     version,
		"preferredLocation":   "Amsterdam",
		"allowRemote":         true,
		"allowHybrid":         false,
		"targetHours":         "32",
		"minMonthlyBaseCents": 450000,
		"salaryCurrency":      "EUR",
		"timezone":            "Europe/Amsterdam",
		"roleCriteria": []map[string]any{
			{"id": "backend-work", "label": "Backend engineering", "description": "Go services and APIs", "kind": "role", "mode": "require"},
			{"id": "no-oncall", "label": "No on-call rota", "description": "", "kind": "responsibility", "mode": "avoid"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestUpdatePreferencesRoundTrip(t *testing.T) {
	h := newRecordHTTP(t)
	status, _ := h.do(http.MethodPut, "/preferences", preferencesBody(t, 1), "", "", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, nil)

	h.login()
	status, body := h.owner(http.MethodGet, "/preferences")
	requireStatus(t, status, http.StatusOK, body)
	current := decodeObject(t, body)
	version, ok := current["version"].(float64)
	if !ok || version < 1 {
		t.Fatalf("current version missing: %s", body)
	}

	status, body = h.owner(http.MethodPut, "/preferences", preferencesBody(t, int64(version)))
	requireStatus(t, status, http.StatusOK, body)
	saved := decodeObject(t, body)
	changeID, _ := saved["changeId"].(string)
	prefs, _ := saved["preferences"].(map[string]any)
	if changeID == "" || prefs == nil || prefs["version"].(float64) != version+1 {
		t.Fatalf("saved profile missing version/change: %s", body)
	}
	if prefs["preferredLocation"] != "Amsterdam" || prefs["targetHours"] != "32" {
		t.Fatalf("saved profile lost fields: %s", body)
	}
	criteria, _ := prefs["roleCriteria"].([]any)
	if len(criteria) != 2 {
		t.Fatalf("saved criteria count: %s", body)
	}

	status, body = h.owner(http.MethodGet, "/preferences")
	requireStatus(t, status, http.StatusOK, body)
	if decodeObject(t, body)["version"].(float64) != version+1 {
		t.Fatalf("saved profile did not persist: %s", body)
	}
}

func TestUpdatePreferencesRejectsStaleAndInvalid(t *testing.T) {
	h := newRecordHTTP(t)
	h.login()
	status, body := h.owner(http.MethodGet, "/preferences")
	requireStatus(t, status, http.StatusOK, body)
	version := int64(decodeObject(t, body)["version"].(float64))

	status, body = h.owner(http.MethodPut, "/preferences", preferencesBody(t, version))
	requireStatus(t, status, http.StatusOK, body)

	status, body = h.owner(http.MethodPut, "/preferences", preferencesBody(t, version))
	requireStatus(t, status, http.StatusConflict, body)

	status, body = h.owner(http.MethodPut, "/preferences", `{"expectedVersion":99}`)
	requireStatus(t, status, http.StatusBadRequest, body)

	invalid := preferencesBody(t, version+1)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(invalid), &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["targetHours"] = "not-a-number"
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.owner(http.MethodPut, "/preferences", string(raw))
	requireStatus(t, status, http.StatusBadRequest, body)
}
