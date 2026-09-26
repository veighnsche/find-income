package researchservice

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// startRecoveryRound starts one owner round for recovery tests and leaves it
// queued; callers drive it to the wanted state. Rounds serialize through
// the single active slot, so tests finish each round before starting the
// next, leaving at most one non-terminal round behind.
func startRecoveryRound(t *testing.T, db *store.Store, key, outcome string) store.Round {
	t.Helper()
	round, created, err := db.StartRound(context.Background(), testOwner, store.StartRoundInput{
		RequestKey: key, Intent: "recovery test run", Outcome: outcome, ProfileVersion: 1,
		Scope: store.RoundScope{Resources: []string{"campaign:active"},
			Operations: []string{store.RoundCodexTurn}},
		Limits:   store.RoundAllowance{Requests: 9, Items: 1, Tools: 3, Turns: 1},
		Deadline: time.Now().Add(30 * time.Minute).UTC(),
	})
	if err != nil || !created {
		t.Fatalf("start %s: created=%v err=%v", key, created, err)
	}
	return round
}

func activateRecoveryRound(t *testing.T, db *store.Store, id string) store.Round {
	t.Helper()
	round, err := db.ActivateRound(context.Background(), testOwner, id)
	if err != nil {
		t.Fatal(err)
	}
	return round
}

