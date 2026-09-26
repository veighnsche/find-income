package musewire

// Live exec transport for the E11 first discovery run. It conducts one
// Contributor turn through `muse exec --json`, connecting the run's
// in-process public tool server through a per-run localhost
// streamable-HTTP MCP endpoint declared in an isolated XDG config home.
// The owner's settings are never mutated. Standard inputs are refused:
// this transport is discovery-only.
//
// Why exec and not serve: the installed 1.4.0 serve host grants the
// sessionMcp capability but rejects every session/start carrying
// config.mcpServers with session_mcp_base_unavailable, with or without
// a settings-file MCP base. Exec is the documented headless path with
// native --max-model-steps, --json events and settings-file MCP.

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
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
)

// mcpServerName is the MCP server key for the run's public tools.
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
	// Provider selects the model backend. Production uses "meta"; the
	// echo provider exists for zero-spend plumbing fixtures.
	Provider string
	// ValidationPrompt replaces the discovery prompt for harness-only
	// validation turns. Production leaves it empty; any non-empty value
	// is recorded in the trace.
	ValidationPrompt string
}

var _ musecode.Transport = (*LiveTransport)(nil)

// discoveryPrompt renders generalized criteria plus operating rules. It
// carries no owner identity, profile facts, or private requirements.
func discoveryPrompt(criteria musecode.PublicCriteria) string {
	var b strings.Builder
	b.WriteString("You are the vacancy-discovery researcher for a personal job search. ")
	b.WriteString("Use ONLY these MCP tools: public_search, public_fetch, public_save_vacancy, public_save_question, public_list_saved. ")
	b.WriteString("Never use any other tool, skill, shell, memory, subagent, or background work. ")
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
	b.WriteString("public_search and public_fetch take full public https:// URLs only, never bare keywords: ")
	b.WriteString("use job-board search pages, company career pages, public API endpoints, or search-engine result URLs you construct. ")
	b.WriteString("Rules: at most 12 public_search/public_fetch calls total; save every real vacancy you verify with public_save_vacancy before moving on; ")
	b.WriteString("pass the receipt_id from the search/fetch output as the save receipt, never the capture_id; search criteria must be an object, never a JSON string; ")
	b.WriteString("every saved field must come from captured evidence; never invent vacancies, employers, questions, or reasons; ")
	b.WriteString("when the evidence is thin, save what you verified and report coverage and gaps honestly. ")
	b.WriteString("End with a short summary of sources searched, vacancies saved, and gaps.")
	return b.String()
}

// mapToolName strips an MCP namespace wrapper. Anything that does not
// resolve to a bare name keeps its verbatim form so the supervisor
// fails the run closed.
func mapToolName(name string) string {
	if musecode.ContributorToolAllowed(name) {
		return name
	}
	// The host sanitizes the server segment (hyphens become underscores):
	// mcp__find_income_public__public_search.
	if parts := strings.Split(name, "__"); len(parts) == 3 && parts[0] == "mcp" &&
		parts[1] == strings.ReplaceAll(mcpServerName, "-", "_") {
		return parts[2]
	}
	if rest, ok := strings.CutPrefix(name, mcpServerName+"."); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(name, mcpServerName+"/"); ok {
		return rest
	}
	return name
}

type traceWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (t *traceWriter) event(entry any) {
	if t == nil || t.w == nil {
		return
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if len(raw) > 1<<20 {
		raw = append(append([]byte(nil), raw[:1<<20]...), []byte(`"<truncated>"`)...)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.w.Write(append(raw, '\n'))
}

func (t *traceWriter) hostLine(line []byte) {
	t.event(map[string]any{"dir": "host->client", "at": time.Now().UTC().Format(time.RFC3339Nano),
		"line": strings.TrimSpace(string(line))})
}

func (t *traceWriter) note(format string, args ...any) {
	t.event(map[string]any{"dir": "note", "at": time.Now().UTC().Format(time.RFC3339Nano),
		"line": fmt.Sprintf(format, args...)})
}

// execEvent is one --json JSONL record. Only the fields the folder reads
// are decoded; the trace keeps every raw line.
type execEvent struct {
	PayloadType string `json:"payload_type"`
	Payload     struct {
		Kind       string `json:"kind"`
		Terminal   string `json:"terminal"`
		Text       string `json:"text"`
		Reason     string `json:"reason"`
		TaskID     string `json:"task_id"`
		ModelID    string `json:"model_id"`
		ProviderID string `json:"provider_id"`
		Event      struct {
			Kind     string `json:"kind"`
			TaskKind string `json:"task_kind"`
			Reason   string `json:"reason"`
		} `json:"event"`
		CorrelationFacts struct {
			ToolName string `json:"tool_name"`
			Outcome  string `json:"outcome"`
		} `json:"correlation_facts"`
	} `json:"payload"`
}

// execFolder maps --json lines to supervisor observations. It is stateful:
// task_kind appears only on proposed events, so later lifecycle events
// resolve through the task id. Model steps count one per model-task
// terminal (success or failure); in-task provider retries undercount
// slightly, and the host-side --max-model-steps cap stays the hard
// backstop.
type execFolder struct {
	modelID    string
	providerID string
	tasks      map[string]string
	called     map[string]bool
	routeSeen  bool
}

func newExecFolder(modelID, providerID string) *execFolder {
	return &execFolder{modelID: modelID, providerID: providerID,
		tasks: map[string]string{}, called: map[string]bool{}}
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
	provider := t.Provider
	if provider == "" {
		provider = "meta"
	}
	runRef := path.Base(spec.Workspace)
	if !validRunRef(runRef) {
		return fmt.Errorf("musewire: workspace %q names no run", spec.Workspace)
	}
	server, ok := t.Servers(runRef)
	if !ok || server == nil {
		return fmt.Errorf("musewire: no live tool server for run %q", runRef)
	}
	// The exec JSONL is the only record of host-side terminals: without a
	// configured trace sink, persist it to the run workspace so failed
	// runs stay debuggable after the host exits.
	traceSink := t.Trace
	if traceSink == nil {
		traceFile, err := os.Create(filepath.Join(spec.Workspace, "trace.jsonl"))
		if err != nil {
			return err
		}
		defer traceFile.Close()
		traceSink = traceFile
	}
	trace := &traceWriter{w: traceSink}
	trace.note("run %s workspace %s model %s provider %s backend %s", runRef, spec.Workspace, t.ModelID, t.ProviderID, provider)

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
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return errors.New("musewire: loopback listener has no TCP port")
	}
	trace.note("mcp endpoint http://127.0.0.1:%d/mcp token %s...", addr.Port, token[:8])

	home, err := writeExecHome(spec.Workspace, addr.Port, token)
	if err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// The meta provider reads auth.json next to the active settings file;
	// MUSE_AUTH_PATH does not redirect it on the observed 1.4.0 host. Copy
	// the owner's credential file into the isolated home unread: its bytes
	// are never parsed, printed, or proxied, and the copy lives and dies
	// with the 0700 run workspace.
	if provider == "meta" {
		credential, err := os.ReadFile(filepath.Join(homeDir, ".config", "muse", "auth.json"))
		if err != nil {
			return fmt.Errorf("musewire: owner CLI credentials unavailable: %w", err)
		}
		if err := os.WriteFile(filepath.Join(home, "config", "muse", "auth.json"), credential, 0o600); err != nil {
			return err
		}
	}
	prompt := discoveryPrompt(public.Criteria)
	if t.ValidationPrompt != "" {
		prompt = t.ValidationPrompt
		trace.note("validation prompt override active")
	}
	promptFile := filepath.Join(spec.Workspace, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
		return err
	}
	maxSteps := spec.Bounds.MaxModelSteps
	if maxSteps > 100 {
		maxSteps = 100
	}
	args := []string{"exec", "--json", "--prompt-file", promptFile,
		"--provider", provider}
	if provider == "meta" {
		args = append(args, "--model", t.ModelID, "--reasoning-effort", "max")
	}
	args = append(args,
		"--max-model-steps", fmt.Sprintf("%d", maxSteps),
		"--max-tool-output-bytes", fmt.Sprintf("%d", spec.Bounds.MaxBytesPerOp),
		"--workspace", spec.Workspace,
		"--approval-mode", "never",
		"--disable-shell", "--disable-write", "--disable-web-tools",
		"--no-foreign-personal-context")
	cmd := exec.Command(t.CLIPath, args...)
	cmd.Dir = spec.Workspace
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
	)
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
		if len(strings.TrimSpace(string(slurp))) > 0 {
			trace.note("host stderr: %s", strings.TrimSpace(string(slurp)))
		}
	}()
	if err := cmd.Start(); err != nil {
		return err
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var stopMu sync.Mutex
	stopped := false
	var waitErr error
	stop := func() error {
		stopMu.Lock()
		defer stopMu.Unlock()
		if stopped {
			return waitErr
		}
		stopped = true
		select {
		case waitErr = <-waited:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case waitErr = <-waited:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				waitErr = <-waited
			}
		}
		return waitErr
	}
	defer stop()

	emitted := map[string]bool{}
	folder := newExecFolder(t.ModelID, t.ProviderID)
	fetch := 0
	finished := false
	terminal := ""
	detail := ""
	reader := bufio.NewReader(stdout)
