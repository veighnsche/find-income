package musewire

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

func TestStandardTransportRefusesPublic(t *testing.T) {
	transport := &StandardTransport{CLIPath: "/nonexistent", ModelID: "m", ProviderID: "p"}
	err := transport.Run(context.Background(), musecode.SessionSpec{},
		musecode.PublicInput{}, musecode.Cursor{}, &collectSink{})
	if err == nil || !strings.Contains(err.Error(), "private preparation only") {
		t.Fatalf("public err = %v, want private-preparation-only refusal", err)
	}
}

func TestStandardPromptGuards(t *testing.T) {
	if _, err := standardPrompt(musecode.StandardInput{Purpose: "discover-jobs",
		Context: map[string]string{"prompt": "facts"}}); err == nil {
		t.Fatal("unknown purpose accepted")
	}
	if _, err := standardPrompt(musecode.StandardInput{Purpose: standardDraftPurpose}); err == nil {
		t.Fatal("empty verified-fact prompt accepted")
	}
	oversize := musecode.StandardInput{Purpose: standardDraftPurpose,
		Context: map[string]string{"prompt": strings.Repeat("f", maxStandardPromptBytes+1)}}
	if _, err := standardPrompt(oversize); err == nil {
		t.Fatal("oversize prompt accepted")
	}
	draft, err := standardPrompt(musecode.StandardInput{Purpose: standardDraftPurpose,
		Context: map[string]string{"prompt": "VERIFIED-FACTS"}, Targets: []string{"q1"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"verified facts only", "VERIFIED-FACTS", `"drafts"`} {
		if !strings.Contains(draft, want) {
			t.Fatalf("draft prompt missing %q", want)
		}
	}
	rewrite, err := standardPrompt(musecode.StandardInput{Purpose: standardRewritePurpose,
		Context: map[string]string{"prompt": "VERIFIED-FACTS"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"owner instruction", `"texts"`} {
		if !strings.Contains(rewrite, want) {
			t.Fatalf("rewrite prompt missing %q", want)
		}
	}
	artifacts, err := standardPrompt(musecode.StandardInput{Purpose: standardArtifactPurpose,
		Context: map[string]string{"prompt": "VERIFIED-FACTS"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"verified facts only", "VERIFIED-FACTS", `"artifacts"`} {
		if !strings.Contains(artifacts, want) {
			t.Fatalf("artifact prompt missing %q", want)
		}
	}
}

func TestStandardSchemasMirrorDisciplines(t *testing.T) {
	for purpose, key := range map[string]string{
		standardDraftPurpose:    "drafts",
		standardRewritePurpose:  "texts",
		standardArtifactPurpose: "artifacts",
	} {
		raw, err := standardSchemaJSON(purpose)
		if err != nil {
			t.Fatalf("%s schema: %v", purpose, err)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatalf("%s schema is not JSON: %v", purpose, err)
		}
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("%s schema misses %q", purpose, key)
		}
		found := false
		for _, req := range schema.Required {
			found = found || req == key
		}
		if !found {
			t.Errorf("%s schema does not require %q", purpose, key)
		}
		discipline, err := standardDiscipline(purpose)
		if err != nil {
			t.Fatalf("%s discipline: %v", purpose, err)
		}
		if !strings.Contains(discipline, `"`+key+`"`) {
			t.Errorf("%s discipline does not name its schema root %q", purpose, key)
		}
	}
	if _, err := standardSchemaJSON("discover-jobs"); err == nil {
		t.Fatal("unknown purpose accepted a schema")
	}
}

func TestExecTextReadsDeltaAndTerminal(t *testing.T) {
	kind, text := execText([]byte(`{"payload_type":"run.output.delta","payload":{"text":"part1"}}`))
	if kind != "run.output.delta" || text != "part1" {
		t.Fatalf("delta = %q %q", kind, text)
	}
	kind, text = execText([]byte(`{"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":"full answer"}}`))
	if kind != "run.terminal.completed" || text != "full answer" {
		t.Fatalf("terminal = %q %q", kind, text)
	}
	if kind, text := execText([]byte(`muse: not json`)); kind != "" || text != "" {
		t.Fatalf("non-JSON = %q %q, want empty", kind, text)
	}
	if _, text := execText([]byte(`{"payload_type":"task.lifecycle.started","payload":{}}`)); text != "" {
		t.Fatalf("lifecycle text = %q, want empty", text)
	}
}

func TestStandardTransportEchoPlumbing(t *testing.T) {
	if os.Getenv("E13_ECHO") != "1" {
		t.Skip("echo plumbing only with E13_ECHO=1")
	}
	cli, err := exec.LookPath("muse")
	if err != nil {
		t.Skip("muse CLI not on PATH")
	}
	workspace := t.TempDir()
	runRef := "e13-echo-plumbing"
	workspace = filepath.Join(workspace, runRef)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := musecode.SessionSpec{Tier: musecode.TierStandard, Workspace: workspace, Public: false,
		Bounds: musecode.Bounds{MaxWallClock: time.Minute, MaxModelSteps: 5, MaxToolCalls: 5,
			MaxBytesPerOp: 1 << 20, MaxBytesTotal: 4 << 20}}
	transport := &StandardTransport{CLIPath: cli, ModelID: "echo", ProviderID: "echo",
		Provider: "echo", Trace: &strings.Builder{}}
	sink := &collectSink{}
	input := musecode.StandardInput{Purpose: standardDraftPurpose, BundleRef: "check-echo",
		Context: map[string]string{"prompt": "Return exactly: ECHO-DRAFT-OK"}, Targets: []string{"q-echo"}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	if err := transport.Run(ctx, spec, input, musecode.Cursor{}, sink); err != nil {
		t.Fatalf("echo run: %v", err)
	}
	var texts []string
	var finished bool
	for _, event := range sink.events {
		switch event.Kind {
		case musecode.EventModelText:
			texts = append(texts, event.Text)
			if event.BytesOut != int64(len(event.Text)) {
				t.Fatalf("text bytes = %d, want %d", event.BytesOut, len(event.Text))
			}
		case musecode.EventToolCall, musecode.EventToolResult:
			t.Fatalf("echo run called a tool: %+v", event)
		case musecode.EventFinished:
			finished = true
		}
	}
	if !finished || len(texts) != 1 || !strings.Contains(texts[0], "ECHO-DRAFT-OK") {
		t.Fatalf("echo texts = %q finished=%v", texts, finished)
	}
}
