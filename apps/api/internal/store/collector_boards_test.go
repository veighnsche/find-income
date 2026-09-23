package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCollectorBoardLeaseCursorAndOwnerConfiguration(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	if _, err := s.CreateCollectorBoard(ctx, owner, CollectorBoardInput{
		Provider: "lever", Site: "bad/site", Region: "global", Enabled: true, IntervalMinutes: 15}); err == nil {
		t.Fatal("accepted invalid site token")
	}
	board, err := s.CreateCollectorBoard(ctx, owner, CollectorBoardInput{
		Provider: "lever", Site: "example", Region: "eu", Enabled: true, IntervalMinutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCollectorBoard(ctx, owner, CollectorBoardInput{
		Provider: "lever", Site: "example", Region: "eu", Enabled: true, IntervalMinutes: 15}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate board did not conflict: %v", err)
	}
	now := time.Now().UTC().Add(time.Second)
	first, found, err := s.ClaimDueCollectorBoard(ctx, now, time.Minute)
	if err != nil || !found || first.ID != board.ID || first.LeaseToken == "" {
		t.Fatalf("claim: %+v %v %v", first, found, err)
	}
	if _, found, err := s.ClaimDueCollectorBoard(ctx, now, time.Minute); err != nil || found {
		t.Fatalf("overlapping scan claimed board: %v %v", found, err)
	}
	later := now.Add(2 * time.Minute)
	second, found, err := s.ClaimDueCollectorBoard(ctx, later, time.Minute)
	if err != nil || !found || second.LeaseToken == first.LeaseToken {
		t.Fatalf("expired claim not recovered: %+v %v %v", second, found, err)
	}
	if applied, err := s.FinishCollectorBoard(ctx, first, CollectorBoardResult{NextOffset: 25}, later); err != nil || applied {
		t.Fatalf("stale lease advanced cursor: %v %v", applied, err)
	}
	if applied, err := s.FinishCollectorBoard(ctx, second, CollectorBoardResult{NextOffset: 25}, later); err != nil || !applied {
		t.Fatalf("current lease finish: %v %v", applied, err)
	}
	boards, err := s.ListCollectorBoards(ctx)
	if err != nil || len(boards) != 1 || boards[0].NextOffset != 25 || boards[0].LastSuccessAt.IsZero() ||
		boards[0].NextScanAt.Sub(later) != time.Minute {
		t.Fatalf("completed scan: %+v %v", boards, err)
	}
	updated, err := s.UpdateCollectorBoard(ctx, owner, board.ID, 1, false, 30)
	if err != nil || updated.Enabled || updated.IntervalMinutes != 30 || updated.Revision != 2 {
		t.Fatalf("owner update: %+v %v", updated, err)
	}
	if _, found, err := s.ClaimDueCollectorBoard(ctx, later.Add(time.Hour), time.Minute); err != nil || found {
		t.Fatalf("disabled board scanned: %v %v", found, err)
	}
}
