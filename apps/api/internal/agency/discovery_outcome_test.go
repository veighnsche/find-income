package agency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/collector"
	"github.com/veighnsche/find-income-dashboard/api/internal/discovery"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type interruptDiscoveryDecision struct {
	base    jevservice.Service
	stop    func(context.Context, string)
	trigger func(jev.DecisionInput) bool
	calls   atomic.Int32
	stopped atomic.Bool
}

func (d *interruptDiscoveryDecision) RunDecision(ctx context.Context, binding jevservice.Binding, input jev.DecisionInput) (jev.DecisionResult, error) {
	if d.trigger(input) {
		d.calls.Add(1)
	}
	result, err := d.base.RunDecision(ctx, binding, input)
	if err == nil && d.trigger(input) && d.stopped.CompareAndSwap(false, true) {
		d.stop(ctx, binding.RoundID)
	}
	return result, err
}
func (d *interruptDiscoveryDecision) RunScreening(ctx context.Context, binding jevservice.Binding, input jev.ScreeningInput) (jev.ScreeningResult, error) {
	return d.base.RunScreening(ctx, binding, input)
}
func (d *interruptDiscoveryDecision) RunOrganisation(ctx context.Context, binding jevservice.Binding, input jev.OrganisationInput) (jev.OrganisationResult, error) {
	return d.base.RunOrganisation(ctx, binding, input)
}

func completingDiscoveryDecisions(t *testing.T) (jevservice.Service, func()) {
	t.Helper()
	var sourceChoices atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			State struct {
				Kind       string `json:"kind"`
				Candidates []struct {
					ID           string   `json:"id"`
					CapabilityID string   `json:"capability_id"`
					SourceIDs    []string `json:"source_ids"`
				} `json:"candidates"`
				Sources []struct {
					ID  string `json:"id"`
					URL string `json:"url"`
				} `json:"sources"`
			} `json:"state"`
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Errorf("Jev request: %v", err)
			return
		}
		answers := map[string]any{}
		for id, question := range wire.Questions {
			selected := ""
			switch {
			case id == "selected_candidate" && wire.State.Kind == string(jev.DecisionNextOutcome) && len(wire.State.Candidates) > 0 && strings.HasPrefix(wire.State.Candidates[0].CapabilityID, "home_"):
				selected = wire.State.Candidates[0].ID
			case id == "selected_candidate" && wire.State.Kind == string(jev.DecisionNextOutcome):
				selected = "finish"
				for _, candidate := range wire.State.Candidates {
					for _, sourceID := range candidate.SourceIDs {
						for _, source := range wire.State.Sources {
							if source.ID == sourceID && strings.Contains(source.URL, "role-one") {
								selected = candidate.ID
							}
						}
					}
				}
			case id == "selected_candidate" && len(wire.State.Candidates) > 0 && wire.State.Candidates[0].CapabilityID == "source_extract":
				index := int(sourceChoices.Add(1))
				selected = wire.State.Candidates[0].ID
				if index == 1 && len(wire.State.Candidates) > 1 {
					for _, candidate := range wire.State.Candidates {
						for _, sourceID := range candidate.SourceIDs {
							for _, source := range wire.State.Sources {
								if source.ID == sourceID && strings.Contains(source.URL, "role-two") {
									selected = candidate.ID
								}
							}
						}
					}
				}
			case id == "selected_candidate" && len(wire.State.Candidates) > 1 && wire.State.Candidates[0].CapabilityID == "official_verify":
				selected = wire.State.Candidates[0].ID
				for _, candidate := range wire.State.Candidates {
					for _, sourceID := range candidate.SourceIDs {
						for _, source := range wire.State.Sources {
							if source.ID == sourceID && strings.Contains(source.URL, "platform-two") {
								selected = candidate.ID
							}
						}
					}
				}
			case id == "selected_candidate":
				selected = wire.State.Candidates[0].ID
			case strings.HasPrefix(id, "scope_"):
				selected = jev.ScopePresent
			case strings.HasPrefix(id, "support_"):
				selected = "span_0"
			case id == "organisation_category":
				for key := range question.Criteria {
					if key != "__uncertain__" {
						selected = key
						break
					}
				}
			}
			probabilities := map[string]float64{}
			for key := range question.Criteria {
				probabilities[key] = 0
			}
			if _, ok := probabilities[selected]; !ok {
				t.Errorf("Jev selected unavailable %q for %s from %+v", selected, id, question.Criteria)
				return
			}
			probabilities[selected] = 1
			answers[id] = map[string]any{"type": "choice", "choice": selected, "probabilities": probabilities, "confidence": 0.8}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": answers, "usage": map[string]any{"input_tokens": 5, "output_tokens": 1}})
	}))
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-key")
	config := jev.DefaultConfig()
	config.Enabled, config.Endpoint, config.Timeout, config.MaxAttempts = true, server.URL+"/v1/systemone", time.Second, 1
	client, err := jev.NewFromEnvironment(config, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return jevservice.Service{Client: client}, server.Close
}

