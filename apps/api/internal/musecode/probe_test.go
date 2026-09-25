package musecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseVersionLine(t *testing.T) {
	cases := map[string]string{
		"Muse Code 1.4.0 (1.4.0-R4161.1)\n": "1.4.0",
		"Muse Code 1.4.0":                   "1.4.0",
		"":                                  "",
		"codex 1.0\n":                       "",
		"Muse":                              "",
	}
	for line, want := range cases {
		if got := parseVersionLine(line); got != want {
			t.Errorf("parse %q = %q, want %q", line, got, want)
		}
	}
}

func TestProbeLocalFactsNeverCallsAModel(t *testing.T) {
	if facts := ProbeLocalFacts(""); facts.CLIPath != "" || facts.CLIReportVersion != "" {
		t.Fatalf("empty path facts = %+v, want empty", facts)
	}
	if facts := ProbeLocalFacts(filepath.Join(t.TempDir(), "absent-muse")); facts.CLIPath == "" || facts.CLIReportVersion != "" {
		t.Fatalf("absent binary facts = %+v, want path without version", facts)
	}
	if facts := ProbeLocalFacts("definitely-absent-muse-binary"); facts != (Facts{}) {
		t.Fatalf("unresolvable name facts = %+v, want empty", facts)
	}
	// The probe shells one metadata-only --version call; the fixture binary
	// proves parsing without touching the real CLI.
	dir := t.TempDir()
	bin := filepath.Join(dir, "muse")
	script := "#!/bin/sh\necho 'Muse Code 1.4.0 (fixture)'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	facts := ProbeLocalFacts(bin)
	if facts.CLIPath != bin || facts.CLIReportVersion != "1.4.0" {
		t.Fatalf("facts = %+v, want path + 1.4.0", facts)
	}
	if facts.SubscriptionLaneProved || facts.SessionProtocolProved || facts.WorkspaceIsolatedProved {
		t.Fatalf("facts = %+v, want nothing proved", facts)
	}
	if status := Check(TierContributor, facts); status.Available || status.Code != CodeLaneUnverified {
		t.Fatalf("status = %+v, want lane-unverified", status)
	}
}

func TestUnavailableTransportFailsClosed(t *testing.T) {
	var transport Transport = UnavailableTransport{}
	err := transport.Run(context.Background(), SessionSpec{}, PublicInput{}, Cursor{}, nil)
	if err == nil {
		t.Fatal("unavailable transport admitted a run")
	}
}
