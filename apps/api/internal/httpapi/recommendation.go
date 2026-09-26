package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/agency"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// latestCompletedRound lets an owner recover saved work after a fresh
// browser session without relying on a locally remembered round ID.
func (h *Handler) latestCompletedRound(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	outcomes := r.URL.Query()["outcome"]
	if len(outcomes) != 1 || !latestCompletedOutcomeAllowed(outcomes[0]) {
		failRound(w, store.ErrInvalid)
		return
	}
	round, err := h.database.LatestCompletedRound(r.Context(), owner.Actor(), outcomes[0])
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.roundWithRecommendationCurrentness(r.Context(), round))
}

func latestCompletedOutcomeAllowed(outcome string) bool {
	switch outcome {
	case "all", "process_input", "prepare", "research_run":
		return true
	default:
		return false
	}
}

// roundWithRecommendationCurrentness is called only after the existing
// getRound owner authorization. It adds a transient read-only verdict to the
// response copy of Report; the stored report remains immutable.
func (h *Handler) roundWithRecommendationCurrentness(ctx context.Context, round store.Round) roundResponse {
	view := roundModel(round)
	if !latestCompletedOutcomeAllowed(round.Outcome) || round.Outcome == "all" || len(round.Report) == 0 {
		return view
	}
	var report map[string]json.RawMessage
	if json.Unmarshal(round.Report, &report) != nil || report == nil {
		return view
	}
	verdict := agency.ReadHomeRecommendationCurrentness(ctx, h.database, round)
	encoded, err := json.Marshal(verdict)
	if err != nil {
		return view
	}
	report["recommendationCurrentness"] = encoded
	view.Report, err = json.Marshal(report)
	if err != nil {
		view.Report = round.Report
	}
	return view
}
