package researchexecute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// execResult is one fully mediated script run: captured output/exit plus the
// proxy's complete egress account.
type execResult struct {
	combined    []byte
	cut         bool
	exitCode    string
	observed    []ObservedRequest
	subrequests int
	bytesIn     int64
	bytesOut    int64
	identity    researchcontract.ExecutorIdentity
	runErr      error
}

// streamCap bounds each captured exec stream; the cut is always explicit.
const execStreamCap = 65536

// runExec executes code with the configured python3 under the per-operation
// sandbox: the child can only reach its recording proxy port on loopback, so
// direct fetches, raw sockets and alternate loopback ports all fail while
// proxy-env requests are observed at that boundary (host + bytes only for
// CONNECT tunnels). Stdout/stderr/exit are captured from the actual child;
// logging the command would not be proof.
func runExec(ctx context.Context, deps *opDeps, code string, maxReqs int) (execResult, error) {
	var out execResult
	identity, err := deps.backends.pythonIdentity(ctx)
	if err != nil {
		return out, err
	}
	out.identity = identity
	if err := checkSandbox(deps.sandboxBinary); err != nil {
		return out, err
	}
	proxy, err := newRecordingProxy(deps.forwardClient(), execStreamCap*2, maxReqs)
	if err != nil {
		return out, err
	}
	defer proxy.close()

	opHome, err := os.MkdirTemp(deps.scratchRoot, "xhome-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(opHome)
	script := filepath.Join(opHome, "program.py")
	if err := os.WriteFile(script, []byte(code), 0600); err != nil {
		return out, err
	}
	proxyURL := proxy.url()
	env := []string{
		"PATH=/usr/bin:/bin", "HOME=" + opHome, "TMPDIR=" + opHome, "LANG=C",
		"HTTP_PROXY=" + proxyURL, "HTTPS_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL, "https_proxy=" + proxyURL,
		"NO_PROXY=", "no_proxy=", "PYTHONDONTWRITEBYTECODE=1",
	}
	cmd, err := sandboxCommand(ctx, deps.sandboxBinary, proxy.port(), opHome, env,
		deps.backends.pythonPath, script)
	if err != nil {
		return out, err
	}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = execStreamCap, execStreamCap
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) && ctx.Err() == nil {
		// Never ran (start failure outside cancellation): a setup error, not
		// an exit code. Cancellation kills surface through the context.
		return out, fmt.Errorf("exec run failed: %w", runErr)
	}
	out.observed = proxy.observed()
	out.combined = append(append([]byte("=== stdout ===\n"), stdout.b...), append([]byte("\n=== stderr ===\n"), stderr.b...)...)
	out.cut = stdout.cut || stderr.cut
	out.subrequests = len(out.observed)
	out.bytesIn = int64(len(out.combined))
	for _, o := range out.observed {
		out.bytesIn += o.BytesIn
		out.bytesOut += o.BytesOut
	}
	out.exitCode = exitCodeOf(runErr)
	out.runErr = runErr
	return out, nil
}

// execExtent summarizes exec mediation for the capture extent.
func execExtent(res execResult) string {
	observedRaw, _ := json.Marshal(res.observed)
	raw, _ := json.Marshal(map[string]any{
		"runtime":     "python3-sandboxed",
		"exit":        res.exitCode,
		"egress":      "proxy-mediated-only",
		"subrequests": res.subrequests,
		"observed":    json.RawMessage(observedRaw),
	})
	return string(raw)
}

type cappedBuffer struct {
	b     []byte
	limit int
	cut   bool
}

func (l *cappedBuffer) Write(p []byte) (int, error) {
	if len(l.b) >= l.limit {
		l.cut = true
		return len(p), nil
	}
	if room := l.limit - len(l.b); len(p) > room {
		l.cut = true
		p = p[:room]
	}
	l.b = append(l.b, p...)
	return len(p), nil
}

func exitCodeOf(err error) string {
	if err == nil {
		return "0"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return strconv.Itoa(exitErr.ExitCode())
	}
	// Killed by signal or context, or never started: reported literally.
	return "signal"
}
