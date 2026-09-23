package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type recordHTTP struct {
	t       *testing.T
	client  *http.Client
	server  *httptest.Server
	db      *store.Store
	service *auth.Service
	cookie  *http.Cookie
	csrf    string
}

func newRecordHTTP(t *testing.T) *recordHTTP {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := auth.NewService(db)
	if err := service.SetupAdministrator(context.Background(), []byte(password)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(db, service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: db}}))
	t.Cleanup(func() { server.Close(); _ = db.Close() })
	return &recordHTTP{t: t, client: server.Client(), server: server, db: db, service: service}
}

func (h *recordHTTP) do(method, path, body, bearer, csrf, requestOrigin string, cookie *http.Cookie, capability ...string) (int, []byte) {
	h.t.Helper()
	request, err := http.NewRequest(method, h.server.URL+"/api/v1"+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if len(capability) > 0 { request.Header.Set("X-Round-Capability",capability[0]) }
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if requestOrigin != "" {
		request.Header.Set("Origin", requestOrigin)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return response.StatusCode, data
}

func (h *recordHTTP) owner(method, path string, body ...string) (int, []byte) {
	value := ""
	if len(body) != 0 {
		value = body[0]
	}
	return h.do(method, path, value, "", h.csrf, origin, h.cookie)
}

func (h *recordHTTP) login() auth.Principal {
	h.t.Helper()
	request, err := http.NewRequest("POST", h.server.URL+"/api/v1/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || len(response.Cookies()) != 1 {
		h.t.Fatalf("owner cookie login: %d cookies=%d", response.StatusCode, len(response.Cookies()))
	}
	h.cookie = response.Cookies()[0]
	// Keep CSRF from the same session as the retained cookie.
	status, data := h.do("GET", "/auth/session", "", "", "", "", h.cookie)
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if status != 200 {
		h.t.Fatalf("session: %d %s", status, data)
	}
	if err := json.Unmarshal(data, &session); err != nil {
		h.t.Fatal(err)
	}
	h.csrf = session.CSRFToken
	p, err := h.service.SessionPrincipal(context.Background(), h.cookie.Value)
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", body, err)
	}
	return value
}

func requireStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status=%d want=%d body=%s", got, want, body)
	}
}
