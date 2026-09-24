package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

type ReplyProcessingCommissionInput struct {
	RequestKey string `json:"requestKey"`
	ThreadID   string `json:"threadId"`
}

type ReplyProcessing struct {
	ID                 string `json:"id"`
	ThreadID           string `json:"threadId"`
	ProfileVersion     int64  `json:"profileVersion"`
	RoundID            string `json:"roundId,omitempty"`
	Intent             string `json:"intent,omitempty"`
	IntentJevAttemptID string `json:"intentJevAttemptId,omitempty"`
	OpportunityID      string `json:"opportunityId,omitempty"`
	ProcessedAt        string `json:"processedAt,omitempty"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

type ReplyDraft struct {
	ID           string          `json:"id"`
	ProcessingID string          `json:"processingId"`
	ThreadID     string          `json:"threadId"`
	RoundID      string          `json:"roundId"`
	Revision     int64           `json:"revision"`
	Draft        json.RawMessage `json:"draft"`
	DraftSHA256  string          `json:"draftSha256"`
	CreatedAt    string          `json:"createdAt"`
	UpdatedAt    string          `json:"updatedAt"`
}

type ReplyUpdateMutation struct {
	ProcessingID  string `json:"processingId"`
	ThreadID      string `json:"threadId"`
	OpportunityID string `json:"opportunityId"`
}

type ReplyDraftMutation struct {
	ProcessingID string          `json:"processingId"`
	ThreadID     string          `json:"threadId"`
	DraftJSON    json.RawMessage `json:"draft"`
}

func (s *Store) CommissionReplyProcessing(ctx context.Context, actor Actor, input ReplyProcessingCommissionInput) (ReplyProcessing, bool, error) {
	if actor.Kind != "administrator" || actor.ID == "" || !ownerRequestKey(input.RequestKey) || input.ThreadID == "" || len(input.ThreadID) > 128 {
		return ReplyProcessing{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReplyProcessing{}, false, err
	}
	defer tx.Rollback()
	digest := interviewDigestBytes([]byte(input.ThreadID))
	var priorID, priorDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM reply_processings WHERE actor_id=? AND request_key=?`, actor.ID, input.RequestKey).Scan(&priorID, &priorDigest)
	if err == nil {
		if digest != priorDigest {
			return ReplyProcessing{}, false, ErrRoundIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return ReplyProcessing{}, false, err
		}
		v, err := s.ReplyProcessing(ctx, priorID)
		if err != nil {
			return ReplyProcessing{}, false, err
		}
		return v, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ReplyProcessing{}, false, err
	}
	var threadOwner string
	if err = tx.QueryRowContext(ctx, `SELECT actor_id FROM correspondence_threads WHERE id=?`, input.ThreadID).Scan(&threadOwner); errors.Is(err, sql.ErrNoRows) {
		return ReplyProcessing{}, false, ErrNotFound
	} else if err != nil {
		return ReplyProcessing{}, false, err
	}
	if threadOwner != actor.ID {
		return ReplyProcessing{}, false, ErrFenced
	}
	var profile int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&profile); err != nil {
		return ReplyProcessing{}, false, err
	}
	id, err := randomID()
	if err != nil {
		return ReplyProcessing{}, false, err
	}
	now := utcNow()
	if _, err = tx.ExecContext(ctx, `INSERT INTO reply_processings(id,actor_id,thread_id,request_key,request_sha256,profile_version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, actor.ID, input.ThreadID, input.RequestKey, digest, profile, now, now); err != nil {
		return ReplyProcessing{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return ReplyProcessing{}, false, err
	}
	v, err := s.ReplyProcessing(ctx, id)
	if err != nil {
		return ReplyProcessing{}, false, err
	}
	return v, true, nil
}

func (s *Store) BindReplyRound(ctx context.Context, actor Actor, processingID, roundID string) error {
	if actor.Kind != "administrator" || actor.ID == "" || processingID == "" || roundID == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, roundID))
	if err != nil {
		return err
	}
	if r.Actor != actor || r.Outcome != "process_replies" || !scopeHas(r.Scope.InputRefs, "replies:"+processingID) {
		return ErrFenced
	}
	var prior sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT round_id FROM reply_processings WHERE id=? AND actor_id=?`, processingID, actor.ID).Scan(&prior)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if prior.Valid && prior.String != roundID {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE reply_processings SET round_id=?,updated_at=? WHERE id=? AND actor_id=?`, roundID, utcNow(), processingID, actor.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReplyProcessing(ctx context.Context, id string) (ReplyProcessing, error) {
	var v ReplyProcessing
	var roundID, intent, jevID, opportunityID, processedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,thread_id,profile_version,round_id,intent,intent_jev_attempt_id,opportunity_id,processed_at,created_at,updated_at FROM reply_processings WHERE id=?`, id).
		Scan(&v.ID, &v.ThreadID, &v.ProfileVersion, &roundID, &intent, &jevID, &opportunityID, &processedAt, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.RoundID, v.Intent, v.IntentJevAttemptID, v.OpportunityID, v.ProcessedAt = roundID.String, intent.String, jevID.String, opportunityID.String, processedAt.String
	return v, nil
}

