package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// Research request states (T03 §1.1).
const (
	ResearchStateFree      = "free"
	ResearchStateClaimed   = "claimed"
	ResearchStateFresh     = "fresh"
	ResearchStateStale     = "stale"
	ResearchStateExhausted = "exhausted"
	ResearchStateUncertain = "uncertain"
)

// Justified refresh reasons (T06 §3).
const (
	RefreshReasonStale           = "stale"
	RefreshReasonChangedSource   = "changed_source"
	RefreshReasonCoverageGap     = "coverage_gap"
	RefreshReasonOwnerCorrection = "owner_correction"
)

// Observation outcomes (T03 §1.2). Empty/blocked/failed/rate-limited/
// uncertain outcomes are rows, not absences; late rows are retained but inert.
const (
	ObservationSuccess     = "success"
	ObservationEmpty       = "empty"
	ObservationBlocked     = "blocked"
	ObservationFailed      = "failed"
	ObservationRateLimited = "rate_limited"
	ObservationUncertain   = "uncertain"
	ObservationLate        = "late"
)

// ResearchLease is the live-claim lease on a claimed request.
type ResearchLease struct {
	Owner      string
	Generation int64
	Until      string
}

// ResearchRequest is one exact-request dedup + lease row. Lease is non-nil
// if and only if State is claimed.
type ResearchRequest struct {
	ID                  string
	ActorKind           string
	ActorID             string
	Fingerprint         string
	Request             researchcontract.RequestDescriptor
	CacheScope          researchcontract.CacheScope
	State               string
	Lease               *ResearchLease
	LatestObservationID string
	LatestCaptureID     string
	FreshUntil          string
	NegativeUntil       string
	RefreshReason       string
	CreatedAt           string
	UpdatedAt           string
}

// ResearchObservation is one immutable attempt row.
type ResearchObservation struct {
	ID               string
	RequestID        string
	AttemptNo        int64
	ActorKind        string
	ActorID          string
	RoundID          string
	RoundAttemptID   string
	Operation        researchcontract.Operation
	ActualURLOrQuery string
	ActualParamsJSON string
	StartedAt        string
	FinishedAt       string
	Outcome          string
	Provenance       researchcontract.ProvenanceKind
	ReceiptRef       string
	Executor         *researchcontract.ExecutorIdentity
	CaptureID        string
	ErrorCode        string
	TruncationNote   string
	IsLate           bool
	CreatedAt        string
}

// ResearchObservationInput carries the caller-supplied observation fields.
// Empty StartedAt/FinishedAt default to now; empty ActualParamsJSON stays NULL.
type ResearchObservationInput struct {
	RequestID        string
	AttemptNo        int64
	RoundID          string
	RoundAttemptID   string
	Operation        researchcontract.Operation
	ActualURLOrQuery string
	ActualParamsJSON string
	StartedAt        string
	FinishedAt       string
	Outcome          string
	Provenance       researchcontract.ProvenanceKind
	ReceiptRef       string
	Executor         *researchcontract.ExecutorIdentity
	CaptureID        string
	ErrorCode        string
	TruncationNote   string
	IsLate           bool
}

// ResearchRequestMutation updates lease/state/link columns. Lease must be
// non-nil if and only if State is claimed.
type ResearchRequestMutation struct {
	State               string
	Lease               *ResearchLease
	LatestObservationID string
	LatestCaptureID     string
	FreshUntil          string
	NegativeUntil       string
	RefreshReason       string
}

const researchRequestColumns = `id,actor_kind,actor_id,fingerprint,request_json,cache_scope,` +
	`state,lease_owner,lease_generation,lease_until,latest_observation_id,` +
	`latest_capture_id,fresh_until,negative_until,refresh_reason,created_at,updated_at`

