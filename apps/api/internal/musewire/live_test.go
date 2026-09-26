package musewire

// Fixtures for the direct exec transport. Parser, prompt and schema
// checks run ungated on synthetic input. TestLiveTransportEchoPlumbing
// drives the real installed CLI with the deterministic echo provider:
// zero model spend, zero credentials, no listener, no MCP. It runs
// whenever the CLI is on PATH. TestLiveTransportValidationTurn drives
// one trivial turn on the Contributor lane and runs only with
// E11_VALIDATE=1.

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
)

func liveFixtureBounds() musecode.Bounds {
	return musecode.Bounds{MaxWallClock: time.Minute, MaxModelSteps: 10,
		MaxToolCalls: 50, MaxBytesPerOp: 1 << 20, MaxBytesTotal: 10 << 20}
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
		{"native tool call and result",
			[]string{
				propose("t1", "tool.web_search"),
				lifecycle("task.lifecycle.started", "t1"),
				line("tool.result", `{"text":"{}","correlation_facts":{"tool_name":"web_search","outcome":"success"}}`),
				lifecycle("task.lifecycle.completed", "t1"),
			},
			[]expectation{{}, {kind: "toolCall", tool: "web_search"},
				{kind: "toolResult", tool: "web_search", bytes: 2},
				{kind: "toolResult", tool: "web_search"}}},
		{"subagent forbidden",
			[]string{propose("s1", "subagent.spawn"), lifecycle("task.lifecycle.started", "s1")},
			[]expectation{{}, {kind: "forbidden", tool: "subagent.spawn"}}},
		{"failed tool is a result",
			[]string{propose("t2", "tool.web_fetch"), lifecycle("task.lifecycle.failed", "t2")},
			[]expectation{{}, {kind: "toolResult", tool: "web_fetch"}}},
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
	if got := exitDetail("contributor turn exceeded the total text bound", "", waitErr); got != "contributor turn exceeded the total text bound" {
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

func TestBareToolName(t *testing.T) {
	for in, want := range map[string]string{
		"web_search":       "web_search",
		"tool.web_fetch":   "web_fetch",
		"tool.tool.nested": "tool.nested",
		"shell":            "shell",
		"":                 "",
	} {
		if got := bareToolName(in); got != want {
			t.Errorf("bare %q = %q, want %q", in, got, want)
		}
	}
}

func TestExecArgsFor(t *testing.T) {
	meta := execArgsFor("p.txt", "s.json", "meta", "muse-spark-1.3", "max", 20, 1<<20, "/ws/run-1", true)
	joined := strings.Join(meta, " ")
	for _, want := range []string{"exec", "--json", "--prompt-file p.txt", "--output-schema s.json",
		"--provider meta", "--model muse-spark-1.3", "--max-model-steps 20",
		"--workspace /ws/run-1", "--approval-mode never", "--disable-shell", "--disable-write",
		"--no-foreign-personal-context"} {
		if !strings.Contains(joined, want) {
			t.Errorf("meta args miss %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--disable-web-tools") {
		t.Errorf("contributor args disable web tools: %s", joined)
	}
	capped := execArgsFor("p.txt", "s.json", "meta", "m", "max", 500, 1<<20, "/ws", true)
	if !strings.Contains(strings.Join(capped, " "), "--max-model-steps 100") {
		t.Errorf("host step cap not applied: %v", capped)
	}
	echo := execArgsFor("p.txt", "s.json", "echo", "m", "max", 5, 1<<20, "/ws", true)
	echoJoined := strings.Join(echo, " ")
	if strings.Contains(echoJoined, "--output-schema") || strings.Contains(echoJoined, "--model") {
		t.Errorf("echo args must omit schema and model: %s", echoJoined)
	}
	standard := execArgsFor("p.txt", "s.json", "meta", "m", "high", 5, 1<<20, "/ws", false)
	if !strings.Contains(strings.Join(standard, " "), "--disable-web-tools") {
		t.Errorf("standard args must disable web tools: %v", standard)
	}
}

func TestContributorSchemasAreStrictJSON(t *testing.T) {
	for name, raw := range map[string]string{"discovery": discoverySchemaJSON, "check": checkSchemaJSON} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatalf("%s schema is not JSON: %v", name, err)
		}
		props, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		if len(props) == 0 || len(required) == 0 || schema["additionalProperties"] != false {
			t.Fatalf("%s schema is not strict: %s", name, raw)
		}
	}
	var discovery struct {
		Properties struct {
			Vacancies struct {
				Items struct {
					Required []string `json:"required"`
				} `json:"items"`
			} `json:"vacancies"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(discoverySchemaJSON), &discovery); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"page_url", "employer_name", "title"} {
		found := false
		for _, key := range discovery.Properties.Vacancies.Items.Required {
			found = found || key == want
		}
		if !found {
			t.Errorf("discovery vacancy misses required %q", want)
		}
	}
}

func TestDiscoveryPromptUsesNativeTools(t *testing.T) {
	prompt := discoveryPrompt(musecode.PublicCriteria{RoleKeywords: []string{"support"},
		RegionText: "Amsterdam", SkillKeywords: []string{"go"}})
	for _, want := range []string{"support", "Amsterdam", "go", "web_search", "web_fetch",
		"no other tool", "never invent", `"vacancies"`, `"page_url"`, "re-fetches every URL itself"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("discovery prompt misses %q:\n%s", want, prompt)
		}
	}
	for _, banned := range []string{"public_search", "public_fetch", "public_save", "public_list", "MCP", "mcp"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("discovery prompt names removed tooling %q:\n%s", banned, prompt)
		}
	}
}

func TestResumeRefsListsPriorSaves(t *testing.T) {
	text := resumeRefs(musecode.Cursor{RunRef: "run-1", SavedRefs: []string{"vac-0001", "vac-0002"}})
	for _, want := range []string{"CONTINUES an earlier session", "already saved 2 openings",
		"vac-0001", "vac-0002", "Do not report them again"} {
		if !strings.Contains(text, want) {
			t.Errorf("continuation misses %q:\n%s", want, text)
		}
	}
	empty := resumeRefs(musecode.Cursor{RunRef: "run-1"})
	if !strings.Contains(empty, "already saved 0 openings") {
		t.Errorf("empty continuation = %q", empty)
	}
}

func TestCheckPromptContractsAdapterSchema(t *testing.T) {
	prompt := checkPrompt(musecode.CheckInput{VacancyRef: "vac-1",
		PageURL: "https://jobs.example.invalid/1", ReceiptRef: "rc-1"})
	for _, want := range []string{"vac-1", "https://jobs.example.invalid/1", "rc-1",
		"web_fetch", "never report other vacancies",
		`"requirements"`, `"route"`, `"documents"`, `"questions"`,
		`"source"`, "copied exactly", "never invent"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("check prompt misses %q:\n%s", want, prompt)
		}
	}
	for _, banned := range []string{"public_fetch", "public_save", "public_search", "public_list",
		"Never save vacancies", "MCP", "mcp", `"capture"`, `"ref"`} {
		if strings.Contains(prompt, banned) {
			t.Errorf("check prompt names removed tooling %q:\n%s", banned, prompt)
		}
	}
}

func TestLiveTransportRefusesStandard(t *testing.T) {
	transport := &LiveTransport{CLIPath: "/nonexistent", ModelID: "m", ProviderID: "p"}
	err := transport.Run(context.Background(), musecode.SessionSpec{},
		musecode.StandardInput{Purpose: "x"}, musecode.Cursor{}, &collectSink{})
	if err == nil || !strings.Contains(err.Error(), "contributor discovery and checks only") {
		t.Fatalf("standard err = %v, want contributor-only refusal", err)
	}
}

func TestLiveTransportMissingCLIFailsHonest(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "ws", "run-missing-cli")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	transport := &LiveTransport{CLIPath: filepath.Join(t.TempDir(), "no-such-cli"),
		ModelID: "m", ProviderID: "p", Trace: &strings.Builder{}}
	err := transport.Run(context.Background(), musecode.SessionSpec{Tier: musecode.TierContributor,
		Workspace: workspace, Public: true, Bounds: liveFixtureBounds()},
		musecode.PublicInput{Criteria: musecode.PublicCriteria{RoleKeywords: []string{"support"}}},
		musecode.Cursor{}, &collectSink{})
	if err == nil {
		t.Fatal("missing CLI accepted")
	}
}

// TestLiveTransportEchoPlumbing is the R1 zero-spend proof: the real
// installed CLI conducts one direct turn with the echo provider (no
// model spend, no credentials, no listener, no MCP), the transport
// folds its JSONL to a finished turn carrying the model text, and the
// workspace holds prompt, schema and trace but no isolated config home.
func TestLiveTransportEchoPlumbing(t *testing.T) {
	cli, err := exec.LookPath("muse")
	if err != nil {
		t.Skip("muse CLI not on PATH")
	}
	workspace := filepath.Join(t.TempDir(), "ws", "run-live-echo")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	var trace strings.Builder
	transport := &LiveTransport{CLIPath: cli, ModelID: "muse-spark-1.3",
		ProviderID: "meta", Provider: "echo", Trace: &trace}
	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := transport.Run(ctx, musecode.SessionSpec{Tier: musecode.TierContributor,
		Workspace: workspace, Public: true, Bounds: liveFixtureBounds()},
		musecode.PublicInput{Criteria: musecode.PublicCriteria{RoleKeywords: []string{"support"}}},
		musecode.Cursor{}, sink); err != nil {
		t.Fatalf("echo run: %v", err)
	}
	var finished, sawText, sawStep bool
	for _, event := range sink.events {
		switch event.Kind {
		case musecode.EventFinished:
			finished = true
		case musecode.EventModelText:
			sawText = true
			if event.BytesOut != int64(len(event.Text)) || event.Text == "" {
				t.Fatalf("text event = %+v, want accounted non-empty text", event)
			}
		case musecode.EventModelStep:
			sawStep = true
		case musecode.EventToolCall:
			if !musecode.ContributorToolAllowed(event.Tool) {
				t.Fatalf("echo run called non-allowlisted tool %q", event.Tool)
			}
		}
	}
	if !finished || !sawText || !sawStep {
		t.Fatalf("kinds = %v, want step + text + finished", sink.kinds())
	}
	if !strings.Contains(trace.String(), "run.terminal.completed") {
		t.Errorf("trace misses terminal completion")
	}
	for _, name := range []string{"prompt.txt", "schema.json"} {
		raw, err := os.ReadFile(filepath.Join(workspace, name))
		if err != nil || len(raw) == 0 {
			t.Errorf("workspace %s: %v (empty or missing)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, "mushome")); !os.IsNotExist(err) {
		t.Errorf("workspace holds an isolated config home; direct invocation must not build one")
	}
	// Without a configured sink the transport persists the exec JSONL to
	// the run workspace, so failed runs stay debuggable after exit.
	fallbackWorkspace := filepath.Join(t.TempDir(), "ws", "run-live-echo-fallback")
	if err := os.MkdirAll(fallbackWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	fallback := &LiveTransport{CLIPath: cli, ModelID: "muse-spark-1.3",
		ProviderID: "meta", Provider: "echo"}
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
// exists to prove direct invocation, the approval default and the
// structured-text return before live discovery runs.
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
	transport := &LiveTransport{CLIPath: cli, ModelID: "muse-spark-1.3",
		ProviderID: "meta",
		Trace:      traceFile,
		ValidationPrompt: "Do no retrieval and call no tool. Reply with exactly one JSON object " +
			`{"vacancies":[],"sources_searched":[],"gaps":["validation turn"]}, and nothing else.`}
	sink := &collectSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := transport.Run(ctx, musecode.SessionSpec{Tier: musecode.TierContributor,
		Workspace: workspace, Public: true, Bounds: liveFixtureBounds()},
		musecode.PublicInput{Criteria: musecode.PublicCriteria{RoleKeywords: []string{"support"}}},
		musecode.Cursor{}, sink); err != nil {
		t.Fatalf("validation turn: %v", err)
	}
	var finished, sawText bool
	for _, event := range sink.events {
		switch event.Kind {
		case musecode.EventModelText:
			sawText = true
		case musecode.EventFinished:
			finished = true
		}
	}
	if !finished || !sawText {
		t.Errorf("kinds = %v, want text + finished", sink.kinds())
	}
}
