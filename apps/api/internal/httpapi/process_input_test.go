package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/agency"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type inputFixtureRuntime struct {
	db                *store.Store
	companyID         string
	sourceSaved       chan struct{}
	continueAfterSave <-chan struct{}
}

func (r *inputFixtureRuntime) CheckRound(context.Context, string) error { return nil }
func (r *inputFixtureRuntime) ExecuteRoundTurn(ctx context.Context, agent store.Actor, roundID string, input codexservice.RoundTurnInput) (store.RoundAttempt, error) {
	turn, _, err := r.db.ReserveRoundAttempt(ctx, agent, roundID, store.RoundAttemptInput{RequestKey: input.RequestKey, Operation: store.RoundCodexTurn, ResourceID: input.ResourceID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		return turn, err
	}
	if turn, err = r.db.MarkRoundDispatched(ctx, roundID, turn.ID); err != nil {
		return turn, err
	}
	if err := r.db.BindRoundThread(ctx, roundID, turn.ID, turn.Generation, "input-fixture-thread"); err != nil {
		return turn, err
	}
	if err := r.db.BindRoundTurn(ctx, roundID, turn.ID, turn.Generation, "input-fixture-thread", "input-fixture-turn"); err != nil {
		return turn, err
	}
	if err := r.db.ObserveRoundTurn(ctx, roundID, turn.ID, turn.Generation, "input-fixture-thread", "input-fixture-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		return turn, err
	}
	capability, err := r.db.IssueRoundToolCapability(ctx, roundID, turn.ID, agent.ID)
	if err != nil {
		return turn, err
	}
	var evidence struct {
		Instruction     store.OwnerInstruction `json:"instruction"`
		SourceOpeningID string                 `json:"sourceOpeningId"`
	}
	if err := json.Unmarshal([]byte(input.Evidence), &evidence); err != nil {
		return turn, err
	}
	if evidence.SourceOpeningID != "" {
		company, readErr := r.db.Company(ctx, r.companyID)
		if readErr != nil {
			return turn, readErr
		}
		_, _, err = r.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "save:" + evidence.SourceOpeningID,
			Operation: store.RoundSaveSourceOpportunity, ResourceID: "source-opening:" + evidence.SourceOpeningID, ExpectedRevision: company.Revision, Capability: capability,
			SourceOpportunity: &store.SourceOpportunityMutationInput{SourceOpeningID: evidence.SourceOpeningID, ExpectedRevision: company.Revision, CompanyID: company.ID,
				Opportunity: store.OpportunityInput{CompanyID: company.ID, Title: "Backend engineer", Kind: "employment", Stage: "discovered", WorkPattern: "remote", LocationText: "Brussels"}}})
	} else {
		profile, readErr := r.db.CurrentPreferences(ctx)
		if readErr != nil {
			return turn, readErr
		}
		profile.PreferredLocation = "Ghent"
		_, _, err = r.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "apply:" + evidence.Instruction.ID,
			Operation: store.RoundCorrectPreferences, ResourceID: "profile:current", ExpectedRevision: evidence.Instruction.ExpectedRevision,
			OwnerInstructionID: evidence.Instruction.ID, Preferences: &profile, Capability: capability})
	}
	if err != nil {
		return turn, err
	}
	finished, err := r.db.FinishRoundAttempt(ctx, agent, roundID, turn.ID, true, json.RawMessage(`{"applied":true}`), "")
	if err == nil && evidence.SourceOpeningID != "" && r.sourceSaved != nil {
		close(r.sourceSaved)
		<-r.continueAfterSave
	}
	return finished, err
}

type inputWorkerBarrier struct {
	engine      *agency.Engine
	waitStarted chan struct{}
}

type blockedOwnerSource struct{ entered chan struct{} }

