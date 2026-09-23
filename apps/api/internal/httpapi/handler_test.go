package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const origin = "http://dashboard.test"
const password = "synthetic-browser-password-2026"

type harness struct {
	t       *testing.T
	db      *store.Store
	service *auth.Service
	handler http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := auth.NewService(db)
	if err := service.SetupAdministrator(context.Background(), []byte(password)); err != nil {
		t.Fatal(err)
	}
	return &harness{t, db, service, NewHandler(db, service, Options{AllowedOrigins: []string{origin}})}
}

func (h *harness) request(method, path, body string, cookie *http.Cookie, bearer, csrf, requestOrigin string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if requestOrigin != "" {
		request.Header.Set("Origin", requestOrigin)
	}
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response
}

func (h *harness) login() (*http.Cookie, string) {
	h.t.Helper()
	response := h.request("POST", "/api/v1/auth/login", `{"password":"`+password+`"}`, nil, "", "", origin)
	if response.Code != 200 {
		h.t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		h.t.Fatalf("session cookies: %d", len(cookies))
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		h.t.Fatal(err)
	}
	return cookies[0], body.CSRFToken
}

func TestPrivateRoutesCookieCSRFAndLogout(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/v1/preferences", "/api/v1/agent-credentials", "/api/v1/private-asset"} {
		response := h.request("GET", path, "", nil, "", "", "")
		if response.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, response.Code)
		}
	}
	cookie, csrf := h.login()
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/v1" {
		t.Fatalf("unsafe cookie settings: %+v", cookie)
	}
	var storedSessionHash string
	if err := h.db.Read(context.Background(), func(reader store.Reader) error {
		return reader.QueryRowContext(context.Background(), "SELECT token_hash FROM auth_sessions LIMIT 1").Scan(&storedSessionHash)
	}); err != nil || storedSessionHash == cookie.Value || len(storedSessionHash) != 64 {
		t.Fatalf("session token was not stored hashed: length=%d err=%v", len(storedSessionHash), err)
	}
	status := h.request("GET", "/api/v1/auth/session", "", cookie, "", "", "")
	if status.Code != 200 || !bytes.Contains(status.Body.Bytes(), []byte(csrf)) {
		t.Fatalf("session: %d %s", status.Code, status.Body.String())
	}
	for _, tc := range []struct{ origin, csrf string }{{"", csrf}, {"http://evil.test", csrf}, {origin, "wrong"}, {origin, ""}} {
		response := h.request("POST", "/api/v1/agent-credentials", `{"name":"test","scopes":["preferences:read"],"expiresAt":"2030-01-01T00:00:00Z"}`, cookie, "", tc.csrf, tc.origin)
		if response.Code != 403 {
			t.Fatalf("CSRF/origin case %+v: %d %s", tc, response.Code, response.Body.String())
		}
	}
	mixed := h.request("GET", "/api/v1/preferences", "", cookie, "anything", "", "")
	if mixed.Code != 400 {
		t.Fatalf("mixed auth: %d", mixed.Code)
	}
	logout := h.request("POST", "/api/v1/auth/logout", "", cookie, "", csrf, origin)
	if logout.Code != 204 {
		t.Fatalf("logout: %d %s", logout.Code, logout.Body.String())
	}
	if response := h.request("GET", "/api/v1/auth/session", "", cookie, "", "", ""); response.Code != 401 {
		t.Fatalf("revoked session still works: %d", response.Code)
	}
}

