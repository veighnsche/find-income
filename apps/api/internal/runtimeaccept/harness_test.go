package runtimeaccept

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// scriptedProvider is a zero-spend jevassess.Provider: verdicts are scripted
// per question id. It pins the binding machinery, not provider evidence.
type scriptedProvider struct {
	model    string
	verdicts map[string]string
	calls    int
}

func (p *scriptedProvider) EncodedRequest(r jev.Request) ([]byte, error) {
	return json.Marshal(r.State)
}

func (p *scriptedProvider) RequestedModel() string { return p.model }

func (p *scriptedProvider) EvaluateOnceCaptured(_ context.Context, r jev.Request) (jev.Result, jev.CapturedExchange, error) {
	p.calls++
	encoded, _ := json.Marshal(r.State)
	out := jev.Result{
		RequestedModel: p.model, ReturnedModel: p.model,
		Answers:     map[string]jev.Answer{},
		Usage:       jev.Usage{InputTokens: 10, OutputTokens: 5},
		RawResponse: json.RawMessage(`{"model":"fixture"}`),
	}
	for id := range r.Questions {
		choice, ok := p.verdicts[id]
		if !ok {
			choice = "abstain"
		}
		out.Answers[id] = jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{
			Choice: choice, Probabilities: map[string]float64{choice: 1}, Confidence: 0.9}}
	}
	return out, jev.CapturedExchange{RequestBytes: encoded, ResponseBytes: []byte("{}"),
		HTTPStatus: 200, ReturnedModel: p.model}, nil
}

var _ jevassess.Provider = (*scriptedProvider)(nil)

// gate blocks fixture handlers until the test releases them, so Stop and
// crash can land deterministically mid-flight. Handlers also unblock on
// client disconnect (Stop cancels the caller context) and on a fail-safe
// timeout, so a test bug fails loudly instead of hanging the suite.
type gate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	hits    atomic.Int64
}

func newGate() *gate { return &gate{entered: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) handler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.hits.Add(1)
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.release:
		case <-r.Context().Done():
		case <-time.After(60 * time.Second):
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body><p>"+body+"</p></body></html>")
	}
}

func (g *gate) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(60 * time.Second):
		t.Fatal("gated request never arrived")
	}
}

// counter serves fixed bodies while counting hits per path.
type counter struct {
	mu   sync.Mutex
	hits map[string]int64
}

func newCounter() *counter { return &counter{hits: map[string]int64{}} }

func (c *counter) handler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.hits[r.URL.Path]++
		c.mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body><p>"+body+"</p></body></html>")
	}
}

func (c *counter) get(path string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

func fixtureServer(t *testing.T, mux *http.ServeMux) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type harness struct {
	ctx      context.Context
	dir      string
	db       *store.Store
	stack    *researchwire.Stack
	owner    store.Actor
	agent    store.Actor
	provider *scriptedProvider
	runID    string
	profile  int64
	rubric   string
	scratch  string
}

func wireStack(t *testing.T, ctx context.Context, db *store.Store, dir string, provider *scriptedProvider, mutate func(*researchwire.Config)) *researchwire.Stack {
	t.Helper()
	cfg := researchwire.Config{
		ArtifactRoot:   filepath.Join(dir, "research-artifacts"),
		ScratchRoot:    filepath.Join(dir, "scratch"),
		AgentID:        researchwire.DefaultAgentID,
		PermitLoopback: true, // test-only: controlled fixture servers
		JevProvider:    provider,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	stack, err := researchwire.Wire(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return stack
}

// newHarness wires one isolated stack and commissions one run through the
// real research service adapter. A nil allowance selects the canary
// defaults; mutate adjusts the wire config (executor binaries) before Wire.
func newHarness(t *testing.T, allow *generated.ResearchAllowance, brief, key string, mutate func(*researchwire.Config)) *harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	provider := &scriptedProvider{model: "fixture-jev-1", verdicts: map[string]string{}}
	stack := wireStack(t, ctx, db, dir, provider, mutate)
	t.Cleanup(stack.Supervisor.Close)
	h := &harness{ctx: ctx, dir: dir, db: db, stack: stack,
		owner:    researchwire.OwnerActor(),
		agent:    store.Actor{Kind: "agent", ID: researchwire.DefaultAgentID},
		provider: provider, scratch: filepath.Join(dir, "scratch"),
	}
	out, err := stack.Research.CommissionResearch(ctx, httpapi.CommissionResearchInput{
		Actor: h.owner, BriefText: brief, Allowance: allow, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || out.View.RunId == "" {
		t.Fatalf("commission: %+v", out)
	}
	h.runID = out.View.RunId
	h.profile = int64(out.View.BriefVersion.ProfileVersion)
	h.rubric = out.View.BriefVersion.RubricVersion
	return h
}

func (h *harness) generation(t *testing.T) int64 {
	t.Helper()
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	return round.Generation
}

func (h *harness) round(t *testing.T) store.Round {
	t.Helper()
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	return round
}

func (h *harness) ledger(t *testing.T) researchcontract.UsageLedger {
	t.Helper()
	ledger, err := h.stack.Supervisor.Usage(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func standardBounds() researchcontract.Bounds {
	return researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000}
}

func fetchRequest(url string) researchcontract.RequestDescriptor {
	return researchcontract.RequestDescriptor{
		Operation: researchcontract.OperationFetch, Backend: "generic-http",
		Method: "GET", URLOrQuery: url,
	}
}

func (h *harness) dispatch(t *testing.T, key string, kind researchcontract.ExecuteKind, req researchcontract.RequestDescriptor) rounds.DispatchOutput {
	t.Helper()
	out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: kind, Request: req,
		Bounds: standardBounds(), IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) captureBytes(t *testing.T, capID string) string {
	t.Helper()
	_, rc, err := h.stack.Captures.OpenCapture(h.ctx, capID)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (h *harness) journal(t *testing.T) []researchcontract.Event {
	t.Helper()
	var all []researchcontract.Event
	cursor := ""
	for {
		events, next, err := h.stack.Journal.List(h.ctx, h.runID, cursor, 500)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, events...)
		if next == "" {
			return all
		}
		cursor = next
	}
}

func journalKinds(events []researchcontract.Event) map[string]int {
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	return kinds
}

func contractErr(t *testing.T, err error) *researchcontract.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected a contract error, got nil")
	}
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *researchcontract.Error, got %T (%v)", err, err)
	}
	return cerr
}

func awaitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(60 * time.Second):
		t.Fatal("background call never settled")
		return nil
	}
}

