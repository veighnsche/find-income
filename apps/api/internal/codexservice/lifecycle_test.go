package codexservice

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type runtimeFixture struct {
	mu               sync.Mutex
	account          bool
	limits           any
	model            string
	effort           string
	calls            map[string]int
	notices          []map[string]any
	logoutFails      bool
	limitFails       bool
	started          chan struct{}
	startOnce        sync.Once
	threadID, turnID string
	historyStatus    string
	completeStatus   string
	badThreadReply   bool
	turnText         string
}

func installRuntime(t *testing.T, s *Service) *runtimeFixture {
	t.Helper()
	f := &runtimeFixture{account: true, model: "test-model", effort: "medium", limits: map[string]any{"rateLimits": nil}, calls: map[string]int{}, started: make(chan struct{}), threadID: "thread-a", turnID: "turn-a"}
	s.dial = func(context.Context, Config) (io.ReadWriteCloser, error) {
		left, right := net.Pipe()
		t.Cleanup(func() { right.Close() })
		go func() {
			defer right.Close()
			reader := bufio.NewReader(right)
			for {
				line, err := reader.ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if json.Unmarshal(line, &req) != nil {
					return
				}
				f.mu.Lock()
				f.calls[req.Method]++
				var result any
				rpcFailure := false
				switch req.Method {
				case "initialized":
					f.mu.Unlock()
					continue
				case "initialize":
					result = map[string]any{"userAgent": "jobseek_dashboard/0.153.4 (Linux; x86_64)", "codexHome": "/runner/state", "platformFamily": "unix", "platformOs": "linux"}
				case "account/read":
					var account any
					if f.account {
						account = map[string]string{"type": "chatgpt"}
					}
					result = map[string]any{"account": account, "requiresOpenaiAuth": true}
				case "account/login/start":
					result = map[string]string{"type": "chatgptDeviceCode", "loginId": "attempt-a", "verificationUrl": "https://auth.openai.com/codex/device", "userCode": "SYNTHETIC"}
				case "account/login/cancel":
					result = map[string]string{"status": "canceled"}
				case "account/logout":
					rpcFailure = f.logoutFails
					if !rpcFailure {
						f.account = false
					}
					result = map[string]any{}
				case "model/list":
					result = map[string]any{"data": []any{map[string]any{"model": f.model, "supportedReasoningEfforts": []any{map[string]string{"reasoningEffort": f.effort}}}}}
				case "account/rateLimits/read":
					rpcFailure = f.limitFails
					result = f.limits
				case "mcpServerStatus/list":
					tools := map[string]any{}
					for _, name := range requiredTools {
						tools[name] = map[string]string{"name": name}
					}
					result = map[string]any{"data": []any{map[string]any{"name": "jobseek", "tools": tools}}}
				case "thread/start":
					threadID := f.threadID
					if f.badThreadReply {
						threadID = ""
					}
					result = map[string]any{"thread": map[string]string{"id": threadID}}
				case "turn/start":
					var turnInput struct {
						Input []struct {
							Text string `json:"text"`
						} `json:"input"`
					}
					if json.Unmarshal(req.Params, &turnInput) == nil && len(turnInput.Input) == 1 {
						f.turnText = turnInput.Input[0].Text
					}
					result = map[string]any{"turn": map[string]string{"id": f.turnID, "status": "inProgress"}}
					if f.completeStatus != "" {
						f.notices = append(f.notices, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": f.threadID, "turn": map[string]string{"id": f.turnID, "status": f.completeStatus}}})
					}
					f.startOnce.Do(func() { close(f.started) })
				case "turn/interrupt":
					result = map[string]any{}
				case "thread/read":
					result = map[string]any{"thread": map[string]any{"id": f.threadID, "turns": []any{map[string]string{"id": f.turnID, "status": f.historyStatus}}}}
				case "thread/turns/list":
					result = map[string]any{"data": []any{map[string]string{"id": f.turnID, "status": f.historyStatus}}}
				default:
					rpcFailure = true
				}
				notices := f.notices
				f.notices = nil
				f.mu.Unlock()
				for _, notice := range notices {
					if json.NewEncoder(right).Encode(notice) != nil {
						return
					}
				}
				response := map[string]any{"id": req.ID, "result": result}
				if rpcFailure {
					delete(response, "result")
					response["error"] = map[string]any{"code": -32600, "message": "synthetic failure"}
				}
				if json.NewEncoder(right).Encode(response) != nil {
					return
				}
			}
		}()
		return left, nil
	}
	return f
}
func (f *runtimeFixture) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}
func (f *runtimeFixture) text() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.turnText
}
func (f *runtimeFixture) loginCompletion(id string, success bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = append(f.notices, map[string]any{"method": "account/login/completed", "params": map[string]any{"loginId": id, "success": success, "error": "do not expose this raw diagnostic"}})
}
func boundedContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func TestCheckRoundRequiresSupportedReadyConnectionWithoutDispatch(t *testing.T) {
	s, _ := testService(t, testConfig())
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	if err := s.CheckRound(ctx, "process_input"); err != nil {
		t.Fatalf("ready process input unavailable: %v", err)
	}
	if err := s.CheckRound(ctx, "discover"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("removed discovery outcome remained available: %v", err)
	}
	if err := s.CheckRound(ctx, "deliver"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsupported outcome: %v", err)
	}
	if f.count("thread/start") != 0 || f.count("turn/start") != 0 {
		t.Fatal("readiness dispatched a model turn")
	}
	f.mu.Lock()
	f.limitFails = true
	f.mu.Unlock()
	if err := s.CheckRound(ctx, "process_input"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unreadable quota passed: %v", err)
	}
}

