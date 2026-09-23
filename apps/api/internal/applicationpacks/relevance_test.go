package applicationpacks

import (
	"context"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

type relevanceEvaluator func(context.Context, jev.Request) (jev.Result, error)

func (f relevanceEvaluator) Evaluate(ctx context.Context, request jev.Request) (jev.Result, error) {
	return f(ctx, request)
}

func TestRelevanceReceivesOnlySuppliedEvidence(t *testing.T) {
	source := fixture(t).Sources[2]
	excerpt := "The project adds a Go environment/access service, OAuth and SQLite-backed state."
	evaluator := relevanceEvaluator(func(_ context.Context, request jev.Request) (jev.Result, error) {
		state := request.State.(struct {
			Requirement string `json:"requirement"`
			SourceID    string `json:"sourceId"`
			SourceSHA   string `json:"sourceSha256"`
			Excerpt     string `json:"excerpt"`
		})
		if state.Excerpt != excerpt || state.SourceSHA != source.SHA256 || len(request.Questions) != 1 {
			t.Fatal("unbounded or wrong Jev context")
		}
		return jev.Result{ReturnedModel: "fixture-jev", Answers: map[string]jev.Answer{"relevance": {Choice: &jev.ChoiceAnswer{Choice: "relevant", Confidence: 0.8}}}}, nil
	})
	got, err := AssessRelevance(context.Background(), evaluator, "Go service implementation", source, excerpt)
	if err != nil || got.Scope != "relevant" || got.SourceID != source.ID || len(got.InputSHA256) != 64 {
		t.Fatalf("relevance: %+v %v", got, err)
	}
}
