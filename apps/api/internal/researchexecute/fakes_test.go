package researchexecute

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchmemory"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// fakeAuthority is a T06-interface authority double with an allowance ledger
// and failure injection. Production uses the real rounds.Authority (T12);
// supervisor integration is T23.
type fakeAuthority struct {
	mu           sync.Mutex
	db           *store.Store
	checks       int
	reserves     int
	releases     []string
	next         int64
	allowance    int64
	budgetHeld   int64
	failCheck    error
	failReserve  error
	reconciled   map[string]researchcontract.Reconciliation
	reservations map[string]string // reservation ID -> attempt ID
	replay       map[string]replayEntry
}

type replayEntry struct {
	reservation researchcontract.Reservation
	payloadHash string
}

func newFakeAuthority(db *store.Store, allowance int64) *fakeAuthority {
	return &fakeAuthority{db: db, allowance: allowance, reconciled: map[string]researchcontract.Reconciliation{},
		reservations: map[string]string{}, replay: map[string]replayEntry{}}
}

func (f *fakeAuthority) Check(_ context.Context, in researchcontract.CheckInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.failCheck != nil {
		return f.failCheck
	}
	if in.RunID == "" || in.Generation <= 0 {
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "run", "bad check input")
	}
	return nil
}

func (f *fakeAuthority) Reserve(ctx context.Context, runID, operation, key, hash string) (researchcontract.Reservation, error) {
	f.mu.Lock()
	f.reserves++
	if f.failReserve != nil {
		err := f.failReserve
		f.mu.Unlock()
		return researchcontract.Reservation{}, err
	}
	if f.budgetHeld >= f.allowance {
		f.mu.Unlock()
		return researchcontract.Reservation{}, researchcontract.NewError(researchcontract.OutcomeBudgetExhausted,
			string(researchcontract.AuthorityAllowance), "fake allowance exhausted")
	}
	if prev, ok := f.replay[runID+"\x00"+key]; ok {
		if prev.payloadHash != hash || prev.reservation.Operation != operation {
			f.mu.Unlock()
			return researchcontract.Reservation{}, researchcontract.NewError(researchcontract.OutcomeConflict,
				"idempotencyKey", "key reserved with a different payload or operation")
		}
		existing := prev.reservation
		f.mu.Unlock()
		return existing, nil
	}
	f.next++
	f.budgetHeld++
	id := fmt.Sprintf("rsv-%d", f.next)
	attempt := fmt.Sprintf("attempt-%d", f.next)
	f.reservations[id] = attempt
	res := researchcontract.Reservation{ID: id, AttemptID: attempt, Operation: operation,
		RunID: runID, Generation: 1, IdempotencyKey: key, PayloadHash: hash,
		ReservedAt: time.Now()}
	f.replay[runID+"\x00"+key] = replayEntry{reservation: res, payloadHash: hash}
	f.mu.Unlock()
	// The attempt id is a REAL round_attempts row (lease owners and Observe's
	// RoundAttemptID resolve through the real helpers, as with T12).
	err := f.db.ResearchWrite(ctx, func(rdb store.ResearchDB) error {
		_, err := rdb.ExecContext(ctx, `INSERT INTO round_attempts
  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
   requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at)
  VALUES (?,?,?,?,?,'research',1,'reserved',1,0,1,0,?,?)`,
			attempt, runID, key, store.FixtureSHA256(key), operation,
			store.FixtureTime, store.FixtureTime)
		return err
	})
	if err != nil {
		return researchcontract.Reservation{}, err
	}
	return res, nil
}

func (f *fakeAuthority) Release(_ context.Context, reservationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	attempt, ok := f.reservations[reservationID]
	if !ok {
		return researchcontract.NewError(researchcontract.OutcomeNotFound, "reservation", "unknown "+reservationID)
	}
	delete(f.reservations, reservationID)
	f.releases = append(f.releases, reservationID)
	f.budgetHeld--
	_ = attempt
	return nil
}

func (f *fakeAuthority) Usage(_ context.Context, _ string) (researchcontract.UsageLedger, error) {
	return researchcontract.UsageLedger{}, nil
}

func (f *fakeAuthority) ReconciliationFor(_ context.Context, attemptID string) (researchcontract.Reconciliation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec, ok := f.reconciled[attemptID]; ok {
		return rec, nil
	}
	return researchcontract.Reconciliation{}, researchcontract.NewError(researchcontract.OutcomeNotFound, "attempt", attemptID)
}

func (f *fakeAuthority) counts() (checks, reserves, held int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.reserves, int(f.budgetHeld)
}

