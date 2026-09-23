package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestOrganisationRoutesOwnerCategoriesAndScopedReads(t *testing.T) {
	h := newRecordHTTP(t)
	owner := h.login()
	status, body := h.owner(http.MethodGet, "/runtime-status")
	requireStatus(t, status, http.StatusOK, body)
	runtime := decodeObject(t, body)
	if runtime["ingestionAvailable"] != false || runtime["organisationAvailable"] != false {
		t.Fatalf("runtime availability: %+v", runtime)
	}
	status, body = h.owner(http.MethodGet, "/organisation/categories")
	requireStatus(t, status, http.StatusOK, body)
	if got := decodeObject(t, body)["version"]; got != float64(1) {
		t.Fatalf("initial category version %v", got)
	}
	status, body = h.owner(http.MethodPut, "/organisation/categories", `{"expectedVersion":1,"categories":[{"id":"remote-first","description":"Remote roles I want to consider"}]}`)
	requireStatus(t, status, http.StatusOK, body)
	if got := decodeObject(t, body)["version"]; got != float64(2) {
		t.Fatalf("saved category version %v", got)
	}
	status, body = h.owner(http.MethodPut, "/organisation/categories", `{"expectedVersion":1,"categories":[]}`)
	requireStatus(t, status, http.StatusConflict, body)
	status, body = h.owner(http.MethodPost, "/companies", `{"name":"Synthetic Org"}`)
	requireStatus(t, status, http.StatusCreated, body)
	companyID := decodeObject(t, body)["company"].(map[string]any)["id"].(string)
	input, _ := json.Marshal(map[string]any{"companyId": companyID, "title": "Remote Engineer", "kind": "employment", "stage": "saved", "sourceUrl": "https://jobs.example.test/org"})
	status, body = h.owner(http.MethodPost, "/opportunities", string(input))
	requireStatus(t, status, http.StatusCreated, body)
	id := decodeObject(t, body)["opportunity"].(map[string]any)["id"].(string)
	status, body = h.owner(http.MethodGet, "/opportunities/"+id+"/organisation")
	requireStatus(t, status, http.StatusOK, body)
	if got := decodeObject(t, body)["status"]; got != "pending" {
		t.Fatalf("organisation status %v", got)
	}
	status, body = h.owner(http.MethodGet, "/organisation/summaries?ids="+id)
	requireStatus(t, status, http.StatusOK, body)
	items := decodeObject(t, body)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("summaries: %+v", items)
	}
	_, readToken, err := h.service.CreateAgent(context.Background(), owner, "org-reader", []string{"opportunities:read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	status, body = h.do(http.MethodGet, "/opportunities/"+id+"/organisation", "", readToken, "", "", nil)
	requireStatus(t, status, http.StatusOK, body)
	status, body = h.do(http.MethodPut, "/organisation/categories", `{"expectedVersion":2,"categories":[]}`, readToken, "", "", nil)
	requireStatus(t, status, http.StatusForbidden, body)
}

func TestOrganisationModelKeepsCapturedDefinitionSeparateFromSource(t *testing.T) {
	input, _ := json.Marshal(jev.OrganisationInput{
		CategorySetVersion: 4,
		Categories:         []jev.OrganisationCategory{{ID: "chosen", Description: "Captured old description"}},
		Facts:              []jev.OrganisationFact{{ID: "fact-1", SourceID: "source-1", SourceRevision: "sha", SourceKind: "vacancy_snapshot", Excerpt: "remote role"}},
	})
	result, _ := json.Marshal(jev.OrganisationResult{
		Disposition: jev.OrganisationCategorySelected, CategoryID: "chosen",
		SourceRefs: []jev.OrganisationSourceRef{{FactID: "fact-1", SourceID: "source-1", SourceRevision: "sha", SourceKind: "vacancy_snapshot"}},
	})
	model, err := organisationAssessmentModel(store.OrganisationAssessment{
		ID: "assessment", CategoryVersion: 4, Disposition: "category_selected", CategoryID: "chosen",
		InputJSON: input, ResultJSON: result, RequestedModel: "requested", ReturnedModel: "returned", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.CategoryDescription == nil || *model.CategoryDescription != "Captured old description" ||
		len(model.SourceFacts) != 1 || model.SourceFacts[0].Excerpt != "remote role" ||
		len(model.SourceRefs) != 1 || model.SourceRefs[0].SourceId != "source-1" {
		t.Fatalf("captured model %+v", model)
	}
}
