package applicationpacks

import (
	"context"
	"fmt"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type SavedReader interface {
	Opportunity(context.Context, string) (store.Opportunity, error)
	Company(context.Context, string) (store.Company, error)
	CurrentPreferences(context.Context) (store.Preferences, error)
}

// PrepareSavedOpportunity snapshots a selected saved role and the current
// profile, then renders. Its output must enter the round mutation promptly;
// that transaction rejects either revision if it has changed meanwhile.
func (renderer Renderer) PrepareSavedOpportunity(ctx context.Context, reader SavedReader, projectRoot, opportunityID, destination string, sourceNames []string, draft Draft) (Prepared, error) {
	if reader == nil || opportunityID == "" {
		return Prepared{}, ErrInvalid
	}
	opportunity, err := reader.Opportunity(ctx, opportunityID)
	if err != nil {
		return Prepared{}, err
	}
	if opportunity.ArchivedAt != "" {
		return Prepared{}, fmt.Errorf("%w: archived opportunity", ErrInvalid)
	}
	company, err := reader.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return Prepared{}, err
	}
	preferences, err := reader.CurrentPreferences(ctx)
	if err != nil {
		return Prepared{}, err
	}
	sources, template, err := LoadApprovedCareerSources(projectRoot, sourceNames)
	if err != nil {
		return Prepared{}, err
	}
	input := Input{Role: Role{
		OpportunityID: opportunity.ID, OpportunityRevision: opportunity.Revision, ProfileRevision: preferences.Version,
		Title: opportunity.Title, Company: company.Name, SourceURL: opportunity.SourceURL,
		Description: opportunity.OriginalText, Destination: destination,
	}, Sources: sources, Draft: draft, CVTemplate: template, TemplateSHA256: hash(template)}
	return renderer.Prepare(ctx, input)
}