// testEnv binds real T11 memory + captures on a TempDir DB with a fake
// authority and a loopback-permitting executor.
type testEnv struct {
	db   *store.Store
	arts *researchmemory.ArtifactStore
	auth *fakeAuthority
	mem  *researchmemory.Memory
	caps *researchmemory.Captures
	ex   *Executor
}

func newTestEnv(t *testing.T, cfg Config) *testEnv {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	arts, err := researchmemory.OpenArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	auth := newFakeAuthority(db, 100)
	mem, err := researchmemory.NewMemory(db, store.FixtureActor(), arts, auth, nil)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := researchmemory.NewCaptures(db, arts)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ResearchWrite(ctx, func(rdb store.ResearchDB) error {
		return store.SeedResearchRoundState(ctx, rdb, "run-1", "seed-attempt", "running")
	}); err != nil {
		t.Fatal(err)
	}
	cfg.ScratchRoot = t.TempDir()
	cfg.PermitLoopback = true
	ex, err := NewExecutor(auth, mem, caps, db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{db: db, arts: arts, auth: auth, mem: mem, caps: caps, ex: ex}
}

func (e *testEnv) execute(t *testing.T, in researchcontract.ExecuteInput) researchcontract.ExecuteOutput {
	t.Helper()
	if in.RunID == "" {
		in.RunID = "run-1"
	}
	if in.Generation == 0 {
		in.Generation = 1
	}
	out, err := e.ex.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out
}

func mustContractErr(t *testing.T, err error, want researchcontract.Outcome) *researchcontract.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want outcome %s, got nil error", want)
	}
	var cerr *researchcontract.Error
	if !isContractError(err, &cerr) {
		t.Fatalf("want contract error %s, got %T %v", want, err, err)
	}
	if cerr.Code != want {
		t.Fatalf("want outcome %s, got %s (%v)", want, cerr.Code, err)
	}
	return cerr
}

func isContractError(err error, target **researchcontract.Error) bool {
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if cerr, ok := err.(*researchcontract.Error); ok {
			*target = cerr
			return true
		}
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// fixtureServer serves deterministic pages on an isolated loopback port and
// records peak concurrent in-flight requests.
type fixtureServer struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	hits      map[string]int
	bodies    map[string][]byte
	redirects map[string]string
	rawQuery  map[string]string
	peak      atomic.Int64
	cur       atomic.Int64
	delay     map[string]time.Duration
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	f := &fixtureServer{t: t, hits: map[string]int{}, bodies: map[string][]byte{},
		redirects: map[string]string{}, rawQuery: map[string]string{}, delay: map[string]time.Duration{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cur := f.cur.Add(1)
		for {
			peak := f.peak.Load()
			if cur <= peak || f.peak.CompareAndSwap(peak, cur) {
				break
			}
		}
		defer f.cur.Add(-1)
		f.mu.Lock()
		f.hits[r.URL.Path]++
		f.rawQuery[r.URL.Path] = r.URL.RawQuery
		if d, ok := f.delay[r.URL.Path]; ok {
			f.mu.Unlock()
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
			f.mu.Lock()
		}
		if loc, ok := f.redirects[r.URL.Path]; ok {
			f.mu.Unlock()
			http.Redirect(w, r, loc, http.StatusFound)
			return
		}
		body, ok := f.bodies[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixtureServer) url(path string) string { return f.srv.URL + path }

func (f *fixtureServer) set(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies[path] = body
}

func (f *fixtureServer) hitsOf(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fixtureServer) redirect(path, location string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redirects[path] = location
}

func (f *fixtureServer) queryOf(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rawQuery[path]
}

func headlessShellPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("T16_CHROME_BIN"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir for headless-shell lookup")
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-mac-arm64/chrome-headless-shell"))
	if len(matches) == 0 {
		// Linux layout (T29 runner).
		matches, _ = filepath.Glob(filepath.Join(home, ".cache/ms-playwright/chromium_headless_shell-*/chrome-linux/headless_shell"))
	}
	if len(matches) == 0 {
		t.Skip("pinned headless shell not installed; run the T02 install step")
	}
	return matches[0]
}

func pythonPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("T16_PYTHON_BIN"); p != "" {
		return p
	}
	for _, p := range []string{"/usr/bin/python3", "/opt/homebrew/bin/python3"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	t.Skip("no python3 found")
	return ""
}

func requireSandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("seatbelt confinement is darwin-only (T22 owns Linux)")
	}
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
}
