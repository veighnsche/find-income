// Package agency handles owner-commissioned work on supplied and saved records.
// It never schedules work from a status read or timer.
package agency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type Runtime interface {
	CheckRound(context.Context, string) error
	ExecuteRoundTurn(context.Context, store.Actor, string, codexservice.RoundTurnInput) (store.RoundAttempt, error)
}

type Decisions interface {
	RunDecision(context.Context, jevservice.Binding, jev.DecisionInput) (jev.DecisionResult, error)
	RunScreening(context.Context, jevservice.Binding, jev.ScreeningInput) (jev.ScreeningResult, error)
	RunOrganisation(context.Context, jevservice.Binding, jev.OrganisationInput) (jev.OrganisationResult, error)
}

type Engine struct {
	Store       *store.Store
	Runtime     Runtime
	Decisions   Decisions
	InputReader OwnerSourceReader
	PackSources PackSourceLoader
	Context     context.Context
	mu          sync.Mutex
	active      map[string]*activeWorker
}

type activeWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (e *Engine) workerDone(id string, worker *activeWorker) {
	e.mu.Lock()
	if e.active[id] == worker {
		delete(e.active, id)
	}
	close(worker.done)
	e.mu.Unlock()
}

// WaitRoundStopped is the handoff barrier between a cancelled generation and
// Resume. It never activates or changes the paused round itself.
func (e *Engine) WaitRoundStopped(ctx context.Context, id string) error {
	e.mu.Lock()
	worker := e.active[id]
	e.mu.Unlock()
	if worker == nil {
		return nil
	}
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) CheckRound(ctx context.Context, outcome string) error {
	if outcome == "prepare" {
		return e.checkPrepare(ctx)
	}
	if e == nil || e.Store == nil || e.Runtime == nil || e.Decisions == nil || outcome != "process_input" {
		return errors.New("commissioned input processing unavailable")
	}
	if err := e.Runtime.CheckRound(ctx, outcome); err != nil {
		return err
	}
	return nil
}

func (e *Engine) LaunchRound(_ context.Context, r store.Round) error {
	if r.Outcome == "prepare" {
		return e.launchPrepare(r)
	}
	if r.Outcome == "process_input" {
		return e.launchInput(r)
	}
	return store.ErrInvalid
}

func (e *Engine) CancelRound(id string) {
	e.mu.Lock()
	worker := e.active[id]
	e.mu.Unlock()
	if worker != nil {
		worker.cancel()
	}
}

func (e *Engine) live(ctx context.Context, roundID string, generation, profileVersion int64) (store.Round, error) {
	if err := ctx.Err(); err != nil {
		return store.Round{}, err
	}
	r, err := e.Store.Round(ctx, roundID)
	if err != nil {
		return r, err
	}
	if r.State != store.RoundRunning || r.Generation != generation {
		return r, store.ErrFenced
	}
	if !time.Now().Before(r.Deadline) {
		_, _ = e.Store.ExpireRound(ctx, roundID)
		return r, store.ErrExpired
	}
	p, err := e.Store.CurrentPreferences(ctx)
	if err != nil {
		return r, err
	}
	if p.Version != profileVersion {
		return r, errProfileChanged
	}
	return r, nil
}

var errProfileChanged = errors.New("profile changed during commissioned round")

func hasRoundResource(resources []string, target string) bool {
	for _, resource := range resources {
		if resource == target {
			return true
		}
	}
	return false
}

func prefixUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func profileDecisionSources(profile store.Preferences, maximum int) ([]jev.DecisionSource, []string, bool) {
	encoded, _ := json.Marshal(struct {
		PreferredLocation     string                `json:"preferredLocation"`
		AllowRemote           bool                  `json:"allowRemote"`
		AllowHybrid           bool                  `json:"allowHybrid"`
		TargetHoursHundredths int64                 `json:"targetHoursHundredths"`
		MinMonthlyBaseCents   int64                 `json:"minMonthlyBaseCents"`
		SalaryCurrency        string                `json:"salaryCurrency"`
		RoleCriteria          []store.RoleCriterion `json:"roleCriteria"`
	}{profile.PreferredLocation, profile.AllowRemote, profile.AllowHybrid, profile.TargetHoursHundredths, profile.MinMonthlyBaseCents, profile.SalaryCurrency, profile.RoleCriteria})
	remaining := string(encoded)
	var sources []jev.DecisionSource
	var ids []string
	for len(remaining) > 0 && len(sources) < maximum {
		part := prefixUTF8(remaining, 1900)
		id := fmt.Sprintf("profile:%d", len(sources))
		sources = append(sources, jev.DecisionSource{ID: id, SourceRevision: fmt.Sprint(profile.Version), SourceKind: "owner_profile", Excerpt: part})
		ids = append(ids, id)
		remaining = remaining[len(part):]
	}
	return sources, ids, remaining == ""
}

func terminalCode(err error) string {
	if err == nil {
		return "operation_not_completed"
	}
	switch {
	case errors.Is(err, errProfileChanged):
		return "profile_changed"
	case errors.Is(err, errDecisionContextTooLarge):
		return "decision_context_too_large"
	case errors.Is(err, store.ErrAllowance):
		return "allowance_exhausted"
	case errors.Is(err, store.ErrExpired):
		return "deadline_reached"
	case errors.Is(err, store.ErrUncertain):
		return "remote_outcome_uncertain"
	case errors.Is(err, context.Canceled):
		return "worker_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_reached"
	case errors.Is(err, store.ErrFenced):
		return "round_fenced"
	default:
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unavailable") {
			return "provider_unavailable"
		}
		return "work_step_failed"
	}
}
