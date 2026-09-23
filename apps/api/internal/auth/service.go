package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var (
	ErrUnauthenticated  = errors.New("authentication required")
	ErrForbidden        = errors.New("not permitted")
	ErrMixedCredentials = errors.New("mixed cookie and bearer credentials")
	ErrInvalid          = errors.New("invalid authentication input")
)

const SessionCookie = "jobseek_session"

type Service struct {
	store      *store.Store
	now        func() time.Time
	sessionTTL time.Duration
	loginSlots chan struct{}
}

func NewService(database *store.Store) *Service {
	return &Service{store: database, now: time.Now, sessionTTL: 12 * time.Hour, loginSlots: make(chan struct{}, 2)}
}

// SetClock is intended for isolated tests. Production uses time.Now.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) SetupAdministrator(ctx context.Context, password []byte) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.store.SetAdministratorHash(ctx, hash)
}

type Session struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

func (s *Service) Login(ctx context.Context, password []byte) (Session, error) {
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	case <-ctx.Done():
		return Session{}, ctx.Err()
	}
	hash, err := s.store.AdministratorHash(ctx)
	if err != nil {
		return Session{}, ErrUnauthenticated
	}
	if !VerifyPassword(hash, password) {
		return Session{}, ErrUnauthenticated
	}
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	csrf := csrfForSession(token)
	expires := s.now().Add(s.sessionTTL)
	if err := s.store.CreateSession(ctx, hashToken(token), hashToken(csrf), expires); err != nil {
		return Session{}, err
	}
	return Session{Token: token, CSRFToken: csrf, ExpiresAt: expires}, nil
}

type Principal struct {
	Kind      string
	ID        string
	Scopes    []string
	TokenHash string // internal lookup key; never serialize
	CSRFToken string // owner session only
	ExpiresAt time.Time
}

func (p Principal) IsOwner() bool      { return p.Kind == "administrator" && p.ID == "owner" }
func (p Principal) Actor() store.Actor { return store.Actor{Kind: p.Kind, ID: p.ID} }
func (p Principal) HasScope(scope string) bool {
	if p.IsOwner() {
		return true
	}
	for _, value := range p.Scopes {
		if value == scope {
			return true
		}
	}
	return false
}

func (s *Service) SessionPrincipal(ctx context.Context, token string) (Principal, error) {
	if len(token) < 32 || len(token) > 256 {
		return Principal{}, ErrUnauthenticated
	}
	tokenHash := hashToken(token)
	record, err := s.store.SessionByHash(ctx, tokenHash)
	if err != nil || record.RevokedAt != nil || !record.ExpiresAt.After(s.now()) {
		return Principal{}, ErrUnauthenticated
	}
	csrf := csrfForSession(token)
	if subtle.ConstantTimeCompare([]byte(record.CSRFHash), []byte(hashToken(csrf))) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Kind: "administrator", ID: "owner", TokenHash: tokenHash, CSRFToken: csrf, ExpiresAt: record.ExpiresAt}, nil
}

func (s *Service) Logout(ctx context.Context, p Principal) error {
	if !p.IsOwner() {
		return ErrForbidden
	}
	return s.store.RevokeSession(ctx, p.TokenHash)
}

var allowedScopes = map[string]bool{
	"preferences:read":    true,
	"opportunities:read":  true,
	"opportunities:write": true,
	"openings:ingest":     true,
	"evidence:write":      true,
	"actions:write":       true,
	"drafts:write":        true,
	"judgments:request":   true,
}

func AllowedScopes() []string {
	out := make([]string, 0, len(allowedScopes))
	for scope := range allowedScopes {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

type AgentCredential struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Revoked   bool      `json:"revoked"`
}

func (s *Service) CreateAgent(ctx context.Context, owner Principal, name string, scopes []string, expiresAt time.Time) (AgentCredential, string, error) {
	if !owner.IsOwner() {
		return AgentCredential{}, "", ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n\x00") || len(scopes) == 0 ||
		!expiresAt.After(s.now()) || expiresAt.After(s.now().Add(366*24*time.Hour)) {
		return AgentCredential{}, "", ErrInvalid
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if !allowedScopes[scope] || seen[scope] {
			return AgentCredential{}, "", ErrInvalid
		}
		seen[scope] = true
		clean = append(clean, scope)
	}
	sort.Strings(clean)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return AgentCredential{}, "", err
	}
	token, err := randomToken()
	if err != nil {
		return AgentCredential{}, "", err
	}
	token = "jsk_" + token
	record, err := s.store.CreateAgentCredential(ctx, name, hashToken(token), string(encoded), expiresAt)
	if err != nil {
		return AgentCredential{}, "", err
	}
	return metadata(record), token, nil
}

func (s *Service) AgentPrincipal(ctx context.Context, token string) (Principal, error) {
	if !strings.HasPrefix(token, "jsk_") || len(token) < 40 || len(token) > 256 {
		return Principal{}, ErrUnauthenticated
	}
	record, err := s.store.AgentCredentialByHash(ctx, hashToken(token))
	if err != nil || record.RevokedAt != nil || !record.ExpiresAt.After(s.now()) {
		return Principal{}, ErrUnauthenticated
	}
	var scopes []string
	if err := json.Unmarshal([]byte(record.ScopesJSON), &scopes); err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Kind: "agent", ID: record.ID, Scopes: scopes, TokenHash: record.TokenHash, ExpiresAt: record.ExpiresAt}, nil
}

func (s *Service) ListAgents(ctx context.Context, owner Principal) ([]AgentCredential, error) {
	if !owner.IsOwner() {
		return nil, ErrForbidden
	}
	records, err := s.store.ListAgentCredentials(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AgentCredential, 0, len(records))
	for _, record := range records {
		out = append(out, metadata(record))
	}
	return out, nil
}

func (s *Service) RevokeAgent(ctx context.Context, owner Principal, id string) error {
	if !owner.IsOwner() {
		return ErrForbidden
	}
	if id == "" {
		return ErrInvalid
	}
	return s.store.RevokeAgentCredential(ctx, id)
}

func metadata(record store.AgentCredentialRecord) AgentCredential {
	var scopes []string
	_ = json.Unmarshal([]byte(record.ScopesJSON), &scopes)
	return AgentCredential{ID: record.ID, Name: record.Name, Scopes: scopes, CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt, Revoked: record.RevokedAt != nil}
}

func (s *Service) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	cookie, err := r.Cookie(SessionCookie)
	hasCookie := err == nil && cookie.Value != ""
	authorization := r.Header.Get("Authorization")
	if hasCookie && authorization != "" {
		return Principal{}, ErrMixedCredentials
	}
	if authorization != "" {
		parts := strings.SplitN(authorization, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" || strings.TrimSpace(parts[1]) != parts[1] || parts[1] == "" {
			return Principal{}, ErrUnauthenticated
		}
		return s.AgentPrincipal(ctx, parts[1])
	}
	if !hasCookie {
		return Principal{}, ErrUnauthenticated
	}
	return s.SessionPrincipal(ctx, cookie.Value)
}

func ValidateCSRF(principal Principal, provided string) bool {
	return principal.IsOwner() && provided != "" &&
		subtle.ConstantTimeCompare([]byte(principal.CSRFToken), []byte(provided)) == 1
}

func hashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func csrfForSession(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("jobseek-csrf-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func randomToken() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(secret[:]), nil
}
