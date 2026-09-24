package agency

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/discovery"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type directSaveRuntime struct {
	t      *testing.T
	db     *store.Store
	reader *discovery.Reader
	turns  atomic.Int32
}

func (r *directSaveRuntime) CheckRound(context.Context, string) error { return nil }

func (r *directSaveRuntime) ExecuteRoundTurn(ctx context.Context, agent store.Actor, roundID string, input codexservice.RoundTurnInput) (store.RoundAttempt, error) {
	r.t.Helper()
	if !strings.HasPrefix(input.RequestKey, "verify:") {
		return store.RoundAttempt{}, fmt.Errorf("unexpected turn %q", input.RequestKey)
	}
	r.turns.Add(1)
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
	var evidence struct {
		Candidate struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			URL           string `json:"url"`
			EvidenceQuote string `json:"evidenceQuote"`
		} `json:"candidate"`
	}
	if err := json.Unmarshal([]byte(input.Evidence), &evidence); err != nil {
		return turn, err
	}
	detail, err := r.reader.Read(ctx, discovery.Input{RoundID: roundID, Capability: capability, RequestKey: "direct-detail", ResourceID: "discovery:himalayas", Method: "get_company_details", CompanySlug: "two"})
	if err != nil {
		return turn, err
	}
	proofCost, _ := store.RoundOperationCost(store.RoundFetchSource)
	proofAttempt, _, err := r.db.ReserveRoundAttempt(ctx, agent, roundID, store.RoundAttemptInput{RequestKey: "direct-official", Operation: store.RoundFetchSource, ResourceID: "discovery:himalayas", Cost: proofCost, BoundCapability: capability})
	if err != nil {
		return turn, err
	}
	if _, err = r.db.MarkRoundDispatched(ctx, roundID, proofAttempt.ID); err != nil {
		return turn, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	proofURL := "https://newco.example/"
	if err := r.db.PrepareDiscoveryOfficialRead(ctx, store.DiscoveryOfficialRead{AttemptID: proofAttempt.ID, RoundID: roundID, CandidateID: evidence.Candidate.ID, CompanyDetailAttemptID: detail.AttemptID, SourceURL: proofURL, ClaimURL: proofURL, ObservedAt: now}); err != nil {
		return turn, err
	}
	proof := codexservice.DiscoveryOfficialSnapshot{AttemptID: proofAttempt.ID, CandidateID: evidence.Candidate.ID, CompanyDetailAttemptID: detail.AttemptID, ClaimURL: proofURL, SourceURL: proofURL, Status: "ok",
		Evidence: codexservice.SourceLinksSnapshot{SourceURL: proofURL, Status: "ok", Links: []codexservice.SourceLink{{URL: "https://newco.example/careers", Text: "Careers"}}}}
	proofJSON, _ := json.Marshal(proof)
	if err := r.db.CompleteDiscoveryOfficialRead(ctx, proofAttempt.ID, "ok", proofJSON, now); err != nil {
		return turn, err
	}
	if _, err := r.db.FinishRoundAttempt(ctx, agent, roundID, proofAttempt.ID, true, proofJSON, ""); err != nil {
		return turn, err
	}
	profile, err := r.db.CurrentPreferences(ctx)
	if err != nil {
		return turn, err
	}
	company, _, err := r.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "company:direct", Operation: store.RoundCreateCompany, ResourceID: "campaign:active", ExpectedRevision: profile.Version, Capability: capability,
		Company: &store.CompanyInput{Name: "NewCo", Website: proofURL}})
	if err != nil {
		return turn, err
	}
	if _, _, err = r.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "save:direct", Operation: store.RoundCreateOpportunity, ResourceID: "company:" + company.EntityID, ExpectedRevision: company.Revision, Capability: capability,
		Opportunity: &store.OpportunityInput{CompanyID: company.EntityID, Title: evidence.Candidate.Title, Kind: "employment", Stage: "new", SourceURL: evidence.Candidate.URL, OriginalText: evidence.Candidate.EvidenceQuote}}); err != nil {
		return turn, err
	}
	if err := r.db.BindRoundThread(ctx, roundID, turn.ID, turn.Generation, "direct-verify-thread"); err != nil {
		return turn, err
	}
	if err := r.db.BindRoundTurn(ctx, roundID, turn.ID, turn.Generation, "direct-verify-thread", "direct-verify-turn"); err != nil {
		return turn, err
	}
	if err := r.db.ObserveRoundTurn(ctx, roundID, turn.ID, turn.Generation, "direct-verify-thread", "direct-verify-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		return turn, err
	}
	return r.db.FinishRoundAttempt(ctx, agent, roundID, turn.ID, true, json.RawMessage(`{"saved":true}`), "")
}

