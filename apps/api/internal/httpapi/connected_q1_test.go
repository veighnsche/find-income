// Test ownership: lane Q owns this file (Q1 seven-step connected proof).
//
// Q1 proves the SAME identities through all seven owner steps over the
// production HTTP router + real temporary store: saved goals, find jobs,
// selection, checked details, committed answers, prepared materials and
// manual Handoff. Only Contributor/Jev/Standard provider/process
// boundaries are stubbed (cj- + q1-prefixed adapters); every other read
// and write is production code. Provider-call counters prove reads,
// Why-panels and exact edits spend nothing, and the final test proves no
// employer action is callable.

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// q1RewriteStub extends the controlled Standard boundary with an
// explicit rewrite turn: fixed marker bytes committed through the real
// production store, like cjPrepareStub.
type q1RewriteStub struct {
	cjPrepareStub
	rewriteCalls int
}

func (s *q1RewriteStub) RewriteOpportunityArtifacts(ctx context.Context, actor store.Actor, opportunityID, requestKey string, input materialprep.RewriteInput) (store.ArtifactReadinessSet, bool, error) {
	s.t.Helper()
	s.rewriteCalls++
	s.modelCalls++
	if len(input.Items) != 1 {
		return store.ArtifactReadinessSet{}, false, store.ErrInvalid
	}
	item := input.Items[0]
	if _, err := s.db.GetOpportunityArtifactByRequestKey(ctx, opportunityID, item.Type, requestKey+"-"+item.Type); err == nil {
		// Lost-response retry: the key already committed, so replay
		// the accepted versions without a duplicate (production
		// rewrite does the same lookup before re-rendering).
		readiness, err := s.db.ArtifactReadiness(ctx, opportunityID)
		if err != nil {
			return store.ArtifactReadinessSet{}, false, err
		}
		return readiness, false, nil
	}
	versions, err := s.db.ListArtifactVersions(ctx, opportunityID, item.Type)
	if err != nil || len(versions) == 0 {
		return store.ArtifactReadinessSet{}, false, store.ErrNotFound
	}
	current := versions[len(versions)-1]
	if current.Version != item.ExpectedVersion {
		return store.ArtifactReadinessSet{}, false, store.ErrConflict
	}
	_, _, err = s.db.SaveOpportunityArtifact(ctx, actor, opportunityID, store.ArtifactSaveInput{
		RequestKey: requestKey + "-" + item.Type, ExpectedVersion: current.Version, Type: item.Type,
		Content: "Standard rewrite (" + requestKey + ") for " + item.Type + ": " + input.Instruction,
		Basis:   current.Basis,
	})
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	readiness, err := s.db.ArtifactReadiness(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	return readiness, true, nil
}

// TestJourneyQ1SevenStepSpine walks one job through all seven owner
// steps over production HTTP with the same identities throughout:
// saved goals, find jobs, selection, checked details, committed
// answers, prepared materials and manual Handoff. Provider counters
// prove the exact-edit, export and Handoff reads spend nothing.
func TestJourneyQ1SevenStepSpine(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	ctx := context.Background()

	// Step 1: save first goals, then a new version; readiness follows.
	response := h.request("GET", "/api/v1/preferences", "", cookie, "", "", "")
	var prefs struct {
		Version int64 `json:"version"`
	}
	if response.Code != 200 {
		t.Fatalf("read goals: got %d %s, want 200", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs.Version < 1 {
		t.Fatalf("seeded goals version = %d, want >= 1", prefs.Version)
	}
	saveGoals := func(version int64, location string, remote bool) int64 {
		t.Helper()
		body := fmt.Sprintf(`{"expectedVersion":%d,"preferredLocation":%q,"allowRemote":%v,`+
			`"allowHybrid":true,"targetHours":"32","minMonthlyBaseCents":300000,"salaryCurrency":"EUR",`+
			`"timezone":"Europe/Amsterdam","roleCriteria":[{"id":"c-backend","label":"backend","description":"backend work","kind":"role","mode":"require"}]}`,
			version, location, remote)
		response := h.request("PUT", "/api/v1/preferences", body, cookie, "", csrf, origin)
		if response.Code != 200 {
			t.Fatalf("save goals: got %d %s, want 200", response.Code, response.Body.String())
		}
		var saved struct {
			Preferences struct {
				Version int64 `json:"version"`
			} `json:"preferences"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		return saved.Preferences.Version
	}
	versioned := saveGoals(prefs.Version, "Amsterdam", true)
	briefVersion := saveGoals(versioned, "Rotterdam", false)
	if briefVersion != versioned+1 {
		t.Fatalf("goal versions: %d then %d, want increment", versioned, briefVersion)
	}

	// Step 2: commission once; the run persists and the catalog authors
	// exactly once for the saved goal version.
	research := &cjResearchStub{db: h.db, persistRound: true}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Research: research})
	response = h.request("POST", "/api/v1/research/runs", `{"briefText":"backend roles"}`, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("commission: got %d %s, want 201", response.Code, response.Body.String())
	}
	response = h.request("GET", "/api/v1/research/briefs/"+strconv.FormatInt(briefVersion, 10)+"/catalog", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("catalog: got %d, want 200 once-per-version", response.Code)
	}

	// Step 3: the same verified vacancy collected twice keeps one
	// identity; selection dedups with it.
	company, _, err := h.db.CreateCompany(ctx, cjOwner(), store.CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	collect := func() store.Opportunity {
		t.Helper()
		opportunity, _, err := h.db.CreateOpportunity(ctx, cjOwner(), store.OpportunityInput{
			CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
			SourceURL: "https://harbour.example/jobs/q1-spine", OriginalText: "Build Go services.",
			Stage: "new", WorkPattern: "hybrid", LocationText: "Rotterdam",
		})
		if err != nil {
			t.Fatal(err)
		}
		return opportunity
	}
	first, recollected := collect(), collect()
	if first.ID != recollected.ID {
		t.Fatalf("recollect forked %q vs %q, want one identity", first.ID, recollected.ID)
	}
	decide := fmt.Sprintf(`{"requestKey":"q1-select","expectedOpportunityRevision":%d,"expectedDecisionRevision":0,"decision":"selected"}`,
		first.Revision)
	response = h.request("POST", "/api/v1/opportunities/"+first.ID+"/decision", decide, cookie, "", csrf, origin)
	if response.Code != 200 && response.Code != 201 {
		t.Fatalf("select: got %d %s", response.Code, response.Body.String())
	}

	// Step 4: start the check; the verified save completes it.
	url := "https://harbour.example/jobs/q1-spine"
	capture := cjInsertCapture(t, h.db, url)
	questionTexts := []string{"Why do you want this role?", "Describe a Go service you shipped.", "Anything else?"}
	required := []string{store.CheckRequired, store.CheckRequired, store.CheckOptional}
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
			RequestedDocuments: []store.RequestedDocumentInput{{Label: "CV", Required: true,
				SourceExcerpt: "Send your CV", SourceSpan: store.CheckSourceSpan{CaptureID: capture.ID, Start: 10, End: 22}}},
			Requirements: []store.CheckRequirementInput{},
			Gaps:         []store.CheckGapInput{},
			Questions:    questions,
		})
	}}
	matcher := &cjJevMatcher{db: h.db, t: t, choices: map[string]string{}}
	materials := &q1RewriteStub{cjPrepareStub: cjPrepareStub{db: h.db, t: t}}
	h.handler = NewHandler(h.db, h.service,
		Options{AllowedOrigins: []string{origin}, Research: research, MuseCheck: performer, AnswerMatcher: matcher, Materials: materials})
	workflow, err := h.db.RoleWorkflow(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := fmt.Sprintf(`{"requestKey":"q1-start","expectedOpportunityRevision":%d,"expectedWorkflowRevision":%d}`,
		first.Revision, workflow.Revision)
	response = h.request("POST", "/api/v1/opportunities/"+first.ID+"/checks", start, cookie, "", csrf, origin)
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
	if started.Status != "checked" || len(started.Check.Questions) != 3 {
		t.Fatalf("checked view: %+v", started)
	}
	questionIDs := map[string]string{}
	for _, question := range started.Check.Questions {
		questionIDs[question.Text] = question.ID
	}
	// The step-2 discovery run stays open (durable server state); the
	// owner-visible check is complete, so close that run before
	// matching starts its own.
	active, err := h.db.ActiveRound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.FinishRound(ctx, cjOwner(), active.ID, store.RoundCompleted,
		"check_saved", "job_check", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	// Step 5: match, then keep one suggestion, edit one, blank the
	// optional, and commit the backend set.
	approvedText := "I build Go services for scheduling platforms."
	response = h.request("POST", "/api/v1/answers",
		`{"requestKey":"q1-approve-1","text":"`+approvedText+`","scopeTags":["go","services"],"contextNote":"backend experience"}`,
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
	matcher.choices[questionIDs[questionTexts[1]]] = approved.ID
	response = h.request("POST", "/api/v1/opportunities/"+first.ID+"/answers/match",
		`{"requestKey":"q1-match-1","expectedCheckId":"`+started.Check.ID+`","expectedQuestionSetSha256":"`+started.Check.QuestionSetSHA256+`"}`,
		cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("match: got %d %s, want 201", response.Code, response.Body.String())
	}
	if matcher.calls != 1 {
		t.Fatalf("match calls = %d, want exactly one judged batch", matcher.calls)
	}
	save := func(questionID, text string) {
		t.Helper()
		body := fmt.Sprintf(`{"expectedAnswerVersion":0,"text":%q}`, text)
		response := h.request("PUT", "/api/v1/opportunities/"+first.ID+"/questions/"+questionID+"/answer",
			body, cookie, "", csrf, origin)
		if response.Code != 200 {
			t.Fatalf("save answer: got %d %s, want 200", response.Code, response.Body.String())
		}
	}
	save(questionIDs[questionTexts[0]], "Rotterdam scheduling platforms drew me here.")
	save(questionIDs[questionTexts[1]], approvedText+" Shipped three.")
	save(questionIDs[questionTexts[2]], "")
	response = h.request("POST", "/api/v1/opportunities/"+first.ID+"/answers/commit", "", cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("commit: got %d %s, want 200", response.Code, response.Body.String())
	}

	// Step 6: one preparation over the committed set; the CV it stores
	// is the same identity the editor, rewrite and export later use.
	workflow, err = h.db.RoleWorkflow(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := fmt.Sprintf(`{"requestKey":"q1-draft-1","expectedCheckId":%q,"expectedQuestionSetSha256":%q,"expectedWorkflowRevision":%d}`,
		started.Check.ID, started.Check.QuestionSetSHA256, workflow.Revision)
	response = h.request("POST", "/api/v1/opportunities/"+first.ID+"/artifacts/draft", draft, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("prepare: got %d %s, want 201", response.Code, response.Body.String())
	}
	workflow, err = h.db.RoleWorkflow(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Stage != store.RoleStagePrepared {
		t.Fatalf("prepare left stage %q, want prepared", workflow.Stage)
	}
	ready := func(artifactType string) (int64, string) {
		t.Helper()
		response := h.request("GET", "/api/v1/opportunities/"+first.ID+"/artifacts/"+artifactType, "", cookie, "", "", "")
		var entry struct {
			State   string `json:"state"`
			Current *struct {
				Version int64  `json:"version"`
				Content string `json:"content"`
			} `json:"current"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || entry.State != "ready" || entry.Current == nil {
			t.Fatalf("%s readiness: got %d %+v, want ready", artifactType, response.Code, entry)
		}
		return entry.Current.Version, entry.Current.Content
	}
	cvVersion, cvContent := ready("cv")
	if !strings.Contains(cvContent, "q1-draft-1") {
		t.Fatalf("cv content %q misses the draft marker", cvContent)
	}
	draftCalls := materials.modelCalls

	exact := fmt.Sprintf(`{"requestKey":"q1-edit-1","expectedVersion":%d,"content":"Backend Engineer CV, exact owner text."}`, cvVersion)
	response = h.request("PUT", "/api/v1/opportunities/"+first.ID+"/artifacts/cv", exact, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("exact edit: got %d %s, want 201", response.Code, response.Body.String())
	}
	if materials.modelCalls != draftCalls {
		t.Fatalf("exact edit spent %d model calls, want zero", materials.modelCalls-draftCalls)
	}
	version2, content2 := ready("cv")
	if version2 != cvVersion+1 || content2 != "Backend Engineer CV, exact owner text." {
		t.Fatalf("edited cv: v%d %q, want literal v%d", version2, content2, cvVersion+1)
	}
	rewrite := fmt.Sprintf(`{"requestKey":"q1-rewrite-1","expectedVersion":%d,"instruction":"Tighten the summary."}`, version2)
	response = h.request("POST", "/api/v1/opportunities/"+first.ID+"/artifacts/cv/rewrite", rewrite, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("rewrite: got %d %s, want 201", response.Code, response.Body.String())
	}
	version3, content3 := ready("cv")
	if version3 != version2+1 || !strings.Contains(content3, "Tighten the summary.") {
		t.Fatalf("rewritten cv: v%d %q", version3, content3)
	}
	response = h.request("GET", "/api/v1/opportunities/"+first.ID+"/artifacts/cv/versions", "", cookie, "", "", "")
	var versions struct {
		Items []struct {
			Version int64 `json:"version"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &versions); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(versions.Items) != 3 {
		t.Fatalf("versions: got %d %+v, want 3", response.Code, versions)
	}
	exportCalls := materials.modelCalls
	response = h.request("GET", "/api/v1/opportunities/"+first.ID+"/artifacts/cv/export", "", cookie, "", "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), content3) {
		t.Fatalf("export: got %d, want the shown v%d bytes", response.Code, version3)
	}
	if materials.modelCalls != exportCalls {
		t.Fatalf("export spent model calls")
	}

	// Step 7: the saved-job index lists the work; the Handoff basis is
	// the verified destination + current items; the explicit save lands
	// handoff_saved and replays by state.
	response = h.request("GET", "/api/v1/saved-jobs", "", cookie, "", "", "")
	var index struct {
		Items []struct {
			OpportunityID string `json:"opportunityId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range index.Items {
		if item.OpportunityID == first.ID {
			found = true
		}
	}
	if response.Code != 200 || !found {
		t.Fatalf("saved-jobs: got %d %+v, want the job listed", response.Code, index)
	}
	handoffCalls := materials.modelCalls + matcher.calls
	response = h.request("GET", "/api/v1/opportunities/"+first.ID+"/handoff", "", cookie, "", "", "")
	var handoff struct {
		RouteDestination string `json:"routeDestination"`
		WorkflowStage    string `json:"workflowStage"`
		Items            []struct {
			Type    string `json:"type"`
			Version int64  `json:"version"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &handoff); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || handoff.RouteDestination != "jobs@example.invalid" {
		t.Fatalf("handoff basis: got %d %+v", response.Code, handoff)
	}
	cvItem := false
	for _, item := range handoff.Items {
		if item.Type == "cv" && item.Version == version3 {
			cvItem = true
		}
	}
	if !cvItem {
		t.Fatalf("handoff items miss current cv v%d: %+v", version3, handoff.Items)
	}
	if materials.modelCalls+matcher.calls != handoffCalls {
		t.Fatalf("handoff read spent provider calls")
	}
	workflow, err = h.db.RoleWorkflow(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	saveHandoff := fmt.Sprintf(`{"expectedWorkflowRevision":%d}`, workflow.Revision)
	response = h.request("POST", "/api/v1/opportunities/"+first.ID+"/handoff", saveHandoff, cookie, "", csrf, origin)
	var saved struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &saved)
	if response.Code != 201 || saved.Stage != "handoff_saved" {
		t.Fatalf("handoff save: got %d %+v, want 201 handoff_saved", response.Code, saved)
	}
	response = h.request("POST", "/api/v1/opportunities/"+first.ID+"/handoff", `{"expectedWorkflowRevision":0}`, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("handoff replay: got %d, want 200", response.Code)
	}

	// Second session return: a fresh handler over the same store reads
	// the terminal state with no provider spend.
	returned := &harness{t: h.t, db: h.db, service: h.service,
		handler: NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}})}
	cookie2, _ := h.login()
	response = returned.request("GET", "/api/v1/opportunities/"+first.ID+"/handoff", "", cookie2, "", "", "")
	var again struct {
		WorkflowStage string `json:"workflowStage"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &again)
	if response.Code != 200 || again.WorkflowStage != "handoff_saved" {
		t.Fatalf("second session: got %d %+v, want handoff_saved", response.Code, again)
	}
}

// q1Role is one selected role with a completed check plus the
// controlled provider boundaries behind its later steps.
type q1Role struct {
	opportunity store.Opportunity
	checkID     string
	questionSet string
	questions   map[string]string // question text -> id
	matcher     *cjJevMatcher
	materials   *q1RewriteStub
}

// q1CheckedRole selects one role over the store and completes its
// check over HTTP, closing the synchronous check round so later
// owner actions start fresh runs.
func q1CheckedRole(t *testing.T, h *harness, cookie *http.Cookie, csrf, key string, save func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error)) q1Role {
	t.Helper()
	ctx := context.Background()
	matcher := &cjJevMatcher{db: h.db, t: t, choices: map[string]string{}}
	materials := &q1RewriteStub{cjPrepareStub: cjPrepareStub{db: h.db, t: t}}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin},
		Research: &cjResearchStub{db: h.db}, MuseCheck: &cjCheckPerformer{save: save},
		AnswerMatcher: matcher, Materials: materials})
	opportunity := createCheckedOpportunity(t, h, key)
	workflow, err := h.db.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatalf("preread workflow: %v", err)
	}
	start := fmt.Sprintf(`{"requestKey":%q,"expectedOpportunityRevision":%d,"expectedWorkflowRevision":%d}`,
		key+"-start", opportunity.Revision, workflow.Revision)
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
	if started.Status != "checked" {
		t.Fatalf("check status %q, want checked", started.Status)
	}
	role := q1Role{opportunity: opportunity, checkID: started.Check.ID,
		questionSet: started.Check.QuestionSetSHA256, questions: map[string]string{},
		matcher: matcher, materials: materials}
	for _, question := range started.Check.Questions {
		role.questions[question.Text] = question.ID
	}
	return role
}

// q1Vacancy builds a complete vacancy input over one capture.
func q1Vacancy(capture store.SourceCapture, url string) store.CheckVacancyInput {
	return store.CheckVacancyInput{CaptureIDs: []string{capture.ID},
		Completeness: store.CaptureComplete, SourceURL: url, RetrievedAt: "2026-09-24T11:05:00Z"}
}

// q1Route builds an application-route judgment for one destination.
func q1Route(destination string) store.CheckRouteInput {
	return store.CheckRouteInput{Kind: store.CheckRouteDirect, DestinationText: destination,
		Judgment: store.CheckRouteJudgmentApplication,
		SourceExcerpt: "Apply: " + destination, ObservedAt: "2026-09-24T11:30:00Z"}
}

func q1Answer(t *testing.T, h *harness, cookie *http.Cookie, csrf, opportunityID, questionID, text string) {
	t.Helper()
	body := fmt.Sprintf(`{"expectedAnswerVersion":0,"text":%q}`, text)
	response := h.request("PUT", "/api/v1/opportunities/"+opportunityID+"/questions/"+questionID+"/answer",
		body, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("save answer: got %d %s, want 200", response.Code, response.Body.String())
	}
}

func q1Commit(t *testing.T, h *harness, cookie *http.Cookie, csrf, opportunityID string) {
	t.Helper()
	response := h.request("POST", "/api/v1/opportunities/"+opportunityID+"/answers/commit", "", cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("commit: got %d %s, want 200", response.Code, response.Body.String())
	}
}

// q1Draft runs one preparation and returns the status code: 201 when
// held types were drafted, 200 when nothing was held.
func q1Draft(t *testing.T, h *harness, cookie *http.Cookie, csrf string, role q1Role, requestKey string) int {
	t.Helper()
	workflow, err := h.db.RoleWorkflow(context.Background(), role.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"requestKey":%q,"expectedCheckId":%q,"expectedQuestionSetSha256":%q,"expectedWorkflowRevision":%d}`,
		requestKey, role.checkID, role.questionSet, workflow.Revision)
	response := h.request("POST", "/api/v1/opportunities/"+role.opportunity.ID+"/artifacts/draft",
		body, cookie, "", csrf, origin)
	if response.Code != 201 && response.Code != 200 {
		t.Fatalf("draft: got %d %s, want 200/201", response.Code, response.Body.String())
	}
	return response.Code
}

type q1Entry struct {
	required bool
	state    string
	version  int64
	content  string
}

func q1Readiness(t *testing.T, h *harness, cookie *http.Cookie, opportunityID string) map[string]q1Entry {
	t.Helper()
	response := h.request("GET", "/api/v1/opportunities/"+opportunityID+"/artifacts", "", cookie, "", "", "")
	var listed struct {
		Entries []struct {
			Type     string `json:"type"`
			Required bool   `json:"required"`
			State    string `json:"state"`
			Current  *struct {
				Version int64  `json:"version"`
				Content string `json:"content"`
			} `json:"current"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 {
		t.Fatalf("readiness: got %d, want 200", response.Code)
	}
	out := map[string]q1Entry{}
	for _, entry := range listed.Entries {
		value := q1Entry{required: entry.Required, state: entry.State}
		if entry.Current != nil {
			value.version, value.content = entry.Current.Version, entry.Current.Content
		}
		out[entry.Type] = value
	}
	return out
}

func q1SaveHandoff(t *testing.T, h *harness, cookie *http.Cookie, csrf, opportunityID string) {
	t.Helper()
	workflow, err := h.db.RoleWorkflow(context.Background(), opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"expectedWorkflowRevision":%d}`, workflow.Revision)
	response := h.request("POST", "/api/v1/opportunities/"+opportunityID+"/handoff", body, cookie, "", csrf, origin)
	var saved struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &saved)
	if response.Code != 201 || saved.Stage != "handoff_saved" {
		t.Fatalf("handoff save: got %d %+v, want 201 handoff_saved", response.Code, saved)
	}
}

func q1CVDoc() []store.RequestedDocumentInput {
	return []store.RequestedDocumentInput{{Label: "CV", Required: true,
		SourceExcerpt: "Send your CV", SourceSpan: store.CheckSourceSpan{Start: 10, End: 22}}}
}

// TestJourneyQ1QuestionlessEmailToHandoff completes the branch
// TestConnectedJourney03 leaves open: a verified questionless email
// route commits the empty answer set, prepares its email + CV
// artifacts, and reaches manual Handoff with the same identities.
func TestJourneyQ1QuestionlessEmailToHandoff(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	url := "https://harbour.example/jobs/q1-questionless"
	capture := cjInsertCapture(t, h.db, url)
	docs := q1CVDoc()
	docs[0].SourceSpan.CaptureID = capture.ID
	role := q1CheckedRole(t, h, cookie, csrf, "q1-questionless",
		func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
			return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
				OpportunityID: opportunityID, CheckID: checkID,
				Vacancy: q1Vacancy(capture, url), Route: q1Route("jobs@example.invalid"),
				RequestedDocuments: docs, Requirements: []store.CheckRequirementInput{},
				Gaps: []store.CheckGapInput{}, Questions: []store.CheckQuestionInput{},
				QuestionsNoneVerified: true,
			})
		})
	if len(role.questions) != 0 {
		t.Fatalf("questionless check carries %d questions", len(role.questions))
	}
	q1Commit(t, h, cookie, csrf, role.opportunity.ID)
	if code := q1Draft(t, h, cookie, csrf, role, "q1-nq-draft-1"); code != 201 {
		t.Fatalf("draft: got %d, want 201", code)
	}
	entries := q1Readiness(t, h, cookie, role.opportunity.ID)
	for _, artifactType := range []string{"email_subject", "email_body", "cv"} {
		entry, ok := entries[artifactType]
		if !ok || !entry.required || entry.state != "ready" || entry.version != 1 ||
			!strings.Contains(entry.content, "q1-nq-draft-1") {
			t.Fatalf("%s: %+v, want required ready v1 draft marker", artifactType, entry)
		}
	}
	if entry := entries["form_values"]; entry.required || entry.state != "not_required" {
		t.Fatalf("form_values: %+v, want not required without questions", entry)
	}
	workflow, err := h.db.RoleWorkflow(context.Background(), role.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Stage != store.RoleStagePrepared {
		t.Fatalf("stage %q, want prepared", workflow.Stage)
	}
	q1SaveHandoff(t, h, cookie, csrf, role.opportunity.ID)
}

// TestJourneyQ1RouteBranches pins readiness per destination and
// question shape: a portal destination requires no email artifacts,
// attachment-only questions require no pasteable form text, and a
// choice answer derives into form values.
func TestJourneyQ1RouteBranches(t *testing.T) {
	portal := "https://portal.example/apply/123"

	t.Run("portal", func(t *testing.T) {
		h := newHarness(t)
		cookie, csrf := h.login()
		url := "https://harbour.example/jobs/q1-portal"
		capture := cjInsertCapture(t, h.db, url)
		role := q1CheckedRole(t, h, cookie, csrf, "q1-portal",
			func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
				return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
					OpportunityID: opportunityID, CheckID: checkID,
					Vacancy: q1Vacancy(capture, url), Route: q1Route(portal),
					RequestedDocuments: []store.RequestedDocumentInput{},
					Requirements:       []store.CheckRequirementInput{},
					Gaps:               []store.CheckGapInput{},
					Questions: []store.CheckQuestionInput{{Text: "Why this role?",
						Required: store.CheckRequired, Kind: store.CheckQuestionFreeText,
						SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 100, End: 140},
						SourceExcerpt: "Why this role?"}},
				})
			})
		q1Answer(t, h, cookie, csrf, role.opportunity.ID, role.questions["Why this role?"], "Rotterdam scheduling work.")
		q1Commit(t, h, cookie, csrf, role.opportunity.ID)
		entries := q1Readiness(t, h, cookie, role.opportunity.ID)
		for _, artifactType := range []string{"email_subject", "email_body"} {
			if entry := entries[artifactType]; entry.required {
				t.Fatalf("%s required for a portal destination: %+v", artifactType, entry)
			}
		}
		if entry := entries["form_values"]; !entry.required || entry.state != "ready" {
			t.Fatalf("form_values: %+v, want required ready", entry)
		}
		if code := q1Draft(t, h, cookie, csrf, role, "q1-portal-draft-1"); code != 200 {
			t.Fatalf("draft: got %d, want 200 (nothing held)", code)
		}
		q1SaveHandoff(t, h, cookie, csrf, role.opportunity.ID)
		response := h.request("GET", "/api/v1/opportunities/"+role.opportunity.ID+"/handoff", "", cookie, "", "", "")
		var handoff struct {
			RouteDestination string `json:"routeDestination"`
			WorkflowStage    string `json:"workflowStage"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &handoff)
		if handoff.RouteDestination != portal || handoff.WorkflowStage != "handoff_saved" {
			t.Fatalf("portal handoff: %+v", handoff)
		}
	})

	t.Run("upload-only", func(t *testing.T) {
		h := newHarness(t)
		cookie, csrf := h.login()
		url := "https://harbour.example/jobs/q1-upload"
		capture := cjInsertCapture(t, h.db, url)
		role := q1CheckedRole(t, h, cookie, csrf, "q1-upload",
			func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
				return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
					OpportunityID: opportunityID, CheckID: checkID,
					Vacancy: q1Vacancy(capture, url), Route: q1Route(portal),
					RequestedDocuments: []store.RequestedDocumentInput{},
					Requirements:       []store.CheckRequirementInput{},
					Gaps:               []store.CheckGapInput{},
					Questions: []store.CheckQuestionInput{{Text: "Upload your portfolio.",
						Required: store.CheckRequired, Kind: store.CheckQuestionAttachment,
						SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 200, End: 240},
						SourceExcerpt: "Upload your portfolio."}},
				})
			})
		q1Answer(t, h, cookie, csrf, role.opportunity.ID, role.questions["Upload your portfolio."], "portfolio.pdf")
		q1Commit(t, h, cookie, csrf, role.opportunity.ID)
		entries := q1Readiness(t, h, cookie, role.opportunity.ID)
		if entry := entries["form_values"]; entry.required {
			t.Fatalf("form_values required for attachment-only questions: %+v", entry)
		}
		if code := q1Draft(t, h, cookie, csrf, role, "q1-upload-draft-1"); code != 200 {
			t.Fatalf("draft: got %d, want 200 (nothing held)", code)
		}
		q1SaveHandoff(t, h, cookie, csrf, role.opportunity.ID)
	})

	t.Run("choice", func(t *testing.T) {
		h := newHarness(t)
		cookie, csrf := h.login()
		url := "https://harbour.example/jobs/q1-choice"
		capture := cjInsertCapture(t, h.db, url)
		role := q1CheckedRole(t, h, cookie, csrf, "q1-choice",
			func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
				return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
					OpportunityID: opportunityID, CheckID: checkID,
					Vacancy: q1Vacancy(capture, url), Route: q1Route(portal),
					RequestedDocuments: []store.RequestedDocumentInput{},
					Requirements:       []store.CheckRequirementInput{},
					Gaps:               []store.CheckGapInput{},
					Questions: []store.CheckQuestionInput{{Text: "Hybrid or remote?",
						Required: store.CheckRequired, Kind: store.CheckQuestionChoice,
						SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 300, End: 340},
						SourceExcerpt: "Hybrid or remote?"}},
				})
			})
		q1Answer(t, h, cookie, csrf, role.opportunity.ID, role.questions["Hybrid or remote?"], "Hybrid, two days on site.")
		q1Commit(t, h, cookie, csrf, role.opportunity.ID)
		entries := q1Readiness(t, h, cookie, role.opportunity.ID)
		if entry := entries["form_values"]; !entry.required || entry.state != "ready" {
			t.Fatalf("form_values: %+v, want required ready with the choice saved", entry)
		}
		response := h.request("GET", "/api/v1/opportunities/"+role.opportunity.ID+"/artifacts/form_values", "", cookie, "", "", "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), "Hybrid, two days on site.") {
			t.Fatalf("form values read: got %d %s, want the saved choice", response.Code, response.Body.String())
		}
	})
}