func TestOneDiscoveryCommissionAssessesTwoSavedPostingsWithJevChoosingSecondFirst(t *testing.T) {
	runTwoRoleDiscovery(t, false)
}

func TestNextOutcomeDecisionSurvivesStopBeforeSourcePin(t *testing.T) {
	runTwoRoleDiscovery(t, true)
}

func TestAssessmentSummaryDisclosesLateOmittedObservation(t *testing.T) {
	observations := make([]jev.ScreeningObservation, 16)
	for i := range observations {
		observations[i] = jev.ScreeningObservation{CriterionID: fmt.Sprintf("criterion-%02d-%s", i, strings.Repeat("x", 34)), Scope: jev.ScopePresent, SupportState: jev.SupportProposed}
	}
	observations[len(observations)-1].Scope = jev.ScopeConflicting
	screenJSON, _ := json.Marshal(jev.ScreeningResult{Observations: observations})
	organisationJSON, _ := json.Marshal(jev.OrganisationResult{CategoryID: "platform"})
	screen := store.RoundJevAssessment{Status: "proposed", ResultJSON: screenJSON}
	organisation := store.RoundJevAssessment{Status: "selected", ResultJSON: organisationJSON}
	summary, omitted := assessedEvidenceSummary(screen, organisation, 0)
	if omitted == 0 || len(summary) > 430 || !strings.Contains(summary, fmt.Sprintf("assessmentObservationsOmitted=%d", omitted)) || strings.Contains(summary, observations[len(observations)-1].CriterionID) {
		t.Fatalf("late conflicting observation omitted without disclosure: %q omitted=%d", summary, omitted)
	}
	shortJSON, _ := json.Marshal(jev.ScreeningResult{Observations: observations[len(observations)-1:]})
	summary, omitted = assessedEvidenceSummary(store.RoundJevAssessment{Status: "proposed", ResultJSON: shortJSON}, organisation, 0)
	if omitted != 0 || !strings.Contains(summary, jev.ScopeConflicting) || !strings.Contains(summary, "assessmentObservationsOmitted=0") {
		t.Fatalf("bounded conflicting observation lost: %q omitted=%d", summary, omitted)
	}
}

func TestOversizedDecisionContextStopsBeforeProviderReservation(t *testing.T) {
	input := jev.DecisionInput{Kind: jev.DecisionSourceResearch, CampaignIntent: "Find sourced platform work", MaxReportedTokens: discoveryDecisionReportedTokenLimit,
		Capabilities:       []jev.DecisionCapability{{ID: "research", Description: "Inspect one saved source."}},
		RemainingAllowance: []jev.DecisionAllowance{{Operation: store.RoundJevRequest, Remaining: 1}},
		Candidates:         []jev.DecisionCandidate{{ID: "choose", Description: "Inspect an exact saved source.", Scope: "One bounded source.", CapabilityID: "research", SourceIDs: []string{"source-0"}}}}
	for i := 0; i < 12; i++ {
		input.Sources = append(input.Sources, jev.DecisionSource{ID: fmt.Sprintf("source-%d", i), SourceRevision: "revision", SourceKind: "saved", Excerpt: strings.Repeat("A", 1900)})
	}
	logical, err := jev.DecisionLogicalRequest(input)
	if err != nil || len(logical) <= discoveryDecisionLogicalByteLimit {
		t.Fatalf("fixture did not exceed logical bound: %d %v", len(logical), err)
	}
	engine := &Engine{} // A provider or store touch would fail this before-call check.
	_, err = engine.savedOrRunDecision(context.Background(), jevservice.Binding{RoundID: "unused", RequestKeyPrefix: "oversized"}, input)
	if !errors.Is(err, errDecisionContextTooLarge) || terminalCode(err) != "decision_context_too_large" {
		t.Fatalf("oversized context was not stopped explicitly: %v", err)
	}
}

