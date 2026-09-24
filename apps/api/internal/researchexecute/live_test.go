package researchexecute

import (
	"os"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// TestLivePublicCheck is the small live public-source check (T02 §3.1e
// pattern): one direct fetch and one proxy-mediated script fetch of
// example.com under the PRODUCTION destination policy (no loopback). It runs
// only with T16_LIVE_CHECK=1; no spend, no credentials, two tiny GETs.
func TestLivePublicCheck(t *testing.T) {
	if os.Getenv("T16_LIVE_CHECK") != "1" {
		t.Skip("live check gated behind T16_LIVE_CHECK=1")
	}
	env := newTestEnv(t, Config{PythonPath: pythonPath(t)})
	env.ex.deps.permitLoopback = false
	requireSandbox(t)

	fetch := env.execute(t, fetchInput("http://example.com/"))
	if fetch.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("fetch outcome = %s receipt=%+v", fetch.Outcome, fetch.Receipt)
	}
	_, body := openCaptureBytes(t, env, fetch.CaptureID)
	if !strings.Contains(string(body), "Example Domain") {
		t.Fatalf("unexpected live body: %q", body)
	}
	t.Logf("live fetch: %d bytes capture=%s", len(body), fetch.CaptureID)

	code := "import urllib.request\n" +
		"body = urllib.request.urlopen('http://example.com/', timeout=15).read()\n" +
		"print('live bytes:', len(body))\n"
	run := env.execute(t, execInput(code))
	if run.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("exec outcome = %s receipt=%+v", run.Outcome, run.Receipt)
	}
	_, combined := openCaptureBytes(t, env, run.CaptureID)
	if !strings.Contains(string(combined), "live bytes:") {
		t.Fatalf("combined = %q", combined)
	}
	ext := execExtentOf(t, env, run.CaptureID)
	if len(ext.Observed) != 1 || ext.Observed[0].Status != 200 {
		t.Fatalf("observed = %+v", ext.Observed)
	}
	t.Logf("live exec: %s", strings.TrimSpace(string(combined)))
}