func finishRecoveryRound(t *testing.T, db *store.Store, id string, terminal store.RoundState, reason string) {
	t.Helper()
	if _, err := db.FinishRound(context.Background(), testOwner, id, terminal,
		reason, "research_run", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func pauseRecoveryRound(t *testing.T, db *store.Store, id string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := db.StopRound(ctx, testOwner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PauseStoppedRound(ctx, testOwner, id); err != nil {
		t.Fatal(err)
	}
}

// D3/C1: the server recovery list spans every run state (running, paused,
// failed, completed), filters by outcome/state, pages newest-first without
// gaps or duplicates, and never starts, resumes or commissions anything.
func TestListResearchRunsRecoversAcrossStates(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()

	completed := startRecoveryRound(t, h.db, "d3-completed", "research_run")
	activateRecoveryRound(t, h.db, completed.ID)
	finishRecoveryRound(t, h.db, completed.ID, store.RoundCompleted, "owner_completed")

	failed := startRecoveryRound(t, h.db, "d3-failed", "research_run")
	activateRecoveryRound(t, h.db, failed.ID)
	finishRecoveryRound(t, h.db, failed.ID, store.RoundFailed, "contributor_error")

	other := startRecoveryRound(t, h.db, "d3-other", "process_input")
	activateRecoveryRound(t, h.db, other.ID)
	finishRecoveryRound(t, h.db, other.ID, store.RoundCompleted, "owner_completed")

	paused := startRecoveryRound(t, h.db, "d3-paused", "research_run")
	activateRecoveryRound(t, h.db, paused.ID)
	pauseRecoveryRound(t, h.db, paused.ID)

	roundsBefore := countRounds(t, h.db)
	page, err := h.svc.ListResearchRuns(ctx, testOwner, httpapi.RunHistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if countRounds(t, h.db) != roundsBefore {
		t.Fatal("recovery list started or resumed a run")
	}
	wantOrder := []string{paused.ID, other.ID, failed.ID, completed.ID}
	if len(page.Items) != len(wantOrder) || page.NextCursor != "" {
		t.Fatalf("full list: %+v", page)
	}
	for i, want := range wantOrder {
		if page.Items[i].RunID != want {
			t.Fatalf("item %d: got %q, want newest-first %q (%+v)", i, page.Items[i].RunID, want, page.Items)
		}
	}
	if page.Items[0].State != "paused" || page.Items[0].Outcome != "research_run" ||
		page.Items[0].RequestKey != "d3-paused" || page.Items[0].UpdatedAt == "" {
		t.Fatalf("paused item omits recovery fields: %+v", page.Items[0])
	}
	if page.Items[2].State != "failed" || page.Items[2].CompletedAt == "" {
		t.Fatalf("failed item omits state/completion: %+v", page.Items[2])
	}

	filtered, err := h.svc.ListResearchRuns(ctx, testOwner, httpapi.RunHistoryOptions{Outcome: "research_run"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 3 {
		t.Fatalf("outcome filter: %+v", filtered.Items)
	}
	pausedOnly, err := h.svc.ListResearchRuns(ctx, testOwner, httpapi.RunHistoryOptions{States: []string{"paused"}})
	if err != nil || len(pausedOnly.Items) != 1 || pausedOnly.Items[0].RunID != paused.ID {
		t.Fatalf("paused filter: %+v err=%v", pausedOnly, err)
	}
	terminal, err := h.svc.ListResearchRuns(ctx, testOwner,
		httpapi.RunHistoryOptions{States: []string{"completed", "failed"}})
	if err != nil || len(terminal.Items) != 3 {
		t.Fatalf("terminal filter: %+v err=%v", terminal, err)
	}
	running, err := h.svc.ListResearchRuns(ctx, testOwner, httpapi.RunHistoryOptions{States: []string{"running"}})
	if err != nil || len(running.Items) != 0 || running.NextCursor != "" {
		t.Fatalf("empty filter page: %+v err=%v", running, err)
	}

	// Paging chains through every run exactly once.
	var seen []string
	cursor := ""
	for {
		chunk, err := h.svc.ListResearchRuns(ctx, testOwner, httpapi.RunHistoryOptions{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range chunk.Items {
			seen = append(seen, item.RunID)
		}
		if chunk.NextCursor == "" {
			break
		}
		cursor = chunk.NextCursor
	}
	if len(seen) != len(wantOrder) {
		t.Fatalf("paged walk saw %v", seen)
	}
	for i, want := range wantOrder {
		if seen[i] != want {
			t.Fatalf("paged walk order: %v, want %v", seen, wantOrder)
		}
	}

	if _, err := h.svc.ListResearchRuns(ctx, testOwner, httpapi.RunHistoryOptions{States: []string{"stopped"}}); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("unknown state: got %v, want invalid", err)
	}
	if _, err := h.svc.ListResearchRuns(ctx, testOwner, httpapi.RunHistoryOptions{Cursor: "bogus"}); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("bogus cursor: got %v, want invalid", err)
	}
	if _, err := h.svc.ListResearchRuns(ctx, store.Actor{}, httpapi.RunHistoryOptions{}); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("anonymous list: got %v, want invalid", err)
	}
}

// D3/C1: the latest-terminal read returns the newest completed/failed run
// per outcome so a fresh context restores it without guessing its id; a
// run without any terminal work is honest not_found.
func TestLatestTerminalResearchRun(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()

	if _, err := h.svc.LatestTerminalResearchRun(ctx, testOwner, ""); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("empty history: got %v, want not_found", err)
	}
	completed := startRecoveryRound(t, h.db, "d3-latest-completed", "research_run")
	activateRecoveryRound(t, h.db, completed.ID)
	finishRecoveryRound(t, h.db, completed.ID, store.RoundCompleted, "owner_completed")
	failed := startRecoveryRound(t, h.db, "d3-latest-failed", "research_run")
	activateRecoveryRound(t, h.db, failed.ID)
	finishRecoveryRound(t, h.db, failed.ID, store.RoundFailed, "contributor_error")

	latest, err := h.svc.LatestTerminalResearchRun(ctx, testOwner, "")
	if err != nil {
		t.Fatal(err)
	}
	if latest.RunID != failed.ID || latest.State != "failed" || latest.CompletedAt == "" {
		t.Fatalf("latest terminal: %+v", latest)
	}
	scoped, err := h.svc.LatestTerminalResearchRun(ctx, testOwner, "research_run")
	if err != nil || scoped.RunID != failed.ID {
		t.Fatalf("scoped latest: %+v err=%v", scoped, err)
	}
	if _, err := h.svc.LatestTerminalResearchRun(ctx, testOwner, "process_input"); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("unrecorded outcome: got %v, want not_found", err)
	}
	if _, err := h.svc.LatestTerminalResearchRun(ctx, store.Actor{}, ""); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("anonymous latest: got %v, want invalid", err)
	}
}

// D3: recovery reads never touch the discovery commissioner, the run
// supervisor, or the journal: a service without a commissioner serves
// them, and they start nothing.
func TestRecoveryReadsUseNoProviderSurface(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	round := startRecoveryRound(t, h.db, "d3-nocommissioner", "research_run")
	activateRecoveryRound(t, h.db, round.ID)
	finishRecoveryRound(t, h.db, round.ID, store.RoundCompleted, "owner_completed")

	journal, err := store.NewRunEventJournal(h.db)
	if err != nil {
		t.Fatal(err)
	}
	commissioner := &countingCommissioner{}
	svc, err := New(Deps{DB: h.db, Supervisor: h.sup, Journal: journal, Captures: h.caps,
		AgentID: testAgent.ID, Muse: commissioner})
	if err != nil {
		t.Fatal(err)
	}
	before := countRounds(t, h.db)
	if _, err := svc.ListResearchRuns(ctx, testOwner, httpapi.RunHistoryOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LatestTerminalResearchRun(ctx, testOwner, "research_run"); err != nil {
		t.Fatal(err)
	}
	if commissioner.calls != 0 || countRounds(t, h.db) != before {
		t.Fatalf("recovery reads commissioned or started work: calls=%d rounds=%d->%d",
			commissioner.calls, before, countRounds(t, h.db))
	}
}

// countingCommissioner proves recovery reads never commission: any
// admission attempt is observable here.
type countingCommissioner struct {
	calls int
}

func (c *countingCommissioner) DiscoveryCeiling() musecode.Bounds {
	return musecode.Bounds{}
}

func (c *countingCommissioner) CommissionDiscoveryAsync(_ context.Context, runRef string, _ musecode.PublicCriteria, _ int64, _ string, _ musecode.Bounds) (musewire.AsyncAdmission, error) {
	c.calls++
	return musewire.AsyncAdmission{RunRef: runRef}, nil
}

func countRounds(t *testing.T, db *store.Store) int {
	t.Helper()
	var n int
	if err := db.Read(context.Background(), func(r store.Reader) error {
		return r.QueryRowContext(context.Background(), "SELECT count(*) FROM rounds").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
