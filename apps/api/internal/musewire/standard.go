package musewire

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// Standard model purposes. They select the hardcoded drafting discipline;
// only these three are admitted, and the discipline never comes from
// caller input. Values match materialprep.StandardDraftPurpose,
// materialprep.StandardRewritePurpose, and
// materialprep.StandardArtifactPurpose without importing that package:
// the transport must not depend on the preparation adapter.
const (
	standardDraftPurpose    = "prepare-draft-required-answers"
	standardRewritePurpose  = "prepare-rewrite-materials"
	standardArtifactPurpose = "prepare-draft-artifacts"
)

// standardDiscipline renders the trusted instructions for one purpose,
// equivalent to the legacy DraftInstructions/RewriteInstructions plus the
// exact accepted output shape. Unknown purposes are refused.
func standardDiscipline(purpose string) (string, error) {
	switch purpose {
	case standardDraftPurpose:
		return `You draft employer-question answers from supplied verified facts only. ` +
			`Use only the facts in the prompt: never invent experience, dates, credentials, or availability, and never contact anyone or browse. ` +
			`Cite an exact approved-source excerpt for every line. Omit any question the facts cannot support. ` +
			`Reply with exactly one JSON object ` +
			`{"drafts":[{"questionId":"...","lines":[{"text":"...","citations":[{"sourceId":"...","excerpt":"..."}]}]}]}, ` +
			`raw or in a single fenced block, and nothing else.`, nil
	case standardRewritePurpose:
		return `Rewrite each listed employer-question answer according to the owner instruction, using only the verified facts in this prompt. ` +
			`Never invent experience, dates, credentials, or availability, and never contact anyone or browse. ` +
			`Every listed question id gets exactly one text; use the empty string for any question the facts cannot support. ` +
			`Reply with exactly one JSON object {"texts":[{"questionId":"...","text":"..."}]}, ` +
			`raw or in a single fenced block, and nothing else.`, nil
	case standardArtifactPurpose:
		return `Draft the listed application artifacts from the supplied verified facts only. ` +
			`Use only the facts in the prompt: never invent experience, dates, credentials, or availability, and never contact anyone or browse. ` +
			`Draft only the listed artifact types, at most once each; omit any type the facts cannot support. ` +
			`Reply with exactly one JSON object ` +
			`{"artifacts":[{"type":"...","content":"...","facts":["source-id"],"answers":["question-id"]}]}, ` +
			`raw or in a single fenced block, and nothing else.`, nil
	default:
		return "", fmt.Errorf("musewire: unknown standard purpose %q", purpose)
	}
}

// StandardTransport conducts private Standard preparation sessions on the
// live CLI. It accepts StandardInput only: verified owner facts travel in
// the prompt (allowed for Standard), while discovery criteria can never
// arrive because PublicInput is refused. The session gets no MCP tools,
// makes no retrieval calls, and any tool call fails the run closed;
// collected model texts return via EventModelText for adapter validation.
type StandardTransport struct {
	CLIPath    string
	ModelID    string
	ProviderID string
	Trace      io.Writer
	// Provider selects the model backend. Production uses "meta"; the
	// echo provider exists for zero-spend plumbing fixtures.
	Provider string
	// ValidationPrompt replaces the preparation prompt for harness-only
	// validation turns. Production leaves it empty; any non-empty value
	// is recorded in the trace.
	ValidationPrompt string
}

var _ musecode.Transport = (*StandardTransport)(nil)

// maxStandardPromptBytes caps the verified-fact prompt of one Standard
// turn. Oversize prompts are refused before any session starts so a
// runaway prompt can never spend Standard budget.
const maxStandardPromptBytes = 65536

// standardPrompt renders the hardcoded discipline plus the verified-fact
// prompt bytes. Targets scope logging only and are never expanded here.
func standardPrompt(input musecode.StandardInput) (string, error) {
	discipline, err := standardDiscipline(input.Purpose)
	if err != nil {
		return "", err
	}
	facts := strings.TrimSpace(input.Context["prompt"])
	if facts == "" {
		return "", errors.New("musewire: standard input carries no verified-fact prompt")
	}
	if len(facts) > maxStandardPromptBytes {
		return "", fmt.Errorf("musewire: standard prompt is %d bytes, over the %d-byte turn budget",
			len(facts), maxStandardPromptBytes)
	}
	return discipline + "\n\n" + facts, nil
}

