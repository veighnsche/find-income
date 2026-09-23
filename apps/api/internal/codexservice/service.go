// Package codexservice joins the isolated runtime, scoped tools and durable
// ingestion queue. It never launches Codex in the database service boundary.
package codexservice

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type Status struct {
	State              string `json:"state"`
	Code               string `json:"code"`
	Connected          bool   `json:"connected"`
	IngestionAvailable bool   `json:"ingestionAvailable"`
	Busy               bool   `json:"busy"`
}
type Connection struct {
	LoginID         string `json:"loginId"`
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
}
type Service struct {
	db      *store.Store
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	client  *codex.Client
	login   *Connection
	loginAt time.Time
	busy    bool
	closed  bool
	dial    func(context.Context, Config) (io.ReadWriteCloser, error)
	bridge  http.Handler
	toolMu  sync.Mutex
	active  *toolScope
}

func NewFromEnvironment(ctx context.Context, db *store.Store) (*Service, error) {
	return New(ctx, db, configFromEnvironment())
}
func New(ctx context.Context, db *store.Store, cfg Config) (*Service, error) {
	if ctx == nil || db == nil {
		return nil, errors.New("Codex service requires context and store")
	}
	if cfg.BridgeName == "" {
		cfg.BridgeName = "jobseek"
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	s := &Service{db: db, cfg: cfg, ctx: serviceCtx, cancel: cancel, dial: dialSSH}
	s.bridge = s.newBridge()
	return s, nil
}

// ensureLocked uses the service lifetime for SSH, not the HTTP request lifetime.
func (s *Service) ensureLocked(ctx context.Context) (*codex.Client, string) {
	if s.closed {
		return nil, "service_stopped"
	}
	if reason := s.cfg.unavailableCode(); reason != "" {
		return nil, reason
	}
	if s.client != nil && s.client.Err() == nil {
		return s.client, ""
	}
	if s.busy {
		return nil, "runtime_disconnected"
	}
	transport, err := s.dial(s.ctx, s.cfg)
	if err != nil {
		return nil, "runner_unreachable"
	}
	client, err := codex.NewClient(transport, codex.Options{})
	if err != nil {
		transport.Close()
		return nil, "runner_unreachable"
	}
	if _, err = client.Initialize(ctx); err != nil {
		client.Close()
		return nil, "runner_protocol_unavailable"
	}
	s.client = client
	s.login = nil
	return client, ""
}

func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(ctx)
}
func (s *Service) statusLocked(ctx context.Context) Status {
	out := Status{State: "unavailable", Busy: s.busy}
	client, reason := s.ensureLocked(ctx)
	if reason != "" {
		out.Code = reason
		return out
	}
	account, err := client.ReadAccount(ctx)
	if err != nil {
		out.Code = "account_unavailable"
		return out
	}
	if account.Account == nil {
		out.State, out.Code = "needs_sign_in", "needs_sign_in"
		if s.login != nil {
			if time.Since(s.loginAt) > 10*time.Minute {
				_ = client.CancelLogin(ctx, s.login.LoginID)
				s.login = nil
				out.Code = "login_attempt_timed_out"
			} else {
				out.State, out.Code = "connecting", "owner_sign_in_pending"
			}
		}
		return out
	}
	if account.Account.Type != "chatgpt" {
		out.Code = "chatgpt_account_required"
		return out
	}
	out.Connected = true
	s.login = nil
	if err := s.checkTools(ctx, client); err != nil {
		out.Code = "required_tools_unavailable"
		return out
	}
	out.State, out.Code, out.IngestionAvailable = "ready", "ready", true
	return out
}

func (s *Service) checkTools(ctx context.Context, client *codex.Client) error {
	var cursor *string
	for page := 0; page < 10; page++ {
		servers, err := client.ListMCPServers(ctx, nil, cursor)
		if err != nil {
			return ErrUnavailable
		}
		for _, server := range servers.Data {
			if server.Name != s.cfg.BridgeName {
				continue
			}
			for _, required := range requiredTools {
				found := false
				for key, tool := range server.Tools {
					if key == required || tool.Name == required {
						found = true
						break
					}
				}
				if !found {
					return ErrUnavailable
				}
			}
			return nil
		}
		if servers.NextCursor == nil {
			return ErrUnavailable
		}
		cursor = servers.NextCursor
	}
	return ErrUnavailable
}

func (s *Service) Connect(ctx context.Context) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return Connection{}, ErrBusy
	}
	client, reason := s.ensureLocked(ctx)
	if reason != "" {
		return Connection{}, ErrUnavailable
	}
	account, err := client.ReadAccount(ctx)
	if err != nil {
		return Connection{}, ErrUnavailable
	}
	if account.Account != nil {
		return Connection{}, errors.New("Codex is already connected")
	}
	if s.login != nil && time.Since(s.loginAt) < 10*time.Minute {
		return *s.login, nil
	}
	if s.login != nil {
		_ = client.CancelLogin(ctx, s.login.LoginID)
		s.login = nil
	}
	attempt, err := client.StartLogin(ctx, codex.DeviceLogin)
	if err != nil {
		return Connection{}, ErrUnavailable
	}
	u, err := url.Parse(attempt.VerificationURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "auth.openai.com" && u.Hostname() != "chatgpt.com") {
		_ = client.CancelLogin(ctx, attempt.LoginID)
		return Connection{}, ErrUnavailable
	}
	s.login = &Connection{LoginID: attempt.LoginID, VerificationURL: attempt.VerificationURL, UserCode: attempt.UserCode}
	s.loginAt = time.Now()
	return *s.login, nil
}

func (s *Service) CancelConnect(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return ErrBusy
	}
	if s.login == nil {
		return nil
	}
	attempt := s.login
	s.login = nil
	if s.client == nil {
		return nil
	}
	return s.client.CancelLogin(ctx, attempt.LoginID)
}
func (s *Service) Close() error {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	client := s.client
	s.mu.Unlock()
	s.toolMu.Lock()
	s.active = nil
	s.toolMu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}
func (s *Service) MCPHandler() http.Handler { return s.bridge }
