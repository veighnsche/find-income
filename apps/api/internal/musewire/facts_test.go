package musewire

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// fakeHome seeds a HOME dir with owner CLI credentials and returns a fake
// pinned-CLI path that reports its version without any model call.
func fakeHome(t *testing.T, withAuth bool) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "muse")
	script := "#!/bin/sh\necho 'Muse Code 1.4.0-fixture (test)'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if withAuth {
		dir := filepath.Join(home, ".config", "muse")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"fixture":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func TestLiveFactsProveLaneFromCredentialPresence(t *testing.T) {
	bin := fakeHome(t, true)
	facts := LiveFacts(bin, musecode.PinnedModelID)
	if facts.CLIPath == "" || facts.CLIReportVersion == "" {
		t.Fatalf("cli facts missing: %+v", facts)
	}
	if facts.EffectiveModel != musecode.PinnedModelID || !facts.SubscriptionLaneProved ||
		!facts.SessionProtocolProved || !facts.WorkspaceIsolatedProved {
		t.Fatalf("proved facts missing: %+v", facts)
	}
	if status := musecode.Check(musecode.TierContributor, facts); !status.Available {
		t.Fatalf("contributor status = %+v, want available", status)
	}
}

func TestLiveFactsFailClosed(t *testing.T) {
	bin := fakeHome(t, false)
	if facts := LiveFacts(bin, musecode.PinnedModelID); facts.SubscriptionLaneProved || facts.EffectiveModel != "" {
		t.Fatalf("lane proved without credentials: %+v", facts)
	}
	bin = fakeHome(t, true)
	if facts := LiveFacts(bin, ""); facts.SubscriptionLaneProved || facts.EffectiveModel != "" {
		t.Fatalf("lane proved without model pin: %+v", facts)
	}
	missing := LiveFacts(filepath.Join(t.TempDir(), "missing"), musecode.PinnedModelID)
	if missing.CLIReportVersion != "" || missing.SubscriptionLaneProved {
		t.Fatalf("facts proved without binary: %+v", missing)
	}
	if status := musecode.Check(musecode.TierContributor, missing); status.Available {
		t.Fatalf("contributor available without binary: %+v", status)
	}
}

func TestCommissionedRunRef(t *testing.T) {
	ref, ok := CommissionedRunRef(store.Round{Intent: CommissionIntent, RequestKey: "muse:run-9"})
	if !ok || ref != "run-9" {
		t.Fatalf("run ref = %q, %v", ref, ok)
	}
	for _, round := range []store.Round{
		{Intent: "Research run", RequestKey: "run-9"},
		{Intent: CommissionIntent, RequestKey: "run-9"},
		{Intent: CommissionIntent, RequestKey: "muse:../escape"},
	} {
		if ref, ok := CommissionedRunRef(round); ok {
			t.Fatalf("round %+v resolved to %q", round, ref)
		}
	}
}