// Run conducts one Standard preparation turn to transport-terminal state.
func (t *StandardTransport) Run(ctx context.Context, spec musecode.SessionSpec, input musecode.SessionInput, resume musecode.Cursor, sink musecode.EventSink) error {
	standard, ok := input.(musecode.StandardInput)
	if !ok {
		return errors.New("musewire: standard transport conducts private preparation only")
	}
	if resume.RunRef != "" {
		return errors.New("musewire: resume arrives with the first live run")
	}
	if t.CLIPath == "" || t.ModelID == "" || t.ProviderID == "" {
		return errors.New("musewire: standard transport needs CLI path, model and provider")
	}
	provider := t.Provider
	if provider == "" {
		provider = "meta"
	}
	runRef := path.Base(spec.Workspace)
	if !validRunRef(runRef) {
		return fmt.Errorf("musewire: workspace %q names no run", spec.Workspace)
	}
	trace := &traceWriter{w: t.Trace}
	trace.note("run %s workspace %s model %s provider %s backend %s purpose %s targets %d bundle %s",
		runRef, spec.Workspace, t.ModelID, t.ProviderID, provider, standard.Purpose, len(standard.Targets), standard.BundleRef)

	home, err := writeStandardHome(spec.Workspace, t.ModelID)
	if err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// The meta provider reads auth.json next to the active settings file.
	// Copy the owner's credential file into the isolated home unread: its
	// bytes are never parsed, printed, or proxied, and the copy lives and
	// dies with the 0700 run workspace.
	if provider == "meta" {
		credential, err := os.ReadFile(filepath.Join(homeDir, ".config", "muse", "auth.json"))
		if err != nil {
			return fmt.Errorf("musewire: owner CLI credentials unavailable: %w", err)
		}
		if err := os.WriteFile(filepath.Join(home, "config", "muse", "auth.json"), credential, 0o600); err != nil {
			return err
		}
	}
	prompt, err := standardPrompt(standard)
	if err != nil {
		return err
	}
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
		args = append(args, "--model", t.ModelID, "--reasoning-effort", "high")
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

	folder := newExecFolder(t.ModelID, t.ProviderID)
	var deltas strings.Builder
	deltaBytes := int64(0)
	finalText := ""
	textSet := false
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
			if kind, text := execText(line); kind == "run.output.delta" {
				deltaBytes += int64(len(text))
				if deltaBytes > spec.Bounds.MaxBytesTotal {
					detail = "standard turn exceeded the total text bound"
					stop()
					break loop
				}
				deltas.WriteString(text)
			} else if text != "" && strings.HasPrefix(kind, "run.terminal.") {
				finalText, textSet = text, true
			}
			kind, tool, bytes, step, done, terr := folder.fold(line)
			switch {
			case terr != "":
				detail = terr
				stop()
				break loop
			case step:
				sink.Emit(musecode.Event{Kind: musecode.EventModelStep})
			case kind == "toolCall":
				detail = "standard session called a tool: " + tool
				stop()
				break loop
			case kind == "toolResult":
				detail = "standard session observed a tool result: " + tool
				stop()
				break loop
			case kind == "forbidden":
				detail = "forbidden item kind " + tool
				stop()
				break loop
			case done:
				terminal = kind
				finished = true
				break loop
			}
			_ = bytes
		}
		if err != nil {
			break
		}
	}
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
	text := finalText
	if !textSet || text == "" {
		text = deltas.String()
	}
	if text == "" {
		runErr := errors.New("musewire: standard turn returned no text")
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	if int64(len(text)) > spec.Bounds.MaxBytesPerOp {
		runErr := errors.New("musewire: standard turn exceeded the per-operation text bound")
		sink.Emit(musecode.Event{Kind: musecode.EventFailed, Detail: runErr.Error()})
		return runErr
	}
	sink.Emit(musecode.Event{Kind: musecode.EventModelText, Text: text, BytesOut: int64(len(text))})
	sink.Emit(musecode.Event{Kind: musecode.EventFinished})
	return nil
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

// writeStandardHome builds an isolated XDG home under the run workspace: a
// config home whose settings pin only the Standard model with no MCP
// servers, tools, or foreign context, and an empty data home. The owner's
// configuration is never touched.
func writeStandardHome(workspace, modelID string) (string, error) {
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
		"model":            modelID,
		"reasoning_effort": "high",
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
