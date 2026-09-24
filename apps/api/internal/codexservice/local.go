package codexservice

import (
	"context"
	"io"
	"os/exec"
)

// dialLocal spawns the configured runner binary on the app machine and speaks
// the same App Server transport over its stdio. It is selected only by an
// explicit JOBSEEK_CODEX_LOCAL_RUNNER setting for owner-accepted localhost
// runs; status surfaces local mode and no runner isolation is claimed.
func dialLocal(ctx context.Context, cfg Config) (io.ReadWriteCloser, error) {
	if ctx == nil || !cfg.Local() || cfg.unavailableCode() != "" {
		return nil, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, cfg.LocalRunner)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC",
		"JOBSEEK_RUNNER_CODEX_BINARY=" + cfg.LocalCodex, "JOBSEEK_RUNNER_CODEX_SHA256=" + cfg.LocalCodexSHA256,
		"JOBSEEK_RUNNER_STATE_DIR=" + cfg.LocalStateDir, "JOBSEEK_RUNNER_WORK_DIR=" + cfg.LocalWorkDir}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, ErrUnavailable
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, ErrUnavailable
	}
	return &localTransport{stdin: stdin, stdout: stdout, cmd: cmd}, nil
}

type localTransport struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (t *localTransport) Read(p []byte) (int, error)  { return t.stdout.Read(p) }
func (t *localTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }
func (t *localTransport) Close() error {
	stdinErr := t.stdin.Close()
	waitErr := t.cmd.Wait()
	_ = t.stdout.Close()
	if stdinErr != nil {
		return stdinErr
	}
	return waitErr
}
