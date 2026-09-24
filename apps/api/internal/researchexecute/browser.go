package researchexecute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// browserResult is one fully mediated render: the DOM bytes plus the proxy's
// complete subrequest account.
type browserResult struct {
	dom         []byte
	truncated   bool
	observed    []ObservedRequest
	subrequests int
	bytesIn     int64
	bytesOut    int64
	mainStatus  *int64
	stderrTail  string
	duration    time.Duration
	identity    researchcontract.ExecutorIdentity
}

// renderBrowser captures the rendered DOM of rawURL through the SELECTED
// isolated headless shell. Every byte the browser sends or receives crosses
// the operation's recording proxy: --proxy-server forces all egress there
// (--proxy-bypass-list=<-loopback> keeps even loopback fixtures mediated),
// and the seatbelt profile denies any other outbound IP. No login, no UI, no
// owner profile: the render gets a fresh per-op --user-data-dir under a
// disposable root with a minimal environment, and the profile is deleted
// afterwards. State never survives the capture: cookies, cache and navigation
// state are per-op only, while the captured DOM bytes are the reusable
// artifact (T06 §3: stateful_context_bound live context vs stateless bytes).
func renderBrowser(ctx context.Context, deps *opDeps, rawURL, locale, viewport string, maxBytes int64, maxReqs int, chromeTimeout time.Duration) (browserResult, error) {
	var out browserResult
	identity, err := deps.backends.chromeIdentity(ctx)
	if err != nil {
		return out, err
	}
	out.identity = identity
	if _, err := checkURLResolves(ctx, deps.resolver, rawURL, deps.permitLoopback); err != nil {
		return out, err
	}
	if err := checkSandbox(deps.sandboxBinary); err != nil {
		return out, err
	}
	proxy, err := newRecordingProxy(deps.forwardClient(), maxBytes, maxReqs)
	if err != nil {
		return out, err
	}
	defer proxy.close()

	opHome, err := os.MkdirTemp(deps.scratchRoot, "bhome-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(opHome)

	args := []string{
		"--no-sandbox", "--disable-gpu",
		"--no-first-run", "--no-default-browser-check", "--disable-sync",
		"--disable-extensions", "--disable-background-networking",
		"--disable-component-update", "--disable-default-apps",
		"--disable-quic",
		"--password-store=basic", "--use-mock-keychain",
		"--virtual-time-budget=3000",
		"--proxy-server=" + proxy.url(),
		"--proxy-bypass-list=<-loopback>",
		"--user-data-dir=" + filepath.Join(opHome, "prof"),
		fmt.Sprintf("--timeout=%d", chromeTimeout.Milliseconds()),
	}
	if locale != "" {
		args = append(args, "--lang="+locale)
	}
	if viewport != "" {
		args = append(args, "--window-size="+viewport)
	}
	// Expose the op profile dir for keychain-silence tests without leaking it
	// into receipts: the path only ever appears in test-scoped hooks.
	if deps.browserProfileHook != nil {
		deps.browserProfileHook(filepath.Join(opHome, "prof"))
	}
	args = append(args, "--dump-dom", rawURL)

	env := []string{"PATH=/usr/bin:/bin", "HOME=" + opHome, "TMPDIR=" + opHome, "LANG=C"}
	start := time.Now()
	cmd, err := sandboxCommand(ctx, deps.sandboxBinary, proxy.port(), opHome, env, deps.backends.chromePath, args...)
	if err != nil {
		return out, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return out, err
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return out, fmt.Errorf("browser start failed: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	waitErr := cmd.Wait()
	out.duration = time.Since(start)
	out.observed = proxy.observed()
	out.stderrTail = tailString(stderr.String(), 2000)
	if readErr != nil {
		return out, fmt.Errorf("browser read failed: %w", readErr)
	}
	if int64(len(data)) > maxBytes {
		out.truncated = true
		data = data[:maxBytes]
	}
	out.dom = data
	out.bytesIn = int64(len(data))
	out.subrequests = len(out.observed)
	for _, o := range out.observed {
		out.bytesIn += o.BytesIn
		out.bytesOut += o.BytesOut
		if o.Blocked || o.Truncated {
			out.truncated = true
		}
	}
	if status := mainResourceStatus(out.observed, rawURL); status != nil {
		out.mainStatus = status
	}
	if waitErr != nil {
		return out, fmt.Errorf("browser exit failed: %w", waitErr)
	}
	return out, nil
}

// mainResourceStatus recovers the initial navigation's status from the proxy
// log (first unblocked non-CONNECT entry for the requested URL).
func mainResourceStatus(observed []ObservedRequest, rawURL string) *int64 {
	for _, o := range observed {
		if o.Tunneled || o.Blocked || o.Method == "CONNECT" {
			continue
		}
		if o.URL == rawURL && o.Status != 0 {
			status := int64(o.Status)
			return &status
		}
	}
	return nil
}

// browserExtent summarizes render mediation for the capture extent.
func browserExtent(res browserResult) string {
	observedRaw, _ := json.Marshal(res.observed)
	raw, _ := json.Marshal(map[string]any{
		"browserContext": "ephemeral-per-op",
		"subrequests":    res.subrequests,
		"observed":       json.RawMessage(observedRaw),
		"stderrTail":     res.stderrTail,
	})
	return string(raw)
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