func (b blockedOwnerSource) ReadVacancy(ctx context.Context, _ string) (string, error) {
	close(b.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

func (w *inputWorkerBarrier) LaunchRound(ctx context.Context, r store.Round) error {
	return w.engine.LaunchRound(ctx, r)
}
func (w *inputWorkerBarrier) CancelRound(id string) { w.engine.CancelRound(id) }
func (w *inputWorkerBarrier) WaitRoundStopped(ctx context.Context, id string) error {
	close(w.waitStarted)
	return w.engine.WaitRoundStopped(ctx, id)
}

type inputFixtureDecisions struct{}

func (inputFixtureDecisions) RunDecision(context.Context, jevservice.Binding, jev.DecisionInput) (jev.DecisionResult, error) {
	return jev.DecisionResult{}, errors.New("unexpected decision")
}
func (inputFixtureDecisions) RunScreening(context.Context, jevservice.Binding, jev.ScreeningInput) (jev.ScreeningResult, error) {
	return jev.ScreeningResult{}, errors.New("unexpected screening")
}
func (inputFixtureDecisions) RunOrganisation(context.Context, jevservice.Binding, jev.OrganisationInput) (jev.OrganisationResult, error) {
	return jev.OrganisationResult{}, errors.New("unexpected organisation")
}

type inputFixtureSource struct {
	expectedURL, text string
	reads             *int
}

func (f inputFixtureSource) ReadVacancy(_ context.Context, sourceURL string) (string, error) {
	if sourceURL != f.expectedURL {
		return "", fmt.Errorf("unexpected source URL: %s", sourceURL)
	}
	*f.reads += 1
	return f.text, nil
}

func TestProcessInputHTTPAppliesProfileAndRebindsExactRound(t *testing.T) {
	h := newRecordHTTP(t)
	h.server.Close()
	engine := &agency.Engine{Store: h.db, Runtime: &inputFixtureRuntime{db: h.db}, Decisions: inputFixtureDecisions{}, Context: context.Background()}
	h.server = httptest.NewServer(NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: h.db, Readiness: engine, Worker: engine}}))
	h.client = h.server.Client()
	t.Cleanup(h.server.Close)
	h.login()
	ctx := context.Background()
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf(`{"requestKey":"profile-action","targetKind":"profile","targetId":"current","expectedRevision":%d,"text":"My preferred location is Ghent."}`, profile.Version)
	status, body := h.owner(http.MethodPost, "/rounds/process-input", request)
	requireStatus(t, status, http.StatusCreated, body)
	var response struct {
		InstructionID string `json:"instructionId"`
		Round         struct {
			ID string `json:"id"`
		} `json:"round"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Round.ID == "" || response.InstructionID == "" {
		t.Fatalf("commissioned response: %s %v", body, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var finished store.Round
	for time.Now().Before(deadline) {
		finished, err = h.db.Round(ctx, response.Round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished.State == store.RoundCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, err := h.db.CurrentPreferences(ctx)
	if err != nil || finished.State != store.RoundCompleted || current.PreferredLocation != "Ghent" ||
		finished.InitialProfileVersion != profile.Version || finished.ProfileVersion != profile.Version+1 {
		t.Fatalf("profile action unfinished: round=%+v current=%+v err=%v", finished, current, err)
	}
	var report struct {
		Code           string                    `json:"code"`
		AppliedChanges []store.RoundHistoryEvent `json:"appliedChanges"`
	}
	if json.Unmarshal(finished.Report, &report) != nil || report.Code != "input_applied" || len(report.AppliedChanges) != 1 || report.AppliedChanges[0].Operation != store.RoundCorrectPreferences {
		t.Fatalf("reported changes: %s", finished.Report)
	}
	status, replay := h.owner(http.MethodPost, "/rounds/process-input", request)
	requireStatus(t, status, http.StatusOK, replay)
	if decodeObject(t, replay)["instructionId"] != response.InstructionID {
		t.Fatalf("input replay changed instruction: %s", replay)
	}
	stale := fmt.Sprintf(`{"requestKey":"stale-profile-action","targetKind":"profile","targetId":"current","expectedRevision":%d,"text":"Move again."}`, profile.Version)
	status, body = h.owner(http.MethodPost, "/rounds/process-input", stale)
	requireStatus(t, status, http.StatusConflict, body)
}

func TestProcessInputHTTPProcessesPastedVacancyAndRecordsOrganisation(t *testing.T) {
	h := newRecordHTTP(t)
	h.server.Close()
	ctx := context.Background()
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	company, _, err := h.db.CreateCompany(ctx, store.Actor{Kind: "administrator", ID: "owner-placeholder"}, store.CompanyInput{Name: "Synthetic Employer", Website: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	categories, err := h.db.CurrentOrganisationCategories(ctx)
	if err != nil || len(categories.Categories) == 0 {
		t.Fatalf("categories: %v", err)
	}
	chosenCategory := categories.Categories[0].ID
	probabilities := map[string]float64{chosenCategory: 0.8, "__uncertain__": 0.2}
	for _, category := range categories.Categories[1:] {
		probabilities[category.ID] = 0
	}
	var jevCalls int
	jevServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jevCalls++
		w.Header().Set("Content-Type", "application/json")
		var wire struct {
			State struct {
				Candidates []struct {
					ID           string `json:"id"`
					CapabilityID string `json:"capability_id"`
				} `json:"candidates"`
			} `json:"state"`
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Errorf("decode Jev request: %v", err)
			return
		}
		if question, ok := wire.Questions["selected_candidate"]; ok {
			chosen := ""
			for _, candidate := range wire.State.Candidates {
				if candidate.CapabilityID == "home_review_result" {
					chosen = candidate.ID
				}
			}
			if chosen == "" {
				t.Errorf("saved result review absent from next-action choices")
				return
			}
			probabilities := make(map[string]float64, len(question.Criteria))
			for id := range question.Criteria {
				probabilities[id] = 0
			}
			probabilities[chosen] = 1
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"selected_candidate": map[string]any{"type": "choice", "choice": chosen, "probabilities": probabilities, "confidence": 0.8}}, "usage": map[string]any{"input_tokens": 32, "output_tokens": 8}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"organisation_category": map[string]any{"type": "choice", "choice": chosenCategory, "probabilities": probabilities, "confidence": 0.8}}, "usage": map[string]any{"input_tokens": 32, "output_tokens": 8}})
	}))
	defer jevServer.Close()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-key")
	config := jev.DefaultConfig()
	config.Enabled, config.Endpoint, config.Timeout, config.MaxAttempts = true, jevServer.URL+"/v1/systemone", time.Second, 1
	client, err := jev.NewFromEnvironment(config, jevServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	sourceSaved, continueAfterSave := make(chan struct{}), make(chan struct{})
	engine := &agency.Engine{Store: h.db, Runtime: &inputFixtureRuntime{db: h.db, companyID: company.ID, sourceSaved: sourceSaved, continueAfterSave: continueAfterSave}, Decisions: jevservice.Service{Store: h.db, Client: client}, Context: ctx}
	worker := &inputWorkerBarrier{engine: engine, waitStarted: make(chan struct{})}
	roundService := &rounds.Service{Store: h.db, Readiness: engine, Worker: worker}
	h.server = httptest.NewServer(NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: roundService}))
	h.client = h.server.Client()
	t.Cleanup(h.server.Close)
	owner := h.login()
	request := fmt.Sprintf(`{"requestKey":"paste-vacancy","targetKind":"campaign","targetId":"active","expectedRevision":%d,"originalText":"Synthetic Employer seeks a backend engineer to build Go services in Brussels."}`, profile.Version)
	status, body := h.owner(http.MethodPost, "/rounds/process-input", request)
	requireStatus(t, status, http.StatusCreated, body)
	var response struct {
		IngestionID string `json:"ingestionId"`
		Round       struct {
			ID string `json:"id"`
		} `json:"round"`
	}
	if json.Unmarshal(body, &response) != nil || response.IngestionID == "" || response.Round.ID == "" {
		t.Fatalf("commission: %s", body)
	}
	<-sourceSaved
	paused, err := roundService.Stop(ctx, owner.Actor(), response.Round.ID)
	if err != nil || paused.State != store.RoundPaused || jevCalls != 0 {
		t.Fatalf("source save did not pause before organisation: %+v jev=%d err=%v", paused, jevCalls, err)
	}
	resumeResult := make(chan error, 1)
	go func() {
		_, resumeErr := roundService.Resume(ctx, owner.Actor(), response.Round.ID)
		resumeResult <- resumeErr
	}()
	<-worker.waitStarted
	select {
	case resumeErr := <-resumeResult:
		t.Fatalf("resume crossed active worker barrier: %v", resumeErr)
	default:
	}
	close(continueAfterSave)
	if err := <-resumeResult; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var finished store.Round
	for time.Now().Before(deadline) {
		finished, err = h.db.Round(ctx, response.Round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished.State == store.RoundCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ingestion, err := h.db.Ingestion(ctx, response.IngestionID)
	if err != nil || finished.State != store.RoundCompleted || ingestion.OpportunityID == "" || ingestion.SourceID == "" || jevCalls != 2 {
		t.Fatalf("pasted vacancy not processed: state=%s ingestion=%+v jevCalls=%d report=%s err=%v", finished.State, ingestion, jevCalls, finished.Report, err)
	}
	opportunity, err := h.db.Opportunity(ctx, ingestion.OpportunityID)
	if err != nil || opportunity.OriginalText != ingestion.OriginalText || !strings.Contains(opportunity.OriginalText, "Go services") {
		t.Fatalf("saved source text: %+v %v", opportunity, err)
	}
	var report struct {
		Code           string                    `json:"code"`
		AppliedChanges []store.RoundHistoryEvent `json:"appliedChanges"`
		Unresolved     []string                  `json:"unresolved"`
	}
	if json.Unmarshal(finished.Report, &report) != nil || report.Code != "input_applied" || len(report.AppliedChanges) != 1 || len(report.Unresolved) != 0 {
		t.Fatalf("input report: %s", finished.Report)
	}
	status, replay := h.owner(http.MethodPost, "/rounds/process-input", request)
	requireStatus(t, status, http.StatusOK, replay)
	if decodeObject(t, replay)["ingestionId"] != response.IngestionID {
		t.Fatalf("vacancy replay changed source: %s", replay)
	}
	changedRevision := fmt.Sprintf(`{"requestKey":"paste-vacancy","targetKind":"campaign","targetId":"active","expectedRevision":%d,"originalText":"Synthetic Employer seeks a backend engineer to build Go services in Brussels."}`, profile.Version+1)
	status, body = h.owner(http.MethodPost, "/rounds/process-input", changedRevision)
	requireStatus(t, status, http.StatusConflict, body)
}

func TestProcessInputHTTPReadsSupportedURLAndSavesVerifiedSource(t *testing.T) {
	h := newRecordHTTP(t)
	h.server.Close()
	ctx := context.Background()
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	company, _, err := h.db.CreateCompany(ctx, store.Actor{Kind: "administrator", ID: "owner-placeholder"}, store.CompanyInput{Name: "Synthetic Employer", Website: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	categories, err := h.db.CurrentOrganisationCategories(ctx)
	if err != nil || len(categories.Categories) == 0 {
		t.Fatalf("categories: %v", err)
	}
	chosen := categories.Categories[0].ID
	probabilities := map[string]float64{chosen: 0.8, "__uncertain__": 0.2}
	for _, category := range categories.Categories[1:] {
		probabilities[category.ID] = 0
	}
	jevServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"organisation_category": map[string]any{"type": "choice", "choice": chosen, "probabilities": probabilities, "confidence": 0.8}}, "usage": map[string]any{"input_tokens": 32, "output_tokens": 8}})
	}))
	defer jevServer.Close()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-key")
	config := jev.DefaultConfig()
	config.Enabled, config.Endpoint, config.Timeout, config.MaxAttempts = true, jevServer.URL+"/v1/systemone", time.Second, 1
	client, err := jev.NewFromEnvironment(config, jevServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	const sourceURL = "https://example.test/jobs/post-1"
	const posting = "Backend engineer. Build Go services in Brussels."
	var reads int
	reader := inputFixtureSource{expectedURL: sourceURL, text: posting, reads: &reads}
	engine := &agency.Engine{Store: h.db, Runtime: &inputFixtureRuntime{db: h.db, companyID: company.ID}, Decisions: jevservice.Service{Store: h.db, Client: client}, InputReader: reader, Context: ctx}
	h.server = httptest.NewServer(NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: h.db, Readiness: engine, Worker: engine}}))
	h.client = h.server.Client()
	t.Cleanup(h.server.Close)
	owner := h.login()
	preexisting, _, err := h.db.SubmitIngestion(ctx, owner.Actor(), store.IngestionInput{Origin: "owner", SourceURL: sourceURL, IdempotencyKey: "earlier-owner-source"})
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf(`{"requestKey":"url-vacancy","targetKind":"campaign","targetId":"active","expectedRevision":%d,"sourceUrl":%q}`, profile.Version, sourceURL)
	status, body := h.owner(http.MethodPost, "/rounds/process-input", request)
	requireStatus(t, status, http.StatusCreated, body)
	var response struct {
		IngestionID string `json:"ingestionId"`
		Round       struct {
			ID string `json:"id"`
		} `json:"round"`
	}
	if json.Unmarshal(body, &response) != nil || response.IngestionID != preexisting.ID || response.Round.ID == "" {
		t.Fatalf("URL commission: %s", body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		finished, err := h.db.Round(ctx, response.Round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished.State == store.RoundCompleted {
			item, err := h.db.Ingestion(ctx, response.IngestionID)
			if err != nil || item.OriginalText != posting || item.SourceID == "" || item.OpportunityID == "" || reads != 1 {
				t.Fatalf("verified URL not saved: source=%+v reads=%d report=%s err=%v", item, reads, finished.Report, err)
			}
			var report struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(finished.Report, &report) != nil || report.Code != "input_applied" {
				t.Fatalf("URL report: %s", finished.Report)
			}
			var beforeID, beforeSHA string
			var beforeSightings, beforeIntakes int
			readSourceState := func() (string, string, int, int) {
				t.Helper()
				var id, sha string
				var sightings, intakes int
				err := h.db.Read(ctx, func(r store.Reader) error {
					if err := r.QueryRowContext(ctx, `SELECT current_ingestion_id,current_sha256 FROM source_openings WHERE id=?`, item.SourceOpeningID).Scan(&id, &sha); err != nil {
						return err
					}
					if err := r.QueryRowContext(ctx, `SELECT count(*) FROM source_sightings WHERE source_opening_id=?`, item.SourceOpeningID).Scan(&sightings); err != nil {
						return err
					}
					return r.QueryRowContext(ctx, `SELECT count(*) FROM ingestion_requests`).Scan(&intakes)
				})
				if err != nil {
					t.Fatal(err)
				}
				return id, sha, sightings, intakes
			}
			beforeID, beforeSHA, beforeSightings, beforeIntakes = readSourceState()
			status, replay := h.owner(http.MethodPost, "/rounds/process-input", request)
			requireStatus(t, status, http.StatusOK, replay)
			if decodeObject(t, replay)["ingestionId"] != preexisting.ID || decodeObject(t, replay)["round"].(map[string]any)["id"] != response.Round.ID {
				t.Fatalf("deduplicated commission replay: %s", replay)
			}
			afterID, afterSHA, afterSightings, afterIntakes := readSourceState()
			if afterID != beforeID || afterSHA != beforeSHA || afterSightings != beforeSightings || afterIntakes != beforeIntakes || reads != 1 {
				t.Fatalf("replay changed source: before=%s/%s/%d/%d after=%s/%s/%d/%d reads=%d", beforeID, beforeSHA, beforeSightings, beforeIntakes, afterID, afterSHA, afterSightings, afterIntakes, reads)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("verified URL round did not complete")
}

func TestProcessInputHTTPReplacesOnlyNamedPausedRound(t *testing.T) {
	h := newRecordHTTP(t)
	owner := h.login()
	ctx := context.Background()
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prior, _, err := h.db.StartRound(ctx, owner.Actor(), store.StartRoundInput{RequestKey: "older-round", Intent: "Earlier owner work", Outcome: "prepare", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active"}, Operations: []string{store.RoundCodexTurn}},
		Limits: store.RoundAllowance{Tools: 1, Turns: 1}, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	prior, err = h.db.ActivateRound(ctx, owner.Actor(), prior.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.db.StopRound(ctx, owner.Actor(), prior.ID); err != nil {
		t.Fatal(err)
	}
	prior, err = h.db.PauseStoppedRound(ctx, owner.Actor(), prior.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.server.Close()
	engine := &agency.Engine{Store: h.db, Runtime: &inputFixtureRuntime{db: h.db}, Decisions: inputFixtureDecisions{}, Context: ctx}
	h.server = httptest.NewServer(NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: h.db, Readiness: engine, Worker: engine}}))
	h.client = h.server.Client()
	t.Cleanup(h.server.Close)
	request := func(revision int64) string {
		return fmt.Sprintf(`{"requestKey":"replace-with-profile","targetKind":"profile","targetId":"current","expectedRevision":%d,"text":"Prefer Ghent.","replacePaused":{"roundId":%q,"expectedRevision":%d}}`, profile.Version, prior.ID, revision)
	}
	status, body := h.owner(http.MethodPost, "/rounds/process-input", request(prior.Revision+1))
	requireStatus(t, status, http.StatusForbidden, body)
	still, err := h.db.Round(ctx, prior.ID)
	if err != nil || still.State != store.RoundPaused {
		t.Fatalf("stale replacement ended prior work: %+v %v", still, err)
	}
	status, body = h.owner(http.MethodPost, "/rounds/process-input", request(prior.Revision))
	requireStatus(t, status, http.StatusCreated, body)
	ended, err := h.db.Round(ctx, prior.ID)
	if err != nil || ended.State != store.RoundFailed || ended.StopReason != "replaced_by_owner_input" {
		t.Fatalf("named paused round not closed: %+v %v", ended, err)
	}
	var response struct {
		ReplacedRoundID string `json:"replacedRoundId"`
		Round           struct {
			ID string `json:"id"`
		} `json:"round"`
	}
	if json.Unmarshal(body, &response) != nil || response.ReplacedRoundID != prior.ID || response.Round.ID == "" {
		t.Fatalf("replacement response: %s", body)
	}
	status, body = h.owner(http.MethodPost, "/rounds/process-input", request(prior.Revision))
	requireStatus(t, status, http.StatusOK, body)
}

func TestProcessInputHTTPRetainsUnsupportedURLAsUnresolved(t *testing.T) {
	h := newRecordHTTP(t)
	h.server.Close()
	ctx := context.Background()
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	engine := &agency.Engine{Store: h.db, Runtime: &inputFixtureRuntime{db: h.db}, Decisions: inputFixtureDecisions{}, Context: ctx}
	h.server = httptest.NewServer(NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: h.db, Readiness: engine, Worker: engine}}))
	h.client = h.server.Client()
	t.Cleanup(h.server.Close)
	h.login()
	request := fmt.Sprintf(`{"requestKey":"unsupported-vacancy","targetKind":"campaign","targetId":"active","expectedRevision":%d,"sourceUrl":"https://example.test/jobs/one"}`, profile.Version)
	status, body := h.owner(http.MethodPost, "/rounds/process-input", request)
	requireStatus(t, status, http.StatusCreated, body)
	var response struct {
		IngestionID string `json:"ingestionId"`
		Round       struct {
			ID string `json:"id"`
		} `json:"round"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.IngestionID == "" || response.Round.ID == "" {
		t.Fatalf("unsupported commission: %s %v", body, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := h.db.Round(ctx, response.Round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == store.RoundCompleted {
			var report struct {
				Code       string   `json:"code"`
				Unresolved []string `json:"unresolved"`
			}
			if json.Unmarshal(r.Report, &report) != nil || report.Code != "unsupported_source_url" || len(report.Unresolved) == 0 {
				t.Fatalf("unsupported report: %s", r.Report)
			}
			item, err := h.db.Ingestion(ctx, response.IngestionID)
			if err != nil || item.SourceURL != "https://example.test/jobs/one" || item.OriginalText != "" {
				t.Fatalf("saved owner source: %+v %v", item, err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("unsupported source round did not finish")
}

func TestProcessInputSourceDeadlineExpiresAndRetainsUncertainRead(t *testing.T) {
	h := newRecordHTTP(t)
	owner := h.login()
	ctx := context.Background()
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := h.db.SubmitIngestion(ctx, owner.Actor(), store.IngestionInput{Origin: "owner", SourceURL: "https://jobs.lever.co/example/blocked", IdempotencyKey: "blocked-source"})
	if err != nil {
		t.Fatal(err)
	}
	reader := blockedOwnerSource{entered: make(chan struct{})}
	engine := &agency.Engine{Store: h.db, Runtime: &inputFixtureRuntime{db: h.db}, Decisions: inputFixtureDecisions{}, InputReader: reader, Context: ctx}
	service := &rounds.Service{Store: h.db, Readiness: engine, Worker: engine}
	round, created, err := service.Start(ctx, owner.Actor(), store.StartRoundInput{RequestKey: "blocked-input", Intent: "Process the saved owner URL", Outcome: "process_input", ProfileVersion: profile.Version,
		Scope: store.RoundScope{InputRefs: []string{"ingestion:" + source.ID}, Resources: []string{"campaign:active", "ingestion:" + source.ID},
			Operations: []string{store.RoundCodexTurn, store.RoundFetchSource}, Delegates: []string{"codex-runner"}},
		Limits: store.RoundAllowance{Requests: 1, Tools: 2, Turns: 1}, Deadline: time.Now().Add(300 * time.Millisecond)})
	if err != nil || !created {
		t.Fatalf("start deadline test: %+v %v", round, err)
	}
	<-reader.entered
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ended, err := h.db.Round(ctx, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ended.State == store.RoundFailed {
			if ended.StopReason != "deadline_reached" || ended.Used.Requests != 1 || ended.Used.Tools != 1 || !ended.ReconciliationRequired {
				t.Fatalf("deadline audit: %+v", ended)
			}
			attempt, err := h.db.RoundAttemptForRequest(ctx, round.ID, fmt.Sprintf("read:%s:g1", source.ID))
			if err != nil || attempt.State != store.AttemptUncertain {
				t.Fatalf("read uncertainty: %+v %v", attempt, err)
			}
			if active, err := h.db.ActiveRound(ctx); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("expired round retained active slot: %+v %v", active, err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("deadline did not terminalize blocked source read")
}

func TestProcessInputHTTPReadbackConfirmsSavedCorrection(t *testing.T) {
	h := newRecordHTTP(t)
	h.server.Close()
	engine := &agency.Engine{Store: h.db, Runtime: &inputFixtureRuntime{db: h.db}, Decisions: inputFixtureDecisions{}, Context: context.Background()}
	h.server = httptest.NewServer(NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Rounds: &rounds.Service{Store: h.db, Readiness: engine, Worker: engine}}))
	h.client = h.server.Client()
	t.Cleanup(h.server.Close)
	h.login()
	ctx := context.Background()
	profile, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf(`{"requestKey":"profile-readback","targetKind":"profile","targetId":"current","expectedRevision":%d,"text":"My preferred location is Ghent."}`, profile.Version)
	status, body := h.owner(http.MethodPost, "/rounds/process-input", request)
	requireStatus(t, status, http.StatusCreated, body)
	roundID := decodeObject(t, body)["round"].(map[string]any)["id"].(string)
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, body = h.owner(http.MethodGet, "/rounds/"+roundID)
		requireStatus(t, status, http.StatusOK, body)
		state := decodeObject(t, body)["state"].(string)
		if state == string(store.RoundCompleted) {
			break
		}
		if state == string(store.RoundFailed) || state == "cancelled" || time.Now().After(deadline) {
			t.Fatalf("correction round did not complete: %s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, body = h.owner(http.MethodGet, "/preferences")
	requireStatus(t, status, http.StatusOK, body)
	readback := decodeObject(t, body)
	if readback["version"].(float64) != float64(profile.Version+1) || readback["preferredLocation"].(string) != "Ghent" {
		t.Fatalf("saved readback mismatch: %s", body)
	}

	invalid := fmt.Sprintf(`{"requestKey":"profile-bad-target","targetKind":"profile","targetId":"other","expectedRevision":%d,"text":"Typo target."}`, profile.Version+1)
	status, body = h.owner(http.MethodPost, "/rounds/process-input", invalid)
	requireStatus(t, status, http.StatusBadRequest, body)
	unknown := fmt.Sprintf(`{"requestKey":"profile-bad-kind","targetKind":"wish","targetId":"current","expectedRevision":%d,"text":"Unknown kind."}`, profile.Version+1)
	status, body = h.owner(http.MethodPost, "/rounds/process-input", unknown)
	requireStatus(t, status, http.StatusBadRequest, body)
}
