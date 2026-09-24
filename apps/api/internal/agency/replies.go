package agency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (e *Engine) checkReply(ctx context.Context, outcome string) error {
	if e == nil || e.Store == nil || e.Runtime == nil {
		return errors.New("reply processing unavailable")
	}
	if e.ReplyIntent == nil {
		return errors.New("reply intent evaluator unavailable")
	}
	return e.Runtime.CheckRound(ctx, outcome)
}

func (e *Engine) launchReply(r store.Round) error {
	if r.State != store.RoundRunning || r.Outcome != "process_replies" ||
		len(r.Scope.Resources) != 2 || r.Scope.Resources[1] != "campaign:active" {
		return store.ErrFenced
	}
	base := e.Context
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithDeadline(base, r.Deadline)
	if err := e.checkReply(ctx, r.Outcome); err != nil {
		cancel()
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = map[string]*activeWorker{}
	}
	if _, exists := e.active[r.ID]; exists {
		cancel()
		return store.ErrConflict
	}
	worker := &activeWorker{cancel: cancel, done: make(chan struct{})}
	e.active[r.ID] = worker
	go func() { defer cancel(); defer e.workerDone(r.ID, worker); e.runReplyProcessing(ctx, r) }()
	return nil
}

type replyOutcome struct {
	Code           string              `json:"code"`
	ProcessingID   string              `json:"processingId,omitempty"`
	Intent         string              `json:"intent,omitempty"`
	UpdatesSaved   bool                `json:"updatesSaved"`
	DraftSaved     bool                `json:"draftSaved"`
	Unknowns       []string            `json:"unknowns"`
	Recommendation *homeRecommendation `json:"recommendation,omitempty"`
}

func (e *Engine) finishReplyProcessing(ctx context.Context, initial store.Round, result replyOutcome) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	r, err := e.Store.Round(cleanup, initial.ID)
	if err != nil || r.State != store.RoundRunning {
		return
	}
	if !time.Now().Before(r.Deadline) {
		_, _ = e.Store.ExpireRound(cleanup, r.ID)
		return
	}
	if result.Unknowns == nil {
		result.Unknowns = []string{}
	}
	applied := 0
	if result.UpdatesSaved {
		applied++
	}
	if result.DraftSaved {
		applied++
	}
	facts := outcomeRecommendationFacts{Outcome: r.Outcome, Code: result.Code, AppliedChanges: applied, UnresolvedCount: len(result.Unknowns)}
	if result.ProcessingID != "" {
		if saved, readErr := e.Store.ReplyProcessing(cleanup, result.ProcessingID); readErr == nil && saved.RoundID == r.ID && saved.Intent != "" {
			facts.ResultID, facts.ResultUpdatedAt, facts.Intent = saved.ID, saved.UpdatedAt, saved.Intent
		}
	}
	result.Recommendation = e.computeOutcomeRecommendation(ctx, r, facts)
	encoded, _ := json.Marshal(result)
	status := "complete"
	if len(result.Unknowns) > 0 || !result.DraftSaved {
		status = "partial"
	}
	_, _ = e.Store.FinishRound(cleanup, initial.Actor, r.ID, store.RoundCompleted, result.Code, status, encoded)
}

