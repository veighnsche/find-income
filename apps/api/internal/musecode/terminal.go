package musecode

import "time"

// Outcome names how a session reached its terminal state. Only completed
// with validated saves counts as success; every other outcome keeps its
// gate open.
type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeStopped   Outcome = "stopped"
	OutcomeFailed    Outcome = "failed"
	OutcomeCrashed   Outcome = "crashed"
	OutcomeExpired   Outcome = "expired"
)

// Usage accounts one session's observed consumption. Subscription-backed CLI
// use has no per-run dollar meter here, so unknown stays unknown.
type Usage struct {
	ModelSteps             int
	ToolCalls              int
	BytesOut               int64
	WallClock              time.Duration
	SubscriptionUsageKnown bool
}

// TerminalResult is the durable record of a finished session. SavedRefs
// lists only validated app-side saves; command admission alone never
// produces one.
type TerminalResult struct {
	RunRef    string
	Tier      Tier
	Outcome   Outcome
	Detail    string
	SavedRefs []string
	Usage     Usage
	EndedAt   time.Time
}

// StopRequest fences a running session: app tools stop accepting calls,
// queued work unqueues, background activity stops, and the supervisor waits
// for cleanup before reporting OutcomeStopped.
type StopRequest struct {
	RunRef      string
	Reason      string
	WaitCleanup time.Duration
}

// Cursor is the durable resume point after process death. Recovery
// reconciles cursors and saved receipts before resuming; uncertain
// requests are never blindly replayed.
type Cursor struct {
	RunRef           string
	Tier             Tier
	LastSavedReceipt string
	SavedCount       int
	SavedRefs        []string
	UpdatedAt        time.Time
}
