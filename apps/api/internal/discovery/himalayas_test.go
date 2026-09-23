package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func testRound(t *testing.T, requests int64) (*store.Store, string, string) {
	return testRoundWithDeadline(t, requests, time.Hour)
}

func testRoundWithDeadline(t *testing.T, requests int64, duration time.Duration) (*store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "discovery-agent"}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "discovery", Intent: "Discover relevant employers", Outcome: "discover", ProfileVersion: prefs.Version, Deadline: time.Now().Add(duration), Scope: store.RoundScope{Resources: []string{"discovery:himalayas"}, Operations: []string{store.RoundSearchSource, store.RoundCodexTurn, store.RoundStageDiscovery}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: requests, Items: requests, Tools: requests*2 + 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActivateRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: "discovery:himalayas", Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return db, capability, r.ID
}

func response(text string) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
	return "event: message\ndata: " + string(b) + "\n\n"
}

func TestReadAndReplayAndEvidence(t *testing.T) {
	db, capability, roundID := testRound(t, 2)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var attemptID string
		if err := db.Read(context.Background(), func(rd store.Reader) error {
			return rd.QueryRowContext(context.Background(), `SELECT id FROM round_attempts WHERE round_id=? AND operation='source.search'`, roundID).Scan(&attemptID)
		}); err != nil {
			t.Errorf("missing charged attempt before HTTP: %v", err)
		} else {
			v, err := db.DiscoveryHTTP(context.Background(), attemptID)
			if err != nil || v.ErrorCode != "pending" || !strings.Contains(v.RequestJSON, "architect") {
				t.Errorf("missing durable request before HTTP: %+v %v", v, err)
			}
		}
		if r.Method != "POST" || r.URL.Path != "/mcp" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(response("Found 1 jobs matching 'architect' (showing page 1)\n🚀 **Architect**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/example/jobs/architect")))
	}))
	defer server.Close()
	r := &Reader{Store: db, endpointURL: server.URL + "/mcp"}
	in := Input{RoundID: roundID, RequestKey: "search-1", ResourceID: "discovery:himalayas", Capability: capability, Method: "search_jobs", Keyword: "architect", Page: 1}
	p, err := r.Read(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 1 || p.NextPage != 0 || p.AttemptID == "" {
		t.Fatalf("bad page: %+v", p)
	}
	v, err := db.DiscoveryHTTP(context.Background(), p.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Method != "search_jobs" || v.ResponseSHA256 != p.ResponseSHA256 || !strings.Contains(string(v.ResponseBody), "Architect") {
		t.Fatalf("bad evidence: %+v", v)
	}
	if _, err = r.Read(context.Background(), in); err != nil || calls.Load() != 1 {
		t.Fatalf("replay %v calls=%d", err, calls.Load())
	}
	in.Keyword = "different"
	if _, err = r.Read(context.Background(), in); !errors.Is(err, store.ErrRoundIdempotencyConflict) || calls.Load() != 1 {
		t.Fatalf("changed replay %v calls=%d", err, calls.Load())
	}
}

func TestFailuresRetainRawAndDoNotRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"rate_limit", 429, "slow down", "http_429"},
		{"malformed", 200, "not JSON", "malformed_response"},
		{"oversize", 200, strings.Repeat("x", maxBody+5), "response_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, capability, roundID := testRound(t, 1)
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			r := &Reader{Store: db, endpointURL: server.URL}
			in := Input{RoundID: roundID, RequestKey: "one", ResourceID: "discovery:himalayas", Capability: capability, Method: "get_company_details", CompanySlug: "example"}
			if _, err := r.Read(context.Background(), in); err == nil {
				t.Fatal("wanted failure")
			}
			var attemptID string
			if err := db.Read(context.Background(), func(rd store.Reader) error {
				return rd.QueryRowContext(context.Background(), `SELECT id FROM round_attempts WHERE round_id=? AND operation='source.search'`, roundID).Scan(&attemptID)
			}); err != nil {
				t.Fatal(err)
			}
			v, err := db.DiscoveryHTTP(context.Background(), attemptID)
			if err != nil {
				t.Fatal(err)
			}
			if v.ErrorCode != tc.want || len(v.ResponseBody) > maxBody {
				t.Fatalf("evidence code=%s bytes=%d", v.ErrorCode, len(v.ResponseBody))
			}
			if _, err := r.Read(context.Background(), in); !errors.Is(err, store.ErrUncertain) {
				t.Fatalf("replay: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("HTTP repeated: %d", calls.Load())
			}
		})
	}
}

