package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestResumeFailedRound(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	profile, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "resume-failed",
		Intent: "Revive a failed round", Outcome: "research_run", ProfileVersion: profile.Version,
		Deadline: time.Now().Add(time.Hour),
		Scope:    RoundScope{Operations: []string{RoundCodexTurn}},
		Limits:   RoundAllowance{Requests: 2, Items: 2, Tools: 2, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := s.FinishRound(ctx, owner, round.ID, RoundFailed, "contributor_error", "research_run", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != RoundFailed || failed.CompletedAt == "" {
		t.Fatalf("finished = %+v, want failed with completion", failed)
	}
	revived, err := s.ResumeFailedRound(ctx, owner, round.ID, failed.Generation)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if revived.State != RoundRunning || revived.CompletedAt != "" || revived.Generation != failed.Generation+1 {
		t.Fatalf("revived = %+v, want running with cleared completion", revived)
	}
	if _, err := s.ResumeFailedRound(ctx, owner, round.ID, revived.Generation); !errors.Is(err, ErrFenced) {
		t.Fatalf("second revive: %v, want fenced (no longer failed)", err)
	}
	done, err := s.FinishRound(ctx, owner, round.ID, RoundCompleted, "done", "research_run", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeFailedRound(ctx, owner, round.ID, done.Generation); !errors.Is(err, ErrFenced) {
		t.Fatalf("completed revive: %v, want fenced", err)
	}
}
