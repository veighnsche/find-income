package researchexecute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchmemory"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func fetchInput(url string) researchcontract.ExecuteInput {
	return researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: BackendHTTP, URLOrQuery: url,
		},
	}
}

func openCaptureBytes(t *testing.T, env *testEnv, captureID string) (researchcontract.Capture, []byte) {
	t.Helper()
	desc, rc, err := env.caps.OpenCapture(context.Background(), captureID)
	if err != nil {
		t.Fatalf("OpenCapture: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return desc, body
}

func TestExecuteFetchCapturesAuthenticBytes(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	want := []byte("<html><body>authentic fixture bytes</body></html>")
	fix.set("/page", want)
	out := env.execute(t, fetchInput(fix.url("/page")))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.Status != researchcontract.ReceiptOK {
		t.Fatalf("receipt status = %s", out.Receipt.Status)
	}
	if out.Receipt.CaptureID == "" || out.CaptureID == "" || out.ObservationID == "" {
		t.Fatal("missing capture/observation binding")
	}
	sum := sha256.Sum256(want)
	if out.Receipt.CaptureID != hex.EncodeToString(sum[:]) {
		t.Fatal("receipt captureId is not the content sha256")
	}
	desc, body := openCaptureBytes(t, env, out.CaptureID)
	if string(body) != string(want) {
		t.Fatal("captured bytes differ from served bytes")
	}
	if desc.SHA256 != out.Receipt.CaptureID || !desc.Complete || desc.Completeness != store.CaptureComplete {
		t.Fatalf("capture descriptor = %+v", desc)
	}
	if desc.Provenance != researchcontract.ProvenanceFetchedResponse {
		t.Fatalf("provenance = %s", desc.Provenance)
	}
	if desc.FinalURL != fix.url("/page") || desc.Bytes != int64(len(want)) {
		t.Fatalf("descriptor = %+v", desc)
	}
	// The receipt resolves through the trusted reader and binds the same bytes.
	rec, err := env.caps.ResolveReceipt(context.Background(), out.Receipt.ID)
	if err != nil {
		t.Fatalf("ResolveReceipt: %v", err)
	}
	if rec.CaptureID != out.Receipt.CaptureID || rec.Fingerprint == "" {
		t.Fatalf("resolved receipt = %+v", rec)
	}
	if out.Usage.Requests != 1 || out.Usage.Redirects != 0 || out.Usage.Bytes <= 0 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestExecuteSearchPreservesParamOrder(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/search", []byte(`{"results":[]}`))
	out := env.execute(t, researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteSearch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationSearch, Backend: BackendHTTP, URLOrQuery: fix.url("/search"),
			Params:     []researchcontract.Param{{Name: "q", Value: "a b"}, {Name: "lang", Value: "en"}, {Name: "q", Value: "second"}},
			Pagination: researchcontract.Pagination{Page: "2"},
		},
	})
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	// Order and repeats preserved exactly (space form-encoded as +).
	if got := fix.queryOf("/search"); got != "q=a+b&lang=en&q=second&page=2" {
		t.Fatalf("query = %q", got)
	}
	desc, _ := openCaptureBytes(t, env, out.CaptureID)
	if desc.Provenance != researchcontract.ProvenanceSearchResult || !desc.IsSnippet {
		t.Fatalf("search capture = %+v", desc)
	}
}

func TestExecuteAPIReadOnlyPOST(t *testing.T) {
	env := newTestEnv(t, Config{})
	var gotMethod, gotCT, gotQuery, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotCT, gotQuery = r.Method, r.Header.Get("Content-Type"), r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"echo":true}`))
	}))
	t.Cleanup(srv.Close)
	out := env.execute(t, researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteAPI,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationAPI, Backend: BackendHTTP,
			Method: "POST", URLOrQuery: srv.URL + "/query",
			Body: `{"q":"x"}`,
			Params: []researchcontract.Param{
				{Name: "Content-Type", Value: "application/json"},
				{Name: "v", Value: "1"},
			},
		},
	})
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if gotMethod != "POST" || gotCT != "application/json" || gotBody != `{"q":"x"}` {
		t.Fatalf("method=%s ct=%s body=%s", gotMethod, gotCT, gotBody)
	}
	if gotQuery != "v=1" {
		t.Fatalf("Content-Type leaked into query: %q", gotQuery)
	}
	_, body := openCaptureBytes(t, env, out.CaptureID)
	if string(body) != `{"echo":true}` {
		t.Fatalf("body = %s", body)
	}
}

func TestExecuteAPIVerbRejectedPreDispatch(t *testing.T) {
	for _, method := range []string{"PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			env := newTestEnv(t, Config{})
			_, err := env.ex.Execute(context.Background(), researchcontract.ExecuteInput{
				Kind: researchcontract.ExecuteAPI, RunID: "run-1", Generation: 1,
				Request: researchcontract.RequestDescriptor{
					Operation: researchcontract.OperationAPI, Backend: BackendHTTP,
					Method: method, URLOrQuery: "https://example.com/x",
				},
			})
			mustContractErr(t, err, researchcontract.OutcomeInvalid)
			if checks, reserves, _ := env.auth.counts(); checks != 0 || reserves != 0 {
				t.Fatalf("rejected verb consumed authority: checks=%d reserves=%d", checks, reserves)
			}
		})
	}
}

func TestExecuteRedirectChainRecorded(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/final", []byte("landed"))
	fix.redirect("/r1", fix.url("/r2"))
	fix.redirect("/r2", fix.url("/final"))
	out := env.execute(t, fetchInput(fix.url("/r1")))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if len(out.Receipt.Redirects) != 2 || out.Receipt.FinalURL != fix.url("/final") {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	if out.Usage.Requests != 3 || out.Usage.Redirects != 2 {
		t.Fatalf("usage = %+v", out.Usage)
	}
	_, body := openCaptureBytes(t, env, out.CaptureID)
	if string(body) != "landed" {
		t.Fatalf("body = %s", body)
	}
}

func TestExecutePrivateLiteralRejectedPreDispatch(t *testing.T) {
	for _, target := range []string{
		"http://10.0.0.1/", "http://192.168.1.1/", "http://169.254.169.254/",
		"http://[fc00::1]/", "ftp://example.com/x",
		"https://user@example.com/", "http://example.com:0x50/",
	} {
		t.Run(target, func(t *testing.T) {
			env := newTestEnv(t, Config{})
			_, err := env.ex.Execute(context.Background(), researchcontract.ExecuteInput{
				Kind: researchcontract.ExecuteFetch, RunID: "run-1", Generation: 1,
				Request: researchcontract.RequestDescriptor{
					Operation: researchcontract.OperationFetch, Backend: BackendHTTP, URLOrQuery: target,
				},
			})
			mustContractErr(t, err, researchcontract.OutcomeInvalid)
			if checks, reserves, _ := env.auth.counts(); checks != 0 || reserves != 0 {
				t.Fatalf("rejected URL consumed authority: checks=%d reserves=%d", checks, reserves)
			}
		})
	}
}

func TestExecuteLoopbackRefusedWithoutFixturePolicy(t *testing.T) {
	env := newTestEnv(t, Config{})
	env.ex.deps.permitLoopback = false // production policy
	fix := newFixtureServer(t)
	fix.set("/loop", []byte("x"))
	out := env.execute(t, fetchInput(fix.url("/loop")))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.ErrorCode != ErrorCodeDestinationForbidden {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	if fix.hitsOf("/loop") != 0 {
		t.Fatal("fenced destination was contacted")
	}
}

func TestExecuteRedirectToPrivateFencedAtHop(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	// Link-local target: fenced before any dial (a dial attempt would fail
	// differently, and the private address is never connected).
	fix.redirect("/evil", "http://169.254.169.254/latest/meta-data")
	out := env.execute(t, fetchInput(fix.url("/evil")))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.Status != researchcontract.ReceiptFailed || out.Receipt.ErrorCode != ErrorCodeDestinationForbidden {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	if out.CaptureID != "" {
		t.Fatal("fenced hop must not produce a capture")
	}
}

func TestExecuteRedirectLimitExplicit(t *testing.T) {
	env := newTestEnv(t, Config{MaxRedirects: 2})
	fix := newFixtureServer(t)
	fix.redirect("/a", fix.url("/b"))
	fix.redirect("/b", fix.url("/c"))
	fix.redirect("/c", fix.url("/d"))
	fix.set("/d", []byte("too far"))
	out := env.execute(t, fetchInput(fix.url("/a")))
	if out.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.ErrorCode != ErrorCodeRedirectLimit {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
}

func TestExecuteStatusMapping(t *testing.T) {
	env := newTestEnv(t, Config{})
	mux := http.NewServeMux()
	mux.HandleFunc("/ok-empty", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/denied", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 403) })
	mux.HandleFunc("/slow-down", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "slow", 429) })
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cases := []struct {
		path        string
		outcome     researchcontract.Outcome
		status      researchcontract.ReceiptStatus
		code        string
		wantCapture bool
	}{
		{"/ok-empty", researchcontract.OutcomeOK, researchcontract.ReceiptOK, "", false},
		{"/missing", researchcontract.OutcomeInvalid, researchcontract.ReceiptFailed, researchmemory.ErrorCodeHTTP404, false},
		{"/denied", researchcontract.OutcomeOK, researchcontract.ReceiptOK, "http_403", false},
		{"/slow-down", researchcontract.OutcomeOK, researchcontract.ReceiptOK, "http_429", false},
		{"/boom", researchcontract.OutcomeInvalid, researchcontract.ReceiptFailed, "http_500", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			out := env.execute(t, fetchInput(srv.URL+c.path))
			if out.Outcome != c.outcome || out.Receipt.Status != c.status || out.Receipt.ErrorCode != c.code {
				t.Fatalf("outcome=%s receipt=%+v", out.Outcome, out.Receipt)
			}
			if (out.CaptureID != "") != c.wantCapture {
				t.Fatalf("captureId=%q", out.CaptureID)
			}
		})
	}
	// The 404 result reuses with the 404 negative TTL (no redispatch).
	out := env.execute(t, fetchInput(srv.URL+"/missing"))
	if out.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("404 repeat outcome = %s", out.Outcome)
	}
}

func TestExecuteExactRepeatReusesWithoutRedispatch(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/stable", []byte("v1"))
	first := env.execute(t, fetchInput(fix.url("/stable")))
	second := env.execute(t, fetchInput(fix.url("/stable")))
	if second.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("outcome = %s", second.Outcome)
	}
	if fix.hitsOf("/stable") != 1 {
		t.Fatalf("redispatch on repeat: hits=%d", fix.hitsOf("/stable"))
	}
	// Reuse returns the ORIGINAL backend-issued receipt, resolvable as ever.
	if second.Receipt.ID != first.Receipt.ID {
		t.Fatal("reuse must return the original receipt, not a fabrication")
	}
	if _, err := env.caps.ResolveReceipt(context.Background(), second.Receipt.ID); err != nil {
		t.Fatalf("reused receipt unresolvable: %v", err)
	}
	if second.ObservationID != first.ObservationID || second.CaptureID != first.CaptureID {
		t.Fatal("reuse must bind the original observation/capture")
	}
}

func TestExecuteClaimedElsewhere(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/busy", []byte("x"))
	req := researchcontract.RequestDescriptor{
		Operation: researchcontract.OperationFetch, Backend: BackendHTTP, URLOrQuery: fix.url("/busy"),
	}
	if _, err := env.mem.Claim(context.Background(), "run-1", "rival-worker", 1, req, "rival-key"); err != nil {
		t.Fatal(err)
	}
	out := env.execute(t, researchcontract.ExecuteInput{Kind: researchcontract.ExecuteFetch, Request: req})
	if out.Outcome != researchcontract.OutcomeClaimedElsewhere {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.ID != "" {
		t.Fatal("no receipt is issued when another worker holds the claim")
	}
	if fix.hitsOf("/busy") != 0 {
		t.Fatal("fenced execution must not dispatch")
	}
}

func TestExecuteUncertainRefusesBlindRetry(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/flaky", []byte("x"))
	req := researchcontract.RequestDescriptor{
		Operation: researchcontract.OperationFetch, Backend: BackendHTTP, URLOrQuery: fix.url("/flaky"),
	}
	claim, err := env.mem.Claim(context.Background(), "run-1", "w1", 1, req, "k1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.mem.Observe(context.Background(), researchmemory.ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "w1", Generation: 1, RoundID: "run-1",
		ActualURLOrQuery: fix.url("/flaky"), Outcome: store.ObservationUncertain,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    researchcontract.ExecutionReceipt{ID: "uncertain-receipt-1", Status: researchcontract.ReceiptUncertain},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := env.execute(t, researchcontract.ExecuteInput{Kind: researchcontract.ExecuteFetch, Request: req})
	if out.Outcome != researchcontract.OutcomeUncertain {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if fix.hitsOf("/flaky") != 0 {
		t.Fatal("uncertain request must not blindly redispatch")
	}
}

func TestExecuteBytesBoundExplicitIncomplete(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/big", []byte(strings.Repeat("0123456789abcdef", 64))) // 1024 bytes
	in := fetchInput(fix.url("/big"))
	in.Bounds.MaxBytes = 32
	out := env.execute(t, in)
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if !out.Receipt.Truncated {
		t.Fatal("bound overrun must set truncated")
	}
	desc, body := openCaptureBytes(t, env, out.CaptureID)
	if len(body) != 32 || desc.Complete || desc.Completeness != store.CaptureTruncated {
		t.Fatalf("capture = %+v len=%d", desc, len(body))
	}
	obs := mustObservation(t, env, out.ObservationID)
	if obs.TruncationNote == "" {
		t.Fatal("incomplete capture requires an explicit truncation note")
	}
}

func TestExecuteDeadlineUncertain(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/slow", []byte("eventually"))
	fix.delay["/slow"] = 5 * time.Second
	in := fetchInput(fix.url("/slow"))
	in.Bounds.DeadlineMs = 300
	out := env.execute(t, in)
	if out.Outcome != researchcontract.OutcomeUncertain {
		t.Fatalf("outcome = %s", out.Outcome)
	}
	if out.Receipt.Status != researchcontract.ReceiptUncertain || !out.Receipt.UnknownUsage {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	if out.Receipt.ErrorCode != ErrorCodeDeadlineExceeded {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
}

func TestExecuteCancellationHonest(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/hang", []byte("never in time"))
	fix.delay["/hang"] = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan researchcontract.ExecuteOutput, 1)
	errCh := make(chan error, 1)
	go func() {
		out, err := env.ex.Execute(ctx, researchcontract.ExecuteInput{
			Kind: researchcontract.ExecuteFetch, RunID: "run-1", Generation: 1,
			Request: researchcontract.RequestDescriptor{
				Operation: researchcontract.OperationFetch, Backend: BackendHTTP, URLOrQuery: fix.url("/hang"),
			},
		})
		if err != nil {
			errCh <- err
			return
		}
		done <- out
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case out := <-done:
		if out.Receipt.Status != researchcontract.ReceiptCanceled {
			t.Fatalf("receipt = %+v", out.Receipt)
		}
		if out.Receipt.ErrorCode != researchmemory.ErrorCodeCanceled {
			t.Fatalf("receipt = %+v", out.Receipt)
		}
		obs := mustObservation(t, env, out.ObservationID)
		if obs.Outcome != store.ObservationFailed || obs.ErrorCode != researchmemory.ErrorCodeCanceled {
			t.Fatalf("observation = %+v", obs)
		}
	case err := <-errCh:
		t.Fatalf("cancel must produce a canceled receipt, got error: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("canceled execution never settled")
	}
}

func TestExecuteConcurrencyBounded(t *testing.T) {
	env := newTestEnv(t, Config{MaxConcurrent: 2})
	fix := newFixtureServer(t)
	for i := 0; i < 4; i++ {
		path := fmt.Sprintf("/c%d", i)
		fix.set(path, []byte("ok"))
		fix.delay[path] = 400 * time.Millisecond
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	outs := make(chan researchcontract.ExecuteOutput, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := env.ex.Execute(context.Background(), researchcontract.ExecuteInput{
				Kind: researchcontract.ExecuteFetch, RunID: "run-1", Generation: 1,
				IdempotencyKey: fmt.Sprintf("conc-%d", i),
				Request: researchcontract.RequestDescriptor{
					Operation: researchcontract.OperationFetch, Backend: BackendHTTP,
					URLOrQuery: fix.url(fmt.Sprintf("/c%d", i)),
				},
			})
			if err != nil {
				errs <- err
				return
			}
			outs <- out
		}(i)
	}
	wg.Wait()
	close(errs)
	close(outs)
	for err := range errs {
		t.Fatalf("concurrent execute: %v", err)
	}
	n := 0
	seen := map[string]bool{}
	for out := range outs {
		n++
		if out.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("outcome = %s", out.Outcome)
		}
		if seen[out.Receipt.ID] {
			t.Fatal("duplicate receipt id")
		}
		seen[out.Receipt.ID] = true
	}
	if n != 4 {
		t.Fatalf("n = %d", n)
	}
	if peak := fix.peak.Load(); peak > 2 {
		t.Fatalf("concurrency bound violated: peak=%d", peak)
	}
	if peak := fix.peak.Load(); peak < 2 {
		t.Fatalf("expected real concurrency, peak=%d", peak)
	}
}

func TestExecuteAuthorityRefusalsPropagate(t *testing.T) {
	env := newTestEnv(t, Config{})
	fix := newFixtureServer(t)
	fix.set("/gated", []byte("x"))
	withRun := func(in researchcontract.ExecuteInput) researchcontract.ExecuteInput {
		in.RunID, in.Generation = "run-1", 1
		return in
	}
	env.auth.failCheck = researchcontract.NewError(researchcontract.OutcomeStopped, "run", "run is stopping")
	_, err := env.ex.Execute(context.Background(), withRun(fetchInput(fix.url("/gated"))))
	mustContractErr(t, err, researchcontract.OutcomeStopped)
	if fix.hitsOf("/gated") != 0 {
		t.Fatal("fenced execution dispatched")
	}

	env.auth.failCheck = nil
	env.auth.failReserve = researchcontract.NewError(researchcontract.OutcomeBudgetExhausted, "allowance", "empty")
	_, err = env.ex.Execute(context.Background(), withRun(fetchInput(fix.url("/gated"))))
	mustContractErr(t, err, researchcontract.OutcomeBudgetExhausted)
	if fix.hitsOf("/gated") != 0 {
		t.Fatal("unfunded execution dispatched")
	}
}

func TestExecuteDescriptorValidation(t *testing.T) {
	env := newTestEnv(t, Config{})
	cases := []struct {
		name   string
		mutate func(*researchcontract.ExecuteInput)
	}{
		{"kind-operation mismatch", func(in *researchcontract.ExecuteInput) {
			in.Request.Operation = researchcontract.OperationFetch
		}},
		{"wrong backend", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteFetch
			in.Request.Operation = researchcontract.OperationFetch
			in.Request.Backend = "chromium-headless-shell"
		}},
		{"body on GET", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteFetch
			in.Request.Operation = researchcontract.OperationFetch
			in.Request.Backend = BackendHTTP
			in.Request.Body = "nope"
		}},
		{"body sha mismatch", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteAPI
			in.Request.Operation = researchcontract.OperationAPI
			in.Request.Backend = BackendHTTP
			in.Request.Method = "POST"
			in.Request.Body = "abc"
			in.Request.BodySHA256 = "deadbeef"
		}},
		{"unknown session field", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteFetch
			in.Request.Operation = researchcontract.OperationFetch
			in.Request.Backend = BackendHTTP
			in.Request.SessionFields = map[string]string{"cookies": "yes"}
		}},
		{"viewport on fetch", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteFetch
			in.Request.Operation = researchcontract.OperationFetch
			in.Request.Backend = BackendHTTP
			in.Request.SessionFields = map[string]string{"viewport": "1280x800"}
		}},
		{"bad viewport", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteBrowse
			in.Request.Operation = researchcontract.OperationBrowser
			in.Request.Backend = BackendBrowser
			in.Request.SessionFields = map[string]string{"viewport": "huge"}
		}},
		{"content-type on GET", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteFetch
			in.Request.Operation = researchcontract.OperationFetch
			in.Request.Backend = BackendHTTP
			in.Request.Params = []researchcontract.Param{{Name: "Content-Type", Value: "text/html"}}
		}},
		{"exec params", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteExec
			in.Request.Operation = researchcontract.OperationExec
			in.Request.Backend = BackendExec
			in.Request.URLOrQuery = "python3"
			in.Request.Body = "print(1)"
			in.Request.Params = []researchcontract.Param{{Name: "x", Value: "y"}}
		}},
		{"negative bounds", func(in *researchcontract.ExecuteInput) {
			in.Kind = researchcontract.ExecuteFetch
			in.Request.Operation = researchcontract.OperationFetch
			in.Request.Backend = BackendHTTP
			in.Bounds.MaxBytes = -1
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := researchcontract.ExecuteInput{
				Kind: researchcontract.ExecuteSearch, RunID: "run-1", Generation: 1,
				Request: researchcontract.RequestDescriptor{
					Operation: researchcontract.OperationSearch, Backend: BackendHTTP,
					URLOrQuery: "https://example.com/",
				},
			}
			c.mutate(&in)
			_, err := env.ex.Execute(context.Background(), in)
			mustContractErr(t, err, researchcontract.OutcomeInvalid)
		})
	}
	if checks, reserves, _ := env.auth.counts(); checks != 0 || reserves != 0 {
		t.Fatalf("invalid descriptors consumed authority: checks=%d reserves=%d", checks, reserves)
	}
}

func mustObservation(t *testing.T, env *testEnv, id string) store.ResearchObservation {
	t.Helper()
	var obs store.ResearchObservation
	if err := env.db.Read(context.Background(), func(r store.Reader) error {
		var err error
		obs, err = store.GetResearchObservation(context.Background(), r, id)
		return err
	}); err != nil {
		t.Fatalf("GetResearchObservation: %v", err)
	}
	return obs
}
