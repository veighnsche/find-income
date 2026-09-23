package codexservice

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func testService(t *testing.T, cfg Config) (*Service, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); db.Close() })
	return s, db
}
func testConfig() Config {
	return Config{Host: "isolated.test", User: "runner", IdentityFile: "/key", KnownHostsFile: "/known", Launcher: "/runner/launch", IsolationVerified: true, BridgeToken: strings.Repeat("s", 64)}
}

func TestUnavailableDoesNotDialOrClaim(t *testing.T) {
	for _, test := range []struct {
		cfg  Config
		code string
	}{{Config{}, "runner_not_configured"}, {Config{Host: "host", User: "user", Launcher: "/launch", IdentityFile: "/key", KnownHostsFile: "/known"}, "isolation_not_verified"}} {
		s, db := testService(t, test.cfg)
		s.dial = func(context.Context, Config) (io.ReadWriteCloser, error) {
			t.Error("unverified runner dialed")
			return nil, ErrUnavailable
		}
		item, _, err := db.SubmitIngestion(context.Background(), store.Actor{Kind: "administrator", ID: "owner"}, store.IngestionInput{Origin: "owner", OriginalText: "Vacancy", IdempotencyKey: "one"})
		if err != nil {
			t.Fatal(err)
		}
		status := s.Status(context.Background())
		if status.Code != test.code || status.IngestionAvailable || status.Connected {
			t.Fatal(status)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err = s.Run(ctx); err != nil {
			t.Fatal(err)
		}
		current, _ := db.Ingestion(context.Background(), item.ID)
		job, _ := db.Job(context.Background(), current.JobID)
		if current.JobState != store.JobQueued || job.AttemptCount != 0 {
			t.Fatal(current)
		}
	}
}

type authTransport struct{ token string }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(r)
}
func sdkSession(t *testing.T, s *Service) (*mcp.ClientSession, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(s.MCPHandler())
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "synthetic", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &http.Client{Transport: authTransport{s.cfg.BridgeToken}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, server
}
func toolCall(ctx context.Context, session *mcp.ClientSession, name string, args any) (*mcp.CallToolResult, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return nil, fmt.Errorf("tool %s rejected", name)
	}
	return result, nil
}

