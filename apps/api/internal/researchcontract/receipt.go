package researchcontract

import (
	"context"
	"io"
	"time"
)

// ReceiptStatus is the backend-issued execution outcome (T06 §1, from T02).
type ReceiptStatus string

const (
	ReceiptOK        ReceiptStatus = "ok"
	ReceiptFailed    ReceiptStatus = "failed"
	ReceiptCanceled  ReceiptStatus = "canceled"
	ReceiptUncertain ReceiptStatus = "uncertain"
	ReceiptLate      ReceiptStatus = "late"
	ReceiptReused    ReceiptStatus = "reused"
	ReceiptShared    ReceiptStatus = "shared"
)

// Valid reports whether s is a known receipt status.
func (s ReceiptStatus) Valid() bool {
	switch s {
	case ReceiptOK, ReceiptFailed, ReceiptCanceled, ReceiptUncertain,
		ReceiptLate, ReceiptReused, ReceiptShared:
		return true
	}
	return false
}

// Outcome maps a receipt status to the shared tool outcome code.
func (s ReceiptStatus) Outcome() Outcome {
	switch s {
	case ReceiptOK, ReceiptLate, ReceiptShared:
		return OutcomeOK
	case ReceiptReused:
		return OutcomeReused
	case ReceiptFailed:
		return OutcomeInvalid
	case ReceiptCanceled:
		return OutcomeStopped
	case ReceiptUncertain:
		return OutcomeUncertain
	}
	return OutcomeInvalid
}

// ExecutorIdentity names the backend that produced a receipt or capture:
// backend plus version/digest (for example the pinned headless-shell build).
type ExecutorIdentity struct {
	Backend string `json:"backend"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// ExecutionReceipt is issued by the execution service and resolved
// server-side. A model assertion is never a receipt. Field names frozen.
type ExecutionReceipt struct {
	ID           string           `json:"id"`
	Operation    Operation        `json:"operation"`
	Fingerprint  string           `json:"fingerprint"`
	Status       ReceiptStatus    `json:"status"`
	CaptureID    string           `json:"captureId,omitempty"`
	Attempts     int              `json:"attempts"`
	Subrequests  int              `json:"subrequests"`
	BytesIn      int64            `json:"bytesIn"`
	BytesOut     int64            `json:"bytesOut"`
	UnknownUsage bool             `json:"unknownUsage"`
	Truncated    bool             `json:"truncated"`
	ErrorCode    string           `json:"errorCode,omitempty"`
	FinalURL     string           `json:"finalUrl,omitempty"`
	Redirects    []string         `json:"redirects,omitempty"`
	Executor     ExecutorIdentity `json:"executor"`
	StartedAt    time.Time        `json:"startedAt"`
	EndedAt      time.Time        `json:"endedAt"`
}

// ProvenanceKind records what kind of material a capture holds.
type ProvenanceKind string

const (
	ProvenanceFetchedResponse ProvenanceKind = "fetched_response"
	ProvenanceRenderedDOM     ProvenanceKind = "rendered_dom"
	ProvenanceSearchResult    ProvenanceKind = "search_result"
	ProvenanceOwnerStatement  ProvenanceKind = "owner_statement"
	ProvenanceModelNote       ProvenanceKind = "model_note"
)

// Capture is an immutable content-addressed artifact descriptor. The id is
// the sha256 of the stored bytes. Bytes live outside the model-writable
// workspace and the web root; excerpts bind (capture id, span, excerpt hash).
type Capture struct {
	ID           string            `json:"id"`
	Operation    Operation         `json:"operation"`
	Fingerprint  string            `json:"fingerprint"`
	Request      RequestDescriptor `json:"request"`
	Status       string            `json:"status"`
	FinalURL     string            `json:"finalUrl,omitempty"`
	Redirects    []string          `json:"redirects,omitempty"`
	ContentType  string            `json:"contentType,omitempty"`
	Complete     bool              `json:"complete"`
	ObservedAt   time.Time         `json:"observedAt"`
	DurationMs   int64             `json:"durationMs"`
	SHA256       string            `json:"sha256"`
	Bytes        int64             `json:"bytes"`
	Provenance   ProvenanceKind    `json:"provenance"`
	IsSnippet    bool              `json:"isSnippet"`
	Completeness string            `json:"completeness,omitempty"` // complete|truncated|paginated|partial
	Executor     ExecutorIdentity  `json:"executor"`
	Extra        map[string]string `json:"extra,omitempty"`
}

// CaptureReader resolves trusted receipts and opens immutable captures.
// Implemented by lane C (production, T11); consumed by lanes B/D/A.
type CaptureReader interface {
	// ResolveReceipt returns the backend-issued receipt for an opaque
	// receipt id. Unknown or forged ids return OutcomeNotFound/OutcomeInvalid.
	ResolveReceipt(ctx context.Context, receiptID string) (ExecutionReceipt, error)
	// OpenCapture returns the descriptor and a reader over the stored bytes.
	// The caller closes the reader. Integrity failures return OutcomeInvalid.
	OpenCapture(ctx context.Context, captureID string) (Capture, io.ReadCloser, error)
}
