package store

import (
	"context"
	"testing"
	"time"
)

func TestExpireRoundFencesDispatchedWorkAndReleasesSlot(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "worker"}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "expiring", Intent: "Bounded read", Outcome: "process_input",
		ProfileVersion: p.Version, Deadline: time.Now().Add(150 * time.Millisecond),
		Scope:  RoundScope{Resources: []string{"campaign:active"}, Operations: []string{RoundCodexTurn}, Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := s.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{RequestKey: "turn", Operation: RoundCodexTurn,
		ResourceID: "campaign:active", Cost: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(r.Deadline) + time.Millisecond)
	expired, err := s.ExpireRound(ctx, r.ID)
	if err != nil || expired.State != RoundFailed || expired.StopReason != "deadline_reached" || !expired.ReconciliationRequired || expired.Generation != r.Generation+1 {
		t.Fatalf("deadline fence: %+v %v", expired, err)
	}
	read, err := s.RoundAttempt(ctx, attempt.ID)
	if err != nil || read.State != AttemptUncertain {
		t.Fatalf("dispatch identity lost: %+v %v", read, err)
	}
	if _, err := s.ExpireRound(ctx, r.ID); err != nil {
		t.Fatalf("deadline replay: %v", err)
	}
	if _, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "next", Intent: "New commission", Outcome: "process_input",
		ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope:  RoundScope{Resources: []string{"campaign:active"}, Operations: []string{RoundCodexTurn}, Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Tools: 1, Turns: 1}}); err != nil {
		t.Fatalf("expired round held active slot: %v", err)
	}
}
