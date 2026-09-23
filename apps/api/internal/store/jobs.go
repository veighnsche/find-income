package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrJobIdempotencyConflict = errors.New("job idempotency key reused with different request")

type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

type JobRequest struct {
	Kind           string
	Payload        json.RawMessage
	Actor          Actor
	IdempotencyKey string
	MaxAttempts    int
	AvailableAt    time.Time // zero means immediately; an explicit value is part of the request digest
}

type JobResult struct {
	Ref      string
	Metadata json.RawMessage
}

type JobFailure struct {
	Code           string
	Message        string // safe, user-visible text only; never a raw provider error
	Retryable      bool
	RetryNotBefore time.Time
}

type Job struct {
	ID             string
	Kind           string
	Payload        json.RawMessage
	PayloadSHA256  string
	Actor          Actor
	IdempotencyKey string
	State          JobState
	AttemptCount   int
	MaxAttempts    int
	AvailableAt    time.Time
	LeaseToken     string
	LeaseOwner     string
	LeaseUntil     time.Time
	Result         JobResult
	LastErrorCode  string
	LastError      string
	CancelledBy    Actor
	CancelledAt    time.Time
	CompletedAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type JobAttempt struct {
	JobID          string
	Number         int
	LeaseToken     string
	WorkerID       string
	StartedAt      time.Time
	LeaseUntil     time.Time
	FinishedAt     time.Time
	Outcome        string
	ErrorCode      string
	ErrorMessage   string
	RetryNotBefore time.Time
	Result         JobResult
}

const jobColumns = `id, kind, payload_json, payload_sha256, actor_kind, actor_id,
  idempotency_key, state, attempt_count, max_attempts, available_at, lease_token,
  lease_owner, lease_until, result_ref, result_json, last_error_code,
  last_error_message, cancelled_by_kind, cancelled_by_id, cancelled_at,
  completed_at, created_at, updated_at`

const jobTimeLayout = "2006-01-02T15:04:05.000000000Z"

func jobTime(t time.Time) string { return t.UTC().Format(jobTimeLayout) }

func parseJobTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

func requiredActor(actor Actor) bool {
	return strings.TrimSpace(actor.Kind) != "" && strings.TrimSpace(actor.ID) != ""
}

func compactJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 1024*1024 || !json.Valid(raw) {
		return nil, fmt.Errorf("%w: valid JSON payload up to 1 MiB required", ErrInvalid)
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, fmt.Errorf("%w: compact payload: %v", ErrInvalid, err)
	}
	return b.Bytes(), nil
}

// EnqueueJob binds a key to actor, kind, payload, attempt limit and explicit
// schedule. Repeating an identical request returns its original job without a
// second audit event. Payload bytes are compacted, not semantically reordered.
func (s *Store) EnqueueJob(ctx context.Context, request JobRequest) (Job, bool, error) {
	request.Kind = strings.TrimSpace(request.Kind)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if !requiredActor(request.Actor) || len(request.Kind) == 0 || len(request.Kind) > 80 ||
		len(request.IdempotencyKey) == 0 || len(request.IdempotencyKey) > 200 ||
		request.MaxAttempts < 1 || request.MaxAttempts > 20 {
		return Job{}, false, fmt.Errorf("%w: job kind, actor, key and max attempts required", ErrInvalid)
	}
	payload, err := compactJSON(request.Payload)
	if err != nil {
		return Job{}, false, err
	}
	scheduled := ""
	if !request.AvailableAt.IsZero() {
		scheduled = jobTime(request.AvailableAt)
	}
	fingerprintInput, _ := json.Marshal(struct {
		Kind        string          `json:"kind"`
		Payload     json.RawMessage `json:"payload"`
		MaxAttempts int             `json:"maxAttempts"`
		Scheduled   string          `json:"scheduled"`
	}{request.Kind, payload, request.MaxAttempts, scheduled})
	requestDigest := sha256.Sum256(fingerprintInput)
	payloadDigest := sha256.Sum256(payload)
	id, err := randomID()
	if err != nil {
		return Job{}, false, err
	}
	now := time.Now().UTC()
	available := now
	if scheduled != "" {
		available = request.AvailableAt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO jobs
  (id,kind,payload_json,payload_sha256,actor_kind,actor_id,idempotency_key,request_sha256,
   state,max_attempts,available_at,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(actor_kind,actor_id,idempotency_key) DO NOTHING`,
		id, request.Kind, string(payload), hex.EncodeToString(payloadDigest[:]), request.Actor.Kind,
		request.Actor.ID, request.IdempotencyKey, hex.EncodeToString(requestDigest[:]),
		JobQueued, request.MaxAttempts, jobTime(available), jobTime(now), jobTime(now))
	if err != nil {
		return Job{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Job{}, false, err
	}
	var existingDigest string
	if count == 0 {
		err = tx.QueryRowContext(ctx, `SELECT id, request_sha256 FROM jobs
  WHERE actor_kind=? AND actor_id=? AND idempotency_key=?`, request.Actor.Kind,
			request.Actor.ID, request.IdempotencyKey).Scan(&id, &existingDigest)
		if err != nil {
			return Job{}, false, err
		}
		if existingDigest != hex.EncodeToString(requestDigest[:]) {
			return Job{}, false, ErrJobIdempotencyConflict
		}
	} else if err := writeJobAudit(ctx, tx, request.Actor, "job.enqueue", id, now); err != nil {
		return Job{}, false, err
	}
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=?`, id))
	if err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return job, count == 1, nil
}

