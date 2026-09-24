package codexservice

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLocalConfigValidation(t *testing.T) {
	token := strings.Repeat("b", 64)
	sha := strings.Repeat("c", 64)
	valid := Config{LocalRunner: "/bin/runner", LocalCodex: "/bin/codex", LocalCodexSHA256: sha, LocalStateDir: "/state", LocalWorkDir: "/work", BridgeToken: token}
	if !valid.Local() || valid.unavailableCode() != "" {
		t.Fatalf("valid local config rejected: %q", valid.unavailableCode())
	}
	for name, mutate := range map[string]func(*Config){
		"missing codex":   func(c *Config) { c.LocalCodex = "" },
		"missing state":   func(c *Config) { c.LocalStateDir = "" },
		"missing work":    func(c *Config) { c.LocalWorkDir = "" },
		"short pin":       func(c *Config) { c.LocalCodexSHA256 = "abc" },
		"relative runner": func(c *Config) { c.LocalRunner = "bin/runner" },
		"short token":     func(c *Config) { c.BridgeToken = "short" },
	} {
		broken := valid
		mutate(&broken)
		if broken.unavailableCode() == "" {
			t.Fatalf("%s accepted", name)
		}
	}
	if (Config{}).Local() {
		t.Fatal("empty config reports local")
	}
}

func TestDialLocalRefusesBadConfigAndMissingBinary(t *testing.T) {
	ctx := context.Background()
	if _, err := dialLocal(ctx, Config{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty config: %v", err)
	}
	if _, err := dialLocal(nil, Config{LocalRunner: "/bin/runner"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil context: %v", err)
	}
	missing := Config{LocalRunner: "/nonexistent/runner-binary", LocalCodex: "/bin/codex", LocalCodexSHA256: strings.Repeat("c", 64), LocalStateDir: "/state", LocalWorkDir: "/work", BridgeToken: strings.Repeat("b", 64)}
	if _, err := dialLocal(ctx, missing); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing binary: %v", err)
	}
}

func TestNewSelectsLocalDial(t *testing.T) {
	s, _ := testService(t, Config{LocalRunner: "/bin/runner", LocalCodex: "/bin/codex", LocalCodexSHA256: strings.Repeat("c", 64), LocalStateDir: "/state", LocalWorkDir: "/work", BridgeToken: strings.Repeat("b", 64)})
	if !s.cfg.Local() {
		t.Fatal("local config not retained")
	}
}
