package codexservice

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
)

type activeRun struct {
	attemptID string
	cancel    context.CancelFunc
	done      chan struct{}
}

func (s *Service) Connect(ctx context.Context) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.disconnecting {
		return Connection{}, ErrBusy
	}
	client, reason := s.ensureLocked(ctx)
	if reason != "" {
		return Connection{}, ErrUnavailable
	}
	// An earlier uncertain cancellation/logout must be settled before another login.
	if s.logoutRequired {
		if err := s.logoutLocked(ctx); err != nil {
			return Connection{}, err
		}
	}
	s.drainAccountEventsLocked(client)
	account, err := client.ReadAccount(ctx)
	if err != nil {
		return Connection{}, ErrUnavailable
	}
	if s.login != nil && time.Since(s.loginAt) < 10*time.Minute {
		return *s.login, nil
	}
	if s.login != nil {
		if err := s.cancelLoginLocked(ctx, "login_attempt_timed_out"); err != nil {
			return Connection{}, err
		}
	} else if account.Account != nil && s.loginCode == "" {
		return Connection{}, errors.New("Codex is already connected")
	}
	// A cancelled/failed attempt may have written a token late. A confirmed
	// signed-out account needs no second logout just to clear a UI status code.
	if s.loginCode != "" && account.Account != nil {
		if err := s.logoutLocked(ctx); err != nil {
			return Connection{}, err
		}
	}
	attempt, err := client.StartLogin(ctx, codex.DeviceLogin)
	if err != nil {
		return Connection{}, ErrUnavailable
	}
	u, err := url.Parse(attempt.VerificationURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "auth.openai.com" && u.Hostname() != "chatgpt.com") {
		s.login = &Connection{LoginID: attempt.LoginID}
		_ = s.cancelLoginLocked(ctx, "login_attempt_failed")
		return Connection{}, ErrUnavailable
	}
	s.login = &Connection{LoginID: attempt.LoginID, VerificationURL: attempt.VerificationURL, UserCode: attempt.UserCode}
	s.loginAt, s.loginCode, s.loginSucceeded, s.loginGeneration = time.Now(), "", false, client.Generation()
	return *s.login, nil
}

func (s *Service) cancelLoginLocked(ctx context.Context, code string) error {
	attempt := s.login
	s.login, s.loginSucceeded, s.loginCode, s.loginGeneration = nil, false, code, 0
	s.logoutRequired = true
	if s.client == nil {
		return ErrUnavailable
	}
	if attempt != nil {
		if err := s.client.CancelLogin(ctx, attempt.LoginID); err != nil {
			return ErrUnavailable
		}
	}
	return s.logoutLocked(ctx)
}

// logoutLocked requires a successful logout plus account readback. Transport
// failure never masquerades as remote credential removal.
func (s *Service) logoutLocked(ctx context.Context) error {
	s.logoutRequired = true
	if s.client == nil || s.client.Logout(ctx) != nil {
		return ErrUnavailable
	}
	account, err := s.client.ReadAccount(ctx)
	if err != nil || account.Account != nil {
		return ErrUnavailable
	}
	s.logoutRequired = false
	return nil
}

func (s *Service) CancelConnect(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.disconnecting {
		return ErrBusy
	}
	if s.login == nil && !s.logoutRequired {
		return nil
	}
	return s.cancelLoginLocked(ctx, "login_attempt_cancelled")
}

// Disconnect blocks new dispatch, cancels the owned run, then logs out of the
// dedicated runner. It neither logs the browser out nor cancels pending jobs.
func (s *Service) Disconnect(ctx context.Context) error {
	s.mu.Lock()
	if s.disconnecting {
		s.mu.Unlock()
		return ErrBusy
	}
	s.disconnecting, s.logoutRequired = true, true
	run := s.run
	if run != nil {
		run.cancel()
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.disconnecting = false; s.mu.Unlock() }()
	if run != nil {
		select {
		case <-run.done:
		case <-ctx.Done():
			return ErrUnavailable
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, reason := s.ensureLocked(ctx)
	if reason != "" {
		return ErrUnavailable
	}
	return s.cancelLoginLocked(ctx, "needs_sign_in")
}

func (s *Service) Close() error {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	client, run := s.client, s.run
	if run != nil {
		run.cancel()
	}
	s.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if run != nil {
		<-run.done
	}
	return nil
}
