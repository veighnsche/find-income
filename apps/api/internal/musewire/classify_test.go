package musewire

import (
	"context"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type countClassifier struct {
	calls int
}

func (c *countClassifier) ClassifyVacancy(context.Context, VacancyClassification) (store.Finding, error) {
	c.calls++
	return store.Finding{}, nil
}

func TestBoundClassifierCapsJudgments(t *testing.T) {
	inner := &countClassifier{}
	bound := &BoundClassifier{Inner: inner, Max: 2}
	in := VacancyClassification{Vacancy: musecode.PublicVacancy{VacancyRef: "vac-1"}}
	ctx := context.Background()
	if _, err := bound.ClassifyVacancy(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := bound.ClassifyVacancy(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := bound.ClassifyVacancy(ctx, in); err == nil || !strings.Contains(err.Error(), "bound of 2 reached") {
		t.Fatalf("third judgment err = %v, want bound failure", err)
	}
	if inner.calls != 2 || bound.Used() != 2 {
		t.Fatalf("inner calls = %d used = %d, want 2 and 2", inner.calls, bound.Used())
	}
	if _, err := (&BoundClassifier{Max: 1}).ClassifyVacancy(ctx, in); err == nil {
		t.Error("nil inner classifier admitted")
	}
}
