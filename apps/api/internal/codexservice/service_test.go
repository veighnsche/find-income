package codexservice

import (
	"bufio"
	"context"
	"encoding/json"
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
	return Config{Host: "isolated.test", User: "runner", IdentityFile: "/key", KnownHostsFile: "/known", Launcher: "/runner/launch", IsolationVerified: true, BridgeToken: strings.Repeat("s", 64), Model: "test-model", Effort: "medium"}
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
func TestRuntimeDiscoversOnlyRoundTools(t *testing.T) {
	s, _ := testService(t, testConfig())
	session, _ := sdkSession(t, s)
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("unexpected tool count: %d", len(result.Tools))
	}
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	if !names["round_context"] || !names["round_mutation"] {
		t.Fatal("required round tools not discoverable")
	}
}

func TestBridgeRejectsBrowserAndWrongBearer(t *testing.T) {
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
					case "account/login/cancel", "account/logout":
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
