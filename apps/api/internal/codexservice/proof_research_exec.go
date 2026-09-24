// T02 proof boundaries for rendered browsing and executable research.
//
// Browser executes a headless Chromium to capture the rendered DOM of an
// arbitrary URL. Only the input URL and the output DOM/exit are captured;
// per-subrequest browser egress is NOT mediated here, which the T02 report
// records as a known gap for the production backend (T16/T22).
//
// Execute runs proof-scoped Python with stdout/stderr/exit capture and
// mediated outbound HTTP: the child inherits a recording forward proxy via
// HTTP_PROXY/HTTPS_PROXY, and every proxied request is observed at that
// boundary. HTTPS CONNECT tunnels report host and byte counts only. Direct
// (non-proxy) egress is not blocked in this proof; production needs network
// isolation, recorded as a T22 dependency.
package codexservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ProofExecConfig selects the subprocess boundaries. Empty paths disable the
// corresponding operation; the proof never searches PATH implicitly.
//
// ChromeIsolatedProfile selects the isolated headless-shell backend: an
// explicit per-operation --user-data-dir under the disposable root plus
// --timeout so --dump-dom exits. Without it, system Chrome falls back to its
// default profile, which on macOS ignores the per-op HOME and resolves to the
// owner's real profile directory (verified in T02: per-op HOME empty after
// render). System Chrome also ignores --timeout with a fresh explicit profile
// and never exits, so isolated mode is only valid for the headless-shell
// backend. See implementation-notes/autonomous-recruitment/T02-proof/.
type ProofExecConfig struct {
	ChromePath            string
	PythonPath            string
	ChromeIsolatedProfile bool
}

