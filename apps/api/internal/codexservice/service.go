// Package codexservice manages the isolated App Server connection and exposes
// scoped round tools. A round executor has not yet been bound to this service.
package codexservice

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type Status struct {
	State     string `json:"state"`
	Code      string `json:"code"`
	Connected bool   `json:"connected"`
	// Local reports explicit localhost-runner mode, which never claims the
	// SSH-isolated runner boundary.
	Local bool `json:"local"`
	// IngestionAvailable describes executable, round-authorized ingestion.
	// Account/model/tool readiness alone cannot set it.
	IngestionAvailable bool          `json:"ingestionAvailable"`
	Busy               bool          `json:"busy"`
	Model              string        `json:"model"`
	Effort             string        `json:"effort"`
	UsageAvailable     bool          `json:"usageAvailable"`
	Usage              []UsageWindow `json:"usage"`
}
type Connection struct {
	LoginID         string `json:"loginId"`
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
}
type Service struct {
	db              *store.Store
	cfg             Config
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	client          *codex.Client
	login           *Connection
	loginAt         time.Time
	loginGeneration uint64
	loginCode       string
	loginSucceeded  bool
	disconnecting   bool
	logoutRequired  bool
	run             *activeRun
	busy            bool
	closed          bool
	dial            func(context.Context, Config) (io.ReadWriteCloser, error)
	bridge          http.Handler
	sourceMu        sync.Mutex
	sourceCalls     map[string]*sourceCall
	linkFetch       func(context.Context, string, SourceLinkPage) (SourceLinksSnapshot, error)
	packConfig      ApplicationPackRuntimeConfig
	interviewConfig InterviewRuntimeConfig
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
	dial := dialSSH
	if cfg.Local() {
		dial = dialLocal
	}
	s := &Service{db: db, cfg: cfg, ctx: serviceCtx, cancel: cancel, dial: dial}
	s.linkFetch = func(ctx context.Context, url string, page SourceLinkPage) (SourceLinksSnapshot, error) {
		return fetchOfficialLinksPage(ctx, url, net.DefaultResolver, pinnedSourceClient, page)
	}
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
	if s.login != nil {
		// A lost transport can race a remote login completion. The replacement
		// generation must first explicitly clear that account state.
		s.login, s.loginGeneration, s.loginSucceeded = nil, 0, false
		s.loginCode, s.logoutRequired = "login_attempt_interrupted", true
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
	s.loginGeneration = 0
	s.loginSucceeded = false
	return client, ""
}

func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(ctx)
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

func (s *Service) MCPHandler() http.Handler { return s.bridge }
