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
	cases := []struct {
		name       string
		line       string
		kind, tool string
		step, done bool
		fatal      bool
	}{
		{"terminal completed", line("run.terminal.completed", `{"kind":"run_terminal","terminal":"completed"}`), "completed", "", false, true, false},
		{"terminal failed", line("run.terminal.failed", `{"kind":"run_terminal","terminal":"failed","reason":"boom"}`), "failed", "", false, true, false},
		{"model step", line("task.lifecycle.completed", `{"task_id":"a","task_kind":"model.response","event":{"kind":"completed"}}`), "", "", true, false, false},
		{"tool started", line("task.lifecycle.started", `{"task_id":"a","task_kind":"tool.public_list_saved","event":{"kind":"started"}}`), "toolCall", "public_list_saved", false, false, false},
		{"tool completed", line("task.lifecycle.completed", `{"task_id":"a","task_kind":"tool.public_list_saved","event":{"kind":"completed"}}`), "toolResult", "public_list_saved", false, false, false},
		{"subagent forbidden", line("task.lifecycle.started", `{"task_id":"a","task_kind":"subagent.spawn","event":{"kind":"started"}}`), "forbidden", "subagent.spawn", false, false, false},
		{"failed tool is a result", line("task.lifecycle.failed", `{"task_id":"a","task_kind":"tool.public_search","event":{"kind":"failed","reason":"denied"}}`), "toolResult", "public_search", false, false, false},
		{"failed session task fatal", line("task.lifecycle.failed", `{"task_id":"a","task_kind":"session.init","event":{"kind":"failed","reason":"auth"}}`), "", "", false, false, true},
		{"reminder failure ignored", line("task.lifecycle.failed", `{"task_id":"a","task_kind":"reminder.agent.x","event":{"kind":"failed","reason":"y"}}`), "", "", false, false, false},
		{"output delta ignored", line("run.output.delta", `{"kind":"run_output_delta","text":"hi"}`), "", "", false, false, false},
		{"non-JSON ignored", "muse: workspace root: /tmp", "", "", false, false, false},
	}
	for _, tc := range cases {
		kind, tool, step, done, fatal := foldExecLine([]byte(tc.line))
		if kind != tc.kind || tool != tc.tool || step != tc.step || done != tc.done || (fatal != "") != tc.fatal {
			t.Errorf("%s: got kind=%q tool=%q step=%v done=%v fatal=%q", tc.name, kind, tool, step, done, fatal)
		}
	}
}

func TestMapToolName(t *testing.T) {
	for in, want := range map[string]string{
		"public_search":                          "public_search",
		"mcp__find-income-public__public_fetch":  "public_fetch",
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

func TestLiveTransportRefusesStandard(t *testing.T) {
	transport := &LiveTransport{CLIPath: "/nonexistent", ModelID: "m", ProviderID: "p",
		Servers: func(string) (*publicresearch.Server, bool) { return nil, false }}
	err := transport.Run(context.Background(), musecode.SessionSpec{},
		musecode.StandardInput{Purpose: "x"}, musecode.Cursor{}, &collectSink{})
	if err == nil || !strings.Contains(err.Error(), "contributor discovery only") {
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
	traceFile, err := os.Create(filepath.Join(t.TempDir(), "validate-trace.jsonl"))
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
