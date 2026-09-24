package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func roundOwner() Actor { return Actor{Kind: "administrator", ID: "round-test-owner"} }

func roundInput(t *testing.T, s *Store, key string, limits RoundAllowance) StartRoundInput {
	t.Helper()
	p, err := s.CurrentPreferences(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return StartRoundInput{RequestKey: key, Intent: "Find source-linked backend opportunities within these limits.",
		Outcome: "process_input", ProfileVersion: p.Version,
		Scope: RoundScope{InputRefs: []string{"campaign:active"}, Resources: []string{"source:example"},
			Operations: []string{"source.fetch", "role.extract"}},
		Limits: limits, Deadline: time.Now().Add(time.Hour).UTC().Round(0)}
}

func startedRound(t *testing.T, s *Store, key string, limits RoundAllowance) Round {
	t.Helper()
	r, created, err := s.StartRound(context.Background(), roundOwner(), roundInput(t, s, key, limits))
	if err != nil || !created || r.State != RoundQueued {
		t.Fatalf("start: %+v created=%v err=%v", r, created, err)
	}
	r, err = s.ActivateRound(context.Background(), roundOwner(), r.ID)
	if err != nil || r.State != RoundRunning {
		t.Fatalf("activate: %+v %v", r, err)
	}
	return r
}

func TestRoundDoubleStartOneActiveAndRestartPauses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	input := roundInput(t, first, "same-click", RoundAllowance{Requests: 3, Items: 3, Tools: 3, Turns: 3})
	type outcome struct {
		r       Round
		created bool
		err     error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, s := range []*Store{first, second} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			r, created, err := s.StartRound(ctx, roundOwner(), input)
			results <- outcome{r, created, err}
		}(s)
	}
	wg.Wait()
	close(results)
	createdCount := 0
	var id string
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if id != "" && id != result.r.ID {
			t.Fatalf("double start returned different rounds: %q %q", id, result.r.ID)
		}
		id = result.r.ID
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created %d rounds", createdCount)
	}
	if _, _, err := second.StartRound(ctx, roundOwner(), roundInput(t, second, "other-click", input.Limits)); !errors.Is(err, ErrActiveRound) {
		t.Fatalf("second active round: %v", err)
	}
	if _, err := first.ActivateRound(ctx, roundOwner(), id); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	r, err := restarted.Round(ctx, id)
	if err != nil || r.State != RoundPaused || !r.ReconciliationRequired || r.StopReason != "process_interrupted" {
		t.Fatalf("restart did not pause: %+v %v", r, err)
	}
	if _, _, err := restarted.StartRound(ctx, roundOwner(), roundInput(t, restarted, "another-click", input.Limits)); !errors.Is(err, ErrActiveRound) {
		t.Fatalf("paused round released active slot: %v", err)
	}
	resumed, err := restarted.ResumeRound(ctx, roundOwner(), id, r.Generation)
	if err != nil || resumed.State != RoundRunning || resumed.Used != (RoundAllowance{}) {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
}

func TestRoundReservationsRaceIdempotencyAndRetryCharge(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	r := startedRound(t, first, "race", RoundAllowance{Requests: 2, Items: 2, Tools: 2, Turns: 2})
	input := func(key string) RoundAttemptInput {
		return RoundAttemptInput{RequestKey: key, Operation: "source.fetch", ResourceID: "source:example",
			Cost: RoundAllowance{Requests: 1, Items: 1, Tools: 1, Turns: 1}}
	}
	var wg sync.WaitGroup
	results := make(chan error, 3)
	for _, pair := range []struct {
		s   *Store
		key string
	}{{first, "first"}, {second, "second"}, {first, "third"}} {
		wg.Add(1)
		go func(s *Store, key string) {
			defer wg.Done()
			_, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input(key))
			results <- err
		}(pair.s, pair.key)
	}
	wg.Wait()
	close(results)
	reserved, exhausted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			reserved++
		case errors.Is(err, ErrAllowance):
			exhausted++
		default:
			t.Fatal(err)
		}
	}
	if reserved != 2 || exhausted != 1 {
		t.Fatalf("competing reservations: reserved=%d exhausted=%d", reserved, exhausted)
	}
	read, err := first.Round(ctx, r.ID)
	if err != nil || read.Used != read.Limits {
		t.Fatalf("allowance overspent or undercharged: %+v %v", read, err)
	}
	// A new retry identity costs another unit. Replaying one identity does not.
	if _, _, err := first.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input("fourth")); !errors.Is(err, ErrAllowance) {
		t.Fatalf("retry exceeded remaining allowance: %v", err)
	}
	if _, _, err := first.ReserveRoundAttempt(ctx, roundOwner(), r.ID,
		RoundAttemptInput{RequestKey: "scope-escape", Operation: "source.fetch", ResourceID: "source:other",
			Cost: RoundAllowance{Requests: 1}}); !errors.Is(err, ErrFenced) {
		t.Fatalf("out-of-scope claim: %v", err)
	}
}

