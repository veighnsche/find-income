package materialprep

import (
	"context"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// JevRelevance adapts a Jev evaluator to the RelevanceAssessor contract via
// applicationpacks.AssessRelevance. The evaluator performs one bounded
// classification over caller-supplied text; it never researches. A nil
// evaluator reports ErrUnavailable so the HTTP layer stays honestly 503.
type JevRelevance struct {
	Evaluator jev.Evaluator
}

var _ RelevanceAssessor = JevRelevance{}

// AssessRelevance implements RelevanceAssessor.
func (a JevRelevance) AssessRelevance(ctx context.Context, requirement string, source applicationpacks.Source, excerpt string) (applicationpacks.Relevance, error) {
	if a.Evaluator == nil {
		return applicationpacks.Relevance{}, ErrUnavailable
	}
	return applicationpacks.AssessRelevance(ctx, a.Evaluator, requirement, source, excerpt)
}
