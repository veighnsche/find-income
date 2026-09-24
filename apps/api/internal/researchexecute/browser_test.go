package researchexecute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

func browseInput(url string) researchcontract.ExecuteInput {
	return researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteBrowse,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationBrowser, Backend: BackendBrowser, URLOrQuery: url,
		},
	}
}

func browseEnv(t *testing.T, cfg Config) *testEnv {
	t.Helper()
	requireSandbox(t)
	cfg.ChromePath = headlessShellPath(t)
	return newTestEnv(t, cfg)
}

// renderFixture serves a page with JS + two subresources on an isolated port.
func renderFixture(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><img src="/pix.png"><div id="m">no-js</div>` +
			`<script src="/app.js"></script></body></html>`))
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(`document.getElementById("m").textContent="JS-MARKER";`))
	})
	mux.HandleFunc("/pix.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a"), make([]byte, 64)...))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type extentLog struct {
	BrowserContext string            `json:"browserContext"`
	Subrequests    int               `json:"subrequests"`
	Observed       []ObservedRequest `json:"observed"`
}

func extentOf(t *testing.T, env *testEnv, captureID string) extentLog {
	t.Helper()
	info, err := env.caps.Info(context.Background(), captureID)
	if err != nil {
		t.Fatal(err)
	}
	var ext extentLog
	if err := json.Unmarshal([]byte(info.ExtentJSON), &ext); err != nil {
		t.Fatalf("extent: %v (%q)", err, info.ExtentJSON)
	}
	return ext
}

func TestBrowseRendersJSWithMediatedSubresources(t *testing.T) {
	env := browseEnv(t, Config{})
	srv := renderFixture(t)
	out := env.execute(t, browseInput(srv.URL+"/"))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s receipt=%+v", out.Outcome, out.Receipt)
	}
	if out.Receipt.Executor.Backend != BackendBrowser || out.Receipt.Executor.Digest == "" {
		t.Fatalf("executor identity = %+v", out.Receipt.Executor)
	}
	_, dom := openCaptureBytes(t, env, out.CaptureID)
	if !strings.Contains(string(dom), "JS-MARKER") {
		t.Fatalf("rendered DOM lacks JS marker: %q", dom)
	}
	// Every subrequest crossed the recording proxy: main + js + png.
	ext := extentOf(t, env, out.CaptureID)
	if ext.BrowserContext != "ephemeral-per-op" {
		t.Fatalf("extent = %+v", ext)
	}
	if len(ext.Observed) != 3 {
		t.Fatalf("observed subrequests = %+v", ext.Observed)
	}
	for _, o := range ext.Observed {
		if o.Blocked || o.Status != 200 {
			t.Fatalf("entry = %+v", o)
		}
	}
	if out.Receipt.Subrequests != 3 || out.Usage.Requests != 3 {
		t.Fatalf("receipt = %+v usage=%+v", out.Receipt, out.Usage)
	}
	if out.Receipt.UnknownUsage || out.Receipt.Truncated {
		t.Fatalf("fully mediated render must be known-complete: %+v", out.Receipt)
	}
	desc, _ := openCaptureBytes(t, env, out.CaptureID)
	if desc.Provenance != researchcontract.ProvenanceRenderedDOM {
		t.Fatalf("provenance = %s", desc.Provenance)
	}
}

func TestBrowseBlockedSubresourceIsExplicit(t *testing.T) {
	env := browseEnv(t, Config{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Link-local subresource: the proxy fences it; the DOM still renders.
		_, _ = w.Write([]byte(`<html><body><img src="http://169.254.169.254/x.png"><p>main</p></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	out := env.execute(t, browseInput(srv.URL+"/"))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s receipt=%+v", out.Outcome, out.Receipt)
	}
	if !out.Receipt.Truncated {
		t.Fatal("blocked subresource must mark the capture truncated")
	}
	obs := mustObservation(t, env, out.ObservationID)
	if !strings.Contains(obs.TruncationNote, "blocked") {
		t.Fatalf("note = %q", obs.TruncationNote)
	}
	ext := extentOf(t, env, out.CaptureID)
	blocked := 0
	for _, o := range ext.Observed {
		if o.Blocked {
			blocked++
			if o.Error != "destination_forbidden" {
				t.Fatalf("entry = %+v", o)
			}
		}
	}
	if blocked == 0 {
		t.Fatalf("no blocked entry in %+v", ext.Observed)
	}
	_, dom := openCaptureBytes(t, env, out.CaptureID)
	if !strings.Contains(string(dom), "main") {
		t.Fatalf("DOM = %q", dom)
	}
}

