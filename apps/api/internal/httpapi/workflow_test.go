package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func createWorkflowOpportunity(t *testing.T, h *harness, key, decision string) store.Opportunity {
	t.Helper()
	company, _, err := h.db.CreateCompany(context.Background(), store.Actor{Kind: "administrator", ID: "owner"}, store.CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := h.db.CreateOpportunity(context.Background(), store.Actor{Kind: "administrator", ID: "owner"}, store.OpportunityInput{
		CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/wf-" + key, OriginalText: "Build Go services.",
		Stage: "new", WorkPattern: "hybrid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != "" {
		_, _, err = h.db.SetOwnerOpportunityDecision(context.Background(), store.Actor{Kind: "administrator", ID: "owner"}, opportunity.ID,
			store.OwnerDecisionInput{RequestKey: key, ExpectedOpportunityRevision: opportunity.Revision, ExpectedDecisionRevision: 0, Decision: decision})
		if err != nil {
			t.Fatal(err)
		}
	}
	return opportunity
}

func TestRoleWorkflowReads(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	selected := createWorkflowOpportunity(t, h, "wf-select-1", "selected")
	unselected := createWorkflowOpportunity(t, h, "wf-dismiss-1", "dismissed")

	response := h.request("GET", "/api/v1/opportunities/"+selected.ID+"/workflow", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("selected role: %d %s", response.Code, response.Body.String())
	}
	var single struct {
		Stage    string `json:"stage"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.Stage != "selected" || single.Revision != 0 {
		t.Fatalf("selection advanced the role: %+v", single)
	}

	response = h.request("GET", "/api/v1/opportunities/"+unselected.ID+"/workflow", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("unselected role: %d", response.Code)
	}
	response = h.request("GET", "/api/v1/opportunities/missing/workflow", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("missing role: %d", response.Code)
	}

	if _, err := h.db.AdvanceRoleWorkflow(context.Background(), selected.ID, 0, "checking", ""); err != nil {
		t.Fatal(err)
	}
	response = h.request("GET", "/api/v1/workflow/roles", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("roles: %d %s", response.Code, response.Body.String())
	}
	var list struct {
		Items []struct {
			OpportunityID string `json:"opportunityId"`
			Stage         string `json:"stage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].OpportunityID != selected.ID || list.Items[0].Stage != "checking" {
		t.Fatalf("roles list: %+v", list)
	}

	response = h.request("GET", "/api/v1/workflow/roles", "", nil, "", "", "")
	if response.Code != 401 {
		t.Fatalf("unauthenticated roles: %d", response.Code)
	}
}
