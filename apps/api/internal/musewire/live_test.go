package musewire

// Provider-disabled protocol fixtures for the live MSP transport. A fake
// `muse serve` host (this test binary re-executed through a shim script)
// speaks scripted NDJSON MSP: no model, no network beyond localhost, no
// credentials. The happy path proves handshake, session route pinning,
// bearer-gated MCP endpoint wiring, namespaced tool mapping and terminal
// folding; the fence path proves a non-allowlisted tool fails the run.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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

// fakeHostShim writes an executable `muse` shim that re-executes this test
// binary as the fake host, ignoring the serve arguments.
func fakeHostShim(t *testing.T, mode, record string) string {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "muse")
	script := "#!/bin/sh\nexec \"$TESTBIN\" -test.run=TestHelperHost -- \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TESTBIN", os.Args[0])
	t.Setenv("GO_TEST_HELPER_MODE", mode)
	t.Setenv("GO_TEST_HELPER_RECORD", record)
	return shim
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

type memoryCursors struct {
	mu      sync.Mutex
	cursors map[string]musecode.Cursor
}

func (m *memoryCursors) SaveCursor(_ context.Context, cursor musecode.Cursor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cursors == nil {
		m.cursors = map[string]musecode.Cursor{}
	}
	m.cursors[cursor.RunRef] = cursor
	return nil
}

func (m *memoryCursors) LoadCursor(_ context.Context, runRef string) (musecode.Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cursor, ok := m.cursors[runRef]
	if !ok {
		return musecode.Cursor{}, fmt.Errorf("no cursor")
	}
	return cursor, nil
}

func liveReadyFacts() musecode.Facts {
	return musecode.Facts{CLIPath: "/fixture/muse", CLIReportVersion: "1.4.0",
		EffectiveModel: "fixture-model", SubscriptionLaneProved: true,
		SessionProtocolProved: true, WorkspaceIsolatedProved: true}
}

