package researchmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// CaptureInput carries one captured artifact for T-observe. Bytes are
// persisted to the private artifact root before the row commits; the digest
// is recomputed from the bytes (a caller-supplied ContentSHA256 must match).
// IsSnippet nil defaults to true for search_result provenance.
type CaptureInput struct {
	Bytes             []byte
	ContentSHA256     string
	MediaType         string
	HTTPStatus        *int64
	OriginalURL       string
	FinalURL          string
	RedirectChainJSON string
	Provenance        researchcontract.ProvenanceKind
	Completeness      string
	ExtentJSON        string
	RoleID            string
	IsSnippet         *bool
}

// ObserveInput carries one T-observe call: the lease being settled, the
// executor-issued receipt, the capture (unless a non-content outcome) and
// the observed result. ActualURLOrQuery records what was really requested.
type ObserveInput struct {
	LeaseID          string
	Owner            string
	Generation       int64
	RoundID          string
	RoundAttemptID   string
	ActualURLOrQuery string
	ActualParamsJSON string
	StartedAt        time.Time
	FinishedAt       time.Time
	Outcome          string
	Provenance       researchcontract.ProvenanceKind
	Receipt          researchcontract.ExecutionReceipt
	Capture          *CaptureInput
	ErrorCode        string
	TruncationNote   string
}

// ObserveOutput is the recorded result. Late reports that the lease was
// already lost: the row is immutable but inert (is_late=1, no state change)
// and the returned error carries outcome stale.
type ObserveOutput struct {
	Outcome       researchcontract.Outcome
	ObservationID string
	CaptureID     string
	Late          bool
}

// validReportedOutcome reports whether an outcome may arrive from a worker.
// "late" is server-assigned on the fenced path, never worker-reported.
func validReportedOutcome(outcome string) bool {
	switch outcome {
	case store.ObservationSuccess, store.ObservationEmpty, store.ObservationBlocked,
		store.ObservationFailed, store.ObservationRateLimited, store.ObservationUncertain:
		return true
	}
	return false
}

func validObservationProvenance(p researchcontract.ProvenanceKind) bool {
	switch p {
	case researchcontract.ProvenanceFetchedResponse, researchcontract.ProvenanceRenderedDOM,
		researchcontract.ProvenanceSearchResult, researchcontract.ProvenanceOwnerStatement,
		researchcontract.ProvenanceModelNote:
		return true
	}
	return false
}

func validCompleteness(c string) bool {
	switch c {
	case store.CaptureComplete, store.CaptureTruncated, store.CapturePaginated, store.CapturePartial:
		return true
	}
	return false
}

