package musewire

import (
	"context"
	"errors"
	"fmt"

	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// SupervisorStandardRunner is the E13 StandardRunner: one bounded private
// Standard turn behind the supervisor, built ONLY for TierStandard. The
// runner is single-use: exactly one RunStandard call is admitted per
// draft or rewrite, and failures fail closed with no retry. Texts are
// collected from EventModelText and returned unvalidated; the preparation
// adapter validates scope, citations, and shape.
type SupervisorStandardRunner struct {
	Supervisor *musecode.Supervisor
	Spec       musecode.SessionSpec
	Facts      musecode.Facts

	runRef string
	texts  []string
	used   bool
}

var _ materialprep.StandardRunner = (*SupervisorStandardRunner)(nil)

// NewSupervisorStandardRunner builds the single-use runner for one
// preparation operation. The spec must be a private TierStandard session
// (via musecode.NewSession over a ready TierStandard status) whose
// workspace is disjoint from every Contributor workspace; transport is
// wrapped so model texts are captured without touching the supervisor
// contract. Facts must carry the pinned effective model and proved lane.
func NewSupervisorStandardRunner(transport musecode.Transport, cursors musecode.CursorStore, validate musecode.ValidateSaveFunc, spec musecode.SessionSpec, facts musecode.Facts, runRef string) (*SupervisorStandardRunner, error) {
	if transport == nil || cursors == nil || validate == nil {
		return nil, errors.New("musewire: standard runner needs transport, cursors and save validation")
	}
	if spec.Tier != musecode.TierStandard || spec.Public {
		return nil, errors.New("musewire: standard runner needs a private TierStandard spec")
	}
	if facts.EffectiveModel == "" || !facts.SubscriptionLaneProved {
		return nil, errors.New("musewire: standard runner needs a pinned model and proved lane")
	}
	if !validRunRef(runRef) {
		return nil, fmt.Errorf("musewire: invalid standard run ref %q", runRef)
	}
	runner := &SupervisorStandardRunner{Spec: spec, Facts: facts, runRef: runRef}
	capture := &captureTransport{next: transport, texts: &runner.texts}
	runner.Supervisor = musecode.NewSupervisor(capture, cursors, validate, facts.EffectiveModel)
	return runner, nil
}

// RunStandard admits one bounded Standard turn and returns its collected
// model texts. Readiness is re-checked without a model call: an unproved
// lane reports musecode.ErrSessionUnavailable and never falls back to
// Contributor or Codex.
func (r *SupervisorStandardRunner) RunStandard(ctx context.Context, input musecode.StandardInput) (materialprep.StandardResult, error) {
	if r.used {
		return materialprep.StandardResult{}, errors.New("musewire: standard runner is single-use")
	}
	r.used = true
	if status := musecode.Check(musecode.TierStandard, r.Facts); !status.Available {
		return materialprep.StandardResult{}, musecode.ErrSessionUnavailable
	}
	if _, err := r.Supervisor.StartRun(ctx, r.runRef, r.Spec, input, r.Facts); err != nil {
		return materialprep.StandardResult{}, err
	}
	terminal, ok := r.Supervisor.Result(r.runRef)
	if !ok {
		return materialprep.StandardResult{}, fmt.Errorf("musewire: standard run %q has no terminal result", r.runRef)
	}
	if terminal.Outcome != musecode.OutcomeCompleted {
		return materialprep.StandardResult{}, fmt.Errorf("musewire: standard run %q ended %s: %s", r.runRef, terminal.Outcome, terminal.Detail)
	}
	return materialprep.StandardResult{Messages: append([]string(nil), r.texts...)}, nil
}

// captureTransport delegates to the wrapped transport while recording
// every EventModelText for the runner. All events still reach the
// supervisor sink unchanged, so bounds and fencing apply as usual.
type captureTransport struct {
	next  musecode.Transport
	texts *[]string
}

func (c *captureTransport) Run(ctx context.Context, spec musecode.SessionSpec, input musecode.SessionInput, resume musecode.Cursor, sink musecode.EventSink) error {
	return c.next.Run(ctx, spec, input, resume, captureSink{next: sink, texts: c.texts})
}

type captureSink struct {
	next  musecode.EventSink
	texts *[]string
}

func (s captureSink) Emit(event musecode.Event) {
	if event.Kind == musecode.EventModelText {
		*s.texts = append(*s.texts, event.Text)
	}
	s.next.Emit(event)
}
