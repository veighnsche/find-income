package store

import (
	"context"
	"errors"
	"testing"
)

func TestCollectorBoardOwnerConfiguration(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	seeded, err := s.ListCollectorBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range seeded {
		if _, err := s.UpdateCollectorBoard(ctx, owner, item.ID, item.Revision, false, item.IntervalMinutes); err != nil {
			t.Fatal(err)
		}
	}
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
	updated, err := s.UpdateCollectorBoard(ctx, owner, board.ID, 1, false, 30)
	if err != nil || updated.Enabled || updated.IntervalMinutes != 30 || updated.Revision != 2 {
		t.Fatalf("owner update: %+v %v", updated, err)
	}

}

func TestVerifiedDefaultCollectorBoardsAreAvailableForCommissionedScope(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	boards, err := s.ListCollectorBoards(ctx)
	if err != nil || len(boards) != 5 {
		t.Fatalf("seeded boards: %+v %v", boards, err)
	}
	expected := map[string]string{
		"eu:pnlfin": "Finom", "eu:wypoon": "Wypoon Technologies",
		"global:protolabs": "Protolabs", "global:sambatv": "Samba TV", "global:yuno": "Yuno",
	}
	for _, board := range boards {
		name, ok := expected[board.Region+":"+board.Site]
		if !ok || board.DisplayName != name || !board.Enabled || board.Provider != "lever" ||
			board.OfficialCareersURL == "" || board.VerifiedAt == "" || board.IntervalMinutes != 60 {
			t.Fatalf("unverified or disabled default: %+v", board)
		}
	}

}
