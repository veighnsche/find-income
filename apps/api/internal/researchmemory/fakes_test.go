package researchmemory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// fakeAuthority is the T11 contract double for researchcontract.Authority
// (the real one lands at T12). Reconciliation entries are keyed by attempt id.
type fakeAuthority struct {
	recon  map[string]researchcontract.Reconciliation
	ledger researchcontract.UsageLedger
}

func newFakeAuthority() *fakeAuthority {
	return &fakeAuthority{recon: map[string]researchcontract.Reconciliation{}}
}

func (f *fakeAuthority) Check(context.Context, researchcontract.CheckInput) error {
	return nil
}

func (f *fakeAuthority) Reserve(_ context.Context, runID, operation, key, hash string) (researchcontract.Reservation, error) {
	return researchcontract.Reservation{ID: "res-" + key, AttemptID: "attempt-1",
		Operation: operation, RunID: runID, Generation: 1,
		IdempotencyKey: key, PayloadHash: hash, ReservedAt: time.Now()}, nil
}

func (f *fakeAuthority) Release(context.Context, string) error { return nil }

func (f *fakeAuthority) Usage(context.Context, string) (researchcontract.UsageLedger, error) {
	return f.ledger, nil
}

func (f *fakeAuthority) ReconciliationFor(_ context.Context, attemptID string) (researchcontract.Reconciliation, error) {
	if r, ok := f.recon[attemptID]; ok {
		return r, nil
	}
	return researchcontract.Reconciliation{}, researchcontract.NewError(
		researchcontract.OutcomeNotFound, "attempt", "no reconciliation for "+attemptID)
}

type manualClock struct{ t time.Time }

func (c *manualClock) now() time.Time { return c.t }
func (c *manualClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

type testEnv struct {
	dir     string
	artsDir string
	db      *store.Store
	arts    *ArtifactStore
	auth    *fakeAuthority
	clock   *manualClock
	mem     *Memory
	caps    *Captures
}

func newTestEnv(t *testing.T, leaseTTL time.Duration) *testEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	artsDir := filepath.Join(t.TempDir(), "artifacts")
	arts, err := OpenArtifactStore(artsDir)
	if err != nil {
		t.Fatal(err)
	}
	auth := newFakeAuthority()
	clock := &manualClock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	mem, err := NewMemory(db, store.FixtureActor(), arts, auth,
		&MemoryOptions{Now: clock.now, LeaseTTL: leaseTTL})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := NewCaptures(db, arts)
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{dir: dir, artsDir: artsDir, db: db, arts: arts, auth: auth, clock: clock, mem: mem, caps: caps}
	env.seedRound(t, "round-1", "attempt-1", "running")
	return env
}

func (e *testEnv) seedRound(t *testing.T, roundID, attemptID, state string) {
	t.Helper()
	err := e.db.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		return store.SeedResearchRoundState(context.Background(), db, roundID, attemptID, state)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func testDescriptor() researchcontract.RequestDescriptor {
	return store.FixtureRequestDescriptor()
}

func testReceipt(id, fingerprint string) researchcontract.ExecutionReceipt {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	return researchcontract.ExecutionReceipt{
		ID: id, Operation: researchcontract.OperationFetch, Fingerprint: fingerprint,
		Status: researchcontract.ReceiptOK, Attempts: 1,
		Executor:  store.FixtureExecutorIdentity(),
		StartedAt: now, EndedAt: now.Add(time.Second),
	}
}

func testCapture(body string) *CaptureInput {
	return &CaptureInput{
		Bytes:        []byte(body),
		MediaType:    "text/html",
		OriginalURL:  "https://example.com/jobs/42",
		FinalURL:     "https://example.com/jobs/42",
		Provenance:   researchcontract.ProvenanceFetchedResponse,
		Completeness: store.CaptureComplete,
		ExtentJSON:   `{"kind":"body"}`,
	}
}

func claimAs(t *testing.T, m *Memory, runID, owner string, gen int64, key string) researchcontract.MemoryClaim {
	t.Helper()
	claim, err := m.Claim(context.Background(), runID, owner, gen, testDescriptor(), key)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func mustOutcome(t *testing.T, claim researchcontract.MemoryClaim, want researchcontract.Outcome) {
	t.Helper()
	if claim.Outcome != want {
		t.Fatalf("outcome = %q, want %q", claim.Outcome, want)
	}
}

func mustContractCode(t *testing.T, err error, want researchcontract.Outcome) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want outcome %q", want)
	}
	var cerr *researchcontract.Error
	if e, ok := err.(*researchcontract.Error); ok {
		cerr = e
	} else {
		t.Fatalf("err = %v (%T), want contract outcome %q", err, err, want)
	}
	if cerr.Code != want {
		t.Fatalf("code = %q, want %q (%v)", cerr.Code, want, err)
	}
}