func TestProcessInputReadinessRequiresRuntimeButNotPackConfiguration(t *testing.T) {
	s, _ := testService(t, testConfig())
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	// The general input path covers profile and vacancy decisions, so Typst
	// and pack source paths are not prerequisites for this outcome.
	if err := s.CheckRound(ctx, "process_input"); err != nil {
		t.Fatalf("ready process input unavailable without pack configuration: %v", err)
	}
	if f.count("thread/start") != 0 || f.count("turn/start") != 0 {
		t.Fatal("process input readiness dispatched a provider turn")
	}
	f.mu.Lock()
	f.limitFails = true
	f.mu.Unlock()
	if err := s.CheckRound(ctx, "process_input"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("process input passed with unavailable quota: %v", err)
	}
	f.mu.Lock()
	f.limitFails = false
	f.account = false
	f.mu.Unlock()
	if err := s.CheckRound(ctx, "process_input"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("process input passed without ChatGPT account: %v", err)
	}
	if f.count("thread/start") != 0 || f.count("turn/start") != 0 {
		t.Fatal("unavailable process input dispatched a provider turn")
	}
}

func submitFixture(t *testing.T, db *store.Store, key string) store.IngestionRequest {
	t.Helper()
	item, _, err := db.SubmitIngestion(context.Background(), store.Actor{Kind: "administrator", ID: "owner"}, store.IngestionInput{Origin: "owner", OriginalText: "Synthetic vacancy " + key, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// Build a terminal historical dispatch directly because the current claimant
// correctly refuses to start standalone recruitment jobs without a round.
func historicalDispatch(t *testing.T, dir string, item store.IngestionRequest, threadID, turnID string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "jobseek.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE jobs SET state='failed' WHERE id=?`, item.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE ingestion_requests SET dispatch_started=1,status='failed',codex_thread_id=?,codex_turn_id=? WHERE id=?`, threadID, turnID, item.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessModelAndUsage(t *testing.T) {
	s, _ := testService(t, testConfig())
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	if status := s.Status(ctx); status.State != "ready" || status.Code != "ready" || !status.Connected || status.IngestionAvailable || status.UsageAvailable || status.Model != "test-model" {
		t.Fatal(status)
	}
	f.mu.Lock()
	f.model = "different-model"
	f.mu.Unlock()
	if status := s.Status(ctx); status.Code != "model_unavailable" || status.IngestionAvailable {
		t.Fatal(status)
	}
	f.mu.Lock()
	f.model = "test-model"
	f.effort = "low"
	f.mu.Unlock()
	if status := s.Status(ctx); status.Code != "effort_unavailable" {
		t.Fatal(status)
	}
	f.mu.Lock()
	f.effort = "medium"
	f.limits = map[string]any{"rateLimitsByLimitId": map[string]any{"codex": map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Now().Add(time.Hour).Unix()}}}}
	f.mu.Unlock()
	if status := s.Status(ctx); status.Code != "usage_exhausted" || status.IngestionAvailable || !status.Connected || !status.UsageAvailable {
		t.Fatal(status)
	}
	f.mu.Lock()
	f.limits = map[string]any{"rateLimitsByLimitId": map[string]any{"codex": map[string]any{"primary": map[string]any{"usedPercent": 12}}, "other": map[string]any{"primary": map[string]any{"usedPercent": 100}}}}
	f.mu.Unlock()
	if status := s.Status(ctx); status.State != "ready" || !status.Connected || status.IngestionAvailable || len(status.Usage) != 2 {
		t.Fatal(status)
	}
	f.mu.Lock()
	f.limitFails = true
	f.mu.Unlock()
	if status := s.Status(ctx); status.Code != "usage_unavailable" || status.IngestionAvailable {
		t.Fatal(status)
	}
	f.mu.Lock()
	f.limitFails = false
	f.limits = map[string]any{"rateLimitsByLimitId": map[string]any{"other": map[string]any{"primary": map[string]any{"usedPercent": 20}}}}
	f.mu.Unlock()
	if status := s.Status(ctx); status.State != "ready" || !status.Connected || status.IngestionAvailable || status.UsageAvailable {
		t.Fatal("unrelated usage bucket misrepresented as Codex capacity", status)
	}
}

func TestLoginCompletionMustMatchAndCancellationLogsOut(t *testing.T) {
	s, _ := testService(t, testConfig())
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	f.mu.Lock()
	f.account = false
	f.mu.Unlock()
	first, err := s.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Connect(ctx)
	if err != nil || first != again || f.count("account/login/start") != 1 {
		t.Fatal("duplicate login", err)
	}
	f.mu.Lock()
	f.account = true
	f.mu.Unlock()
	f.loginCompletion("old-attempt", true)
	if status := s.Status(ctx); status.State != "connecting" || status.Connected {
		t.Fatal("accepted unrelated completion", status)
	}
	f.loginCompletion("attempt-a", true)
	if status := s.Status(ctx); status.State != "ready" || !status.Connected || status.IngestionAvailable {
		t.Fatal(status)
	}
	if err = s.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if status := s.Status(ctx); status.State != "needs_sign_in" || status.Connected {
		t.Fatal(status)
	}
	if _, err = s.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelConnect(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.account = true
	f.mu.Unlock() // stale remote token appearance cannot revive a cancelled attempt
	f.loginCompletion("attempt-a", true)
	if status := s.Status(ctx); status.State != "needs_sign_in" || status.Connected {
		t.Fatal(status)
	}
	if f.count("account/logout") != 3 {
		t.Fatal("late token was not revoked")
	}
}

func TestLoginFailureAndExpiryAreSafe(t *testing.T) {
	for _, failure := range []bool{true, false} {
		t.Run(map[bool]string{true: "failure", false: "expiry"}[failure], func(t *testing.T) {
			s, _ := testService(t, testConfig())
			f := installRuntime(t, s)
			ctx := boundedContext(t)
			f.mu.Lock()
			f.account = false
			f.mu.Unlock()
			if _, err := s.Connect(ctx); err != nil {
				t.Fatal(err)
			}
			want := "login_attempt_failed"
			if failure {
				f.loginCompletion("attempt-a", false)
			} else {
				s.mu.Lock()
				s.loginAt = time.Now().Add(-11 * time.Minute)
				s.mu.Unlock()
				want = "login_attempt_timed_out"
			}
			status := s.Status(ctx)
			if status.Code != want || status.IngestionAvailable {
				t.Fatal(status)
			}
			data, _ := json.Marshal(status)
			if strings.Contains(string(data), "diagnostic") {
				t.Fatal("raw event leaked")
			}
		})
	}
}

func TestLostLoginGenerationRequiresRemoteAccountCleanup(t *testing.T) {
	s, _ := testService(t, testConfig())
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	f.mu.Lock()
	f.account = false
	f.mu.Unlock()
	if _, err := s.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	old := s.client
	oldGeneration := s.loginGeneration
	s.mu.Unlock()
	_ = old.Close()
	f.mu.Lock()
	f.account = true // the lost generation could have saved authentication
	f.mu.Unlock()
	if status := s.Status(ctx); status.Code != "disconnect_pending" || status.IngestionAvailable {
		t.Fatal("lost login was accepted", status)
	}
	if _, err := s.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	newGeneration := s.loginGeneration
	s.mu.Unlock()
	if oldGeneration == newGeneration || f.count("account/logout") != 1 {
		t.Fatal("replacement generation did not clear prior login")
	}
	f.loginCompletion("unrelated-login", true)
	if status := s.Status(ctx); status.State != "connecting" || status.IngestionAvailable {
		t.Fatal("unrelated completion was accepted", status)
	}
}

func TestDisconnectFailureRemainsBlockedAndRetrySettles(t *testing.T) {
	s, db := testService(t, testConfig())
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	item := submitFixture(t, db, "pending")
	f.mu.Lock()
	f.logoutFails = true
	f.mu.Unlock()
	if err := s.Disconnect(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if status := s.Status(ctx); status.Code != "disconnect_pending" || status.IngestionAvailable {
		t.Fatal(status)
	}
	f.mu.Lock()
	f.logoutFails = false
	f.mu.Unlock()
	if err := s.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := db.Ingestion(ctx, item.ID)
	job, _ := db.Job(ctx, item.JobID)
	if current.JobState != store.JobQueued || job.AttemptCount != 0 {
		t.Fatal(current, job)
	}
}

func TestDisconnectCancelsOwnedRunAndPreservesPending(t *testing.T) {
	s, db := testService(t, testConfig())
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	pending := submitFixture(t, db, "pending")
	if status := s.Status(ctx); status.State != "ready" || !status.Connected || status.IngestionAvailable {
		t.Fatal(status)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	run := &activeRun{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.run, s.busy = run, true
	s.mu.Unlock()
	go func() { <-runCtx.Done(); s.mu.Lock(); s.run, s.busy = nil, false; close(run.done); s.mu.Unlock() }()
	if err := s.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if runCtx.Err() == nil || f.count("account/logout") != 1 {
		t.Fatal("run remained live or account remained signed in")
	}
	job, _ := db.Job(ctx, pending.JobID)
	if job.State != store.JobQueued || job.AttemptCount != 0 {
		t.Fatal(job)
	}
}

func TestUsageProjectionPreservesUnknownAndRejectsInvalid(t *testing.T) {
	negative := -1.0
	zero := 0.0
	hundred := 100.0
	out, blocked := projectUsage(codex.RateLimits{RateLimits: &codex.LimitSnapshot{Primary: &codex.LimitWindow{UsedPercent: &negative}}}, time.Now())
	if len(out) != 0 || blocked {
		t.Fatal(out, blocked)
	}
	out, blocked = projectUsage(codex.RateLimits{RateLimits: &codex.LimitSnapshot{Primary: &codex.LimitWindow{UsedPercent: &zero}, Secondary: &codex.LimitWindow{UsedPercent: &hundred}}}, time.Now())
	if len(out) != 2 || !blocked || out[0].ResetsAt != nil {
		t.Fatal(out, blocked)
	}
}

func TestRetryRequiresCorrelatedTerminalHistory(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), db, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); db.Close() })
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	actor := store.Actor{Kind: "administrator", ID: "owner"}
	item := submitFixture(t, db, "history")
	historicalDispatch(t, dir, item, f.threadID, f.turnID)
	if _, err = db.RetryIngestion(ctx, actor, item.ID, nil); !errors.Is(err, store.ErrUncertain) {
		t.Fatal("store bypassed uncertain dispatch", err)
	}
	f.mu.Lock()
	f.historyStatus = "inProgress"
	f.mu.Unlock()
	if _, err = s.RetryIngestion(ctx, actor, item.ID, nil); !errors.Is(err, store.ErrUncertain) {
		t.Fatal("running turn retried", err)
	}
	current, _ := db.Ingestion(ctx, item.ID)
	if current.JobID != item.JobID || !current.DispatchStarted {
		t.Fatal("uncertain IDs lost")
	}
	f.mu.Lock()
	f.historyStatus = "failed"
	f.mu.Unlock()
	if _, err = s.RetryIngestion(ctx, store.Actor{Kind: "agent", ID: "other"}, item.ID, nil); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("unauthorized retry", err)
	}
	retried, err := s.RetryIngestion(ctx, actor, item.ID, nil)
	if err != nil || retried.JobID == item.JobID || retried.DispatchStarted {
		t.Fatal(retried, err)
	}
	// T06 §5 observation contract: ObserveTurn is turn-list-authoritative.
	// Both reconciliations observe through thread/turns/list only; no
	// thread/read identity check is made. Gating semantics are unchanged:
	// inProgress refuses, terminal allows.
	if f.count("thread/read") != 0 || f.count("thread/turns/list") != 2 {
		t.Fatal("unexpected history observations")
	}
}

func TestRetryWithoutRemoteIDsRemainsUncertain(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), db, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); db.Close() })
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	actor := store.Actor{Kind: "administrator", ID: "owner"}
	item := submitFixture(t, db, "lost-ack")
	historicalDispatch(t, dir, item, "", "")
	if _, err = s.RetryIngestion(ctx, actor, item.ID, nil); !errors.Is(err, store.ErrUncertain) {
		t.Fatal("missing IDs retried", err)
	}
	if f.count("thread/read") != 0 || f.count("thread/turns/list") != 0 {
		t.Fatal("guessed remote identity")
	}
}
