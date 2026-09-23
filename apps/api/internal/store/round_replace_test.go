package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReplacePausedRoundNamesExactAuthorityAndRetainsUncertainty(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := roundOwner()
	first, _, err := db.StartRound(ctx, owner, roundInput(t, db, "prior-work", RoundAllowance{Requests: 2, Tools: 2, Turns: 1}))
	if err != nil {
		t.Fatal(err)
	}
	first, err = db.ActivateRound(ctx, owner, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := db.ReserveRoundAttempt(ctx, owner, first.ID, RoundAttemptInput{RequestKey: "prior-dispatch", Operation: "source.fetch", ResourceID: "source:example", Cost: RoundAllowance{Requests: 1, Tools: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, first.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.StopRound(ctx, owner, first.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := db.PauseStoppedRound(ctx, owner, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input := StartRoundInput{RequestKey: "new-input-work", Intent: "Handle exact owner input", Outcome: "process_input", ProfileVersion: profile.Version,
		Scope:  RoundScope{InputRefs: []string{"instruction:fixture", "replacement:" + paused.ID}, Resources: []string{"profile:current"}, Operations: []string{RoundCodexTurn, RoundCorrectPreferences}},
		Limits: RoundAllowance{Requests: 2, Items: 1, Tools: 2, Turns: 1}, Deadline: time.Now().Add(time.Hour).UTC().Round(0)}
	if _, _, err := db.ReplacePausedRound(ctx, owner, paused.ID, paused.Revision+1, input); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale paused revision ended work: %v", err)
	}
	still, err := db.Round(ctx, paused.ID)
	if err != nil || still.State != RoundPaused || still.Revision != paused.Revision {
		t.Fatalf("stale replacement changed paused round: %+v %v", still, err)
	}
	next, created, err := db.ReplacePausedRound(ctx, owner, paused.ID, paused.Revision, input)
	if err != nil || !created || next.State != RoundQueued {
		t.Fatalf("replace: %+v created=%v err=%v", next, created, err)
	}
	replayed, created, err := db.ReplacePausedRound(ctx, owner, paused.ID, paused.Revision, input)
	if err != nil || created || replayed.ID != next.ID {
		t.Fatalf("replacement replay: %+v created=%v err=%v", replayed, created, err)
	}
	ended, err := db.Round(ctx, paused.ID)
	if err != nil || ended.State != RoundFailed || ended.StopReason != "replaced_by_owner_input" || ended.Used != paused.Used {
		t.Fatalf("old authority/history lost: %+v %v", ended, err)
	}
	savedAttempt, err := db.RoundAttempt(ctx, attempt.ID)
	if err != nil || savedAttempt.State != AttemptUncertain || savedAttempt.RoundID != ended.ID {
		t.Fatalf("uncertain dispatch lost: %+v %v", savedAttempt, err)
	}
	active, err := db.ActiveRound(ctx)
	if err != nil || active.ID != next.ID {
		t.Fatalf("wrong active successor: %+v %v", active, err)
	}
}
