package agency

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/discovery"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// refreshRuntime answers discovery research turns with one fixed job lead and
// delegates verify turns to the direct-save runtime.
type refreshRuntime struct {
	t      *testing.T
	db     *store.Store
	reader *discovery.Reader
	verify *directSaveRuntime
	turns  int
}

func (r *refreshRuntime) CheckRound(context.Context, string) error { return nil }

func (r *refreshRuntime) ExecuteRoundTurn(ctx context.Context, agent store.Actor, roundID string, input codexservice.RoundTurnInput) (store.RoundAttempt, error) {
	r.t.Helper()
	if strings.HasPrefix(input.RequestKey, "verify:") {
		return r.verify.ExecuteRoundTurn(ctx, agent, roundID, input)
	}
	if !strings.HasPrefix(input.RequestKey, "discover:") {
		return store.RoundAttempt{}, fmt.Errorf("unexpected turn %q", input.RequestKey)
	}
	r.turns++
	var evidence struct {
		Keyword string `json:"keyword"`
		Page    int    `json:"page"`
	}
	if err := json.Unmarshal([]byte(input.Evidence), &evidence); err != nil {
		return store.RoundAttempt{}, err
	}
	turn, _, err := r.db.ReserveRoundAttempt(ctx, agent, roundID, store.RoundAttemptInput{RequestKey: input.RequestKey, Operation: store.RoundCodexTurn, ResourceID: input.ResourceID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		return turn, err
	}
	if turn, err = r.db.MarkRoundDispatched(ctx, roundID, turn.ID); err != nil {
		return turn, err
	}
	capability, err := r.db.IssueRoundToolCapability(ctx, roundID, turn.ID, agent.ID)
	if err != nil {
		return turn, err
	}
	search, err := r.reader.Read(ctx, discovery.Input{RoundID: roundID, Capability: capability, RequestKey: "refresh-search", ResourceID: "discovery:himalayas", Method: "search_jobs", Keyword: evidence.Keyword, Page: evidence.Page})
	if err != nil {
		return turn, err
	}
	leadURL := "https://himalayas.app/companies/newco/jobs/platform-lead"
	quote := "🚀 **Platform lead**\n🔗 **Apply on Himalayas:** " + leadURL
	if _, err := r.db.StageDiscoveryCandidate(ctx, store.DiscoveryStageInput{RoundID: roundID, Capability: capability, RequestKey: "stage-refresh",
		Candidate: store.DiscoveryCandidate{AttemptID: search.AttemptID, Kind: "job", Title: "Platform lead", URL: leadURL, EvidenceQuote: quote}}); err != nil {
		return turn, err
	}
	thread := "refresh-research-" + roundID
	if err := r.db.BindRoundThread(ctx, roundID, turn.ID, turn.Generation, thread); err != nil {
		return turn, err
	}
	if err := r.db.BindRoundTurn(ctx, roundID, turn.ID, turn.Generation, thread, "refresh-turn"); err != nil {
		return turn, err
	}
	if err := r.db.ObserveRoundTurn(ctx, roundID, turn.ID, turn.Generation, thread, "refresh-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		return turn, err
	}
	return r.db.FinishRoundAttempt(ctx, agent, roundID, turn.ID, true, json.RawMessage(`{"staged":1}`), "")
}

// TestRepeatedSearchRefreshesAndSkipsSaved proves a new round searches again
// even after an earlier round exhausted the keyword pages, and that a
// re-staged lead already saved as an opportunity is skipped instead of
// saved twice.
func TestRepeatedSearchRefreshesAndSkipsSaved(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	searchText := "Found 1 jobs matching 'Backend and platform work' (showing page 1)\n\n🚀 **Platform lead**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/newco/jobs/platform-lead\n"
	detailText := "# NewCo ✅ Verified\n\n## Links\n🌐 **Website:** https://newco.example/\n"
	reader := &discovery.Reader{Store: db, Client: &http.Client{Transport: collectorTransport(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		text := searchText
		if strings.Contains(string(body), "get_company_details") {
			text = detailText
		}
		protocol, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: message\ndata: " + string(protocol) + "\n\n")), Request: req}, nil
	})}}
	refresh := &refreshRuntime{t: t, db: db, reader: reader, verify: &directSaveRuntime{t: t, db: db, reader: reader}}
	decisions, closeJev := completingDiscoveryDecisions(t)
	defer closeJev()
	decisions.Store = db
	engine := &Engine{Store: db, Runtime: refresh, Decisions: decisions, Context: ctx}
	run := func(key string) store.Round {
		t.Helper()
		round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: key, Intent: "Find sourced platform work", Outcome: "discover", ProfileVersion: profile.Version,
			Scope:  store.RoundScope{Resources: []string{"campaign:active", "discovery:himalayas"}, Operations: []string{store.RoundCodexTurn, store.RoundSearchSource, store.RoundStageDiscovery, store.RoundJevRequest, store.RoundFetchSource, store.RoundCreateCompany, store.RoundCreateOpportunity, store.RoundContextTool}, Delegates: []string{agent.ID}},
			Limits: store.RoundAllowance{Requests: 16, Items: 10, Tools: 18, Turns: 4}, Deadline: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		round, err = db.ActivateRound(ctx, owner, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		engine.run(ctx, round)
		finished, err := db.Round(ctx, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		return finished
	}
	first := run("refresh-first")
	var firstResult struct {
		Code          string `json:"code"`
		Opportunities int    `json:"opportunities"`
	}
	if json.Unmarshal(first.Report, &firstResult) != nil || first.State != store.RoundCompleted || firstResult.Code != "sourced_opportunity_saved" || firstResult.Opportunities != 1 {
		t.Fatalf("first search: state=%s report=%s", first.State, first.Report)
	}
	staged, err := db.RoundDiscoveryCandidates(ctx, first.ID)
	if err != nil || len(staged) != 1 {
		t.Fatalf("first search staged: %+v %v", staged, err)
	}
	read, err := db.DiscoveryHTTP(ctx, staged[0].AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var call struct {
		Params struct {
			Arguments struct {
				Keyword string `json:"keyword"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(read.RequestJSON), &call) != nil || call.Params.Arguments.Keyword != "backend engineer" {
		t.Fatalf("first search keyword: %s", read.RequestJSON)
	}
	second := run("refresh-second")
	var secondResult struct {
		Code                 string `json:"code"`
		Opportunities        int    `json:"opportunities"`
		DiscoveryCandidates  int    `json:"discoveryCandidates"`
		UnreviewedCandidates int    `json:"unreviewedCandidates"`
	}
	if json.Unmarshal(second.Report, &secondResult) != nil || second.State != store.RoundCompleted || secondResult.Code != "discovery_search_no_new_leads" {
		t.Fatalf("second search: state=%s report=%s", second.State, second.Report)
	}
	if secondResult.Opportunities != 0 || secondResult.DiscoveryCandidates != 1 {
		t.Fatalf("second search counts: %+v", secondResult)
	}
	if second.Used.Turns != 1 {
		t.Fatalf("second search turns (research only, no verify): %+v", second.Used)
	}
	_, searches, err := db.RoundCandidateLeads(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	searched := false
	for _, read := range searches {
		if read.Method == "search_jobs" && read.Succeeded {
			searched = true
		}
	}
	if !searched {
		t.Fatalf("second search recorded no fresh job search: %+v", searches)
	}
	cards, err := db.RoundCards(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Fatalf("second search saved duplicates: %+v", cards)
	}
	leads, _, err := db.RoundCandidateLeads(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(leads) != 1 || leads[0].Status != "saved" {
		t.Fatalf("re-staged lead status: %+v", leads)
	}
}
