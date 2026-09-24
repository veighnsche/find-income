package researchcontract

import (
	"context"
	"time"
)

// ExecuteKind selects the research execution path (T06 §8). The executor is
// infrastructure; it defines no list of job sources.
type ExecuteKind string

const (
	ExecuteSearch ExecuteKind = "search"
	ExecuteFetch  ExecuteKind = "fetch"
	ExecuteBrowse ExecuteKind = "browse"
	ExecuteAPI    ExecuteKind = "api"
	ExecuteExec   ExecuteKind = "exec"
)

// Bounds caps one execution. Resource bounds produce explicit incomplete
// captures, never hidden truncation.
type Bounds struct {
	MaxBytes    int64 `json:"maxBytes"`
	MaxRequests int   `json:"maxRequests"`
	DeadlineMs  int64 `json:"deadlineMs"`
}

// ExecuteInput scopes one general-research call to the active run.
type ExecuteInput struct {
	Kind           ExecuteKind       `json:"kind"`
	Request        RequestDescriptor `json:"request"`
	Bounds         Bounds            `json:"bounds"`
	IdempotencyKey string            `json:"idempotencyKey"`
	RunID          string            `json:"runId"`
	Generation     int64             `json:"generation"`
}

// ExecuteUsage accounts one execution: actual requests/bytes/redirects.
type ExecuteUsage struct {
	Requests  int   `json:"requests"`
	Bytes     int64 `json:"bytes"`
	Redirects int   `json:"redirects"`
}

// ExecuteOutput is the automatic claim → dispatch → capture → observation →
// receipt result. Codex performs no ceremonial memory calls before dispatch.
type ExecuteOutput struct {
	Outcome       Outcome          `json:"outcome"`
	ObservationID string           `json:"observationId,omitempty"`
	CaptureID     string           `json:"captureId,omitempty"`
	Receipt       ExecutionReceipt `json:"receipt"`
	Usage         ExecuteUsage     `json:"usage"`
}

// Executor runs general research operations. Implemented by lane C (T16);
// lane B tests supervision against a fake of this interface.
type Executor interface {
	Execute(ctx context.Context, in ExecuteInput) (ExecuteOutput, error)
}

// Lease is one live exact-request claim.
type Lease struct {
	LeaseID    string    `json:"leaseId"`
	Owner      string    `json:"owner"`
	Generation int64     `json:"generation"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// MemoryClaim is the result of claiming or looking up an exact request.
type MemoryClaim struct {
	Outcome     Outcome  `json:"outcome"`
	Reusable    []string `json:"reusableResults,omitempty"` // observation/capture ids
	Lease       *Lease   `json:"lease,omitempty"`
	Failed      []string `json:"failedOrUncertain,omitempty"`
	RefreshFrom string   `json:"refreshFrom,omitempty"` // prior observation id when refreshing
}

// ClaimStore owns exact-request memory: lookup, claim/renew/release,
// conclusions, checkpoints and justified refresh. Implemented by lane C
// (T11); investigation-intent operations ride the same interface while
// general execution claims automatically.
type ClaimStore interface {
	Lookup(ctx context.Context, runID string, request RequestDescriptor) (MemoryClaim, error)
	Claim(ctx context.Context, runID string, owner string, generation int64, request RequestDescriptor, idempotencyKey string) (MemoryClaim, error)
	Renew(ctx context.Context, leaseID string, owner string, generation int64) (Lease, error)
	Release(ctx context.Context, leaseID string, owner string, generation int64, observationID string) error
	Refresh(ctx context.Context, runID string, owner string, generation int64, request RequestDescriptor, reason string) (MemoryClaim, error)
}