// Observe records one attempt (T-observe): observation insert + capture row +
// request latest_*/state update in one BEGIN IMMEDIATE txn. Artifact bytes
// and the receipt file are persisted before the row commits. A worker whose
// lease is lost, foreign or expired lands only an immutable is_late
// observation and receives outcome stale; late rows never flip request
// state, release leases or commit records.
func (m *Memory) Observe(ctx context.Context, in ObserveInput) (ObserveOutput, error) {
	if in.LeaseID == "" || in.Owner == "" || in.Generation <= 0 || in.RoundID == "" {
		return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"observe", "observe requires leaseId, owner, generation and roundId")
	}
	if in.ActualURLOrQuery == "" {
		return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actualUrlOrQuery", "observe requires the actual url_or_query")
	}
	if !validReportedOutcome(in.Outcome) {
		return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"outcome", "unknown or server-assigned outcome "+in.Outcome)
	}
	if !validObservationProvenance(in.Provenance) {
		return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"provenance", "unknown provenance "+string(in.Provenance))
	}
	if err := checkReceiptForObserve(in.Receipt, in.Outcome, in.ErrorCode, in.Capture); err != nil {
		return ObserveOutput{}, err
	}
	if in.Provenance == researchcontract.ProvenanceModelNote && in.Capture != nil {
		return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"provenance", "model_note observations carry no capture (provenance firewall)")
	}
	if in.Capture == nil {
		switch in.Outcome {
		case store.ObservationSuccess:
			return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"outcome", "a success observation requires capture bytes (capture-before-claims)")
		case store.ObservationEmpty, store.ObservationBlocked,
			store.ObservationFailed, store.ObservationRateLimited, store.ObservationUncertain:
		}
	} else {
		if !validCompleteness(in.Capture.Completeness) {
			return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"completeness", "unknown completeness "+in.Capture.Completeness)
		}
		if in.Capture.Completeness != store.CaptureComplete && in.TruncationNote == "" {
			return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"truncationNote", "incomplete captures require an explicit truncation note")
		}
		if len(in.Capture.Bytes) == 0 {
			return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"bytes", "capture bytes must not be empty")
		}
		if in.Capture.RedirectChainJSON != "" && !json.Valid([]byte(in.Capture.RedirectChainJSON)) {
			return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"redirectChain", "redirect chain must be valid JSON")
		}
		if in.Capture.ExtentJSON != "" && !json.Valid([]byte(in.Capture.ExtentJSON)) {
			return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"extent", "extent must be valid JSON")
		}
	}
	if in.ActualParamsJSON != "" && !json.Valid([]byte(in.ActualParamsJSON)) {
		return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actualParams", "actual params must be valid JSON")
	}

	// Stamp the receipt from the claimed request: the stored receipt file is
	// always complete (fingerprint/operation/captureId), so resolution never
	// depends on what the executor chose to echo.
	var reqFP string
	var reqOP researchcontract.Operation
	err := m.db.Read(ctx, func(r store.Reader) error {
		var err error
		req, err := store.GetResearchRequestByID(ctx, r, in.LeaseID)
		if errors.Is(err, store.ErrNotFound) || req.ActorKind != m.actor.Kind || req.ActorID != m.actor.ID {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"lease", "unknown lease "+in.LeaseID)
		}
		if err != nil {
			return err
		}
		reqFP, reqOP = req.Fingerprint, req.Request.Operation
		return nil
	})
	if err != nil {
		return ObserveOutput{}, err
	}
	if in.Receipt.Fingerprint == "" {
		in.Receipt.Fingerprint = reqFP
	}
	if !in.Receipt.Operation.Valid() {
		in.Receipt.Operation = reqOP
	}

	// Pre-commit persistence: bytes + receipt exist before the row commits.
	var contentSHA, artifactRef string
	if in.Capture != nil {
		sum := sha256.Sum256(in.Capture.Bytes)
		contentSHA = hex.EncodeToString(sum[:])
		if in.Capture.ContentSHA256 != "" && in.Capture.ContentSHA256 != contentSHA {
			return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"contentSHA256", "capture bytes do not match the claimed digest")
		}
		if in.Receipt.CaptureID != "" && in.Receipt.CaptureID != contentSHA {
			return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"receipt", "receipt captureId does not match the captured bytes")
		}
		in.Receipt.CaptureID = contentSHA
		ref, err := m.artifacts.PutBlob(contentSHA, in.Capture.Bytes)
		if err != nil {
			return ObserveOutput{}, err
		}
		artifactRef = ref
	} else if in.Receipt.CaptureID != "" {
		return ObserveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"receipt", "receipt names a capture but no bytes were supplied")
	}
	if err := m.artifacts.PutReceipt(in.Receipt); err != nil {
		return ObserveOutput{}, err
	}

	var out ObserveOutput
	now := m.now()
	err = m.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		req, err := store.GetResearchRequestByID(ctx, db, in.LeaseID)
		if errors.Is(err, store.ErrNotFound) || req.ActorKind != m.actor.Kind || req.ActorID != m.actor.ID {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"lease", "unknown lease "+in.LeaseID)
		}
		if err != nil {
			return err
		}
		if in.Receipt.Fingerprint != "" && in.Receipt.Fingerprint != req.Fingerprint {
			return researchcontract.NewError(researchcontract.OutcomeInvalid,
				"receipt", "receipt fingerprint does not match the claimed request")
		}
		if in.Receipt.Operation.Valid() && in.Receipt.Operation != req.Request.Operation {
			return researchcontract.NewError(researchcontract.OutcomeInvalid,
				"receipt", "receipt operation does not match the claimed request")
		}
		if _, err := store.GetResearchObservationByReceipt(ctx, db, in.Receipt.ID); err == nil {
			return researchcontract.NewError(researchcontract.OutcomeConflict,
				"receipt", "receipt "+in.Receipt.ID+" is already recorded")
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		ok, err := store.ResearchRoundExists(ctx, db, in.RoundID)
		if err != nil {
			return err
		}
		if !ok {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"roundId", "unknown run "+in.RoundID)
		}
		if in.RoundAttemptID != "" {
			if _, err := store.ResearchAttemptState(ctx, db, in.RoundAttemptID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return researchcontract.NewError(researchcontract.OutcomeNotFound,
						"roundAttemptId", "unknown attempt "+in.RoundAttemptID)
				}
				return err
			}
		}

		fresh := req.State == store.ResearchStateClaimed && req.Lease != nil &&
			req.Lease.Owner == in.Owner && req.Lease.Generation == in.Generation &&
			leaseValid(req, now)
		outcome := in.Outcome
		isLate := !fresh
		if isLate && in.Capture != nil {
			outcome = store.ObservationLate
		}
		var captureID string
		if in.Capture != nil {
			isSnippet := in.Provenance == researchcontract.ProvenanceSearchResult
			if in.Capture.IsSnippet != nil {
				isSnippet = *in.Capture.IsSnippet
			}
			exec := in.Receipt.Executor
			retrieved := formatTime(now)
			if !in.FinishedAt.IsZero() {
				retrieved = formatTime(in.FinishedAt)
			}
			capt, err := store.InsertSourceCapture(ctx, db, store.SourceCaptureInput{
				ContentSHA256: contentSHA, ArtifactRef: artifactRef,
				ByteLength: int64(len(in.Capture.Bytes)), MediaType: in.Capture.MediaType,
				HTTPStatus: in.Capture.HTTPStatus, OriginalURL: in.Capture.OriginalURL,
				FinalURL: in.Capture.FinalURL, RedirectChainJSON: in.Capture.RedirectChainJSON,
				RetrievedAt: retrieved, Provenance: in.Provenance,
				Completeness: in.Capture.Completeness, ExtentJSON: in.Capture.ExtentJSON,
				Executor: exec, RoleID: in.Capture.RoleID, IsSnippet: isSnippet,
			})
			if err != nil {
				return err
			}
			captureID = capt.ID
		}
		attemptNo, err := store.NextResearchAttemptNo(ctx, db, req.ID)
		if err != nil {
			return err
		}
		started, finished := "", ""
		if !in.StartedAt.IsZero() {
			started = formatTime(in.StartedAt)
		}
		if !in.FinishedAt.IsZero() {
			finished = formatTime(in.FinishedAt)
		}
		execIdentity := in.Receipt.Executor
		obs, err := store.InsertResearchObservation(ctx, db, m.actor, store.ResearchObservationInput{
			RequestID: req.ID, AttemptNo: attemptNo, RoundID: in.RoundID,
			RoundAttemptID: in.RoundAttemptID, Operation: req.Request.Operation,
			ActualURLOrQuery: in.ActualURLOrQuery, ActualParamsJSON: in.ActualParamsJSON,
			StartedAt: started, FinishedAt: finished, Outcome: outcome,
			Provenance: in.Provenance, ReceiptRef: in.Receipt.ID,
			Executor: &execIdentity, CaptureID: captureID, ErrorCode: in.ErrorCode,
			TruncationNote: in.TruncationNote, IsLate: isLate,
		})
		if err != nil {
			return err
		}
		out = ObserveOutput{Outcome: researchcontract.OutcomeOK,
			ObservationID: obs.ID, CaptureID: captureID, Late: isLate}
		attemptRef := in.RoundAttemptID
		if attemptRef == "" {
			attemptRef = eventAttemptID(ctx, db, in.Owner)
		}
		if isLate {
			return store.AppendRunEvent(ctx, db, researchcontract.Event{
				ID: newEventID(), RunID: in.RoundID, AttemptID: attemptRef,
				Kind:               researchcontract.EventLateObservation,
				RequestFingerprint: req.Fingerprint, ObservationID: obs.ID,
				CaptureID: captureID, Outcome: researchcontract.OutcomeStale,
				ObservedAt: now, RecordedAt: now,
			})
		}
		if captureID != "" {
			if err := store.AppendRunEvent(ctx, db, researchcontract.Event{
				ID: newEventID(), RunID: in.RoundID, AttemptID: attemptRef,
				Kind: researchcontract.EventCapture, RequestFingerprint: req.Fingerprint,
				ObservationID: obs.ID, CaptureID: captureID,
				Outcome:    researchcontract.OutcomeOK,
				ObservedAt: now, RecordedAt: now,
			}); err != nil {
				return err
			}
		}
		if err := store.AppendRunEvent(ctx, db, researchcontract.Event{
			ID: newEventID(), RunID: in.RoundID, AttemptID: attemptRef,
			Kind: researchcontract.EventObservation, RequestFingerprint: req.Fingerprint,
			ObservationID: obs.ID, CaptureID: captureID,
			Outcome:    researchcontract.OutcomeOK,
			ObservedAt: now, RecordedAt: now,
		}); err != nil {
			return err
		}
		state, freshUntil, negativeUntil, err := m.commitOutcome(ctx, db, req, obs, now)
		if err != nil {
			return err
		}
		_, err = store.UpdateResearchRequest(ctx, db, req.ID, store.ResearchRequestMutation{
			State: state, LatestObservationID: obs.ID, LatestCaptureID: captureID,
			FreshUntil: freshUntil, NegativeUntil: negativeUntil,
			RefreshReason: req.RefreshReason,
		})
		return err
	})
	if err != nil {
		return ObserveOutput{}, err
	}
	if out.Late {
		out.Outcome = researchcontract.OutcomeStale
		return out, researchcontract.NewError(researchcontract.OutcomeStale,
			"lease", "lease was already lost; content retained as a late observation only")
	}
	return out, nil
}