func TestPageContinuationAndCandidateEvidence(t *testing.T) {
	db, capability, roundID := testRound(t, 1)
	var blocks strings.Builder
	for i := 0; i < 20; i++ {
		blocks.WriteString(fmt.Sprintf("\n🚀 **Role %d**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/example/jobs/role-%d\n", i, i))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(response("Found 21 jobs matching 'architect' (showing page 1)" + blocks.String())))
	}))
	defer server.Close()
	r := &Reader{Store: db, endpointURL: server.URL}
	p, err := r.Read(context.Background(), Input{RoundID: roundID, RequestKey: "page", ResourceID: "discovery:himalayas", Capability: capability, Method: "search_jobs", Keyword: "architect", Page: 1})
	if err != nil || p.NextPage != 2 || p.Omitted != 1 {
		t.Fatalf("page %+v: %v", p, err)
	}
	cursor, err := db.DiscoverySearchContinuation(context.Background(), "architect", "")
	if err != nil || cursor.Page != 1 || cursor.NextPage != 2 || cursor.AttemptID != p.AttemptID {
		t.Fatalf("saved continuation %+v: %v", cursor, err)
	}
	quote := "🚀 **Role 0**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/example/jobs/role-0"
	c := store.DiscoveryCandidate{AttemptID: p.AttemptID, Kind: "job", Title: "Role 0", URL: "https://himalayas.app/companies/example/jobs/role-0", EvidenceQuote: quote}
	stage := store.DiscoveryStageInput{RoundID: roundID, Capability: capability, RequestKey: "candidate-one", Candidate: c}
	first, err := db.StageDiscoveryCandidate(context.Background(), stage)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := db.StageDiscoveryCandidate(context.Background(), stage)
	if err != nil || replayed != first {
		t.Fatalf("candidate replay %+v %v", replayed, err)
	}
	stage.Candidate = store.DiscoveryCandidate{AttemptID: p.AttemptID, Kind: "job", Title: "Role 1", URL: "https://himalayas.app/companies/example/jobs/role-1", EvidenceQuote: "🚀 **Role 1**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/example/jobs/role-1"}
	if _, err := db.StageDiscoveryCandidate(context.Background(), stage); !errors.Is(err, store.ErrRoundIdempotencyConflict) {
		t.Fatalf("changed candidate replay: %v", err)
	}
	stage.RequestKey = "candidate-two"
	stage.Candidate = store.DiscoveryCandidate{AttemptID: p.AttemptID, Kind: "job", Title: "Role 1", URL: "https://himalayas.app/companies/example/jobs/role-1", EvidenceQuote: "🚀 **Role 1**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/example/jobs/role-1"}
	if _, err := db.StageDiscoveryCandidate(context.Background(), stage); !errors.Is(err, store.ErrAllowance) {
		t.Fatalf("exhausted item allowance: %v", err)
	}
	c.URL = "https://himalayas.app/companies/example/jobs/invented"
	stage.Candidate = c
	stage.RequestKey = "invented"
	if _, err := db.StageDiscoveryCandidate(context.Background(), stage); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("invented URL: %v", err)
	}
}

func TestStopFencesResultButKeepsLateHTTP(t *testing.T) {
	db, capability, roundID := testRound(t, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		_, _ = w.Write([]byte(response("Found 0 jobs matching 'architect' (showing page 1)")))
	}))
	defer server.Close()
	r := &Reader{Store: db, endpointURL: server.URL}
	in := Input{RoundID: roundID, RequestKey: "late", ResourceID: "discovery:himalayas", Capability: capability, Method: "search_jobs", Keyword: "architect", Page: 1}
	done := make(chan error, 1)
	go func() { _, err := r.Read(context.Background(), in); done <- err }()
	<-started
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if _, _, err := db.StopRound(context.Background(), owner, roundID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, store.ErrFenced) {
		t.Fatalf("late finish: %v", err)
	}
	var attemptID string
	if err := db.Read(context.Background(), func(rd store.Reader) error {
		return rd.QueryRowContext(context.Background(), `SELECT id FROM round_attempts WHERE round_id=? AND operation='source.search'`, roundID).Scan(&attemptID)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DiscoveryHTTP(context.Background(), attemptID); err != nil {
		t.Fatalf("late HTTP evidence: %v", err)
	}
	if _, err := r.Read(context.Background(), in); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stopped replay: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP repeated after stop: %d", calls.Load())
	}
}

func TestWrongCapabilityNeverSendsHTTP(t *testing.T) {
	db, _, roundID := testRound(t, 1)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }))
	defer server.Close()
	r := &Reader{Store: db, endpointURL: server.URL}
	_, err := r.Read(context.Background(), Input{RoundID: roundID, RequestKey: "wrong", ResourceID: "discovery:himalayas", Capability: "wrong", Method: "search_jobs", Keyword: "architect", Page: 1})
	if !errors.Is(err, store.ErrFenced) || calls.Load() != 0 {
		t.Fatalf("capability %v, calls %d", err, calls.Load())
	}
}

func TestDeadlineExpiresRoundAndRetainsRequest(t *testing.T) {
	db, capability, roundID := testRoundWithDeadline(t, 1, 500*time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(700 * time.Millisecond)
		_, _ = w.Write([]byte(response("Found 0 jobs matching 'architect' (showing page 1)")))
	}))
	defer server.Close()
	r := &Reader{Store: db, endpointURL: server.URL}
	_, err := r.Read(context.Background(), Input{RoundID: roundID, RequestKey: "timeout", ResourceID: "discovery:himalayas", Capability: capability, Method: "search_jobs", Keyword: "architect", Page: 1})
	if !errors.Is(err, store.ErrExpired) {
		t.Fatalf("deadline: %v", err)
	}
	round, err := db.Round(context.Background(), roundID)
	if err != nil {
		t.Fatal(err)
	}
	if round.State == store.RoundRunning {
		t.Fatalf("expired round retained active slot: %s", round.State)
	}
	var attemptID string
	if err := db.Read(context.Background(), func(rd store.Reader) error {
		return rd.QueryRowContext(context.Background(), `SELECT id FROM round_attempts WHERE round_id=? AND operation='source.search'`, roundID).Scan(&attemptID)
	}); err != nil {
		t.Fatal(err)
	}
	v, err := db.DiscoveryHTTP(context.Background(), attemptID)
	if err != nil || !strings.Contains(v.RequestJSON, "architect") || v.ErrorCode == "pending" {
		t.Fatalf("deadline evidence %+v: %v", v, err)
	}
}
