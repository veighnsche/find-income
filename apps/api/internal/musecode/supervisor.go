package musecode

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrDisconnected reports transport process death. The supervisor records a
// crash cursor instead of replaying uncertain work.
var ErrDisconnected = errors.New("musecode: session transport disconnected")

// EventKind names one observable session-host event.
type EventKind string

const (
	EventModelStep  EventKind = "model_step"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventSaved      EventKind = "saved"
	EventFinished   EventKind = "finished"
	EventFailed     EventKind = "failed"
)

// Event is a single transport observation. SaveRef carries an app-side save
// handle that must pass validation before it counts as a saved result.
type Event struct {
	Kind     EventKind
	Tool     string
	BytesOut int64
	SaveRef  string
	Detail   string
}

// EventSink receives transport events synchronously during Run.
type EventSink interface {
	Emit(Event)
}

// SessionInput marks which tier an input belongs to. Only PublicInput
// exists until E09 adds the Standard preparation input.
type SessionInput interface {
	inputTier() Tier
}

// PublicInput carries general criteria into a Contributor session. Its type
// cannot express private owner data.
type PublicInput struct {
	Criteria PublicCriteria
}

func (PublicInput) inputTier() Tier { return TierContributor }

// Transport conducts one local CLI session to transport-terminal state.
// Production implementations speak to `muse serve`; tests replay scripts.
type Transport interface {
	Run(ctx context.Context, spec SessionSpec, input SessionInput, resume Cursor, sink EventSink) error
}

// CursorStore persists crash-recovery cursors. Production wiring lands in
// E06; E02 defines the boundary.
type CursorStore interface {
	SaveCursor(ctx context.Context, cursor Cursor) error
	LoadCursor(ctx context.Context, runRef string) (Cursor, error)
}

// ValidateSaveFunc confirms an app-side save handle before the supervisor
// records it. Unvalidated saves never appear in a terminal result.
type ValidateSaveFunc func(ref string) error

// Admission acknowledges that a run was accepted. It never implies
// terminal completion or any validated save.
type Admission struct {
	RunRef     string
	Tier       Tier
	AdmittedAt time.Time
}

// Supervisor admits bounded runs, enforces the frozen contract on every
// transport event, and fences Stop and crash recovery.
type Supervisor struct {
	transport   Transport
	cursors     CursorStore
	validate    ValidateSaveFunc
	pinnedModel string

	mu   sync.Mutex
	runs map[string]*run
}

// NewSupervisor pins the effective model observed at readiness time. Runs
// admitted later must present the same model and a proved lane.
func NewSupervisor(transport Transport, cursors CursorStore, validate ValidateSaveFunc, pinnedModel string) *Supervisor {
	return &Supervisor{transport: transport, cursors: cursors, validate: validate,
		pinnedModel: pinnedModel, runs: map[string]*run{}}
}

type run struct {
	mu        sync.Mutex
	spec      SessionSpec
	input     SessionInput
	cancel    context.CancelFunc
	startedAt time.Time
	stopped   bool
	fenced    int
	usage     Usage
	saved     []string
	seen      map[string]bool
	finished  bool
	failed    string
	terminal  *TerminalResult
}

// StartRun admits one run and conducts it to a terminal result. Admission is
// returned before completion; use Result for the terminal record.
func (s *Supervisor) StartRun(ctx context.Context, runRef string, spec SessionSpec, input SessionInput, current Facts) (Admission, error) {
	if input.inputTier() != spec.Tier {
		return Admission{}, errors.New("musecode: input tier does not match session tier")
	}
	if current.EffectiveModel == "" || current.EffectiveModel != s.pinnedModel || !current.SubscriptionLaneProved {
		return Admission{}, errors.New("musecode: effective model/lane no longer matches pin")
	}
	runCtx, cancel := context.WithTimeout(ctx, spec.Bounds.MaxWallClock)
	r := &run{spec: spec, input: input, cancel: cancel, startedAt: time.Now(), seen: map[string]bool{}}
	s.mu.Lock()
	if _, dup := s.runs[runRef]; dup {
		s.mu.Unlock()
		cancel()
		return Admission{}, errors.New("musecode: duplicate run ref")
	}
	s.runs[runRef] = r
	s.mu.Unlock()
	admission := Admission{RunRef: runRef, Tier: spec.Tier, AdmittedAt: time.Now()}
	r.conduct(runCtx, s, runRef, Cursor{})
	return admission, nil
}

// Resume continues a crashed run from its durable cursor. Finished runs and
// runs without a cursor are never replayed.
func (s *Supervisor) Resume(ctx context.Context, runRef string, spec SessionSpec, input SessionInput, current Facts) (Admission, error) {
	s.mu.Lock()
	prior, ok := s.runs[runRef]
	s.mu.Unlock()
	if ok && prior.terminal != nil && prior.terminal.Outcome == OutcomeCompleted {
		return Admission{}, errors.New("musecode: completed runs are never replayed")
	}
	cursor, err := s.cursors.LoadCursor(ctx, runRef)
	if err != nil {
		return Admission{}, err
	}
	if cursor.RunRef != runRef {
		return Admission{}, errors.New("musecode: cursor does not match run")
	}
	if input.inputTier() != spec.Tier {
		return Admission{}, errors.New("musecode: input tier does not match session tier")
	}
	if current.EffectiveModel == "" || current.EffectiveModel != s.pinnedModel || !current.SubscriptionLaneProved {
		return Admission{}, errors.New("musecode: effective model/lane no longer matches pin")
	}
	runCtx, cancel := context.WithTimeout(ctx, spec.Bounds.MaxWallClock)
	r := &run{spec: spec, input: input, cancel: cancel, startedAt: time.Now(),
		seen: map[string]bool{}, saved: append([]string(nil), cursor.SavedRefs...)}
	for _, ref := range cursor.SavedRefs {
		r.seen[ref] = true
	}
	s.mu.Lock()
	s.runs[runRef] = r
	s.mu.Unlock()
	r.conduct(runCtx, s, runRef, cursor)
	return Admission{RunRef: runRef, Tier: spec.Tier, AdmittedAt: time.Now()}, nil
}

// Stop fences a running session: app tools stop accepting calls, queued
// work unqueues via context cancellation, and the supervisor waits for the
// transport to return before reporting OutcomeStopped.
func (s *Supervisor) Stop(runRef, reason string) {
	s.mu.Lock()
	r, ok := s.runs[runRef]
	s.mu.Unlock()
	if !ok {
		return
	}
	r.mu.Lock()
	if r.terminal == nil {
		r.stopped = true
		r.failed = reason
	}
	r.mu.Unlock()
	r.cancel()
}

// Result returns the terminal record once the run has completed.
func (s *Supervisor) Result(runRef string) (TerminalResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runRef]
	if !ok || r.terminal == nil {
		return TerminalResult{}, false
	}
	return *r.terminal, true
}
