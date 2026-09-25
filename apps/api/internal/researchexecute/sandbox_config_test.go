package researchexecute

import "testing"

// The research sandbox binary must be configurable per host: the wire layer
// passes JOBSEEK_RESEARCH_SANDBOX_BINARY through, and empty keeps the macOS
// default. Without the explicit path a Linux host fails closed even when
// correctly installed.
func TestSandboxBinaryExplicitSurvivesDefaults(t *testing.T) {
	if got := (Config{SandboxBinary: "/usr/bin/bwrap"}).withDefaults().SandboxBinary; got != "/usr/bin/bwrap" {
		t.Fatalf("explicit sandbox binary: got %q, want /usr/bin/bwrap", got)
	}
	if got := (Config{}).withDefaults().SandboxBinary; got != defaultSandboxBinary {
		t.Fatalf("default sandbox binary: got %q, want %q", got, defaultSandboxBinary)
	}
}
