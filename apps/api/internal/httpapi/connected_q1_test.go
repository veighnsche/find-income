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
	// The synchronous check run leaves its round open; the owner-visible
	// check is complete, so close the run before matching starts its own.
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
