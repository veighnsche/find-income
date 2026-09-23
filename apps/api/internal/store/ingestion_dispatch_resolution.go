package store

import (
	"context"
	"database/sql"
	"errors"
)

// ConfirmIngestionTerminal records a caller-observed terminal Codex turn for
// the exact current dispatch. The caller must establish terminal history from
// the runtime; missing identifiers never count as evidence of completion.
func (s *Store) ConfirmIngestionTerminal(ctx context.Context, ingestionID, jobID, threadID, turnID, status string) error {
	if ingestionID == "" || jobID == "" || threadID == "" || turnID == "" ||
		(status != "completed" && status != "failed" && status != "interrupted") {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentJob, currentThread, currentTurn, currentStatus, jobState string
	var started int
	err = tx.QueryRowContext(ctx, `SELECT i.job_id, i.dispatch_started,
	  COALESCE(i.codex_thread_id,''), COALESCE(i.codex_turn_id,''),
	  COALESCE(i.dispatch_terminal_status,''), j.state
	  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, ingestionID).
		Scan(&currentJob, &started, &currentThread, &currentTurn, &currentStatus, &jobState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if currentJob != jobID || started != 1 || currentThread != threadID || currentTurn != turnID ||
		(jobState != string(JobSucceeded) && jobState != string(JobFailed) && jobState != string(JobCancelled)) {
		return ErrFenced
	}
	if currentStatus != "" && currentStatus != status {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET dispatch_terminal_status=?,updated_at=?
	  WHERE id=? AND job_id=? AND dispatch_started=1 AND codex_thread_id=? AND codex_turn_id=?
	  AND (dispatch_terminal_status IS NULL OR dispatch_terminal_status=?)`,
		status, utcNow(), ingestionID, jobID, threadID, turnID, status)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ingestion_dispatch_history
	  (ingestion_id,job_id,codex_thread_id,codex_turn_id,terminal_status,confirmed_at)
	  VALUES (?,?,?,?,?,?) ON CONFLICT(job_id) DO NOTHING`, ingestionID, jobID, threadID, turnID, status, utcNow())
	if err != nil {
		return err
	}
	return tx.Commit()
}
