package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (h *Handler) processCorrespondenceThread(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.rounds == nil {
		failRound(w, store.ErrFenced)
		return
	}
	var body generated.ProcessRepliesRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	threadID := r.PathValue("id")
	thread, err := h.database.OwnerCorrespondenceThread(r.Context(), p.ID, threadID)
	if err != nil {
		failRound(w, err)
		return
	}
	commission, created, err := h.database.CommissionReplyProcessing(r.Context(), actor, store.ReplyProcessingCommissionInput{RequestKey: body.RequestKey, ThreadID: threadID})
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
		writeJSON(w, http.StatusOK, map[string]any{"processingId": commission.ID, "threadId": threadID, "round": roundModel(prior)})
		return
	}
	input := store.StartRoundInput{RequestKey: body.RequestKey, Intent: "Interpret one correspondence thread and save a cited follow-up draft.", Outcome: "process_replies", ProfileVersion: commission.ProfileVersion, Deadline: time.Now().Add(30 * time.Minute).UTC(), Scope: store.RoundScope{InputRefs: []string{"replies:" + commission.ID}, Resources: []string{"thread:" + thread.ID, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundJevRequest, store.RoundReplyUpdateSave, store.RoundReplyDraftSave}, Delegates: []string{"codex-runner"}}, Limits: store.RoundAllowance{Requests: 5, Items: 2, Tools: 6, Turns: 1}}
	round, roundCreated, err := h.rounds.Start(r.Context(), actor, input)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created && roundCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"processingId": commission.ID, "threadId": threadID, "round": roundModel(round)})
}

func (h *Handler) listCorrespondenceThreads(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	items, err := h.database.OwnerCorrespondenceThreads(r.Context(), p.ID)
	if err != nil {
		failStore(w, err, "list correspondence threads")
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, v := range items {
		views = append(views, correspondenceThreadView(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (h *Handler) getCorrespondenceThread(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	v, err := h.database.OwnerCorrespondenceThread(r.Context(), p.ID, r.PathValue("id"))
	if err != nil {
		failStore(w, err, "read correspondence thread")
		return
	}
	messages, err := h.database.CorrespondenceThreadMessages(r.Context(), p.ID, v.ID)
	if err != nil {
		failStore(w, err, "read correspondence messages")
		return
	}
	views := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		views = append(views, map[string]any{"id": m.ID, "sender": m.Sender, "recipients": m.Recipients, "sentAt": m.SentAt, "bodySha256": m.BodySHA256, "body": m.Body})
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": correspondenceThreadView(v), "messages": views})
}

func correspondenceThreadView(v store.CorrespondenceThread) map[string]any {
	view := map[string]any{"id": v.ID, "accountId": v.AccountID, "subject": v.Subject, "lastMessageAt": v.LastMessageAt, "messageCount": v.MessageCount, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
	if v.OpportunityID != "" {
		view["opportunityId"] = v.OpportunityID
	}
	return view
}

func (h *Handler) getReplyProcessing(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	v, err := h.database.OwnerReplyProcessing(r.Context(), p.ID, r.PathValue("id"))
	if err != nil {
		failStore(w, err, "read reply processing")
		return
	}
	view := map[string]any{"id": v.ID, "threadId": v.ThreadID, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
	if v.RoundID != "" {
		view["roundId"] = v.RoundID
	}
	if v.Intent != "" {
		view["intent"] = v.Intent
	}
	if v.OpportunityID != "" {
		view["opportunityId"] = v.OpportunityID
	}
	if v.ProcessedAt != "" {
		view["processedAt"] = v.ProcessedAt
	}
	out := map[string]any{"processing": view}
	draft, err := h.database.ReplyDraftForProcessing(r.Context(), v.ID)
	if err == nil {
		var decoded any
		if decodeErr := json.Unmarshal(draft.Draft, &decoded); decodeErr != nil {
			failStore(w, decodeErr, "read reply draft")
			return
		}
		out["draft"] = map[string]any{"id": draft.ID, "revision": draft.Revision, "draftSha256": draft.DraftSHA256, "draft": decoded}
	} else if err != store.ErrNotFound {
		failStore(w, err, "read reply draft")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
