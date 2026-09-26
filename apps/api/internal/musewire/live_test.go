package musewire

// Fixtures for the live exec transport. Parser and mapping checks run
// ungated on synthetic lines. TestLiveTransportEchoPlumbing drives the
// real installed CLI with the deterministic echo provider: zero model
// spend, zero credentials, localhost MCP only. It runs only with
// E11_ECHO=1. TestLiveTransportValidationTurn drives one trivial turn
// on the Contributor lane and runs only with E11_VALIDATE=1.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

func liveFixtureBounds() musecode.Bounds {
	return musecode.Bounds{MaxWallClock: time.Minute, MaxModelSteps: 10,
		MaxToolCalls: 50, MaxBytesPerOp: 1 << 20, MaxBytesTotal: 10 << 20}
}

func liveFixtureServer(t *testing.T) *publicresearch.Server {
	t.Helper()
	server, err := publicresearch.NewServer(publicresearch.Deps{
		Executor: &fakeExecutor{}, Captures: &fakeCaptures{
			receipts: map[string]researchcontract.ExecutionReceipt{},
			blobs:    map[string][]byte{},
		}, Bounds: liveFixtureBounds(), RunID: "round-fixture", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

type collectSink struct {
	mu     sync.Mutex
	events []musecode.Event
}

func (s *collectSink) Emit(event musecode.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *collectSink) kinds() []musecode.EventKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := make([]musecode.EventKind, 0, len(s.events))
	for _, event := range s.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func TestFoldExecLine(t *testing.T) {
	line := func(payloadType, payload string) string {
		return fmt.Sprintf(`{"payload_type":%q,"payload":%s}`, payloadType, payload)
	}
	propose := func(id, kind string) string {
		return line("task.lifecycle.proposed", fmt.Sprintf(`{"task_id":%q,"event":{"kind":"proposed","task_kind":%q}}`, id, kind))
	}
	lifecycle := func(payloadType, id string) string {
		return line(payloadType, fmt.Sprintf(`{"task_id":%q,"event":{"kind":"x"}}`, id))
	}
	type expectation struct {
		kind, tool string
		bytes      int64
		step, done bool
		fatal      bool
	}
	cases := []struct {
		name   string
		lines  []string
		expect []expectation
	}{
		{"terminal completed",
			[]string{line("run.terminal.completed", `{"kind":"run_terminal","terminal":"completed"}`)},
			[]expectation{{kind: "completed", done: true}}},
		{"terminal failed",
			[]string{line("run.terminal.failed", `{"kind":"run_terminal","terminal":"failed","reason":"boom"}`)},
			[]expectation{{kind: "failed", done: true}}},
		{"route enforced",
			[]string{line("run.model.configured", `{"model_id":"other","provider_id":"meta"}`)},
			[]expectation{{fatal: true}}},
		{"route pinned passes",
			[]string{line("run.model.configured", `{"model_id":"fixture-model","provider_id":"meta"}`)},
			[]expectation{{}}},
		{"model step",
			[]string{propose("m1", "model.meta.response"), lifecycle("task.lifecycle.completed", "m1")},
			[]expectation{{}, {step: true}}},
		{"tool call and result",
			[]string{
				propose("t1", "tool.mcp__find_income_public__public_list_saved"),
				lifecycle("task.lifecycle.started", "t1"),
				line("tool.result", `{"text":"{}","correlation_facts":{"tool_name":"mcp__find_income_public__public_list_saved","outcome":"success"}}`),
				lifecycle("task.lifecycle.completed", "t1"),
			},
			[]expectation{{}, {kind: "toolCall", tool: "mcp__find_income_public__public_list_saved"},
				{kind: "toolResult", tool: "mcp__find_income_public__public_list_saved", bytes: 2},
				{kind: "toolResult", tool: "mcp__find_income_public__public_list_saved"}}},
		{"subagent forbidden",
			[]string{propose("s1", "subagent.spawn"), lifecycle("task.lifecycle.started", "s1")},
			[]expectation{{}, {kind: "forbidden", tool: "subagent.spawn"}}},
		{"failed tool is a result",
			[]string{propose("t2", "tool.mcp__find_income_public__public_search"), lifecycle("task.lifecycle.failed", "t2")},
			[]expectation{{}, {kind: "toolResult", tool: "mcp__find_income_public__public_search"}}},
		{"failed session task fatal",
			[]string{propose("x1", "session.init"), lifecycle("task.lifecycle.failed", "x1")},
			[]expectation{{}, {fatal: true}}},
		{"reminder failure ignored",
			[]string{propose("r1", "reminder.agent.x"), lifecycle("task.lifecycle.failed", "r1")},
			[]expectation{{}, {}}},
		{"unknown task ignored",
			[]string{lifecycle("task.lifecycle.started", "ghost")},
			[]expectation{{}}},
		{"output delta ignored",
			[]string{line("run.output.delta", `{"kind":"run_output_delta","text":"hi"}`)},
			[]expectation{{}}},
		{"non-JSON ignored",
			[]string{"muse: workspace root: /tmp"},
			[]expectation{{}}},
	}
	for _, tc := range cases {
		folder := newExecFolder("fixture-model", "meta")
		for i, raw := range tc.lines {
			kind, tool, bytes, step, done, fatal := folder.fold([]byte(raw))
			want := tc.expect[i]
			if kind != want.kind || tool != want.tool || bytes != want.bytes || step != want.step || done != want.done || (fatal != "") != want.fatal {
				t.Errorf("%s line %d: got kind=%q tool=%q bytes=%d step=%v done=%v fatal=%q",
					tc.name, i, kind, tool, bytes, step, done, fatal)
			}
		}
	}
}

func TestExitDetailKeepsRecordedCause(t *testing.T) {
	waitErr := errors.New("exit status 143")
	if got := exitDetail("retrieval fetch bound 12 exceeded", "", waitErr); got != "retrieval fetch bound 12 exceeded" {
		t.Errorf("bound detail = %q, want the recorded bound", got)
	}
	if got := exitDetail("", "", waitErr); got != "host exit: exit status 143" {
		t.Errorf("unexplained exit = %q, want host exit", got)
	}
	if got := exitDetail("", "completed", waitErr); got != "" {
		t.Errorf("clean terminal = %q, want empty", got)
	}
	if got := exitDetail("", "", nil); got != "" {
		t.Errorf("quiet end = %q, want empty", got)
	}
}

func TestMapToolName(t *testing.T) {
	for in, want := range map[string]string{
		"public_search":                          "public_search",
		"mcp__find_income_public__public_fetch":  "public_fetch",
		"find-income-public.public_save_vacancy": "public_save_vacancy",
		"shell":                                  "shell",
		"mcp__other__public_search":              "mcp__other__public_search",
	} {
		if got := mapToolName(in); got != want {
			t.Errorf("map %q = %q, want %q", in, got, want)
		}
	}
}

func TestWriteExecHomeIsolated(t *testing.T) {
	workspace := t.TempDir()
	home, err := writeExecHome(workspace, 18231, "token-fixture")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "config", "muse", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Model      string `json:"model"`
		MCPServers map[string]struct {
			Transport string            `json:"transport"`
			URL       string            `json:"url"`
			Headers   map[string]string `json:"headers"`
			Mode      string            `json:"mode"`
		} `json:"mcp_servers"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	entry, ok := settings.MCPServers[mcpServerName]
	if !ok || entry.Transport != "streamable_http" || entry.Mode != "required" {
		t.Fatalf("settings = %s, want required streamable server", raw)
	}
	if entry.URL != "http://127.0.0.1:18231/mcp" || entry.Headers["Authorization"] != "Bearer token-fixture" {
		t.Fatalf("settings = %s, want rendered loopback URL and bearer token", raw)
	}
	if len(settings.MCPServers) != 1 {
		t.Fatalf("settings declare %d servers, want exactly 1", len(settings.MCPServers))
	}
	info, err := os.Stat(filepath.Join(home, "config", "muse", "settings.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings perms = %v, want 0600", info)
	}
}

func TestResumeContinuationListsPriorOpenings(t *testing.T) {
	server, err := publicresearch.NewServer(publicresearch.Deps{
		Executor: &fakeExecutor{},
		Captures: &fakeCaptures{
			receipts: map[string]researchcontract.ExecutionReceipt{
				"rc-1": {ID: "rc-1", Status: researchcontract.ReceiptOK, CaptureID: "cap-1"},
			},
			blobs: map[string][]byte{},
		},
		Bounds: liveFixtureBounds(), RunID: "round-fixture", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := server.SeedVacancies([]publicresearch.SeedVacancy{{
		PageURL: "https://jobs.example.invalid/1", EmployerName: "Example BV",
		Title: "Senior support engineer", ReceiptRef: "rc-1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := resumeContinuation(server, musecode.Cursor{RunRef: "run-1",
		SavedRefs: []string{seeded[0].VacancyRef, "q-absent"}})
	for _, want := range []string{"CONTINUES an earlier session", "already saved 1 openings",
		"https://jobs.example.invalid/1", "Example BV", "Senior support engineer",
		"1 earlier saves are no longer listed here", "public_list_saved"} {
		if !strings.Contains(text, want) {
			t.Errorf("continuation misses %q:\n%s", want, text)
		}
	}
	empty := resumeContinuation(liveFixtureServer(t), musecode.Cursor{RunRef: "run-1", SavedRefs: []string{"vac-gone"}})
	if !strings.Contains(empty, "already saved 0 openings") || !strings.Contains(empty, "public_list_saved") {
		t.Errorf("empty continuation = %q", empty)
	}
}

func TestCheckPromptContractsAdapterSchema(t *testing.T) {
	prompt := checkPrompt(musecode.CheckInput{VacancyRef: "vac-1",
		PageURL: "https://jobs.example.invalid/1", ReceiptRef: "rc-1"})
	for _, want := range []string{"vac-1", "https://jobs.example.invalid/1", "rc-1",
		"public_fetch", "public_save_question", "Never save vacancies",
		`"requirements"`, `"route"`, `"documents"`, `"questions"`,
		`"capture"`, "copied exactly", "never invent"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("check prompt misses %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "public_save_vacancy") || strings.Contains(prompt, "public_search") {
		t.Errorf("check prompt must not offer discovery tools:\n%s", prompt)
	}
}

func TestLiveTransportRefusesStandard(t *testing.T) {
	transport := &LiveTransport{CLIPath: "/nonexistent", ModelID: "m", ProviderID: "p",
		Servers: func(string) (*publicresearch.Server, bool) { return nil, false }}
	err := transport.Run(context.Background(), musecode.SessionSpec{},
		musecode.StandardInput{Purpose: "x"}, musecode.Cursor{}, &collectSink{})
	if err == nil || !strings.Contains(err.Error(), "contributor discovery and checks only") {
		t.Fatalf("standard err = %v, want contributor-only refusal", err)
	}
}

func TestLiveTransportEchoPlumbing(t *testing.T) {
	if os.Getenv("E11_ECHO") != "1" {
		t.Skip("echo plumbing only with E11_ECHO=1")
	}
	cli, err := exec.LookPath("muse")
	if err != nil {
		t.Skip("muse CLI not on PATH")
	}
	workspace := filepath.Join(t.TempDir(), "ws", "run-live-echo")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	server := liveFixtureServer(t)
	var trace strings.Builder
	transport := &LiveTransport{CLIPath: cli, ModelID: "muse-spark-1.3-contributor",
		ProviderID: "meta", Provider: "echo",
		Servers: func(string) (*publicresearch.Server, bool) { return server, true },
		Trace:   &trace}
	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := transport.Run(ctx, musecode.SessionSpec{Tier: musecode.TierContributor,
		Workspace: workspace, Public: true, Bounds: liveFixtureBounds()},
		musecode.PublicInput{Criteria: musecode.PublicCriteria{RoleKeywords: []string{"support"}}},
		musecode.Cursor{}, sink); err != nil {
		t.Fatalf("echo run: %v", err)
	}
	found := false
	for _, event := range sink.kinds() {
		if event == musecode.EventFinished {
			found = true
		}
	}
	if !found {
		t.Errorf("no finished event; kinds=%v", sink.kinds())
	}
	if !strings.Contains(trace.String(), "run.terminal.completed") {
		t.Errorf("trace misses terminal completion")
	}
	// Without a configured sink the transport persists the exec JSONL to
	// the run workspace, so failed runs stay debuggable after exit.
	fallbackWorkspace := filepath.Join(t.TempDir(), "ws", "run-live-echo-fallback")
	if err := os.MkdirAll(fallbackWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	fallback := &LiveTransport{CLIPath: cli, ModelID: "muse-spark-1.3-contributor",
		ProviderID: "meta", Provider: "echo",
		Servers: func(string) (*publicresearch.Server, bool) { return server, true }}
	fallbackSink := &collectSink{}
	if err := fallback.Run(ctx, musecode.SessionSpec{Tier: musecode.TierContributor,
		Workspace: fallbackWorkspace, Public: true, Bounds: liveFixtureBounds()},
		musecode.PublicInput{Criteria: musecode.PublicCriteria{RoleKeywords: []string{"support"}}},
		musecode.Cursor{}, fallbackSink); err != nil {
		t.Fatalf("fallback echo run: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(fallbackWorkspace, "trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "run.terminal.completed") {
		t.Errorf("workspace trace misses terminal completion")
	}
}

// TestLiveTransportValidationTurn drives one trivial turn against the real
// installed CLI. It runs only with E11_VALIDATE=1, costs one minimal turn
// on the Contributor subscription lane, performs zero retrieval, and
// exists to prove MCP negotiation, tool-name mapping and the approval
// default before the E11 discovery run.
func TestLiveTransportValidationTurn(t *testing.T) {
	if os.Getenv("E11_VALIDATE") != "1" {
		t.Skip("live validation only with E11_VALIDATE=1")
	}
	cli, err := exec.LookPath("muse")
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "ws", "run-live-validate")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	server := liveFixtureServer(t)
	traceDir := os.Getenv("E11_TRACE_DIR")
	if traceDir == "" {
		traceDir = t.TempDir()
	} else if err := os.MkdirAll(traceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	traceFile, err := os.Create(filepath.Join(traceDir, "validate-trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer traceFile.Close()
	t.Logf("trace: %s", traceFile.Name())
	transport := &LiveTransport{CLIPath: cli, ModelID: "muse-spark-1.3-contributor",
		ProviderID: "meta",
		Servers:    func(string) (*publicresearch.Server, bool) { return server, true },
		Trace:      traceFile,
		ValidationPrompt: "Call public_list_saved exactly once with no arguments, then reply with the single word done. " +
			"Use no other tool, skill, shell, memory, subagent, or background work."}
	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := transport.Run(ctx, musecode.SessionSpec{Tier: musecode.TierContributor,
		Workspace: workspace, Public: true, Bounds: liveFixtureBounds()},
		musecode.PublicInput{Criteria: musecode.PublicCriteria{RoleKeywords: []string{"support"}}},
		musecode.Cursor{}, sink); err != nil {
		t.Fatalf("validation turn: %v", err)
	}
	calls := 0
	finished := false
	for _, event := range sink.events {
		switch event.Kind {
		case musecode.EventToolCall:
			calls++
			if event.Tool != publicresearch.ToolListSaved {
				t.Errorf("tool call = %q, want %q", event.Tool, publicresearch.ToolListSaved)
			}
		case musecode.EventFinished:
			finished = true
		}
	}
	if calls != 1 {
		t.Errorf("tool calls = %d, want exactly 1", calls)
	}
	if !finished {
		t.Error("no finished event")
	}
}
