package musewire

// Live MSP transport for the E11 first discovery run. It conducts one
// Contributor session against the installed `muse serve` host over NDJSON
// JSON-RPC, connects the run's in-process public tool server through a
// per-run localhost streamable-HTTP MCP endpoint, and folds host view
// events into supervisor events. Standard inputs are refused: this
// transport is discovery-only.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
)

// mcpServerName is the sessionMcp server key for the run's public tools.
const mcpServerName = "find-income-public"

// e11FetchBound caps public_search/public_fetch tool calls for one run.
// The supervisor owns model/tool/byte ceilings; this retrieval bound is
// enforced here because only the transport sees tool granularity.
const e11FetchBound = 12

// LiveTransport conducts Contributor discovery sessions on the live CLI.
type LiveTransport struct {
	CLIPath    string
	ModelID    string
	ProviderID string
	Servers    func(runRef string) (*publicresearch.Server, bool)
	Trace      io.Writer
}

var _ musecode.Transport = (*LiveTransport)(nil)

type mspFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mspError       `json:"error,omitempty"`
}

type mspError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *mspError) Error() string { return fmt.Sprintf("msp %d: %s", e.Code, e.Message) }

type mspClient struct {
	stdin   io.WriteCloser
	writeMu sync.Mutex
	nextID  int64
	pending map[string]chan mspFrame
	mu      sync.Mutex
	notify  chan mspFrame
	done    chan struct{}
	trace   *traceWriter
}

func newMSPClient(stdin io.WriteCloser, trace *traceWriter) *mspClient {
	return &mspClient{stdin: stdin, pending: map[string]chan mspFrame{},
		notify: make(chan mspFrame, 256), done: make(chan struct{}), trace: trace}
}

func (c *mspClient) serve(stdout io.Reader) {
	defer close(c.done)
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		if len(line) > 64<<20 {
			return
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		c.trace.hostLine(line)
		var frame mspFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			continue
		}
		if len(frame.ID) != 0 {
			c.mu.Lock()
			ch := c.pending[string(frame.ID)]
			delete(c.pending, string(frame.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- frame
			}
			continue
		}
		select {
		case c.notify <- frame:
		default:
		}
	}
}

func (c *mspClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.writeMu.Lock()
	c.nextID++
	frame := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		frame["params"] = params
	}
	body, err := json.Marshal(frame)
	if err != nil {
		c.writeMu.Unlock()
		return nil, err
	}
	ch := make(chan mspFrame, 1)
	c.mu.Lock()
	c.pending[fmt.Sprintf("%d", c.nextID)] = ch
	c.mu.Unlock()
	c.trace.clientLine(body)
	if _, err := c.stdin.Write(append(body, '\n')); err != nil {
		c.writeMu.Unlock()
		return nil, err
	}
	c.writeMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, musecode.ErrDisconnected
	case frame := <-ch:
		if frame.Error != nil {
			return nil, frame.Error
		}
		return frame.Result, nil
	}
}

func (c *mspClient) notifyOne(method string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	c.trace.clientLine(body)
	_, err = c.stdin.Write(append(body, '\n'))
	return err
}

type traceWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (t *traceWriter) line(dir string, raw []byte) {
	if t == nil || t.w == nil {
		return
	}
	out := raw
	if len(out) > 1<<20 {
		out = append(append([]byte(nil), out[:1<<20]...), []byte("\n<truncated>\n")...)
	}
	entry, _ := json.Marshal(map[string]string{"dir": dir, "at": time.Now().UTC().Format(time.RFC3339Nano),
		"frame": strings.TrimSpace(string(out))})
	t.mu.Lock()
	defer t.mu.Unlock()
	t.w.Write(append(entry, '\n'))
}

func (t *traceWriter) hostLine(raw []byte)   { t.line("host->client", raw) }
func (t *traceWriter) clientLine(raw []byte) { t.line("client->host", raw) }
func (t *traceWriter) note(format string, args ...any) {
	t.line("note", []byte(fmt.Sprintf(format, args...)))
}

func uuidv7() string {
	var b [16]byte
	now := uint64(time.Now().UnixMilli())
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(now>>40), byte(now>>32), byte(now>>24), byte(now>>16), byte(now>>8), byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("musewire: no randomness for command id")
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	hexed := hex.EncodeToString(b[:])
	return hexed[:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:]
}