// CreateResearchRequest inserts a free request row. The fingerprint is derived
// by calling RequestDescriptor.Fingerprint, never duplicated here.
func CreateResearchRequest(ctx context.Context, db ResearchDB, actor Actor, descriptor researchcontract.RequestDescriptor, scope researchcontract.CacheScope) (ResearchRequest, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return ResearchRequest{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if scope != researchcontract.CacheStatelessReusable && scope != researchcontract.CacheStatefulContext {
		return ResearchRequest{}, fmt.Errorf("%w: unknown cache scope %q", ErrInvalid, scope)
	}
	fingerprint, err := descriptor.Fingerprint()
	if err != nil {
		return ResearchRequest{}, err
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		return ResearchRequest{}, err
	}
	id, err := randomID()
	if err != nil {
		return ResearchRequest{}, err
	}
	now := recordNow()
	_, err = db.ExecContext(ctx, `INSERT INTO research_requests
  (id,actor_kind,actor_id,fingerprint,request_json,cache_scope,state,created_at,updated_at)
  VALUES (?,?,?,?,?,?, 'free',?,?)`,
		id, actor.Kind, actor.ID, fingerprint, string(raw), string(scope), now, now)
	if err != nil {
		return ResearchRequest{}, err
	}
	return ResearchRequest{
		ID: id, ActorKind: actor.Kind, ActorID: actor.ID,
		Fingerprint: fingerprint, Request: descriptor, CacheScope: scope,
		State: ResearchStateFree, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// GetResearchRequest loads one request by (actor, fingerprint).
func GetResearchRequest(ctx context.Context, r Reader, actorKind, actorID, fingerprint string) (ResearchRequest, error) {
	row := r.QueryRowContext(ctx, `SELECT `+researchRequestColumns+`
  FROM research_requests WHERE actor_kind=? AND actor_id=? AND fingerprint=?`,
		actorKind, actorID, fingerprint)
	return scanResearchRequest(row)
}

// GetResearchRequestByID loads one request by its row id (T11 lease lookup).
func GetResearchRequestByID(ctx context.Context, r Reader, id string) (ResearchRequest, error) {
	if id == "" {
		return ResearchRequest{}, fmt.Errorf("%w: request id required", ErrInvalid)
	}
	row := r.QueryRowContext(ctx, `SELECT `+researchRequestColumns+`
  FROM research_requests WHERE id=?`, id)
	req, err := scanResearchRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ResearchRequest{}, ErrNotFound
	}
	return req, err
}

// ListLiveClaims returns an actor's claimed rows, earliest expiry first.
func ListLiveClaims(ctx context.Context, r Reader, actorKind, actorID string, limit int) ([]ResearchRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.QueryContext(ctx, `SELECT `+researchRequestColumns+`
  FROM research_requests WHERE actor_kind=? AND actor_id=? AND state='claimed'
  ORDER BY lease_until LIMIT ?`, actorKind, actorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchRequest
	for rows.Next() {
		req, err := scanResearchRequestRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// NextResearchAttemptNo returns max(attempt_no)+1 for a request (T-observe).
func NextResearchAttemptNo(ctx context.Context, r Reader, requestID string) (int64, error) {
	var next sql.NullInt64
	if err := r.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_no),0)+1
  FROM research_observations WHERE request_id=?`, requestID).Scan(&next); err != nil {
		return 0, err
	}
	return next.Int64, nil
}

// GetResearchObservationByCapture loads the latest observation that
// captured a retrieval row id. Models sometimes pass the capture id
// where a receipt is required; resolving through the recorded
// observation keeps the save bound to the same trusted bytes instead
// of failing on the identifier mix-up.
func GetResearchObservationByCapture(ctx context.Context, r Reader, captureID string) (ResearchObservation, error) {
	if captureID == "" {
		return ResearchObservation{}, fmt.Errorf("%w: capture id required", ErrInvalid)
	}
	row := r.QueryRowContext(ctx, `SELECT `+researchObservationColumns+`
  FROM research_observations WHERE capture_id=? ORDER BY rowid DESC LIMIT 1`, captureID)
	obs, err := scanResearchObservationRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ResearchObservation{}, ErrNotFound
	}
	return obs, err
}

// GetResearchObservationByReceipt loads the observation carrying an executor
// receipt ref, for server-side receipt resolution (T11). Receipt refs are
// unique by writer discipline (checked inside the T-observe txn); the frozen
// schema carries no unique index for them.
func GetResearchObservationByReceipt(ctx context.Context, r Reader, receiptRef string) (ResearchObservation, error) {
	if receiptRef == "" {
		return ResearchObservation{}, fmt.Errorf("%w: receipt ref required", ErrInvalid)
	}
	row := r.QueryRowContext(ctx, `SELECT `+researchObservationColumns+`
  FROM research_observations WHERE receipt_ref=?`, receiptRef)
	obs, err := scanResearchObservationRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ResearchObservation{}, ErrNotFound
	}
	return obs, err
}

// ResearchAttemptState returns a round attempt's state for T11 lease
// decisions (expired-lease takeover needs a terminal prior attempt). It
// reads through the caller's handle so it composes inside ResearchWrite;
// the pool holds a single connection, so *Store queries cannot nest there.
func ResearchAttemptState(ctx context.Context, r Reader, attemptID string) (RoundAttemptState, error) {
	var state RoundAttemptState
	if err := r.QueryRowContext(ctx, `SELECT state FROM round_attempts WHERE id=?`,
		attemptID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return state, nil
}

// ResearchRoundExists reports whether a round row exists (event/observation
// FK precheck with a readable error instead of a constraint failure).
func ResearchRoundExists(ctx context.Context, r Reader, roundID string) (bool, error) {
	if roundID == "" {
		return false, nil
	}
	var one int
	if err := r.QueryRowContext(ctx, `SELECT 1 FROM rounds WHERE id=?`, roundID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// UpdateResearchRequest applies a lease/state mutation and returns the row.
func UpdateResearchRequest(ctx context.Context, db ResearchDB, id string, m ResearchRequestMutation) (ResearchRequest, error) {
	if !validEnum(m.State, ResearchStateFree, ResearchStateClaimed, ResearchStateFresh,
		ResearchStateStale, ResearchStateExhausted, ResearchStateUncertain) {
		return ResearchRequest{}, fmt.Errorf("%w: unknown request state %q", ErrInvalid, m.State)
	}
	if (m.State == ResearchStateClaimed) != (m.Lease != nil) {
		return ResearchRequest{}, fmt.Errorf("%w: lease must be set iff state is claimed", ErrInvalid)
	}
	if m.RefreshReason != "" && !validEnum(m.RefreshReason, RefreshReasonStale,
		RefreshReasonChangedSource, RefreshReasonCoverageGap, RefreshReasonOwnerCorrection) {
		return ResearchRequest{}, fmt.Errorf("%w: unknown refresh reason %q", ErrInvalid, m.RefreshReason)
	}
	var owner sql.NullString
	var generation sql.NullInt64
	var until sql.NullString
	if m.Lease != nil {
		if strings.TrimSpace(m.Lease.Owner) == "" || m.Lease.Generation <= 0 || m.Lease.Until == "" {
			return ResearchRequest{}, fmt.Errorf("%w: lease owner/generation/until required", ErrInvalid)
		}
		owner = nullString(m.Lease.Owner)
		generation = sql.NullInt64{Int64: m.Lease.Generation, Valid: true}
		until = nullString(m.Lease.Until)
	}
	now := recordNow()
	res, err := db.ExecContext(ctx, `UPDATE research_requests SET state=?,
  lease_owner=?,lease_generation=?,lease_until=?,latest_observation_id=?,
  latest_capture_id=?,fresh_until=?,negative_until=?,refresh_reason=?,updated_at=?
  WHERE id=?`,
		m.State, owner, generation, until,
		nullString(m.LatestObservationID), nullString(m.LatestCaptureID),
		nullString(m.FreshUntil), nullString(m.NegativeUntil),
		nullString(m.RefreshReason), now, id)
	if err != nil {
		return ResearchRequest{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ResearchRequest{}, err
	}
	if n == 0 {
		return ResearchRequest{}, ErrNotFound
	}
	return scanResearchRequest(db.QueryRowContext(ctx,
		`SELECT `+researchRequestColumns+` FROM research_requests WHERE id=?`, id))
}

// ListExpiredClaims returns claimed rows whose lease passed, for the T11
// expiry sweep.
func ListExpiredClaims(ctx context.Context, r Reader, now string, limit int) ([]ResearchRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.QueryContext(ctx, `SELECT `+researchRequestColumns+`
  FROM research_requests WHERE state='claimed' AND lease_until < ?
  ORDER BY lease_until LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchRequest
	for rows.Next() {
		req, err := scanResearchRequestRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

type researchRequestScanner interface {
	Scan(...any) error
}

func scanResearchRequest(row *sql.Row) (ResearchRequest, error) {
	var req ResearchRequest
	err := scanResearchRequestInto(row, &req)
	if errors.Is(err, sql.ErrNoRows) {
		return ResearchRequest{}, ErrNotFound
	}
	return req, err
}

func scanResearchRequestRows(rows *sql.Rows) (ResearchRequest, error) {
	var req ResearchRequest
	return req, scanResearchRequestInto(rows, &req)
}

func scanResearchRequestInto(s researchRequestScanner, req *ResearchRequest) error {
	var requestJSON, scope string
	var owner, until, latestObs, latestCap, fresh, negative, reason sql.NullString
	var generation sql.NullInt64
	if err := s.Scan(&req.ID, &req.ActorKind, &req.ActorID, &req.Fingerprint,
		&requestJSON, &scope, &req.State, &owner, &generation, &until,
		&latestObs, &latestCap, &fresh, &negative, &reason,
		&req.CreatedAt, &req.UpdatedAt); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(requestJSON), &req.Request); err != nil {
		return err
	}
	req.CacheScope = researchcontract.CacheScope(scope)
	if owner.Valid {
		req.Lease = &ResearchLease{Owner: owner.String, Generation: generation.Int64, Until: until.String}
	}
	req.LatestObservationID = latestObs.String
	req.LatestCaptureID = latestCap.String
	req.FreshUntil = fresh.String
	req.NegativeUntil = negative.String
	req.RefreshReason = reason.String
	return nil
}

const researchObservationColumns = `id,request_id,attempt_no,actor_kind,actor_id,` +
	`round_id,round_attempt_id,operation,actual_url_or_query,actual_params_json,` +
	`started_at,finished_at,outcome,provenance_kind,receipt_ref,executor_identity_json,` +
	`capture_id,error_code,truncation_note,is_late,created_at`

func validObservationProvenance(p researchcontract.ProvenanceKind) bool {
	switch p {
	case researchcontract.ProvenanceFetchedResponse, researchcontract.ProvenanceRenderedDOM,
		researchcontract.ProvenanceSearchResult, researchcontract.ProvenanceOwnerStatement,
		researchcontract.ProvenanceModelNote:
		return true
	}
	return false
}

// InsertResearchObservation appends one immutable attempt row.
func InsertResearchObservation(ctx context.Context, db ResearchDB, actor Actor, in ResearchObservationInput) (ResearchObservation, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return ResearchObservation{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if in.RequestID == "" || in.RoundID == "" || in.AttemptNo <= 0 {
		return ResearchObservation{}, fmt.Errorf("%w: request/round/attempt_no required", ErrInvalid)
	}
	if !in.Operation.Valid() {
		return ResearchObservation{}, fmt.Errorf("%w: unknown operation %q", ErrInvalid, in.Operation)
	}
	if in.ActualURLOrQuery == "" {
		return ResearchObservation{}, fmt.Errorf("%w: actual url_or_query required", ErrInvalid)
	}
	if !validEnum(in.Outcome, ObservationSuccess, ObservationEmpty, ObservationBlocked,
		ObservationFailed, ObservationRateLimited, ObservationUncertain, ObservationLate) {
		return ResearchObservation{}, fmt.Errorf("%w: unknown outcome %q", ErrInvalid, in.Outcome)
	}
	if !validObservationProvenance(in.Provenance) {
		return ResearchObservation{}, fmt.Errorf("%w: unknown provenance %q", ErrInvalid, in.Provenance)
	}
	var executor sql.NullString
	if in.Executor != nil {
		raw, err := json.Marshal(in.Executor)
		if err != nil {
			return ResearchObservation{}, err
		}
		executor = nullString(string(raw))
	}
	id, err := randomID()
	if err != nil {
		return ResearchObservation{}, err
	}
	now := recordNow()
	started := in.StartedAt
	if started == "" {
		started = now
	}
	_, err = db.ExecContext(ctx, `INSERT INTO research_observations
  (id,request_id,attempt_no,actor_kind,actor_id,round_id,round_attempt_id,operation,
   actual_url_or_query,actual_params_json,started_at,finished_at,outcome,provenance_kind,
   receipt_ref,executor_identity_json,capture_id,error_code,truncation_note,is_late,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, in.RequestID, in.AttemptNo, actor.Kind, actor.ID, in.RoundID,
		nullString(in.RoundAttemptID), string(in.Operation), in.ActualURLOrQuery,
		nullString(in.ActualParamsJSON), started, nullString(in.FinishedAt),
		in.Outcome, string(in.Provenance), nullString(in.ReceiptRef), executor,
		nullString(in.CaptureID), in.ErrorCode, in.TruncationNote, boolInt(in.IsLate), now)
	if err != nil {
		return ResearchObservation{}, err
	}
	return ResearchObservation{
		ID: id, RequestID: in.RequestID, AttemptNo: in.AttemptNo,
		ActorKind: actor.Kind, ActorID: actor.ID, RoundID: in.RoundID,
		RoundAttemptID: in.RoundAttemptID, Operation: in.Operation,
		ActualURLOrQuery: in.ActualURLOrQuery, ActualParamsJSON: in.ActualParamsJSON,
		StartedAt: started, FinishedAt: in.FinishedAt, Outcome: in.Outcome,
		Provenance: in.Provenance, ReceiptRef: in.ReceiptRef, Executor: in.Executor,
		CaptureID: in.CaptureID, ErrorCode: in.ErrorCode,
		TruncationNote: in.TruncationNote, IsLate: in.IsLate, CreatedAt: now,
	}, nil
}

// GetResearchObservation loads one observation by id.
func GetResearchObservation(ctx context.Context, r Reader, id string) (ResearchObservation, error) {
	row := r.QueryRowContext(ctx, `SELECT `+researchObservationColumns+`
  FROM research_observations WHERE id=?`, id)
	obs, err := scanResearchObservationRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ResearchObservation{}, ErrNotFound
	}
	return obs, err
}

// ListResearchObservations returns a request's attempts in attempt order.
func ListResearchObservations(ctx context.Context, r Reader, requestID string) ([]ResearchObservation, error) {
	rows, err := r.QueryContext(ctx, `SELECT `+researchObservationColumns+`
  FROM research_observations WHERE request_id=? ORDER BY attempt_no`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchObservation
	for rows.Next() {
		obs, err := scanResearchObservationRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	return out, rows.Err()
}

func scanResearchObservationRow(row *sql.Row) (ResearchObservation, error) {
	var obs ResearchObservation
	return obs, scanResearchObservationInto(row, &obs)
}

func scanResearchObservationRows(rows *sql.Rows) (ResearchObservation, error) {
	var obs ResearchObservation
	return obs, scanResearchObservationInto(rows, &obs)
}

func scanResearchObservationInto(s researchRequestScanner, obs *ResearchObservation) error {
	var operation, outcome, provenance string
	var roundAttempt, params, finished, receipt, executor, capture sql.NullString
	var isLate int64
	if err := s.Scan(&obs.ID, &obs.RequestID, &obs.AttemptNo, &obs.ActorKind,
		&obs.ActorID, &obs.RoundID, &roundAttempt, &operation,
		&obs.ActualURLOrQuery, &params, &obs.StartedAt, &finished, &outcome,
		&provenance, &receipt, &executor, &capture, &obs.ErrorCode,
		&obs.TruncationNote, &isLate, &obs.CreatedAt); err != nil {
		return err
	}
	obs.RoundAttemptID = roundAttempt.String
	obs.Operation = researchcontract.Operation(operation)
	obs.ActualParamsJSON = params.String
	obs.FinishedAt = finished.String
	obs.Outcome = outcome
	obs.Provenance = researchcontract.ProvenanceKind(provenance)
	obs.ReceiptRef = receipt.String
	if executor.Valid {
		var identity researchcontract.ExecutorIdentity
		if err := json.Unmarshal([]byte(executor.String), &identity); err != nil {
			return err
		}
		obs.Executor = &identity
	}
	obs.CaptureID = capture.String
	obs.IsLate = isLate == 1
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
