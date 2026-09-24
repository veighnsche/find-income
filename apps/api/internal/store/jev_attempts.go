package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// JevAttempt is a private immutable request and terminal-response record. Raw
// byte fields are BLOBs and are never JSON-reserialized by this repository.
type JevAttempt struct {
	ID                    string
	RoundID               string
	RoundAttemptID        string
	StepIndex             int
	Purpose               string
	InputSHA256           string
	SourceRefsJSON        []byte
	CandidateSetJSON      []byte
	ProfileVersion        int64
	RubricVersion         string
	RequestedModel        string
	ReturnedModel         string
	LogicalRequestJSON    []byte
	TransportRequestBytes []byte
	RawResponseBytes      []byte
	ResponseTruncated     bool
	ResponseReadError     bool
	Status                string
	ErrorKind             string
	HTTPStatus            *int
	InputTokens           *int64
	OutputTokens          *int64
	CreatedAt             string
	FinishedAt            string
}

type JevAttemptStart struct {
	RoundID               string
	RoundAttemptID        string
	StepIndex             int
	Purpose               string
	InputSHA256           string
	SourceRefsJSON        []byte
	CandidateSetJSON      []byte
	ProfileVersion        int64
	RubricVersion         string
	RequestedModel        string
	LogicalRequestJSON    []byte
	TransportRequestBytes []byte
}

type JevAttemptFinish struct {
	ID                string
	Status            string // succeeded, invalid_response, failed, budget_exceeded, uncertain
	ReturnedModel     string
	RawResponseBytes  []byte
	ResponseTruncated bool
	ResponseReadError bool
	ErrorKind         string
	HTTPStatus        *int
	InputTokens       *int64
	OutputTokens      *int64
}

const jevAttemptColumns = `id,round_id,round_attempt_id,step_index,purpose,input_sha256,
 source_refs_json,candidate_set_json,profile_version,rubric_version,requested_model,
 returned_model,logical_request_json,transport_request_bytes,raw_response_bytes,
 status,error_kind,http_status,input_tokens,output_tokens,response_truncated,response_read_error,created_at,finished_at`

func scanJevAttempt(row rowScanner) (JevAttempt, error) {
	var a JevAttempt
	var requested, returned, logical, transport, response, errorKind, finished sql.NullString
	var status sql.NullInt64
	var in, out sql.NullInt64
	var truncated, readError int
	var refs, candidates string
	err := row.Scan(&a.ID, &a.RoundID, &a.RoundAttemptID, &a.StepIndex, &a.Purpose,
		&a.InputSHA256, &refs, &candidates, &a.ProfileVersion, &a.RubricVersion,
		&requested, &returned, &logical, &transport, &response, &a.Status,
		&errorKind, &status, &in, &out, &truncated, &readError, &a.CreatedAt, &finished)
	if err != nil {
		return JevAttempt{}, err
	}
	a.SourceRefsJSON, a.CandidateSetJSON = []byte(refs), []byte(candidates)
	a.RequestedModel, a.ReturnedModel, a.ErrorKind, a.FinishedAt = requested.String, returned.String, errorKind.String, finished.String
	a.ResponseTruncated, a.ResponseReadError = truncated != 0, readError != 0
	if logical.Valid {
		a.LogicalRequestJSON = []byte(logical.String)
	}
	if transport.Valid {
		a.TransportRequestBytes = []byte(transport.String)
	}
	if response.Valid {
		a.RawResponseBytes = []byte(response.String)
	}
	if status.Valid {
		value := int(status.Int64)
		a.HTTPStatus = &value
	}
	if in.Valid {
		value := in.Int64
		a.InputTokens = &value
	}
	if out.Valid {
		value := out.Int64
		a.OutputTokens = &value
	}
	return a, nil
}

func validJevText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximum
}

func validJevJSON(value []byte, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && json.Valid(value)
}