func TestRoundStopFencesLateWriteReconciliationAndPartialResult(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := startedRound(t, s, "stop", RoundAllowance{Requests: 5, Items: 3, Tools: 5, Turns: 3})
	input := func(key string) RoundAttemptInput {
		return RoundAttemptInput{RequestKey: key, Operation: "role.extract", ResourceID: "source:example",
			Cost: RoundAllowance{Requests: 1, Items: 1, Tools: 1, Turns: 1}}
	}
	first, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input("completed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	_, changeID, err := s.CreateCompany(ctx, roundOwner(), CompanyInput{Name: "Synthetic round result"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachRoundRecordChange(ctx, r.ID, first.ID, changeID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundAttempt(ctx, roundOwner(), r.ID, first.ID, true,
		json.RawMessage(`{"source":"example","fact":"observed"}`), ""); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input("in-flight"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	stopping, pending, err := s.StopRound(ctx, roundOwner(), r.ID)
	if err != nil || stopping.State != RoundStopping || len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("stop fence: %+v %+v %v", stopping, pending, err)
	}
	if _, err := s.FinishRoundAttempt(ctx, roundOwner(), r.ID, second.ID, true, json.RawMessage(`{"late":true}`), ""); !errors.Is(err, ErrFenced) {
		t.Fatalf("late write passed fence: %v", err)
	}
	if _, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input("after-stop")); !errors.Is(err, ErrFenced) {
		t.Fatalf("new work passed stop fence: %v", err)
	}
	if err := s.RecordLateRoundResult(ctx, r.ID, second.ID, json.RawMessage(`{"late":true}`)); err != nil {
		t.Fatal(err)
	}
	paused, err := s.PauseStoppedRound(ctx, roundOwner(), r.ID)
	if err != nil || paused.State != RoundPaused {
		t.Fatalf("pause after cancellation: %+v %v", paused, err)
	}
	if _, err := s.ResumeRound(ctx, roundOwner(), r.ID, paused.Generation); !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertain attempt redispatched: %v", err)
	}
	check, err := s.BeginRoundReconciliation(ctx, r.ID, second.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundReconciliation(ctx, check, AttemptUncertain, json.RawMessage(`{"status":"unknown"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeRound(ctx, roundOwner(), r.ID, paused.Generation); !errors.Is(err, ErrUncertain) {
		t.Fatalf("unknown check cleared dispatch: %v", err)
	}
	check, err = s.BeginRoundReconciliation(ctx, r.ID, second.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := s.FinishRoundReconciliation(ctx, check, AttemptObservedSuccess, json.RawMessage(`{"status":"confirmed_remote"}`))
	if err != nil || observed.State != AttemptObservedSuccess || len(observed.LateResult) == 0 {
		t.Fatalf("observation: %+v %v", observed, err)
	}
	resumed, err := s.ResumeRound(ctx, roundOwner(), r.ID, paused.Generation)
	if err != nil || resumed.Used.Requests != 4 || resumed.Used.Tools != 4 || resumed.Used.Items != 2 || resumed.Used.Turns != 2 {
		t.Fatalf("resume replenished allowance: %+v %v", resumed, err)
	}
	results, err := s.RoundResults(ctx, r.ID)
	if err != nil || len(results) != 1 {
		t.Fatalf("partial result lost or late result applied: %s %v", results, err)
	}
	changes, err := s.RoundRecordChanges(ctx, r.ID)
	if err != nil || len(changes) != 1 || changes[0] != changeID {
		t.Fatalf("record change link lost: %v %v", changes, err)
	}
	if err := s.AttachRoundRecordChange(ctx, r.ID, second.ID, changeID); !errors.Is(err, ErrFenced) {
		t.Fatalf("stopped attempt attached a change: %v", err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, second.ID); !errors.Is(err, ErrFenced) {
		t.Fatalf("observed remote attempt redispatched: %v", err)
	}
}

func TestRoundRetryHasNewChargeAndRestartKeepsPartialResult(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	r := startedRound(t, s, "retry", RoundAllowance{Requests: 3, Items: 3, Tools: 3, Turns: 3})
	input := func(key string) RoundAttemptInput {
		return RoundAttemptInput{RequestKey: key, Operation: "source.fetch", ResourceID: "source:example",
			Cost: RoundAllowance{Requests: 1, Items: 1, Tools: 1, Turns: 1}}
	}
	first, created, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input("attempt-1"))
	if err != nil || !created {
		t.Fatalf("first reserve: %+v %v %v", first, created, err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundAttempt(ctx, roundOwner(), r.ID, first.ID, false, nil, "temporary_failure"); err != nil {
		t.Fatal(err)
	}
	replayed, created, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input("attempt-1"))
	if err != nil || created || replayed.ID != first.ID {
		t.Fatalf("same identity charged again: %+v %v %v", replayed, created, err)
	}
	retry, created, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input("attempt-2"))
	if err != nil || !created || retry.ID == first.ID {
		t.Fatalf("retry not distinct: %+v %v %v", retry, created, err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, retry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundAttempt(ctx, roundOwner(), r.ID, retry.ID, true,
		json.RawMessage(`{"recordChangeId":"synthetic-audit-ref"}`), ""); err != nil {
		t.Fatal(err)
	}
	inflight, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, input("attempt-3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, inflight.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	paused, err := restarted.Round(ctx, r.ID)
	if err != nil || paused.State != RoundPaused || paused.Used != paused.Limits {
		t.Fatalf("restart refunded charges: %+v %v", paused, err)
	}
	uncertain, err := restarted.UncertainRoundAttempts(ctx, r.ID)
	if err != nil || len(uncertain) != 1 || uncertain[0].ID != inflight.ID {
		t.Fatalf("inflight dispatch lost: %+v %v", uncertain, err)
	}
	results, err := restarted.RoundResults(ctx, r.ID)
	if err != nil || len(results) != 1 {
		t.Fatalf("partial result lost: %s %v", results, err)
	}
	if _, err := restarted.ResumeRound(ctx, roundOwner(), r.ID, paused.Generation); !errors.Is(err, ErrUncertain) {
		t.Fatalf("restart redispatched uncertain work: %v", err)
	}
	if _, err := restarted.BeginRoundReconciliation(ctx, r.ID, inflight.ID, paused.Generation); !errors.Is(err, ErrAllowance) {
		t.Fatalf("read-only check bypassed exhausted allowance: %v", err)
	}
}

func TestRoundAwaitingInputAndTerminalReport(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := startedRound(t, s, "report", RoundAllowance{Requests: 1, Items: 1, Tools: 1, Turns: 1})
	r, err = s.SaveRoundProgress(ctx, roundOwner(), r.ID, r.Revision, RoundProgress{
		Step: "reading_source", Cursor: json.RawMessage(`{"page":1}`),
		Unresolved: json.RawMessage(`[]`), Report: json.RawMessage(`{"seen":1}`)})
	if err != nil || r.Step != "reading_source" {
		t.Fatalf("progress: %+v %v", r, err)
	}
	awaiting, err := s.AwaitRoundInput(ctx, roundOwner(), r.ID, json.RawMessage(`["owner-held-fact"]`))
	if err != nil || awaiting.State != RoundAwaitingInput {
		t.Fatalf("await: %+v %v", awaiting, err)
	}
	if _, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, RoundAttemptInput{RequestKey: "blocked",
		Operation: "source.fetch", ResourceID: "source:example", Cost: RoundAllowance{Requests: 1}}); !errors.Is(err, ErrFenced) {
		t.Fatalf("awaiting-input worker claimed: %v", err)
	}
	r, err = s.ContinueRound(ctx, roundOwner(), r.ID)
	if err != nil || r.State != RoundRunning || string(r.Unresolved) != "[]" {
		t.Fatalf("continue: %+v %v", r, err)
	}
	finished, err := s.FinishRound(ctx, roundOwner(), r.ID, RoundCompleted, "owner_completed",
		"useful_result", json.RawMessage(`{"seen":1,"useful":1}`))
	if err != nil || finished.State != RoundCompleted || finished.DeliverableStatus != "useful_result" ||
		finished.StopReason != "owner_completed" || finished.CompletedAt == "" {
		t.Fatalf("finish: %+v %v", finished, err)
	}
	if _, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, RoundAttemptInput{RequestKey: "late",
		Operation: "source.fetch", ResourceID: "source:example", Cost: RoundAllowance{Requests: 1}}); !errors.Is(err, ErrFenced) {
		t.Fatalf("terminal round allowed claim: %v", err)
	}
	if _, _, err := s.StartRound(ctx, roundOwner(), roundInput(t, s, "next", RoundAllowance{Requests: 1})); err != nil {
		t.Fatalf("terminal round retained active slot: %v", err)
	}
}

func TestRoundRestartFencesPendingPausedReconciliation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	r := startedRound(t, s, "pending-check", RoundAllowance{Requests: 4, Tools: 4})
	a, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, RoundAttemptInput{RequestKey: "dispatch",
		Operation: "source.fetch", ResourceID: "source:example", Cost: RoundAllowance{Requests: 1, Tools: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StopRound(ctx, roundOwner(), r.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := s.PauseStoppedRound(ctx, roundOwner(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	check, err := s.BeginRoundReconciliation(ctx, r.ID, a.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	current, err := restarted.Round(ctx, r.ID)
	if err != nil || current.State != RoundPaused || current.Generation <= paused.Generation ||
		current.Used.Requests != 2 || current.Used.Tools != 2 {
		t.Fatalf("restart did not fence check or retained wrong charge: %+v %v", current, err)
	}
	if _, err := restarted.FinishRoundReconciliation(ctx, check, AttemptObservedFailure,
		json.RawMessage(`{"stale":true}`)); !errors.Is(err, ErrFenced) {
		t.Fatalf("pre-restart check changed attempt: %v", err)
	}
	if _, err := restarted.BeginRoundReconciliation(ctx, r.ID, a.ID, paused.Generation); !errors.Is(err, ErrFenced) {
		t.Fatalf("pre-restart Resume reserved another check: %v", err)
	}
	if _, err := restarted.ResumeRound(ctx, roundOwner(), r.ID, paused.Generation); !errors.Is(err, ErrFenced) {
		t.Fatalf("pre-restart Resume reactivated: %v", err)
	}
	if _, err := restarted.ResumeRound(ctx, roundOwner(), r.ID, current.Generation); !errors.Is(err, ErrUncertain) {
		t.Fatalf("new generation skipped uncertain attempt: %v", err)
	}
	newCheck, err := restarted.BeginRoundReconciliation(ctx, r.ID, a.ID, current.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.FinishRoundReconciliation(ctx, newCheck, AttemptObservedFailure,
		json.RawMessage(`{"observed":"failed"}`)); err != nil {
		t.Fatal(err)
	}
	resumed, err := restarted.ResumeRound(ctx, roundOwner(), r.ID, current.Generation)
	if err != nil || resumed.State != RoundRunning || resumed.Used.Requests != 3 || resumed.Used.Tools != 3 {
		t.Fatalf("new generation did not resume with retained charges: %+v %v", resumed, err)
	}
}

func TestRoundNewStopFencesResolvedCheckBeforeResume(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := startedRound(t, s, "stop-after-check", RoundAllowance{Requests: 3, Tools: 3})
	a, _, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, RoundAttemptInput{RequestKey: "dispatch",
		Operation: "source.fetch", ResourceID: "source:example", Cost: RoundAllowance{Requests: 1, Tools: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StopRound(ctx, roundOwner(), r.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := s.PauseStoppedRound(ctx, roundOwner(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	check, err := s.BeginRoundReconciliation(ctx, r.ID, a.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundReconciliation(ctx, check, AttemptObservedFailure,
		json.RawMessage(`{"observed":"failed"}`)); err != nil {
		t.Fatal(err)
	}
	stopped, _, err := s.StopRound(ctx, roundOwner(), r.ID)
	if err != nil || stopped.Generation <= paused.Generation || stopped.State != RoundPaused {
		t.Fatalf("newer stop did not fence Resume: %+v %v", stopped, err)
	}
	if _, err := s.ResumeRound(ctx, roundOwner(), r.ID, paused.Generation); !errors.Is(err, ErrFenced) {
		t.Fatalf("old Resume reactivated after newer Stop: %v", err)
	}
}

func TestRoundStartReplayAfterDeadlineReturnsExistingRound(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input := roundInput(t, s, "expired-replay", RoundAllowance{Requests: 1})
	input.Deadline = time.Now().Add(250 * time.Millisecond)
	first, created, err := s.StartRound(ctx, roundOwner(), input)
	if err != nil || !created {
		t.Fatalf("start: %+v %v %v", first, created, err)
	}
	time.Sleep(time.Until(input.Deadline) + time.Millisecond)
	replay, created, err := s.StartRound(ctx, roundOwner(), input)
	if err != nil || created || replay.ID != first.ID || replay.Generation != first.Generation {
		t.Fatalf("expired retry failed to return original commission: %+v %v %v", replay, created, err)
	}
	input.RequestKey = "new-expired-start"
	if _, _, err := s.StartRound(ctx, roundOwner(), input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("new expired commission accepted: %v", err)
	}
}
