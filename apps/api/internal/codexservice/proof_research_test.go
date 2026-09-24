// T02 proof tests: instrumented fixture sources exercise the proof research
// backend directly. No Codex binary, login or live network is used here; the
// real-binary/live checks live in proof_live_test.go behind T02_PROOF_LIVE=1.
package codexservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type proofFixtures struct {
	searchURL string
	siteURL   string
	apiURL    string
	searchHit *atomic.Int64
	siteHit   *atomic.Int64
	apiHit    *atomic.Int64
	pageBody  string
	jsBody    string
}

func startProofFixtures(t *testing.T) *proofFixtures {
	t.Helper()
	fx := &proofFixtures{searchHit: &atomic.Int64{}, siteHit: &atomic.Int64{}, apiHit: &atomic.Int64{}}
	fx.pageBody = "<!doctype html><html><body><h1>Synthetic vacancy: proof engineer</h1><p>req: t02-fixture-marker</p></body></html>"
	fx.jsBody = "<!doctype html><html><body><div id=\"app\">no-js</div><script>document.getElementById('app').textContent='rendered-proof-marker';</script></body></html>"
	var site *httptest.Server
	site = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.siteHit.Add(1)
		switch r.URL.Path {
		case "/page-a":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(fx.pageBody))
		case "/js":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(fx.jsBody))
		case "/redirect":
			http.Redirect(w, r, site.URL+"/page-a", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.searchHit.Add(1)
		q := r.URL.Query().Get("q")
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query": q, "page": page,
			"results": []map[string]string{
				{"title": "Proof engineer", "url": site.URL + "/page-a"},
				{"title": "Rendered role", "url": site.URL + "/js"},
			},
		})
	}))
	t.Cleanup(search.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.apiHit.Add(1)
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"method": r.Method, "path": r.URL.Path,
			"params": r.URL.Query(), "body": string(body),
			"contentType": r.Header.Get("Content-Type"),
		})
	}))
	t.Cleanup(api.Close)
	fx.searchURL = search.URL + "/search"
	fx.siteURL = site.URL
	fx.apiURL = api.URL
	return fx
}

