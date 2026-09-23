package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestOrganisationModelKeepsCapturedDefinitionSeparateFromSource(t *testing.T) {
	input, _ := json.Marshal(jev.OrganisationInput{
		CategorySetVersion: 4,
		Categories:         []jev.OrganisationCategory{{ID: "chosen", Description: "Captured old description"}},
		Facts:              []jev.OrganisationFact{{ID: "fact-1", SourceID: "source-1", SourceRevision: "sha", SourceKind: "vacancy_snapshot", Excerpt: "remote role"}},
	})
	result, _ := json.Marshal(jev.OrganisationResult{
		Disposition: jev.OrganisationCategorySelected, CategoryID: "chosen",
		RequestedModel: "requested", ReturnedModel: "returned",
		SourceRefs: []jev.OrganisationSourceRef{{FactID: "fact-1", SourceID: "source-1", SourceRevision: "sha", SourceKind: "vacancy_snapshot"}},
	})
	model, err := organisationAssessmentModel(store.RoundOrganisationProjection{Status: "selected", CategoryID: "chosen", Assessment: store.RoundJevAssessment{
		ID: "assessment", CategoryVersion: 4,
		InputJSON: input, ResultJSON: result, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	},
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
