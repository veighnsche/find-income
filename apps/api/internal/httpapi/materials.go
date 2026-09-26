package httpapi

import (
	"context"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (h *Handler) materialUnavailable(w http.ResponseWriter, what string) {
	fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, what+" is not configured.")
}

// MaterialPreparer runs grounded preparation over the single canonical
// route-artifact set. Implemented by materialprep.Service; nil until the
// server wires it. There is no combined-pack workflow: no prepare, edit,
// rewrite, or version endpoint exists for packs.
type MaterialPreparer interface {
	// DraftOpportunityArtifacts drafts required held artifact types in
	// one bounded Standard turn and returns the stored readiness set.
	DraftOpportunityArtifacts(ctx context.Context, actor store.Actor, opportunityID, requestKey string,
		expectedCheckID, expectedQuestionSetSHA256 string, expectedWorkflowRevision int64) (store.ArtifactReadinessSet, bool, error)
}