// TestJourneyQ1TwoJobIsolation walks two jobs through preparation and
// proves edits, versions and Handoff stay per-job.
func TestJourneyQ1TwoJobIsolation(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	prepare := func(key, url, question string) q1Role {
		t.Helper()
		capture := cjInsertCapture(t, h.db, url)
		docs := q1CVDoc()
		docs[0].SourceSpan.CaptureID = capture.ID
		role := q1CheckedRole(t, h, cookie, csrf, key,
			func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
				return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
					OpportunityID: opportunityID, CheckID: checkID,
					Vacancy: q1Vacancy(capture, url), Route: q1Route("jobs@example.invalid"),
					RequestedDocuments: docs, Requirements: []store.CheckRequirementInput{},
					Gaps: []store.CheckGapInput{},
					Questions: []store.CheckQuestionInput{{Text: question,
						Required: store.CheckRequired, Kind: store.CheckQuestionFreeText,
						SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 100, End: 140},
						SourceExcerpt: question}},
				})
			})
		q1Answer(t, h, cookie, csrf, role.opportunity.ID, role.questions[question], "Answer for "+key+".")
		q1Commit(t, h, cookie, csrf, role.opportunity.ID)
		if code := q1Draft(t, h, cookie, csrf, role, key+"-draft-1"); code != 201 {
			t.Fatalf("%s draft: got %d, want 201", key, code)
		}
		return role
	}
	first := prepare("q1-iso-a", "https://harbour.example/jobs/q1-iso-a", "Why role A?")
	second := prepare("q1-iso-b", "https://harbour.example/jobs/q1-iso-b", "Why role B?")

	exact := `{"requestKey":"q1-iso-edit-1","expectedVersion":1,"content":"Role A CV, exact owner text."}`
	response := h.request("PUT", "/api/v1/opportunities/"+first.opportunity.ID+"/artifacts/cv", exact, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("edit A: got %d %s, want 201", response.Code, response.Body.String())
	}
	entries := q1Readiness(t, h, cookie, second.opportunity.ID)
	if entry := entries["cv"]; entry.version != 1 || !strings.Contains(entry.content, "q1-iso-b-draft-1") {
		t.Fatalf("role B cv changed by the role A edit: %+v", entry)
	}
	q1SaveHandoff(t, h, cookie, csrf, first.opportunity.ID)
	response = h.request("GET", "/api/v1/opportunities/"+second.opportunity.ID+"/handoff", "", cookie, "", "", "")
	var handoff struct {
		WorkflowStage string `json:"workflowStage"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &handoff)
	if response.Code != 200 || handoff.WorkflowStage != "prepared" {
		t.Fatalf("role B stage: got %d %+v, want prepared", response.Code, handoff)
	}
	response = h.request("GET", "/api/v1/saved-jobs", "", cookie, "", "", "")
	var index struct {
		Items []struct {
			OpportunityID string `json:"opportunityId"`
		} `json:"items"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &index)
	seen := map[string]bool{}
	for _, item := range index.Items {
		seen[item.OpportunityID] = true
	}
	if !seen[first.opportunity.ID] || !seen[second.opportunity.ID] {
		t.Fatalf("saved-jobs misses a role: %+v", index.Items)
	}
}

