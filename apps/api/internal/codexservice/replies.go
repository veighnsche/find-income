package codexservice

import (
	"context"
	"encoding/json"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/replydraft"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type replyUpdateArgs struct {
	RoundID       string `json:"roundId"`
	Capability    string `json:"capability"`
	RequestKey    string `json:"requestKey"`
	ProcessingID  string `json:"processingId"`
	OpportunityID string `json:"opportunityId"`
}

type replyDraftArgs struct {
	RoundID      string                     `json:"roundId"`
	Capability   string                     `json:"capability"`
	RequestKey   string                     `json:"requestKey"`
	ProcessingID string                     `json:"processingId"`
	Body         string                     `json:"body"`
	Citations    []replydraft.DraftCitation `json:"citations"`
	Unknowns     []string                   `json:"unknowns,omitempty"`
}

func (s *Service) replyUpdateTool(ctx context.Context, args replyUpdateArgs) (map[string]any, error) {
	if args.RoundID == "" || args.Capability == "" || args.RequestKey == "" || args.ProcessingID == "" || args.OpportunityID == "" {
		return nil, store.ErrInvalid
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	r, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return nil, err
	}
	if r.State != store.RoundRunning || r.Outcome != "process_replies" || !scopeContains(r.Scope.InputRefs, "replies:"+args.ProcessingID) || !scopeContains(r.Scope.Operations, store.RoundReplyUpdateSave) {
		return nil, store.ErrFenced
	}
	processing, err := s.db.ReplyProcessing(ctx, args.ProcessingID)
	if err != nil {
		return nil, err
	}
	if processing.RoundID != r.ID || !scopeContains(r.Scope.Resources, "thread:"+processing.ThreadID) {
		return nil, store.ErrFenced
	}
	opportunity, err := s.db.Opportunity(ctx, args.OpportunityID)
	if err != nil {
		return nil, err
	}
	_, created, err := s.db.ApplyRoundMutation(ctx, authority.Actor, r.ID, store.RoundMutationInput{RequestKey: args.RequestKey, Operation: store.RoundReplyUpdateSave, ResourceID: "thread:" + processing.ThreadID, ExpectedRevision: opportunity.Revision, ReplyUpdate: &store.ReplyUpdateMutation{ProcessingID: processing.ID, ThreadID: processing.ThreadID, OpportunityID: opportunity.ID}, Capability: args.Capability})
	if err != nil {
		return nil, err
	}
	return map[string]any{"processingId": processing.ID, "opportunityId": opportunity.ID, "created": created}, nil
}

func (s *Service) replyDraftTool(ctx context.Context, args replyDraftArgs) (map[string]any, error) {
	if args.RoundID == "" || args.Capability == "" || args.RequestKey == "" || args.ProcessingID == "" {
		return nil, store.ErrInvalid
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	r, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return nil, err
	}
	if r.State != store.RoundRunning || r.Outcome != "process_replies" || !scopeContains(r.Scope.InputRefs, "replies:"+args.ProcessingID) || !scopeContains(r.Scope.Operations, store.RoundReplyDraftSave) {
		return nil, store.ErrFenced
	}
	processing, err := s.db.ReplyProcessing(ctx, args.ProcessingID)
	if err != nil {
		return nil, err
	}
	if processing.RoundID != r.ID || !scopeContains(r.Scope.Resources, "thread:"+processing.ThreadID) {
		return nil, store.ErrFenced
	}
	messages, err := s.db.CorrespondenceThreadMessages(ctx, r.Actor.ID, processing.ThreadID)
	if err != nil {
		return nil, err
	}
	bodies := make(map[string]string, len(messages))
	for _, message := range messages {
		bodies[message.ID] = message.Body
	}
	validated, err := replydraft.ValidateDraft(replydraft.DraftInput{ProcessingID: processing.ID, ThreadID: processing.ThreadID, Body: args.Body, Citations: args.Citations, Unknowns: args.Unknowns}, bodies)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(validated)
	result, created, err := s.db.ApplyRoundMutation(ctx, authority.Actor, r.ID, store.RoundMutationInput{RequestKey: args.RequestKey, Operation: store.RoundReplyDraftSave, ResourceID: "thread:" + processing.ThreadID, ExpectedRevision: 1, ReplyDraft: &store.ReplyDraftMutation{ProcessingID: processing.ID, ThreadID: processing.ThreadID, DraftJSON: encoded}, Capability: args.Capability})
	if err != nil {
		return nil, err
	}
	return map[string]any{"draftId": result.EntityID, "created": created}, nil
}

// recoverReplyDispatch settles only a complete captured Jev intent request.
// Unknown Codex turns continue through normal terminal observation.
func (s *Service) recoverReplyDispatch(ctx context.Context, roundID, attemptID string, generation int64) (bool, bool, error) {
	if s == nil || s.db == nil {
		return false, false, ErrUnavailable
	}
	round, err := s.db.Round(ctx, roundID)
	if err != nil {
		return false, false, err
	}
	if round.Outcome != "process_replies" {
		return false, false, nil
	}
	attempt, err := s.db.RoundAttempt(ctx, attemptID)
	if err != nil {
		return false, false, err
	}
	if attempt.Operation != store.RoundJevRequest {
		return false, false, nil
	}
	if attempt.RoundID != roundID || attempt.State != store.AttemptUncertain || round.State != store.RoundPaused || round.Generation != generation {
		return true, false, store.ErrFenced
	}
	items, err := s.db.JevAttemptsForRound(ctx, roundID)
	if err != nil {
		return true, false, err
	}
	var captured *store.JevAttempt
	for i := range items {
		if items[i].RoundAttemptID == attemptID {
			captured = &items[i]
			break
		}
	}
	if captured == nil || captured.Purpose != "reply_intent" || captured.Status != "succeeded" || captured.ResponseTruncated || captured.ResponseReadError || captured.InputTokens == nil || captured.OutputTokens == nil {
		return true, false, nil
	}
	var recorded struct {
		State jev.ReplyIntentInput `json:"state"`
	}
	if json.Unmarshal(captured.LogicalRequestJSON, &recorded) != nil {
		return true, false, nil
	}
	result, err := jev.RecoverCapturedReplyIntent(recorded.State, captured.LogicalRequestJSON, captured.RawResponseBytes, captured.RequestedModel)
	if err != nil || result.ProviderResult.ReturnedModel != captured.ReturnedModel || result.ProviderResult.Usage.InputTokens != *captured.InputTokens || result.ProviderResult.Usage.OutputTokens != *captured.OutputTokens {
		return true, false, nil
	}
	resolved, err := s.db.RecoverCapturedReplyIntentAttempt(ctx, roundID, attemptID, captured.ID, generation)
	return true, resolved, err
}
