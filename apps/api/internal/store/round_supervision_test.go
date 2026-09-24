package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

func openSupervisionDB(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dir
}

func supervisionOwner() Actor { return Actor{Kind: "administrator", ID: "owner"} }

func startSupervisionRound(t *testing.T, db *Store, key string) Round {
	t.Helper()
	ctx := context.Background()
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, created, err := db.StartRound(ctx, supervisionOwner(), StartRoundInput{
		RequestKey: key, Intent: "supervision test", Outcome: "research_run",
		ProfileVersion: p.Version,
		Scope: RoundScope{
			Operations: append(append([]string{RoundCodexTurn}, ResearchOperations()...), RoundJevRequest),
			Resources:  []string{ResearchAuthorityResource},
			Delegates:  []string{"agent-1"},
		},
		Limits:   RoundAllowance{Requests: 20, Items: 20, Tools: 20, Turns: 4},
		Deadline: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("start: %+v %v %v", r, created, err)
	}
	r, err = db.ActivateRound(ctx, supervisionOwner(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func journalEvent(runID, id, kind string) researchcontract.Event {
	now := time.Now()
	return researchcontract.Event{
		ID: id, RunID: runID, Kind: kind, Outcome: researchcontract.OutcomeOK,
		Payload: json.RawMessage(`{"n":1}`), ObservedAt: now, RecordedAt: now,
	}
}

func TestRunEventJournalAppendListDedup(t *testing.T) {
	ctx := context.Background()
	db, dir := openSupervisionDB(t)
	round := startSupervisionRound(t, db, "journal-1")
	journal, err := NewRunEventJournal(db)
	if err != nil {
		t.Fatal(err)
	}

	first := journalEvent(round.ID, "e1", "run.dispatched")
	if err := journal.Append(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Idempotent replay: same id, different payload — first write wins.
	replay := first
	replay.Payload = json.RawMessage(`{"n":2}`)
	if err := journal.Append(ctx, replay); err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(ctx, journalEvent(round.ID, "e2", "run.observed")); err != nil {
		t.Fatal(err)
	}

	got, err := journal.RunEvent(ctx, "e1")
	if err != nil || string(got.Payload) != `{"n":1}` || got.RunID != round.ID {
		t.Fatalf("point read after dedup: %+v %v", got, err)
	}
	if _, err := journal.RunEvent(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing event: %v", err)
	}

	// Cursor paging with an exact tail: page 1, then the tail page with no
	// phantom cursor.
	page, next, err := journal.List(ctx, round.ID, "", 1)
	if err != nil || len(page) != 1 || page[0].ID != "e1" || next != "e1" {
		t.Fatalf("page 1: %+v %q %v", page, next, err)
	}
	page, next, err = journal.List(ctx, round.ID, next, 1)
	if err != nil || len(page) != 1 || page[0].ID != "e2" || next != "" {
		t.Fatalf("tail page: %+v %q %v", page, next, err)
	}
	if _, _, err := journal.List(ctx, round.ID, "bogus", 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown cursor: %v", err)
	}

	// Durability: a reopened store serves the same journal.
	db.Close()
	reopened, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	journal2, err := NewRunEventJournal(reopened)
	if err != nil {
		t.Fatal(err)
	}
	events, next, err := journal2.List(ctx, round.ID, "", 10)
	if err != nil || len(events) != 2 || next != "" {
		t.Fatalf("reopened journal: %d events %q %v", len(events), next, err)
	}
}

func TestRunEventJournalValidation(t *testing.T) {
	ctx := context.Background()
	db, _ := openSupervisionDB(t)
	round := startSupervisionRound(t, db, "journal-2")
	journal, err := NewRunEventJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunEventJournal(nil); err == nil {
		t.Fatal("nil store accepted")
	}

	base := journalEvent(round.ID, "v1", "run.dispatched")
	for name, mutate := range map[string]func(*researchcontract.Event){
		"empty id":      func(e *researchcontract.Event) { e.ID = "" },
		"empty run":     func(e *researchcontract.Event) { e.RunID = "" },
		"empty kind":    func(e *researchcontract.Event) { e.Kind = "" },
		"long kind":     func(e *researchcontract.Event) { e.Kind = strings.Repeat("k", 65) },
		"bad outcome":   func(e *researchcontract.Event) { e.Outcome = "bogus" },
		"zero observed": func(e *researchcontract.Event) { e.ObservedAt = time.Time{} },
		"bad json":      func(e *researchcontract.Event) { e.Payload = json.RawMessage(`{`) },
		"oversize": func(e *researchcontract.Event) {
			e.Payload = json.RawMessage(`"` + strings.Repeat("p", MaxRunEventPayload) + `"`)
		},
	} {
		e := base
		e.ID = "v-" + name
		mutate(&e)
		if err := journal.Append(ctx, e); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Unknown run fails the foreign key as not_found, never a raw driver error.
	if err := journal.Append(ctx, journalEvent("nope", "v-fk", "run.dispatched")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
	events, next, err := journal.List(ctx, round.ID, "", 0)
	if err != nil || len(events) != 0 || next != "" {
		t.Fatalf("journal holds rejected events: %d %q %v", len(events), next, err)
	}
}

func TestSupervisionReads(t *testing.T) {
	ctx := context.Background()
	db, _ := openSupervisionDB(t)
	round := startSupervisionRound(t, db, "reads-1")
	agent := Actor{Kind: "agent", ID: "agent-1"}

	var none string
	if err := db.Read(ctx, func(r Reader) error {
		var err error
		none, err = LatestRoundThreadTx(ctx, r, round.ID)
		return err
	}); err != nil || none != "" {
		t.Fatalf("no thread: %q %v", none, err)
	}

	turnCost, _ := RoundOperationCost(RoundCodexTurn)
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, RoundAttemptInput{
		RequestKey: "turn-1", Operation: RoundCodexTurn,
		ResourceID: ResearchAuthorityResource, Cost: turnCost, CursorAttemptID: "brief-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundThread(ctx, round.ID, turn.ID, turn.Generation, "thread-9"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "thread-9", "turn-9"); err != nil {
		t.Fatal(err)
	}

	researchCost, _ := RoundOperationCost(RoundResearchFetch)
	stale, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, RoundAttemptInput{
		RequestKey: "stale-1", Operation: RoundResearchFetch,
		ResourceID: ResearchAuthorityResource, Cost: researchCost, CursorAttemptID: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Read(ctx, func(r Reader) error {
		thread, err := LatestRoundThreadTx(ctx, r, round.ID)
		if err != nil || thread != "thread-9" {
			t.Fatalf("latest thread: %q %v", thread, err)
		}
		live, err := DispatchedTurnAttemptTx(ctx, r, round.ID, round.Generation)
		if err != nil || live.ID != turn.ID {
			t.Fatalf("live turn: %+v %v", live, err)
		}
		if _, err := DispatchedTurnAttemptTx(ctx, r, round.ID, round.Generation+1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("turn at wrong generation: %v", err)
		}
		staleList, err := StaleReservedAttemptsTx(ctx, r, round.ID, round.Generation)
		if err != nil || len(staleList) != 0 {
			t.Fatalf("nothing stale yet: %d %v", len(staleList), err)
		}
		staleList, err = StaleReservedAttemptsTx(ctx, r, round.ID, round.Generation+1)
		if err != nil || len(staleList) != 1 || staleList[0].ID != stale.ID {
			t.Fatalf("stale holds: %+v %v", staleList, err)
		}
		uncertain, err := UncertainAttemptsTx(ctx, r, round.ID)
		if err != nil || len(uncertain) != 0 {
			t.Fatalf("nothing uncertain: %d %v", len(uncertain), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := db.StopRound(ctx, supervisionOwner(), round.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(r Reader) error {
		uncertain, err := UncertainAttemptsTx(ctx, r, round.ID)
		if err != nil || len(uncertain) != 1 || uncertain[0].ID != turn.ID {
			t.Fatalf("uncertain after stop: %+v %v", uncertain, err)
		}
		// The thread survives the fence: continuation resumes it.
		thread, err := LatestRoundThreadTx(ctx, r, round.ID)
		if err != nil || thread != "thread-9" {
			t.Fatalf("thread after stop: %q %v", thread, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