func TestLiveTransportHappyPath(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.json")
	shim := fakeHostShim(t, "happy", record)
	workspace := filepath.Join(t.TempDir(), "ws", "run-live-happy")
	server := liveFixtureServer(t)
	var trace strings.Builder
	transport := &LiveTransport{CLIPath: shim, ModelID: "fixture-model", ProviderID: "meta",
		Servers: func(runRef string) (*publicresearch.Server, bool) {
			if runRef != "run-live-happy" {
				return nil, false
			}
			return server, true
		}, Trace: &trace}
	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := transport.Run(ctx, musecode.SessionSpec{Tier: musecode.TierContributor,
		Workspace: workspace, Public: true, Bounds: liveFixtureBounds()},
		musecode.PublicInput{Criteria: musecode.PublicCriteria{RoleKeywords: []string{"support"}}},
		musecode.Cursor{}, sink)
	if err != nil {
		t.Fatalf("run: %v\ntrace:\n%s", err, trace.String())
	}
	kinds := sink.kinds()
	joined := fmt.Sprintf("%v", kinds)
	for _, want := range []musecode.EventKind{musecode.EventModelStep, musecode.EventToolCall,
		musecode.EventToolResult, musecode.EventFinished} {
		found := false
		for _, kind := range kinds {
			if kind == want {
				found = true
			}
		}
		if !found {
			t.Errorf("events %s miss %s", joined, want)
		}
	}
	for _, event := range sink.events {
		if event.Kind == musecode.EventToolCall && event.Tool != publicresearch.ToolListSaved {
			t.Errorf("tool call = %q, want mapped %q", event.Tool, publicresearch.ToolListSaved)
		}
	}
	if !strings.Contains(trace.String(), "run-live-happy") || !strings.Contains(trace.String(), "turn") {
		t.Errorf("trace misses run/turn records:\n%s", trace.String())
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		URL            string   `json:"url"`
		Mode           string   `json:"mode"`
		Transport      string   `json:"transport"`
		HasAuthHeader  bool     `json:"has_auth_header"`
		UnauthRejected bool     `json:"unauth_rejected"`
		AuthPassedGate bool     `json:"auth_passed_gate"`
		Tools          []string `json:"tools"`
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recorded.URL, "http://127.0.0.1:") || recorded.Transport != "streamableHttp" || recorded.Mode != "required" {
		t.Errorf("mcp endpoint = %+v, want loopback streamableHttp required", recorded)
	}
	if !recorded.HasAuthHeader || !recorded.UnauthRejected || !recorded.AuthPassedGate {
		t.Errorf("mcp auth record = %+v, want bearer gate both ways", recorded)
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

func TestLiveTransportFencesForeignTool(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.json")
	shim := fakeHostShim(t, "fence-shell", record)
	workspace := filepath.Join(t.TempDir(), "ws", "run-live-fence")
	server := liveFixtureServer(t)
	var trace strings.Builder
	transport := &LiveTransport{CLIPath: shim, ModelID: "fixture-model", ProviderID: "meta",
		Servers: func(string) (*publicresearch.Server, bool) { return server, true }, Trace: &trace}
	validate := func(ref string) error { return nil }
	supervisor := musecode.NewSupervisor(transport, &memoryCursors{}, validate, "fixture-model")
	status := musecode.Status{Tier: musecode.TierContributor, Available: true, Code: musecode.CodeReady}
	spec, err := musecode.NewSession(status, workspace, liveFixtureBounds(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := supervisor.StartRun(ctx, "run-live-fence", spec,
		musecode.PublicInput{Criteria: musecode.PublicCriteria{RoleKeywords: []string{"support"}}},
		liveReadyFacts()); err != nil {
		t.Fatal(err)
	}
	terminal, ok := supervisor.Result("run-live-fence")
	if !ok {
		t.Fatal("no terminal result")
	}
	if terminal.Outcome != musecode.OutcomeFailed || !strings.Contains(terminal.Detail, "not allowlisted") {
		t.Fatalf("terminal = %+v, want failed/not-allowlisted", terminal)
	}
}

// TestHelperHost is re-executed as the fake `muse serve` process. It is
// never run as a real test; the name only routes the helper flag.
func TestHelperHost(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_MODE") == "" {
		t.Skip("helper only")
	}
	mode := os.Getenv("GO_TEST_HELPER_MODE")
	recordPath := os.Getenv("GO_TEST_HELPER_RECORD")
	stdin := bufio.NewReader(os.Stdin)
	stdout := bufio.NewWriter(os.Stdout)
	defer stdout.Flush()
	emit := func(frame any) {
		raw, _ := json.Marshal(frame)
		stdout.Write(raw)
		stdout.Write([]byte("\n"))
		stdout.Flush()
	}
	toolName := "mcp__find-income-public__public_list_saved"
	if mode == "fence-shell" {
		toolName = "shell"
	}
	for {
		line, err := stdin.ReadBytes('\n')
		if err != nil {
			return
		}
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			continue
		}
		if frame.Method == "" || frame.Method == "initialized" {
			continue
		}
		var id any
		_ = json.Unmarshal(frame.ID, &id)
		reply := func(result any) {
			emit(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		}
		switch frame.Method {
		case "initialize":
			reply(map[string]any{"grantedCapabilities": []string{"sessionMcp"},
				"serverInfo": map[string]any{"name": "muse", "version": "1.4.0"}})
		case "model/list":
			reply(map[string]any{"models": []any{}})
		case "session/start":
			var params struct {
				ModelID    string `json:"modelId"`
				ProviderID string `json:"providerId"`
				Config     struct {
					MCPServers map[string]struct {
						Transport string            `json:"transport"`
						URL       string            `json:"url"`
						Mode      string            `json:"mode"`
						Headers   map[string]string `json:"headers"`
					} `json:"mcpServers"`
				} `json:"config"`
			}
			_ = json.Unmarshal(frame.Params, &params)
			endpoint := params.Config.MCPServers["find-income-public"]
			record := map[string]any{"url": endpoint.URL, "mode": endpoint.Mode,
				"transport":       endpoint.Transport,
				"has_auth_header": endpoint.Headers["Authorization"] != ""}
			unauth, err := http.Post(endpoint.URL, "application/json", strings.NewReader(`{}`))
			if err == nil {
				record["unauth_rejected"] = unauth.StatusCode == http.StatusForbidden
				unauth.Body.Close()
			}
			req, _ := http.NewRequest(http.MethodPost, endpoint.URL, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", endpoint.Headers["Authorization"])
			authed, err := http.DefaultClient.Do(req)
			if err == nil {
				record["auth_passed_gate"] = authed.StatusCode != http.StatusForbidden
				authed.Body.Close()
			}
			raw, _ := json.Marshal(record)
			_ = os.WriteFile(recordPath, raw, 0o600)
			reply(map[string]any{"session": map[string]any{"sessionId": "sess-1",
				"modelId": params.ModelID, "providerId": params.ProviderID}})
		case "usage/read":
			reply(map[string]any{})
		case "turn/start":
			reply(map[string]any{"turnId": "turn-1", "disposition": "started",
				"startedNewTurn": true, "status": "accepted", "commandId": "x"})
			emit(map[string]any{"jsonrpc": "2.0", "method": "session/tokenUsage", "params": map[string]any{}})
			emit(map[string]any{"jsonrpc": "2.0", "method": "item/started", "params": map[string]any{
				"sessionId": "sess-1", "viewCursor": "1",
				"item": map[string]any{"kind": "toolCall", "tool": toolName, "status": "inProgress"}}})
			emit(map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{
				"sessionId": "sess-1", "viewCursor": "2",
				"item": map[string]any{"kind": "toolCall", "tool": toolName, "status": "completed"}}})
			emit(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
				"sessionId": "sess-1", "turnId": "turn-1", "terminal": "completed", "viewCursor": "3"}})
		case "turn/cancel":
			reply(map[string]any{"status": "accepted"})
		default:
			emit(map[string]any{"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32601, "message": "unknown method"}})
		}
	}
}
