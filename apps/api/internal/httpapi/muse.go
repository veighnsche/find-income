package httpapi

import (
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
)

func (h *Handler) museReadiness(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	var tier musecode.Tier
	switch r.URL.Query().Get("tier") {
	case "contributor":
		tier = musecode.TierContributor
	case "standard":
		tier = musecode.TierStandard
	default:
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Tier must be contributor or standard.")
		return
	}
	if h.muse == nil {
		writeJSON(w, http.StatusOK, generated.MuseReadiness{
			State: generated.MuseReadinessStateNeedsSetup, Code: musecode.CodeNotConfigured,
			Detail: "Muse discovery backends are not wired.", Tier: generated.MuseReadinessTier(tier),
		})
		return
	}
	status := h.muse.Readiness(tier)
	writeJSON(w, http.StatusOK, generated.MuseReadiness{
		State: museReadinessState(status.Code), Code: status.Code,
		Detail: status.Detail, Tier: generated.MuseReadinessTier(tier),
	})
}

func museReadinessState(code string) generated.MuseReadinessState {
	switch code {
	case musecode.CodeReady:
		return generated.MuseReadinessStateReady
	case musecode.CodeNotConfigured:
		return generated.MuseReadinessStateNeedsSetup
	default:
		return generated.MuseReadinessStateUnavailable
	}
}

func (h *Handler) museCheckpoints(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	runRef := r.URL.Query().Get("runRef")
	if runRef == "" || len(runRef) > 100 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "A run ref up to 100 characters is required.")
		return
	}
	if h.muse == nil {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Muse run not found.")
		return
	}
	checkpoints, err := h.muse.Checkpoints(r.Context(), runRef)
	if err != nil {
		failMuse(w, err)
		return
	}
	out := make([]generated.MuseRunCheckpoint, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		out = append(out, generated.MuseRunCheckpoint{
			RunRef: checkpoint.RunRef, Tier: generated.MuseRunCheckpointTier(checkpoint.Tier),
			SavedCount: checkpoint.SavedCount, LastSavedReceipt: checkpoint.LastSavedReceipt,
			SavedRefs: checkpoint.SavedRefs, UpdatedAt: checkpoint.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) museReport(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	runRef := r.URL.Query().Get("runRef")
	if runRef == "" || len(runRef) > 100 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "A run ref up to 100 characters is required.")
		return
	}
	if h.muse == nil {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Muse run not found.")
		return
	}
	report, err := h.muse.Report(r.Context(), runRef)
	if err != nil {
		failMuse(w, err)
		return
	}
	writeJSON(w, http.StatusOK, generated.MuseRunReport{
		RunRef: report.RunRef, Tier: generated.MuseRunReportTier(report.Tier),
		Outcome: generated.MuseRunReportOutcome(report.Outcome), Detail: report.Detail,
		SavedRefs: report.SavedRefs, Searched: report.Searched, Reused: report.Reused,
		Gaps: report.Gaps, NextAction: report.NextAction,
	})
}

func (h *Handler) museCommissions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	var count int64
	if h.muse != nil {
		count = h.muse.Commissions()
	}
	writeJSON(w, http.StatusOK, generated.MuseCommissions{Count: int(count)})
}

func failMuse(w http.ResponseWriter, err error) {
	if errors.Is(err, musewire.ErrUnknownRun) {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Muse run not found.")
		return
	}
	fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read the Muse run.")
}
