// Test ownership: lane Q owns this file (Q0 connected regression harness).
//
// Connected journey boundary: production HTTP router (NewHandler) + real
// temporary store; only Contributor/Jev/Standard provider/process boundaries
// are stubbed with controlled adapters (cj-prefixed). Every case starts with
// NO seeded catalog, NO answered stage, and NO ready artifacts. Each case
// must FAIL on current code with an assertion proving the reviewed gap; the
// failure message names the finding (R01, R03, ...).

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var errCJUnexpectedProviderCall = fmt.Errorf("connected journey: unexpected provider call")

// cjAnswerMatcher counts Jev provider invocations behind answer matching.
type cjAnswerMatcher struct {
	calls int
}

func (s *cjAnswerMatcher) RunAnswerMatch(_ context.Context, _ jevservice.Binding, _ jev.AnswerMatchInput) (jev.AnswerMatchResult, error) {
	s.calls++
	return jev.AnswerMatchResult{}, errCJUnexpectedProviderCall
}

// cjJevEvaluator is the controlled Jev classifier behind answer matching: it
// judges through the production jev.MatchAnswers protocol with fixed choices.
type cjJevEvaluator struct {
	choices map[string]string // match key -> answer id or none_fits
	calls   int
}

func (f *cjJevEvaluator) Evaluate(_ context.Context, request jev.Request) (jev.Result, error) {
	f.calls++
	answers := map[string]jev.Answer{}
	for id, question := range request.Questions {
		options := question.(jev.ChoiceQuestion).Criteria
		choice, ok := f.choices[id]
		if !ok {
			choice = jev.AnswerMatchNoneFits
		}
		probabilities := map[string]float64{}
		for option := range options {
			probabilities[option] = 0
		}
		probabilities[choice] = 1
		answers[id] = jev.Answer{Type: "choice",
			Choice: &jev.ChoiceAnswer{Choice: choice, Probabilities: probabilities, Confidence: 0.8}}
	}
	return jev.Result{RequestedModel: "jev-test-1", ReturnedModel: "jev-test-1",
		Usage: jev.Usage{InputTokens: 12, OutputTokens: 4}, Answers: answers}, nil
}

// cjJevMatcher judges one batch through cjJevEvaluator and records the
// captured exchange exactly like the production recording evaluator, so the
// production store validation (ValidateCapturedAnswerMatch) applies.
type cjJevMatcher struct {
	db      *store.Store
	t       *testing.T
	calls   int
	choices map[string]string // question id -> answer id or none_fits
}

func (s *cjJevMatcher) RunAnswerMatch(ctx context.Context, binding jevservice.Binding, input jev.AnswerMatchInput) (jev.AnswerMatchResult, error) {
	s.calls++
	ordered := append([]jev.AnswerMatchQuestion(nil), input.Questions...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].QuestionID < ordered[j].QuestionID })
	byKey := map[string]string{}
	position := 0
	for _, question := range ordered {
		if len(question.Candidates) == 0 {
			continue
		}
		choice := s.choices[question.QuestionID]
		if choice == "" {
			choice = jev.AnswerMatchNoneFits
		}
		byKey["match_"+strconv.Itoa(position)] = choice
		position++
	}
	fake := &cjJevEvaluator{choices: byKey}
	result, err := jev.MatchAnswers(ctx, fake, input)
	if err != nil {
		return jev.AnswerMatchResult{}, err
	}
	if fake.calls != 1 {
		return jev.AnswerMatchResult{}, fmt.Errorf("connected journey: one charged call per batch, got %d", fake.calls)
	}
	raw := cjAnswerMatchRaw(s.t, input, s.choices)
	attempt, _, err := s.db.ReserveRoundAttempt(ctx, binding.Actor, binding.RoundID, store.RoundAttemptInput{
		RequestKey: binding.RequestKeyPrefix + "/0", Operation: store.RoundJevRequest,
		ResourceID: binding.ResourceID, Cost: store.RoundAllowance{Requests: 1}})
	if err != nil {
		return jev.AnswerMatchResult{}, err
	}
	if _, err := s.db.MarkRoundDispatched(ctx, binding.RoundID, attempt.ID); err != nil {
		return jev.AnswerMatchResult{}, err
	}
	sum := sha256.Sum256(result.RequestSnapshot)
	refs, _ := json.Marshal(map[string]string{"check_id": input.CheckID})
	candidates, _ := json.Marshal([]string{"answer", jev.AnswerMatchNoneFits})
	exchange, err := s.db.BeginJevAttempt(ctx, store.JevAttemptStart{RoundID: binding.RoundID,
		RoundAttemptID: attempt.ID, Purpose: jev.AnswerMatchPurpose, InputSHA256: hex.EncodeToString(sum[:]),
		SourceRefsJSON: refs, CandidateSetJSON: candidates, ProfileVersion: binding.ProfileVersion,
		RubricVersion: jev.AnswerMatchRubricVersion, RequestedModel: result.RequestedModel,
		LogicalRequestJSON: result.RequestSnapshot, TransportRequestBytes: []byte(`{"test":true}`)})
	if err != nil {
		return jev.AnswerMatchResult{}, err
	}
	if _, err := s.db.FinishJevAttempt(ctx, store.JevAttemptFinish{ID: exchange.ID, Status: "succeeded",
		ReturnedModel: result.ReturnedModel, RawResponseBytes: raw}); err != nil {
		return jev.AnswerMatchResult{}, err
	}
	done, _ := json.Marshal(map[string]string{"jevAttemptId": exchange.ID})
	if _, err := s.db.FinishRoundAttempt(ctx, binding.Actor, binding.RoundID, attempt.ID, true, done, ""); err != nil {
		return jev.AnswerMatchResult{}, err
	}
	return result, nil
}

