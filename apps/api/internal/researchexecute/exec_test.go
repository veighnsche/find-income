package researchexecute

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func execInput(code string) researchcontract.ExecuteInput {
	return researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteExec,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationExec, Backend: BackendExec,
			URLOrQuery: "python3", Body: code,
		},
	}
}

func execEnv(t *testing.T, cfg Config) *testEnv {
	t.Helper()
	requireSandbox(t)
	cfg.PythonPath = pythonPath(t)
	return newTestEnv(t, cfg)
}

type execExtentLog struct {
	Runtime  string            `json:"runtime"`
	Exit     string            `json:"exit"`
	Egress   string            `json:"egress"`
	Observed []ObservedRequest `json:"observed"`
}

func execExtentOf(t *testing.T, env *testEnv, captureID string) execExtentLog {
	t.Helper()
	info, err := env.caps.Info(context.Background(), captureID)
	if err != nil {
		t.Fatal(err)
	}
	var ext execExtentLog
	if err := json.Unmarshal([]byte(info.ExtentJSON), &ext); err != nil {
		t.Fatalf("extent: %v (%q)", err, info.ExtentJSON)
	}
	return ext
}

func TestExecCapturesOutputAndObservedEgress(t *testing.T) {
	env := execEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/data", []byte(`{"v":42}`))
	code := "import urllib.request\n" +
		"body = urllib.request.urlopen('" + fix.url("/data") + "', timeout=10).read()\n" +
		"print('fetched bytes:', len(body))\n"
	out := env.execute(t, execInput(code))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s receipt=%+v", out.Outcome, out.Receipt)
	}
	if out.Receipt.Executor.Backend != BackendExec || out.Receipt.Executor.Version == "" {
		t.Fatalf("executor identity = %+v", out.Receipt.Executor)
	}
	_, combined := openCaptureBytes(t, env, out.CaptureID)
	if !strings.Contains(string(combined), "fetched bytes: 8") {
		t.Fatalf("combined output = %q", combined)
	}
	ext := execExtentOf(t, env, out.CaptureID)
	if ext.Runtime != "python3-sandboxed" || ext.Exit != "0" || ext.Egress != "proxy-mediated-only" {
		t.Fatalf("extent = %+v", ext)
	}
	if len(ext.Observed) != 1 || ext.Observed[0].URL != fix.url("/data") || ext.Observed[0].Status != 200 {
		t.Fatalf("observed = %+v", ext.Observed)
	}
	if out.Receipt.Subrequests != 1 || out.Receipt.UnknownUsage {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	desc, _ := openCaptureBytes(t, env, out.CaptureID)
	if !desc.Complete || desc.ContentType != "text/plain" {
		t.Fatalf("capture = %+v", desc)
	}
}

func TestExecDirectSocketBypassDenied(t *testing.T) {
	env := execEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/direct", []byte("x"))
	host := strings.TrimPrefix(fix.url("/direct"), "http://")
	hostOnly := strings.Split(host, "/")[0] // 127.0.0.1:port (NOT the proxy port)
	code := "import socket\n" +
		"s = socket.create_connection(('" + strings.Split(hostOnly, ":")[0] + "', " + strings.Split(hostOnly, ":")[1] + "), timeout=8)\n" +
		"print('bypass connected')\n"
	out := env.execute(t, execInput(code))
	// The script fails: only the proxy port is reachable. The denial, not a
	// timeout, proves confinement (an unconfined child would connect).
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	_, combined := openCaptureBytes(t, env, out.CaptureID)
	if !strings.Contains(string(combined), "Operation not permitted") {
		t.Fatalf("want EPERM denial, got %q", combined)
	}
	if strings.Contains(string(combined), "bypass connected") {
		t.Fatalf("bypass connected: %q", combined)
	}
	ext := execExtentOf(t, env, out.CaptureID)
	if len(ext.Observed) != 0 {
		t.Fatalf("denied egress must leave no proxy trace: %+v", ext.Observed)
	}
}

func TestExecProxyBypassDenied(t *testing.T) {
	env := execEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/nb", []byte("x"))
	// A script that deliberately disables proxy env must still fail: there is
	// no direct route out of the sandbox.
	code := "import urllib.request\n" +
		"opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))\n" +
		"print(opener.open('" + fix.url("/nb") + "', timeout=8).read())\n"
	out := env.execute(t, execInput(code))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	_, combined := openCaptureBytes(t, env, out.CaptureID)
	if !strings.Contains(string(combined), "Operation not permitted") {
		t.Fatalf("want EPERM denial, got %q", combined)
	}
}

func TestExecExitCodeExplicitWithOutput(t *testing.T) {
	env := execEnv(t, Config{})
	out := env.execute(t, execInput("import sys\nprint('partial evidence')\nsys.exit(7)\n"))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.Status != researchcontract.ReceiptFailed || out.Receipt.ErrorCode != "exit_7" {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	// Failure output is still captured evidence.
	_, combined := openCaptureBytes(t, env, out.CaptureID)
	if !strings.Contains(string(combined), "partial evidence") {
		t.Fatalf("combined = %q", combined)
	}
	ext := execExtentOf(t, env, out.CaptureID)
	if ext.Exit != "7" {
		t.Fatalf("extent = %+v", ext)
	}
}

func TestExecFailsClosedWithoutSandbox(t *testing.T) {
	env := newTestEnv(t, Config{PythonPath: pythonPath(t), SandboxBinary: "/nonexistent/sandbox-exec"})
	out := env.execute(t, execInput("print('never runs')\n"))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.ErrorCode != ErrorCodeSandboxUnavailable {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	if out.CaptureID != "" {
		t.Fatal("unmediated exec must not produce a capture")
	}
}

func TestExecBackendNotConfigured(t *testing.T) {
	env := newTestEnv(t, Config{})
	out := env.execute(t, execInput("print(1)\n"))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.ErrorCode != ErrorCodeBackendNotConfigured {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
}

func TestExecOutputCutIsExplicit(t *testing.T) {
	env := execEnv(t, Config{})
	out := env.execute(t, execInput("print('y' * 200000)\n"))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s receipt=%+v", out.Outcome, out.Receipt)
	}
	if !out.Receipt.Truncated {
		t.Fatal("cut output must mark truncation")
	}
	desc, body := openCaptureBytes(t, env, out.CaptureID)
	if desc.Complete || desc.Completeness != store.CapturePartial {
		t.Fatalf("capture = %+v", desc)
	}
	if len(body) > execStreamCap*2+100 {
		t.Fatalf("cap not applied: %d", len(body))
	}
	obs := mustObservation(t, env, out.ObservationID)
	if obs.TruncationNote == "" {
		t.Fatal("missing truncation note")
	}
}
