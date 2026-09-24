package store

// Durable run-supervision helpers. Owner: lane B (runtime), T13.
//
// T08 delivered the run/turn/steering event vocabulary plus an in-process
// journal (codexservice.RunEventSink) because no durable event table fit the
// run/attempt tables. T07 has since added run_events/run_checkpoints with
// C-owned accessors (research_events.go); this file binds the agreed
// researchcontract.EventSink durably to those tables and adds the small
// Reader-composable supervision reads the T13 supervisor needs. It does not
// modify any C-owned file or the frozen schema.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// MaxRunEventPayload bounds one journaled event payload. 32KB fits owner
// steering bodies (≤20KB per the T10 contract) plus the event envelope. The
// T08 in-process fake mirrors this bound so doubles accept what the durable
// journal accepts.
const MaxRunEventPayload = 32 * 1024

const (
	defaultJournalPage = 50
	maxJournalPage     = 100
)

// RunEventJournal is the durable researchcontract.EventSink: an append-only,
// cursor-paged journal over run_events. Appends are idempotent (first write
// wins per event id); events are immutable once written (schema triggers).
type RunEventJournal struct {
	s *Store
}

var _ researchcontract.EventSink = (*RunEventJournal)(nil)

// NewRunEventJournal binds a durable journal to a store.
func NewRunEventJournal(s *Store) (*RunEventJournal, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: journal store required", ErrInvalid)
	}
	return &RunEventJournal{s: s}, nil
}

func validateRunEvent(e researchcontract.Event) error {
	if strings.TrimSpace(e.ID) == "" || len(e.ID) > 300 || e.RunID == "" {
		return fmt.Errorf("%w: event/run id required", ErrInvalid)
	}
	if e.Kind == "" || len(e.Kind) > 64 {
		return fmt.Errorf("%w: event kind length 1..64 required", ErrInvalid)
	}
	if e.Outcome != "" && !e.Outcome.Valid() {
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalid, e.Outcome)
	}
	// Strict like the T08 fake: callers stamp observations; a missing stamp
	// is a caller bug, not a defaulting opportunity.
	if e.ObservedAt.IsZero() {
		return fmt.Errorf("%w: observed_at required", ErrInvalid)
	}
	if len(e.Payload) > MaxRunEventPayload {
		return fmt.Errorf("%w: event payload exceeds %d bytes", ErrInvalid, MaxRunEventPayload)
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return fmt.Errorf("%w: event payload must be valid JSON", ErrInvalid)
	}
	return nil
}