// discoveryPrompt renders generalized criteria plus operating rules. It
// carries no owner identity, profile facts, or private requirements.
func discoveryPrompt(criteria musecode.PublicCriteria) string {
	var b strings.Builder
	b.WriteString("You are the vacancy-discovery researcher for a personal job search. ")
	b.WriteString("Use ONLY these MCP tools: public_search, public_fetch, public_save_vacancy, public_save_question, public_list_saved. ")
	b.WriteString("Never use any other tool, skill, shell, or background work. ")
	b.WriteString("Freely choose public sources, queries, public APIs and company career pages for this generalized brief:\n")
	if len(criteria.RoleKeywords) > 0 {
		fmt.Fprintf(&b, "- roles: %s\n", strings.Join(criteria.RoleKeywords, ", "))
	}
	if criteria.RegionText != "" {
		fmt.Fprintf(&b, "- region: %s\n", criteria.RegionText)
	}
	if len(criteria.SkillKeywords) > 0 {
		fmt.Fprintf(&b, "- skills: %s\n", strings.Join(criteria.SkillKeywords, ", "))
	}
	b.WriteString("Rules: at most 12 public_search/public_fetch calls total; save every real vacancy you verify with public_save_vacancy before moving on; ")
	b.WriteString("every saved field must come from captured evidence; never invent vacancies, employers, questions, or reasons; ")
	b.WriteString("when the evidence is thin, save what you verified and report coverage and gaps honestly. ")
	b.WriteString("End with a short summary of sources searched, vacancies saved, and gaps.")
	return b.String()
}

// mapToolName strips the sessionMcp namespace wrapper. Anything that does
// not resolve to a bare name keeps its verbatim form so the supervisor
// fails the run closed.
func mapToolName(name string) string {
	if musecode.ContributorToolAllowed(name) {
		return name
	}
	if parts := strings.Split(name, "__"); len(parts) == 3 && parts[0] == "mcp" && parts[1] == mcpServerName {
		return parts[2]
	}
	return name
}

