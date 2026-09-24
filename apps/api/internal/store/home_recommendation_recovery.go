package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// ResolveCapturedHomeRecommendationAttempt settles only an exact, already
// validated next-outcome Jev response. The caller validates the saved logical
// request, current context, candidate and raw response before this write.
func (s *Store) ResolveCapturedHomeRecommendationAttempt(ctx context.Context, roundID, attemptID, jevID string, generation int64, logicalSHA, choice string) error {
	if roundID == "" || attemptID == "" || jevID == "" || generation < 1 || len(logicalSHA) != 64 || choice == "" {
		return ErrInvalid
	}
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if round.State != RoundPaused || round.Generation != generation || !homeRecommendationOutcome(round.Outcome) {
		return ErrFenced
	}
	var operation, resource, requestKey, state string
	var attemptGeneration int64
	err = tx.QueryRowContext(ctx, `SELECT operation,resource_id,request_key,generation,state FROM round_attempts WHERE id=? AND round_id=?`, attemptID, roundID).
		Scan(&operation, &resource, &requestKey, &attemptGeneration, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if operation != RoundJevRequest || resource != "campaign:active" || requestKey != "home-recommendation/0" ||
		state != string(AttemptUncertain) || attemptGeneration < 1 || attemptGeneration > generation {
		return ErrFenced
	}
	var status, purpose, rubric, inputSHA string
	var logical []byte
	var profileVersion int64
	var step, truncated, readError int
	err = tx.QueryRowContext(ctx, `SELECT status,purpose,rubric_version,input_sha256,logical_request_json,profile_version,step_index,
	 response_truncated,response_read_error FROM jev_attempts WHERE id=? AND round_id=? AND round_attempt_id=?`, jevID, roundID, attemptID).
		Scan(&status, &purpose, &rubric, &inputSHA, &logical, &profileVersion, &step, &truncated, &readError)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	digest := sha256.Sum256(logical)
	if status != "succeeded" || purpose != "next_outcome" || rubric != "decision-v1" || step != 0 ||
		truncated != 0 || readError != 0 || profileVersion != round.ProfileVersion ||
		inputSHA != logicalSHA || hex.EncodeToString(digest[:]) != logicalSHA {
		return ErrFenced
	}
	var currentProfile int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); err != nil {
		return err
	}
	if currentProfile != profileVersion {
		return ErrConflict
	}
	result, _ := json.Marshal(map[string]any{"jevAttemptId": jevID, "localRecovered": true, "choice": choice})
	changed, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='observed_success',result_json=?,finished_at=?,updated_at=?
	 WHERE id=? AND round_id=? AND state='uncertain'`, string(result), utcNow(), utcNow(), attemptID, roundID)
	if err != nil {
		return err
	}
	count, err := changed.RowsAffected()
	if err != nil || count != 1 {
		return ErrFenced
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "home-recommendation-recovery"}, "round.reconcile.local", roundID); err != nil {
		return err
	}
	return tx.Commit()
}

func homeRecommendationOutcome(outcome string) bool {
	switch outcome {
	case "process_input", "prepare", "compare_offers", "deliver", "interview_prepare", "interview_debrief", "process_replies":
		return true
	default:
		return false
	}
}