func TestDefaultBatchFullExcerptDecisionContextFits(t *testing.T) {
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
	profileSources, profileIDs, complete := profileDecisionSources(profile, 8)
	if !complete {
		t.Fatal("default profile did not fit supplied source context")
	}
	intent := "Find substantive platform engineering work in Brussels with remote or hybrid options."
	postingText := prefixUTF8(strings.Repeat("Build Go services in Brussels with PostgreSQL, cloud deployments, team ownership, and production incident response. ", 30), 1900)
	input := jev.DecisionInput{Kind: jev.DecisionSourceResearch, CampaignIntent: intent, MaxReportedTokens: discoveryDecisionReportedTokenLimit,
		Capabilities: []jev.DecisionCapability{{ID: "source_extract", Description: "Extract one current exact vacancy into a source-linked opportunity."}},
		Sources:      profileSources, RemainingAllowance: []jev.DecisionAllowance{{Operation: store.RoundCodexTurn, Remaining: 4}}}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("%032x", i+1)
		url := fmt.Sprintf("https://jobs.lever.co/example/role-%d", i+1)
		input.Sources = append(input.Sources, jev.DecisionSource{ID: id, SourceRevision: strings.Repeat("a", 64), SourceKind: "lever_posting", URL: url,
			ObservedAt: "2026-09-23T10:00:00Z", Excerpt: postingText})
		input.Candidates = append(input.Candidates, jev.DecisionCandidate{ID: id, Description: "Review this current saved vacancy source.",
			Scope: "One source-linked opportunity extraction; no external application.", CapabilityID: "source_extract", SourceIDs: append([]string{id}, profileIDs...)})
	}
	sourceChoice, err := jev.DecisionLogicalRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	nextProfile, nextIDs, complete := profileDecisionSources(profile, 4)
	if !complete {
		t.Fatal("default profile did not fit next-outcome context")
	}
	next := jev.DecisionInput{Kind: jev.DecisionNextOutcome, CampaignIntent: intent, MaxReportedTokens: discoveryDecisionReportedTokenLimit,
		Capabilities: []jev.DecisionCapability{{ID: "assess_exact_source", Description: "Assess one exact current unassessed posting already saved in this bounded source batch."},
			{ID: "finish_round", Description: "Finish this commission with the sourced opportunities and assessments already recorded."}},
		Sources: nextProfile, PreviousOutcomes: []jev.PreviousDecisionOutcome{{ID: "prior-role", Description: strings.Repeat("x", 430)}},
		RemainingAllowance: []jev.DecisionAllowance{{Operation: store.RoundCodexTurn, Remaining: 3}, {Operation: store.RoundJevRequest, Remaining: 12}, {Operation: store.RoundSaveSourceOpportunity, Remaining: 6}, {Operation: store.RoundContextTool, Remaining: 15}}}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("%032x", i+1)
		url := fmt.Sprintf("https://jobs.lever.co/example/role-%d", i+1)
		next.Sources = append(next.Sources, jev.DecisionSource{ID: id, SourceRevision: strings.Repeat("a", 64), SourceKind: "lever_posting", URL: url,
			ObservedAt: "2026-09-23T10:00:00Z", Excerpt: postingText})
		if i == 0 {
			next.Candidates = append(next.Candidates, jev.DecisionCandidate{ID: "finish", Description: "Finish with assessed sourced work and retain other saved postings.", Scope: "No further source or provider work in this round; all unassessed postings remain durable.", CapabilityID: "finish_round", SourceIDs: append([]string{id}, nextIDs...)})
		} else {
			next.Candidates = append(next.Candidates, jev.DecisionCandidate{ID: id, Description: "Assess this exact retained current posting from " + url,
				Scope:        "Use the complete saved source for one source linked opportunity extraction, screening and organisation; the decision sees a bounded excerpt and may abstain if insufficient.",
				CapabilityID: "assess_exact_source", SourceIDs: append([]string{id}, nextIDs...)})
		}
	}
	nextChoice, err := jev.DecisionLogicalRequest(next)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("default full-excerpt logical requests: source choice=%d next outcome=%d bytes", len(sourceChoice), len(nextChoice))
	if len(sourceChoice) > discoveryDecisionLogicalByteLimit || len(nextChoice) > discoveryDecisionLogicalByteLimit {
		t.Fatalf("full bounded batch exceeds preflight: source=%d next=%d bound=%d", len(sourceChoice), len(nextChoice), discoveryDecisionLogicalByteLimit)
	}
}

