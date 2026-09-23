package store

import (
	"context"
	"errors"
	"testing"
)

func TestIngestionRetryRequiresCorrelatedTerminalTurn(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	item, _, err := s.SubmitIngestion(ctx, owner, IngestionInput{
		Origin: "owner", SourceURL: "https://example.test/role", OriginalText: "Source text",
		IdempotencyKey: "uncertain-dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE jobs SET state='failed',completed_at=? WHERE id=?`, utcNow(), item.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE ingestion_requests SET dispatch_started=1,
	  codex_thread_id='thread-1',codex_turn_id='turn-1',status='failed' WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryIngestion(ctx, owner, item.ID, nil); !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertain turn retried: %v", err)
	}
	if err := s.ConfirmIngestionTerminal(ctx, item.ID, item.JobID, "thread-1", "wrong-turn", "failed"); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong turn confirmed: %v", err)
	}
	if err := s.ConfirmIngestionTerminal(ctx, item.ID, item.JobID, "thread-1", "turn-1", "failed"); err != nil {
		t.Fatal(err)
	}
	retried, err := s.RetryIngestion(ctx, owner, item.ID, nil)
	if err != nil || retried.JobID == item.JobID || retried.DispatchStarted {
		t.Fatalf("confirmed retry: %+v %v", retried, err)
	}
	var threadID, turnID, status string
	if err := s.db.QueryRowContext(ctx, `SELECT codex_thread_id,codex_turn_id,terminal_status
	  FROM ingestion_dispatch_history WHERE job_id=?`, item.JobID).Scan(&threadID, &turnID, &status); err != nil ||
		threadID != "thread-1" || turnID != "turn-1" || status != "failed" {
		t.Fatalf("dispatch identity lost: %s %s %s %v", threadID, turnID, status, err)
	}
}