// checkReceiptForObserve enforces receipt/observation consistency before any
// write: status validity, status-to-outcome mapping and truncation honesty.
func checkReceiptForObserve(rec researchcontract.ExecutionReceipt, outcome, errorCode string, capture *CaptureInput) error {
	if rec.ID == "" {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"receipt", "observe requires the executor-issued receipt id")
	}
	if !receiptIDShape.MatchString(rec.ID) {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"receipt", "receipt id has an unsupported shape")
	}
	if !rec.Status.Valid() {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"receipt", "unknown receipt status "+string(rec.Status))
	}
	switch rec.Status {
	case researchcontract.ReceiptFailed:
		if outcome != store.ObservationFailed {
			return researchcontract.NewError(researchcontract.OutcomeInvalid,
				"receipt", "a failed receipt requires a failed observation")
		}
	case researchcontract.ReceiptUncertain:
		if outcome != store.ObservationUncertain {
			return researchcontract.NewError(researchcontract.OutcomeInvalid,
				"receipt", "an uncertain receipt requires an uncertain observation")
		}
	case researchcontract.ReceiptCanceled:
		if outcome != store.ObservationFailed || errorCode != ErrorCodeCanceled {
			return researchcontract.NewError(researchcontract.OutcomeInvalid,
				"receipt", "a canceled receipt requires a failed observation with errorCode canceled")
		}
	case researchcontract.ReceiptReused:
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"receipt", "a reused receipt records nothing new; use Lookup instead")
	}
	if rec.Truncated && (capture == nil || capture.Completeness == store.CaptureComplete) {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"receipt", "a truncated receipt requires an explicitly incomplete capture")
	}
	return nil
}

