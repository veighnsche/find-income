package agency

import (
	"context"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// StoreClarifications adapts *store.Store to ClarificationStore. The
// store cannot import agency (agency owns the contract), so this file
// maps the identical shapes across the boundary.
type StoreClarifications struct {
	DB *store.Store
}

var _ ClarificationStore = (*StoreClarifications)(nil)

func toStoreRequirement(requirement ClarificationRequirement) store.ClarificationRequirement {
	return store.ClarificationRequirement{Statement: requirement.Statement,
		CaptureID: requirement.CaptureID, SpanStart: requirement.SpanStart, SpanEnd: requirement.SpanEnd}
}

func toStoreWork(refs []ClarificationWorkRef) []store.ClarificationWorkRef {
	out := make([]store.ClarificationWorkRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, store.ClarificationWorkRef{Kind: ref.Kind, ID: ref.ID})
	}
	return out
}

func toAgencyClarification(item store.Clarification) Clarification {
	return Clarification{ID: item.ID, OpportunityID: item.OpportunityID, CheckID: item.CheckID,
		Origin: item.Origin,
		Requirement: ClarificationRequirement{Statement: item.Requirement.Statement,
			CaptureID: item.Requirement.CaptureID, SpanStart: item.Requirement.SpanStart, SpanEnd: item.Requirement.SpanEnd},
		Prompt: item.Prompt, AffectedWork: toAgencyWork(item.AffectedWork),
		Status: item.Status, Answer: item.Answer, AnsweredAt: item.AnsweredAt,
		AnsweredBy: item.AnsweredBy, CreatedAt: item.CreatedAt}
}

func toAgencyWork(refs []store.ClarificationWorkRef) []ClarificationWorkRef {
	out := make([]ClarificationWorkRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ClarificationWorkRef{Kind: ref.Kind, ID: ref.ID})
	}
	return out
}

// OpenClarification implements ClarificationStore.
func (a *StoreClarifications) OpenClarification(ctx context.Context, actor store.Actor, opportunityID string, input ClarificationOpenInput) (Clarification, bool, error) {
	item, created, err := a.DB.OpenOwnerClarification(ctx, actor, opportunityID, store.ClarificationOpenInput{
		RequestKey: input.RequestKey, CheckID: input.CheckID,
		Requirement: toStoreRequirement(input.Requirement), Prompt: input.Prompt,
		AffectedWork: toStoreWork(input.AffectedWork)})
	if err != nil {
		return Clarification{}, false, err
	}
	return toAgencyClarification(item), created, nil
}

// AnswerClarification implements ClarificationStore.
func (a *StoreClarifications) AnswerClarification(ctx context.Context, actor store.Actor, id string, input ClarificationAnswer) (Clarification, error) {
	item, err := a.DB.AnswerOwnerClarification(ctx, actor, id, store.ClarificationAnswer{
		RequestKey: input.RequestKey, Text: input.Text})
	if err != nil {
		return Clarification{}, err
	}
	return toAgencyClarification(item), nil
}

// GetClarification implements ClarificationStore.
func (a *StoreClarifications) GetClarification(ctx context.Context, id string) (Clarification, error) {
	item, err := a.DB.GetOwnerClarification(ctx, id)
	if err != nil {
		return Clarification{}, err
	}
	return toAgencyClarification(item), nil
}

// ListClarifications implements ClarificationStore.
func (a *StoreClarifications) ListClarifications(ctx context.Context, opportunityID string) ([]Clarification, error) {
	items, err := a.DB.ListOwnerClarifications(ctx, opportunityID)
	if err != nil {
		return nil, err
	}
	out := make([]Clarification, 0, len(items))
	for _, item := range items {
		out = append(out, toAgencyClarification(item))
	}
	return out, nil
}
