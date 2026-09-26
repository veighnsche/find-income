package musewire

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Shared direct-`muse exec` machinery (R1). Contributor (live.go) and
// Standard (standard.go) turns run the same headless invocation with the
// same timeout, structured-output, error and identity rules; only the
// prompt, output schema and tool policy differ. The CLI runs with the
// owner's real configuration: no isolated config home, no MCP settings,
// no loopback server. Model output is untrusted until the app verifies
// it against its own captures (see the evidence/save boundary in
// publicresearch/direct.go).

// maxHostModelSteps caps --max-model-steps on every turn. The supervisor
// enforces the run's own (possibly narrower) bound; this is the host-side
// backstop against a misconfigured ceiling.
const maxHostModelSteps = 100

// execArgsFor builds the direct `muse exec` argv shared by Contributor and
// Standard turns. Web tools stay on for Contributor retrieval and off for
// Standard preparation; shell and filesystem writes stay off and approval
// stays never on both. The meta provider pins --model plus
// --output-schema; the echo provider takes neither (it rejects
// --output-schema) and exists only for zero-spend plumbing fixtures.
func execArgsFor(promptFile, schemaFile, provider, modelID, effort string, maxSteps int, maxBytesPerOp int64, workspace string, webTools bool) []string {
	if maxSteps > maxHostModelSteps {
		maxSteps = maxHostModelSteps
	}
	args := []string{"exec", "--json", "--prompt-file", promptFile}
	if provider == "meta" && schemaFile != "" {
		args = append(args, "--output-schema", schemaFile)
	}
	args = append(args, "--provider", provider)
	if provider == "meta" {
		args = append(args, "--model", modelID, "--reasoning-effort", effort)
	}
	args = append(args,
		"--max-model-steps", fmt.Sprintf("%d", maxSteps),
		"--max-tool-output-bytes", fmt.Sprintf("%d", maxBytesPerOp),
		"--workspace", workspace,
		"--approval-mode", "never",
		"--disable-shell", "--disable-write")
	if !webTools {
		args = append(args, "--disable-web-tools")
	}
	return append(args, "--no-foreign-personal-context")
}

// bareToolName strips one "tool." task-kind prefix. Native tool names
// (web_search, web_fetch) pass through verbatim for the supervisor
// allowlist; anything else keeps its form and fails closed there.
func bareToolName(name string) string {
	if rest, ok := strings.CutPrefix(name, "tool."); ok {
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
// already-recorded fatal detail (bound breach, forbidden item, route
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

// execText extracts the payload type and text of one --json line for the
// model-text return path. The folder stays authoritative for control flow;
// this only reads run.output.delta and run.terminal.* texts.
func execText(line []byte) (string, string) {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "{") {
		return "", ""
	}
	var event execEvent
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
		return "", ""
	}
	return event.PayloadType, event.Payload.Text
}

// execProc is one supervised `muse exec` child: stdout streams to the
// caller while stop() fences shutdown (SIGTERM, 30s grace, then kill).
type execProc struct {
	cmd     *exec.Cmd
	waited  chan error
	mu      sync.Mutex
	stopped bool
	waitErr error
}

// startExec spawns one direct turn. Stderr slurps to the trace; the
// returned reader streams stdout JSONL. The caller must call stop.
func startExec(cliPath string, args []string, dir string, trace *traceWriter) (*execProc, *bufio.Reader, error) {
	cmd := exec.Command(cliPath, args...)
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	go func() {
		slurp, _ := io.ReadAll(stderr)
		if len(strings.TrimSpace(string(slurp))) > 0 {
			trace.note("host stderr: %s", strings.TrimSpace(string(slurp)))
		}
	}()
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	proc := &execProc{cmd: cmd, waited: make(chan error, 1)}
	go func() { proc.waited <- cmd.Wait() }()
	return proc, bufio.NewReader(stdout), nil
}

func (p *execProc) stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return p.waitErr
	}
	p.stopped = true
	select {
	case p.waitErr = <-p.waited:
	default:
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case p.waitErr = <-p.waited:
		case <-time.After(30 * time.Second):
			_ = p.cmd.Process.Kill()
			p.waitErr = <-p.waited
		}
	}
	return p.waitErr
}

// openRunTrace resolves the JSONL trace sink for one turn: the configured
// writer, or workspace/trace.jsonl so failed runs stay debuggable after
// the host exits. A file sink returns its closer; otherwise closer is nil.
func openRunTrace(configured io.Writer, workspace string) (io.Writer, io.Closer, error) {
	if configured != nil {
		return configured, nil, nil
	}
	traceFile, err := os.Create(filepath.Join(workspace, "trace.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	return traceFile, traceFile, nil
}
