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

const runEventColumns = `event_id,round_id,attempt_id,kind,request_fingerprint,` +
	`observation_id,capture_id,outcome,payload_json,observed_at,recorded_at`

// AppendRunEvent journals one activity record. event_id is the dedupe key;
// replays of the same id fail instead of duplicating.
func AppendRunEvent(ctx context.Context, db ResearchDB, e researchcontract.Event) error {
	if strings.TrimSpace(e.ID) == "" || e.RunID == "" {
		return fmt.Errorf("%w: event/run id required", ErrInvalid)
	}
	if trimmed := strings.TrimSpace(e.Kind); len(trimmed) < 1 || len(trimmed) > 64 {
		return fmt.Errorf("%w: event kind length 1..64 required", ErrInvalid)
	}
	if e.Outcome != "" && !researchcontract.Outcome(e.Outcome).Valid() {
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalid, e.Outcome)
	}
	var payload sql.NullString
	if len(e.Payload) > 0 {
		if !json.Valid(e.Payload) {
			return fmt.Errorf("%w: event payload must be valid JSON", ErrInvalid)
		}
		payload = nullString(string(e.Payload))
	}
	now := recordNow()
	observed := now
	if !e.ObservedAt.IsZero() {
		observed = formatResearchTime(e.ObservedAt)
	}
	recorded := now
	if !e.RecordedAt.IsZero() {
		recorded = formatResearchTime(e.RecordedAt)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO run_events
  (event_id,round_id,attempt_id,kind,request_fingerprint,observation_id,
   capture_id,outcome,payload_json,observed_at,recorded_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.RunID, nullString(e.AttemptID), e.Kind,
		nullString(e.RequestFingerprint), nullString(e.ObservationID),
		nullString(e.CaptureID), nullString(string(e.Outcome)), payload,
		observed, recorded)
	return err
}

