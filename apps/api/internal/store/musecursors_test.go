package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMuseCursorRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	if _, err := s.LoadMuseCursor(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent cursor err = %v, want ErrNotFound", err)
	}
	want := MuseCursor{RunRef: "run-1", Tier: "contributor", LastSavedReceipt: "rc-9",
		SavedCount: 2, SavedRefs: []string{"vac-1", "vac-2"}, UpdatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := s.SaveMuseCursor(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadMuseCursor(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RunRef != want.RunRef || got.Tier != want.Tier || got.LastSavedReceipt != want.LastSavedReceipt ||
		got.SavedCount != want.SavedCount || len(got.SavedRefs) != 2 || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
	want.SavedCount = 3
	want.SavedRefs = append(want.SavedRefs, "vac-3")
	if err := s.SaveMuseCursor(ctx, want); err != nil {
		t.Fatal(err)
	}
	updated, err := s.LoadMuseCursor(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.SavedCount != 3 || len(updated.SavedRefs) != 3 {
		t.Fatalf("updated = %+v, want count 3", updated)
	}
	if err := s.SaveMuseCursor(ctx, MuseCursor{}); err == nil {
		t.Error("empty cursor accepted")
	}
}