func writeJobAudit(ctx context.Context, tx *sql.Tx, actor Actor, operation, id string, at time.Time) error {
	auditID, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id, actor_kind, actor_id, operation, entity_kind, entity_id, occurred_at)
  VALUES (?, ?, ?, ?, 'job', ?, ?)`, auditID, actor.Kind, actor.ID, operation, id, jobTime(at))
	return err
}

func scanJob(row rowScanner) (Job, error) {
	var job Job
	var payload string
	var available, created, updated string
	var leaseToken, leaseOwner, leaseUntil, resultRef, resultJSON sql.NullString
	var errorCode, errorMessage, cancelledKind, cancelledID, cancelledAt, completedAt sql.NullString
	err := row.Scan(&job.ID, &job.Kind, &payload, &job.PayloadSHA256, &job.Actor.Kind,
		&job.Actor.ID, &job.IdempotencyKey, &job.State, &job.AttemptCount, &job.MaxAttempts,
		&available, &leaseToken, &leaseOwner, &leaseUntil, &resultRef, &resultJSON,
		&errorCode, &errorMessage, &cancelledKind, &cancelledID, &cancelledAt,
		&completedAt, &created, &updated)
	if err != nil {
		return Job{}, err
	}
	job.Payload = json.RawMessage(payload)
	job.LeaseToken, job.LeaseOwner = leaseToken.String, leaseOwner.String
	job.Result.Ref = resultRef.String
	if resultJSON.Valid {
		job.Result.Metadata = json.RawMessage(resultJSON.String)
	}
	job.LastErrorCode, job.LastError = errorCode.String, errorMessage.String
	job.CancelledBy = Actor{Kind: cancelledKind.String, ID: cancelledID.String}
	for _, item := range []struct {
		value string
		out   *time.Time
	}{{available, &job.AvailableAt}, {leaseUntil.String, &job.LeaseUntil},
		{cancelledAt.String, &job.CancelledAt}, {completedAt.String, &job.CompletedAt},
		{created, &job.CreatedAt}, {updated, &job.UpdatedAt}} {
		*item.out, err = parseJobTime(item.value)
		if err != nil {
			return Job{}, fmt.Errorf("decode job time: %w", err)
		}
	}
	return job, nil
}

func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return job, err
}

// ClaimNextJob atomically fences one due job. The recovery update also closes
// expired attempts and makes unfinished work available after a process restart.
func (s *Store) ClaimNextJob(ctx context.Context, workerID string, kinds []string, lease time.Duration, now time.Time) (Job, bool, error) {
	if strings.TrimSpace(workerID) == "" || len(kinds) == 0 || lease <= 0 || lease > 24*time.Hour {
		return Job{}, false, fmt.Errorf("%w: worker, kinds and lease required", ErrInvalid)
	}
	for _, kind := range kinds {
		if strings.TrimSpace(kind) == "" {
			return Job{}, false, fmt.Errorf("%w: empty job kind", ErrInvalid)
		}
	}
	nowText := jobTime(now)
	token, err := randomID()
	if err != nil {
		return Job{}, false, err
	}
	leaseUntil := jobTime(now.Add(lease))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE job_attempts SET outcome='expired', finished_at=?,
  error_code='lease_expired', error_message='Worker lease expired'
  WHERE outcome='running' AND job_id IN
    (SELECT id FROM jobs WHERE state='running' AND lease_until<=?)`, nowText, nowText)
	if err != nil {
		return Job{}, false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET
  state=CASE WHEN attempt_count<max_attempts THEN 'queued' ELSE 'failed' END,
  available_at=?, lease_token=NULL, lease_owner=NULL, lease_until=NULL,
  last_error_code='lease_expired', last_error_message='Worker lease expired',
  completed_at=CASE WHEN attempt_count>=max_attempts THEN ? ELSE NULL END,
  updated_at=? WHERE state='running' AND lease_until<=?`, nowText, nowText, nowText, nowText)
	if err != nil {
		return Job{}, false, err
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	query := `UPDATE jobs SET state='running', attempt_count=attempt_count+1,
  lease_token=?, lease_owner=?, lease_until=?, updated_at=?
  WHERE id=(SELECT id FROM jobs WHERE state='queued' AND available_at<=? AND kind IN (` + marks + `)
    ORDER BY available_at, created_at, id LIMIT 1)
  RETURNING ` + jobColumns
	args := []any{token, workerID, leaseUntil, nowText, nowText}
	for _, kind := range kinds {
		args = append(args, kind)
	}
	job, err := scanJob(tx.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO job_attempts
  (job_id,attempt_no,lease_token,worker_id,started_at,lease_until,outcome)
  VALUES (?,?,?,?,?,?,'running')`, job.ID, job.AttemptCount, token, workerID, nowText, leaseUntil)
	if err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

