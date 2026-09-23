package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestCodexOwnerRoutesAndUnavailableState(t *testing.T) {
	h := newHarness(t)
	if r := h.request(http.MethodGet, "/api/v1/codex/status", "", nil, "", "", ""); r.Code != http.StatusUnauthorized {
		t.Fatal(r.Code)
	}
	cookie, csrf := h.login()
	r := h.request(http.MethodGet, "/api/v1/codex/status", "", cookie, "", "", "")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"code":"runner_not_configured"`) || !strings.Contains(r.Body.String(), `"connected":false`) {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if r = h.request(http.MethodPost, "/api/v1/codex/connect", "", cookie, "", "", origin); r.Code != http.StatusForbidden {
		t.Fatal("missing CSRF accepted", r.Code)
	}
	if r = h.request(http.MethodPost, "/api/v1/codex/connect", "", cookie, "", csrf, origin); r.Code != http.StatusServiceUnavailable {
		t.Fatal("missing runner connected", r.Code)
	}
	if r = h.request(http.MethodPost, "/api/v1/codex/connect/cancel", "", cookie, "", csrf, origin); r.Code != http.StatusNoContent {
		t.Fatal(r.Code)
	}
}
