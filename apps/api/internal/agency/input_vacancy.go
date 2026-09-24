package agency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (e *Engine) ownerVacancyEvidence(ctx context.Context, r store.Round, ingestionID string) (string, string, error) {
	item, err := e.Store.Ingestion(ctx, ingestionID)
	if err != nil {
		return "", "", err
	}
	resource := "ingestion:" + ingestionID
	if item.Actor != r.Actor || item.Origin != "owner" || item.SourceOpeningID == "" || !hasRoundResource(r.Scope.Resources, resource) {
		return "", "", store.ErrFenced
	}
	if strings.TrimSpace(item.OriginalText) == "" {
		for generation := int64(1); generation < r.Generation; generation++ {
			prior, readErr := e.Store.RoundAttemptForRequest(ctx, r.ID, fmt.Sprintf("read:%s:g%d", ingestionID, generation))
			if readErr != nil && !errors.Is(readErr, store.ErrNotFound) {
				return "", "", readErr
			}
			if readErr == nil && prior.DispatchedAt != "" {
				return "", "", store.ErrUncertain
			}
		}
		reader := e.InputReader
		if reader == nil {
			return "", "", ErrUnsupportedOwnerSource
		}
		cost, _ := store.RoundOperationCost(store.RoundFetchSource)
		attempt, created, reserveErr := e.Store.ReserveRoundAttempt(ctx, r.Actor, r.ID, store.RoundAttemptInput{RequestKey: fmt.Sprintf("read:%s:g%d", ingestionID, r.Generation), Operation: store.RoundFetchSource, ResourceID: resource, Cost: cost})
		if reserveErr != nil || !created {
			return "", "", store.ErrUncertain
		}
		if _, err := e.Store.MarkRoundDispatched(ctx, r.ID, attempt.ID); err != nil {
			return "", "", err
		}
		text, readErr := reader.ReadVacancy(ctx, item.SourceURL)
		if readErr != nil {
			_, _ = e.Store.FinishRoundAttempt(ctx, r.Actor, r.ID, attempt.ID, false, nil, "source_read_failed")
			return "", "", readErr
		}
		item, err = e.Store.PublishOwnerInputSourceText(ctx, r.Actor, r.ID, attempt.ID, ingestionID, text)
		if err != nil {
			return "", "", err
		}
	}
	if len(item.OriginalText) > 29000 {
		return "", "", store.ErrInvalid
	}
	encoded, err := json.Marshal(struct {
		IngestionID     string `json:"ingestionId"`
		SourceOpeningID string `json:"sourceOpeningId"`
		SourceURL       string `json:"sourceUrl"`
		OriginalText    string `json:"originalText"`
		ProfileVersion  int64  `json:"profileVersion"`
	}{item.ID, item.SourceOpeningID, item.SourceURL, item.OriginalText, r.ProfileVersion})
	if err != nil || len(encoded) > 32000 {
		return "", "", store.ErrInvalid
	}
	return "campaign:active", string(encoded), nil
}

func (e *Engine) organiseInputVacancy(ctx context.Context, r store.Round, ingestionID string) error {
	item, err := e.Store.Ingestion(ctx, ingestionID)
	if err != nil {
		return err
	}
	if item.OpportunityID == "" || item.SourceID == "" {
		return store.ErrConflict
	}
	if _, err := e.Store.InputOrganisationAssessment(ctx, r.ID, item.OpportunityID, item.SourceID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	opportunity, err := e.Store.Opportunity(ctx, item.OpportunityID)
	if err != nil {
		return err
	}
	source, err := e.Store.EvidenceSource(ctx, item.SourceID)
	if err != nil {
		return err
	}
	categories, err := e.Store.CurrentOrganisationCategories(ctx)
	if err != nil || len(categories.Categories) == 0 {
		return store.ErrConflict
	}
	input := jev.OrganisationInput{CategorySetVersion: categories.Version}
	for _, category := range categories.Categories {
		input.Categories = append(input.Categories, jev.OrganisationCategory{ID: category.ID, Description: category.Description})
	}
	remaining := source.OriginalText
	for len(remaining) > 0 && len(input.Facts) < 12 {
		part := prefixUTF8(remaining, 2000)
		input.Facts = append(input.Facts, jev.OrganisationFact{ID: fmt.Sprintf("%s:%d", source.ID, len(input.Facts)), SourceID: source.ID,
			SourceRevision: source.ContentSHA256, SourceKind: "vacancy_snapshot", ObservedAt: source.RecordedAt, Excerpt: part})
		remaining = remaining[len(part):]
	}
	if len(remaining) != 0 || len(input.Facts) == 0 {
		return store.ErrInvalid
	}
	prefix := fmt.Sprintf("input-organise:%s:g%d", ingestionID, r.Generation)
	result, err := e.Decisions.RunOrganisation(ctx, jevservice.Binding{Actor: r.Actor, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: prefix, ProfileVersion: r.ProfileVersion}, input)
	if err != nil {
		return err
	}
	attemptIDs, err := e.Store.JevAttemptIDsForRequestPrefix(ctx, r.ID, prefix)
	if err != nil {
		return err
	}
	binding := store.RoundAssessmentInput{Actor: r.Actor, RoundID: r.ID, RoundGeneration: r.Generation, ResourceID: "campaign:active",
		OpportunityID: opportunity.ID, OpportunityRevision: opportunity.Revision, SourceID: source.ID, SourceRevision: source.ContentSHA256,
		ProfileVersion: r.ProfileVersion, JevAttemptIDs: attemptIDs}
	_, err = e.Store.ApplyRoundOrganisation(ctx, binding, input, result)
	return err
}