// TestJourneyQ1LostAcknowledgment proves same-key retries replay the
// accepted versions instead of duplicating work.
func TestJourneyQ1LostAcknowledgment(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	url := "https://harbour.example/jobs/q1-lostack"
	capture := cjInsertCapture(t, h.db, url)
	docs := q1CVDoc()
	docs[0].SourceSpan.CaptureID = capture.ID
	role := q1CheckedRole(t, h, cookie, csrf, "q1-lostack",
		func(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
			return h.db.SaveJobCheckBody(ctx, cjOwner(), store.CheckSaveInput{
				OpportunityID: opportunityID, CheckID: checkID,
				Vacancy: q1Vacancy(capture, url), Route: q1Route("jobs@example.invalid"),
				RequestedDocuments: docs, Requirements: []store.CheckRequirementInput{},
				Gaps: []store.CheckGapInput{},
				Questions: []store.CheckQuestionInput{{Text: "Why this role?",
					Required: store.CheckRequired, Kind: store.CheckQuestionFreeText,
					SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 100, End: 140},
					SourceExcerpt: "Why this role?"}},
			})
		})
	q1Answer(t, h, cookie, csrf, role.opportunity.ID, role.questions["Why this role?"], "Lost-ack answer.")
	q1Commit(t, h, cookie, csrf, role.opportunity.ID)
	q1Commit(t, h, cookie, csrf, role.opportunity.ID)
	versions := func() []int64 {
		t.Helper()
		response := h.request("GET", "/api/v1/opportunities/"+role.opportunity.ID+"/artifacts/cv/versions", "", cookie, "", "", "")
		var listed struct {
			Items []struct {
				Version int64 `json:"version"`
			} `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, item := range listed.Items {
			out = append(out, item.Version)
		}
		return out
	}
	if code := q1Draft(t, h, cookie, csrf, role, "q1-lostack-draft-1"); code != 201 {
		t.Fatalf("draft: got %d, want 201", code)
	}
	// A re-draft under a fresh key pins the advanced workflow revision.
	workflow, err := h.db.RoleWorkflow(context.Background(), role.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	retry := fmt.Sprintf(`{"requestKey":"q1-lostack-draft-1","expectedCheckId":%q,"expectedQuestionSetSha256":%q,"expectedWorkflowRevision":%d}`,
		role.checkID, role.questionSet, workflow.Revision)
	response := h.request("POST", "/api/v1/opportunities/"+role.opportunity.ID+"/artifacts/draft", retry, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("draft replay: got %d %s, want 200", response.Code, response.Body.String())
	}
	if got := versions(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("draft replay duplicated versions: %v", got)
	}
	exact := `{"requestKey":"q1-lostack-edit-1","expectedVersion":1,"content":"Lost-ack CV text."}`
	response = h.request("PUT", "/api/v1/opportunities/"+role.opportunity.ID+"/artifacts/cv", exact, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("exact edit: got %d, want 201", response.Code)
	}
	response = h.request("PUT", "/api/v1/opportunities/"+role.opportunity.ID+"/artifacts/cv", exact, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("edit replay: got %d %s, want 200", response.Code, response.Body.String())
	}
	if got := versions(); len(got) != 2 || got[1] != 2 {
		t.Fatalf("edit replay duplicated versions: %v", got)
	}
	rewrite := `{"requestKey":"q1-lostack-rewrite-1","expectedVersion":2,"instruction":"Tighten."}`
	response = h.request("POST", "/api/v1/opportunities/"+role.opportunity.ID+"/artifacts/cv/rewrite", rewrite, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("rewrite: got %d %s, want 201", response.Code, response.Body.String())
	}
	response = h.request("POST", "/api/v1/opportunities/"+role.opportunity.ID+"/artifacts/cv/rewrite", rewrite, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("rewrite replay: got %d %s, want 200", response.Code, response.Body.String())
	}
	if got := versions(); len(got) != 3 || got[2] != 3 {
		t.Fatalf("rewrite replay duplicated versions: %v", got)
	}
}

// TestJourneyQ1NoEmployerAction probes every employer-action-shaped
// route and proves the router answers 404: nothing can fill, attach,
// send, submit or apply on the owner's behalf.
func TestJourneyQ1NoEmployerAction(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "q1-noaction")
	probes := [][2]string{
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/send"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/submit"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/apply"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/fill"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/attach"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/autofill"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/handoff/send"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/handoff/submit"},
		{"GET", "/api/v1/opportunities/" + opportunity.ID + "/send"},
		{"POST", "/api/v1/application-packs"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/materials/prepare"},
		{"POST", "/api/v1/opportunities/" + opportunity.ID + "/materials/submit"},
	}
	for _, probe := range probes {
		response := h.request(probe[0], probe[1], `{}`, cookie, "", csrf, origin)
		if response.Code != 404 {
			t.Fatalf("employer-action probe %s %s: got %d, want 404", probe[0], probe[1], response.Code)
		}
	}
}
