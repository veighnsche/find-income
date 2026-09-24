package runtimeaccept

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
)

// renderFixture serves a page with JS plus script and image subresources.
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
	return fixtureServer(t, mux)
}

type observedEntry struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Status int    `json:"status"`
}

type browseExtent struct {
	BrowserContext string          `json:"browserContext"`
	Subrequests    int             `json:"subrequests"`
	Observed       []observedEntry `json:"observed"`
}

type execExtent struct {
	Runtime  string          `json:"runtime"`
	Exit     string          `json:"exit"`
	Egress   string          `json:"egress"`
	Observed []observedEntry `json:"observed"`
}

func browseDispatch(t *testing.T, h *harness, key, page string) rounds.DispatchOutput {
	t.Helper()
	out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteBrowse,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationBrowser, Backend: researchexecute.BackendBrowser,
			Method: "GET", URLOrQuery: page,
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 30000},
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func execDispatch(t *testing.T, h *harness, key, code string) rounds.DispatchOutput {
	t.Helper()
	out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteExec,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationExec, Backend: researchexecute.BackendExec,
			URLOrQuery: "python3", Body: code,
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 30000},
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A real browser render through the wired supervisor path: JS executes,
// every subrequest is proxy-observed, and the capture extent names only
// the fixture origin.
func TestBrowseEgressMediatedThroughWiredStack(t *testing.T) {
	requireSandbox(t)
	shell := headlessShellPath(t)
	h := newHarness(t, nil, "browse egress", "t24-egress-browse-1", func(cfg *researchwire.Config) {
		cfg.ChromePath = shell
	})
	page := renderFixture(t)

	out := browseDispatch(t, h, "t24-egress-browse", page.URL+"/")
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("browse dispatch: %s %+v", out.Outcome, out.Receipt)
	}
	if out.Receipt.Executor.Backend != researchexecute.BackendBrowser || out.Receipt.Executor.Digest == "" {
		t.Fatalf("executor identity: %+v", out.Receipt.Executor)
	}
	if out.Receipt.UnknownUsage {
		t.Fatal("mediated render reports unknown usage")
	}
	if body := h.captureBytes(t, out.CaptureID); !strings.Contains(body, "JS-MARKER") {
		t.Fatalf("rendered DOM missing JS marker: %q", body)
	}
	info, err := h.stack.Captures.Info(h.ctx, out.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	var ext browseExtent
	if err := json.Unmarshal([]byte(info.ExtentJSON), &ext); err != nil {
		t.Fatalf("extent: %v (%q)", err, info.ExtentJSON)
	}
	if ext.Subrequests < 3 || len(ext.Observed) < 3 {
		t.Fatalf("subresource mediation: %+v", ext)
	}
	for _, o := range ext.Observed {
		u, err := url.Parse(o.URL)
		if err != nil {
			t.Fatal(err)
		}
		if host := u.Hostname(); host != "127.0.0.1" && host != "localhost" && host != "::1" {
			t.Fatalf("render reached beyond the fixture origin: %s", o.URL)
		}
	}
}

// A real sandboxed script through the wired supervisor path: proxy-mediated
// egress succeeds and is observed, while a direct socket bypass dies with
// EPERM and leaves no proxy trace.
func TestExecEgressMediatedThroughWiredStack(t *testing.T) {
	requireSandbox(t)
	python := pythonPath(t)
	h := newHarness(t, nil, "exec egress", "t24-egress-exec-1", func(cfg *researchwire.Config) {
		cfg.PythonPath = python
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"v":42}`))
	})
	fix := fixtureServer(t, mux)

	code := "import urllib.request\n" +
		"body = urllib.request.urlopen('" + fix.URL + "/data', timeout=10).read()\n" +
		"print('fetched bytes:', len(body))\n"
	out := execDispatch(t, h, "t24-egress-exec", code)
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("exec dispatch: %s %+v", out.Outcome, out.Receipt)
	}
	if out.Receipt.Executor.Backend != researchexecute.BackendExec {
		t.Fatalf("executor identity: %+v", out.Receipt.Executor)
	}
	if body := h.captureBytes(t, out.CaptureID); !strings.Contains(body, "fetched bytes: 8") {
		t.Fatalf("script output: %q", body)
	}
	var ext execExtent
	if info, err := h.stack.Captures.Info(h.ctx, out.CaptureID); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal([]byte(info.ExtentJSON), &ext); err != nil {
		t.Fatalf("extent: %v (%q)", err, info.ExtentJSON)
	}
	if ext.Runtime != "python3-sandboxed" || ext.Exit != "0" || ext.Egress != "proxy-mediated-only" {
		t.Fatalf("extent: %+v", ext)
	}
	if len(ext.Observed) == 0 {
		t.Fatalf("mediated egress unobserved: %+v", ext)
	}
}

func TestExecDirectBypassDeniedThroughWiredStack(t *testing.T) {
	requireSandbox(t)
	python := pythonPath(t)
	h := newHarness(t, nil, "exec bypass", "t24-egress-bypass-1", func(cfg *researchwire.Config) {
		cfg.PythonPath = python
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/direct", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	})
	fix := fixtureServer(t, mux)
	u, err := url.Parse(fix.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := strings.Cut(u.Host, ":")
	code := "import socket\n" +
		"s = socket.create_connection(('" + host + "', " + port + "), timeout=8)\n" +
		"print('bypass connected')\n"
	out := execDispatch(t, h, "t24-egress-bypass", code)
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("bypass outcome: %s", out.Outcome)
	}
	combined := h.captureBytes(t, out.CaptureID)
	if !strings.Contains(combined, "Operation not permitted") {
		t.Fatalf("want EPERM denial, got %q", combined)
	}
	if strings.Contains(combined, "bypass connected") {
		t.Fatalf("bypass connected: %q", combined)
	}
	var ext execExtent
	if info, err := h.stack.Captures.Info(h.ctx, out.CaptureID); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal([]byte(info.ExtentJSON), &ext); err != nil {
		t.Fatalf("extent: %v (%q)", err, info.ExtentJSON)
	}
	if len(ext.Observed) != 0 {
		t.Fatalf("denied egress left a proxy trace: %+v", ext.Observed)
	}
}
