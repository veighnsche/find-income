// Package codexrunner supervises one private App Server inside an already
// isolated runner. It does not create a security boundary or provision a runner.
package codexrunner

import (
	"context"
	"errors"
	"io"
)

var (
	ErrConfiguration = errors.New("codex runner: invalid configuration")
	ErrExecutable    = errors.New("codex runner: executable verification failed")
	ErrStart         = errors.New("codex runner: start failed")
	ErrClosed        = errors.New("codex runner: closed")
	ErrCanceled      = errors.New("codex runner: canceled")
	ErrDeadline      = errors.New("codex runner: deadline exceeded")
	ErrExited        = errors.New("codex runner: process exited")
	ErrTransport     = errors.New("codex runner: transport ended")
	ErrCleanup       = errors.New("codex runner: cleanup failed")
	ErrUnsupported   = errors.New("codex runner: unsupported platform")
)

// Config is trusted runner-local configuration, never a browser request.
// All paths must be absolute, canonical and supplied explicitly. StateDir and
// WorkDir must already exist, be distinct/nonoverlapping, and owned mode 0700.
// They must be dedicated to this runner with no dashboard/secret mounts.
// Executable and its parent directories must be immutable to untrusted writers
// throughout launch; SHA256 verifies contents, not the deployment boundary.
type Config struct {
	Executable string
	SHA256     string
	StateDir   string
	WorkDir    string
}

// Process satisfies codex.Transport without importing the protocol client.
// Close unblocks concurrent Read/Write and joins the lifecycle goroutine.
// Done closes after leader reaping and owned-group kill delivery. Descendants
// are not our children and require the runner's init/subreaper to reap zombies.
type Process struct{ impl *process }

func Start(ctx context.Context, cfg Config) (*Process, error) { return start(ctx, cfg) }
func (p *Process) Read(b []byte) (int, error)                 { return p.impl.read(b) }
func (p *Process) Write(b []byte) (int, error)                { return p.impl.write(b) }
func (p *Process) Close() error                               { return p.impl.close() }
func (p *Process) Done() <-chan struct{}                      { return p.impl.doneChan() }

// Err returns a fixed terminal reason (nil until Done). It never contains
// executable paths, raw OS errors, stdout, stderr or child configuration.
func (p *Process) Err() error { return p.impl.err() }

var _ io.ReadWriteCloser = (*Process)(nil)

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrDeadline
	}
	return ErrCanceled
}
