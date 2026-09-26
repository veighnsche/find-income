package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMuseRunReportRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	if _, err := s.LoadMuseRunReport(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent report err = %v, want ErrNotFound", err)
	}
	want := MuseRunReport{RunRef: "run-1", RoundID: "round-1", Tier: "contributor",
		Outcome: "completed", SavedRefs: []string{"vac-1"},
		ClassifyErrors: []string{"vac-9: no employer name"}, UpdatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := s.SaveMuseRunReport(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadMuseRunReport(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RunRef != want.RunRef || got.RoundID != want.RoundID || got.Outcome != want.Outcome ||
		len(got.SavedRefs) != 1 || len(got.ClassifyErrors) != 1 || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("report = %+v, want %+v", got, want)
	}
	if err := s.SaveMuseRunReport(ctx, MuseRunReport{RunRef: "run-1"}); err == nil {
		t.Error("outcome-less report accepted")
	}
}

func TestMuseRunResumeRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	if _, err := s.LoadMuseRunResume(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent resume err = %v, want ErrNotFound", err)
	}
	want := MuseRunResume{RunRef: "run-1", RoundID: "round-1",
		CriteriaJSON: `{"roleKeywords":["support"]}`, BoundsJSON: `{"MaxModelSteps":7}`}
	if err := s.SaveMuseRunResume(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadMuseRunResume(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resume = %+v, want %+v", got, want)
	}
	// Admission writes once: a second write keeps the first payload.
	second := MuseRunResume{RunRef: "run-1", RoundID: "round-2",
		CriteriaJSON: `{}`, BoundsJSON: `{}`}
	if err := s.SaveMuseRunResume(ctx, second); err != nil {
		t.Fatal(err)
	}
	kept, err := s.LoadMuseRunResume(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if kept != want {
		t.Fatalf("resume after re-save = %+v, want %+v", kept, want)
	}
	if err := s.SaveMuseRunResume(ctx, MuseRunResume{}); err == nil {
		t.Error("ref-less resume accepted")
	}
}
