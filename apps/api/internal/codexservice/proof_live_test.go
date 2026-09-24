// T02 real-binary proof: the actual selected Codex binary dispatches proof
// research tools through mcpServer/tool/call, and saved-thread continuation,
// steering, interruption and uncertain disconnect are exercised against real
// persisted history.
//
// This test launches the pinned binary with an isolated CODEX_HOME, uses only
// instrumented httptest fixtures plus exactly one small live public GET, and
// never logs in or touches existing user configuration. It runs only with
// T02_PROOF_LIVE=1 so ordinary suites never spawn the binary or live egress.
package codexservice

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexrunner"
)

// T02 selected runtime pin (macOS proof host). The Linux runner artifact keeps
// its own separately verified pin at T29.
const t02CodexBinary = "/opt/homebrew/Caskroom/codex/0.153.4/bin/codex"
const t02CodexVersion = "codex-cli 0.153.4"
const t02CodexSHA256 = "b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proof-mcp" {
		os.Exit(runProofMCP(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// runProofMCP is a stdio MCP server exposing the proof research backend to the
// real App Server. Args: rootDir chromePath pythonPath. It speaks raw JSON-RPC
// (protocolVersion 2025-06-18) and appends a call log for evidence.
func runProofMCP(args []string) int {
	if len(args) != 3 {
		return 2
	}
	root, chrome, python := args[0], args[1], args[2]
	backend, err := OpenProofResearch(ProofConfig{RootDir: root, MaxActions: 1000,
		MaxConcurrent: 4, MaxBodyBytes: 1 << 20, OpTimeout: 90 * time.Second, PermitLoopback: true})
	if err != nil {
		return 2
	}
	execCfg := ProofExecConfig{ChromePath: chrome, PythonPath: python,
		ChromeIsolatedProfile: os.Getenv("PROOF_CHROME_ISOLATED") == "1"}
	callLog, err := os.OpenFile(filepath.Join(root, "mcp-calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 2
	}
	defer callLog.Close()
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	respond := func(id json.RawMessage, result any, rpcErr any) {
		frame := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id)}
		if rpcErr != nil {
			frame["error"] = rpcErr
		} else {
			frame["result"] = result
		}
		raw, _ := json.Marshal(frame)
		_, _ = out.Write(raw)
		_ = out.WriteByte('\n')
		_ = out.Flush()
	}
	strProp := map[string]any{"type": "string"}
	objProp := map[string]any{"type": "object"}
	schema := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	tools := []map[string]any{
		{"name": "proof_search",
			"description": "Search a base URL with caller-selected query and params.",
			"inputSchema": schema(map[string]any{"base": strProp, "query": strProp, "params": objProp}, "base", "query")},
		{"name": "proof_fetch",
			"description": "Fetch a URL with redirect and byte capture.",
			"inputSchema": schema(map[string]any{"url": strProp}, "url")},
		{"name": "proof_api",
			"description": "Read-only API call (GET or POST) with caller-selected params.",
			"inputSchema": schema(map[string]any{"method": strProp, "url": strProp, "params": objProp, "body": strProp, "contentType": strProp}, "method", "url")},
		{"name": "proof_browser",
			"description": "Render a URL headlessly and capture the DOM.",
			"inputSchema": schema(map[string]any{"url": strProp}, "url")},
		{"name": "proof_execute",
			"description": "Run Python with mediated outbound observation.",
			"inputSchema": schema(map[string]any{"code": strProp}, "code")},
	}
	for in.Scan() {
		line := in.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		_, _ = callLog.Write(append(append([]byte{}, line...), '\n'))
		if len(req.ID) == 0 {
			continue
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			respond(req.ID, map[string]any{"protocolVersion": p.ProtocolVersion,
				"capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo":   map[string]any{"name": "t02-proof", "version": "0.1.0"}}, nil)
		case "tools/list":
			respond(req.ID, map[string]any{"tools": tools}, nil)
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text, toolErr := proofMCPCall(backend, execCfg, p.Name, p.Arguments)
			if toolErr != nil {
				respond(req.ID, nil, map[string]any{"code": -32000, "message": "proof tool failed: " + toolErr.Error()})
				continue
			}
			respond(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}, nil)
		case "resources/list":
			respond(req.ID, map[string]any{"resources": []any{}}, nil)
		case "resources/templates/list":
			respond(req.ID, map[string]any{"resourceTemplates": []any{}}, nil)
		case "ping":
			respond(req.ID, map[string]any{}, nil)
		default:
			respond(req.ID, nil, map[string]any{"code": -32601, "message": "not implemented by proof MCP"})
		}
	}
	return 0
}

func proofMCPCall(backend *ProofResearch, execCfg ProofExecConfig, name string, args json.RawMessage) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var receipt ProofReceipt
	var err error
	switch name {
	case "proof_search":
		var a struct {
			Base   string            `json:"base"`
			Query  string            `json:"query"`
			Params map[string]string `json:"params"`
		}
		if json.Unmarshal(args, &a) != nil {
			return "", errors.New("invalid search arguments")
		}
		receipt, err = backend.Search(ctx, a.Base, a.Query, a.Params)
	case "proof_fetch":
		var a struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(args, &a) != nil {
			return "", errors.New("invalid fetch arguments")
		}
		receipt, err = backend.Fetch(ctx, a.URL)
	case "proof_api":
		var a struct {
			Method      string            `json:"method"`
			URL         string            `json:"url"`
			Params      map[string]string `json:"params"`
			Body        string            `json:"body"`
			ContentType string            `json:"contentType"`
		}
		if json.Unmarshal(args, &a) != nil {
			return "", errors.New("invalid API arguments")
		}
		receipt, err = backend.API(ctx, a.Method, a.URL, a.Params, []byte(a.Body), a.ContentType)
	case "proof_browser":
		var a struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(args, &a) != nil {
			return "", errors.New("invalid browser arguments")
		}
		receipt, err = backend.Browser(ctx, execCfg, a.URL)
	case "proof_execute":
		var a struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(args, &a) != nil {
			return "", errors.New("invalid execute arguments")
		}
		receipt, err = backend.Execute(ctx, execCfg, a.Code)
	default:
		return "", errors.New("unknown proof tool")
	}
	if err != nil {
		return "", err
	}
	excerpt := ""
	if receipt.CaptureID != "" {
		if body, err := backend.CaptureBody(receipt.CaptureID); err == nil {
			excerpt = string(body)
			if len(excerpt) > 2048 {
				excerpt = excerpt[:2048]
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"receipt": receipt, "excerpt": excerpt})
	return string(raw), nil
}

type t02Conn struct {
	proc   *codexrunner.Process
	client *codex.Client
}

func (c *t02Conn) close() {
	if c.client != nil {
		_ = c.client.Close()
	}
	if c.proc != nil {
		_ = c.proc.Close()
	}
}

func launchT02Runtime(t *testing.T, ctx context.Context, binary, state, work string) *t02Conn {
	t.Helper()
	proc, err := codexrunner.Start(ctx, codexrunner.Config{Executable: binary, SHA256: t02CodexSHA256, StateDir: state, WorkDir: work})
	if err != nil {
		t.Fatalf("runner start failed: %v", err)
	}
	client, err := codex.NewClient(proc, codex.Options{Timeout: 120 * time.Second})
	if err != nil {
		_ = proc.Close()
		t.Fatalf("client failed: %v", err)
	}
	info, err := client.Initialize(ctx)
	if err != nil {
		_ = client.Close()
		t.Fatalf("initialize failed: %v", err)
	}
	t.Logf("runtime userAgent=%q home=%q os=%q", info.UserAgent, info.CodexHome, info.PlatformOS)
	if !strings.HasPrefix(info.UserAgent, "jobseek_dashboard/0.153.4 ") {
		_ = client.Close()
		t.Fatalf("unexpected runtime version: %q", info.UserAgent)
	}
	if info.CodexHome != state {
		_ = client.Close()
		t.Fatalf("runtime home mismatch: %q", info.CodexHome)
	}
	return &t02Conn{proc: proc, client: client}
}

func callProofTool(t *testing.T, ctx context.Context, client *codex.Client, threadID, tool string, args map[string]any) (ProofReceipt, string) {
	t.Helper()
	rawArgs, _ := json.Marshal(args)
	raw, err := client.CallMCPTool(ctx, "t02proof", threadID, tool, rawArgs)
	if err != nil {
		t.Fatalf("tool %s failed: %v", tool, err)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) != 1 {
		t.Fatalf("tool %s result shape wrong: %.300s %v", tool, raw, err)
	}
	var payload struct {
		Receipt ProofReceipt `json:"receipt"`
		Excerpt string       `json:"excerpt"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("tool %s payload wrong: %.300s %v", tool, result.Content[0].Text, err)
	}
	if payload.Receipt.Operation != strings.TrimPrefix(tool, "proof_") {
		t.Fatalf("tool %s receipt operation wrong: %+v", tool, payload.Receipt)
	}
	return payload.Receipt, payload.Excerpt
}

// waitTurnActive polls history until the turn is observable and in flight.
// Early reads can fail transiently while the server attaches the turn.
func waitTurnActive(t *testing.T, ctx context.Context, client *codex.Client, threadID, turnID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		turns, err := client.ListTurns(ctx, threadID, nil)
		if err == nil {
			for _, item := range turns.Data {
				if item.ID == turnID {
					if item.Status == "inProgress" {
						return
					}
					t.Fatalf("turn %s settled before interrupt: %s", turnID, item.Status)
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn %s never became active: %v", turnID, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// interruptWhenActive retries the interrupt until the server accepts it. A
// turn that only just became visible can still reject the first attempt.
func interruptWhenActive(t *testing.T, ctx context.Context, client *codex.Client, threadID, turnID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if err := client.Interrupt(ctx, threadID, turnID); err == nil {
			return
		} else if status, listErr := client.ObserveTurn(ctx, threadID, turnID); listErr == nil && status != "inProgress" {
			t.Fatalf("turn %s settled during interrupt: %s (%v)", turnID, status, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("interrupt of %s never accepted", turnID)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitTurnTerminal(t *testing.T, client *codex.Client, threadID, turnID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("no terminal event for %s/%s", threadID, turnID)
		case event, ok := <-client.Events():
			if !ok {
				t.Fatalf("events closed: %v", client.Err())
			}
			if event.Request != nil || event.Method != "turn/completed" {
				continue
			}
			var p struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			if json.Unmarshal(event.Params, &p) != nil {
				continue
			}
			if p.ThreadID == threadID && p.Turn.ID == turnID {
				return p.Turn.Status
			}
		}
	}
}

func TestT02RealRuntimeProof(t *testing.T) {
	if os.Getenv("T02_PROOF_LIVE") != "1" {
		t.Skip("real-binary proof runs only with T02_PROOF_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	fx := startProofFixtures(t)
	chrome := proofChromePath(t)
	python := proofPythonPath(t)
	root := t.TempDir()

	binary := t02CodexBinary
	if custom := os.Getenv("T02_CODEX_BIN"); custom != "" {
		binary = custom
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatalf("codex binary missing: %v", err)
	}
	versionOut, err := exec.CommandContext(ctx, resolved, "--version").Output()
	if err != nil || strings.TrimSpace(string(versionOut)) != t02CodexVersion {
		t.Fatalf("codex version mismatch: %q %v", versionOut, err)
	}
	digest, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(digest)
	if hex.EncodeToString(sum[:]) != t02CodexSHA256 {
		t.Fatalf("codex digest mismatch: %x", sum)
	}
	t.Logf("selected binary=%s version=%s sha256=%s", resolved, strings.TrimSpace(string(versionOut)), t02CodexSHA256)

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, work := filepath.Join(base, "state"), filepath.Join(base, "work")
	for _, dir := range []string{state, work} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper, err = filepath.EvalSymlinks(helper)
	if err != nil {
		t.Fatal(err)
	}
	config := "model = \"gpt-5\"\n\n[mcp_servers.t02proof]\ncommand = " + quoteTOML(helper) + "\nargs = " +
		"[" + quoteTOML("proof-mcp") + ", " + quoteTOML(root) + ", " + quoteTOML(chrome) + ", " + quoteTOML(python) + "]\n" +
		"required = true\nstartup_timeout_sec = 15\nenabled_tools = [\"proof_search\", \"proof_fetch\", \"proof_api\", \"proof_browser\", \"proof_execute\"]\n"
	if err := os.WriteFile(filepath.Join(state, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	conn := launchT02Runtime(t, ctx, resolved, state, work)
	thread, err := conn.client.StartThread(ctx, "T02 proof instructions (synthetic)", "gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	threadID := thread.ID
	t.Logf("thread=%s", threadID)

	// Discovery: the proof MCP server must be connected with all five tools.
	servers, err := conn.client.ListMCPServers(ctx, &threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var proofServer *codex.MCPServer
	for i := range servers.Data {
		if servers.Data[i].Name == "t02proof" {
			proofServer = &servers.Data[i]
		}
	}
	if proofServer == nil {
		t.Fatalf("proof MCP server not discovered: %+v", servers.Data)
	}
	for _, tool := range []string{"proof_search", "proof_fetch", "proof_api", "proof_browser", "proof_execute"} {
		if _, ok := proofServer.Tools[tool]; !ok {
			t.Fatalf("proof tool %s missing from discovery", tool)
		}
	}
	t.Logf("discovered proof MCP tools=%d authStatus=%s", len(proofServer.Tools), proofServer.AuthStatus)

	// Unseeded-style search through the real App Server dispatch path.
	searchReceipt, searchExcerpt := callProofTool(t, ctx, conn.client, threadID, "proof_search", map[string]any{
		"base": fx.searchURL, "query": "synthetic t02 query", "params": map[string]string{"page": "1"},
	})
	if searchReceipt.Status != ProofOK || searchReceipt.CaptureID == "" {
		t.Fatalf("search receipt wrong: %+v", searchReceipt)
	}
	var found struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(searchExcerpt), &found); err != nil || len(found.Results) != 2 {
		t.Fatalf("search excerpt wrong: %.200s %v", searchExcerpt, err)
	}

	// Follow the first unseeded result URL.
	fetchReceipt, fetchExcerpt := callProofTool(t, ctx, conn.client, threadID, "proof_fetch", map[string]any{
		"url": found.Results[0].URL,
	})
	if fetchReceipt.Status != ProofOK || !strings.Contains(fetchExcerpt, "t02-fixture-marker") {
		t.Fatalf("follow receipt wrong: %+v %.200s", fetchReceipt, fetchExcerpt)
	}

	// Discovered-API POST with caller-selected parameters.
	apiReceipt, apiExcerpt := callProofTool(t, ctx, conn.client, threadID, "proof_api", map[string]any{
		"method": "POST", "url": fx.apiURL + "/query",
		"params": map[string]string{"type": "role"}, "body": `{"remote":true}`, "contentType": "application/json",
	})
	var apiEcho struct {
		Method      string `json:"method"`
		Body        string `json:"body"`
		ContentType string `json:"contentType"`
	}
	if apiReceipt.Status != ProofOK || json.Unmarshal([]byte(apiExcerpt), &apiEcho) != nil ||
		apiEcho.Method != "POST" || apiEcho.Body != `{"remote":true}` || apiEcho.ContentType != "application/json" {
		t.Fatalf("API receipt wrong: %+v %.200s", apiReceipt, apiExcerpt)
	}

	// Rendered browsing through the real dispatch path.
	browserReceipt, browserExcerpt := callProofTool(t, ctx, conn.client, threadID, "proof_browser", map[string]any{
		"url": found.Results[1].URL,
	})
	if browserReceipt.Status != ProofOK || !strings.Contains(browserExcerpt, "rendered-proof-marker") {
		t.Fatalf("browser receipt wrong: %+v %.200s", browserReceipt, browserExcerpt)
	}

	// Executable research with mediated outbound observation.
	executeReceipt, executeExcerpt := callProofTool(t, ctx, conn.client, threadID, "proof_execute", map[string]any{
		"code": "import urllib.request\nprint('exec-saw:' + urllib.request.urlopen('" + fx.searchURL + "?q=via-mcp', timeout=10).read().decode()[:40])\n",
	})
	if executeReceipt.Status != ProofOK || !strings.Contains(executeExcerpt, "exec-saw:") {
		t.Fatalf("execute receipt wrong: %+v %.200s", executeReceipt, executeExcerpt)
	}

	// Authentic capture: bytes behind the receipts must equal fixture bytes.
	local, err := OpenProofResearch(ProofConfig{RootDir: root, PermitLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	page, err := local.CaptureBody(fetchReceipt.CaptureID)
	if err != nil || string(page) != fx.pageBody {
		t.Fatal("dispatch capture does not match fixture bytes")
	}

	// Steering and interruption against a live in-flight turn. The turn is
	// observed first: an interrupt issued before the server-side turn is
	// active races startup and is rejected with -32600 (T02 probe evidence).
	turn, err := conn.client.StartTurn(ctx, threadID, "T02 synthetic turn (no owner content)", "medium")
	if err != nil {
		t.Fatal(err)
	}
	waitTurnActive(t, ctx, conn.client, threadID, turn.ID, 20*time.Second)
	steered, err := conn.client.SteerTurn(ctx, threadID, turn.ID, "T02 synthetic steer")
	if err != nil || steered != turn.ID {
		t.Fatalf("steer failed: %q %v", steered, err)
	}
	interruptWhenActive(t, ctx, conn.client, threadID, turn.ID, 20*time.Second)
	if status := waitTurnTerminal(t, conn.client, threadID, turn.ID, 30*time.Second); status != "interrupted" {
		t.Fatalf("interrupted turn status: %s", status)
	}

	// Saved-thread continuation on a new connection to the same state.
	conn.close()
	conn2 := launchT02Runtime(t, ctx, resolved, state, work)
	resumed, err := conn2.client.ResumeThread(ctx, threadID, true)
	if err != nil || resumed.ID != threadID {
		t.Fatalf("resume failed: %+v %v", resumed, err)
	}
	turns, err := conn2.client.ListTurns(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var seen map[string]string
	seen = map[string]string{}
	for _, item := range turns.Data {
		seen[item.ID] = item.Status
	}
	if seen[turn.ID] != "interrupted" {
		t.Fatalf("continued history wrong: %+v", seen)
	}
	observed, err := conn2.client.ObserveTurn(ctx, threadID, turn.ID)
	if err != nil || observed != "interrupted" {
		t.Fatalf("observe continued turn: %q %v", observed, err)
	}

	// Fresh-connection ObserveTurn must resume on its own (no explicit resume).
	conn2.close()
	conn3 := launchT02Runtime(t, ctx, resolved, state, work)
	observed, err = conn3.client.ObserveTurn(ctx, threadID, turn.ID)
	if err != nil || observed != "interrupted" {
		t.Fatalf("fresh observe failed: %q %v", observed, err)
	}

	// Uncertain disconnect: kill the process mid-turn, then reconcile honestly.
	// Observation alone does not load the thread for writes; a new connection
	// must resume before starting work (T02 probe evidence).
	resumed, err = conn3.client.ResumeThread(ctx, threadID, true)
	if err != nil || resumed.ID != threadID {
		t.Fatalf("pre-kill resume failed: %+v %v", resumed, err)
	}
	turn2, err := conn3.client.StartTurn(ctx, threadID, "T02 uncertain turn (synthetic)", "medium")
	if err != nil {
		t.Fatal(err)
	}
	conn3.close() // no interrupt: destructive transport shutdown mid-flight
	conn4 := launchT02Runtime(t, ctx, resolved, state, work)
	resumed, err = conn4.client.ResumeThread(ctx, threadID, true)
	if err != nil || resumed.ID != threadID {
		t.Fatalf("post-kill resume failed: %+v %v", resumed, err)
	}
	turns, err = conn4.client.ListTurns(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen = map[string]string{}
	for _, item := range turns.Data {
		seen[item.ID] = item.Status
	}
	t.Logf("post-kill history=%v", seen)
	if len(turns.Data) != 2 || seen[turn.ID] != "interrupted" {
		t.Fatalf("post-kill history lost or duplicated: %+v", seen)
	}
	// The killed turn must never be presented as a silent success, and this
	// client must not have created any replacement turn (no blind replay).
	status2 := seen[turn2.ID]
	if status2 == "" {
		t.Fatalf("killed turn missing from history: %+v", seen)
	}
	if status2 == "completed" {
		t.Fatalf("killed turn reported completed: %+v", seen)
	}
	t.Logf("killed turn status=%s (honest non-success; see report for staleness note)", status2)
	conn4.close()

	// Exactly one small live public-source check through the same capture path.
	live, err := local.Fetch(ctx, "https://example.com/")
	if err != nil || live.Status != ProofOK {
		t.Fatalf("live check failed: %+v %v", live, err)
	}
	liveBody, err := local.CaptureBody(live.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live check url=%s status-capture=%s bytes=%d redirects=%v", live.FinalURL, live.CaptureID[:16], len(liveBody), live.Redirects)
	usage := local.Usage()
	t.Logf("local ledger actions=%d bytesIn=%d unknown=%v", usage.Actions, usage.BytesIn, usage.Unknown)
}

func quoteTOML(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
