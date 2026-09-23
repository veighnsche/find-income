package httpapi

import (
	"net/http"
	"testing"
)

func TestCodexStatusAndConnectRequireOwner(t *testing.T) {
	h := newRecordHTTP(t)
	status, body := h.do(http.MethodGet, "/codex/status", "", "", "", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, body)
	h.login()
	status, body = h.owner(http.MethodGet, "/codex/status")
	requireStatus(t, status, http.StatusOK, body)
	view := decodeObject(t, body)
	if view["state"] != "unavailable" || view["code"] != "runner_not_configured" || view["ingestionAvailable"] != false || view["userCode"] != nil {
		t.Fatalf("unavailable runner status: %+v", view)
	}
	status, body = h.do(http.MethodPost, "/codex/connect", "", "", "", "", h.cookie)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = h.owner(http.MethodPost, "/codex/connect")
	requireStatus(t, status, http.StatusServiceUnavailable, body)
}
