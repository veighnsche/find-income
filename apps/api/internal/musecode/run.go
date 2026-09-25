package musecode

import (
	"context"
	"errors"
	"time"
)

// conduct runs one session to a terminal result, enforcing bounds, tool
// allowlist, save validation and Stop/crash semantics on every event.
func (r *run) conduct(ctx context.Context, s *Supervisor, runRef string, resume Cursor) {
	defer r.cancel()
	err := s.transport.Run(ctx, r.spec, r.input, resume, runSink{supervisor: s, run: r})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage.WallClock = time.Since(r.startedAt)
	base := TerminalResult{RunRef: runRef, Tier: r.spec.Tier, SavedRefs: append([]string(nil), r.saved...),
		Usage: r.usage, EndedAt: time.Now()}
	switch {
	case r.stopped:
		base.Outcome = OutcomeStopped
		base.Detail = r.failed
		r.terminal = &base
	case errors.Is(err, ErrDisconnected):
		base.Outcome = OutcomeCrashed
		base.Detail = "transport disconnected"
		cursor := Cursor{RunRef: runRef, Tier: r.spec.Tier, SavedCount: len(r.saved),
			SavedRefs: append([]string(nil), r.saved...), UpdatedAt: time.Now()}
		if len(r.saved) > 0 {
			cursor.LastSavedReceipt = r.saved[len(r.saved)-1]
		} else {
			cursor.LastSavedReceipt = resume.LastSavedReceipt
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if saveErr := s.cursors.SaveCursor(saveCtx, cursor); saveErr != nil {
			base.Outcome = OutcomeFailed
			base.Detail = "cursor not durable: " + saveErr.Error()
		}
		cancel()
		r.terminal = &base
	case r.failed != "":
		base.Outcome = OutcomeFailed
		base.Detail = r.failed
		r.terminal = &base
	case ctx.Err() == context.DeadlineExceeded:
		base.Outcome = OutcomeExpired
		base.Detail = "wall-clock bound exceeded"
		r.terminal = &base
	case err != nil:
		base.Outcome = OutcomeFailed
		base.Detail = err.Error()
		r.terminal = &base
	case !r.finished:
		base.Outcome = OutcomeFailed
		base.Detail = "transport ended without terminal event"
		r.terminal = &base
	default:
		base.Outcome = OutcomeCompleted
		r.terminal = &base
	}
}

// runSink applies the frozen contract to each transport event. The first
// violation fails the run closed and cancels remaining work.
type runSink struct {
	supervisor *Supervisor
	run        *run
}

func (k runSink) Emit(event Event) {
	r := k.run
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		// Fenced: late tools and saves after Stop are observed but never
		// admitted as work.
		switch event.Kind {
		case EventToolCall, EventToolResult, EventSaved:
			r.fenced++
		case EventFinished:
			r.finished = true
		}
		return
	}
	if r.failed != "" {
		return
	}
	fail := func(detail string) {
		r.failed = detail
		r.cancel()
	}
	switch event.Kind {
	case EventModelStep:
		r.usage.ModelSteps++
	case EventToolCall:
		r.usage.ToolCalls++
		if r.spec.Tier == TierContributor && !ContributorToolAllowed(event.Tool) {
			fail("tool not allowlisted for contributor: " + event.Tool)
			return
		}
	case EventToolResult:
		r.usage.BytesOut += event.BytesOut
	case EventModelText:
		r.usage.BytesOut += event.BytesOut
	case EventSaved:
		if event.SaveRef == "" {
			fail("empty save ref")
			return
		}
		if r.seen[event.SaveRef] {
			fail("duplicate save ref, refusing replay: " + event.SaveRef)
			return
		}
		if err := k.supervisor.validate(event.SaveRef); err != nil {
			fail("unvalidated save rejected: " + event.SaveRef)
			return
		}
		r.seen[event.SaveRef] = true
		r.saved = append(r.saved, event.SaveRef)
	case EventFinished:
		r.finished = true
		return
	case EventFailed:
		fail("transport failed: " + event.Detail)
		return
	default:
		fail("unknown event kind")
		return
	}
	if event.BytesOut > r.spec.Bounds.MaxBytesPerOp {
		fail("per-operation byte bound exceeded")
		return
	}
	switch {
	case r.usage.ModelSteps > r.spec.Bounds.MaxModelSteps:
		fail("model-step bound exceeded")
	case r.usage.ToolCalls > r.spec.Bounds.MaxToolCalls:
		fail("tool-call bound exceeded")
	case r.usage.BytesOut > r.spec.Bounds.MaxBytesTotal:
		fail("total byte bound exceeded")
	}
}