func runTwoRoleDiscovery(t *testing.T, interruptAfterNextChoice bool) {
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
	boards, err := db.ListCollectorBoards(ctx)
	if err != nil || len(boards) == 0 {
		t.Fatalf("boards: %v", err)
	}
	board := boards[0]
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: board.DisplayName, Website: board.OfficialCareersURL})
	if err != nil {
		t.Fatal(err)
	}
	host := "jobs.lever.co"
	if board.Region == "eu" {
		host = "jobs.eu.lever.co"
	}
	posting := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"text":%q,"descriptionPlain":%q,"hostedUrl":%q}`, id, "Engineer "+id,
			"Build Go services for "+id+" in Brussels.", "https://"+host+"/"+board.Site+"/"+id)
	}
	page := []byte("[" + strings.Join([]string{posting("role-one"), posting("role-two"), posting("role-three"), posting("role-four"), posting("role-five")}, ",") + "]")
	var calls atomic.Int32
	collectorHTTP := &http.Client{Transport: collectorTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(page)), Request: req}, nil
	})}
	decisions, closeJev := completingDiscoveryDecisions(t)
	defer closeJev()
	decisions.Store = db
	runtime := &savingRuntime{db: db}
	var decisionRunner Decisions = decisions
	var interrupted *interruptDiscoveryDecision
	var service *rounds.Service
	stopped := make(chan error, 1)
	if interruptAfterNextChoice {
		interrupted = &interruptDiscoveryDecision{base: decisions, trigger: func(input jev.DecisionInput) bool {
			return input.Kind == jev.DecisionNextOutcome && len(input.Candidates) > 0 && input.Candidates[0].CapabilityID == "finish_round"
		}}
		interrupted.stop = func(_ context.Context, id string) {
			_, stopErr := service.Stop(context.Background(), owner, id)
			stopped <- stopErr
		}
		decisionRunner = interrupted
	}
	engine := &Engine{Store: db, Runtime: runtime, Decisions: decisionRunner, Collector: &collector.Collector{HTTPClient: collectorHTTP}, Context: ctx}
	service = &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	round, _, err := service.Start(ctx, owner, store.StartRoundInput{RequestKey: "two-role-discovery", Intent: "Assess sourced engineering work", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active", "board:" + board.ID, "company:" + company.ID}, Operations: []string{store.RoundJevRequest, store.RoundCollectorPage, store.RoundCodexTurn, store.RoundSaveSourceOpportunity}, Delegates: []string{"codex-runner"}},
		Limits: store.RoundAllowance{Requests: 16, Items: 10, Tools: 18, Turns: 4}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if interruptAfterNextChoice {
		select {
		case stopErr := <-stopped:
			if stopErr != nil {
				t.Fatal(stopErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("next outcome did not stop at the decision boundary")
		}
		paused, readErr := db.Round(ctx, round.ID)
		if readErr != nil || paused.State != store.RoundPaused {
			t.Fatalf("next choice did not pause: %+v %v", paused, readErr)
		}
		var cursor agencyCursor
		if json.Unmarshal(paused.Cursor, &cursor) != nil || cursor.Selected == nil || cursor.Phase != 0 {
			t.Fatalf("source pin advanced before Stop: %s", paused.Cursor)
		}
		choice, readErr := db.RoundAttemptForRequest(ctx, round.ID, "next-outcome:p0/0")
		if readErr != nil || choice.State != store.AttemptSucceeded {
			t.Fatalf("decision response was not retained: %+v %v", choice, readErr)
		}
		if _, err := service.Resume(ctx, owner, round.ID); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		round, err = db.Round(ctx, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if round.State == store.RoundCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var report struct {
		Code                 string           `json:"code"`
		AssessedSources      []assessedSource `json:"assessedSources"`
		Opportunities        int              `json:"opportunities"`
		CollectorAttemptID   string           `json:"collectorAttemptId"`
		CollectorHasMore     bool             `json:"collectorHasMore"`
		UnreviewedCandidates int              `json:"unreviewedCandidates"`
	}
	if json.Unmarshal(round.Report, &report) != nil || round.State != store.RoundCompleted || report.Code != "sourced_opportunity_assessed" ||
		len(report.AssessedSources) != 2 || report.Opportunities != 2 || calls.Load() != 1 || runtime.turns.Load() != 2 {
		t.Fatalf("two-role result: state=%s report=%s used=%+v provider=%d turns=%d", round.State, round.Report, round.Used, calls.Load(), runtime.turns.Load())
	}
	if round.Used != (store.RoundAllowance{Requests: 13, Items: 6, Tools: 5, Turns: 2}) {
		t.Fatalf("default discovery allowance changed: used=%+v report=%s", round.Used, round.Report)
	}
	if verdict := ReadHomeRecommendationCurrentness(ctx, db, round); verdict.Status != "current" {
		t.Fatalf("completed sourced result has stale saved advice: %+v report=%s", verdict, round.Report)
	}
	batchJSON, err := db.RoundCollectorBatch(ctx, report.CollectorAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var retained collector.Batch
	if json.Unmarshal(batchJSON, &retained) != nil || !report.CollectorHasMore || report.UnreviewedCandidates != 2 || retained.Next == nil || len(retained.Next.Pending) != 1 ||
		!bytes.Equal(retained.Next.Pending[0].Raw, []byte(posting("role-five"))) {
		t.Fatalf("buffered posting lost after bounded multi-role round: report=%s batch=%+v", round.Report, retained)
	}
	first, err := db.Ingestion(ctx, report.AssessedSources[0].IngestionID)
	if err != nil || !strings.Contains(first.OriginalText, "role-two") {
		t.Fatalf("Jev did not choose non-first source: %+v %v", first, err)
	}
	if interruptAfterNextChoice && interrupted.calls.Load() != 1 {
		t.Fatalf("next outcome repeated semantic decision: %d", interrupted.calls.Load())
	}
}

func TestNeutralPublicCandidatesRequireJevSelectionBeforeOfficialVerification(t *testing.T) {
	runNeutralPublicCandidates(t, false)
}

func TestReconciledNeutralSearchContinuesFromSavedCandidates(t *testing.T) {
	runNeutralPublicCandidates(t, true)
}

func runNeutralPublicCandidates(t *testing.T, observedSuccess bool) {
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
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "neutral-leads", Intent: "Research sourced platform work", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active", "discovery:himalayas"}, Operations: []string{store.RoundCodexTurn, store.RoundSearchSource, store.RoundStageDiscovery, store.RoundJevRequest, store.RoundFetchSource, store.RoundRegisterDiscoveryBoard}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 16, Items: 10, Tools: 18, Turns: 4}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "neutral-stage", Operation: store.RoundCodexTurn, ResourceID: "discovery:himalayas", Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstURL := "https://himalayas.app/companies/one/jobs/platform-one"
	secondURL := "https://himalayas.app/companies/two/jobs/platform-two"
	firstQuote := "🚀 **Platform One**\n🔗 **Apply on Himalayas:** " + firstURL
	secondQuote := "🚀 **Platform Two**\n🔗 **Apply on Himalayas:** " + secondURL
	text := "Found 2 jobs matching 'platform' (showing page 1)\n" + firstQuote + "\n" + secondQuote + "\n"
	protocol, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
	reader := &discovery.Reader{Store: db, Client: &http.Client{Transport: collectorTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: message\ndata: " + string(protocol) + "\n\n")), Request: req}, nil
	})}}
	search, err := reader.Read(ctx, discovery.Input{RoundID: round.ID, Capability: capability, RequestKey: "neutral-search", ResourceID: "discovery:himalayas", Method: "search_jobs", Keyword: "platform", Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.StageDiscoveryCandidate(ctx, store.DiscoveryStageInput{RoundID: round.ID, Capability: capability, RequestKey: "stage-one", Candidate: store.DiscoveryCandidate{AttemptID: search.AttemptID, Kind: "job", Title: "Platform One", URL: firstURL, EvidenceQuote: firstQuote}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.StageDiscoveryCandidate(ctx, store.DiscoveryStageInput{RoundID: round.ID, Capability: capability, RequestKey: "stage-two", Candidate: store.DiscoveryCandidate{AttemptID: search.AttemptID, Kind: "job", Title: "Platform Two", URL: secondURL, EvidenceQuote: secondQuote}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundThread(ctx, round.ID, turn.ID, turn.Generation, "neutral-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "neutral-thread", "neutral-turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.ObserveRoundTurn(ctx, round.ID, turn.ID, turn.Generation, "neutral-thread", "neutral-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if !observedSuccess {
		if _, err := db.FinishRoundAttempt(ctx, agent, round.ID, turn.ID, true, json.RawMessage(`{"staged":2}`), ""); err != nil {
			t.Fatal(err)
		}
	}
	round, err = db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	research := agencyCursor{Research: &discoveryResearchPin{Criterion: store.RoleCriterion{ID: "role-platform", Label: "platform"}, SearchID: "search-0", Page: 1, TurnKey: "neutral-stage"}}
	encodedResearch, _ := json.Marshal(research)
	round, err = db.SaveRoundProgress(ctx, owner, round.ID, round.Revision, store.RoundProgress{Step: "discovery_research_selected", Cursor: encodedResearch, Unresolved: round.Unresolved, Report: round.Report})
	if err != nil {
		t.Fatal(err)
	}
	if observedSuccess {
		if _, _, err = db.StopRound(ctx, owner, round.ID); err != nil {
			t.Fatal(err)
		}
		paused, pauseErr := db.PauseStoppedRound(ctx, owner, round.ID)
		if pauseErr != nil {
			t.Fatal(pauseErr)
		}
		check, checkErr := db.BeginRoundReconciliation(ctx, round.ID, turn.ID, paused.Generation)
		if checkErr != nil {
			t.Fatal(checkErr)
		}
		if _, err = db.FinishRoundReconciliation(ctx, check, store.AttemptObservedSuccess, json.RawMessage(`{"status":"completed"}`)); err != nil {
			t.Fatal(err)
		}
		if round, err = db.ResumeRound(ctx, owner, round.ID, paused.Generation); err != nil {
			t.Fatal(err)
		}
	}
	decisions, closeJev := completingDiscoveryDecisions(t)
	defer closeJev()
	decisions.Store = db
	interrupted := &interruptDiscoveryDecision{base: decisions, trigger: func(input jev.DecisionInput) bool {
		return len(input.Candidates) > 0 && input.Candidates[0].CapabilityID == "official_verify"
	}}
	interrupted.stop = func(_ context.Context, id string) {
		if _, _, stopErr := db.StopRound(context.Background(), owner, id); stopErr != nil {
			t.Errorf("stop after neutral choice: %v", stopErr)
			return
		}
		if _, pauseErr := db.PauseStoppedRound(context.Background(), owner, id); pauseErr != nil {
			t.Errorf("pause after neutral choice: %v", pauseErr)
		}
	}
	runtime := &inertRuntime{}
	engine := &Engine{Store: db, Runtime: runtime, Decisions: interrupted}
	round, err = db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, cursor, count, err := engine.continueDiscoveryResearch(ctx, round, research)
	if err != store.ErrFenced || cursor.SelectedDiscovery != nil || count != 2 || interrupted.calls.Load() != 1 {
		t.Fatalf("neutral choice did not stop before pin: count=%d cursor=%+v calls=%d err=%v", count, cursor, interrupted.calls.Load(), err)
	}
	paused, err := db.Round(ctx, round.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("neutral choice not paused: %+v %v", paused, err)
	}
	round, err = db.ResumeRound(ctx, owner, round.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	round, cursor, count, err = engine.continueDiscoveryResearch(ctx, round, research)
	if err != nil || count != 2 || cursor.SelectedDiscovery == nil || cursor.SelectedDiscovery.CandidateID != second.CandidateID {
		t.Fatalf("neutral Jev choice: count=%d cursor=%+v err=%v", count, cursor, err)
	}
	if interrupted.calls.Load() != 1 {
		t.Fatalf("neutral selection repeated semantic choice: %d", interrupted.calls.Load())
	}
	if runtime.turns != 0 {
		t.Fatalf("saved search was re-extracted: %d", runtime.turns)
	}
	if err := db.CheckSelectedDiscoveryCandidate(ctx, round.ID, first.CandidateID); err != store.ErrFenced {
		t.Fatalf("unselected lead gained official authority: %v", err)
	}
	if err := db.CheckSelectedDiscoveryCandidate(ctx, round.ID, second.CandidateID); err != nil {
		t.Fatalf("selected lead lacks official authority: %v", err)
	}
	var reads int
	if err := db.Read(ctx, func(r store.Reader) error {
		return r.QueryRowContext(ctx, `SELECT count(*) FROM discovery_official_reads WHERE round_id=?`, round.ID).Scan(&reads)
	}); err != nil || reads != 0 {
		t.Fatalf("official read preceded Jev choice: %d %v", reads, err)
	}
}

func TestFourNeutralLeadsNewEmployerCompletesSourcedAssessmentWithinDefaultAllowance(t *testing.T) {
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
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "four-leads-new-employer", Intent: "Find sourced platform work", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active", "discovery:himalayas"}, Operations: []string{store.RoundCodexTurn, store.RoundSearchSource, store.RoundStageDiscovery, store.RoundJevRequest, store.RoundFetchSource, store.RoundRegisterDiscoveryBoard, store.RoundCollectorPage, store.RoundCreateCompany, store.RoundSaveSourceOpportunity}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 16, Items: 10, Tools: 18, Turns: 4}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	researchTurn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "four-lead-search", Operation: store.RoundCodexTurn, ResourceID: "discovery:himalayas", Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
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
	searchText.WriteString("Found 4 jobs matching 'platform' (showing page 1)\n")
	for _, slug := range []string{"one", "two", "three", "four"} {
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
	search, err := reader.Read(ctx, discovery.Input{RoundID: round.ID, Capability: researchCap, RequestKey: "four-search", ResourceID: "discovery:himalayas", Method: "search_jobs", Keyword: "platform", Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"one", "two", "three", "four"} {
		url := fmt.Sprintf("https://himalayas.app/companies/%s/jobs/platform-%s", slug, slug)
		quote := fmt.Sprintf("🚀 **Platform %s**\n🔗 **Apply on Himalayas:** %s", slug, url)
		if _, err := db.StageDiscoveryCandidate(ctx, store.DiscoveryStageInput{RoundID: round.ID, Capability: researchCap, RequestKey: "stage-" + slug,
			Candidate: store.DiscoveryCandidate{AttemptID: search.AttemptID, Kind: "job", Title: "Platform " + slug, URL: url, EvidenceQuote: quote}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.BindRoundThread(ctx, round.ID, researchTurn.ID, researchTurn.Generation, "four-research-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, round.ID, researchTurn.ID, researchTurn.Generation, "four-research-thread", "four-research-turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.ObserveRoundTurn(ctx, round.ID, researchTurn.ID, researchTurn.Generation, "four-research-thread", "four-research-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, agent, round.ID, researchTurn.ID, true, json.RawMessage(`{"staged":4}`), ""); err != nil {
		t.Fatal(err)
	}
	round, err = db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	cursor := agencyCursor{Research: &discoveryResearchPin{Criterion: store.RoleCriterion{ID: "role-platform", Label: "platform"}, SearchID: "research-platform", Page: 1, TurnKey: researchTurn.RequestKey}}
	encoded, _ := json.Marshal(cursor)
	round, err = db.SaveRoundProgress(ctx, owner, round.ID, round.Revision, store.RoundProgress{Step: "discovery_research_selected", Cursor: encoded, Unresolved: round.Unresolved, Report: round.Report})
	if err != nil {
		t.Fatal(err)
	}
	decisions, closeJev := completingDiscoveryDecisions(t)
	defer closeJev()
	decisions.Store = db
	engine := &Engine{Store: db, Runtime: &savingRuntime{db: db}, Decisions: decisions, Context: ctx}
	round, cursor, count, err := engine.continueDiscoveryResearch(ctx, round, cursor)
	if err != nil || count != 4 || cursor.SelectedDiscovery == nil {
		t.Fatalf("four-lead selection: %d %+v %v", count, cursor, err)
	}
	chosen, err := db.RoundDiscoveryCandidate(ctx, round.ID, cursor.SelectedDiscovery.CandidateID)
	if err != nil || !strings.Contains(chosen.URL, "platform-two") {
		t.Fatalf("Jev did not choose saved second lead: %+v %v", chosen, err)
	}
	verifyTurn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "verify-four-leads", Operation: store.RoundCodexTurn, ResourceID: "discovery:himalayas", Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, round.ID, verifyTurn.ID); err != nil {
		t.Fatal(err)
	}
	verifyCap, err := db.IssueRoundToolCapability(ctx, round.ID, verifyTurn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := reader.Read(ctx, discovery.Input{RoundID: round.ID, Capability: verifyCap, RequestKey: "four-company-detail", ResourceID: "discovery:himalayas", Method: "get_company_details", CompanySlug: "two"})
	if err != nil {
		t.Fatal(err)
	}
	boardURL := "https://jobs.lever.co/newco/role-one"
	proofURL := "https://newco.example/"
	proofCost, _ := store.RoundOperationCost(store.RoundFetchSource)
	proofAttempt, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "four-official", Operation: store.RoundFetchSource, ResourceID: "discovery:himalayas", Cost: proofCost, BoundCapability: verifyCap})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, round.ID, proofAttempt.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.PrepareDiscoveryOfficialRead(ctx, store.DiscoveryOfficialRead{AttemptID: proofAttempt.ID, RoundID: round.ID, CandidateID: chosen.ID, CompanyDetailAttemptID: detail.AttemptID, SourceURL: proofURL, ClaimURL: proofURL, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	proof := codexservice.DiscoveryOfficialSnapshot{AttemptID: proofAttempt.ID, CandidateID: chosen.ID, CompanyDetailAttemptID: detail.AttemptID, ClaimURL: proofURL, SourceURL: proofURL, Status: "ok",
		Evidence: codexservice.SourceLinksSnapshot{SourceURL: proofURL, Status: "ok", Links: []codexservice.SourceLink{{URL: boardURL, Text: "Platform Engineer"}}}}
	proofJSON, _ := json.Marshal(proof)
	if err := db.CompleteDiscoveryOfficialRead(ctx, proofAttempt.ID, "ok", proofJSON, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, agent, round.ID, proofAttempt.ID, true, proofJSON, ""); err != nil {
		t.Fatal(err)
	}
	board, err := db.RegisterDiscoveryBoard(ctx, store.DiscoveryBoardInput{RoundID: round.ID, Capability: verifyCap, RequestKey: "four-board", CandidateID: chosen.ID, OfficialLinksAttemptID: proofAttempt.ID, LeverURL: boardURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundThread(ctx, round.ID, verifyTurn.ID, verifyTurn.Generation, "four-verify-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRoundTurn(ctx, round.ID, verifyTurn.ID, verifyTurn.Generation, "four-verify-thread", "four-verify-turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.ObserveRoundTurn(ctx, round.ID, verifyTurn.ID, verifyTurn.Generation, "four-verify-thread", "four-verify-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, agent, round.ID, verifyTurn.ID, true, json.RawMessage(`{"verified":true}`), ""); err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	page := `[{"id":"role-one","text":"Engineer one","descriptionPlain":"Build Go services in Brussels.","hostedUrl":"https://jobs.lever.co/newco/role-one"},{"id":"role-two","text":"Engineer two","descriptionPlain":"Build Go services in Brussels.","hostedUrl":"https://jobs.lever.co/newco/role-two"},{"id":"role-three","text":"Engineer three","descriptionPlain":"Build Go services in Brussels.","hostedUrl":"https://jobs.lever.co/newco/role-three"},{"id":"role-four","text":"Engineer four","descriptionPlain":"Build Go services in Brussels.","hostedUrl":"https://jobs.lever.co/newco/role-four"}]`
	engine.Collector = &collector.Collector{HTTPClient: &http.Client{Transport: collectorTransport(func(req *http.Request) (*http.Response, error) {
		providerCalls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(page)), Request: req}, nil
	})}}
	engine.run(ctx, round)
	finished, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Code                 string           `json:"code"`
		AssessedSources      []assessedSource `json:"assessedSources"`
		CollectorAttemptID   string           `json:"collectorAttemptId"`
		CollectorHasMore     bool             `json:"collectorHasMore"`
		UnreviewedCandidates int              `json:"unreviewedCandidates"`
	}
	if json.Unmarshal(finished.Report, &result) != nil || finished.State != store.RoundCompleted || result.Code != "sourced_opportunity_assessed" || len(result.AssessedSources) != 1 || result.AssessedSources[0].ScreeningStatus != "proposed" || result.AssessedSources[0].OrganisationStatus != "selected" || providerCalls.Load() != 1 {
		t.Fatalf("new employer discovery incomplete: state=%s report=%s used=%+v board=%+v", finished.State, finished.Report, finished.Used, board)
	}
	if finished.Used != (store.RoundAllowance{Requests: 12, Items: 10, Tools: 14, Turns: 3}) || finished.Limits != (store.RoundAllowance{Requests: 16, Items: 10, Tools: 18, Turns: 4}) {
		t.Fatalf("default allowance accounting: used=%+v limits=%+v", finished.Used, finished.Limits)
	}
	batchJSON, err := db.RoundCollectorBatch(ctx, result.CollectorAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var batch collector.Batch
	if json.Unmarshal(batchJSON, &batch) != nil || !result.CollectorHasMore || batch.Next == nil || len(batch.Next.Pending) != 1 || !bytes.Contains(batch.Next.Pending[0].Raw, []byte("role-four")) {
		t.Fatalf("fourth posting was not retained: report=%s batch=%+v", finished.Report, batch)
	}
}
