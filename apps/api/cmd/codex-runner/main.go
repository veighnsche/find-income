// codex-runner is installed only on the separately verified isolated host.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexrunner"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	p, err := codexrunner.Start(ctx, codexrunner.Config{Executable: os.Getenv("JOBSEEK_RUNNER_CODEX_BINARY"), SHA256: os.Getenv("JOBSEEK_RUNNER_CODEX_SHA256"), StateDir: os.Getenv("JOBSEEK_RUNNER_STATE_DIR"), WorkDir: os.Getenv("JOBSEEK_RUNNER_WORK_DIR")})
	if err != nil {
		os.Exit(1)
	}
	defer p.Close()
	go func() { _, _ = io.Copy(p, os.Stdin); cancel() }()
	_, _ = io.Copy(os.Stdout, p)
	cancel()
	_ = p.Close()
}