func TestAgentScopesRevocationExpiryAndAudit(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	expiry := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	create := func(name string, scopes []string) (string, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"name": name, "scopes": scopes, "expiresAt": expiry})
		response := h.request("POST", "/api/v1/agent-credentials", string(body), cookie, "", csrf, origin)
		if response.Code != 201 {
			t.Fatalf("create agent: %d %s", response.Code, response.Body.String())
		}
		var created struct {
			Credential struct {
				ID string `json:"id"`
			} `json:"credential"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.Credential.ID, created.Token
	}
	readID, readToken := create("reader", []string{"preferences:read"})
	_, writeToken := create("writer", []string{"actions:write"})
	var storedAgentHash string
	if err := h.db.Read(context.Background(), func(reader store.Reader) error {
		return reader.QueryRowContext(context.Background(), "SELECT token_hash FROM agent_credentials WHERE id=?", readID).Scan(&storedAgentHash)
	}); err != nil || storedAgentHash == readToken || len(storedAgentHash) != 64 {
		t.Fatalf("agent token was not stored hashed: length=%d err=%v", len(storedAgentHash), err)
	}
	duplicateBody, _ := json.Marshal(map[string]any{"name": "reader", "scopes": []string{"preferences:read"}, "expiresAt": expiry})
	duplicate := h.request("POST", "/api/v1/agent-credentials", string(duplicateBody), cookie, "", csrf, origin)
	if duplicate.Code != 409 {
		t.Fatalf("duplicate name: %d %s", duplicate.Code, duplicate.Body.String())
	}
	list := h.request("GET", "/api/v1/agent-credentials", "", cookie, "", "", "")
	if list.Code != 200 || bytes.Contains(list.Body.Bytes(), []byte(readToken)) || bytes.Contains(list.Body.Bytes(), []byte(writeToken)) {
		t.Fatalf("credential listing leaked tokens: %d %s", list.Code, list.Body.String())
	}
	if response := h.request("GET", "/api/v1/preferences", "", nil, readToken, "", ""); response.Code != 200 {
		t.Fatalf("scoped reader: %d %s", response.Code, response.Body.String())
	}
	if response := h.request("GET", "/api/v1/preferences", "", nil, writeToken, "", ""); response.Code != 403 {
		t.Fatalf("wrong scope: %d", response.Code)
	}
	if response := h.request("GET", "/api/v1/agent-credentials", "", nil, readToken, "", ""); response.Code != 403 {
		t.Fatalf("agent administered accounts: %d", response.Code)
	}
	if response := h.request("PATCH", "/api/v1/preferences", `{"targetHours":40}`, nil, readToken, "", origin); response.Code != 404 {
		t.Fatalf("agent mutated preferences: %d", response.Code)
	}
	bad := h.request("POST", "/api/v1/agent-credentials", `{"name":"forged","scopes":["preferences:read"],"expiresAt":"2030-01-01T00:00:00Z","actorId":"admin"}`, cookie, "", csrf, origin)
	if bad.Code != 400 {
		t.Fatalf("caller actor accepted: %d", bad.Code)
	}
	var auditActor string
	err := h.db.Read(context.Background(), func(reader store.Reader) error {
		return reader.QueryRowContext(context.Background(), "SELECT actor_id FROM audit_changes WHERE entity_id=? AND operation='agent.create'", readID).Scan(&auditActor)
	})
	if err != nil || auditActor != "owner" {
		t.Fatalf("audit actor=%s err=%v", auditActor, err)
	}
	revoke := h.request("POST", "/api/v1/agent-credentials/"+readID+"/revoke", "", cookie, "", csrf, origin)
	if revoke.Code != 204 {
		t.Fatalf("revoke: %d %s", revoke.Code, revoke.Body.String())
	}
	if response := h.request("GET", "/api/v1/preferences", "", nil, readToken, "", ""); response.Code != 401 {
		t.Fatalf("revoked token still works: %d", response.Code)
	}
	h.service.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) })
	if response := h.request("GET", "/api/v1/preferences", "", nil, writeToken, "", ""); response.Code != 401 {
		t.Fatalf("expired token still works: %d", response.Code)
	}
}

func TestWrongPasswordRateLimitAndLoginOrigin(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		response := h.request("POST", "/api/v1/auth/login", `{"password":"wrong-password"}`, nil, "", "", origin)
		if response.Code != 401 {
			t.Fatalf("wrong password %d: %d", i, response.Code)
		}
	}
	response := h.request("POST", "/api/v1/auth/login", `{"password":"`+password+`"}`, nil, "", "", origin)
	if response.Code != 429 {
		t.Fatalf("rate limit: %d", response.Code)
	}
	other := newHarness(t)
	response = other.request("POST", "/api/v1/auth/login", `{"password":"`+password+`"}`, nil, "", "", "http://evil.test")
	if response.Code != 403 {
		t.Fatalf("login origin: %d", response.Code)
	}
	response = other.request("POST", "/api/v1/auth/login", `{"password":"`+password+`"}`, nil, "agent-token", "", origin)
	if response.Code != 400 {
		t.Fatalf("bearer login ambiguity: %d", response.Code)
	}
}

func TestHostedLoginSetsSecureCookie(t *testing.T) {
	h := newHarness(t)
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{"https://dashboard.test"}, SecureCookies: true})
	response := h.request("POST", "/api/v1/auth/login", `{"password":"`+password+`"}`, nil, "", "", "https://dashboard.test")
	if response.Code != 200 {
		t.Fatalf("hosted login: %d %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("hosted session cookie settings: %+v", cookies)
	}
}
