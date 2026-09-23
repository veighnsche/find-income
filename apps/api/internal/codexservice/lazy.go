package codexservice

import (
	"context"
	"net/http"
	"sync"

	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Lazy builds the bridge only for an explicit control or commissioned work
// request. Health and idle startup never construct its MCP server.
type Lazy struct {
	ctx        context.Context
	db         *store.Store
	mu         sync.Mutex
	service    *Service
	err        error
	closed     bool
	packConfig *ApplicationPackRuntimeConfig
}

func NewLazy(ctx context.Context, db *store.Store) *Lazy { return &Lazy{ctx: ctx, db: db} }

func (l *Lazy) SetApplicationPackConfig(cfg ApplicationPackRuntimeConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed && l.service == nil {
		l.packConfig = &cfg
	}
}

func (l *Lazy) get() (*Service, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrUnavailable
	}
	if l.service == nil && l.err == nil {
		l.service, l.err = NewFromEnvironment(l.ctx, l.db)
		if l.err == nil && l.packConfig != nil {
			_ = l.service.ConfigureApplicationPacks(*l.packConfig)
		}
	}
	return l.service, l.err
}

func (l *Lazy) Status(ctx context.Context) Status {
	s, err := l.get()
	if err != nil {
		return Status{State: "unavailable", Code: "runner_not_configured"}
	}
	return s.Status(ctx)
}
func (l *Lazy) Connect(ctx context.Context) (Connection, error) {
	s, err := l.get()
	if err != nil {
		return Connection{}, err
	}
	return s.Connect(ctx)
}
func (l *Lazy) CancelConnect(ctx context.Context) error {
	s, err := l.get()
	if err != nil {
		return err
	}
	return s.CancelConnect(ctx)
}
func (l *Lazy) MCPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := l.get()
		if err != nil {
			http.Error(w, "Bridge unavailable", http.StatusServiceUnavailable)
			return
		}
		s.MCPHandler().ServeHTTP(w, r)
	})
}
func (l *Lazy) CheckRound(ctx context.Context, outcome string) error {
	s, err := l.get()
	if err != nil {
		return err
	}
	return s.CheckRound(ctx, outcome)
}
func (l *Lazy) ExecuteRoundTurn(ctx context.Context, actor store.Actor, roundID string, input RoundTurnInput) (store.RoundAttempt, error) {
	s, err := l.get()
	if err != nil {
		return store.RoundAttempt{}, err
	}
	return s.ExecuteRoundTurn(ctx, actor, roundID, input)
}

func (l *Lazy) CompletePackDeliveryRoute(ctx context.Context, roundID, packID string) (string, error) {
	s, err := l.get()
	if err != nil {
		return "", err
	}
	return s.CompletePackDeliveryRoute(ctx, roundID, packID)
}
func (l *Lazy) CancelDispatch(ctx context.Context, attemptID string) error {
	s, err := l.get()
	if err != nil {
		return err
	}
	return s.CancelDispatch(ctx, attemptID)
}
func (l *Lazy) ObserveDispatch(ctx context.Context, attemptID string) (rounds.Observation, error) {
	s, err := l.get()
	if err != nil {
		return rounds.Observation{}, err
	}
	return s.ObserveDispatch(ctx, attemptID)
}
func (l *Lazy) RecoverLocalDispatch(ctx context.Context, roundID, attemptID string, generation int64) (bool, bool, error) {
	s, err := l.get()
	if err != nil {
		return false, false, err
	}
	return s.RecoverLocalDispatch(ctx, roundID, attemptID, generation)
}
func (l *Lazy) Close() error {
	l.mu.Lock()
	l.closed = true
	service := l.service
	l.mu.Unlock()
	if service != nil {
		return service.Close()
	}
	return nil
}