loop:
	for {
		select {
		case <-ctx.Done():
			stop()
			return ctx.Err()
		default:
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trace.hostLine(line)
			kind, tool, bytes, step, done, terr := folder.fold(line)
			switch {
			case terr != "":
				detail = terr
				stop()
				break loop
			case step:
				sink.Emit(musecode.Event{Kind: musecode.EventModelStep})
			case kind == "toolCall":
				mapped := mapToolName(tool)
				trace.note("tool call %q -> %q", tool, mapped)
				sink.Emit(musecode.Event{Kind: musecode.EventToolCall, Tool: mapped})
				if mapped == publicresearch.ToolSearch || mapped == publicresearch.ToolFetch {
					fetch++
					if fetch > e11FetchBound {
						detail = fmt.Sprintf("retrieval fetch bound %d exceeded", e11FetchBound)
						stop()
						break loop
					}
				}
			case kind == "toolResult":
				sink.Emit(musecode.Event{Kind: musecode.EventToolResult, Tool: mapToolName(tool), BytesOut: bytes})
				emitNewSaves(server, sink, emitted)
			case kind == "forbidden":
				detail = "forbidden item kind " + tool
				stop()
				break loop
			case done:
				terminal = kind
				finished = true
				break loop
			}
		}
		if err != nil {
			break
		}
	}
	emitNewSaves(server, sink, emitted)
	detail = exitDetail(detail, terminal, stop())
	if detail != "" {
		runErr := errors.New("musewire: " + detail)
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	if !finished || terminal != "completed" {
		runErr := fmt.Errorf("musewire: turn ended %q without completion", terminal)
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	sink.Emit(musecode.Event{Kind: musecode.EventFinished})
	return nil
}

// fold maps one --json line to a supervisor observation. It returns the
// mapped kind (toolCall, toolResult, forbidden, or a run terminal), the
// verbatim tool/task name, result bytes for tool results, whether a
// model step completed, whether the run reached terminal state, and a
// fatal detail. Unknown lines are ignored: the trace keeps them and the
// terminal event decides the outcome.
func (f *execFolder) fold(line []byte) (kind, tool string, bytes int64, step, done bool, fatal string) {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "{") {
		return "", "", 0, false, false, ""
	}
	var event execEvent
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
		return "", "", 0, false, false, ""
	}
	switch event.PayloadType {
	case "run.model.configured":
		f.routeSeen = true
		if event.Payload.ModelID != f.modelID || event.Payload.ProviderID != f.providerID {
			return "", "", 0, false, false, fmt.Sprintf("route drift: %s/%s != pinned %s/%s",
				event.Payload.ProviderID, event.Payload.ModelID, f.providerID, f.modelID)
		}
		return "", "", 0, false, false, ""
	case "run.terminal.completed", "run.terminal.failed", "run.terminal.cancelled":
		terminal := strings.TrimPrefix(event.PayloadType, "run.terminal.")
		if event.Payload.Terminal != "" {
			terminal = event.Payload.Terminal
		}
		return terminal, "", 0, false, true, ""
	case "tool.result":
		return "toolResult", event.Payload.CorrelationFacts.ToolName, int64(len(event.Payload.Text)), false, false, ""
	case "task.lifecycle.proposed":
		if event.Payload.TaskID != "" && event.Payload.Event.TaskKind != "" {
			f.tasks[event.Payload.TaskID] = event.Payload.Event.TaskKind
		}
		return "", "", 0, false, false, ""
	case "task.lifecycle.started":
		taskKind := f.tasks[event.Payload.TaskID]
		if name, ok := strings.CutPrefix(taskKind, "tool."); ok && name != "" {
			if f.called[event.Payload.TaskID] {
				return "", "", 0, false, false, ""
			}
			f.called[event.Payload.TaskID] = true
			return "toolCall", name, 0, false, false, ""
		}
		if isForbiddenTask(taskKind) {
			return "forbidden", taskKind, 0, false, false, ""
		}
	case "task.lifecycle.completed", "task.lifecycle.failed":
		taskKind := f.tasks[event.Payload.TaskID]
		if name, ok := strings.CutPrefix(taskKind, "model."); ok && name != "" {
			return "", "", 0, true, false, ""
		}
		if name, ok := strings.CutPrefix(taskKind, "tool."); ok && name != "" {
			return "toolResult", name, 0, false, false, ""
		}
		if isForbiddenTask(taskKind) {
			return "forbidden", taskKind, 0, false, false, ""
		}
		if event.PayloadType == "task.lifecycle.failed" && taskKind != "" &&
			!strings.HasPrefix(taskKind, "reminder.") {
			return "", "", 0, false, false, "task failed: " + taskKind + ": " + event.Payload.Event.Reason
		}
	}
	return "", "", 0, false, false, ""
}