// RenewJobLease returns false when cancellation, expiry or a later claimant has
// fenced this attempt. The attempt history and current lease move together.
func (s *Store) RenewJobLease(ctx context.Context, claim Job, lease time.Duration, now time.Time) (bool, error) {
	if claim.ID == "" || claim.LeaseToken == "" || claim.AttemptCount < 1 || lease <= 0 {
		return false, fmt.Errorf("%w: valid lease claim required", ErrInvalid)
	}
	until := jobTime(now.Add(lease))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET lease_until=?, updated_at=?
  WHERE id=? AND state='running' AND lease_token=? AND attempt_count=? AND lease_until>?`,
		until, jobTime(now), claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(now))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE job_attempts SET lease_until=?
  WHERE job_id=? AND attempt_no=? AND lease_token=? AND outcome='running'`,
		until, claim.ID, claim.AttemptCount, claim.LeaseToken)
	if err != nil {
		return false, err
	}
	count, err = result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count != 1 {
		return false, fmt.Errorf("job attempt missing for lease renewal")
	}
	return true, tx.Commit()
}

func resultValues(result JobResult) (any, any, error) {
	if len(result.Ref) > 1024 || len(result.Metadata) > 64*1024 ||
		(len(result.Metadata) > 0 && !json.Valid(result.Metadata)) {
		return nil, nil, fmt.Errorf("%w: invalid job result metadata", ErrInvalid)
	}
	var ref, metadata any
	if result.Ref != "" {
		ref = result.Ref
	}
	if len(result.Metadata) > 0 {
		metadata = string(result.Metadata)
	}
	return ref, metadata, nil
}

// CompleteJob is the only durable publication point for a prepared result.
// A handler must not make externally visible side effects before this fenced
// commit; later artifact integrations must stage privately and publish only
// when this call succeeds.
func (s *Store) CompleteJob(ctx context.Context, claim Job, result JobResult, now time.Time) (bool, error) {
	ref, metadata, err := resultValues(result)
	if err != nil {
		return false, err
	}
	return s.finishJob(ctx, claim, `UPDATE jobs SET state='succeeded', result_ref=?, result_json=?,
  completed_at=?, lease_token=NULL, lease_owner=NULL, lease_until=NULL,
  last_error_code=NULL, last_error_message=NULL, updated_at=?
  WHERE id=? AND state='running' AND lease_token=? AND attempt_count=? AND lease_until>?`,
		[]any{ref, metadata, jobTime(now), jobTime(now), claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(now)},
		`UPDATE job_attempts SET outcome='succeeded', finished_at=?, result_ref=?, result_json=?
  WHERE job_id=? AND attempt_no=? AND lease_token=? AND outcome='running'`,
		[]any{jobTime(now), ref, metadata, claim.ID, claim.AttemptCount, claim.LeaseToken})
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		return time.Second
	}
	delay := time.Second
	for i := 1; i < attempt && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

const maxJobRetryWait = 30 * 24 * time.Hour