func (s *Store) OwnerReplyProcessing(ctx context.Context, ownerID, id string) (ReplyProcessing, error) {
	v, err := s.ReplyProcessing(ctx, id)
	if err != nil {
		return v, err
	}
	thread, err := s.OwnerCorrespondenceThread(ctx, ownerID, v.ThreadID)
	if err != nil {
		return ReplyProcessing{}, ErrNotFound
	}
	if thread.ID != v.ThreadID {
		return ReplyProcessing{}, ErrNotFound
	}
	return v, nil
}

func (s *Store) ReplyDraftForProcessing(ctx context.Context, processingID string) (ReplyDraft, error) {
	var v ReplyDraft
	var draft string
	err := s.db.QueryRowContext(ctx, `SELECT id,processing_id,thread_id,round_id,revision,draft_json,draft_sha256,created_at,updated_at FROM reply_drafts WHERE processing_id=?`, processingID).
		Scan(&v.ID, &v.ProcessingID, &v.ThreadID, &v.RoundID, &v.Revision, &draft, &v.DraftSHA256, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.Draft = json.RawMessage(draft)
	return v, nil
}

// ApplyReplyIntent records one validated Jev intent for its processing. The
// Jev attempt must be the charged succeeded request for this round's thread.
func (s *Store) ApplyReplyIntent(ctx context.Context, actor Actor, roundID string, expectedGeneration int64, processingID, jevAttemptID, intent string) (ReplyProcessing, error) {
	if actor.Kind != "agent" || actor.ID == "" || expectedGeneration < 1 || processingID == "" || jevAttemptID == "" || intent == "" || len(intent) > 80 {
		return ReplyProcessing{}, ErrInvalid
	}
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return ReplyProcessing{}, err
	}
	defer tx.Rollback()
	if round.Generation != expectedGeneration || round.Outcome != "process_replies" {
		return ReplyProcessing{}, ErrFenced
	}
	var threadID, priorIntent string
	var profile int64
	err = tx.QueryRowContext(ctx, `SELECT thread_id,intent,profile_version FROM reply_processings WHERE id=? AND round_id=?`, processingID, roundID).Scan(&threadID, &priorIntent, &profile)
	if errors.Is(err, sql.ErrNoRows) {
		return ReplyProcessing{}, ErrNotFound
	}
	if err != nil {
		return ReplyProcessing{}, err
	}
	if priorIntent != "" || profile != round.ProfileVersion || !scopeHas(round.Scope.InputRefs, "replies:"+processingID) || !scopeHas(round.Scope.Resources, "thread:"+threadID) {
		return ReplyProcessing{}, ErrFenced
	}
	var state, attemptRound, operation, attemptResource string
	var attemptState RoundAttemptState
	err = tx.QueryRowContext(ctx, `SELECT j.status,j.round_id,a.operation,a.resource_id,a.state FROM jev_attempts j JOIN round_attempts a ON a.id=j.round_attempt_id WHERE j.id=? AND j.purpose='reply_intent'`, jevAttemptID).
		Scan(&state, &attemptRound, &operation, &attemptResource, &attemptState)
	if errors.Is(err, sql.ErrNoRows) {
		return ReplyProcessing{}, ErrNotFound
	}
	if err != nil {
		return ReplyProcessing{}, err
	}
	if state != "succeeded" || attemptRound != roundID || operation != RoundJevRequest || attemptResource != "thread:"+threadID ||
		(attemptState != AttemptSucceeded && attemptState != AttemptObservedSuccess) {
		return ReplyProcessing{}, ErrFenced
	}
	var currentProfile int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); err != nil {
		return ReplyProcessing{}, err
	}
	if currentProfile != profile {
		return ReplyProcessing{}, ErrConflict
	}
	now := utcNow()
	if _, err = tx.ExecContext(ctx, `UPDATE reply_processings SET intent=?,intent_jev_attempt_id=?,processed_at=?,updated_at=? WHERE id=? AND intent=''`, intent, jevAttemptID, now, now, processingID); err != nil {
		return ReplyProcessing{}, err
	}
	if err = interviewAuditTx(ctx, tx, actor, "reply.intent_apply", "reply_processing", processingID, 1); err != nil {
		return ReplyProcessing{}, err
	}
	if err = tx.Commit(); err != nil {
		return ReplyProcessing{}, err
	}
	return s.ReplyProcessing(ctx, processingID)
}