func isForbiddenTask(taskKind string) bool {
	return taskKind == "subagent" || strings.HasPrefix(taskKind, "subagent.") ||
		taskKind == "workflow" || strings.HasPrefix(taskKind, "workflow.")
}

// exitDetail decides the recorded detail after the event loop. An
// already-recorded fatal detail (fetch bound, forbidden item, route
// drift) always wins over the host's wait status: stopping the host to
// enforce a bound must not mask the bound itself.
func exitDetail(recorded, terminal string, waitErr error) string {
	if recorded != "" {
		return recorded
	}
	if terminal == "" && waitErr != nil {
		return "host exit: " + waitErr.Error()
	}
	return recorded
}

func emitNewSaves(server *publicresearch.Server, sink musecode.EventSink, emitted map[string]bool) {
	for _, ref := range append(append([]string(nil), server.SavedVacancyRefs()...), server.SavedQuestionRefs()...) {
		if emitted[ref] {
			continue
		}
		emitted[ref] = true
		sink.Emit(musecode.Event{Kind: musecode.EventSaved, SaveRef: ref})
	}
}

// writeExecHome builds an isolated XDG home under the run workspace: a
// config home whose settings declare only the run MCP server with the
// concrete loopback URL and bearer token rendered in, and an empty data
// home. The owner's configuration is never touched. Values are rendered
// literally because observed 1.4.0 hosts do not interpolate ${VAR} in
// this block (a ${VAR} URL yields zero connection attempts).
func writeExecHome(workspace string, port int, token string) (string, error) {
	home := filepath.Join(workspace, "mushome")
	configDir := filepath.Join(home, "config", "muse")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(home, "data"), 0o700); err != nil {
		return "", err
	}
	settings := map[string]any{
		"schema_version":   1,
		"model":            "muse-spark-1.3-contributor",
		"reasoning_effort": "max",
		"mcp_servers": map[string]any{
			mcpServerName: map[string]any{
				"transport": "streamable_http",
				"url":       fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
				"headers":   map[string]any{"Authorization": "Bearer " + token},
				"mode":      "required",
			},
		},
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), raw, 0o600); err != nil {
		return "", err
	}
	return home, nil
}