// Run conducts one Contributor discovery turn to transport-terminal state.
func (t *LiveTransport) Run(ctx context.Context, spec musecode.SessionSpec, input musecode.SessionInput, resume musecode.Cursor, sink musecode.EventSink) error {
	public, ok := input.(musecode.PublicInput)
	if !ok {
		return errors.New("musewire: live transport conducts contributor discovery only")
	}
	if resume.RunRef != "" {
		return errors.New("musewire: resume arrives with the first live run")
	}
	if t.CLIPath == "" || t.ModelID == "" || t.ProviderID == "" || t.Servers == nil {
		return errors.New("musewire: live transport needs CLI path, model, provider and run servers")
	}
	runRef := path.Base(spec.Workspace)
	if !validRunRef(runRef) {
		return fmt.Errorf("musewire: workspace %q names no run", spec.Workspace)
	}
	server, ok := t.Servers(runRef)
	if !ok || server == nil {
		return fmt.Errorf("musewire: no live tool server for run %q", runRef)
	}
	trace := &traceWriter{w: t.Trace}
	trace.note("run %s workspace %s model %s provider %s", runRef, spec.Workspace, t.ModelID, t.ProviderID)

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		server.Handler().ServeHTTP(w, r)
	}))
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	defer httpServer.Close()
	go httpServer.Serve(listener)
	mcpURL := "http://" + listener.Addr().String() + "/mcp"
	trace.note("mcp endpoint %s token %s...", mcpURL, token[:8])

	// Plain Command, not CommandContext: Stop and wall-clock expiry must
	// deliver turn/cancel gracefully before the deferred cleanup below
	// reaps the host. Killing here would strand the cancel and the
	// usage-after read.
	cmd := exec.Command(t.CLIPath, "serve", "--disable-shell", "--disable-write")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	go func() {
		slurp, _ := io.ReadAll(stderr)
		if len(slurp) > 0 {
			trace.note("host stderr: %s", strings.TrimSpace(string(slurp)))
		}
	}()
	if err := cmd.Start(); err != nil {
		return err
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	defer func() {
		stdin.Close()
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-waited
		}
	}()

	client := newMSPClient(stdin, trace)
	go client.serve(stdout)
	rpcCtx, cancelRPC := context.WithTimeout(ctx, 60*time.Second)
	initRaw, err := client.call(rpcCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "find_income_e11", "version": "0.1.0"},
		"capabilities": map[string]any{"requestedCapabilities": []string{"sessionMcp"}}})
	cancelRPC()
	if err != nil {
		return fmt.Errorf("musewire: initialize: %w", err)
	}
	var initialized struct {
		Granted []string `json:"grantedCapabilities"`
		Server  struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(initRaw, &initialized); err != nil {
		return err
	}
	granted := false
	for _, cap := range initialized.Granted {
		if cap == "sessionMcp" {
			granted = true
		}
	}
	if !granted {
		return errors.New("musewire: host did not grant sessionMcp")
	}
	trace.note("host %s %s granted sessionMcp", initialized.Server.Name, initialized.Server.Version)
	if err := client.notifyOne("initialized"); err != nil {
		return err
	}
	rpcCtx, cancelRPC = context.WithTimeout(ctx, 60*time.Second)
	modelRaw, err := client.call(rpcCtx, "model/list", nil)
	cancelRPC()
	if err != nil {
		return fmt.Errorf("musewire: model/list: %w", err)
	}
	trace.note("catalog: %s", compactJSON(modelRaw, 2000))

	sessCtx, cancelSess := context.WithTimeout(ctx, 120*time.Second)
	startRaw, err := client.call(sessCtx, "session/start", map[string]any{
		"commandId": uuidv7(), "modelId": t.ModelID, "providerId": t.ProviderID,
		"workspaceRoot": spec.Workspace,
		"config": map[string]any{"mcpServers": map[string]any{mcpServerName: map[string]any{
			"transport": "streamableHttp", "url": mcpURL, "mode": "required",
			"headers": map[string]any{"Authorization": "Bearer " + token}}}},
	})
	cancelSess()
	if err != nil {
		return fmt.Errorf("musewire: session/start: %w", err)
	}
	var started struct {
		Session struct {
			SessionID  string `json:"sessionId"`
			ModelID    string `json:"modelId"`
			ProviderID string `json:"providerId"`
		} `json:"session"`
	}
	if err := json.Unmarshal(startRaw, &started); err != nil {
		return err
	}
	if started.Session.SessionID == "" {
		return errors.New("musewire: session/start returned no session id")
	}
	if started.Session.ModelID != t.ModelID || started.Session.ProviderID != t.ProviderID {
		return fmt.Errorf("musewire: session route %s/%s != pinned %s/%s",
			started.Session.ProviderID, started.Session.ModelID, t.ProviderID, t.ModelID)
	}
	trace.note("session %s route %s/%s", started.Session.SessionID, started.Session.ProviderID, started.Session.ModelID)
	sessionID := started.Session.SessionID

	usageCtx, cancelUsage := context.WithTimeout(ctx, 30*time.Second)
	usageBefore, err := client.call(usageCtx, "usage/read", nil)
	cancelUsage()
	if err != nil {
		trace.note("usage/read before failed: %v", err)
	} else {
		trace.note("usage before: %s", compactJSON(usageBefore, 1000))
	}

	turnID, err := t.startTurn(ctx, client, sessionID, discoveryPrompt(public.Criteria))
	if err != nil {
		return err
	}
	emitted := map[string]bool{}
	terminal, runErr := t.followTurn(ctx, client, sessionID, turnID, server, sink, trace, emitted)
	usageCtx, cancelUsage = context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	usageAfter, err := client.call(usageCtx, "usage/read", nil)
	cancelUsage()
	if err != nil {
		trace.note("usage/read after failed: %v", err)
	} else {
		trace.note("usage after: %s", compactJSON(usageAfter, 1000))
	}
	t.emitNewSaves(server, sink, emitted)
	if runErr != nil {
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	trace.note("turn %s terminal %s", turnID, terminal)
	if terminal != "completed" {
		err := fmt.Errorf("musewire: turn %s ended %s", turnID, terminal)
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: err.Error()})
		return err
	}
	sink.Emit(musecode.Event{Kind: musecode.EventFinished})
	return nil
}

func (t *LiveTransport) startTurn(ctx context.Context, client *mspClient, sessionID, prompt string) (string, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := client.call(rpcCtx, "turn/start", map[string]any{
		"commandId": uuidv7(), "sessionId": sessionID,
		"input": []any{map[string]any{"type": "text", "text": prompt}}})
	if err != nil {
		return "", fmt.Errorf("musewire: turn/start: %w", err)
	}
	var started struct {
		TurnID      string `json:"turnId"`
		Disposition string `json:"disposition"`
	}
	if err := json.Unmarshal(raw, &started); err != nil {
		return "", err
	}
	if started.TurnID == "" || started.Disposition != "started" {
		return "", fmt.Errorf("musewire: turn admission = %+v, want started", started)
	}
	return started.TurnID, nil
}