// cjAnswerMatchRaw rebuilds provider bytes for the canonical judged order so
// the captured exchange replays exactly what the fake evaluator returned.
func cjAnswerMatchRaw(t *testing.T, input jev.AnswerMatchInput, choices map[string]string) []byte {
	t.Helper()
	ordered := append([]jev.AnswerMatchQuestion(nil), input.Questions...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].QuestionID < ordered[j].QuestionID })
	answers := map[string]any{}
	position := 0
	for _, question := range ordered {
		if len(question.Candidates) == 0 {
			continue
		}
		choice, ok := choices[question.QuestionID]
		if !ok {
			choice = jev.AnswerMatchNoneFits
		}
		probabilities := map[string]float64{jev.AnswerMatchNoneFits: 0}
		for _, candidate := range question.Candidates {
			probabilities[candidate.AnswerID] = 0
		}
		probabilities[choice] = 1
		answers["match_"+strconv.Itoa(position)] = map[string]any{"type": "choice", "choice": choice,
			"probabilities": probabilities, "confidence": 0.8}
		position++
	}
	raw, err := json.Marshal(map[string]any{"model": "jev-test-1", "answers": answers,
		"usage": map[string]any{"input_tokens": 12, "output_tokens": 4}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// cjMaterialStub counts Standard provider invocations behind artifact
// drafting; exact edits must never need it.
type cjMaterialStub struct {
	draftCalls int
	modelCalls int
}

func (s *cjMaterialStub) DraftOpportunityArtifacts(_ context.Context, _ store.Actor, _, _, _, _ string, _ int64) (store.ArtifactReadinessSet, bool, error) {
	s.draftCalls++
	return store.ArtifactReadinessSet{}, false, errCJUnexpectedProviderCall
}

// cjPrepareStub is the controlled Standard boundary behind artifact
// drafting: it simulates Standard rendering (fixed marker bytes per
// request key) and commits through the real production store.
type cjPrepareStub struct {
	db                     *store.Store
	t                      *testing.T
	draftCalls, modelCalls int
}

func (s *cjPrepareStub) DraftOpportunityArtifacts(ctx context.Context, actor store.Actor, opportunityID, requestKey, expectedCheckID, expectedQuestionSetSHA256 string, expectedWorkflowRevision int64) (store.ArtifactReadinessSet, bool, error) {
	s.t.Helper()
	s.draftCalls++
	s.modelCalls++
	check, err := s.db.GetJobCheck(ctx, opportunityID, expectedCheckID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	if check.QuestionSetSHA256 != expectedQuestionSetSHA256 {
		return store.ArtifactReadinessSet{}, false, store.ErrConflict
	}
	workflow, err := s.db.RoleWorkflow(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	if workflow.Revision != expectedWorkflowRevision {
		return store.ArtifactReadinessSet{}, false, store.ErrConflict
	}
	answers, err := s.db.CurrentQuestionAnswers(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	refs := make([]store.ArtifactAnswerRef, 0, len(answers.Values))
	for _, value := range answers.Values {
		refs = append(refs, store.ArtifactAnswerRef{QuestionID: value.QuestionID, AnswerVersion: value.Version})
	}
	readiness, err := s.db.ArtifactReadiness(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	created := false
	for _, entry := range readiness.Entries {
		if !entry.Required || entry.State != store.ArtifactStateHeld || entry.Type == store.ArtifactFormValues {
			continue
		}
		_, wasCreated, err := s.db.SaveOpportunityArtifact(ctx, actor, opportunityID, store.ArtifactSaveInput{
			RequestKey: requestKey + "-" + entry.Type, ExpectedVersion: 0, Type: entry.Type,
			Content: "Standard draft (" + requestKey + ") for " + entry.Type + ".",
			Basis:   store.ArtifactBasis{AnswerRefs: refs},
		})
		if err != nil {
			return store.ArtifactReadinessSet{}, false, err
		}
		created = created || wasCreated
	}
	readiness, err = s.db.ArtifactReadiness(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	return readiness, created, nil
}

func cjInsertCapture(t *testing.T, db *store.Store, url string) store.SourceCapture {
	t.Helper()
	ctx := context.Background()
	var capture store.SourceCapture
	err := db.ResearchWrite(ctx, func(rdb store.ResearchDB) error {
		sum := sha256.Sum256([]byte(url))
		var err error
		capture, err = store.InsertSourceCapture(ctx, rdb, store.SourceCaptureInput{
			ContentSHA256: hex.EncodeToString(sum[:]), ArtifactRef: "artifact/cj-test",
			ByteLength: 2048, MediaType: "text/html", OriginalURL: url,
			Provenance: researchcontract.ProvenanceFetchedResponse, Completeness: store.CaptureComplete,
			Executor: researchcontract.ExecutorIdentity{Backend: "test-shell"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return capture
}

// cjCheckPerformer is the controlled Contributor check boundary behind
// POST .../checks. The hook simulates Contributor evidence capture; the
// production store writer (SaveJobCheckBody) persists it.
type cjCheckPerformer struct {
	calls      int
	modelCalls int
	save       func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error)
}

func (s *cjCheckPerformer) Authorized() bool { return true }

func (s *cjCheckPerformer) PerformCheck(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
	s.calls++
	s.modelCalls++
	return s.save(ctx, opportunityID, checkID)
}

func cjOwner() store.Actor { return store.Actor{Kind: "administrator", ID: "owner"} }

// cjResearchStub is the controlled Contributor/Jev provider boundary behind
// POST /api/v1/research/runs. It simulates the supervisor accepting a
// commission and doing provider work; production persistence and catalog
// authoring stay the production path's job (C2/D1), so this stub authors
// nothing itself.
type cjResearchStub struct {
	db            *store.Store
	commissions   int
	providerCalls int
	runID         string
	persistRound  bool
}

func (s *cjResearchStub) CommissionResearch(ctx context.Context, in CommissionResearchInput) (CommissionResearchOutput, error) {
	s.commissions++
	s.providerCalls++
	runID := s.runID
	if runID == "" {
		runID = "cj-run-1"
	}
	if s.persistRound {
		prefs, err := s.db.CurrentPreferences(ctx)
		if err != nil {
			return CommissionResearchOutput{}, err
		}
		round, _, err := s.db.StartRound(ctx, in.Actor, store.StartRoundInput{
			RequestKey: fmt.Sprintf("cj-commission-%d", s.commissions),
			Intent:     "Connected journey discovery commission.", Outcome: "research_run",
			ProfileVersion: prefs.Version,
			Scope: store.RoundScope{Resources: []string{"campaign:active"},
				Operations: []string{store.RoundCodexTurn, store.RoundJevRequest}},
			Limits:   store.RoundAllowance{Requests: 9, Items: 1, Tools: 3, Turns: 1},
			Deadline: time.Now().Add(30 * time.Minute).UTC()})
		if err != nil {
			return CommissionResearchOutput{}, err
		}
		if _, err := s.db.ActivateRound(ctx, in.Actor, round.ID); err != nil {
			return CommissionResearchOutput{}, err
		}
		runID = round.ID
	}
	return CommissionResearchOutput{View: generated.ResearchRunView{
		RunId: runID, State: generated.ResearchRunViewState("running"),
	}, Created: true}, nil
}

// cjRunControlStub routes stop/resume to the real durable round lifecycle;
// only the provider-side supervision is stubbed out.
type cjRunControlStub struct {
	db      *store.Store
	stops   int
	resumes int
}

func (s *cjRunControlStub) Stop(ctx context.Context, actor store.Actor, runID, _ string) (rounds.StopOutput, error) {
	s.stops++
	if _, _, err := s.db.StopRound(ctx, actor, runID); err != nil {
		return rounds.StopOutput{}, err
	}
	if _, err := s.db.PauseStoppedRound(ctx, actor, runID); err != nil {
		return rounds.StopOutput{}, err
	}
	return rounds.StopOutput{}, nil
}

func (s *cjRunControlStub) Resume(ctx context.Context, actor store.Actor, runID string) (rounds.ResumeOutput, error) {
	s.resumes++
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return rounds.ResumeOutput{}, err
	}
	if _, err := s.db.ResumeRound(ctx, actor, runID, round.Generation); err != nil {
		return rounds.ResumeOutput{}, err
	}
	return rounds.ResumeOutput{}, nil
}

func (s *cjResearchStub) SteerResearch(_ context.Context, _ SteerResearchInput) (generated.SteeringMessage, error) {
	return generated.SteeringMessage{}, store.ErrNotFound
}

func (s *cjResearchStub) ResearchRun(_ context.Context, _ store.Actor, _ string) (generated.ResearchRunView, error) {
	return generated.ResearchRunView{}, store.ErrNotFound
}

func (s *cjResearchStub) ResearchActivity(_ context.Context, _ store.Actor, _, _ string, _ int) (generated.ResearchActivityPage, error) {
	return generated.ResearchActivityPage{}, store.ErrNotFound
}

func (s *cjResearchStub) ResearchCapture(_ context.Context, _ store.Actor, _ string) (generated.ResearchCaptureView, error) {
	return generated.ResearchCaptureView{}, store.ErrNotFound
}

func (s *cjResearchStub) ResearchIdentity(_ context.Context, _ store.Actor, _, _ string) (generated.ResearchIdentityView, error) {
	return generated.ResearchIdentityView{}, store.ErrNotFound
}

func (s *cjResearchStub) ResearchReport(_ context.Context, _ store.Actor, _ string) (generated.ResearchReportView, error) {
	return generated.ResearchReportView{}, store.ErrNotFound
}

func TestConnectedJourney01CatalogLifecycle(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()

	response := h.request("GET", "/api/v1/research/brief", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("fresh brief: got %d, want 200", response.Code)
	}
	var brief struct {
		ProfileVersion int64   `json:"profileVersion"`
		CatalogVersion *string `json:"catalogVersion"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &brief); err != nil {
		t.Fatal(err)
	}
	if brief.ProfileVersion < 1 {
		t.Fatalf("fresh brief has no profile version: %+v", brief)
	}
	if brief.CatalogVersion != nil {
		t.Fatalf("fresh DB must start with no catalog, got %q", *brief.CatalogVersion)
	}

	stub := &cjResearchStub{}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Research: stub})

	response = h.request("POST", "/api/v1/research/runs", `{"briefText":"backend roles, remote"}`, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("first commission: got %d %s, want 201", response.Code, response.Body.String())
	}
	if stub.commissions != 1 {
		t.Fatalf("commission did not reach the provider boundary: %d", stub.commissions)
	}

	// C2: the first explicit commission authors the catalog for the current
	// saved goal version (R01: no production caller authors it today).
	response = h.request("GET", "/api/v1/research/briefs/"+strconv.FormatInt(brief.ProfileVersion, 10)+"/catalog", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("R01: catalog for brief version %d after first explicit commission: got %d, want 200 (commission must author catalog + classify)",
			brief.ProfileVersion, response.Code)
	}
	// Follow-on once authored: a second run reuses the same catalog version,
	// and a new goal version authors exactly one new catalog.
}

func TestConnectedJourney02AnswerCommit(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "cj2-select")
	url := "https://harbour.example/jobs/backend-2"
	capture := cjInsertCapture(t, h.db, url)

	questionTexts := []string{
		"Describe your remote work setup and availability.",
		"Why do you want this role at Harbour Systems?",
		"Anything else you want to share?",
		"Summarize your relevant backend experience for this vacancy.",
	}
	required := []string{store.CheckRequired, store.CheckRequired, store.CheckOptional, store.CheckRequired}
	performer := &cjCheckPerformer{save: func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
		questions := make([]store.CheckQuestionInput, 0, len(questionTexts))
		for i, text := range questionTexts {
			questions = append(questions, store.CheckQuestionInput{Text: text, Required: required[i],
				Kind:          store.CheckQuestionFreeText,
				SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 100 + i*100, End: 140 + i*100},
				SourceExcerpt: text})
		}
		return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
			OpportunityID: opportunityID, CheckID: checkID,
			Vacancy: store.CheckVacancyInput{CaptureIDs: []string{capture.ID},
				Completeness: store.CaptureComplete, SourceURL: url, RetrievedAt: "2026-09-24T11:05:00Z"},
			Route: store.CheckRouteInput{Kind: store.CheckRouteDirect,
				DestinationText: "jobs@example.invalid", Judgment: store.CheckRouteJudgmentApplication,
				SourceExcerpt: "Send your CV to jobs@example.invalid", ObservedAt: "2026-09-24T11:30:00Z"},
			RequestedDocuments: []store.RequestedDocumentInput{},
			Requirements:       []store.CheckRequirementInput{},
			Gaps:               []store.CheckGapInput{},
			Questions:          questions,
		})
	}}
	matcher := &cjJevMatcher{db: h.db, t: t, choices: map[string]string{}}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, MuseCheck: performer, AnswerMatcher: matcher})

	start := fmt.Sprintf(`{"requestKey":"cj2-start","expectedOpportunityRevision":%d,"expectedWorkflowRevision":0}`, opportunity.Revision)
	response := h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/checks", start, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("check start: got %d %s, want 201", response.Code, response.Body.String())
	}
	var started struct {
		Status string `json:"status"`
		Check  struct {
			ID                string `json:"id"`
			QuestionSetSHA256 string `json:"questionSetSha256"`
			Questions         []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"questions"`
		} `json:"check"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Status != "checked" || len(started.Check.Questions) != 4 {
		t.Fatalf("checked view: %+v", started)
	}
	questionIDs := map[string]string{}
	for _, question := range started.Check.Questions {
		questionIDs[question.Text] = question.ID
	}
	for _, text := range questionTexts {
		if questionIDs[text] == "" {
			t.Fatalf("question %q missing from %+v", text, started.Check.Questions)
		}
	}

	// One owner-approved library answer overlapping q1/q2 wording.
	approvedText := "I work remotely from Example City with full daytime availability."
	response = h.request("POST", "/api/v1/answers",
		`{"requestKey":"cj2-approve-1","text":"`+approvedText+`","scopeTags":["remote","work","availability","role","harbour"],"contextNote":"remote work setup and availability; role motivation at harbour"}`,
		cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("approve answer: got %d %s, want 201", response.Code, response.Body.String())
	}
	var approved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &approved); err != nil {
		t.Fatal(err)
	}
	matcher.choices[questionIDs[questionTexts[0]]] = approved.ID
	matcher.choices[questionIDs[questionTexts[1]]] = approved.ID

	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/answers/match",
		`{"requestKey":"cj2-match-1","expectedCheckId":"`+started.Check.ID+`","expectedQuestionSetSha256":"`+started.Check.QuestionSetSHA256+`"}`,
		cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("jev match: got %d %s, want 201", response.Code, response.Body.String())
	}
	if matcher.calls != 1 {
		t.Fatalf("match must judge exactly one batch, got %d calls", matcher.calls)
	}

	put := func(questionID, body string) (int, map[string]any) {
		t.Helper()
		response := h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/questions/"+questionID+"/answer",
			body, cookie, "", csrf, origin)
		var value map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &value)
		return response.Code, value
	}
	// Untouched suggestion kept: the exact suggested text persists as an
	// explicit saved choice (R04 backend side).
	code, kept := put(questionIDs[questionTexts[0]], `{"expectedAnswerVersion":0,"text":"`+approvedText+`"}`)
	if code != 200 {
		t.Fatalf("keep suggestion: got %d %v, want 200", code, kept)
	}
	provenance, _ := kept["provenance"].(map[string]any)
	if kept["version"] != float64(1) || kept["state"] != "answered" || provenance["origin"] != "jev_suggestion" {
		t.Fatalf("kept suggestion must persist v1 answered jev_suggestion: %v", kept)
	}
	// Edited answer persists with owner-edited provenance.
	code, edited := put(questionIDs[questionTexts[1]], `{"expectedAnswerVersion":0,"text":"Harbour Systems builds the scheduling platform I want to work on."}`)
	if code != 200 {
		t.Fatalf("edit answer: got %d %v, want 200", code, edited)
	}
	editedProvenance, _ := edited["provenance"].(map[string]any)
	if editedProvenance["origin"] != "owner_edited" {
		t.Fatalf("edited answer must persist owner_edited: %v", edited)
	}
	// Optional blank is an explicit saved choice.
	code, blank := put(questionIDs[questionTexts[2]], `{"expectedAnswerVersion":0,"text":""}`)
	if code != 200 || blank["state"] != "blank" {
		t.Fatalf("optional blank: got %d %v, want 200 blank", code, blank)
	}

	// C4: a required blank with an explicit draft_requested choice is
	// committable (Standard drafts from verified facts). R03: the current
	// API has no such choice and the commit requires nonblank text.
	code, draft := put(questionIDs[questionTexts[3]], `{"expectedAnswerVersion":0,"text":"","draftRequested":true}`)
	if code != 200 {
		t.Fatalf("R03: required draft-request blank: got %d %v, want 200 (no draft_requested choice exists; commit demands nonblank text)",
			code, draft)
	}
	// Follow-on once expressible: POST .../answers/commit advances to
	// answered; stale/cross-job writes conflict with current versions.
}

func TestConnectedJourney03QuestionlessRoute(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()

	vacancy := func(capture store.SourceCapture, url string) store.CheckVacancyInput {
		return store.CheckVacancyInput{CaptureIDs: []string{capture.ID},
			Completeness: store.CaptureComplete, SourceURL: url, RetrievedAt: "2026-09-24T11:05:00Z"}
	}
	route := store.CheckRouteInput{Kind: store.CheckRouteDirect,
		DestinationText: "jobs@example.invalid", Judgment: store.CheckRouteJudgmentApplication,
		SourceExcerpt: "Send your CV to jobs@example.invalid", ObservedAt: "2026-09-24T11:30:00Z"}

	// Unresolved questions stay held with retained findings.
	unresolvedURL := "https://harbour.example/jobs/unresolved"
	unresolvedCapture := cjInsertCapture(t, h.db, unresolvedURL)
	unresolvedOpp := createCheckedOpportunity(t, h, "cj3-unresolved")
	held := &cjCheckPerformer{save: func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
		return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
			OpportunityID: opportunityID, CheckID: checkID,
			Vacancy: vacancy(unresolvedCapture, unresolvedURL), Route: route,
			RequestedDocuments: []store.RequestedDocumentInput{},
			Requirements:       []store.CheckRequirementInput{},
			Gaps:               []store.CheckGapInput{},
			Blocked: &store.CheckBlockedInput{Code: store.CheckBlockedQuestionsUnresolved,
				Detail: "application form behind login; questions not verified"},
		})
	}}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, MuseCheck: held})
	start := fmt.Sprintf(`{"requestKey":"cj3-start-unresolved","expectedOpportunityRevision":%d,"expectedWorkflowRevision":0}`, unresolvedOpp.Revision)
	response := h.request("POST", "/api/v1/opportunities/"+unresolvedOpp.ID+"/checks", start, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("unresolved start: got %d %s, want 201", response.Code, response.Body.String())
	}
	var heldView struct {
		Status string `json:"status"`
		Check  struct {
			Status        string `json:"status"`
			BlockedReason *struct {
				Code string `json:"code"`
			} `json:"blockedReason"`
			Route struct {
				DestinationText *string `json:"destinationText"`
			} `json:"route"`
		} `json:"check"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &heldView); err != nil {
		t.Fatal(err)
	}
	if heldView.Status != "blocked" || heldView.Check.BlockedReason == nil ||
		heldView.Check.BlockedReason.Code != "questions_unresolved" {
		t.Fatalf("unresolved must stay held: %+v", heldView)
	}
	response = h.request("POST", "/api/v1/opportunities/"+unresolvedOpp.ID+"/answers/commit", "", cookie, "", csrf, origin)
	if response.Code == 200 {
		t.Fatal("unresolved check must not commit answers")
	}

	// A verified questionless email route completes and can proceed (C3/R06).
	questionlessURL := "https://harbour.example/jobs/email-apply"
	questionlessCapture := cjInsertCapture(t, h.db, questionlessURL)
	questionlessOpp := createCheckedOpportunity(t, h, "cj3-questionless")
	verified := &cjCheckPerformer{save: func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
		return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
			OpportunityID: opportunityID, CheckID: checkID,
			Vacancy: vacancy(questionlessCapture, questionlessURL), Route: route,
			RequestedDocuments: []store.RequestedDocumentInput{},
			Requirements:       []store.CheckRequirementInput{},
			Gaps:               []store.CheckGapInput{},
			Questions:          []store.CheckQuestionInput{},
			// The simulated performer verified the email route carries
			// zero employer questions (mirrors DecideCheckVerdict).
			QuestionsNoneVerified: true,
		})
	}}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, MuseCheck: verified})
	start = fmt.Sprintf(`{"requestKey":"cj3-start-questionless","expectedOpportunityRevision":%d,"expectedWorkflowRevision":0}`, questionlessOpp.Revision)
	response = h.request("POST", "/api/v1/opportunities/"+questionlessOpp.ID+"/checks", start, cookie, "", csrf, origin)
	var doneView struct {
		Status string `json:"status"`
		Check  struct {
			Status    string `json:"status"`
			Questions []any  `json:"questions"`
		} `json:"check"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &doneView)
	if response.Code != 201 || doneView.Status != "checked" || len(doneView.Check.Questions) != 0 {
		t.Fatalf("R06: verified questionless email route: got %d %+v, want 201 checked with zero questions",
			response.Code, doneView)
	}
	// Follow-on once completable: empty-set answer commit, then preparation.
}

func TestConnectedJourney04ArtifactIdentity(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	ctx := context.Background()
	opportunity := createCheckedOpportunity(t, h, "cj4-select")
	url := "https://harbour.example/jobs/backend-4"
	capture := cjInsertCapture(t, h.db, url)
	questionText := "Why do you want this role?"
	performer := &cjCheckPerformer{save: func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
		return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
			OpportunityID: opportunityID, CheckID: checkID,
			Vacancy: store.CheckVacancyInput{CaptureIDs: []string{capture.ID},
				Completeness: store.CaptureComplete, SourceURL: url, RetrievedAt: "2026-09-24T11:05:00Z"},
			Route: store.CheckRouteInput{Kind: store.CheckRouteDirect,
				DestinationText: "jobs@example.invalid", Judgment: store.CheckRouteJudgmentApplication,
				SourceExcerpt: "Send your motivation email to jobs@example.invalid", ObservedAt: "2026-09-24T11:30:00Z"},
			RequestedDocuments: []store.RequestedDocumentInput{},
			Requirements:       []store.CheckRequirementInput{},
			Gaps:               []store.CheckGapInput{},
			Questions: []store.CheckQuestionInput{{Text: questionText, Required: store.CheckRequired,
				Kind:          store.CheckQuestionFreeText,
				SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 100, End: 140},
				SourceExcerpt: questionText}},
		})
	}}
	materials := &cjPrepareStub{db: h.db, t: t}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, MuseCheck: performer, Materials: materials})

	start := fmt.Sprintf(`{"requestKey":"cj4-start","expectedOpportunityRevision":%d,"expectedWorkflowRevision":0}`, opportunity.Revision)
	response := h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/checks", start, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("check start: got %d %s, want 201", response.Code, response.Body.String())
	}
	var started struct {
		Check struct {
			ID                string `json:"id"`
			QuestionSetSHA256 string `json:"questionSetSha256"`
			Questions         []struct {
				ID string `json:"id"`
			} `json:"questions"`
		} `json:"check"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	questionID := started.Check.Questions[0].ID
	response = h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/questions/"+questionID+"/answer",
		`{"expectedAnswerVersion":0,"text":"I want this role for its scheduling platform."}`, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("answer: got %d %s, want 200", response.Code, response.Body.String())
	}
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/answers/commit", "", cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("commit: got %d %s, want 200", response.Code, response.Body.String())
	}
	workflow, err := h.db.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}

	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/draft",
		fmt.Sprintf(`{"requestKey":"cj4-draft-1","expectedCheckId":%q,"expectedQuestionSetSha256":%q,"expectedWorkflowRevision":%d}`,
			started.Check.ID, started.Check.QuestionSetSHA256, workflow.Revision),
		cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("draft: got %d %s, want 201", response.Code, response.Body.String())
	}
	var drafted struct {
		Entries []struct {
			Type     string `json:"type"`
			State    string `json:"state"`
			Required bool   `json:"required"`
			Current  *struct {
				Content string `json:"content"`
				Version int64  `json:"version"`
			} `json:"current"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &drafted); err != nil {
		t.Fatal(err)
	}
	var email *struct {
		State   string
		Content string
		Version int64
	}
	for _, entry := range drafted.Entries {
		if entry.Type == "email_body" && entry.Current != nil {
			found := struct {
				State   string
				Content string
				Version int64
			}{entry.State, entry.Current.Content, entry.Current.Version}
			email = &found
		}
	}
	if email == nil || email.State != "ready" || email.Version != 1 || !strings.Contains(email.Content, "cj4-draft-1") {
		t.Fatalf("drafted email_body: %+v", drafted.Entries)
	}
	// An exact edit updates the same artifact identity with literal text.
	response = h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/email_body",
		`{"requestKey":"cj4-edit-1","expectedVersion":1,"content":"Owner literal email text.","basis":{"factIds":[],"answerRefs":[],"checkSpans":[]}}`,
		cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("exact edit: got %d %s, want 201", response.Code, response.Body.String())
	}
	// Readback, list, and Handoff read agree on the same current version.
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/email_body", "", cookie, "", "", "")
	var current struct {
		State   string `json:"state"`
		Current *struct {
			Content string `json:"content"`
			Version int64  `json:"version"`
		} `json:"current"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &current)
	if response.Code != 200 || current.State != "ready" || current.Current == nil || current.Current.Content != "Owner literal email text." || current.Current.Version != 2 {
		t.Fatalf("R08: artifact readback after draft+edit: got %d state %q current %+v, want 200 ready v2 literal (one current set)",
			response.Code, current.State, current.Current)
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts", "", cookie, "", "", "")
	var listed struct {
		Entries []struct {
			Type    string `json:"type"`
			Current *struct {
				Content string `json:"content"`
				Version int64  `json:"version"`
			} `json:"current"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &listed)
	listedOK := false
	for _, entry := range listed.Entries {
		if entry.Type == "email_body" && entry.Current != nil && entry.Current.Content == "Owner literal email text." && entry.Current.Version == 2 {
			listedOK = true
		}
	}
	if response.Code != 200 || !listedOK {
		t.Fatalf("R08: artifact list after draft+edit: got %d %+v, want email_body v2 literal (one current set)", response.Code, listed.Entries)
	}
	// The second preparation truth is gone: no pack endpoint answers.
	for _, probe := range [][2]string{
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/materials/prepare"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/materials/rewrite"},
		{"GET", "/api/v1/application-packs/cj4-no-such-pack"},
	} {
		response = h.request(probe[0], probe[1], `{}`, cookie, "", csrf, origin)
		if response.Code != 404 {
			t.Fatalf("R08: removed pack endpoint %s %s: got %d, want 404", probe[0], probe[1], response.Code)
		}
	}
}
func TestConnectedJourney05StaleInvalidation(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "cj5-select")
	url := "https://harbour.example/jobs/backend-5"
	capture := cjInsertCapture(t, h.db, url)
	questionText := "Describe your remote work setup."
	performer := &cjCheckPerformer{save: func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
		return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
			OpportunityID: opportunityID, CheckID: checkID,
			Vacancy: store.CheckVacancyInput{CaptureIDs: []string{capture.ID},
				Completeness: store.CaptureComplete, SourceURL: url, RetrievedAt: "2026-09-24T11:05:00Z"},
			Route: store.CheckRouteInput{Kind: store.CheckRouteDirect,
				DestinationText: "jobs@example.invalid", Judgment: store.CheckRouteJudgmentApplication,
				SourceExcerpt: "Send your CV to jobs@example.invalid", ObservedAt: "2026-09-24T11:30:00Z"},
			RequestedDocuments: []store.RequestedDocumentInput{{Label: "CV", Required: true,
				SourceExcerpt: "Send your CV",
				SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 10, End: 22}}},
			Requirements: []store.CheckRequirementInput{},
			Gaps:         []store.CheckGapInput{},
			Questions: []store.CheckQuestionInput{{Text: questionText, Required: store.CheckRequired,
				Kind:          store.CheckQuestionFreeText,
				SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 100, End: 140},
				SourceExcerpt: questionText}},
		})
	}}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, MuseCheck: performer})

	start := fmt.Sprintf(`{"requestKey":"cj5-start","expectedOpportunityRevision":%d,"expectedWorkflowRevision":0}`, opportunity.Revision)
	response := h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/checks", start, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("check start: got %d %s, want 201", response.Code, response.Body.String())
	}
	var started struct {
		Check struct {
			Questions []struct {
				ID string `json:"id"`
			} `json:"questions"`
		} `json:"check"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	questionID := started.Check.Questions[0].ID

	response = h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/questions/"+questionID+"/answer",
		`{"expectedAnswerVersion":0,"text":"I work remotely from Example City."}`, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("answer v1: got %d %s, want 200", response.Code, response.Body.String())
	}
	response = h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv",
		`{"requestKey":"cj5-cv-1","expectedVersion":0,"content":"CV naming Example City.","basis":{"factIds":[],"answerRefs":[{"questionId":"`+questionID+`","answerVersion":1}],"checkSpans":[]}}`,
		cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("artifact v1: got %d %s, want 201", response.Code, response.Body.String())
	}
	readiness := func() map[string]map[string]any {
		t.Helper()
		response := h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts", "", cookie, "", "", "")
		if response.Code != 200 {
			t.Fatalf("readiness: got %d, want 200", response.Code)
		}
		var set struct {
			Entries []map[string]any `json:"entries"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &set); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, entry := range set.Entries {
			entryType, _ := entry["type"].(string)
			out[entryType] = entry
		}
		return out
	}
	if state := readiness()["cv"]["state"]; state != "ready" {
		t.Fatalf("cv readiness before input change: got %v, want ready", state)
	}

	// The supporting answer changes: the pinned-basis artifact is stale.
	response = h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/questions/"+questionID+"/answer",
		`{"expectedAnswerVersion":1,"text":"I work remotely from Harbor Town."}`, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("answer v2: got %d %s, want 200", response.Code, response.Body.String())
	}
	if state := readiness()["cv"]["state"]; state == "ready" {
		t.Errorf("R07: cv still ready after its answer moved v1->v2, want held/outdated (readiness ignores basis versions)")
	}
	// Prior content stays readable under staleness.
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv", "", cookie, "", "", "")
	var current struct {
		Current *struct {
			Content string `json:"content"`
			Version int64  `json:"version"`
		} `json:"current"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &current)
	if response.Code != 200 || current.Current == nil || current.Current.Content != "CV naming Example City." {
		t.Fatalf("prior artifact content must stay readable: got %d %+v", response.Code, current)
	}

	// R23: the same request key with changed content/basis must conflict,
	// not replay success.
	response = h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv",
		`{"requestKey":"cj5-cv-1","expectedVersion":1,"content":"CV naming Harbor Town.","basis":{"factIds":[],"answerRefs":[{"questionId":"`+questionID+`","answerVersion":2}],"checkSpans":[]}}`,
		cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("R23: changed-payload replay of key cj5-cv-1: got %d, want 409 (store replays without comparing content/basis)",
			response.Code)
	}
}

func TestConnectedJourney06VacancyIdentity(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	ctx := context.Background()

	collect := func(url, title string) store.Opportunity {
		t.Helper()
		// Same production record path the classifier uses (R12): always a
		// fresh company + opportunity, no verified-source reconciliation.
		company, _, err := h.db.CreateCompany(ctx, cjOwner(), store.CompanyInput{Name: "Harbour Systems"})
		if err != nil {
			t.Fatal(err)
		}
		opportunity, _, err := h.db.CreateOpportunity(ctx, cjOwner(), store.OpportunityInput{
			CompanyID: company.ID, Title: title, Kind: "employment",
			SourceURL: url, OriginalText: "Build Go services.", Stage: "new", WorkPattern: "hybrid",
		})
		if err != nil {
			t.Fatal(err)
		}
		return opportunity
	}
	sourceIDs := func(sourceURL string) []string {
		t.Helper()
		response := h.request("GET", "/api/v1/opportunities", "", cookie, "", "", "")
		if response.Code != 200 {
			t.Fatalf("list opportunities: got %d", response.Code)
		}
		var page struct {
			Items []struct {
				Opportunity struct {
					ID        string `json:"id"`
					SourceURL string `json:"sourceUrl"`
				} `json:"opportunity"`
			} `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, item := range page.Items {
			if item.Opportunity.SourceURL == sourceURL {
				ids = append(ids, item.Opportunity.ID)
			}
		}
		return ids
	}

	sameURL := "https://harbour.example/jobs/backend-1"
	first := collect(sameURL, "Backend Engineer")
	if _, _, err := h.db.SetOwnerOpportunityDecision(ctx, cjOwner(), first.ID,
		store.OwnerDecisionInput{RequestKey: "cj6-select", ExpectedOpportunityRevision: first.Revision,
			ExpectedDecisionRevision: 0, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	// Find more recollects the same verified vacancy.
	second := collect(sameURL, "Backend Engineer")

	// A genuinely different vacancy stays distinct.
	other := collect("https://harbour.example/jobs/frontend-9", "Frontend Engineer")
	if other.ID == first.ID || other.ID == second.ID {
		t.Fatalf("different vacancy merged: %q vs %q/%q", other.ID, first.ID, second.ID)
	}

	// D2: the same verified vacancy keeps ONE identity + links across passes.
	ids := sourceIDs(sameURL)
	if len(ids) != 1 {
		t.Fatalf("R12: same verified vacancy %q has %d identities %v, want 1 (recollect must reconcile, not fork)",
			sameURL, len(ids), ids)
	}
}

func TestConnectedJourney07RunRecovery(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	research := &cjResearchStub{db: h.db, persistRound: true}
	control := &cjRunControlStub{db: h.db}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Research: research, ResearchControl: control})

	response := h.request("POST", "/api/v1/research/runs", `{"briefText":"backend roles"}`, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("commission: got %d %s, want 201", response.Code, response.Body.String())
	}
	var commissioned struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &commissioned); err != nil {
		t.Fatal(err)
	}
	if commissioned.RunID == "" {
		t.Fatalf("commission returned no run id: %s", response.Body.String())
	}
	response = h.request("POST", "/api/v1/rounds/"+commissioned.RunID+"/stop", "", cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("stop: got %d %s, want 200", response.Code, response.Body.String())
	}

	// Second browser context: fresh session, no local run state, same server.
	h2 := &harness{t: t, db: h.db, service: h.service,
		handler: NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Research: research, ResearchControl: control})}
	cookie2, csrf2 := h2.login()
	response = h2.request("GET", "/api/v1/rounds/"+commissioned.RunID, "", cookie2, "", "", "")
	if response.Code != 200 {
		t.Fatalf("deep-link run read: got %d, want 200", response.Code)
	}
	// A paused run is recoverable through the active read.
	response = h2.request("GET", "/api/v1/rounds/active", "", cookie2, "", "", "")
	var active struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || active.ID != commissioned.RunID {
		t.Fatalf("paused run recovery via active: got %d %q, want 200 %q", response.Code, active.ID, commissioned.RunID)
	}
	// Resume continues the same durable identity.
	response = h2.request("POST", "/api/v1/rounds/"+commissioned.RunID+"/resume", "", cookie2, "", csrf2, origin)
	if response.Code != 200 {
		t.Fatalf("resume: got %d %s, want 200", response.Code, response.Body.String())
	}
	var resumed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.ID != commissioned.RunID {
		t.Fatalf("resume forked identity: got %q, want %q", resumed.ID, commissioned.RunID)
	}
	// The run ends failed; durable server state keeps it.
	if _, err := h.db.FinishRound(context.Background(), cjOwner(), commissioned.RunID,
		store.RoundFailed, "contributor_error", "research_run", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	response = h2.request("GET", "/api/v1/rounds/active", "", cookie2, "", "", "")
	if response.Code == 200 {
		t.Fatalf("failed run must not surface as the active round: %s", response.Body.String())
	}
	// C1 error shape for unknown runs.
	response = h2.request("GET", "/api/v1/rounds/cj-no-such-run", "", cookie2, "", "", "")
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &envelope)
	if response.Code != 404 || envelope.Error.Code != "run_not_found" {
		t.Errorf("C1: unknown run: got %d code %q, want 404 run_not_found", response.Code, envelope.Error.Code)
	}

	// C1: the server exposes a relevant-run lookup (active/paused/stopped/
	// failed/completed/empty) so a fresh context recovers the failed run
	// without localStorage and without guessing its id.
	response = h2.request("GET", "/api/v1/rounds", "", cookie2, "", "", "")
	if response.Code != 200 {
		t.Fatalf("R13: server relevant-run lookup: got %d, want 200 listing the failed run (second context cannot discover it)",
			response.Code)
	}
}

func TestConnectedJourney08ZeroProviderReads(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "cj8-select")
	research := &cjResearchStub{}
	control := &cjRunControlStub{db: h.db}
	matcher := &cjAnswerMatcher{}
	materials := &cjMaterialStub{}
	performer := &cjCheckPerformer{save: func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
		return store.CheckView{}, errCJUnexpectedProviderCall
	}}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin},
		Research: research, ResearchControl: control, AnswerMatcher: matcher,
		Materials: materials, MuseCheck: performer})

	reads := []string{
		"/api/v1/research/brief",
		"/api/v1/research/briefs/1/catalog",
		"/api/v1/research/runs/cj-run-1/findings",
		"/api/v1/opportunities/" + opportunity.ID + "/finding",
		"/api/v1/opportunities/" + opportunity.ID + "/answers/match/current",
		"/api/v1/opportunities/" + opportunity.ID + "/answers/current",
		"/api/v1/opportunities/" + opportunity.ID + "/checks/current",
		"/api/v1/opportunities/" + opportunity.ID + "/artifacts",
		"/api/v1/opportunities/" + opportunity.ID + "/artifacts/cv",
		"/api/v1/opportunities/" + opportunity.ID + "/artifacts/email_body",
		"/api/v1/opportunities/" + opportunity.ID + "/workflow",
		"/api/v1/rounds/active",
		"/api/v1/rounds/cj-no-such-run",
		"/api/v1/muse/readiness?tier=contributor",
		"/api/v1/codex/status",
	}
	for _, path := range reads {
		h.request("GET", path, "", cookie, "", "", "")
	}
	// An exact artifact edit is literal owner text with zero model calls.
	response := h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv",
		`{"requestKey":"cj8-exact-1","expectedVersion":0,"content":"CV text, owned by the owner.","basis":{"factIds":[],"answerRefs":[],"checkSpans":[]}}`,
		cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("exact artifact edit: got %d %s, want 201", response.Code, response.Body.String())
	}
	if research.providerCalls != 0 || matcher.calls != 0 || materials.modelCalls != 0 ||
		materials.draftCalls != 0 || performer.modelCalls != 0 {
		t.Fatalf("reads/Why/exact edit caused provider work: research=%d match=%d materials=%+v check=%d",
			research.providerCalls, matcher.calls, materials, performer.modelCalls)
	}

	// No employer-action endpoint is reachable: the app never fills,
	// attaches, sends or submits to employers.
	employerActions := [][2]string{
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/deliveries"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/send"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/submit"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/apply"},
		{"POST", "/api/v1/application-packs/cj-no-such-pack/send"},
	}
	for _, probe := range employerActions {
		response := h.request(probe[0], probe[1], `{}`, cookie, "", csrf, origin)
		if response.Code != 404 {
			t.Fatalf("employer action %s %s reachable: got %d, want 404", probe[0], probe[1], response.Code)
		}
	}

	// R30 residue: the rejected session ceremony is still registered and
	// must be removed (direct CLI invocation only, per product vision).
	response = h.request("POST", "/api/v1/codex/connect", `{}`, cookie, "", csrf, origin)
	if response.Code != 404 {
		t.Fatalf("R30: POST /api/v1/codex/connect still registered: got %d, want 404 (remove session ceremony)",
			response.Code)
	}
	response = h.request("GET", "/api/v1/codex/mcp", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("R30: /api/v1/codex/mcp still registered: got %d, want 404 (remove session ceremony)", response.Code)
	}
}
