package httpapi

import (
	"context"
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
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type Options struct {
	AllowedOrigins        []string
	SecureCookies         bool
	IngestionAvailable    bool
	OrganisationAvailable bool
	Codex                 CodexControl
	Rounds                *rounds.Service
	Research              ResearchService
	ResearchControl       ResearchRunControl
	AnswerMatcher         AnswerMatcher
	Materials             MaterialPreparer
	Muse                  *musewire.Service
	MuseCheck             musewire.CheckPerformer
}

type CodexControl interface {
	Status(context.Context) codexservice.Status
	Connect(context.Context) (codexservice.Connection, error)
	CancelConnect(context.Context) error
	MCPHandler() http.Handler
}

type Handler struct {
	auth                  *auth.Service
	database              *store.Store
	origins               map[string]bool
	secureCookies         bool
	ingestionAvailable    bool
	organisationAvailable bool
	codex                 CodexControl
	rounds                *rounds.Service
	research              ResearchService
	researchControl       ResearchRunControl
	answerMatcher         AnswerMatcher
	materials             MaterialPreparer
	muse                  *musewire.Service
	museCheck             musewire.CheckPerformer
	limiter               *loginLimiter
}

func NewHandler(database *store.Store, service *auth.Service, options Options) http.Handler {
	h := &Handler{auth: service, database: database, origins: map[string]bool{}, secureCookies: options.SecureCookies,
		ingestionAvailable: options.IngestionAvailable, organisationAvailable: options.OrganisationAvailable,
		codex:           options.Codex,
		rounds:          options.Rounds,
		research:        options.Research,
		researchControl: options.ResearchControl,
		answerMatcher:   options.AnswerMatcher,
		materials:       options.Materials,
		muse:            options.Muse,
		museCheck:       options.MuseCheck,
		limiter:         newLoginLimiter()}
	for _, origin := range options.AllowedOrigins {
		h.origins[origin] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", h.health)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("GET /api/v1/auth/session", h.session)
	mux.HandleFunc("GET /api/v1/preferences", h.preferences)
	// No interview, correspondence, or reply routes: the downstream
	// lifecycle after manual handoff is out of scope.
	mux.HandleFunc("GET /api/v1/rounds/active", h.activeRound)
	mux.HandleFunc("GET /api/v1/rounds/latest-completed", h.latestCompletedRound)
	mux.HandleFunc("POST /api/v1/rounds/prepare", h.prepareRound)
	// No offer-comparison routes: the downstream lifecycle after manual
	// handoff is out of scope.
	mux.HandleFunc("POST /api/v1/rounds/process-input", h.processInput)
	mux.HandleFunc("GET /api/v1/rounds/{id}", h.getRound)
	mux.HandleFunc("GET /api/v1/rounds/{id}/results", h.roundResults)
	mux.HandleFunc("GET /api/v1/rounds/{id}/history", h.roundHistory)
	mux.HandleFunc("POST /api/v1/rounds/{id}/stop", h.stopAnyRound)
	mux.HandleFunc("POST /api/v1/rounds/{id}/resume", h.resumeAnyRound)
	mux.HandleFunc("POST /api/v1/rounds/{id}/mutations", h.roundMutation)
	mux.HandleFunc("POST /api/v1/research/runs", h.commissionResearchRun)
	mux.HandleFunc("GET /api/v1/research/runs/{id}", h.getResearchRun)
	mux.HandleFunc("POST /api/v1/research/runs/{id}/steer", h.steerResearchRun)
	mux.HandleFunc("GET /api/v1/research/runs/{id}/activity", h.listResearchActivity)
	mux.HandleFunc("GET /api/v1/research/runs/{id}/report", h.getResearchReport)
	mux.HandleFunc("GET /api/v1/research/captures/{id}", h.getResearchCapture)
	mux.HandleFunc("GET /api/v1/research/identity", h.explainResearchIdentity)
	mux.HandleFunc("GET /api/v1/owner-instructions", h.ownerInstructions)
	mux.HandleFunc("POST /api/v1/owner-instructions", h.addOwnerInstruction)
	mux.HandleFunc("POST /api/v1/owner-instructions/{id}/revoke", h.revokeOwnerInstruction)
	mux.HandleFunc("GET /api/v1/runtime-status", h.runtimeStatus)
	mux.HandleFunc("GET /api/v1/muse/readiness", h.museReadiness)
	mux.HandleFunc("GET /api/v1/muse/checkpoints", h.museCheckpoints)
	mux.HandleFunc("GET /api/v1/muse/report", h.museReport)
	mux.HandleFunc("GET /api/v1/muse/commissions", h.museCommissions)
	mux.HandleFunc("GET /api/v1/codex/status", h.codexStatus)
	mux.HandleFunc("POST /api/v1/codex/connect", h.codexConnect)
	mux.HandleFunc("POST /api/v1/codex/connect/cancel", h.codexCancelConnect)
	mux.HandleFunc("/api/v1/codex/mcp", h.codexMCP)
	mux.HandleFunc("/api/v1/codex/mcp/", h.codexMCP)
	mux.HandleFunc("GET /api/v1/organisation/categories", h.organisationCategories)
	mux.HandleFunc("PUT /api/v1/organisation/categories", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/organisation/summaries", h.organisationSummaries)
	mux.HandleFunc("PUT /api/v1/preferences", h.updatePreferences)
	mux.HandleFunc("GET /api/v1/agent-credentials", h.listAgents)
	mux.HandleFunc("POST /api/v1/agent-credentials", h.createAgent)
	mux.HandleFunc("POST /api/v1/agent-credentials/{id}/revoke", h.revokeAgent)
	mux.HandleFunc("GET /api/v1/companies", h.listCompanies)
	mux.HandleFunc("POST /api/v1/companies", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/companies/{id}", h.getCompany)
	mux.HandleFunc("PATCH /api/v1/companies/{id}", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("POST /api/v1/companies/{id}/archive", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/ingestions", h.listIngestions)
	mux.HandleFunc("POST /api/v1/ingestions", h.submitIngestion)
	mux.HandleFunc("GET /api/v1/ingestions/{id}", h.getIngestion)
	mux.HandleFunc("POST /api/v1/ingestions/{id}/retry", h.retryIngestion)
	mux.HandleFunc("GET /api/v1/opportunities", h.listOpportunities)
	mux.HandleFunc("POST /api/v1/opportunities", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/opportunities/{id}", h.getOpportunity)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/decision", h.getOwnerOpportunityDecision)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/decision", h.setOwnerOpportunityDecision)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/organisation", h.opportunityOrganisation)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/screening", h.opportunityScreening)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/application-packs", h.listApplicationPacks)
	mux.HandleFunc("GET /api/v1/relationships/counterparties", h.listRelationshipCounterparties)
	mux.HandleFunc("GET /api/v1/relationships/events", h.listRelationshipEvents)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/routes", h.listOpportunityRoutes)
	mux.HandleFunc("GET /api/v1/application-packs/{id}", h.getApplicationPack)
	mux.HandleFunc("GET /api/v1/application-packs/{id}/pdf", h.applicationPackPDF)
	mux.HandleFunc("GET /api/v1/application-packs/{id}/source.zip", h.applicationPackSourceArchive)
	// No delivery routes: this app never emails, submits, attaches, or
	// autofills on an employer site. Handoff is manual and owner-driven.
	mux.HandleFunc("PATCH /api/v1/opportunities/{id}", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/archive", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/actions", h.listActions)
	mux.HandleFunc("POST /api/v1/actions", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/actions/due", h.listDueActions)
	mux.HandleFunc("GET /api/v1/actions/overdue", h.listOverdueActions)
	mux.HandleFunc("GET /api/v1/actions/{id}", h.getAction)
	mux.HandleFunc("PATCH /api/v1/actions/{id}", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("POST /api/v1/actions/{id}/reschedule", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("POST /api/v1/actions/{id}/complete", h.completeAction)
	mux.HandleFunc("POST /api/v1/actions/{id}/cancel", h.cancelAction)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/evidence-sources", h.listEvidenceSources)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/evidence-sources", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/evidence-sources/{sourceId}", h.getEvidenceSource)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/evidence", h.listEvidence)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/evidence", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/evidence/{evidenceId}", h.getEvidence)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/evidence/{evidenceId}/supersede", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/qualification", h.getQualification)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/qualification/history", h.listQualificationHistory)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/qualification/reevaluate", h.unsupportedRecruitmentMutation)
	mux.HandleFunc("GET /api/v1/changes", h.listChanges)
	mux.HandleFunc("GET /api/v1/changes/{id}", h.getChange)
	mux.HandleFunc("GET /api/v1/workflow/roles", h.listRoleWorkflows)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/workflow", h.getRoleWorkflow)
	mux.HandleFunc("GET /api/v1/research/brief", h.getSearchBrief)
	mux.HandleFunc("GET /api/v1/research/briefs/{version}/catalog", h.getReasonCatalog)
	mux.HandleFunc("GET /api/v1/research/runs/{id}/findings", h.listRunFindings)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/finding", h.getOpportunityFinding)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/checks", h.startOpportunityCheck)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/checks/current", h.getCurrentOpportunityCheck)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/checks/current/activity", h.listOpportunityCheckActivity)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/checks/{checkId}", h.getOpportunityCheck)
	mux.HandleFunc("GET /api/v1/answers", h.listSavedAnswers)
	mux.HandleFunc("POST /api/v1/answers", h.createSavedAnswer)
	mux.HandleFunc("GET /api/v1/answers/{answerId}", h.getSavedAnswer)
	mux.HandleFunc("POST /api/v1/answers/{answerId}/versions", h.approveSavedAnswerVersion)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/answers/match", h.matchOpportunityAnswers)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/answers/match/current", h.getCurrentAnswerMatch)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/answers/current", h.getCurrentQuestionAnswers)
	mux.HandleFunc("PUT /api/v1/opportunities/{id}/questions/{questionId}/answer", h.saveQuestionAnswer)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/answers/commit", h.commitRoleAnswers)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/materials/prepare", h.prepareOpportunityMaterials)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/materials/current", h.getCurrentOpportunityMaterials)
	mux.HandleFunc("PUT /api/v1/opportunities/{id}/materials/current", h.editOpportunityMaterials)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/materials/rewrite", h.rewriteOpportunityMaterials)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/materials/versions/{version}", h.getOpportunityMaterialVersion)
	mux.HandleFunc("POST /api/v1/opportunities/{id}/artifacts/draft", h.draftOpportunityArtifacts)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/artifacts/activity", h.listPrepareActivity)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/artifacts", h.listArtifactReadiness)
	mux.HandleFunc("GET /api/v1/opportunities/{id}/artifacts/{artifactType}", h.getArtifactReadiness)
	mux.HandleFunc("PUT /api/v1/opportunities/{id}/artifacts/{artifactType}", h.saveOpportunityArtifact)
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
	writeJSON(w, http.StatusOK, generated.SessionResponse{ActorKind: generated.SessionResponseActorKindAdministrator, ActorId: "owner",
		CsrfToken: session.CSRFToken, ExpiresAt: session.ExpiresAt})
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, generated.SessionResponse{ActorKind: generated.SessionResponseActorKindAdministrator, ActorId: principal.ID,
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
	now := l.now()
	cutoff := now.Add(-time.Minute)
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
	for key, values := range l.byIP {
		active := keep(values)
		if len(active) == 0 {
			delete(l.byIP, key)
		} else {
			l.byIP[key] = active
		}
	}
	attempts := l.byIP[ip]
	if len(attempts) >= 5 || len(l.global) >= 30 {
		return false
	}
	if _, exists := l.byIP[ip]; !exists && len(l.byIP) >= 30 {
		return false
	}
	l.byIP[ip] = append(attempts, now)
	l.global = append(l.global, now)
	return true
}
