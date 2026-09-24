package runtimeaccept

import (
	"context"
	"io/fs"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Startup is inert and status reads dispatch nothing: wiring with absent
// executor binaries still succeeds (backends verify lazily), no per-op
// scratch appears before the first subprocess kind runs, and every read
// path leaves the ledger, journal and results untouched.
func TestWiringIsLazyAndReadsAreInert(t *testing.T) {
	h := newHarness(t, nil, "inert startup", "t24-inert-1", func(cfg *researchwire.Config) {
		cfg.ChromePath = "/nonexistent/t24-chrome"
		cfg.PythonPath = "/nonexistent/t24-python"
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/role", newCounter().handler("inert posting body"))
	board := fixtureServer(t, mux)

	assertNoScratchChildren(t, h.scratch)
	fetched := h.dispatch(t, "t24-inert-fetch", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/role"))
	if fetched.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("fetch: %s", fetched.Outcome)
	}
	assertNoScratchChildren(t, h.scratch)

	ledgerBefore := h.ledger(t)
	journalBefore := len(h.journal(t))
	resultsBefore, err := h.db.RoundResults(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}

	// Every status surface: supervisor reads plus the full HTTP-adapter
	// read set. ResearchIdentity targets a missing subject (not_found is
	// still a pure read).
	if _, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Supervisor.Usage(h.ctx, h.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Supervisor.RunBoundsFor(h.ctx, h.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Research.ResearchRun(h.ctx, h.owner, h.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Research.ResearchActivity(h.ctx, h.owner, h.runID, "", 50); err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Research.ResearchCapture(h.ctx, h.owner, fetched.CaptureID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Research.ResearchReport(h.ctx, h.owner, h.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Research.ResearchIdentity(h.ctx, h.owner, "vacancy", "missing-subject"); contractErr(t, err).Code != researchcontract.OutcomeNotFound {
		t.Fatalf("missing identity read: %v", err)
	}

	if after := h.ledger(t); after != ledgerBefore {
		t.Fatalf("reads moved the ledger: %+v -> %+v", ledgerBefore, after)
	}
	if n := len(h.journal(t)); n != journalBefore {
		t.Fatalf("reads appended %d journal events", n-journalBefore)
	}
	resultsAfter, err := h.db.RoundResults(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resultsAfter) != len(resultsBefore) {
		t.Fatal("reads wrote round results")
	}
	assertNoScratchChildren(t, h.scratch)

	// The absent binaries fail closed only when their kinds dispatch: the
	// attempt runs, fails honestly with backend_not_configured, records the
	// failure as an observation, and captures nothing.
	bad := browseDispatch(t, h, "t24-inert-bad-browse", board.URL+"/role")
	if bad.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("absent-binary browse: %s", bad.Outcome)
	}
	if bad.Receipt.ErrorCode != "backend_not_configured" {
		t.Fatalf("absent-binary receipt: %+v", bad.Receipt)
	}
	if bad.ObservationID == "" || bad.CaptureID != "" {
		t.Fatalf("absent-binary browse evidence: obs=%q cap=%q", bad.ObservationID, bad.CaptureID)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 2 {
		t.Fatalf("failed dispatch not charged: %+v", ledger.Observed)
	}
}

func assertNoScratchChildren(t *testing.T, scratch string) {
	t.Helper()
	var names []string
	if err := filepath.WalkDir(scratch, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != scratch {
			names = append(names, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("scratch droppings before first subprocess op: %v", names)
	}
}

// Wiring fails closed: without a Jev provider (or an absolute artifact
// root) research stays unwired rather than half-built.
func TestWireFailsClosed(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	provider := &scriptedProvider{model: "fixture-jev-1", verdicts: map[string]string{}}

	if _, err := researchwire.Wire(db, researchwire.Config{
		ArtifactRoot: filepath.Join(dir, "research-artifacts"),
	}); err == nil {
		t.Fatal("Wire without a Jev provider succeeded")
	}
	if _, err := researchwire.Wire(db, researchwire.Config{
		ArtifactRoot: "relative/path", JevProvider: provider,
	}); err == nil {
		t.Fatal("Wire with a relative artifact root succeeded")
	}
	if _, err := researchwire.Wire(nil, researchwire.Config{
		ArtifactRoot: filepath.Join(dir, "research-artifacts"), JevProvider: provider,
	}); err == nil {
		t.Fatal("Wire without a store succeeded")
	}
}
