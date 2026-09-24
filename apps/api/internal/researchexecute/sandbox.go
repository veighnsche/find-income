package researchexecute

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// Subprocess egress confinement (closes the T02 §8 gaps on macOS). Browser
// and exec children run under a per-operation seatbelt profile that denies
// all outbound IP networking except to the operation's own recording proxy
// port on loopback. The profile was verified empirically: direct external
// fetches fail (DNS + connect denied), raw external sockets fail with EPERM,
// other loopback ports fail, and proxied requests through the allowed port
// succeed with full observation (see the T16 note).
//
// The Linux confinement backend is a T22 dependency: until it exists, running
// without a working sandbox binary fails the affected kinds closed with an
// explicit error code instead of running unmediated.

var errSandboxUnavailable = errors.New("sandbox backend unavailable")

// defaultSandboxBinary is the macOS seatbelt frontend.
const defaultSandboxBinary = "/usr/bin/sandbox-exec"

// sandboxProfile renders the per-operation profile. Only the proxy port
// varies; it is validated numeric so profile text can never inject.
func sandboxProfile(proxyPort string) (string, error) {
	port, err := strconv.Atoi(proxyPort)
	if err != nil || port <= 0 || port > 65535 {
		return "", fmt.Errorf("sandbox profile requires a numeric proxy port: %q", proxyPort)
	}
	return fmt.Sprintf(`(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip "localhost:%d"))`, port), nil
}

// checkSandbox verifies the sandbox binary exists and is executable.
func checkSandbox(sandboxBinary string) error {
	if sandboxBinary == "" {
		return fmt.Errorf("%w: no sandbox binary configured", errSandboxUnavailable)
	}
	info, err := os.Stat(sandboxBinary)
	if err != nil {
		return fmt.Errorf("%w: %s", errSandboxUnavailable, sandboxBinary)
	}
	if info.IsDir() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("%w: %s is not executable", errSandboxUnavailable, sandboxBinary)
	}
	return nil
}

// sandboxCommand builds a context-bound child confined to the proxy port. dir
// scopes the child's working directory; env fully replaces the environment
// (nothing is inherited).
func sandboxCommand(ctx context.Context, sandboxBinary, proxyPort, dir string, env []string, name string, args ...string) (*exec.Cmd, error) {
	if err := checkSandbox(sandboxBinary); err != nil {
		return nil, err
	}
	profile, err := sandboxProfile(proxyPort)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, errors.New("sandbox command requires a binary path")
	}
	argv := append([]string{"-p", profile, name}, args...)
	cmd := exec.CommandContext(ctx, sandboxBinary, argv...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd, nil
}