func (e *Engine) runReplyProcessing(ctx context.Context, initial store.Round) {
	result := replyOutcome{Code: "replies_unresolved"}
	defer func() {
		if !errors.Is(ctx.Err(), context.Canceled) {
			e.finishReplyProcessing(ctx, initial, result)
		}
	}()
	r, err := e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		result.Code = terminalCode(err)
		return
	}
	id, err := roundInterviewRef(r, "replies:")
	if err != nil {
		result.Code = "replies_scope_invalid"
		return
	}
	result.ProcessingID = id
	processing, err := e.Store.ReplyProcessing(ctx, id)
	if err != nil || processing.RoundID != r.ID || r.Scope.Resources[0] != "thread:"+processing.ThreadID {
		result.Code = "replies_context_stale"
		return
	}
	thread, err := e.Store.OwnerCorrespondenceThread(ctx, r.Actor.ID, processing.ThreadID)
	if err != nil {
		result.Code = "replies_context_stale"
		return
	}
	messages, err := e.Store.CorrespondenceThreadMessages(ctx, r.Actor.ID, thread.ID)
	if err != nil || len(messages) == 0 {
		result.Code = "replies_context_stale"
		return
	}
	intent, status := e.assessReplyIntent(ctx, r, processing, thread, messages)
	processing.Intent = intent
	switch status {
	case "selected":
		result.Intent = intent
	case "unresolved":
		result.Unknowns = []string{"Jev could not choose a supported intent; the complete thread remains available for review."}
	default:
		result.Code = "replies_intent_" + status
		result.Unknowns = []string{"The reply intent requires review; the complete thread remains available."}
		return
	}
	key := "replies:" + id
	if prior, err := e.Store.RoundAttemptForRequest(ctx, r.ID, key); err == nil {
		result.Code = "replies_turn_already_dispatched"
		result.Unknowns = []string{"A prior reply turn exists; its outcome must be reviewed before another commission."}
		if prior.State == store.AttemptSucceeded {
			result.Code = "replies_result_missing"
		}
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		result.Code = "replies_attempt_unavailable"
		return
	}
	evidence, err := replyTurnEvidence(thread, messages)
	if err != nil {
		result.Code = "replies_context_too_large"
		result.Unknowns = []string{"The complete thread exceeds this bounded turn."}
		return
	}
	_, err = e.Runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID,
		codexservice.RoundTurnInput{RequestKey: key, ResourceID: "thread:" + thread.ID,
			Brief: "Process one correspondence thread from its complete saved messages. Call reply_update once to link the exact current opportunity, then reply_draft once with a cited follow-up draft grounded in exact thread excerpts. Attribute conflicting claims to their exact message. State unknown dates, parties, or terms as unknown. Do not invent a sender fact or hiring decision; do not send, book, or message anything.", Evidence: evidence})
	if err != nil {
		result.Code = terminalCode(err)
		result.Unknowns = []string{"The Codex turn did not settle; the saved thread and intent remain readable while its status is reviewed."}
		return
	}
	if _, err = e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion); err != nil {
		result.Code = terminalCode(err)
		return
	}
	processing, err = e.Store.ReplyProcessing(ctx, id)
	if err != nil {
		result.Code = "replies_result_unavailable"
		return
	}
	result.UpdatesSaved = processing.OpportunityID != ""
	if _, err := e.Store.ReplyDraftForProcessing(ctx, id); err == nil {
		result.DraftSaved = true
	} else if !errors.Is(err, store.ErrNotFound) {
		result.Code = "replies_result_unavailable"
		return
	}
	if !result.DraftSaved {
		result.Code = "replies_draft_missing"
		result.Unknowns = append(result.Unknowns, "No cited follow-up draft was saved.")
		return
	}
	if result.Intent == "" {
		result.Code = "replies_intent_unresolved"
		return
	}
	result.Code = "replies_processed"
}

func (e *Engine) assessReplyIntent(ctx context.Context, r store.Round, processing store.ReplyProcessing, thread store.CorrespondenceThread, messages []store.CorrespondenceMessage) (string, string) {
	if processing.Intent != "" {
		return processing.Intent, "selected"
	}
	if e.ReplyIntent == nil {
		return "", "unavailable"
	}
	account, err := e.Store.OwnerCorrespondenceAccount(ctx, r.Actor.ID, thread.AccountID)
	if err != nil {
		return "", "unavailable"
	}
	input, err := replyIntentInput(thread, account.ExternalAccountID, messages)
	if err != nil {
		return "", "invalid"
	}
	prefix := "reply-intent:" + processing.ID
	attempt, err := e.Store.RoundAttemptForRequest(ctx, r.ID, prefix+"/0")
	var selected jev.ReplyIntentResult
	var jevID string
	if err == nil {
		if attempt.Operation != store.RoundJevRequest || attempt.ResourceID != "thread:"+thread.ID {
			return "", "invalid"
		}
		if attempt.State != store.AttemptSucceeded && attempt.State != store.AttemptObservedSuccess {
			return "", "uncertain"
		}
		saved, err := e.Store.JevAttemptsForRound(ctx, r.ID)
		if err != nil {
			return "", "unavailable"
		}
		var capture *store.JevAttempt
		for i := range saved {
			if saved[i].RoundAttemptID == attempt.ID {
				capture = &saved[i]
				break
			}
		}
		if capture == nil || capture.Status != "succeeded" || capture.ResponseTruncated || capture.ResponseReadError || capture.InputTokens == nil || capture.OutputTokens == nil {
			return "", "uncertain"
		}
		selected, err = jev.RecoverCapturedReplyIntent(input, capture.LogicalRequestJSON, capture.RawResponseBytes, capture.RequestedModel)
		if err != nil || selected.ProviderResult.ReturnedModel != capture.ReturnedModel || selected.ProviderResult.Usage.InputTokens != *capture.InputTokens || selected.ProviderResult.Usage.OutputTokens != *capture.OutputTokens {
			return "", "invalid"
		}
		jevID = capture.ID
	} else if errors.Is(err, store.ErrNotFound) {
		if _, err := e.live(ctx, r.ID, r.Generation, r.ProfileVersion); err != nil {
			return "", "stale"
		}
		selected, err = e.ReplyIntent.RunReplyIntent(ctx, jevservice.Binding{Actor: store.Actor{Kind: "agent", ID: "codex-runner"}, RoundID: r.ID,
			ResourceID: "thread:" + thread.ID, RequestKeyPrefix: prefix, ProfileVersion: r.ProfileVersion, MaxReportedTokens: 2500}, input)
		if err != nil {
			return "", "unavailable"
		}
		ids, err := e.Store.JevAttemptIDsForRequestPrefix(ctx, r.ID, prefix)
		if err != nil || len(ids) != 1 {
			return "", "uncertain"
		}
		jevID = ids[0]
	} else {
		return "", "unavailable"
	}
	if selected.Disposition == jev.ReplyIntentUnresolved {
		return "", "unresolved"
	}
	if _, err = e.live(ctx, r.ID, r.Generation, r.ProfileVersion); err != nil {
		return "", "stale"
	}
	if _, err := e.Store.ApplyReplyIntent(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, r.Generation, processing.ID, jevID, selected.SelectedID); err != nil {
		return "", "unavailable"
	}
	return selected.SelectedID, "selected"
}

