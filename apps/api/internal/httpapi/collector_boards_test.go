package httpapi

import (
	"net/http"
	"testing"
)

func TestCollectorBoardOwnerConfigurationAndHonestStatus(t *testing.T) {
	h := newRecordHTTP(t)
	status, body := h.do(http.MethodGet, "/collector-boards", "", "", "", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, body)
	h.login()
	status, body = h.owner(http.MethodGet, "/runtime-status")
	requireStatus(t, status, http.StatusOK, body)
	if got := decodeObject(t, body)["collectionAvailable"]; got != false {
		t.Fatalf("collector loop unexpectedly mounted: %v", got)
	}
	status, body = h.owner(http.MethodGet, "/collector-boards")
	requireStatus(t, status, http.StatusOK, body)
	if got := decodeObject(t, body)["items"].([]any); len(got) == 0 || got[0].(map[string]any)["displayName"] == "" || got[0].(map[string]any)["verifiedAt"] == nil {
		t.Fatalf("fresh verified board defaults: %+v", got)
	}
	input := `{"provider":"lever","site":"synthetic-qa","region":"global","enabled":true,"intervalMinutes":60}`
	status, body = h.do(http.MethodPost, "/collector-boards", input, "", "", "", h.cookie)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = h.owner(http.MethodPost, "/collector-boards", input)
	requireStatus(t, status, http.StatusCreated, body)
	board := decodeObject(t, body)
	id := board["id"].(string)
	if board["lastRunAt"] != nil || board["lastSuccessAt"] != nil || board["enabled"] != true {
		t.Fatalf("new board falsely reports a scan: %+v", board)
	}
	status, body = h.owner(http.MethodPost, "/collector-boards", input)
	requireStatus(t, status, http.StatusConflict, body)
	status, body = h.owner(http.MethodPut, "/collector-boards/"+id, `{"expectedRevision":1,"enabled":false,"intervalMinutes":120}`)
	requireStatus(t, status, http.StatusOK, body)
	if got := decodeObject(t, body)["enabled"]; got != false {
		t.Fatalf("board was not paused: %v", got)
	}
	status, body = h.owner(http.MethodPut, "/collector-boards/"+id, `{"expectedRevision":1,"enabled":true,"intervalMinutes":60}`)
	requireStatus(t, status, http.StatusConflict, body)
}
