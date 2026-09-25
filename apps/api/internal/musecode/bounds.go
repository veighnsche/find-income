package musecode

import (
	"errors"
	"time"
)

// Bounds caps every model, tool and background usage dimension of one
// session. These are hard ceilings; an authorized run may narrow them.
type Bounds struct {
	MaxWallClock   time.Duration
	MaxModelSteps  int
	MaxToolCalls   int
	MaxBytesPerOp  int64
	MaxBytesTotal  int64
	BackgroundWork bool
}

// DefaultBounds returns the frozen E01 ceilings. The first discovery run
// targets 30–45 minutes with a proposed 45-minute stop; background work,
// including observers and subagents, stays off until explicitly bounded.
func DefaultBounds() Bounds {
	return Bounds{
		MaxWallClock:   45 * time.Minute,
		MaxModelSteps:  120,
		MaxToolCalls:   400,
		MaxBytesPerOp:  2 << 20,
		MaxBytesTotal:  200 << 20,
		BackgroundWork: false,
	}
}

// Validate rejects bounds that are unbounded, non-positive or internally
// inconsistent. Zero Bounds never validate: ceilings must be explicit.
func (b Bounds) Validate() error {
	if b.MaxWallClock <= 0 {
		return errors.New("musecode: wall-clock bound must be positive")
	}
	if b.MaxModelSteps <= 0 {
		return errors.New("musecode: model-step bound must be positive")
	}
	if b.MaxToolCalls <= 0 {
		return errors.New("musecode: tool-call bound must be positive")
	}
	if b.MaxBytesPerOp <= 0 || b.MaxBytesTotal <= 0 {
		return errors.New("musecode: byte bounds must be positive")
	}
	if b.MaxBytesPerOp > b.MaxBytesTotal {
		return errors.New("musecode: per-operation bytes exceed total bytes")
	}
	if b.BackgroundWork {
		return errors.New("musecode: background work is not bounded yet")
	}
	return nil
}