func replyIntentInput(thread store.CorrespondenceThread, ownerAddress string, messages []store.CorrespondenceMessage) (jev.ReplyIntentInput, error) {
	if len(messages) == 0 || len(messages) > 60 {
		return jev.ReplyIntentInput{}, store.ErrInvalid
	}
	var context []jev.ReplyIntentContext
	var evidence []jev.ReplyIntentEvidence
	used := 0
	for i, message := range messages {
		if i >= 12 {
			break
		}
		body := prefixUTF8(message.Body, 4000)
		sum := sha256.Sum256([]byte(body))
		kind := "inbound"
		if message.Sender == ownerAddress {
			kind = "outbound"
		}
		context = append(context, jev.ReplyIntentContext{ID: message.ID, Kind: kind, Revision: message.ProviderMessageID, SHA256: hex.EncodeToString(sum[:]), Body: body})
		used += len(body)
		if used > 30000 {
			return jev.ReplyIntentInput{}, store.ErrInvalid
		}
		if kind == "inbound" && len(evidence) < 8 {
			excerpt := prefixUTF8(message.Body, 2000)
			evidence = append(evidence, jev.ReplyIntentEvidence{ID: "excerpt-" + message.ID, SourceID: message.ID, SourceRevision: message.ProviderMessageID, SourceKind: "inbound_message", SourceSHA256: message.BodySHA256, Excerpt: excerpt})
		}
	}
	if len(evidence) == 0 {
		return jev.ReplyIntentInput{}, store.ErrInvalid
	}
	evidenceIDs := make([]string, 0, len(evidence))
	for _, item := range evidence {
		evidenceIDs = append(evidenceIDs, item.ID)
	}
	candidates := make([]jev.ReplyIntentCandidate, 0, len(jev.ReplyIntentTaxonomy))
	for _, id := range jev.ReplyIntentTaxonomy {
		candidates = append(candidates, jev.ReplyIntentCandidate{ID: id, Description: replyIntentDescription(id), EvidenceIDs: evidenceIDs})
	}
	return jev.ReplyIntentInput{ThreadID: thread.ID, Context: context, Evidence: evidence, Candidates: candidates, MaxReportedTokens: 2500}, nil
}

func replyIntentDescription(id string) string {
	switch id {
	case "interview_invitation":
		return "The sender invites the owner to an interview."
	case "scheduling_exchange":
		return "The thread negotiates interview or call scheduling."
	case "information_request":
		return "The sender asks the owner for information or materials."
	case "offer_terms":
		return "The thread discusses offer terms or compensation."
	case "rejection":
		return "The sender declines the owner for this opening."
	case "referral_introduction":
		return "The thread introduces a referral contact or route."
	case "follow_up_nudge":
		return "The thread needs an owner follow-up without new inbound facts."
	default:
		return "The message needs no owner action."
	}
}

func replyTurnEvidence(thread store.CorrespondenceThread, messages []store.CorrespondenceMessage) (string, error) {
	type excerpt struct {
		ID      string `json:"id"`
		SHA256  string `json:"sha256"`
		Sender  string `json:"sender"`
		SentAt  string `json:"sentAt"`
		Body    string `json:"body"`
		Omitted int    `json:"omittedBytes"`
	}
	var parts []excerpt
	for _, message := range messages {
		part := prefixUTF8(message.Body, 5000)
		parts = append(parts, excerpt{message.ID, message.BodySHA256, message.Sender, message.SentAt, part, len(message.Body) - len(part)})
	}
	encode := func() ([]byte, error) {
		return json.Marshal(struct {
			ThreadID string    `json:"threadId"`
			Subject  string    `json:"subject"`
			Messages []excerpt `json:"messages"`
		}{thread.ID, thread.Subject, parts})
	}
	data, err := encode()
	if err == nil && len(data) > 32000 {
		for i := range parts {
			full := parts[i].Body
			parts[i].Body = prefixUTF8(full, 1200)
			parts[i].Omitted += len(full) - len(parts[i].Body)
		}
		data, err = encode()
	}
	if err != nil || len(data) > 32000 {
		return "", store.ErrInvalid
	}
	return string(data), nil
}
