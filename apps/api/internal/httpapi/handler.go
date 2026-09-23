package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type Options struct {
	AllowedOrigins []string
	SecureCookies  bool
}

type Handler struct {
	auth          *auth.Service
	database      *store.Store
	origins       map[string]bool
	secureCookies bool
	limiter       *loginLimiter
}

func NewHandler(database *store.Store, service *auth.Service, options Options) http.Handler {
	h := &Handler{auth: service, database: database, origins: map[string]bool{}, secureCookies: options.SecureCookies,
		limiter: newLoginLimiter()}
	for _, origin := range options.AllowedOrigins {
		h.origins[origin] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", h.health)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("GET /api/v1/auth/session", h.session)
	mux.HandleFunc("GET /api/v1/preferences", h.preferences)
	mux.HandleFunc("GET /api/v1/agent-credentials", h.listAgents)
	mux.HandleFunc("POST /api/v1/agent-credentials", h.createAgent)
	mux.HandleFunc("POST /api/v1/agent-credentials/{id}/revoke", h.revokeAgent)
	mux.HandleFunc("/api/v1/", h.privateNotFound)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, generated.HealthResponse{Status: generated.Ok, Service: generated.JobseekApi, Version: "0.1.0"})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Bearer credentials cannot be used for owner login.")
		return
	}
	if !h.sameOrigin(r) {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeCsrfFailed, "Request origin is not allowed.")
		return
	}
	if !h.limiter.Allow(remoteIP(r.RemoteAddr)) {
		w.Header().Set("Retry-After", "60")
		fail(w, http.StatusTooManyRequests, generated.ApiErrorCodeRateLimited, "Try again shortly.")
		return
	}
	var request generated.LoginRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Password == nil || *request.Password == "" {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Password is required.")
		return
	}
	session, err := h.auth.Login(r.Context(), []byte(*request.Password))
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			fail(w, http.StatusUnauthorized, generated.ApiErrorCodeUnauthenticated, "Invalid credentials.")
			return
		}
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not start a session.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: session.Token, Path: "/api/v1",
		HttpOnly: true, Secure: h.secureCookies, SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt})
	writeJSON(w, http.StatusOK, generated.SessionResponse{ActorKind: generated.Administrator, ActorId: "owner",
		CsrfToken: session.CSRFToken, ExpiresAt: session.ExpiresAt})
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, generated.SessionResponse{ActorKind: generated.Administrator, ActorId: principal.ID,
		CsrfToken: principal.CSRFToken, ExpiresAt: principal.ExpiresAt})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, principal) {
		return
	}
	if err := h.auth.Logout(r.Context(), principal); err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not end the session.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/api/v1", HttpOnly: true,
		Secure: h.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) preferences(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	if !principal.HasScope("preferences:read") {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Scope is required.")
		return
	}
	p, err := h.database.CurrentPreferences(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not load preferences.")
		return
	}
	writeJSON(w, http.StatusOK, generated.PreferencesResponse{Version: p.Version, PreferredLocation: p.PreferredLocation,
		AllowRemote: p.AllowRemote, AllowHybrid: p.AllowHybrid, TargetHours: p.TargetHours,
		MinMonthlyBaseCents: p.MinMonthlyBaseCents, SalaryCurrency: p.SalaryCurrency,
		RequireBackendPlatform: p.RequireBackendPlatform, ExcludeFrontendDuties: p.ExcludeFrontendDuties,
		ExcludePHPFocused: p.ExcludePHPFocused, Timezone: p.Timezone})
}

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok {
		return
	}
	records, err := h.auth.ListAgents(r.Context(), principal)
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not list agent credentials.")
		return
	}
	items := make([]generated.AgentCredential, 0, len(records))
	for _, record := range records {
		items = append(items, agentModel(record))
	}
	writeJSON(w, http.StatusOK, generated.AgentCredentialList{Items: items})
}

func (h *Handler) createAgent(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, principal) {
		return
	}
	var request generated.CreateAgentCredentialRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	credential, token, err := h.auth.CreateAgent(r.Context(), principal, request.Name, request.Scopes, request.ExpiresAt)
	if err != nil {
		if errors.Is(err, auth.ErrInvalid) {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Check the name, scopes and expiry.")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "An agent with this name already exists.")
			return
		}
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not create the agent credential.")
		return
	}
	writeJSON(w, http.StatusCreated, generated.CreatedAgentCredential{Credential: agentModel(credential), Token: token})
}

func (h *Handler) revokeAgent(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, principal) {
		return
	}
	if err := h.auth.RevokeAgent(r.Context(), principal, r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Agent credential not found.")
			return
		}
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not revoke agent credential.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) privateNotFound(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.principal(w, r); !ok {
		return
	}
	fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Resource not found.")
}

func (h *Handler) principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, err := h.auth.Authenticate(r.Context(), r)
	if err != nil {
		if errors.Is(err, auth.ErrMixedCredentials) {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Choose one authentication method.")
			return auth.Principal{}, false
		}
		fail(w, http.StatusUnauthorized, generated.ApiErrorCodeUnauthenticated, "Authentication is required.")
		return auth.Principal{}, false
	}
	return principal, true
}

func (h *Handler) owner(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := h.principal(w, r)
	if !ok {
		return auth.Principal{}, false
	}
	if !principal.IsOwner() {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Owner session required.")
		return auth.Principal{}, false
	}
	return principal, true
}

func (h *Handler) mutationAllowed(w http.ResponseWriter, r *http.Request, principal auth.Principal) bool {
	if !h.sameOrigin(r) || !auth.ValidateCSRF(principal, r.Header.Get("X-CSRF-Token")) {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeCsrfFailed, "Origin or CSRF token is invalid.")
		return false
	}
	return true
}

func (h *Handler) sameOrigin(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return h.origins[u.Scheme+"://"+u.Host]
}

func agentModel(record auth.AgentCredential) generated.AgentCredential {
	return generated.AgentCredential{Id: record.ID, Name: record.Name, Scopes: record.Scopes,
		CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt, Revoked: record.Revoked}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "JSON content type is required.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid JSON request.")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Only one JSON value is allowed.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, code generated.ApiErrorCode, message string) {
	writeJSON(w, status, generated.ErrorEnvelope{Error: generated.ApiError{Code: code, Message: message}})
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

type loginLimiter struct {
	mu     sync.Mutex
	now    func() time.Time
	byIP   map[string][]time.Time
	global []time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{now: time.Now, byIP: map[string][]time.Time{}}
}

func (l *loginLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-time.Minute)
	keep := func(values []time.Time) []time.Time {
		kept := values[:0]
		for _, value := range values {
			if value.After(cutoff) {
				kept = append(kept, value)
			}
		}
		return kept
	}
	l.global = keep(l.global)
	attempts := keep(l.byIP[ip])
	if len(attempts) >= 5 || len(l.global) >= 30 {
		l.byIP[ip] = attempts
		return false
	}
	now := l.now()
	l.byIP[ip] = append(attempts, now)
	l.global = append(l.global, now)
	if len(l.byIP) > 1024 {
		for key, values := range l.byIP {
			if len(keep(values)) == 0 {
				delete(l.byIP, key)
			}
		}
	}
	return true
}