// NoteInput carries the caller-supplied investigation-note fields for AddNote
// (T-note): a single note insert plus its journaled event, never sharing a
// transaction with business writes.
type NoteInput struct {
	RoundID             string
	BriefProfileVersion int64
	BriefRubricVersion  string
	Intent              string
	UsefulnessJSON      string
	CoverageJSON        string
	OverlapRefsJSON     string
	Conclusion          string
	EvidenceRefsJSON    string
	OutstandingJSON     string
	SupersedesID        string
}

// AddNote appends one investigation note (model-authored memory, never
// evidence) and journals it.
func (m *Memory) AddNote(ctx context.Context, in NoteInput) (store.ResearchNote, error) {
	if in.RoundID == "" {
		return store.ResearchNote{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "roundId", "a note requires its run id")
	}
	var out store.ResearchNote
	now := m.now()
	err := m.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		ok, err := store.ResearchRoundExists(ctx, db, in.RoundID)
		if err != nil {
			return err
		}
		if !ok {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"roundId", "unknown run "+in.RoundID)
		}
		note, err := store.InsertResearchNote(ctx, db, m.actor, store.ResearchNoteInput{
			RoundID: in.RoundID, BriefProfileVersion: in.BriefProfileVersion,
			BriefRubricVersion: in.BriefRubricVersion, Intent: in.Intent,
			UsefulnessJSON: in.UsefulnessJSON, CoverageJSON: in.CoverageJSON,
			OverlapRefsJSON: in.OverlapRefsJSON, Conclusion: in.Conclusion,
			EvidenceRefsJSON: in.EvidenceRefsJSON, OutstandingJSON: in.OutstandingJSON,
			SupersedesID: in.SupersedesID,
		})
		if err != nil {
			if errors.Is(err, store.ErrInvalid) {
				return researchcontract.NewError(researchcontract.OutcomeInvalid,
					"note", err.Error())
			}
			return err
		}
		out = note
		return store.AppendRunEvent(ctx, db, researchcontract.Event{
			ID: newEventID(), RunID: in.RoundID, Kind: researchcontract.EventNote,
			Outcome:    researchcontract.OutcomeOK,
			Payload:    eventPayload(map[string]string{"noteId": note.ID}),
			ObservedAt: now, RecordedAt: now,
		})
	})
	return out, err
}