// AppendRunEventIdempotent journals one event inside any research transaction.
// A repeated event id is an idempotent no-op (first write wins); unknown
// run/attempt/observation/capture references fail with ErrNotFound.
func AppendRunEventIdempotent(ctx context.Context, db ResearchDB, e researchcontract.Event) error {
	if err := validateRunEvent(e); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("%w: journal handle required", ErrInvalid)
	}
	var payload sql.NullString
	if len(e.Payload) > 0 {
		payload = nullString(string(e.Payload))
	}
	recorded := formatResearchTime(time.Now())
	if !e.RecordedAt.IsZero() {
		recorded = formatResearchTime(e.RecordedAt)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO run_events
  (event_id,round_id,attempt_id,kind,request_fingerprint,observation_id,
   capture_id,outcome,payload_json,observed_at,recorded_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(event_id) DO NOTHING`,
		e.ID, e.RunID, nullString(e.AttemptID), e.Kind,
		nullString(e.RequestFingerprint), nullString(e.ObservationID),
		nullString(e.CaptureID), nullString(string(e.Outcome)), payload,
		formatResearchTime(e.ObservedAt), recorded)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return fmt.Errorf("%w: journal references an unknown run, attempt, observation or capture", ErrNotFound)
		}
		return err
	}
	return nil
}

// Append journals one event idempotently.
func (j *RunEventJournal) Append(ctx context.Context, e researchcontract.Event) error {
	if j == nil || j.s == nil {
		return fmt.Errorf("%w: journal store required", ErrInvalid)
	}
	if err := validateRunEvent(e); err != nil {
		return err
	}
	return j.s.ResearchWrite(ctx, func(db ResearchDB) error {
		return AppendRunEventIdempotent(ctx, db, e)
	})
}

// List pages one run's journal in record order with T08-compatible paging
// (default 50, max 100). The cursor is the last seen event id; nextCursor is
// "" exactly at the tail (no phantom extra page).
func (j *RunEventJournal) List(ctx context.Context, runID string, cursor string, limit int) ([]researchcontract.Event, string, error) {
	if j == nil || j.s == nil {
		return nil, "", fmt.Errorf("%w: journal store required", ErrInvalid)
	}
	if limit <= 0 {
		limit = defaultJournalPage
	}
	if limit > maxJournalPage {
		limit = maxJournalPage
	}
	// Fetch one past the page to decide the tail exactly.
	events, _, err := ListRunEvents(ctx, j.s.db, runID, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	if len(events) <= limit {
		return events, "", nil
	}
	return events[:limit], events[limit-1].ID, nil
}

// GetRunEventTx loads one event by id, or ErrNotFound.
func GetRunEventTx(ctx context.Context, r Reader, eventID string) (researchcontract.Event, error) {
	var e researchcontract.Event
	if r == nil || eventID == "" {
		return e, fmt.Errorf("%w: event id required", ErrInvalid)
	}
	var attempt, fingerprint, observation, capture, outcome, payload sql.NullString
	var observed, recorded string
	err := r.QueryRowContext(ctx, `SELECT `+runEventColumns+` FROM run_events WHERE event_id=?`,
		eventID).Scan(&e.ID, &e.RunID, &attempt, &e.Kind, &fingerprint,
		&observation, &capture, &outcome, &payload, &observed, &recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.AttemptID = attempt.String
	e.RequestFingerprint = fingerprint.String
	e.ObservationID = observation.String
	e.CaptureID = capture.String
	e.Outcome = researchcontract.Outcome(outcome.String)
	if payload.Valid {
		e.Payload = json.RawMessage(payload.String)
	}
	if e.ObservedAt, err = parseResearchTime(observed); err != nil {
		return e, err
	}
	if e.RecordedAt, err = parseResearchTime(recorded); err != nil {
		return e, err
	}
	return e, nil
}

// RunEvent loads one journaled event by id, or ErrNotFound.
func (j *RunEventJournal) RunEvent(ctx context.Context, eventID string) (researchcontract.Event, error) {
	if j == nil || j.s == nil {
		return researchcontract.Event{}, fmt.Errorf("%w: journal store required", ErrInvalid)
	}
	return GetRunEventTx(ctx, j.s.db, eventID)
}

// LatestRoundThreadTx returns the most recently bound conversation thread for
// a run, across all control generations: a stored thread outlives Stop and
// crash recovery, and continuation resumes it. No bound thread returns "".
func LatestRoundThreadTx(ctx context.Context, r Reader, roundID string) (string, error) {
	if r == nil || roundID == "" {
		return "", fmt.Errorf("%w: round id required", ErrInvalid)
	}
	var thread sql.NullString
	err := r.QueryRowContext(ctx, `SELECT thread_id FROM round_remote_dispatches
  WHERE round_id=? AND thread_id IS NOT NULL AND thread_id<>''
  ORDER BY updated_at DESC, attempt_id DESC LIMIT 1`, roundID).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return thread.String, nil
}

func scanRoundAttempts(rows *sql.Rows) ([]RoundAttempt, error) {
	var out []RoundAttempt
	for rows.Next() {
		a, err := scanRoundAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// StaleReservedAttemptsTx lists reserved (never dispatched) attempts whose
// generation no longer owns the run. Stop reaps them: the fence already
// revoked their dispatch authority, so releasing refunds holds for work that
// can never run.
func StaleReservedAttemptsTx(ctx context.Context, r Reader, roundID string, currentGeneration int64) ([]RoundAttempt, error) {
	if r == nil || roundID == "" || currentGeneration < 1 {
		return nil, fmt.Errorf("%w: round id and current generation required", ErrInvalid)
	}
	rows, err := r.QueryContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts
  WHERE round_id=? AND state='reserved' AND generation<>? ORDER BY created_at,id`,
		roundID, currentGeneration)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRoundAttempts(rows)
}

// DispatchedTurnAttemptTx returns the in-flight model turn at one generation,
// or ErrNotFound when no turn holds the conversation. Steering uses it to
// decide between the live-turn path and the queued-for-next-turn path.
func DispatchedTurnAttemptTx(ctx context.Context, r Reader, roundID string, generation int64) (RoundAttempt, error) {
	if r == nil || roundID == "" || generation < 1 {
		return RoundAttempt{}, fmt.Errorf("%w: round id and generation required", ErrInvalid)
	}
	a, err := scanRoundAttempt(r.QueryRowContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts
  WHERE round_id=? AND operation=? AND state='dispatched' AND generation=?
  ORDER BY dispatched_at,id LIMIT 1`, roundID, RoundCodexTurn, generation))
	if errors.Is(err, sql.ErrNoRows) {
		return RoundAttempt{}, ErrNotFound
	}
	return a, err
}

// UncertainAttemptsTx lists uncertain attempts in any round state. Unlike
// UncertainRoundAttempts (paused/stopping only), it also serves checkpoint
// assembly on a running round.
func UncertainAttemptsTx(ctx context.Context, r Reader, roundID string) ([]RoundAttempt, error) {
	if r == nil || roundID == "" {
		return nil, fmt.Errorf("%w: round id required", ErrInvalid)
	}
	rows, err := r.QueryContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts
  WHERE round_id=? AND state='uncertain' ORDER BY created_at,id`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRoundAttempts(rows)
}