func spanOf(t *testing.T, body, needle string) (int64, int64) {
	t.Helper()
	at := strings.Index(body, needle)
	if at < 0 {
		t.Fatalf("needle %q missing", needle)
	}
	return int64(at), int64(at + len(needle))
}

func excerptSHA(body string, start, end int) string {
	sum := sha256.Sum256([]byte(body[start:end]))
	return hex.EncodeToString(sum[:])
}

// scriptRunner is a zero-spend rounds.TurnRunner: no model, no transport.
// When release is set the turn waits for it (ignoring cancellation when
// ignoreCancel is set, so late completions can be driven past the fence).
type scriptRunner struct {
	mu           sync.Mutex
	entered      chan struct{}
	once         sync.Once
	release      chan struct{}
	ignoreCancel bool
	status       string
	threadID     string
	turnID       string
	nextWork     []string
	calls        []rounds.RunnerTurnInput
}

func newScriptRunner(threadID, turnID string) *scriptRunner {
	return &scriptRunner{status: "completed", threadID: threadID, turnID: turnID,
		entered: make(chan struct{})}
}

func (r *scriptRunner) RunTurn(ctx context.Context, _ store.Actor, in rounds.RunnerTurnInput) (rounds.RunnerTurnOutput, error) {
	r.mu.Lock()
	r.calls = append(r.calls, in)
	release, ignoreCancel := r.release, r.ignoreCancel
	r.mu.Unlock()
	r.once.Do(func() { close(r.entered) })
	if release != nil {
		if ignoreCancel {
			select {
			case <-release:
			case <-time.After(60 * time.Second):
				return rounds.RunnerTurnOutput{}, errors.New("scriptRunner: release never closed")
			}
		} else {
			select {
			case <-release:
			case <-ctx.Done():
				return rounds.RunnerTurnOutput{}, ctx.Err()
			case <-time.After(60 * time.Second):
				return rounds.RunnerTurnOutput{}, errors.New("scriptRunner: release never closed")
			}
		}
	}
	return rounds.RunnerTurnOutput{ThreadID: r.threadID, TurnID: r.turnID,
		Status: r.status, NextWork: r.nextWork}, nil
}

func (r *scriptRunner) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-r.entered:
	case <-time.After(60 * time.Second):
		t.Fatal("turn never started")
	}
}

var _ rounds.TurnRunner = (*scriptRunner)(nil)

// scriptObserver is a rounds.Reconciler double: the verdict source is the
// only simulated part of the resume path. Production binds codexservice;
// T24 finding F1 records that it cannot verify research attempts.
type scriptObserver struct {
	mu       sync.Mutex
	state    store.RoundAttemptState
	evidence json.RawMessage
	calls    []string
}

func (o *scriptObserver) ObserveDispatch(_ context.Context, attemptID string) (rounds.Observation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, attemptID)
	return rounds.Observation{State: o.state, Evidence: o.evidence}, nil
}

var _ rounds.Reconciler = (*scriptObserver)(nil)

func headlessShellPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("T23_CHROME_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Skip("T23_CHROME_PATH points at a missing binary")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir for headless-shell lookup")
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-mac-arm64/chrome-headless-shell"))
	if len(matches) == 0 {
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
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Skip("T16_PYTHON_BIN points at a missing binary")
	}
	for _, p := range []string{"/usr/bin/python3", "/opt/homebrew/bin/python3"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no python3 found")
	return ""
}

func requireSandbox(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable; seatbelt confinement is darwin-only (T22 owns Linux)")
	}
}