// ExpireLeases journals lease_expired events for expired claimed rows. It
// flips no state: expiry and staleness are derived from lease_until and the
// freshness TTLs at read time, so correctness never depends on the sweep
// schedule. Rounds resolve via the latest observation; rows without one
// emit no event.
func (m *Memory) ExpireLeases(ctx context.Context, limit int) (int, error) {
	now := m.now()
	count := 0
	err := m.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		expired, err := store.ListExpiredClaims(ctx, db, formatTime(now), limit)
		if err != nil {
			return err
		}
		for _, req := range expired {
			if req.LatestObservationID == "" {
				continue
			}
			obs, err := store.GetResearchObservation(ctx, db, req.LatestObservationID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			// Deterministic per-lease event id: repeats are idempotent,
			// distinct leases stay distinct.
			if err := store.AppendRunEvent(ctx, db, researchcontract.Event{
				ID:        "lease-expired:" + req.ID + ":" + req.Lease.Until,
				RunID:     obs.RoundID,
				AttemptID: eventAttemptID(ctx, db, req.Lease.Owner), Kind: researchcontract.EventLeaseExpired,
				RequestFingerprint: req.Fingerprint, ObservationID: req.LatestObservationID,
				Outcome:    researchcontract.OutcomeStale,
				ObservedAt: now, RecordedAt: now,
			}); err != nil {
				if isUniqueViolation(err) {
					continue // event already journaled; sweep is idempotent
				}
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

// LiveClaims returns the actor's current live leases for checkpoints and
// context reads.
func (m *Memory) LiveClaims(ctx context.Context, limit int) ([]researchcontract.ActiveClaim, error) {
	var out []researchcontract.ActiveClaim
	now := m.now()
	err := m.db.Read(ctx, func(r store.Reader) error {
		rows, err := store.ListLiveClaims(ctx, r, m.actor.Kind, m.actor.ID, limit)
		if err != nil {
			return err
		}
		for _, req := range rows {
			if !leaseValid(req, now) {
				continue
			}
			until, _ := parseTime(req.Lease.Until)
			out = append(out, researchcontract.ActiveClaim{
				Fingerprint: req.Fingerprint, LeaseUntil: until})
		}
		return nil
	})
	return out, err
}

// CheckpointData reads one run's checkpoint (B owns the mechanics; C reads
// the research payload for context/checkpoint responses).
func (m *Memory) CheckpointData(ctx context.Context, runID string) (researchcontract.Checkpoint, error) {
	var cp researchcontract.Checkpoint
	if runID == "" {
		return cp, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"runId", "a checkpoint read requires its run id")
	}
	err := m.db.Read(ctx, func(r store.Reader) error {
		var err error
		cp, err = store.GetRunCheckpoint(ctx, r, runID)
		if errors.Is(err, store.ErrNotFound) {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"runId", "no checkpoint for run "+runID)
		}
		return err
	})
	return cp, err
}

// SearchNotes runs an FTS5 overlap query over investigation notes. The query
// is model prose, never raw MATCH: it is tokenized into quoted terms so
// punctuation cannot inject FTS syntax or column filters.
func (m *Memory) SearchNotes(ctx context.Context, query string, limit int) ([]store.ResearchNote, error) {
	match, err := ftsTerms(query)
	if err != nil {
		return nil, err
	}
	var out []store.ResearchNote
	err = m.db.Read(ctx, func(r store.Reader) error {
		notes, err := store.SearchResearchNotes(ctx, r, match, limit)
		if err != nil {
			if errors.Is(err, store.ErrInvalid) {
				return researchcontract.NewError(researchcontract.OutcomeInvalid,
					"intent", err.Error())
			}
			return err
		}
		out = notes
		return nil
	})
	return out, err
}

// ftsTerms tokenizes model prose into a safe FTS5 conjunction of quoted
// terms (bounded to 10). Raw MATCH syntax never reaches the engine.
func ftsTerms(query string) (string, error) {
	var terms []string
	for _, tok := range strings.FieldsFunc(query, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		if tok == "" {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
		if len(terms) >= 10 {
			break
		}
	}
	if len(terms) == 0 {
		return "", researchcontract.NewError(researchcontract.OutcomeInvalid,
			"intent", "intent carries no searchable terms")
	}
	return strings.Join(terms, " "), nil
}

// eventAttemptID resolves an event's attempt reference: the owner's id only
// when it names a real round attempt, else "" (the column is nullable and
// lease owners may be worker ids, not attempts).
func eventAttemptID(ctx context.Context, db store.ResearchDB, owner string) string {
	if owner == "" {
		return ""
	}
	if _, err := store.ResearchAttemptState(ctx, db, owner); err != nil {
		return ""
	}
	return owner
}
