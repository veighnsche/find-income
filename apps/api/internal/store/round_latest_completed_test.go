package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestLatestCompletedRoundFindsOneOwnerDiscoveryInCompletedOrder(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.LatestCompletedRound(ctx, roundOwner(), "discover"); !errors.Is(err, ErrNotFound) {
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
	latest, err := db.LatestCompletedRound(ctx, roundOwner(), "discover")
	if err != nil || latest.ID != second.ID || latest.Report == nil {
		t.Fatalf("wrong completed round: %+v %v", latest, err)
	}
	if _, err := db.LatestCompletedRound(ctx, Actor{Kind: "administrator", ID: "another-owner"}, "discover"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner read: %v", err)
	}
	if _, err := db.LatestCompletedRound(ctx, roundOwner(), "prepare"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported outcome filter: %v", err)
	}
}