func (s *Store) BeginJevAttempt(ctx context.Context, input JevAttemptStart) (JevAttempt, error) {
	if !validJevText(input.RoundID, 128) || !validJevText(input.RoundAttemptID, 128) || input.StepIndex != 0 ||
		!validJevText(input.Purpose, 80) || len(input.InputSHA256) != 64 ||
		!validJevJSON(input.SourceRefsJSON, 256<<10) || !validJevJSON(input.CandidateSetJSON, 256<<10) ||
		input.ProfileVersion < 1 || !validJevText(input.RubricVersion, 80) ||
		(input.RequestedModel != "" && !validJevText(input.RequestedModel, 100)) ||
		!validJevJSON(input.LogicalRequestJSON, 2<<20) || len(input.TransportRequestBytes) > 8<<20 {
		return JevAttempt{}, ErrInvalid
	}
	tx, round, err := s.roundWriter(ctx, input.RoundID)
	if err != nil {
		return JevAttempt{}, err
	}
	defer tx.Rollback()
	if err := requireRoundState(round, RoundRunning); err != nil {
		return JevAttempt{}, err
	}
	if input.ProfileVersion != round.ProfileVersion {
		return JevAttempt{}, ErrConflict
	}
	var state RoundAttemptState
	var generation int64
	var operation string
	var requests, items, tools, turns int64
	err = tx.QueryRowContext(ctx, `SELECT state,generation,operation,requests_reserved,items_reserved,tools_reserved,turns_reserved
 FROM round_attempts WHERE id=? AND round_id=?`, input.RoundAttemptID, input.RoundID).Scan(&state, &generation, &operation, &requests, &items, &tools, &turns)
	if errors.Is(err, sql.ErrNoRows) {
		return JevAttempt{}, ErrNotFound
	}
	if err != nil {
		return JevAttempt{}, err
	}
	cost, ok := RoundOperationCost(operation)
	if !ok || (operation != RoundJevRequest && operation != RoundJevAssess) {
		return JevAttempt{}, ErrFenced
	}
	if state != AttemptDispatched || generation != round.Generation ||
		(RoundAllowance{Requests: requests, Items: items, Tools: tools, Turns: turns}) != cost {
		return JevAttempt{}, ErrFenced
	}
	var existing int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM jev_attempts WHERE round_attempt_id=?`, input.RoundAttemptID).Scan(&existing)
	if err == nil {
		return JevAttempt{}, ErrFenced
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return JevAttempt{}, err
	}
	id, err := randomID()
	if err != nil {
		return JevAttempt{}, err
	}
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO jev_attempts
 (id,round_id,round_attempt_id,step_index,purpose,input_sha256,source_refs_json,candidate_set_json,
 profile_version,rubric_version,requested_model,logical_request_json,transport_request_bytes,status,created_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'dispatched',?)`, id, input.RoundID, input.RoundAttemptID, input.StepIndex, input.Purpose,
		input.InputSHA256, string(input.SourceRefsJSON), string(input.CandidateSetJSON), input.ProfileVersion,
		input.RubricVersion, optionalText(input.RequestedModel), input.LogicalRequestJSON, input.TransportRequestBytes, now)
	if err != nil {
		return JevAttempt{}, err
	}
	a, err := scanJevAttempt(tx.QueryRowContext(ctx, `SELECT `+jevAttemptColumns+` FROM jev_attempts WHERE id=?`, id))
	if err != nil {
		return JevAttempt{}, err
	}
	return a, tx.Commit()
}

// FinishJevAttempt records late and failed provider evidence even if the round
// was stopped. It never applies the decision or grants fresh round authority.
func (s *Store) FinishJevAttempt(ctx context.Context, input JevAttemptFinish) (JevAttempt, error) {
	if !validJevText(input.ID, 128) || (input.Status != "succeeded" && input.Status != "invalid_response" && input.Status != "failed" && input.Status != "budget_exceeded" && input.Status != "uncertain") ||
		(input.ReturnedModel != "" && !validJevText(input.ReturnedModel, 100)) ||
		len(input.RawResponseBytes) > 16<<20 || (input.ErrorKind != "" && !validJevText(input.ErrorKind, 100)) ||
		(input.HTTPStatus != nil && (*input.HTTPStatus < 0 || *input.HTTPStatus > 999)) {
		return JevAttempt{}, ErrInvalid
	}
	var status, in, out any
	if input.HTTPStatus != nil {
		status = *input.HTTPStatus
	}
	if input.InputTokens != nil {
		in = *input.InputTokens
	}
	if input.OutputTokens != nil {
		out = *input.OutputTokens
	}
	var response any
	if input.RawResponseBytes != nil {
		response = input.RawResponseBytes
	}
	result, err := s.db.ExecContext(ctx, `UPDATE jev_attempts SET status=?,returned_model=?,raw_response_bytes=?,
 error_kind=?,http_status=?,input_tokens=?,output_tokens=?,response_truncated=?,response_read_error=?,finished_at=? WHERE id=? AND status='dispatched'`,
		input.Status, optionalText(input.ReturnedModel), response, optionalText(input.ErrorKind), status, in, out, input.ResponseTruncated, input.ResponseReadError, utcNow(), input.ID)
	if err != nil {
		return JevAttempt{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return JevAttempt{}, err
	}
	if count != 1 {
		return JevAttempt{}, ErrFenced
	}
	return s.JevAttempt(ctx, input.ID)
}

func (s *Store) JevAttempt(ctx context.Context, id string) (JevAttempt, error) {
	if !validJevText(id, 128) {
		return JevAttempt{}, ErrInvalid
	}
	a, err := scanJevAttempt(s.db.QueryRowContext(ctx, `SELECT `+jevAttemptColumns+` FROM jev_attempts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return JevAttempt{}, ErrNotFound
	}
	return a, err
}

func (s *Store) JevAttemptsForRound(ctx context.Context, roundID string) ([]JevAttempt, error) {
	if !validJevText(roundID, 128) {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jevAttemptColumns+` FROM jev_attempts WHERE round_id=? ORDER BY created_at,id`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []JevAttempt
	for rows.Next() {
		a, err := scanJevAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}

// ReclassifyJevAttempt records a helper-level rejection after a typed provider
// response was already captured. It preserves the exact request/response.
func (s *Store) ReclassifyJevAttempt(ctx context.Context, id, status, errorKind string) error {
	if !validJevText(id, 128) || (status != "invalid_response" && status != "budget_exceeded") || !validJevText(errorKind, 100) {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE jev_attempts SET status=?,error_kind=? WHERE id=? AND status='succeeded'`, status, errorKind, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	return nil
}