// ListRunEvents pages one run's journal in record order. cursor is the last
// seen event_id ("" starts at the head); nextCursor is "" at the tail.
func ListRunEvents(ctx context.Context, r Reader, roundID, cursor string, limit int) (events []researchcontract.Event, nextCursor string, err error) {
	if roundID == "" {
		return nil, "", fmt.Errorf("%w: round id required", ErrInvalid)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const base = `SELECT ` + runEventColumns + ` FROM run_events WHERE round_id=?`
	var rows *sql.Rows
	if cursor == "" {
		rows, err = r.QueryContext(ctx, base+` ORDER BY recorded_at,event_id LIMIT ?`, roundID, limit)
	} else {
		var recorded string
		if err := r.QueryRowContext(ctx,
			`SELECT recorded_at FROM run_events WHERE event_id=? AND round_id=?`,
			cursor, roundID).Scan(&recorded); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", fmt.Errorf("%w: unknown event cursor", ErrInvalid)
			}
			return nil, "", err
		}
		rows, err = r.QueryContext(ctx, base+` AND (recorded_at,event_id) > (?,?)
  ORDER BY recorded_at,event_id LIMIT ?`, roundID, recorded, cursor, limit)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var e researchcontract.Event
		var attempt, fingerprint, observation, capture, outcome, payload sql.NullString
		var observed, recorded string
		if err := rows.Scan(&e.ID, &e.RunID, &attempt, &e.Kind, &fingerprint,
			&observation, &capture, &outcome, &payload, &observed, &recorded); err != nil {
			return nil, "", err
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
			return nil, "", err
		}
		if e.RecordedAt, err = parseResearchTime(recorded); err != nil {
			return nil, "", err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(events) == limit {
		nextCursor = events[len(events)-1].ID
	}
	return events, nextCursor, nil
}

// SaveRunCheckpoint upserts one run's resumable state (T06 §5). Codex may
// propose next work; the supervisor owns durable state.
func SaveRunCheckpoint(ctx context.Context, db ResearchDB, roundID string, cp researchcontract.Checkpoint) error {
	if roundID == "" {
		return fmt.Errorf("%w: round id required", ErrInvalid)
	}
	if cp.ProfileVersion <= 0 || cp.RubricVersion == "" {
		return fmt.Errorf("%w: brief profile/rubric version required", ErrInvalid)
	}
	if cp.Generation <= 0 {
		return fmt.Errorf("%w: checkpoint generation required", ErrInvalid)
	}
	marshal := func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		if string(raw) == "null" {
			return "[]", nil
		}
		return string(raw), nil
	}
	active, err := marshal(cp.ActiveClaims)
	if err != nil {
		return err
	}
	evidence, err := marshal(cp.EvidenceIDs)
	if err != nil {
		return err
	}
	saved, err := marshal(cp.SavedRecordIDs)
	if err != nil {
		return err
	}
	unresolved, err := marshal(cp.UnresolvedAttempts)
	if err != nil {
		return err
	}
	next, err := marshal(cp.NextWork)
	if err != nil {
		return err
	}
	var allowance sql.NullString
	if len(cp.RemainingAllowance) > 0 {
		if !json.Valid(cp.RemainingAllowance) {
			return fmt.Errorf("%w: remaining allowance must be valid JSON", ErrInvalid)
		}
		allowance = nullString(string(cp.RemainingAllowance))
	}
	updated := recordNow()
	if !cp.UpdatedAt.IsZero() {
		updated = formatResearchTime(cp.UpdatedAt)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO run_checkpoints
  (round_id,profile_version,rubric_version,active_claims_json,evidence_ids_json,
   saved_record_ids_json,unresolved_attempts_json,remaining_allowance_json,
   next_work_json,generation,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(round_id) DO UPDATE SET profile_version=excluded.profile_version,
   rubric_version=excluded.rubric_version,active_claims_json=excluded.active_claims_json,
   evidence_ids_json=excluded.evidence_ids_json,
   saved_record_ids_json=excluded.saved_record_ids_json,
   unresolved_attempts_json=excluded.unresolved_attempts_json,
   remaining_allowance_json=excluded.remaining_allowance_json,
   next_work_json=excluded.next_work_json,generation=excluded.generation,
   updated_at=excluded.updated_at`,
		roundID, cp.ProfileVersion, cp.RubricVersion, active, evidence, saved,
		unresolved, allowance, next, cp.Generation, updated)
	return err
}

// GetRunCheckpoint loads one run's checkpoint, or ErrNotFound.
func GetRunCheckpoint(ctx context.Context, r Reader, roundID string) (researchcontract.Checkpoint, error) {
	var cp researchcontract.Checkpoint
	var active, evidence, saved, unresolved, next, updated string
	var allowance sql.NullString
	err := r.QueryRowContext(ctx, `SELECT profile_version,rubric_version,
  active_claims_json,evidence_ids_json,saved_record_ids_json,
  unresolved_attempts_json,remaining_allowance_json,next_work_json,generation,
  updated_at FROM run_checkpoints WHERE round_id=?`, roundID).Scan(
		&cp.ProfileVersion, &cp.RubricVersion, &active, &evidence, &saved,
		&unresolved, &allowance, &next, &cp.Generation, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return researchcontract.Checkpoint{}, ErrNotFound
	}
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	unmarshal := func(raw string, v any) error {
		if err := json.Unmarshal([]byte(raw), v); err != nil {
			return err
		}
		return nil
	}
	if err := unmarshal(active, &cp.ActiveClaims); err != nil {
		return researchcontract.Checkpoint{}, err
	}
	if err := unmarshal(evidence, &cp.EvidenceIDs); err != nil {
		return researchcontract.Checkpoint{}, err
	}
	if err := unmarshal(saved, &cp.SavedRecordIDs); err != nil {
		return researchcontract.Checkpoint{}, err
	}
	if err := unmarshal(unresolved, &cp.UnresolvedAttempts); err != nil {
		return researchcontract.Checkpoint{}, err
	}
	if err := unmarshal(next, &cp.NextWork); err != nil {
		return researchcontract.Checkpoint{}, err
	}
	if allowance.Valid {
		cp.RemainingAllowance = json.RawMessage(allowance.String)
	}
	if cp.UpdatedAt, err = parseResearchTime(updated); err != nil {
		return researchcontract.Checkpoint{}, err
	}
	return cp, nil
}
