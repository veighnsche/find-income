package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestLatestCompletedRoundFindsOwnerInputInCompletedOrder(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.LatestCompletedRound(ctx, roundOwner(), "process_input"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty owner history: %v", err)
	}
	finish := func(key string) Round {
		round := startedRound(t, db, key, RoundAllowance{Requests: 1})
		completed, err := db.FinishRound(ctx, roundOwner(), round.ID, RoundCompleted, "done", "complete", json.RawMessage(`{"code":"done"}`))
		if err != nil {
			t.Fatal(err)
		}
		return completed
	}
	first := finish("first-complete")
	second := finish("second-complete")
	if _, err := db.db.ExecContext(ctx, `UPDATE rounds SET completed_at='2026-01-01T00:00:00Z' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE rounds SET completed_at='2026-01-02T00:00:00Z' WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	latest, err := db.LatestCompletedRound(ctx, roundOwner(), "process_input")
	if err != nil || latest.ID != second.ID || latest.Report == nil {
		t.Fatalf("wrong completed round: %+v %v", latest, err)
	}
	if _, err := db.LatestCompletedRound(ctx, Actor{Kind: "administrator", ID: "another-owner"}, "process_input"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner read: %v", err)
	}
	if latest, err := db.LatestCompletedRound(ctx, roundOwner(), "all"); err != nil || latest.ID != second.ID {
		t.Fatalf("all-outcome owner read: %+v %v", latest, err)
	}
	if _, err := db.LatestCompletedRound(ctx, roundOwner(), "unsupported"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported outcome filter: %v", err)
	}
}

func TestLatestCompletedRoundIncludesRecordedFailedDeliveryOnly(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := func(key string) Round {
		input := roundInput(t, db, key, RoundAllowance{Requests: 1})
		input.Outcome = "deliver"
		input.Scope.InputRefs = []string{"delivery_review:fixture"}
		input.Scope.Resources = []string{"delivery:fixture"}
		input.Scope.Operations = []string{RoundDeliverApplication}
		round, _, err := db.StartRound(ctx, roundOwner(), input)
		if err != nil {
			t.Fatal(err)
		}
		round, err = db.ActivateRound(ctx, roundOwner(), round.ID)
		if err != nil {
			t.Fatal(err)
		}
		return round
	}
	recorded := start("delivery-recorded")
	recorded, err = db.FinishRound(ctx, roundOwner(), recorded.ID, RoundFailed, "smtp_submission_failed", "submission_unverified", json.RawMessage(`{"employerReceiptVerified":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.LatestCompletedRound(ctx, roundOwner(), "deliver"); err != nil || got.ID != recorded.ID {
		t.Fatalf("recorded failed delivery not recoverable: %+v %v", got, err)
	}
	if got, err := db.LatestCompletedRound(ctx, roundOwner(), "all"); err != nil || got.ID != recorded.ID {
		t.Fatalf("recorded delivery missing from all outcomes: %+v %v", got, err)
	}
	unrecorded := start("delivery-unrecorded")
	if _, err := db.FinishRound(ctx, roundOwner(), unrecorded.ID, RoundFailed, "deadline_reached", "partial", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := db.LatestCompletedRound(ctx, roundOwner(), "deliver"); err != nil || got.ID != recorded.ID {
		t.Fatalf("unrecorded failure replaced saved result: %+v %v", got, err)
	}
}
