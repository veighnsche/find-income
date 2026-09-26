package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestArtifactReadsLive(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	response := h.request("GET", "/api/v1/opportunities/synthetic-role/artifacts", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("missing set: got %d, want honest 404", response.Code)
	}
	opportunity := createCheckedOpportunity(t, h, "artifact-read-1")
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts", "", cookie, "", "", "")
	var set struct {
		OpportunityID string `json:"opportunityId"`
		CheckStatus   string `json:"checkStatus"`
		Entries       []struct {
			Type     string `json:"type"`
			Required bool   `json:"required"`
			State    string `json:"state"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || set.CheckStatus != "not_checked" || len(set.Entries) != 5 {
		t.Fatalf("unchecked set: %d %+v", response.Code, set)
	}
	for _, entry := range set.Entries {
		if entry.Required || entry.State != "unresolved" {
			t.Fatalf("unchecked %s: %+v", entry.Type, entry)
		}
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv", "", cookie, "", "", "")
	var entry struct {
		Type  string `json:"type"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || entry.Type != "cv" || entry.State != "unresolved" {
		t.Fatalf("single entry: %d %+v", response.Code, entry)
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/portfolio", "", cookie, "", "", "")
	if response.Code != 400 {
		t.Fatalf("unknown type: got %d, want 400", response.Code)
	}
}

func TestPrepareActivityEndpoint(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	response := h.request("GET", "/api/v1/opportunities/synthetic-role/artifacts/activity", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("missing role: got %d, want honest 404", response.Code)
	}
	opportunity := createCheckedOpportunity(t, h, "artifact-activity-1")
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/activity", "", cookie, "", "", "")
	var page struct {
		Events []struct {
			Kind    string `json:"kind"`
			Summary string `json:"summary"`
		} `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(page.Events) != 0 {
		t.Fatalf("empty: %d %+v", response.Code, page)
	}
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if _, err := h.db.RecordPrepareActivity(ctx, owner, opportunity.ID, store.PrepareActivityInput{
		Kind: store.PrepareTurnStarted, Outcome: "started",
		Payload: json.RawMessage(`{"targets":["cv"],"factIds":["cv-vince-liem.md"],"answerIds":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.RecordPrepareActivity(ctx, owner, opportunity.ID, store.PrepareActivityInput{
		Kind: store.PrepareCompleted, Outcome: "ok",
		Payload: json.RawMessage(`{"drafted":["cv"],"held":["email_body"]}`)}); err != nil {
		t.Fatal(err)
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/activity", "", cookie, "", "", "")
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(page.Events) != 2 {
		t.Fatalf("page: %d %+v", response.Code, page)
	}
	if page.Events[0].Kind != "prepare.turn_started" ||
		!strings.Contains(page.Events[0].Summary, "cv-vince-liem.md") {
		t.Fatalf("turn summary: %+v", page.Events[0])
	}
	if !strings.Contains(page.Events[1].Summary, "email_body") {
		t.Fatalf("completed summary: %+v", page.Events[1])
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/activity?limit=0", "", cookie, "", "", "")
	if response.Code != 400 {
		t.Fatalf("limit: got %d, want 400", response.Code)
	}
}

type stubPreparer struct {
	draftSet       store.ArtifactReadinessSet
	draftCreated   bool
	draftErr       error
	draftCalls     int
	rewriteSet     store.ArtifactReadinessSet
	rewriteCreated bool
	rewriteErr     error
	rewriteCalls   int
	rewriteInput   materialprep.RewriteInput
}

func (s *stubPreparer) DraftOpportunityArtifacts(context.Context, store.Actor, string, string, string, string, int64) (store.ArtifactReadinessSet, bool, error) {
	s.draftCalls++
	return s.draftSet, s.draftCreated, s.draftErr
}

func (s *stubPreparer) RewriteOpportunityArtifacts(_ context.Context, _ store.Actor, _ string, _ string, input materialprep.RewriteInput) (store.ArtifactReadinessSet, bool, error) {
	s.rewriteCalls++
	s.rewriteInput = input
	return s.rewriteSet, s.rewriteCreated, s.rewriteErr
}

func TestArtifactDraftEndpoint(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	response := h.request("POST", "/api/v1/opportunities/synthetic-role/artifacts/draft",
		`{"requestKey":"d","expectedCheckId":"c","expectedQuestionSetSha256":"s","expectedWorkflowRevision":0}`,
		cookie, "", csrf, origin)
	if response.Code != 503 {
		t.Fatalf("unwired draft: got %d, want honest 503", response.Code)
	}
	stub := &stubPreparer{draftSet: store.ArtifactReadinessSet{OpportunityID: "synthetic-role",
		CheckStatus: "checked", Entries: []store.ArtifactReadinessEntry{{Type: "cv", Required: true, State: "ready"}}},
		draftCreated: true}
	h.handler = NewHandler(h.db, h.service, Options{AllowedOrigins: []string{origin}, Materials: stub})
	response = h.request("POST", "/api/v1/opportunities/synthetic-role/artifacts/draft",
		`{"requestKey":"d","expectedCheckId":"c","expectedQuestionSetSha256":"s","expectedWorkflowRevision":0}`,
		cookie, "", csrf, origin)
	var set struct {
		Entries []struct {
			Type  string `json:"type"`
			State string `json:"state"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if response.Code != 201 || stub.draftCalls != 1 || len(set.Entries) != 1 || set.Entries[0].State != "ready" {
		t.Fatalf("draft: %d calls=%d %+v", response.Code, stub.draftCalls, set)
	}
}

func TestArtifactExactEditVersions(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "artifact-edit-1")
	put := func(artifactType, body string) (int, map[string]any) {
		response := h.request("PUT", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/"+artifactType, body, cookie, "", csrf, origin)
		var decoded map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return response.Code, decoded
	}
	code, first := put("cv", `{"requestKey":"edit-1","expectedVersion":0,"content":"cv v1"}`)
	if code != 201 || first["version"] != float64(1) || first["content"] != "cv v1" {
		t.Fatalf("first edit: %d %+v", code, first)
	}
	code, replay := put("cv", `{"requestKey":"edit-1","expectedVersion":0,"content":"cv v1"}`)
	if code != 200 || replay["id"] != first["id"] {
		t.Fatalf("replay: %d %+v", code, replay)
	}
	code, second := put("cv", `{"requestKey":"edit-2","expectedVersion":1,"content":"cv v2"}`)
	if code != 201 || second["version"] != float64(2) {
		t.Fatalf("second edit: %d %+v", code, second)
	}
	if code, _ := put("cv", `{"requestKey":"edit-3","expectedVersion":1,"content":"fork"}`); code != 409 {
		t.Fatalf("stale fence: got %d, want 409", code)
	}
	if code, _ := put("form_values", `{"requestKey":"edit-4","expectedVersion":0,"content":"derived"}`); code != 400 {
		t.Fatalf("form values write: got %d, want 400", code)
	}
	if code, _ := put("cv", `{"requestKey":"edit-5","expectedVersion":2,"content":""}`); code != 400 {
		t.Fatalf("empty content: got %d, want 400", code)
	}
	response := h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv", "", cookie, "", "", "")
	var entry struct {
		Current *struct {
			Version int64  `json:"version"`
			Content string `json:"content"`
		} `json:"current"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || entry.Current == nil || entry.Current.Version != 2 || entry.Current.Content != "cv v2" {
		t.Fatalf("read back: %d %+v", response.Code, entry)
	}
}

// callHandler invokes an as-yet-unregistered handler directly with
// path values and owner auth, mirroring the harness request shape.
func callHandler(h *harness, handler func(http.ResponseWriter, *http.Request), method, target, body string,
	cookie *http.Cookie, csrf string, pathValues ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
		request.Header.Set("Origin", origin)
	}
	for i := 0; i+1 < len(pathValues); i += 2 {
		request.SetPathValue(pathValues[i], pathValues[i+1])
	}
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func TestRewriteEndpoint(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "artifact-rewrite-1")
	concrete := &Handler{database: h.db, auth: h.service, origins: map[string]bool{origin: true}}
	call := func(body string) *httptest.ResponseRecorder {
		return callHandler(h, concrete.rewriteOpportunityArtifact, "POST",
			"/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv/rewrite", body, cookie, csrf,
			"id", opportunity.ID, "artifactType", "cv")
	}
	if response := call(`{"requestKey":"r","expectedVersion":1}`); response.Code != 503 {
		t.Fatalf("unwired rewrite: got %d, want honest 503", response.Code)
	}
	stub := &stubPreparer{rewriteSet: store.ArtifactReadinessSet{OpportunityID: opportunity.ID,
		CheckStatus: "checked", Entries: []store.ArtifactReadinessEntry{{Type: "cv", Required: true, State: "ready"}}},
		rewriteCreated: true}
	concrete = &Handler{database: h.db, auth: h.service, origins: map[string]bool{origin: true}, materials: stub}
	response := callHandler(h, concrete.rewriteOpportunityArtifact, "POST",
		"/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv/rewrite",
		`{"requestKey":"r-1","expectedVersion":1,"instruction":"Tighten."}`, cookie, csrf,
		"id", opportunity.ID, "artifactType", "cv")
	var set struct {
		Entries []struct {
			Type  string `json:"type"`
			State string `json:"state"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if response.Code != 201 || stub.rewriteCalls != 1 || len(set.Entries) != 1 {
		t.Fatalf("rewrite: %d calls=%d %+v", response.Code, stub.rewriteCalls, set)
	}
	if len(stub.rewriteInput.Items) != 1 || stub.rewriteInput.Items[0].Type != "cv" ||
		stub.rewriteInput.Items[0].ExpectedVersion != 1 || stub.rewriteInput.Instruction != "Tighten." {
		t.Fatalf("rewrite input: %+v", stub.rewriteInput)
	}
}

func TestArtifactVersionsAndExportEndpoints(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "artifact-export-1")
	concrete := &Handler{database: h.db, auth: h.service, origins: map[string]bool{origin: true}}
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	for i, content := range []string{"cv v1", "cv v2"} {
		if _, _, err := h.db.SaveOpportunityArtifact(ctx, owner, opportunity.ID, store.ArtifactSaveInput{
			RequestKey: "export-" + string(rune('a'+i)), ExpectedVersion: int64(i),
			Type: "cv", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	response := callHandler(h, concrete.listArtifactVersions, "GET",
		"/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv/versions", "", cookie, "",
		"id", opportunity.ID, "artifactType", "cv")
	var history struct {
		Items []struct {
			Version int64  `json:"version"`
			Content string `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(history.Items) != 2 || history.Items[0].Content != "cv v1" {
		t.Fatalf("versions: %d %+v", response.Code, history)
	}
	response = callHandler(h, concrete.listArtifactVersions, "GET",
		"/api/v1/opportunities/"+opportunity.ID+"/artifacts/form_values/versions", "", cookie, "",
		"id", opportunity.ID, "artifactType", "form_values")
	if response.Code != 400 {
		t.Fatalf("derived versions: got %d, want 400", response.Code)
	}
	response = callHandler(h, concrete.exportOpportunityArtifact, "GET",
		"/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv/export", "", cookie, "",
		"id", opportunity.ID, "artifactType", "cv")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "cv v2") ||
		!strings.Contains(response.Body.String(), "artifact-version: 2") {
		t.Fatalf("export current: %d %q", response.Code, response.Body.String())
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, "harbour-systems-backend-engineer-cv-v2.md") {
		t.Fatalf("disposition: %q", disposition)
	}
	if response.Header().Get("X-Content-SHA256") == "" {
		t.Fatal("export misses the content checksum")
	}
	response = callHandler(h, concrete.exportOpportunityArtifact, "GET",
		"/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv/export?version=1", "", cookie, "",
		"id", opportunity.ID, "artifactType", "cv")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "cv v1") {
		t.Fatalf("export v1: %d %q", response.Code, response.Body.String())
	}
	response = callHandler(h, concrete.exportOpportunityArtifact, "GET",
		"/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv/export?version=9", "", cookie, "",
		"id", opportunity.ID, "artifactType", "cv")
	if response.Code != 404 {
		t.Fatalf("missing version: got %d, want 404", response.Code)
	}
	_ = csrf
}

func TestSavedJobsAndHandoffEndpoints(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	opportunity := createCheckedOpportunity(t, h, "artifact-index-1")
	concrete := &Handler{database: h.db, auth: h.service, origins: map[string]bool{origin: true}}
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if _, _, err := h.db.SaveOpportunityArtifact(ctx, owner, opportunity.ID, store.ArtifactSaveInput{
		RequestKey: "index-cv", Type: "cv", Content: "cv v1"}); err != nil {
		t.Fatal(err)
	}
	response := callHandler(h, concrete.listSavedJobs, "GET", "/api/v1/saved-jobs", "", cookie, "")
	var index struct {
		Items []struct {
			OpportunityID string `json:"opportunityId"`
			Title         string `json:"title"`
			Items         []struct {
				Type    string `json:"type"`
				State   string `json:"state"`
				Version int64  `json:"version"`
			} `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(index.Items) != 1 || index.Items[0].OpportunityID != opportunity.ID {
		t.Fatalf("index: %d %+v", response.Code, index)
	}
	if len(index.Items[0].Items) == 0 || index.Items[0].Items[0].Type != "cv" ||
		index.Items[0].Items[0].Version != 1 {
		t.Fatalf("index items: %+v", index.Items[0].Items)
	}
	response = callHandler(h, concrete.getOpportunityHandoff, "GET",
		"/api/v1/opportunities/"+opportunity.ID+"/handoff", "", cookie, "",
		"id", opportunity.ID)
	var handoff struct {
		OpportunityID string `json:"opportunityId"`
		CheckStatus   string `json:"checkStatus"`
		Items         []struct {
			Type    string `json:"type"`
			State   string `json:"state"`
			Content string `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &handoff); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || handoff.OpportunityID != opportunity.ID || handoff.CheckStatus != "not_checked" {
		t.Fatalf("handoff: %d %+v", response.Code, handoff)
	}
	found := false
	for _, item := range handoff.Items {
		if item.Type == "cv" && item.Content == "cv v1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("handoff items: %+v", handoff.Items)
	}
	workflow, err := h.db.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Stage == store.RoleStageHandoffSaved {
		t.Fatal("passive handoff read transitioned the workflow")
	}
}
