package researchcontract

import (
	"context"
	"encoding/json"
	"time"
)

// Permission names the kind of action an authority check gates. It never
// names companies, URLs, search terms, page numbers or argument values.
type Permission string

const (
	PermissionResearchDispatch Permission = "research.dispatch"
	PermissionRecordWrite      Permission = "record.write"
	PermissionJevRequest       Permission = "jev.request"
	PermissionRunControl       Permission = "run.control"
)

// Allowance is a finite multi-dimensional budget. Field names mirror the
// existing round ledger so B can map them without reinterpretation.
type Allowance struct {
	Requests int64 `json:"requests"`
	Items    int64 `json:"items"`
	Tools    int64 `json:"tools"`
	Turns    int64 `json:"turns"`
}

// UsageLedger distinguishes enforced limits, reserved in-flight usage,
// observed usage and unknown usage (T06 §5). Unknown latches on any
// uncertain work and is never silently zero.
type UsageLedger struct {
	Enforced Allowance `json:"enforced"`
	Reserved Allowance `json:"reserved"`
	Observed Allowance `json:"observed"`
	Unknown  bool      `json:"unknown"`
}

// CheckInput scopes one authority check to the active run.
type CheckInput struct {
	RunID      string
	Generation int64
	Permission Permission
	Now        time.Time
}

// Reservation is a pre-dispatch hold against the run allowance.
type Reservation struct {
	ID             string    `json:"id"`
	AttemptID      string    `json:"attemptId"`
	Operation      string    `json:"operation"`
	RunID          string    `json:"runId"`
	Generation     int64     `json:"generation"`
	IdempotencyKey string    `json:"idempotencyKey"`
	PayloadHash    string    `json:"payloadHash"`
	ReservedAt     time.Time `json:"reservedAt"`
}

// Reconciliation records how an uncertain attempt was settled. Expired-lease
// takeover requires the prior attempt terminal or reconciled-uncertain.
type Reconciliation struct {
	AttemptID  string    `json:"attemptId"`
	State      string    `json:"state"` // terminal outcome or "reconciled_uncertain"
	RecordedAt time.Time `json:"recordedAt"`
}

// Authority binds current run authority: generation, deadline, permission and
// allowance checks plus stable reservations. Implemented by lane B (T12);
// lanes C/D test against doubles of this interface.
type Authority interface {
	// Check validates run/generation/deadline/permission/allowance and
	// reports WHICH failed plus what remains allowed.
	Check(ctx context.Context, in CheckInput) error
	// Reserve takes a pre-dispatch hold. The replay identity excludes
	// credential secrets: rotation never turns a legitimate replay into a
	// new request, while same key + different payload conflicts.
	Reserve(ctx context.Context, runID string, operation string, idempotencyKey string, payloadHash string) (Reservation, error)
	// Release drops a reservation without consuming it (failed pre-dispatch).
	Release(ctx context.Context, reservationID string) error
	// Usage returns the current enforced/reserved/observed/unknown ledger.
	Usage(ctx context.Context, runID string) (UsageLedger, error)
	// ReconciliationFor returns how an uncertain attempt was settled, or
	// OutcomeNotFound when it was never reconciled.
	ReconciliationFor(ctx context.Context, attemptID string) (Reconciliation, error)
}

// Research event kinds (T06 §5). B adds run/turn/steering kinds at T08/T13.
const (
	EventClaim           = "claim"
	EventReuse           = "reuse"
	EventCapture         = "capture"
	EventObservation     = "observation"
	EventNote            = "note"
	EventRefresh         = "refresh"
	EventLeaseExpired    = "lease_expired"
	EventLeaseTakeover   = "lease_takeover"
	EventLateObservation = "late_observation"
	EventExhausted       = "exhausted"
)

// Event is one journaled activity record with a deduplicating ID.
type Event struct {
	ID                 string          `json:"eventId"`
	RunID              string          `json:"roundId"`
	AttemptID          string          `json:"attemptId,omitempty"`
	Kind               string          `json:"kind"`
	RequestFingerprint string          `json:"requestFingerprint,omitempty"`
	ObservationID      string          `json:"observationId,omitempty"`
	CaptureID          string          `json:"captureId,omitempty"`
	Outcome            Outcome         `json:"outcome,omitempty"`
	Payload            json.RawMessage `json:"payload,omitempty"`
	ObservedAt         time.Time       `json:"observedAt"`
	RecordedAt         time.Time       `json:"recordedAt"`
}

// EventSink appends and pages journaled events. B owns the mechanics;
// C/D supply research/save payloads.
type EventSink interface {
	Append(ctx context.Context, e Event) error
	List(ctx context.Context, runID string, cursor string, limit int) (events []Event, nextCursor string, err error)
}

// ActiveClaim names one live lease inside a checkpoint.
type ActiveClaim struct {
	Fingerprint string    `json:"fingerprint"`
	LeaseUntil  time.Time `json:"leaseUntil"`
}

// UnresolvedAttempt names one attempt still needing reconciliation.
type UnresolvedAttempt struct {
	AttemptID string `json:"attemptId"`
	Reason    string `json:"reason"`
}

// Checkpoint is the resumable run state (T06 §5). Codex may propose next
// work; the supervisor owns durable state.
type Checkpoint struct {
	ProfileVersion     int64               `json:"profileVersion"`
	RubricVersion      string              `json:"rubricVersion"`
	ActiveClaims       []ActiveClaim       `json:"activeClaims,omitempty"`
	EvidenceIDs        []string            `json:"evidenceIds,omitempty"`
	SavedRecordIDs     []string            `json:"savedRecordIds,omitempty"`
	UnresolvedAttempts []UnresolvedAttempt `json:"unresolvedAttempts,omitempty"`
	RemainingAllowance json.RawMessage     `json:"remainingAllowance,omitempty"` // opaque B struct
	NextWork           []string            `json:"nextWork,omitempty"`
	Generation         int64               `json:"generation"`
	UpdatedAt          time.Time           `json:"updatedAt"`
}