type liveFollower struct {
	fetch  int
	turnID string
}

func (t *LiveTransport) followTurn(ctx context.Context, client *mspClient, sessionID, turnID string,
	server *publicresearch.Server, sink musecode.EventSink, trace *traceWriter, emitted map[string]bool) (string, error) {
	f := &liveFollower{turnID: turnID}
	cancelTurn := func(reason string) {
		trace.note("cancel turn %s: %s", turnID, reason)
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := client.call(cancelCtx, "turn/cancel", map[string]any{
			"commandId": uuidv7(), "sessionId": sessionID, "turnId": turnID}); err != nil {
			trace.note("turn/cancel failed: %v", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			cancelTurn(ctx.Err().Error())
			return "", ctx.Err()
		case frame, ok := <-client.notify:
			if !ok {
				return "", musecode.ErrDisconnected
			}
			terminal, done, err := t.handleNotification(ctx, client, sessionID, server, sink, trace, f, emitted, frame, cancelTurn)
			if err != nil {
				return "", err
			}
			if done {
				return terminal, nil
			}
		}
	}
}

func (t *LiveTransport) handleNotification(ctx context.Context, client *mspClient, sessionID string,
	server *publicresearch.Server, sink musecode.EventSink, trace *traceWriter,
	f *liveFollower, emitted map[string]bool, frame mspFrame, cancelTurn func(string)) (string, bool, error) {
	switch frame.Method {
	case "session/tokenUsage":
		sink.Emit(musecode.Event{Kind: musecode.EventModelStep})
	case "item/started", "item/updated", "item/completed":
		var params struct {
			Item struct {
				Kind   string `json:"kind"`
				Tool   string `json:"tool"`
				Status string `json:"status"`
				Text   string `json:"text"`
			} `json:"item"`
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			return "", false, err
		}
		if params.SessionID != "" && params.SessionID != sessionID {
			return "", false, nil
		}
		switch params.Item.Kind {
		case "toolCall":
			mapped := mapToolName(params.Item.Tool)
			if frame.Method == "item/started" {
				trace.note("tool call %q -> %q", params.Item.Tool, mapped)
				sink.Emit(musecode.Event{Kind: musecode.EventToolCall, Tool: mapped})
				if mapped == publicresearch.ToolSearch || mapped == publicresearch.ToolFetch {
					f.fetch++
					if f.fetch > e11FetchBound {
						cancelTurn("retrieval fetch bound exceeded")
						return "", false, fmt.Errorf("musewire: retrieval fetch bound %d exceeded", e11FetchBound)
					}
				}
			} else if frame.Method == "item/completed" {
				sink.Emit(musecode.Event{Kind: musecode.EventToolResult, Tool: mapped})
				t.emitNewSaves(server, sink, emitted)
			}
		case "subagent", "workflow", "userShell":
			cancelTurn("forbidden item kind " + params.Item.Kind)
			return "", false, fmt.Errorf("musewire: forbidden item kind %s", params.Item.Kind)
		}
	case "approval/requested", "userInput/requested":
		cancelTurn("headless run cannot satisfy " + frame.Method)
		return "", false, fmt.Errorf("musewire: %s needs an operator; run fails closed", frame.Method)
	case "turn/completed":
		var params struct {
			TurnID   string `json:"turnId"`
			Terminal string `json:"terminal"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			return "", false, err
		}
		if params.TurnID != f.turnID {
			return "", false, nil
		}
		trace.note("turn completed terminal=%s reason=%s", params.Terminal, params.Reason)
		return params.Terminal, true, nil
	case "view/gap":
		trace.note("view gap observed; continuing on durable terminals")
	}
	return "", false, nil
}

func (t *LiveTransport) emitNewSaves(server *publicresearch.Server, sink musecode.EventSink, emitted map[string]bool) {
	for _, ref := range append(append([]string(nil), server.SavedVacancyRefs()...), server.SavedQuestionRefs()...) {
		if emitted[ref] {
			continue
		}
		emitted[ref] = true
		sink.Emit(musecode.Event{Kind: musecode.EventSaved, SaveRef: ref})
	}
}

func compactJSON(raw json.RawMessage, limit int) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		return text[:limit] + "<truncated>"
	}
	return text
}
