// Package organisation applies Jev's reversible category choice to a saved
// sourced vacancy. It never changes qualification or application state.
package organisation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jobs"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const maxFactBytes = 2000
const maxFacts = 16

func sourcedFacts(snapshot store.OrganisationSnapshot) ([]jev.OrganisationFact, error) {
	text := snapshot.OriginalText
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return nil, store.ErrInvalid
	}
	var facts []jev.OrganisationFact
	segments := (len(text) + maxFactBytes - 1) / maxFactBytes
	if segments > maxFacts {
		segments = maxFacts
	}
	for i := 0; i < segments; i++ {
		start := i * maxFactBytes
		if len(text) > maxFactBytes*maxFacts {
			// Cover the complete long source with bounded, deterministic excerpts.
			start = i * (len(text) - maxFactBytes) / (segments - 1)
		}
		for start < len(text) && !utf8.RuneStart(text[start]) {
			start++
		}
		end := start + maxFactBytes
		if end > len(text) {
			end = len(text)
		}
		for end < len(text) && end > start && !utf8.RuneStart(text[end]) {
			end--
		}
		if end <= start {
			return nil, store.ErrInvalid
		}
		excerpt := strings.TrimSpace(text[start:end])
		if excerpt != "" {
			facts = append(facts, jev.OrganisationFact{
				ID: fmt.Sprintf("vacancy-%02d", len(facts)+1), SourceID: snapshot.SourceID,
				SourceRevision: snapshot.SourceRevision, SourceKind: "vacancy_snapshot",
				ObservedAt: snapshot.SourceRecordedAt, Excerpt: excerpt})
		}
	}
	if len(facts) == 0 {
		return nil, store.ErrInvalid
	}
	return facts, nil
}

// Handler prepares current facts, calls Jev outside the database transaction,
// and commits only after the store rechecks category/source freshness and job
// lease. Register it with jobs.Worker under store.OrganisationJobKind.
func Handler(database *store.Store, evaluator jev.Evaluator) jobs.Handler {
	return func(ctx context.Context, claim store.Job) (store.JobResult, error) {
		if database == nil || evaluator == nil {
			return store.JobResult{}, jobs.Permanent("organisation_unavailable", store.ErrInvalid)
		}
		snapshot, err := database.OrganisationSnapshotForJob(ctx, claim)
		if errors.Is(err, store.ErrConflict) {
			return store.JobResult{Ref: "outdated"}, nil
		}
		if err != nil {
			return store.JobResult{}, jobs.Permanent("organisation_snapshot", err)
		}
		facts, err := sourcedFacts(snapshot)
		if err != nil {
			return store.JobResult{}, jobs.Permanent("organisation_source", err)
		}
		input := jev.OrganisationInput{CategorySetVersion: snapshot.CategorySet.Version, Facts: facts}
		for _, category := range snapshot.CategorySet.Categories {
			input.Categories = append(input.Categories, jev.OrganisationCategory{ID: category.ID, Description: category.Description})
		}
		result, err := jev.Organise(ctx, evaluator, input)
		if err != nil {
			return store.JobResult{}, classifyJev(err)
		}
		assessment, err := database.ApplyOrganisation(ctx, claim, input, result)
		if errors.Is(err, store.ErrConflict) {
			return store.JobResult{Ref: "outdated"}, nil
		}
		if err != nil {
			return store.JobResult{}, jobs.Permanent("organisation_apply", err)
		}
		return store.JobResult{Ref: assessment.ID}, nil
	}
}

func classifyJev(err error) error {
	var provider *jev.Error
	if errors.As(err, &provider) {
		switch provider.Kind {
		case jev.ErrRateLimited, jev.ErrUnavailable:
			if provider.RetryAfter > 0 {
				return jobs.TransientAfter("jev_unavailable", "Organisation will retry after the provider delay.", err, provider.RetryAfter)
			}
			return jobs.Transient("jev_unavailable", err)
		case jev.ErrCanceled:
			return jobs.Transient("jev_cancelled", err)
		default:
			return jobs.Permanent("jev_"+string(provider.Kind), err)
		}
	}
	return jobs.Permanent("jev_error", err)
}