func TestSyntheticRuntimeThroughSDKPersistsSourcedOpportunity(t *testing.T) {
	s, db := testService(t, testConfig())
	session, _ := sdkSession(t, s)
	ctx := context.Background()
	source := "Backend platform engineering. Pay negotiable."
	item, _, err := db.SubmitIngestion(ctx, store.Actor{Kind: "administrator", ID: "owner"}, store.IngestionInput{Origin: "owner", OriginalText: source, IdempotencyKey: "paste"})
	if err != nil {
		t.Fatal(err)
	}
	claim, found, err := db.ClaimNextJob(ctx, "runtime-test", []string{store.IngestionJobKind}, time.Minute, time.Now())
	if err != nil || !found {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	t.Cleanup(func() { right.Close() })
	s.dial = func(context.Context, Config) (io.ReadWriteCloser, error) { return left, nil }
	peerDone := make(chan error, 1)
	terminalStates := make(chan string, 4)
	var capability string
	go func() {
		reader := bufio.NewReader(right)
		sequence := 0
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				peerDone <- err
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &req) != nil {
				peerDone <- fmt.Errorf("bad request")
				return
			}
			if req.Method == "initialized" {
				continue
			}
			var result any
			switch req.Method {
			case "initialize":
				result = map[string]any{"userAgent": "jobseek_dashboard/0.153.4 (Linux; x86_64)", "codexHome": "/runner/state", "platformFamily": "unix", "platformOs": "linux"}
			case "account/read":
				result = map[string]any{"account": map[string]string{"type": "chatgpt"}, "requiresOpenaiAuth": true}
			case "mcpServerStatus/list":
				tools := map[string]any{}
				for _, name := range requiredTools {
					tools[name] = map[string]any{"name": name, "inputSchema": map[string]string{"type": "object"}}
				}
				result = map[string]any{"data": []any{map[string]any{"name": "jobseek", "tools": tools}}}
			case "thread/start":
				sequence++
				result = map[string]any{"thread": map[string]string{"id": fmt.Sprintf("thread-%d", sequence)}}
			case "turn/start":
				var p struct {
					Input []struct {
						Text string `json:"text"`
					} `json:"input"`
				}
				_ = json.Unmarshal(req.Params, &p)
				var in struct {
					Capability string `json:"capability"`
				}
				_ = json.Unmarshal([]byte(p.Input[0].Text), &in)
				capability = in.Capability
				result = map[string]any{"turn": map[string]any{"id": fmt.Sprintf("turn-%d", sequence), "status": "inProgress", "items": []any{}}}
			default:
				peerDone <- fmt.Errorf("unexpected method %s", req.Method)
				return
			}
			response, _ := json.Marshal(map[string]any{"id": req.ID, "result": result})
			if _, err = right.Write(append(response, '\n')); err != nil {
				peerDone <- err
				return
			}
			if req.Method != "turn/start" {
				continue
			}
			if _, err = toolCall(ctx, session, "ingestion_context", capabilityArgs{capability}); err != nil {
				peerDone <- err
				return
			}
			args := saveArgs{Capability: capability, CompanyName: "Synthetic Example", Title: "Engineer", Kind: "employment", WorkPattern: "unknown"}
			if _, err = toolCall(ctx, session, "save_vacancy", args); err != nil {
				peerDone <- err
				return
			}
			// Replay save through the real SDK; the transaction returns same record.
			if _, err = toolCall(ctx, session, "save_vacancy", args); err != nil {
				peerDone <- err
				return
			}
			s.toolMu.Lock()
			intakeID := s.active.intakeID
			s.toolMu.Unlock()
			current, _ := db.Ingestion(ctx, intakeID)
			versions, _ := db.QualificationInputVersions(ctx, current.OpportunityID)
			if _, err = toolCall(ctx, session, "source_context", capabilityArgs{capability}); err != nil {
				peerDone <- err
				return
			}
			observation := evidenceArgs{Capability: capability, Quote: "Pay negotiable.", Criterion: "monthly_base_salary", Finding: "ambiguous", ObservedValue: "Pay negotiable", ExpectedEvidenceVersion: versions.EvidenceVersion, ExpectedPreferencesVersion: versions.PreferencesVersion}
			if _, err = toolCall(ctx, session, "add_evidence", observation); err != nil {
				peerDone <- err
				return
			}
			status := "completed"
			select {
			case status = <-terminalStates:
			default:
			}
			terminal, _ := json.Marshal(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": fmt.Sprintf("thread-%d", sequence), "turn": map[string]any{"id": fmt.Sprintf("turn-%d", sequence), "status": status, "items": []any{}}}})
			if _, err = right.Write(append(terminal, '\n')); err != nil {
				peerDone <- err
				return
			}
			peerDone <- nil
		}
	}()
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := s.Handle(bounded, claim)
	if err != nil {
		select {
		case peerErr := <-peerDone:
			t.Fatalf("handle %v, peer %v", err, peerErr)
		default:
			t.Fatal(err)
		}
	}
	if err = <-peerDone; err != nil {
		t.Fatal(err)
	}
	current, err := db.Ingestion(ctx, item.ID)
	if err != nil || current.SourceID == "" || current.RecordChangeID == "" || result.Ref != current.OpportunityID {
		t.Fatalf("mapping %+v %v", current, err)
	}
	op, err := db.Opportunity(ctx, result.Ref)
	if err != nil || op.OriginalText != source {
		t.Fatal("source not preserved", err)
	}
	claims, err := db.ListEvidence(ctx, op.ID, false, "", 20)
	if err != nil || len(claims.Items) != 1 || claims.Items[0].ConfirmationState == "confirmed" {
		t.Fatalf("published unknown %+v %v", claims, err)
	}
	if _, err = toolCall(ctx, session, "save_vacancy", saveArgs{Capability: capability, CompanyName: "Late", Title: "Late", Kind: "employment"}); err == nil {
		t.Fatal("revoked capability accepted")
	}
	if _, err = db.CompleteJob(ctx, claim, result, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A distinct intake of the exact same source links the existing record.
	second, _, err := db.SubmitIngestion(ctx, store.Actor{Kind: "administrator", ID: "owner"}, store.IngestionInput{Origin: "owner", OriginalText: source, IdempotencyKey: "same-source-new-intake"})
	if err != nil {
		t.Fatal(err)
	}
	secondClaim, found, err := db.ClaimNextJob(ctx, "runtime-test", []string{store.IngestionJobKind}, time.Minute, time.Now())
	if err != nil || !found {
		t.Fatal(err)
	}
	secondResult, err := s.Handle(bounded, secondClaim)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-peerDone; err != nil {
		t.Fatal(err)
	}
	secondMapped, _ := db.Ingestion(ctx, second.ID)
	if secondResult.Ref != result.Ref || secondMapped.OpportunityID != current.OpportunityID {
		t.Fatal("same source created duplicate")
	}
	if _, err = db.CompleteJob(ctx, secondClaim, secondResult, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Different source text is not silently merged. A failed turn after core
	// save remains failed, then explicit retry runs another turn on that record.
	partial, _, err := db.SubmitIngestion(ctx, store.Actor{Kind: "administrator", ID: "owner"}, store.IngestionInput{Origin: "owner", OriginalText: source + " Different role.", IdempotencyKey: "partial"})
	if err != nil {
		t.Fatal(err)
	}
	partialClaim, found, err := db.ClaimNextJob(ctx, "runtime-test", []string{store.IngestionJobKind}, time.Minute, time.Now())
	if err != nil || !found {
		t.Fatal(err)
	}
	terminalStates <- "failed"
	_, err = s.Handle(bounded, partialClaim)
	if err == nil {
		t.Fatal("failed turn became success")
	}
	if peerErr := <-peerDone; peerErr != nil {
		t.Fatal(peerErr)
	}
	if _, err = db.FailJob(ctx, partialClaim, store.JobFailure{Code: "turn_failed", Message: "Synthetic failed turn"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	partialMapped, _ := db.Ingestion(ctx, partial.ID)
	if partialMapped.OpportunityID == "" || partialMapped.OpportunityID == current.OpportunityID || partialMapped.Status != "failed" {
		t.Fatal("partial result misrepresented", partialMapped)
	}
	if _, err = db.RetryIngestion(ctx, store.Actor{Kind: "administrator", ID: "owner"}, partial.ID, nil); err != nil {
		t.Fatal(err)
	}
	retryClaim, found, err := db.ClaimNextJob(ctx, "runtime-test", []string{store.IngestionJobKind}, time.Minute, time.Now())
	if err != nil || !found {
		t.Fatal(err)
	}
	retryResult, err := s.Handle(bounded, retryClaim)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-peerDone; err != nil {
		t.Fatal(err)
	}
	if retryResult.Ref != partialMapped.OpportunityID {
		t.Fatal("retry did not reuse saved record")
	}
}

func TestBridgeAuthAndPublicFetchBoundary(t *testing.T) {
	s, _ := testService(t, testConfig())
	for _, headers := range []map[string]string{{}, {"Authorization": "Bearer wrong"}, {"Authorization": "Bearer " + s.cfg.BridgeToken, "Cookie": "owner=1"}, {"Authorization": "Bearer " + s.cfg.BridgeToken, "Origin": "https://foreign.test"}} {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{}`))
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		s.MCPHandler().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatal(w.Code)
		}
	}
	for _, value := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.1", "100.64.0.1"} {
		if publicIP(net.ParseIP(value)) {
			t.Fatal("private address allowed", value)
		}
	}
	if publicURL("file:///etc/passwd") == nil || publicURL("http://user:password@example.com") == nil {
		t.Fatal("invalid URL allowed")
	}
}

func TestDeviceConnectValidatesURLAndCancels(t *testing.T) {
	for _, verificationURL := range []string{"https://auth.openai.com/codex/device", "https://auth.openai.com.attacker.test/device"} {
		t.Run(verificationURL, func(t *testing.T) {
			s, _ := testService(t, testConfig())
			left, right := net.Pipe()
			t.Cleanup(func() { right.Close() })
			s.dial = func(context.Context, Config) (io.ReadWriteCloser, error) { return left, nil }
			go func() {
				reader := bufio.NewReader(right)
				for {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						return
					}
					var req struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(line, &req) != nil {
						return
					}
					var result any
					switch req.Method {
					case "initialized":
						continue
					case "initialize":
						result = map[string]any{"userAgent": "jobseek_dashboard/0.153.4 (Linux; x86_64)", "codexHome": "/runner/state", "platformFamily": "unix", "platformOs": "linux"}
					case "account/read":
						result = map[string]any{"account": nil, "requiresOpenaiAuth": true}
					case "account/login/start":
						result = map[string]any{"type": "chatgptDeviceCode", "loginId": "attempt-a", "verificationUrl": verificationURL, "userCode": "SYNTHETIC"}
					case "account/login/cancel":
						result = map[string]any{}
					default:
						return
					}
					response, _ := json.Marshal(map[string]any{"id": req.ID, "result": result})
					if _, err = right.Write(append(response, '\n')); err != nil {
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			connection, err := s.Connect(ctx)
			if strings.Contains(verificationURL, "attacker") {
				if err == nil || connection.VerificationURL != "" {
					t.Fatal("untrusted login URL exposed")
				}
				return
			}
			if err != nil || connection.LoginID != "attempt-a" {
				t.Fatal(connection, err)
			}
			if status := s.Status(ctx); status.State != "connecting" || status.Connected || status.IngestionAvailable {
				t.Fatal(status)
			}
			if err = s.CancelConnect(ctx); err != nil {
				t.Fatal(err)
			}
			if status := s.Status(ctx); status.State != "needs_sign_in" || status.Connected {
				t.Fatal(status)
			}
		})
	}
}

func TestAssignedPrivateURLBecomesNeedsText(t *testing.T) {
	s, db := testService(t, testConfig())
	session, _ := sdkSession(t, s)
	ctx := context.Background()
	item, _, err := db.SubmitIngestion(ctx, store.Actor{Kind: "administrator", ID: "owner"}, store.IngestionInput{Origin: "owner", SourceURL: "http://127.0.0.1/private", IdempotencyKey: "url"})
	if err != nil {
		t.Fatal(err)
	}
	claim, found, err := db.ClaimNextJob(ctx, "fetch-test", []string{store.IngestionJobKind}, time.Minute, time.Now())
	if err != nil || !found {
		t.Fatal(err)
	}
	capability := strings.Repeat("a", 64)
	s.active = &toolScope{claim: claim, intakeID: item.ID, capability: capability, ctx: ctx}
	if _, err = toolCall(ctx, session, "fetch_vacancy", capabilityArgs{capability}); err != nil {
		t.Fatal(err)
	}
	current, _ := db.Ingestion(ctx, item.ID)
	if current.Status != "needs_text" || current.OriginalText != "" || current.OpportunityID != "" {
		t.Fatal(current)
	}
}

func TestSSHOwnedPipeDrainsAfterReap(t *testing.T) {
	const frame = "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"t\",\"turn\":{\"id\":\"u\",\"status\":\"completed\"}}}\n"
	if os.Getenv("JOBSEEK_SYNTHETIC_PIPE_CHILD") == "1" {
		_, _ = io.WriteString(os.Stdout, frame)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSSHOwnedPipeDrainsAfterReap$")
	cmd.Env = []string{"JOBSEEK_SYNTHETIC_PIPE_CHILD=1"}
	transport, err := startSSHTransport(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	select {
	case <-transport.(*sshTransport).done:
	case <-time.After(4 * time.Second):
		t.Fatal("child did not exit")
	}
	data, err := io.ReadAll(transport)
	if err != nil || string(data) != frame {
		t.Fatalf("final frame lost after Wait: %q %v", data, err)
	}
}
