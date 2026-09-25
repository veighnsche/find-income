package musewire

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func standardFactoryConfig(workspaces string) StandardRunnerConfig {
	return StandardRunnerConfig{CLIPath: "/bin/muse", ModelID: "muse-spark-1.3-standard",
		ProviderID: "meta", Cursors: &runnerMemoryCursors{}, Facts: standardRunnerFacts(),
		Bounds: standardRunnerSpec().Bounds, Workspaces: workspaces}
}

func TestNewStandardRunnerFactoryGates(t *testing.T) {
	workspaces := t.TempDir()
	if _, err := NewStandardRunnerFactory(standardFactoryConfig(workspaces)); err != nil {
		t.Fatalf("valid factory: %v", err)
	}
	unproved := standardFactoryConfig(workspaces)
	unproved.Facts.SubscriptionLaneProved = false
	if _, err := NewStandardRunnerFactory(unproved); err == nil {
		t.Fatal("unproved lane admitted")
	}
	relative := standardFactoryConfig("relative/workspaces")
	if _, err := NewStandardRunnerFactory(relative); err == nil {
		t.Fatal("relative workspaces admitted")
	}
	noCLI := standardFactoryConfig(workspaces)
	noCLI.CLIPath = ""
	if _, err := NewStandardRunnerFactory(noCLI); err == nil {
		t.Fatal("empty CLI path admitted")
	}
}

func TestStandardRunnerFactoryEchoEndToEnd(t *testing.T) {
	if os.Getenv("E13_ECHO") != "1" {
		t.Skip("echo plumbing only with E13_ECHO=1")
	}
	cli, err := exec.LookPath("muse")
	if err != nil {
		t.Skip("muse CLI not on PATH")
	}
	workspaces := t.TempDir()
	config := standardFactoryConfig(workspaces)
	config.CLIPath = cli
	config.Provider = "echo"
	config.Bounds.MaxWallClock = time.Minute
	factory, err := NewStandardRunnerFactory(config)
	if err != nil {
		t.Fatal(err)
	}
	input := standardRunnerInput()
	input.Context = map[string]string{"prompt": "Return exactly: ECHO-FACTORY-OK"}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	result, err := factory.RunStandard(ctx, input)
	if err != nil {
		t.Fatalf("echo factory run: %v", err)
	}
	if len(result.Messages) != 1 || !strings.Contains(result.Messages[0], "ECHO-FACTORY-OK") {
		t.Fatalf("messages = %q", result.Messages)
	}
	entries, err := os.ReadDir(filepath.Join(workspaces, "standard"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("standard workspaces = %v %v, want exactly one run dir", entries, err)
	}
	trace := filepath.Join(workspaces, "standard", entries[0].Name(), "trace.jsonl")
	if info, err := os.Stat(trace); err != nil || info.Size() == 0 {
		t.Fatalf("trace = %v %v, want a non-empty trace", info, err)
	}
}