type proofExecRequest struct {
	Kind string            `json:"kind"`
	URL  string            `json:"url,omitempty"`
	Code string            `json:"code,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// Browser renders url with headless Chromium and captures the resulting DOM.
// JavaScript execution is real: the fixture page mutates the DOM from script.
func (p *ProofResearch) Browser(ctx context.Context, execCfg ProofExecConfig, rawURL string) (ProofReceipt, error) {
	if strings.TrimSpace(execCfg.ChromePath) == "" {
		return ProofReceipt{}, errors.New("proof research: browser backend not configured")
	}
	if strings.TrimSpace(rawURL) == "" {
		return ProofReceipt{}, errors.New("proof research: browser requires a URL")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ProofReceipt{}, fmt.Errorf("%w: invalid browser URL", ErrProofForbidden)
	}
	if !p.cfg.PermitLoopback {
		host := parsed.Hostname()
		if ip := net.ParseIP(host); ip != nil {
			if !isPublicIP(ip) {
				return ProofReceipt{}, fmt.Errorf("%w: literal browser host", ErrProofForbidden)
			}
		} else {
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return ProofReceipt{}, err
			}
			for _, ip := range ips {
				if !isPublicIP(ip) {
					return ProofReceipt{}, fmt.Errorf("%w: resolved browser host", ErrProofForbidden)
				}
			}
		}
	}
	fp := fingerprintParts(ProofBrowser, "render", rawURL)
	now := time.Now()
	return p.dispatch(ctx, fp, func(ctx context.Context) (ProofReceipt, error) {
		// Every render gets a fresh per-op dir. In isolated mode the profile
		// itself lives there via an explicit --user-data-dir, so concurrent
		// renders cannot contend on a shared Singleton lock and no cookie,
		// cache or profile state survives the capture. In default (system
		// Chrome) mode the dir is only HOME/TMPDIR, which macOS Chrome does
		// not honor for profile placement: the default profile resolves to
		// the owner's real profile directory.
		opHome, err := os.MkdirTemp(p.cfg.RootDir, "bhome-")
		if err != nil {
			return proofExecFailure(ProofBrowser, fp, now, "browser_profile_failed", err), err
		}
		defer os.RemoveAll(opHome)
		args := []string{"--no-sandbox", "--disable-gpu",
			"--no-first-run", "--no-default-browser-check", "--disable-sync",
			"--disable-extensions", "--disable-background-networking",
			"--disable-component-update", "--disable-default-apps",
			"--password-store=basic", "--use-mock-keychain",
			"--virtual-time-budget=3000"}
		if execCfg.ChromeIsolatedProfile {
			profile := filepath.Join(opHome, "prof")
			args = append(args,
				"--user-data-dir="+profile,
				fmt.Sprintf("--timeout=%d", p.cfg.OpTimeout.Milliseconds()))
		} else {
			args = append([]string{"--headless=new"}, args...)
		}
		args = append(args, "--dump-dom", rawURL)
		start := time.Now()
		cmd := exec.CommandContext(ctx, execCfg.ChromePath, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + opHome, "TMPDIR=" + p.cfg.RootDir, "LANG=C"}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return proofExecFailure(ProofBrowser, fp, now, "browser_pipe_failed", err), err
		}
		stderr := &strings.Builder{}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			return proofExecFailure(ProofBrowser, fp, now, "browser_start_failed", err), err
		}
		data, err := io.ReadAll(io.LimitReader(stdout, p.cfg.MaxBodyBytes+1))
		waitErr := cmd.Wait()
		duration := time.Since(start)
		if err != nil {
			return proofExecFailure(ProofBrowser, fp, now, "browser_read_failed", err), err
		}
		truncated := false
		if int64(len(data)) > p.cfg.MaxBodyBytes {
			truncated = true
			data = data[:p.cfg.MaxBodyBytes]
		}
		receipt := ProofReceipt{ID: proofID(), Operation: ProofBrowser, Fingerprint: fp,
			StartedAt: now, EndedAt: time.Now(), Attempts: 1, Subrequests: 1,
			BytesIn: int64(len(data)), FinalURL: rawURL, Truncated: truncated}
		if waitErr != nil {
			receipt.Status = ProofFailed
			receipt.ErrorCode = "browser_exit_failed"
			return receipt, waitErr
		}
		capture, err := p.storeCapture(ProofBrowser, fp,
			proofExecRequest{Kind: ProofBrowser, URL: rawURL},
			0, rawURL, nil, "text/html", data, !truncated, now, duration,
			map[string]string{"stderrTail": tailString(stderr.String(), 2000)})
		if err != nil {
			receipt.Status = ProofFailed
			receipt.ErrorCode = "capture_failed"
			return receipt, err
		}
		receipt.Status = ProofOK
		receipt.CaptureID = capture.ID
		return receipt, nil
	})
}

func proofExecFailure(operation, fp string, start time.Time, code string, cause error) ProofReceipt {
	return ProofReceipt{ID: proofID(), Operation: operation, Fingerprint: fp,
		Status: ProofFailed, StartedAt: start, EndedAt: time.Now(), Attempts: 1, ErrorCode: code}
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ObservedRequest is one outbound request seen at the execution proxy.
type ObservedRequest struct {
	Method   string `json:"method"`
	URL      string `json:"url"`
	Status   int    `json:"status"`
	BytesIn  int64  `json:"bytesIn"`
	BytesOut int64  `json:"bytesOut"`
	Tunneled bool   `json:"tunneled,omitempty"`
}

// Execute runs code with Python 3, captures input/output/exit, and observes
// outbound HTTP at a recording proxy injected through the child environment.
// Logging the command is not the proof: receipt bytes and observed requests
// are captured from the actual child output and proxy log.
func (p *ProofResearch) Execute(ctx context.Context, execCfg ProofExecConfig, code string) (ProofReceipt, error) {
	if strings.TrimSpace(execCfg.PythonPath) == "" {
		return ProofReceipt{}, errors.New("proof research: execution backend not configured")
	}
	if strings.TrimSpace(code) == "" {
		return ProofReceipt{}, errors.New("proof research: execute requires code")
	}
	sum := sha256.Sum256([]byte(code))
	fp := fingerprintParts(ProofExecute, "python3", hex.EncodeToString(sum[:]))
	now := time.Now()
	return p.dispatch(ctx, fp, func(ctx context.Context) (ProofReceipt, error) {
		proxy, err := newProofProxy(p)
		if err != nil {
			return proofExecFailure(ProofExecute, fp, now, "proxy_start_failed", err), err
		}
		defer proxy.close()
		proxyURL := "http://" + proxy.addr
		start := time.Now()
		cmd := exec.CommandContext(ctx, execCfg.PythonPath, "-c", code)
		cmd.Env = []string{
			"PATH=/usr/bin:/bin", "HOME=" + p.cfg.RootDir, "TMPDIR=" + p.cfg.RootDir, "LANG=C",
			"HTTP_PROXY=" + proxyURL, "HTTPS_PROXY=" + proxyURL,
			"http_proxy=" + proxyURL, "https_proxy=" + proxyURL,
			"NO_PROXY=", "no_proxy=", "PYTHONDONTWRITEBYTECODE=1",
		}
		var stdout, stderr limitedBuffer
		stdout.limit, stderr.limit = 65536, 65536
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		duration := time.Since(start)
		observed := proxy.observed()
		combined := append(append([]byte("=== stdout ===\n"), stdout.b...), append([]byte("\n=== stderr ===\n"), stderr.b...)...)
		exitCode := "0"
		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				exitCode = fmt.Sprintf("%d", exitErr.ExitCode())
			} else {
				exitCode = "signal"
			}
		}
		observedRaw, _ := json.Marshal(observed)
		receipt := ProofReceipt{ID: proofID(), Operation: ProofExecute, Fingerprint: fp,
			StartedAt: now, EndedAt: time.Now(), Attempts: 1, Subrequests: len(observed),
			BytesIn: int64(len(combined))}
		for _, o := range observed {
			receipt.BytesIn += o.BytesIn
			receipt.BytesOut += o.BytesOut
		}
		if runErr != nil && ctx.Err() == nil {
			receipt.Status = ProofFailed
			receipt.ErrorCode = "exit_" + exitCode
			return receipt, runErr
		}
		capture, err := p.storeCapture(ProofExecute, fp,
			proofExecRequest{Kind: ProofExecute, Code: code},
			0, "", nil, "text/plain", combined, !stdout.cut && !stderr.cut, now, duration,
			map[string]string{"exit": exitCode, "observed": string(observedRaw),
				"stdoutCut": fmt.Sprintf("%v", stdout.cut), "stderrCut": fmt.Sprintf("%v", stderr.cut)})
		if err != nil {
			receipt.Status = ProofFailed
			receipt.ErrorCode = "capture_failed"
			return receipt, err
		}
		receipt.Status = ProofOK
		receipt.CaptureID = capture.ID
		receipt.Truncated = stdout.cut || stderr.cut
		return receipt, nil
	})
}

type limitedBuffer struct {
	b     []byte
	limit int
	cut   bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(l.b) >= l.limit {
		l.cut = true
		return len(p), nil
	}
	room := l.limit - len(l.b)
	if len(p) > room {
		l.cut = true
		p = p[:room]
	}
	l.b = append(l.b, p...)
	return len(p), nil
}

// proofProxy is a minimal recording forward proxy for the Execute boundary.
// Plain HTTP requests are forwarded through the guarded proof client so every
// request line, status and byte count is observed. CONNECT tunnels forward
// opaque bytes with host and byte counts only.
type proofProxy struct {
	listener net.Listener
	addr     string
	server   *http.Server
	parent   *ProofResearch
	mu       sync.Mutex
	requests []ObservedRequest
}

func newProofProxy(parent *ProofResearch) (*proofProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	proxy := &proofProxy{listener: listener, parent: parent, addr: listener.Addr().String()}
	proxy.server = &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go proxy.server.Serve(listener)
	return proxy, nil
}

func (pr *proofProxy) close() {
	_ = pr.server.Close()
	_ = pr.listener.Close()
}

func (pr *proofProxy) observed() []ObservedRequest {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	out := make([]ObservedRequest, len(pr.requests))
	copy(out, pr.requests)
	return out
}

func (pr *proofProxy) record(entry ObservedRequest) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.requests = append(pr.requests, entry)
}

func (pr *proofProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		pr.serveConnect(w, r)
		return
	}
	target := r.URL
	if !target.IsAbs() {
		http.Error(w, "proxy requires absolute URI", http.StatusBadRequest)
		return
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		http.Error(w, "proxy forbids scheme", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, pr.parent.cfg.MaxBodyBytes+1))
	if err != nil {
		http.Error(w, "proxy read failed", http.StatusBadGateway)
		return
	}
	_ = r.Body.Close()
	ctx, cancel := context.WithTimeout(r.Context(), pr.parent.cfg.OpTimeout)
	defer cancel()
	res, err := pr.parent.doHTTP(ctx, r.Method, target.String(), nil, body, r.Header.Get("Content-Type"))
	entry := ObservedRequest{Method: r.Method, URL: target.String(), BytesOut: int64(len(body))}
	if err != nil {
		pr.record(entry)
		http.Error(w, "proxy forward failed", http.StatusBadGateway)
		return
	}
	entry.Status = res.status
	entry.BytesIn = res.bytesIn
	pr.record(entry)
	for k, vs := range map[string]string{"Content-Type": res.contentType} {
		if vs != "" {
			w.Header().Set(k, vs)
		}
	}
	w.WriteHeader(res.status)
	_, _ = w.Write(res.body)
}

func (pr *proofProxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Host
	if host == "" {
		host = r.Host
	}
	entry := ObservedRequest{Method: http.MethodConnect, URL: "https://" + host, Tunneled: true}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	upstream, err := dialer.DialContext(r.Context(), "tcp", host)
	if err != nil {
		pr.record(entry)
		http.Error(w, "tunnel failed", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		pr.record(entry)
		http.Error(w, "tunnel unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		pr.record(entry)
		http.Error(w, "tunnel failed", http.StatusBadGateway)
		return
	}
	defer client.Close()
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	var wg sync.WaitGroup
	var up, down int64
	closeWrite := func(c net.Conn) {
		if tcp, ok := c.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}
	wg.Add(2)
	go func() { defer wg.Done(); up, _ = io.Copy(upstream, client); closeWrite(upstream) }()
	go func() { defer wg.Done(); down, _ = io.Copy(client, upstream); closeWrite(client) }()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-r.Context().Done():
		_ = upstream.Close()
		_ = client.Close()
		<-done
	}
	entry.BytesOut, entry.BytesIn = up, down
	entry.Status = 200
	pr.record(entry)
}