// FailJob retries only explicitly transient failures and only while attempts
// remain. Permanent/poison input reaches a terminal state on its first try.
func (s *Store) FailJob(ctx context.Context, claim Job, failure JobFailure, now time.Time) (bool, error) {
	if len(failure.Code) == 0 || len(failure.Code) > 80 || len(failure.Message) > 1024 ||
		(!failure.Retryable && !failure.RetryNotBefore.IsZero()) ||
		(!failure.RetryNotBefore.IsZero() && failure.RetryNotBefore.After(now.Add(maxJobRetryWait))) {
		return false, fmt.Errorf("%w: bounded failure code/message required", ErrInvalid)
	}
	retry := failure.Retryable && claim.AttemptCount < claim.MaxAttempts
	state, outcome := JobFailed, "failed"
	var available, completed, retryNotBefore any
	if !failure.RetryNotBefore.IsZero() {
		retryNotBefore = jobTime(failure.RetryNotBefore)
	}
	if retry {
		state, outcome = JobQueued, "retry"
		next := now.Add(retryDelay(claim.AttemptCount))
		if failure.RetryNotBefore.After(next) {
			next = failure.RetryNotBefore
		}
		available = jobTime(next)
	} else {
		completed = jobTime(now)
	}
	return s.finishJob(ctx, claim, `UPDATE jobs SET state=?, available_at=COALESCE(?,available_at),
  completed_at=?, last_error_code=?, last_error_message=?,
  lease_token=NULL, lease_owner=NULL, lease_until=NULL, updated_at=?
  WHERE id=? AND state='running' AND lease_token=? AND attempt_count=? AND lease_until>?`,
		[]any{state, available, completed, failure.Code, failure.Message, jobTime(now), claim.ID,
			claim.LeaseToken, claim.AttemptCount, jobTime(now)},
		`UPDATE job_attempts SET outcome=?, finished_at=?, error_code=?, error_message=?, retry_not_before=?
  WHERE job_id=? AND attempt_no=? AND lease_token=? AND outcome='running'`,
		[]any{outcome, jobTime(now), failure.Code, failure.Message, retryNotBefore, claim.ID, claim.AttemptCount, claim.LeaseToken})
}

func (s *Store) finishJob(ctx context.Context, claim Job, updateJob string, jobArgs []any, updateAttempt string, attemptArgs []any) (bool, error) {
	if claim.ID == "" || claim.LeaseToken == "" || claim.AttemptCount < 1 {
		return false, fmt.Errorf("%w: valid lease claim required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, updateJob, jobArgs...)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	result, err = tx.ExecContext(ctx, updateAttempt, attemptArgs...)
	if err != nil {
		return false, err
	}
	count, err = result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count != 1 {
		return false, fmt.Errorf("job attempt missing for fenced completion")
	}
	return true, tx.Commit()
}

// CancelJob fences both queued and running work. A running handler should stop
// on its next renewal; any late completion is rejected regardless.
func (s *Store) CancelJob(ctx context.Context, id string, actor Actor, now time.Time) (bool, error) {
	if id == "" || !requiredActor(actor) {
		return false, fmt.Errorf("%w: job and actor required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled', lease_token=NULL,
  lease_owner=NULL, lease_until=NULL, cancelled_by_kind=?, cancelled_by_id=?,
  cancelled_at=?, completed_at=?, updated_at=? WHERE id=? AND state IN ('queued','running')`,
		actor.Kind, actor.ID, jobTime(now), jobTime(now), jobTime(now), id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		var exists int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM jobs WHERE id=?", id).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE job_attempts SET outcome='cancelled', finished_at=?,
  error_code='cancelled', error_message='Job cancelled'
  WHERE job_id=? AND outcome='running'`, jobTime(now), id)
	if err != nil {
		return false, err
	}
	if err := writeJobAudit(ctx, tx, actor, "job.cancel", id, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) JobAttempts(ctx context.Context, id string) ([]JobAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT job_id,attempt_no,lease_token,worker_id,
  started_at,lease_until,finished_at,outcome,error_code,error_message,retry_not_before,result_ref,result_json
  FROM job_attempts WHERE job_id=? ORDER BY attempt_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []JobAttempt
	for rows.Next() {
		var attempt JobAttempt
		var started, until string
		var finished, code, message, retryNotBefore, ref, metadata sql.NullString
		if err := rows.Scan(&attempt.JobID, &attempt.Number, &attempt.LeaseToken,
			&attempt.WorkerID, &started, &until, &finished, &attempt.Outcome,
			&code, &message, &retryNotBefore, &ref, &metadata); err != nil {
			return nil, err
		}
		for _, item := range []struct {
			value string
			out   *time.Time
		}{{started, &attempt.StartedAt}, {until, &attempt.LeaseUntil},
			{finished.String, &attempt.FinishedAt}, {retryNotBefore.String, &attempt.RetryNotBefore}} {
			*item.out, err = parseJobTime(item.value)
			if err != nil {
				return nil, err
			}
		}
		attempt.ErrorCode, attempt.ErrorMessage = code.String, message.String
		attempt.Result.Ref = ref.String
		if metadata.Valid {
			attempt.Result.Metadata = json.RawMessage(metadata.String)
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}
