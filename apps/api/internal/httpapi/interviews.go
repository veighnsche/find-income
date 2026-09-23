package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/interviewprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (h *Handler) prepareInterview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.rounds == nil {
		failRound(w, store.ErrFenced)
		return
	}
	var body generated.PrepareInterviewRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	commission, created, err := h.database.CommissionInterview(r.Context(), actor, store.InterviewCommissionInput{RequestKey: body.RequestKey, OpportunityID: body.OpportunityId, Context: body.Context})
	if err != nil {
		failRound(w, err)
		return
	}
	if commission.RoundID != "" {
		prior, err := h.database.Round(r.Context(), commission.RoundID)
		if err != nil {
			failRound(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"interviewId": commission.ID, "round": roundModel(prior)})
		return
	}
	input := store.StartRoundInput{RequestKey: body.RequestKey, Intent: "Prepare one sourced private interview brief from the owner's complete supplied context.", Outcome: "interview_prepare", ProfileVersion: commission.ProfileVersion, Deadline: time.Now().Add(30 * time.Minute).UTC(), Scope: store.RoundScope{InputRefs: []string{"interview:" + commission.ID}, Resources: []string{"opportunity:" + commission.OpportunityID}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundInterviewBriefSave, store.RoundJevRequest}, Delegates: []string{"codex-runner"}}, Limits: store.RoundAllowance{Requests: 4, Items: 1, Tools: 5, Turns: 1}}
	round, roundCreated, err := h.rounds.Start(r.Context(), actor, input)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created && roundCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"interviewId": commission.ID, "round": roundModel(round)})
}

func (h *Handler) listInterviews(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	items, err := h.database.OwnerInterviews(r.Context(), p.ID)
	if err != nil {
		failStore(w, err, "list interviews")
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, v := range items {
		view, err := interviewView(v)
		if err != nil {
			failStore(w, err, "read interview")
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (h *Handler) getInterview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	v, err := h.database.OwnerInterview(r.Context(), p.ID, r.PathValue("id"))
	if err != nil {
		failStore(w, err, "read interview")
		return
	}
	debriefs, err := h.database.OwnerInterviewDebriefs(r.Context(), p.ID, v.ID)
	if err != nil {
		failStore(w, err, "read interview debriefs")
		return
	}
	view, err := interviewView(v)
	if err != nil {
		failStore(w, err, "read interview")
		return
	}
	debriefViews := make([]map[string]any, 0, len(debriefs))
	for _, d := range debriefs {
		item, err := interviewDebriefView(d)
		if err != nil {
			failStore(w, err, "read interview debrief")
			return
		}
		debriefViews = append(debriefViews, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"interview": view, "debriefs": debriefViews})
}

func interviewView(v store.Interview) (map[string]any, error) {
	view := map[string]any{"id": v.ID, "opportunityId": v.OpportunityID, "opportunityRevision": v.OpportunityRevision, "profileVersion": v.ProfileVersion, "context": v.Context, "contextSha256": v.ContextSHA256, "current": v.Current, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
	if v.RoundID != "" {
		view["roundId"] = v.RoundID
	}
	if len(v.Brief) != 0 {
		view["brief"] = v.Brief
	}
	if len(v.Focus) != 0 {
		var prepared struct {
			Selection struct {
				Disposition string `json:"disposition"`
				SelectedID  string `json:"selected_id"`
				InputSHA256 string `json:"input_sha256"`
			} `json:"selection"`
		}
		if err := json.Unmarshal(v.Focus, &prepared); err != nil {
			return nil, err
		}
		focus := map[string]any{"disposition": prepared.Selection.Disposition, "inputSha256": prepared.Selection.InputSHA256, "jevAttemptId": v.FocusJevAttemptID}
		if prepared.Selection.SelectedID != "" {
			focus["selectedId"] = prepared.Selection.SelectedID
		}
		view["focus"] = focus
	}
	return view, nil
}

func interviewDebriefView(d store.InterviewDebrief) (map[string]any, error) {
	view := map[string]any{"id": d.ID, "interviewId": d.InterviewID, "notes": d.Notes, "createdAt": d.CreatedAt, "updatedAt": d.UpdatedAt}
	if d.RoundID != "" {
		view["roundId"] = d.RoundID
	}
	if len(d.Debrief) != 0 {
		var debrief interviewprep.Debrief
		if err := json.Unmarshal(d.Debrief, &debrief); err != nil {
			return nil, err
		}
		view["observations"] = debrief.Input.Observations
		view["unknowns"] = debrief.Input.Unknowns
		view["attribution"] = debrief.Attribution
	}
	return view, nil
}

func (h *Handler) debriefInterview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.rounds == nil {
		failRound(w, store.ErrFenced)
		return
	}
	var body generated.DebriefInterviewRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	interviewID := r.PathValue("id")
	interview, err := h.database.OwnerInterview(r.Context(), p.ID, interviewID)
	if err != nil {
		failRound(w, err)
		return
	}
	commission, created, err := h.database.CommissionInterviewDebrief(r.Context(), actor, store.InterviewDebriefCommissionInput{RequestKey: body.RequestKey, InterviewID: interviewID, Notes: body.Notes})
	if err != nil {
		failRound(w, err)
		return
	}
	if commission.RoundID != "" {
		prior, err := h.database.Round(r.Context(), commission.RoundID)
		if err != nil {
			failRound(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"debriefId": commission.ID, "interviewId": interviewID, "round": roundModel(prior)})
		return
	}
	if !interview.Current {
		failRound(w, store.ErrConflict)
		return
	}
	input := store.StartRoundInput{RequestKey: body.RequestKey, Intent: "Record one owner-reported interview debrief with exact note citations.", Outcome: "interview_debrief", ProfileVersion: interview.ProfileVersion, Deadline: time.Now().Add(30 * time.Minute).UTC(), Scope: store.RoundScope{InputRefs: []string{"debrief:" + commission.ID}, Resources: []string{"interview:" + interviewID}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundInterviewDebriefSave}, Delegates: []string{"codex-runner"}}, Limits: store.RoundAllowance{Requests: 3, Items: 1, Tools: 5, Turns: 1}}
	round, roundCreated, err := h.rounds.Start(r.Context(), actor, input)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created && roundCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"debriefId": commission.ID, "interviewId": interviewID, "round": roundModel(round)})
}