func openProofForTest(t *testing.T, mutate func(*ProofConfig)) *ProofResearch {
	t.Helper()
	// OpTimeout stays generous: Chromium cold start (including OS
	// verification stalls) varies from 2s to well over 10s on this host.
	cfg := ProofConfig{RootDir: t.TempDir(), MaxActions: 60, MaxConcurrent: 2,
		MaxBodyBytes: 1 << 20, OpTimeout: 90 * time.Second, PermitLoopback: true}
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := OpenProofResearch(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProofSearchAndFollowResult(t *testing.T) {
	fx := startProofFixtures(t)
	p := openProofForTest(t, nil)
	ctx := context.Background()
	receipt, err := p.Search(ctx, fx.searchURL, "synthetic alpha", map[string]string{"page": "1"})
	if err != nil || receipt.Status != ProofOK || receipt.CaptureID == "" {
		t.Fatalf("search failed: %+v %v", receipt, err)
	}
	body, err := p.CaptureBody(receipt.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	var found struct {
		Query   string `json:"query"`
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &found); err != nil || found.Query != "synthetic alpha" || len(found.Results) != 2 {
		t.Fatalf("search capture wrong: %s %v", body, err)
	}
	// Follow the unseeded result URL through the same capture path.
	follow, err := p.Fetch(ctx, found.Results[0].URL)
	if err != nil || follow.Status != ProofOK {
		t.Fatalf("follow failed: %+v %v", follow, err)
	}
	page, err := p.CaptureBody(follow.CaptureID)
	if err != nil || string(page) != fx.pageBody {
		t.Fatal("followed capture does not match fixture bytes")
	}
	meta, err := p.CaptureMeta(follow.CaptureID)
	if err != nil || !meta.Complete || meta.FinalURL != found.Results[0].URL {
		t.Fatalf("capture meta wrong: %+v %v", meta, err)
	}
	if p.Usage().Actions != 2 {
		t.Fatalf("usage wrong: %+v", p.Usage())
	}
}

func TestProofReadOnlyPOSTAPI(t *testing.T) {
	fx := startProofFixtures(t)
	p := openProofForTest(t, nil)
	ctx := context.Background()
	receipt, err := p.API(ctx, "POST", fx.apiURL+"/query",
		map[string]string{"type": "role", "page": "2"},
		[]byte(`{"filters":{"remote":true}}`), "application/json")
	if err != nil || receipt.Status != ProofOK {
		t.Fatalf("POST API failed: %+v %v", receipt, err)
	}
	body, err := p.CaptureBody(receipt.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	var echoed struct {
		Method      string              `json:"method"`
		Params      map[string][]string `json:"params"`
		Body        string              `json:"body"`
		ContentType string              `json:"contentType"`
	}
	if err := json.Unmarshal(body, &echoed); err != nil {
		t.Fatal(err)
	}
	if echoed.Method != "POST" || echoed.Params["type"][0] != "role" || echoed.Body != `{"filters":{"remote":true}}` || echoed.ContentType != "application/json" {
		t.Fatalf("API capture wrong: %s", body)
	}
	if _, err := p.API(ctx, "PUT", fx.apiURL+"/query", nil, []byte(`{}`), "application/json"); !errors.Is(err, ErrProofForbidden) {
		t.Fatalf("state-changing verb dispatched: %v", err)
	}
	if _, err := p.API(ctx, "DELETE", fx.apiURL+"/query", nil, nil, ""); !errors.Is(err, ErrProofForbidden) {
		t.Fatalf("state-changing verb dispatched: %v", err)
	}
	if p.Usage().Actions != 1 {
		t.Fatalf("rejected verbs consumed allowance: %+v", p.Usage())
	}
}

func TestProofFreshnessReuse(t *testing.T) {
	fx := startProofFixtures(t)
	p := openProofForTest(t, nil)
	ctx := context.Background()
	first, err := p.Fetch(ctx, fx.siteURL+"/page-a")
	if err != nil || first.Status != ProofOK {
		t.Fatalf("first fetch failed: %+v %v", first, err)
	}
	second, err := p.Fetch(ctx, fx.siteURL+"/page-a")
	if err != nil || second.Status != ProofReused || second.CaptureID != first.CaptureID {
		t.Fatalf("exact repeat not reused: %+v %v", second, err)
	}
	if fx.siteHit.Load() != 1 {
		t.Fatalf("repeat dispatched externally: %d", fx.siteHit.Load())
	}
	if p.Usage().Actions != 1 {
		t.Fatalf("reuse consumed allowance: %+v", p.Usage())
	}
}

func TestProofConcurrentClaimSharesOneDispatch(t *testing.T) {
	release := make(chan struct{})
	var entered atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered.Add(1)
		<-release
		_, _ = w.Write([]byte("shared-bytes"))
	}))
	t.Cleanup(server.Close)
	p := openProofForTest(t, nil)
	ctx := context.Background()
	type outcome struct {
		receipt ProofReceipt
		err     error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			receipt, err := p.Fetch(ctx, server.URL)
			results <- outcome{receipt, err}
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for p.Usage().InFlight != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Second caller shares the leader's claim; exactly one external dispatch.
	close(release)
	var statuses []string
	var capture string
	for range 2 {
		select {
		case o := <-results:
			if o.err != nil {
				t.Fatalf("shared fetch failed: %v", o.err)
			}
			statuses = append(statuses, o.receipt.Status)
			if capture == "" {
				capture = o.receipt.CaptureID
			} else if capture != o.receipt.CaptureID {
				t.Fatal("shared claim returned different captures")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("shared fetch timed out")
		}
	}
	if entered.Load() != 1 {
		t.Fatalf("shared claim dispatched %d times", entered.Load())
	}
	joined := strings.Join(statuses, ",")
	if joined != "ok,shared" && joined != "shared,ok" {
		t.Fatalf("claim statuses wrong: %v", statuses)
	}
}

func TestProofBudgetAndConcurrency(t *testing.T) {
	fx := startProofFixtures(t)
	p := openProofForTest(t, func(c *ProofConfig) { c.MaxActions = 2 })
	ctx := context.Background()
	type outcome struct {
		receipt ProofReceipt
		err     error
	}
	results := make(chan outcome, 2)
	go func() { r, e := p.Fetch(ctx, fx.siteURL+"/page-a"); results <- outcome{r, e} }()
	go func() { r, e := p.Fetch(ctx, fx.siteURL+"/js"); results <- outcome{r, e} }()
	ids := map[string]bool{}
	for range 2 {
		select {
		case o := <-results:
			if o.err != nil || o.receipt.Status != ProofOK {
				t.Fatalf("concurrent op failed: %+v %v", o.receipt, o.err)
			}
			if ids[o.receipt.ID] {
				t.Fatal("duplicate receipt IDs")
			}
			ids[o.receipt.ID] = true
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent ops timed out")
		}
	}
	if p.Usage().Actions != 2 || p.Usage().Remaining != 0 {
		t.Fatalf("allowance wrong: %+v", p.Usage())
	}
	if _, err := p.Fetch(ctx, fx.searchURL); !errors.Is(err, ErrProofBudget) {
		t.Fatalf("exhausted allowance dispatched: %v", err)
	}
}

func TestProofUncertainDisconnectHasNoBlindReplay(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte("too-late"))
	}))
	t.Cleanup(server.Close)
	p := openProofForTest(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ProofReceipt, 1)
	go func() {
		receipt, err := p.Fetch(ctx, server.URL)
		if err != nil {
			t.Errorf("canceled fetch returned error: %v", err)
		}
		done <- receipt
	}()
	deadline := time.Now().Add(3 * time.Second)
	for p.Usage().InFlight != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	var receipt ProofReceipt
	select {
	case receipt = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled fetch timed out")
	}
	if receipt.Status != ProofUncertain || !receipt.Unknown {
		t.Fatalf("canceled dispatch not uncertain: %+v", receipt)
	}
	if !p.Usage().Unknown {
		t.Fatal("usage hides unknown work")
	}
	// No blind replay: the same request is refused until reconciled.
	if _, err := p.Fetch(context.Background(), server.URL); !errors.Is(err, ErrProofUncertain) {
		t.Fatalf("uncertain request replayed: %v", err)
	}
	p.AcknowledgeUncertain(receipt.Fingerprint)
	close(release)
	retry, err := p.Fetch(context.Background(), server.URL)
	if err != nil || retry.Status != ProofOK {
		t.Fatalf("reconciled retry failed: %+v %v", retry, err)
	}
}

