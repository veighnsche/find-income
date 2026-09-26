package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func createCheckedOpportunity(t *testing.T, h *harness, key string) store.Opportunity {
	t.Helper()
	company, _, err := h.db.CreateCompany(context.Background(), store.Actor{Kind: "administrator", ID: "owner"}, store.CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := h.db.CreateOpportunity(context.Background(), store.Actor{Kind: "administrator", ID: "owner"}, store.OpportunityInput{
		CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/1", OriginalText: "Build Go services.",
		Stage: "new", WorkPattern: "hybrid",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.db.SetOwnerOpportunityDecision(context.Background(), store.Actor{Kind: "administrator", ID: "owner"}, opportunity.ID,
		store.OwnerDecisionInput{RequestKey: key, ExpectedOpportunityRevision: opportunity.Revision, ExpectedDecisionRevision: 0, Decision: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	return opportunity
}

func TestCommitRoleAnswersEndpoint(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	for _, id := range []string{"synthetic-role"} {
		response := h.request("POST", "/api/v1/opportunities/"+id+"/answers/commit", "", cookie, "", csrf, origin)
		if response.Code != 404 {
			t.Fatalf("missing commit: got %d, want honest 404", response.Code)
		}
	}
	opportunity := createCheckedOpportunity(t, h, "answer-commit-1")
	response := h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/answers/commit", "", cookie, "", csrf, origin)
	if response.Code != 404 {
		t.Fatalf("unchecked commit: got %d, want honest 404", response.Code)
	}
}

func TestCheckAnswerValuesLiveHonestNotFound(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	response := h.request("GET", "/api/v1/opportunities/synthetic-role/answers/current", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("answer read: got %d, want honest 404", response.Code)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "not_found" {
		t.Fatalf("answer read code: got %q, want not_found", envelope.Error.Code)
	}
	response = h.request("PUT", "/api/v1/opportunities/synthetic-role/questions/synthetic-q/answer",
		`{"expectedAnswerVersion":0,"text":""}`, cookie, "", csrf, origin)
	if response.Code != 404 {
		t.Fatalf("answer save: got %d, want honest 404", response.Code)
	}
}

func TestOpportunityCheckEndpoints(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	opportunity := createCheckedOpportunity(t, h, "check-select-1")

	response := h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/checks/current", "", cookie, "", "", "")
	var status struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || status.Status != "not_checked" {
		t.Fatalf("current before start: %d %+v", response.Code, status)
	}

	start := fmt.Sprintf(`{"requestKey":"check-start-1","expectedOpportunityRevision":%d,"expectedWorkflowRevision":0}`, opportunity.Revision)
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/checks", start, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	var started struct {
		Status string `json:"status"`
		Check  struct {
			ID string `json:"id"`
		} `json:"check"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Status != "checking" || started.Check.ID == "" {
		t.Fatalf("started view: %+v", started)
	}
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/checks", start, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("replay: %d", response.Code)
	}

	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/checks/"+started.Check.ID, "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("get check: %d %s", response.Code, response.Body.String())
	}
	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/checks/current/activity", "", cookie, "", "", "")
	var activity struct {
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &activity); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(activity.Events) == 0 {
		t.Fatalf("activity: %d %+v", response.Code, activity)
	}
	response = h.request("POST", "/api/v1/opportunities/missing/checks", start, cookie, "", csrf, origin)
	if response.Code != 404 {
		t.Fatalf("unselected start: %d", response.Code)
	}
}

func TestSavedAnswerEndpoints(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	response := h.request("GET", "/api/v1/answers", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("list empty: %d", response.Code)
	}
	create := `{"requestKey":"answer-approve-1","text":"I work remotely from Example City.","scopeTags":["work_pattern"]}`
	response = h.request("POST", "/api/v1/answers", create, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var saved struct {
		ID             string `json:"id"`
		CurrentVersion int64  `json:"currentVersion"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.CurrentVersion != 1 {
		t.Fatalf("saved view: %+v", saved)
	}
	response = h.request("POST", "/api/v1/answers", create, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("replay: %d", response.Code)
	}
	response = h.request("GET", "/api/v1/answers/"+saved.ID, "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("get: %d", response.Code)
	}
	approve := `{"requestKey":"answer-approve-2","expectedVersion":1,"text":"I work remotely from Example City, CET."}`
	response = h.request("POST", "/api/v1/answers/"+saved.ID+"/versions", approve, cookie, "", csrf, origin)
	if response.Code != 201 {
		t.Fatalf("approve v2: %d %s", response.Code, response.Body.String())
	}
	response = h.request("POST", "/api/v1/answers/"+saved.ID+"/versions", approve, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("approve replay: %d", response.Code)
	}
	stale := `{"requestKey":"answer-approve-3","expectedVersion":1,"text":"Stale."}`
	response = h.request("POST", "/api/v1/answers/"+saved.ID+"/versions", stale, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("stale version: %d", response.Code)
	}
	response = h.request("GET", "/api/v1/answers", "", nil, "", "", "")
	if response.Code != 401 {
		t.Fatalf("unauthenticated answers: %d", response.Code)
	}
}
