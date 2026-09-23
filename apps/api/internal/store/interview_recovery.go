package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// RecoverCapturedInterviewFocusAttempt settles only the charged Jev request.
// A saved brief or focus never supplies terminal evidence for its Codex turn.
func (s *Store) RecoverCapturedInterviewFocusAttempt(ctx context.Context, roundID, attemptID, jevID string, generation int64, briefSHA string) (bool, error) {
	if roundID == "" || attemptID == "" || jevID == "" || generation < 1 || len(briefSHA) != 64 {
		return false, ErrInvalid
	}
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if round.State != RoundPaused || round.Generation != generation || round.Outcome != "interview_prepare" || !time.Now().Before(round.Deadline) {
		return false, ErrFenced
	}
	var operation, resource, requestKey string
	var attemptGeneration int64
	var attemptState RoundAttemptState
	err = tx.QueryRowContext(ctx, `SELECT operation,resource_id,request_key,generation,state FROM round_attempts WHERE id=? AND round_id=?`, attemptID, roundID).
		Scan(&operation, &resource, &requestKey, &attemptGeneration, &attemptState)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if operation != RoundJevRequest || attemptState != AttemptUncertain || attemptGeneration < 1 || attemptGeneration > generation {
		return false, ErrFenced
	}
	var interviewID, opportunityID string
	var opportunityRevision, profile int64
	var brief []byte
	err = tx.QueryRowContext(ctx, `SELECT id,opportunity_id,opportunity_revision,profile_version,brief_json FROM interviews WHERE round_id=?`, roundID).
		Scan(&interviewID, &opportunityID, &opportunityRevision, &profile, &brief)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(brief)
	if len(brief) == 0 || hex.EncodeToString(digest[:]) != briefSHA || resource != "opportunity:"+opportunityID ||
		requestKey != "interview-focus:"+interviewID+"/0" || profile != round.ProfileVersion ||
		!scopeHas(round.Scope.InputRefs, "interview:"+interviewID) || !scopeHas(round.Scope.Resources, resource) {
		return false, ErrFenced
	}
	var currentRevision, currentProfile int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&currentRevision); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); err != nil {
		return false, err
	}
	if currentRevision != opportunityRevision || currentProfile != profile {
		return false, ErrConflict
	}
	var status, purpose, requestedModel, returnedModel, inputSHA string
	var logical, raw []byte
	var truncated, readError int
	var inTokens, outTokens sql.NullInt64
	var captureProfile int64
	err = tx.QueryRowContext(ctx, `SELECT status,purpose,COALESCE(requested_model,''),COALESCE(returned_model,''),input_sha256,
        logical_request_json,raw_response_bytes,response_truncated,response_read_error,input_tokens,output_tokens,profile_version
        FROM jev_attempts WHERE id=? AND round_id=? AND round_attempt_id=?`, jevID, roundID, attemptID).
		Scan(&status, &purpose, &requestedModel, &returnedModel, &inputSHA, &logical, &raw, &truncated, &readError, &inTokens, &outTokens, &captureProfile)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	logicalHash := sha256.Sum256(logical)
	if status != "succeeded" || purpose != "interview_focus" || truncated != 0 || readError != 0 ||
		!inTokens.Valid || !outTokens.Valid || captureProfile != profile || inputSHA != hex.EncodeToString(logicalHash[:]) {
		return false, nil
	}
	var recorded struct {
		State jev.InterviewFocusInput `json:"state"`
	}
	if json.Unmarshal(logical, &recorded) != nil || recorded.State.InterviewID != interviewID {
		return false, nil
	}
	decision, err := jev.RecoverCapturedInterviewFocus(recorded.State, logical, raw, requestedModel)
	if err != nil || decision.ProviderResult.ReturnedModel != returnedModel ||
		decision.ProviderResult.Usage.InputTokens != inTokens.Int64 || decision.ProviderResult.Usage.OutputTokens != outTokens.Int64 {
		return false, nil
	}
	result, _ := json.Marshal(map[string]any{"code": "captured_interview_focus", "jevAttemptId": jevID})
	changed, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='observed_success',result_json=?,finished_at=?,updated_at=?
        WHERE id=? AND round_id=? AND state='uncertain'`, string(result), utcNow(), utcNow(), attemptID, roundID)
	if err != nil {
		return false, err
	}
	count, err := changed.RowsAffected()
	if err != nil || count != 1 {
		return false, ErrFenced
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "interview-focus-recovery"}, "interview.focus_capture_recover", jevID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
