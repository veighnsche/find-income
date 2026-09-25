package materialprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

type stubEvaluator struct {
	result jev.Result
	err    error
	calls  int
}

func (s *stubEvaluator) Evaluate(_ context.Context, _ jev.Request) (jev.Result, error) {
	s.calls++
	return s.result, s.err
}

func approvedSource() applicationpacks.Source {
	body := "Built Go services for Harbour Systems."
	sum := sha256.Sum256([]byte(body))
	return applicationpacks.Source{ID: "cv-vince-liem.md", Name: "cv-vince-liem.md",
		SHA256: hex.EncodeToString(sum[:]), Approved: true, Body: body}
}

func TestJevRelevancePassThrough(t *testing.T) {
	stub := &stubEvaluator{result: jev.Result{ReturnedModel: "jev-test",
		Answers: map[string]jev.Answer{"relevance": {Type: "choice",
			Choice: &jev.ChoiceAnswer{Choice: "relevant", Confidence: 0.9}}}}}
	got, err := JevRelevance{Evaluator: stub}.AssessRelevance(context.Background(),
		"Go services", approvedSource(), "Built Go services for Harbour Systems.")
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != "relevant" || got.Confidence != 0.9 || got.Model != "jev-test" {
		t.Fatalf("unexpected relevance: %+v", got)
	}
	if stub.calls != 1 {
		t.Fatalf("evaluator calls: got %d, want 1", stub.calls)
	}
}

func TestJevRelevanceNilEvaluatorUnavailable(t *testing.T) {
	_, err := JevRelevance{}.AssessRelevance(context.Background(),
		"Go services", approvedSource(), "Built Go services for Harbour Systems.")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
}
