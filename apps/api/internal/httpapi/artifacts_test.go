package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