func writeReplyUpdateTx(ctx context.Context, tx *sql.Tx, round Round, expectedRevision int64, update ReplyUpdateMutation) (string, string, int64, error) {
	if update.ProcessingID == "" || update.ThreadID == "" || update.OpportunityID == "" {
		return "", "", 0, ErrInvalid
	}
	var thread, priorOpportunity string
	var profile int64
	err := tx.QueryRowContext(ctx, `SELECT thread_id,COALESCE(opportunity_id,''),profile_version FROM reply_processings WHERE id=? AND round_id=?`, update.ProcessingID, round.ID).Scan(&thread, &priorOpportunity, &profile)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, ErrFenced
	}
	if err != nil {
		return "", "", 0, err
	}
	if thread != update.ThreadID || profile != round.ProfileVersion {
		return "", "", 0, ErrFenced
	}
	if priorOpportunity != "" && priorOpportunity != update.OpportunityID {
		return "", "", 0, ErrConflict
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, update.OpportunityID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, ErrNotFound
	}
	if err != nil {
		return "", "", 0, err
	}
	if revision != expectedRevision {
		return "", "", 0, ErrConflict
	}
	now := utcNow()
	if _, err = tx.ExecContext(ctx, `UPDATE reply_processings SET opportunity_id=?,updated_at=? WHERE id=?`, update.OpportunityID, now, update.ProcessingID); err != nil {
		return "", "", 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE correspondence_threads SET opportunity_id=?,updated_at=? WHERE id=?`, update.OpportunityID, now, update.ThreadID); err != nil {
		return "", "", 0, err
	}
	return update.ProcessingID, "reply_update", 1, nil
}

func writeReplyDraftTx(ctx context.Context, tx *sql.Tx, round Round, draft ReplyDraftMutation) (string, string, int64, error) {
	var snapshot struct {
		Input       json.RawMessage `json:"input"`
		InputSHA256 string          `json:"inputSha256"`
	}
	if len(draft.DraftJSON) == 0 || len(draft.DraftJSON) > 60000 || json.Unmarshal(draft.DraftJSON, &snapshot) != nil || interviewDigestBytes(snapshot.Input) != snapshot.InputSHA256 {
		return "", "", 0, ErrInvalid
	}
	var input struct {
		ProcessingID string `json:"processingId"`
		ThreadID     string `json:"threadId"`
		Body         string `json:"body"`
	}
	if json.Unmarshal(snapshot.Input, &input) != nil || input.ProcessingID != draft.ProcessingID || input.ThreadID != draft.ThreadID || len(input.Body) == 0 || len(input.Body) > 8000 {
		return "", "", 0, ErrInvalid
	}
	var thread, priorIntent string
	var profile int64
	err := tx.QueryRowContext(ctx, `SELECT thread_id,intent,profile_version FROM reply_processings WHERE id=? AND round_id=?`, draft.ProcessingID, round.ID).Scan(&thread, &priorIntent, &profile)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, ErrFenced
	}
	if err != nil {
		return "", "", 0, err
	}
	if thread != draft.ThreadID || profile != round.ProfileVersion {
		return "", "", 0, ErrFenced
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM reply_drafts WHERE processing_id=?`, draft.ProcessingID).Scan(&existing)
	if err == nil {
		return "", "", 0, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, err
	}
	id, err := randomID()
	if err != nil {
		return "", "", 0, err
	}
	now := utcNow()
	sha := interviewDigestBytes(draft.DraftJSON)
	var actorID string
	if err = tx.QueryRowContext(ctx, `SELECT actor_id FROM reply_processings WHERE id=?`, draft.ProcessingID).Scan(&actorID); err != nil {
		return "", "", 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO reply_drafts(id,processing_id,actor_id,thread_id,round_id,revision,draft_json,draft_sha256,created_at,updated_at) VALUES(?,?,?,?,?,1,?,?,?,?)`, id, draft.ProcessingID, actorID, draft.ThreadID, round.ID, string(draft.DraftJSON), sha, now, now); err != nil {
		return "", "", 0, err
	}
	return id, "reply_draft", 1, nil
}

// RecoverCapturedReplyIntentAttempt settles only the charged Jev request. A
// saved intent or draft never supplies terminal evidence for its Codex turn.
func (s *Store) RecoverCapturedReplyIntentAttempt(ctx context.Context, roundID, attemptID, jevID string, generation int64) (bool, error) {
	if roundID == "" || attemptID == "" || jevID == "" || generation < 1 {
		return false, ErrInvalid
	}
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if round.State != RoundPaused || round.Generation != generation || round.Outcome != "process_replies" {
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
	var processingID, threadID string
	var profile int64
	err = tx.QueryRowContext(ctx, `SELECT id,thread_id,profile_version FROM reply_processings WHERE round_id=?`, roundID).Scan(&processingID, &threadID, &profile)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if resource != "thread:"+threadID || requestKey != "reply-intent:"+processingID+"/0" || profile != round.ProfileVersion ||
		!scopeHas(round.Scope.InputRefs, "replies:"+processingID) || !scopeHas(round.Scope.Resources, resource) {
		return false, ErrFenced
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
	if status != "succeeded" || purpose != "reply_intent" || truncated != 0 || readError != 0 ||
		!inTokens.Valid || !outTokens.Valid || captureProfile != profile || inputSHA != hex.EncodeToString(logicalHash[:]) {
		return false, nil
	}
	var recorded struct {
		State jev.ReplyIntentInput `json:"state"`
	}
	if json.Unmarshal(logical, &recorded) != nil || recorded.State.ThreadID != threadID {
		return false, nil
	}
	decision, err := jev.RecoverCapturedReplyIntent(recorded.State, logical, raw, requestedModel)
	if err != nil || decision.ProviderResult.ReturnedModel != returnedModel ||
		decision.ProviderResult.Usage.InputTokens != inTokens.Int64 || decision.ProviderResult.Usage.OutputTokens != outTokens.Int64 {
		return false, nil
	}
	result, _ := json.Marshal(map[string]any{"code": "captured_reply_intent", "jevAttemptId": jevID})
	changed, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='observed_success',result_json=?,finished_at=?,updated_at=?
        WHERE id=? AND round_id=? AND state='uncertain'`, string(result), utcNow(), utcNow(), attemptID, roundID)
	if err != nil {
		return false, err
	}
	count, err := changed.RowsAffected()
	if err != nil || count != 1 {
		return false, ErrFenced
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "reply-intent-recovery"}, "reply.intent_capture_recover", jevID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
