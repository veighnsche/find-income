package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func walkHTTPToPrepared(t *testing.T, h *harness, opportunityID string) store.RoleWorkflow {
	t.Helper()
	ctx := context.Background()
	var workflow store.RoleWorkflow
	var err error
	for _, stage := range []string{store.RoleStageChecking, store.RoleStageChecked, store.RoleStageAnswering,
		store.RoleStageAnswered, store.RoleStagePreparing, store.RoleStagePrepared} {
		workflow, err = h.db.AdvanceRoleWorkflow(ctx, opportunityID, workflow.Revision, stage, "")
		if err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
	}
	return workflow
}

func TestSaveOpportunityHandoffEndpoint(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()

	unselected := createUnselectedOpportunity(t, h)
	response := h.request("POST", "/api/v1/opportunities/"+unselected.ID+"/handoff",
		`{"expectedWorkflowRevision":0}`, cookie, "", csrf, origin)
	if response.Code != 404 {
		t.Fatalf("unselected save: got %d, want 404", response.Code)
	}

	early := createCheckedOpportunity(t, h, "handoff-early")
	response = h.request("POST", "/api/v1/opportunities/"+early.ID+"/handoff",
		`{"expectedWorkflowRevision":0}`, cookie, "", csrf, origin)
	if response.Code != 400 {
		t.Fatalf("unprepared save: got %d %s, want 400", response.Code, response.Body.String())
	}

	opportunity := createCheckedOpportunity(t, h, "handoff-ready")
	prepared := walkHTTPToPrepared(t, h, opportunity.ID)
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/handoff",
		`{"expectedWorkflowRevision":99}`, cookie, "", csrf, origin)
	if response.Code != 409 {
		t.Fatalf("stale save: got %d, want 409", response.Code)
	}
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/handoff",
		fmt.Sprintf(`{"expectedWorkflowRevision":%d}`, prepared.Revision), cookie, "", csrf, origin)
	var saved struct {
		Stage    string `json:"stage"`
		Revision int64  `json:"revision"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &saved)
	if response.Code != 201 || saved.Stage != "handoff_saved" || saved.Revision != prepared.Revision+1 {
		t.Fatalf("save: got %d %+v, want 201 handoff_saved", response.Code, saved)
	}
	response = h.request("POST", "/api/v1/opportunities/"+opportunity.ID+"/handoff",
		`{"expectedWorkflowRevision":0}`, cookie, "", csrf, origin)
	if response.Code != 200 {
		t.Fatalf("replay: got %d, want 200", response.Code)
	}
}

// The M6 read routes are registered and reachable: versions, export,
// saved-jobs and Handoff answer with real shapes, never 404-by-route.
func TestM6ReadRoutesRegistered(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	opportunity := createCheckedOpportunity(t, h, "handoff-routes")

	response := h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/cv/versions", "", cookie, "", "", "")
	var versions struct {
		Items []any `json:"items"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &versions)
	if response.Code != 200 || versions.Items == nil {
		t.Fatalf("versions: got %d %s, want 200 list", response.Code, response.Body.String())
	}

	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/artifacts/bogus/versions", "", cookie, "", "", "")
	if response.Code != 400 {
		t.Fatalf("bogus versions: got %d, want 400", response.Code)
	}

	response = h.request("GET", "/api/v1/saved-jobs", "", cookie, "", "", "")
	var index struct {
		Items []any `json:"items"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &index)
	if response.Code != 200 || index.Items == nil {
		t.Fatalf("saved-jobs: got %d %s, want 200 list", response.Code, response.Body.String())
	}

	response = h.request("GET", "/api/v1/opportunities/"+opportunity.ID+"/handoff", "", cookie, "", "", "")
	var view struct {
		OpportunityID string `json:"opportunityId"`
		Items         []any  `json:"items"`
		Uploads       []any  `json:"uploads"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &view)
	if response.Code != 200 || view.OpportunityID != opportunity.ID || view.Items == nil || view.Uploads == nil {
		t.Fatalf("handoff: got %d %+v, want 200 projection", response.Code, view)
	}
}