func TestProofStopFencesDispatchAndCancelsInflight(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var entered atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		entered.Add(1)
		<-release
		_, _ = w.Write([]byte("late-bytes"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	p := openProofForTest(t, nil)
	ctx := context.Background()
	type outcome struct {
		receipt ProofReceipt
		err     error
	}
	results := make(chan outcome, 2)
	go func() { r, e := p.Fetch(ctx, server.URL+"/a"); results <- outcome{r, e} }()
	go func() { r, e := p.Fetch(ctx, server.URL+"/b"); results <- outcome{r, e} }()
	deadline := time.Now().Add(3 * time.Second)
	for (p.Usage().InFlight != 2 || entered.Load() != 2) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.Usage().InFlight != 2 {
		t.Fatalf("ops not in flight: %+v", p.Usage())
	}
	p.Stop()
	for range 2 {
		select {
		case o := <-results:
			if o.err != nil {
				t.Fatalf("stopped op returned error: %v", o.err)
			}
			if o.receipt.Status != ProofUncertain {
				t.Fatalf("stopped op not uncertain: %+v", o.receipt)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stopped ops timed out")
		}
	}
	close(release)
	// Late bytes cannot revive the run: new dispatch stays fenced.
	if _, err := p.Fetch(ctx, server.URL+"/c"); !errors.Is(err, ErrProofStopped) {
		t.Fatalf("dispatch after Stop: %v", err)
	}
	// Late completion is retained as an observation, not a success.
	time.Sleep(200 * time.Millisecond)
	if !p.Usage().Unknown {
		t.Fatal("usage hides stopped work")
	}
}

func TestProofPrivateDestinationsForbidden(t *testing.T) {
	p := openProofForTest(t, func(c *ProofConfig) { c.PermitLoopback = false })
	ctx := context.Background()
	for _, target := range []string{"http://127.0.0.1/", "http://10.0.0.1/", "http://192.168.1.1/", "http://169.254.169.254/", "http://[::1]/"} {
		receipt, err := p.Fetch(ctx, target)
		if err == nil {
			t.Fatalf("private destination dispatched: %s -> %+v", target, receipt)
		}
		if receipt.Status != ProofFailed || receipt.ErrorCode != "destination_forbidden" {
			t.Fatalf("private destination not fenced: %s -> %+v", target, receipt)
		}
	}
	if _, err := p.Fetch(ctx, "ftp://example.com/x"); !errors.Is(err, ErrProofForbidden) {
		t.Fatalf("non-HTTP scheme accepted: %v", err)
	}
}

func TestProofBytesBoundProducesExplicitIncomplete(t *testing.T) {
	fx := startProofFixtures(t)
	p := openProofForTest(t, func(c *ProofConfig) { c.MaxBodyBytes = 32 })
	receipt, err := p.Fetch(context.Background(), fx.siteURL+"/page-a")
	if err != nil || receipt.Status != ProofOK || !receipt.Truncated {
		t.Fatalf("bounded fetch wrong: %+v %v", receipt, err)
	}
	body, err := p.CaptureBody(receipt.CaptureID)
	if err != nil || len(body) != 32 {
		t.Fatalf("truncated body wrong: %d %v", len(body), err)
	}
	meta, err := p.CaptureMeta(receipt.CaptureID)
	if err != nil || meta.Complete {
		t.Fatalf("truncated capture marked complete: %+v %v", meta, err)
	}
}

func TestProofCaptureIntegrity(t *testing.T) {
	fx := startProofFixtures(t)
	p := openProofForTest(t, nil)
	receipt, err := p.Fetch(context.Background(), fx.siteURL+"/page-a")
	if err != nil || receipt.Status != ProofOK {
		t.Fatalf("fetch failed: %+v %v", receipt, err)
	}
	if _, err := p.CaptureBody("not-a-capture"); err == nil {
		t.Fatal("forged capture reference accepted")
	}
	if _, err := p.CaptureMeta("not-a-capture"); err == nil {
		t.Fatal("forged capture meta accepted")
	}
	real := receipt.CaptureID
	// Alter the stored bytes: integrity verification must fail.
	bodyPath := filepath.Join(p.cfg.RootDir, "bodies", real+".bin")
	raw, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0xff
	if err := os.WriteFile(bodyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CaptureBody(real); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("altered capture accepted: %v", err)
	}
}

func TestProofRedirectChainRecorded(t *testing.T) {
	fx := startProofFixtures(t)
	p := openProofForTest(t, nil)
	receipt, err := p.Fetch(context.Background(), fx.siteURL+"/redirect")
	if err != nil || receipt.Status != ProofOK {
		t.Fatalf("redirect fetch failed: %+v %v", receipt, err)
	}
	if receipt.FinalURL != fx.siteURL+"/page-a" || len(receipt.Redirects) != 1 {
		t.Fatalf("redirect chain wrong: %+v", receipt)
	}
	body, err := p.CaptureBody(receipt.CaptureID)
	if err != nil || string(body) != fx.pageBody {
		t.Fatal("redirected capture does not match fixture bytes")
	}
}

func TestProofBrowserRendersJavaScript(t *testing.T) {
	execCfg := proofBrowserConfig(t)
	fx := startProofFixtures(t)
	p := openProofForTest(t, nil)
	receipt, err := p.Browser(context.Background(), execCfg, fx.siteURL+"/js")
	if err != nil || receipt.Status != ProofOK {
		t.Fatalf("browser failed: %+v %v", receipt, err)
	}
	dom, err := p.CaptureBody(receipt.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dom), "rendered-proof-marker") {
		t.Fatalf("DOM shows no JS execution: %.300s", dom)
	}
	meta, err := p.CaptureMeta(receipt.CaptureID)
	if err != nil || meta.Operation != ProofBrowser || !meta.Complete {
		t.Fatalf("browser capture meta wrong: %+v %v", meta, err)
	}
}

func TestProofExecuteObservesOutboundRequests(t *testing.T) {
	python := proofPythonPath(t)
	fx := startProofFixtures(t)
	p := openProofForTest(t, nil)
	code := "import urllib.request\n" +
		"resp = urllib.request.urlopen('" + fx.searchURL + "?q=from-script&page=9', timeout=10)\n" +
		"print('script-saw:' + resp.read().decode()[:60])\n"
	receipt, err := p.Execute(context.Background(), ProofExecConfig{PythonPath: python}, code)
	if err != nil || receipt.Status != ProofOK {
		t.Fatalf("execute failed: %+v %v", receipt, err)
	}
	out, err := p.CaptureBody(receipt.CaptureID)
	if err != nil || !strings.Contains(string(out), "script-saw:") {
		t.Fatalf("script output not captured: %.300s %v", out, err)
	}
	meta, err := p.CaptureMeta(receipt.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	var observed []ObservedRequest
	if err := json.Unmarshal([]byte(meta.Extra["observed"]), &observed); err != nil {
		t.Fatalf("observed requests missing: %v %v", meta.Extra, err)
	}
	if len(observed) != 1 || !strings.Contains(observed[0].URL, "q=from-script") || observed[0].Status != 200 {
		t.Fatalf("outbound request not observed at proxy: %+v", observed)
	}
	if receipt.Subrequests != 1 {
		t.Fatalf("subrequest count wrong: %+v", receipt)
	}
}

func TestProofExecuteFailureIsExplicit(t *testing.T) {
	python := proofPythonPath(t)
	p := openProofForTest(t, nil)
	receipt, err := p.Execute(context.Background(), ProofExecConfig{PythonPath: python}, "raise SystemExit(7)")
	if err == nil || receipt.Status != ProofFailed || receipt.ErrorCode != "exit_7" {
		t.Fatalf("script failure not explicit: %+v %v", receipt, err)
	}
}

// TestProofTwoSimultaneousBrowserRenders keeps two renders in flight under one
// allowance with correlated captures. In isolated mode each render owns a
// per-op profile dir, so there is no shared Singleton contention.
func TestProofTwoSimultaneousBrowserRenders(t *testing.T) {
	execCfg := proofBrowserConfig(t)
	fx := startProofFixtures(t)
	p := openProofForTest(t, nil)
	ctx := context.Background()
	type outcome struct {
		receipt ProofReceipt
		err     error
	}
	results := make(chan outcome, 2)
	go func() { r, e := p.Browser(ctx, execCfg, fx.siteURL+"/js"); results <- outcome{r, e} }()
	go func() { r, e := p.Browser(ctx, execCfg, fx.siteURL+"/page-a"); results <- outcome{r, e} }()
	captures := map[string]string{}
	for range 2 {
		select {
		case o := <-results:
			if o.err != nil || o.receipt.Status != ProofOK {
				t.Fatalf("concurrent render failed: %+v %v", o.receipt, o.err)
			}
			body, err := p.CaptureBody(o.receipt.CaptureID)
			if err != nil {
				t.Fatal(err)
			}
			captures[o.receipt.CaptureID] = string(body)
		case <-time.After(120 * time.Second):
			t.Fatal("concurrent renders timed out")
		}
	}
	if len(captures) != 2 {
		t.Fatalf("renders did not produce distinct captures: %d", len(captures))
	}
	var sawJS, sawPage bool
	for _, body := range captures {
		if strings.Contains(body, "rendered-proof-marker") {
			sawJS = true
		}
		if strings.Contains(body, "t02-fixture-marker") {
			sawPage = true
		}
	}
	if !sawJS || !sawPage {
		t.Fatal("concurrent renders returned wrong bodies")
	}
	if p.Usage().Actions != 2 {
		t.Fatalf("allowance wrong: %+v", p.Usage())
	}
}

func proofBrowserConfig(t *testing.T) ProofExecConfig {
	t.Helper()
	return ProofExecConfig{ChromePath: proofChromePath(t),
		ChromeIsolatedProfile: os.Getenv("PROOF_CHROME_ISOLATED") == "1"}
}

func proofChromePath(t *testing.T) string {
	t.Helper()
	if custom := os.Getenv("PROOF_CHROME_BIN"); custom != "" {
		if _, err := os.Stat(custom); err != nil {
			t.Skipf("PROOF_CHROME_BIN missing: %s", custom)
		}
		return custom
	}
	for _, candidate := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no headless Chromium available for browser proof")
	return ""
}

func proofPythonPath(t *testing.T) string {
	t.Helper()
	if custom := os.Getenv("PROOF_PYTHON_BIN"); custom != "" {
		if _, err := os.Stat(custom); err != nil {
			t.Skipf("PROOF_PYTHON_BIN missing: %s", custom)
		}
		return custom
	}
	for _, candidate := range []string{"/usr/bin/python3", "/opt/homebrew/bin/python3", "/usr/local/bin/python3"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no python3 available for execute proof")
	return ""
}

func TestProofFingerprintStability(t *testing.T) {
	a := fingerprintParts(ProofAPI, "POST", "https://x.test/q", encodeParams(map[string]string{"b": "2", "a": "1"}), "application/json", "body")
	b := fingerprintParts(ProofAPI, "POST", "https://x.test/q", encodeParams(map[string]string{"a": "1", "b": "2"}), "application/json", "body")
	if a != b {
		t.Fatal("param order changes fingerprint")
	}
	c := fingerprintParts(ProofAPI, "POST", "https://x.test/q", encodeParams(map[string]string{"a": "1"}), "application/json", "body")
	if a == c {
		t.Fatal("dropped param keeps fingerprint")
	}
}
