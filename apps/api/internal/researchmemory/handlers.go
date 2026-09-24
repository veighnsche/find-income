package researchmemory

import (
	"context"
	"errors"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// This file supplies the C-owned tool handlers for T17 registration. Each
// constructor binds its dependencies; each Handle method matches the bridge
// shape func(context.Context, Args) (map[string]any, error) so lane B can
// register it directly. Contract outcomes (claimed_elsewhere, stale,
// invalid, ...) travel IN-BAND as {"outcome": ...} maps with a nil error,
// because the bridge collapses handler errors into one generic tool error;
// a non-nil error means an internal failure only.

// outcomeOf maps a typed contract error to its in-band response. ok=false
// means err is internal and must propagate as a handler error.
func outcomeOf(err error) (map[string]any, bool) {
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) {
		return nil, false
	}
	out := map[string]any{"outcome": string(cerr.Code)}
	if cerr.Field != "" {
		out["field"] = cerr.Field
	}
	if cerr.Detail != "" {
		out["detail"] = cerr.Detail
	}
	if hint, ok := nextSteps[cerr.Code]; ok {
		out["nextSteps"] = hint
	}
	return out, true
}

var nextSteps = map[researchcontract.Outcome][]string{
	researchcontract.OutcomeClaimedElsewhere:  {"another worker holds this request; investigate elsewhere or retry after the lease expires"},
	researchcontract.OutcomeStale:             {"this lease is lost; late content is retained read-only and cannot commit state"},
	researchcontract.OutcomeUncertain:         {"reconcile the attempt before any retry; never automatically replay an unknown outcome"},
	researchcontract.OutcomeCaptureIncomplete: {"fetch or cite the full source before making a complete claim"},
	researchcontract.OutcomeNotFound:          {"check the id; receipts and captures resolve server-side only"},
}

func invalidArgs(field, detail string) (map[string]any, error) {
	out, _ := outcomeOf(researchcontract.NewError(researchcontract.OutcomeInvalid, field, detail))
	return out, nil
}

// ---------------------------------------------------------------------------
// research_memory
// ---------------------------------------------------------------------------

// ResearchMemoryArgs is the research_memory tool input (T05 §2).
type ResearchMemoryArgs struct {
	Op             string                              `json:"op"`
	RunID          string                              `json:"runId,omitempty"`
	Owner          string                              `json:"owner,omitempty"`
	Generation     int64                               `json:"generation,omitempty"`
	IdempotencyKey string                              `json:"idempotencyKey,omitempty"`
	Request        *researchcontract.RequestDescriptor `json:"requestDescriptor,omitempty"`
	LeaseID        string                              `json:"leaseId,omitempty"`
	ObservationID  string                              `json:"observationId,omitempty"`
	RefreshReason  string                              `json:"refreshReason,omitempty"`
	Intent         string                              `json:"intent,omitempty"`
	Conclusion     *ConclusionArgs                     `json:"conclusion,omitempty"`
}

// ConclusionArgs carries a conclude payload: the brief pair plus note fields.
type ConclusionArgs struct {
	BriefProfileVersion int64  `json:"briefProfileVersion"`
	BriefRubricVersion  string `json:"briefRubricVersion"`
	Intent              string `json:"intent,omitempty"`
	UsefulnessJSON      string `json:"usefulness,omitempty"`
	CoverageJSON        string `json:"coverage,omitempty"`
	Conclusion          string `json:"conclusion,omitempty"`
	EvidenceRefsJSON    string `json:"evidenceRefs,omitempty"`
	OutstandingJSON     string `json:"outstanding,omitempty"`
	SupersedesID        string `json:"supersedesId,omitempty"`
}

// ResearchMemoryHandler serves the research_memory capability.
type ResearchMemoryHandler struct{ mem *Memory }

// NewResearchMemoryHandler binds the handler to its memory.
func NewResearchMemoryHandler(mem *Memory) *ResearchMemoryHandler {
	return &ResearchMemoryHandler{mem: mem}
}