func TestDirectSavedLeadCompletesWithoutBoard(t *testing.T) {
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
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "direct-save-lead", Intent: "Find sourced platform work", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active", "discovery:himalayas"}, Operations: []string{store.RoundCodexTurn, store.RoundSearchSource, store.RoundStageDiscovery, store.RoundJevRequest, store.RoundFetchSource, store.RoundCreateCompany, store.RoundCreateOpportunity, store.RoundContextTool}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 16, Items: 10, Tools: 18, Turns: 4}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	researchTurn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "direct-lead-search", Operation: store.RoundCodexTurn, ResourceID: "discovery:himalayas", Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, round.ID, researchTurn.ID); err != nil {
		t.Fatal(err)
	}
	researchCap, err := db.IssueRoundToolCapability(ctx, round.ID, researchTurn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var searchText strings.Builder
	searchText.WriteString("Found 2 jobs matching 'platform' (showing page 1)\n")
	for _, slug := range []string{"one", "two"} {
		fmt.Fprintf(&searchText, "🚀 **Platform %s**\n🔗 **Apply on Himalayas:** https://himalayas.app/companies/%s/jobs/platform-%s\n", slug, slug, slug)
	}
	detailText := "# NewCo ✅ Verified\n\n## Links\n🌐 **Website:** https://newco.example/\n"
	reader := &discovery.Reader{Store: db, Client: &http.Client{Transport: collectorTransport(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		text := searchText.String()
		if strings.Contains(string(body), "get_company_details") {
			text = detailText
		}
		protocol, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: message\ndata: " + string(protocol) + "\n\n")), Request: req}, nil
	})}}
	search, err := reader.Read(ctx, discovery.Input{RoundID: round.ID, Capability: researchCap, RequestKey: "direct-search", ResourceID: "discovery:himalayas", Method: "search_jobs", Keyword: "platform", Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"one", "two"} {
		url := fmt.Sprintf("https://himalayas.app/companies/%s/jobs/platform-%s", slug, slug)
		quote := fmt.Sprintf("🚀 **Platform %s**\n🔗 **Apply on Himalayas:** %s", slug, url)
		if _, err := db.StageDiscoveryCandidate(ctx, store.DiscoveryStageInput{RoundID: round.ID, Capability: researchCap, RequestKey: "stage-" + slug,
			Candidate: store.DiscoveryCandidate{AttemptID: search.AttemptID, Kind: "job", Title: "Platform " + slug, URL: url, EvidenceQuote: quote}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.BindRoundThread(ctx, round.ID, researchTurn.ID, researchTurn.Generation, "direct-research-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, round.ID, researchTurn.ID, researchTurn.Generation, "direct-research-thread", "direct-research-turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.ObserveRoundTurn(ctx, round.ID, researchTurn.ID, researchTurn.Generation, "direct-research-thread", "direct-research-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, agent, round.ID, researchTurn.ID, true, json.RawMessage(`{"staged":2}`), ""); err != nil {
		t.Fatal(err)
	}
	round, err = db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	cursor := agencyCursor{Research: &discoveryResearchPin{Criterion: store.RoleCriterion{ID: "role-platform", Label: "platform"}, Keyword: "platform", SearchID: "research-platform", Page: 1, TurnKey: researchTurn.RequestKey}}
	encoded, _ := json.Marshal(cursor)
	round, err = db.SaveRoundProgress(ctx, owner, round.ID, round.Revision, store.RoundProgress{Step: "discovery_research_selected", Cursor: encoded, Unresolved: round.Unresolved, Report: round.Report})
	if err != nil {
		t.Fatal(err)
	}
	decisions, closeJev := completingDiscoveryDecisions(t)
	defer closeJev()
	decisions.Store = db
	engine := &Engine{Store: db, Runtime: &directSaveRuntime{t: t, db: db, reader: reader}, Decisions: decisions, Context: ctx}
	engine.run(ctx, round)
	finished, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Code                 string `json:"code"`
		Opportunities        int    `json:"opportunities"`
		DiscoveryCandidates  int    `json:"discoveryCandidates"`
		UnreviewedCandidates int    `json:"unreviewedCandidates"`
	}
	if json.Unmarshal(finished.Report, &result) != nil || finished.State != store.RoundCompleted || result.Code != "sourced_opportunity_saved" ||
		result.Opportunities != 1 || result.DiscoveryCandidates != 2 || result.UnreviewedCandidates != 1 {
		t.Fatalf("direct save incomplete: state=%s report=%s used=%+v", finished.State, finished.Report, finished.Used)
	}
	if finished.Used.Turns != 2 {
		t.Fatalf("direct save turns: %+v", finished.Used)
	}
	cards, err := db.RoundCards(ctx, round.ID)
	if err != nil || len(cards) != 1 || !strings.Contains(cards[0].SourceURL, "platform-two") {
		t.Fatalf("direct save cards: %+v %v", cards, err)
	}
	leads, _, err := db.RoundCandidateLeads(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := 0
	for _, lead := range leads {
		if lead.Status == "saved" {
			saved++
		}
	}
	if saved != 1 {
		t.Fatalf("direct save lead statuses: %+v", leads)
	}
}

func TestDiscoveryVerifyBriefNamesExactTools(t *testing.T) {
	brief := discoveryVerifyBrief()
	for _, token := range []string{"get_company_details", "discovery_official_links", "discovery_board_register", "company.create", "opportunity.create"} {
		if !strings.Contains(brief, token) {
			t.Fatalf("verify brief misses %q: %s", token, brief)
		}
	}
}