func TestBrowseStateNeverSurvivesCapture(t *testing.T) {
	env := browseEnv(t, Config{})
	var profiles []string
	env.ex.deps.browserProfileHook = func(p string) { profiles = append(profiles, p) }
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		http.SetCookie(w, &http.Cookie{Name: "sess", Value: "abc"})
		_, _ = w.Write([]byte("<html><body>cookie-seen:" + r.Header.Get("Cookie") + "</body></html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	first := env.execute(t, browseInput(srv.URL+"/"))
	second := env.execute(t, researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteBrowse, IdempotencyKey: "fresh-context",
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationBrowser, Backend: BackendBrowser,
			URLOrQuery: srv.URL + "/",
			Params:     []researchcontract.Param{{Name: "round", Value: "2"}},
		},
	})
	if second.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s", second.Outcome)
	}
	_, dom := openCaptureBytes(t, env, second.CaptureID)
	if strings.Contains(string(dom), "sess=abc") {
		t.Fatalf("cookie survived across renders (stateful replay): %q", dom)
	}
	if len(profiles) != 2 || profiles[0] == profiles[1] {
		t.Fatalf("profiles = %v", profiles)
	}
	for _, p := range profiles {
		if !strings.HasPrefix(p, env.ex.cfg.ScratchRoot) {
			t.Fatalf("profile escaped scratch root: %s", p)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("per-op profile survived: %s", p)
		}
	}
	_ = first
}

func TestBrowseKeychainSilence(t *testing.T) {
	env := browseEnv(t, Config{})
	srv := renderFixture(t)
	securityBefore := countSecurityAgent(t)
	keychainsBefore := snapshotKeychains(t)
	out := env.execute(t, browseInput(srv.URL+"/"))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if got := countSecurityAgent(t); got != securityBefore {
		t.Fatalf("SecurityAgent count changed %d -> %d", securityBefore, got)
	}
	assertKeychainsUntouched(t, keychainsBefore)
	// No UI process and no owner-dir writes: the render ran headless under a
	// temp HOME with an explicit per-op profile (proven by the profile hook in
	// TestBrowseStateNeverSurvivesCapture).
	if home := os.Getenv("HOME"); home == "" {
		t.Fatal("no HOME to compare against")
	}
}

func countSecurityAgent(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-x", "SecurityAgent").Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return 0 // none running
		}
		t.Skipf("pgrep unavailable: %v", err)
	}
	return len(strings.Split(strings.TrimSpace(string(out)), "\n"))
}

func snapshotKeychains(t *testing.T) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	dir := filepath.Join(home, "Library", "Keychains")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	if st, err := os.Stat(dir); err == nil {
		out[dir] = st.ModTime()
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			out[filepath.Join(dir, e.Name())] = info.ModTime()
		}
	}
	return out
}

func assertKeychainsUntouched(t *testing.T, before map[string]time.Time) {
	t.Helper()
	after := snapshotKeychains(t)
	if len(after) != len(before) {
		t.Fatalf("keychain dir changed: %d -> %d entries", len(before), len(after))
	}
	for p, mt := range before {
		if after[p] != mt {
			t.Fatalf("keychain entry touched: %s", p)
		}
	}
}

func TestBrowseFailsClosedWithoutSandbox(t *testing.T) {
	env := newTestEnv(t, Config{ChromePath: headlessShellPath(t), SandboxBinary: "/nonexistent/sandbox-exec"})
	srv := renderFixture(t)
	out := env.execute(t, browseInput(srv.URL+"/"))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.ErrorCode != ErrorCodeSandboxUnavailable {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	if out.CaptureID != "" {
		t.Fatal("unmediated render must not produce a capture")
	}
}

func TestBrowseRefusesUnpinnedBinary(t *testing.T) {
	env := newTestEnv(t, Config{
		ChromePath: headlessShellPath(t), ExpectedChromeSHA256: strings.Repeat("0", 64),
	})
	srv := renderFixture(t)
	out := env.execute(t, browseInput(srv.URL+"/"))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.ErrorCode != ErrorCodeBackendUntrusted {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
}

func TestBrowseRequestBoundExplicit(t *testing.T) {
	env := browseEnv(t, Config{})
	srv := renderFixture(t)
	in := browseInput(srv.URL + "/")
	in.Bounds.MaxRequests = 1 // main + js + png never fit
	out := env.execute(t, in)
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s receipt=%+v", out.Outcome, out.Receipt)
	}
	if !out.Receipt.Truncated {
		t.Fatal("request bound overrun must mark truncation")
	}
	obs := mustObservation(t, env, out.ObservationID)
	if !strings.Contains(obs.TruncationNote, "subrequest bound") {
		t.Fatalf("note = %q", obs.TruncationNote)
	}
}

func TestBrowsePrivateURLFencedPreDispatch(t *testing.T) {
	env := newTestEnv(t, Config{ChromePath: headlessShellPath(t)})
	_, err := env.ex.Execute(context.Background(), researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteBrowse, RunID: "run-1", Generation: 1,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationBrowser, Backend: BackendBrowser,
			URLOrQuery: "http://169.254.169.254/",
		},
	})
	mustContractErr(t, err, researchcontract.OutcomeInvalid)
	if checks, reserves, _ := env.auth.counts(); checks != 0 || reserves != 0 {
		t.Fatalf("fenced URL consumed authority: checks=%d reserves=%d", checks, reserves)
	}
}