// Handle dispatches lookup|claim|renew|release|conclude|checkpoint|refresh.
func (h *ResearchMemoryHandler) Handle(ctx context.Context, args ResearchMemoryArgs) (map[string]any, error) {
	switch args.Op {
	case "lookup":
		return h.lookup(ctx, args)
	case "claim":
		return h.claim(ctx, args, false)
	case "refresh":
		return h.claim(ctx, args, true)
	case "renew":
		return h.renew(ctx, args)
	case "release":
		return h.release(ctx, args)
	case "conclude":
		return h.conclude(ctx, args)
	case "checkpoint":
		return h.checkpoint(ctx, args)
	default:
		return invalidArgs("op", "unknown research_memory op "+args.Op)
	}
}

func (h *ResearchMemoryHandler) needRequest(args ResearchMemoryArgs) (researchcontract.RequestDescriptor, map[string]any, error) {
	if args.Request == nil {
		out, _ := invalidArgs("requestDescriptor", "this op requires a requestDescriptor")
		return researchcontract.RequestDescriptor{}, out, nil
	}
	return *args.Request, nil, nil
}

func claimMap(claim researchcontract.MemoryClaim) map[string]any {
	out := map[string]any{"outcome": string(claim.Outcome)}
	if len(claim.Reusable) > 0 {
		out["reusableResults"] = claim.Reusable
	}
	if len(claim.Failed) > 0 {
		out["failedOrUncertain"] = claim.Failed
	}
	if claim.Lease != nil {
		out["lease"] = map[string]any{"leaseId": claim.Lease.LeaseID,
			"owner": claim.Lease.Owner, "generation": claim.Lease.Generation,
			"expiresAt": claim.Lease.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z")}
	}
	if claim.RefreshFrom != "" {
		out["refreshFrom"] = claim.RefreshFrom
	}
	if hint, ok := nextSteps[claim.Outcome]; ok {
		out["nextSteps"] = hint
	}
	return out
}

func (h *ResearchMemoryHandler) withOverlaps(ctx context.Context, out map[string]any, intent string) (map[string]any, error) {
	if strings.TrimSpace(intent) == "" {
		return out, nil
	}
	notes, err := h.mem.SearchNotes(ctx, intent, 5)
	if err != nil {
		if strings.Contains(err.Error(), "syntax error") {
			return invalidArgs("intent", "intent is not a valid search query")
		}
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	var overlaps []map[string]any
	for _, n := range notes {
		overlaps = append(overlaps, map[string]any{"noteId": n.ID,
			"intent": n.Intent, "conclusion": n.Conclusion})
	}
	if len(overlaps) > 0 {
		out["overlappingInvestigations"] = overlaps
	}
	return out, nil
}

func (h *ResearchMemoryHandler) lookup(ctx context.Context, args ResearchMemoryArgs) (map[string]any, error) {
	req, out, err := h.needRequest(args)
	if out != nil || err != nil {
		return out, err
	}
	claim, err := h.mem.Lookup(ctx, args.RunID, req)
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	return h.withOverlaps(ctx, claimMap(claim), args.Intent)
}

func (h *ResearchMemoryHandler) claim(ctx context.Context, args ResearchMemoryArgs, isRefresh bool) (map[string]any, error) {
	req, out, err := h.needRequest(args)
	if out != nil || err != nil {
		return out, err
	}
	var claim researchcontract.MemoryClaim
	if isRefresh {
		claim, err = h.mem.Refresh(ctx, args.RunID, args.Owner, args.Generation, req, args.RefreshReason)
	} else {
		claim, err = h.mem.Claim(ctx, args.RunID, args.Owner, args.Generation, req, args.IdempotencyKey)
	}
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	return h.withOverlaps(ctx, claimMap(claim), args.Intent)
}

func (h *ResearchMemoryHandler) renew(ctx context.Context, args ResearchMemoryArgs) (map[string]any, error) {
	if args.LeaseID == "" || args.Owner == "" || args.Generation <= 0 {
		return invalidArgs("lease", "renew requires leaseId, owner and generation")
	}
	lease, err := h.mem.Renew(ctx, args.LeaseID, args.Owner, args.Generation)
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	return map[string]any{"outcome": string(researchcontract.OutcomeOK),
		"lease": map[string]any{"leaseId": lease.LeaseID, "owner": lease.Owner,
			"generation": lease.Generation,
			"expiresAt":  lease.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z")}}, nil
}

func (h *ResearchMemoryHandler) release(ctx context.Context, args ResearchMemoryArgs) (map[string]any, error) {
	if args.LeaseID == "" || args.Owner == "" || args.Generation <= 0 {
		return invalidArgs("lease", "release requires leaseId, owner and generation")
	}
	if err := h.mem.Release(ctx, args.LeaseID, args.Owner, args.Generation, args.ObservationID); err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	return map[string]any{"outcome": string(researchcontract.OutcomeOK)}, nil
}

func (h *ResearchMemoryHandler) conclude(ctx context.Context, args ResearchMemoryArgs) (map[string]any, error) {
	if args.Conclusion == nil {
		return invalidArgs("conclusion", "conclude requires a conclusion payload")
	}
	c := args.Conclusion
	note, err := h.mem.AddNote(ctx, NoteInput{
		RoundID: args.RunID, BriefProfileVersion: c.BriefProfileVersion,
		BriefRubricVersion: c.BriefRubricVersion, Intent: firstNonEmpty(c.Intent, args.Intent),
		UsefulnessJSON: c.UsefulnessJSON, CoverageJSON: c.CoverageJSON,
		Conclusion: c.Conclusion, EvidenceRefsJSON: c.EvidenceRefsJSON,
		OutstandingJSON: c.OutstandingJSON, SupersedesID: c.SupersedesID,
	})
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	return map[string]any{"outcome": string(researchcontract.OutcomeOK),
		"noteId": note.ID}, nil
}

func (h *ResearchMemoryHandler) checkpoint(ctx context.Context, args ResearchMemoryArgs) (map[string]any, error) {
	if args.RunID == "" {
		return invalidArgs("runId", "checkpoint requires runId")
	}
	cp, err := h.mem.CheckpointData(ctx, args.RunID)
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	claims, err := h.mem.LiveClaims(ctx, 100)
	if err != nil {
		return nil, err
	}
	var active []map[string]any
	for _, c := range claims {
		active = append(active, map[string]any{"fingerprint": c.Fingerprint,
			"leaseUntil": c.LeaseUntil.UTC().Format("2006-01-02T15:04:05.999999999Z")})
	}
	return map[string]any{"outcome": string(researchcontract.OutcomeOK),
		"briefVersion": map[string]any{"profileVersion": cp.ProfileVersion,
			"rubricVersion": cp.RubricVersion},
		"activeClaims": active, "evidenceIds": cp.EvidenceIDs,
		"savedRecordIds": cp.SavedRecordIDs, "unresolvedAttempts": cp.UnresolvedAttempts,
		"nextWork": cp.NextWork, "generation": cp.Generation}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// evidence_capture
// ---------------------------------------------------------------------------

// ExcerptArgs binds one quoted span to its immutable capture version.
type ExcerptArgs struct {
	SpanStart     int64  `json:"spanStart"`
	SpanEnd       int64  `json:"spanEnd"`
	ExcerptSHA256 string `json:"excerptSha256"`
}

// EvidenceCaptureArgs is the evidence_capture tool input (T05 §3).
type EvidenceCaptureArgs struct {
	Receipt        string       `json:"receipt"`
	Excerpt        *ExcerptArgs `json:"excerpt,omitempty"`
	AssertComplete bool         `json:"assertComplete,omitempty"`
	ProvenanceNote string       `json:"provenanceNote,omitempty"`
}

// EvidenceCaptureHandler serves the evidence_capture capability.
type EvidenceCaptureHandler struct{ caps *Captures }

// NewEvidenceCaptureHandler binds the handler to its capture reader.
func NewEvidenceCaptureHandler(caps *Captures) *EvidenceCaptureHandler {
	return &EvidenceCaptureHandler{caps: caps}
}

// Handle resolves a trusted receipt server-side, verifies the immutable
// capture (and an optional quoted span), and reports its extent. A
// provenanceNote is echoed, not stored: durable notes ride
// research_memory/conclude.
func (h *EvidenceCaptureHandler) Handle(ctx context.Context, args EvidenceCaptureArgs) (map[string]any, error) {
	if args.Receipt == "" {
		return invalidArgs("receipt", "evidence_capture requires the trusted receipt id")
	}
	rec, err := h.caps.ResolveReceipt(ctx, args.Receipt)
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	if rec.CaptureID == "" {
		// Receipt without content: report the receipt outcome honestly.
		return map[string]any{"outcome": string(rec.Status.Outcome()),
			"receiptStatus": string(rec.Status), "errorCode": rec.ErrorCode,
			"finalUrl": rec.FinalURL}, nil
	}
	if args.Excerpt != nil {
		if err := h.caps.VerifyExcerpt(ctx, rec.CaptureID,
			args.Excerpt.SpanStart, args.Excerpt.SpanEnd, args.Excerpt.ExcerptSHA256); err != nil {
			if o, ok := outcomeOf(err); ok {
				return o, nil
			}
			return nil, err
		}
	}
	if args.AssertComplete {
		if err := h.caps.RequireFullCapture(ctx, rec.CaptureID); err != nil {
			if o, ok := outcomeOf(err); ok {
				return o, nil
			}
			return nil, err
		}
	}
	_, rc, err := h.caps.OpenCapture(ctx, rec.CaptureID)
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	_ = rc.Close()
	info, err := h.caps.Info(ctx, rec.CaptureID)
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	obs, err := h.caps.ObservationForReceipt(ctx, args.Receipt)
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	extent := map[string]any{"bytes": info.ByteLength,
		"complete": info.Completeness == store.CaptureComplete}
	if obs.TruncationNote != "" {
		extent["truncation"] = obs.TruncationNote
	}
	out := map[string]any{"outcome": string(researchcontract.OutcomeOK),
		"captureId": info.ContentSHA256, "observedUrl": info.OriginalURL,
		"finalUrl": info.FinalURL, "retrievedAt": info.RetrievedAt,
		"status": httpStatusText(info.HTTPStatus), "mediaType": info.MediaType,
		"contentHash": info.ContentSHA256, "extent": extent,
		"isSnippet": info.IsSnippet, "completeness": info.Completeness,
		"contentRef": "capture:" + info.ContentSHA256}
	if args.ProvenanceNote != "" {
		out["provenanceNote"] = args.ProvenanceNote
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// context_read
// ---------------------------------------------------------------------------

// ContextSubject selects one record, investigation or run to read.
type ContextSubject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ContextReadArgs is the context_read tool input (T05 §1).
type ContextReadArgs struct {
	Brief      bool            `json:"brief,omitempty"`
	Profile    bool            `json:"profile,omitempty"`
	Subject    *ContextSubject `json:"subject,omitempty"`
	Checkpoint string          `json:"checkpoint,omitempty"`
	Cursor     string          `json:"cursor,omitempty"`
	Limit      int             `json:"limit,omitempty"`
}

// ContextReadHandler serves the context_read capability: run checkpoints,
// live claims, journaled activity, investigation notes and run limits.
// Owner brief facts and company/opportunity records are NOT served here;
// T17 composes them from the owner-fact and D-owned record readers, and
// this handler names that deferral explicitly instead of inventing data.
type ContextReadHandler struct {
	db        *store.Store
	mem       *Memory
	authority researchcontract.Authority
}

// NewContextReadHandler binds the handler to its store, memory and authority.
func NewContextReadHandler(db *store.Store, mem *Memory, authority researchcontract.Authority) *ContextReadHandler {
	return &ContextReadHandler{db: db, mem: mem, authority: authority}
}

// Handle reads versioned context without side effects.
func (h *ContextReadHandler) Handle(ctx context.Context, args ContextReadArgs) (map[string]any, error) {
	runID := args.Checkpoint
	var includeNotes bool
	if args.Subject != nil {
		switch args.Subject.Kind {
		case "run", "investigation":
			if args.Subject.ID == "" {
				return invalidArgs("subject", "subject id is required")
			}
			runID = args.Subject.ID
			includeNotes = args.Subject.Kind == "investigation"
		case "company", "opportunity":
			return invalidArgs("subject", "record reads are served by the D-owned record reader composed at T17")
		default:
			return invalidArgs("subject", "unknown subject kind "+args.Subject.Kind)
		}
	}
	if runID == "" {
		return invalidArgs("checkpoint", "context_read requires a checkpoint run id or a run/investigation subject")
	}
	limit := args.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	out := map[string]any{"outcome": string(researchcontract.OutcomeOK)}
	var deferred []string
	if args.Brief || args.Profile {
		deferred = append(deferred, "owner brief facts and preferences are served by the T17-composed brief reader")
	}
	cp, err := h.mem.CheckpointData(ctx, runID)
	if err != nil {
		if o, ok := outcomeOf(err); ok {
			return o, nil
		}
		return nil, err
	}
	out["briefVersion"] = map[string]any{"profileVersion": cp.ProfileVersion,
		"rubricVersion": cp.RubricVersion}
	claims, err := h.mem.LiveClaims(ctx, 100)
	if err != nil {
		return nil, err
	}
	var active []map[string]any
	for _, c := range claims {
		active = append(active, map[string]any{"fingerprint": c.Fingerprint,
			"leaseUntil": c.LeaseUntil.UTC().Format("2006-01-02T15:04:05.999999999Z")})
	}
	out["activeClaims"] = active
	out["unfinishedWork"] = cp.NextWork
	out["uncertainty"] = cp.UnresolvedAttempts
	out["evidenceRefs"] = cp.EvidenceIDs
	var events []researchcontract.Event
	var next string
	err = h.db.Read(ctx, func(r store.Reader) error {
		var err error
		events, next, err = store.ListRunEvents(ctx, r, runID, args.Cursor, limit)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			return invalidArgs("cursor", "unknown activity cursor")
		}
		return nil, err
	}
	var history []map[string]any
	for _, e := range events {
		history = append(history, map[string]any{"eventId": e.ID,
			"kind": e.Kind, "outcome": string(e.Outcome),
			"requestFingerprint": e.RequestFingerprint,
			"observationId":      e.ObservationID, "captureId": e.CaptureID,
			"observedAt": e.ObservedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")})
	}
	out["history"] = history
	if next != "" {
		out["nextCursor"] = next
	}
	if includeNotes {
		var notes []store.ResearchNote
		err := h.db.Read(ctx, func(r store.Reader) error {
			var err error
			notes, err = store.ListResearchNotesByRound(ctx, r, runID)
			return err
		})
		if err != nil {
			return nil, err
		}
		var items []map[string]any
		for _, n := range notes {
			items = append(items, map[string]any{"noteId": n.ID,
				"intent": n.Intent, "conclusion": n.Conclusion})
		}
		out["notes"] = items
	}
	ledger, err := h.authority.Usage(ctx, runID)
	if err != nil {
		return nil, err
	}
	out["runLimits"] = map[string]any{
		"enforced": allowanceMap(ledger.Enforced), "reserved": allowanceMap(ledger.Reserved),
		"observed": allowanceMap(ledger.Observed), "unknown": ledger.Unknown}
	if len(deferred) > 0 {
		out["deferred"] = deferred
	}
	return out, nil
}

func allowanceMap(a researchcontract.Allowance) map[string]any {
	return map[string]any{"requests": a.Requests, "items": a.Items,
		"tools": a.Tools, "turns": a.Turns}
}
