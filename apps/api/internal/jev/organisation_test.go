package jev

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type organisationEvaluator func(context.Context, Request) (Result, error)

func (f organisationEvaluator) Evaluate(ctx context.Context, request Request) (Result, error) {
	return f(ctx, request)
}

func organisationFixture() OrganisationInput {
	return OrganisationInput{
		CategorySetVersion: 7,
		Categories: []OrganisationCategory{
			{ID: "research", Description: "Research openings involving primary investigation."},
			{ID: "operations", Description: "Operations openings involving running services."},
		},
		Facts: []OrganisationFact{
			{ID: "fact-b", SourceID: "source-42", SourceRevision: "rev-3", SourceKind: "vacancy_page", Excerpt: "Interview users and analyse the findings."},
			{ID: "fact-a", SourceID: "source-42", SourceRevision: "rev-3", SourceKind: "vacancy_page", Excerpt: "Senior user researcher."},
		},
	}
}

func organisationAnswer(choice string, probabilities map[string]float64) Result {
	return Result{
		RequestedModel: "jev-1.13.0", ReturnedModel: "jev-1.13.0",
		Usage: Usage{InputTokens: 64, OutputTokens: 8},
		Answers: map[string]Answer{organisationQuestionID: {
			Type: "choice", Choice: &ChoiceAnswer{Choice: choice, Probabilities: probabilities, Confidence: 0.8},
		}},
	}
}

func TestOrganiseUsesCallerCategoriesAndSourcedFacts(t *testing.T) {
	input := organisationFixture()
	categoryDescriptions := map[string]string{}
	for _, category := range input.Categories {
		categoryDescriptions[category.ID] = category.Description
	}
	evaluate := organisationEvaluator(func(_ context.Context, request Request) (Result, error) {
		if len(request.Questions) != 1 {
			t.Fatalf("questions: %#v", request.Questions)
		}
		question, ok := request.Questions[organisationQuestionID].(ChoiceQuestion)
		if !ok || len(question.Criteria) != 3 || question.Criteria["research"] != categoryDescriptions["research"] ||
			question.Criteria["operations"] != categoryDescriptions["operations"] || question.Criteria[organisationUncertainOption] == "" {
			t.Fatalf("unexpected caller-derived choice question: %#v", question)
		}
		state, ok := request.State.(map[string]any)
		if !ok || state["category_set_version"] != input.CategorySetVersion {
			t.Fatalf("unexpected state: %#v", request.State)
		}
		facts, ok := state["source_facts"].([]OrganisationFact)
		if !ok || len(facts) != 2 || facts[0].ID != "fact-a" || facts[1].ID != "fact-b" {
			t.Fatalf("facts not preserved and canonicalised: %#v", state["source_facts"])
		}
		return organisationAnswer("research", map[string]float64{"research": 0.8, "operations": 0.15, organisationUncertainOption: 0.05}), nil
	})
	result, err := Organise(context.Background(), evaluate, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != OrganisationCategorySelected || result.CategoryID != "research" ||
		result.CategorySetVersion != 7 || result.InputSHA256 == "" || result.ReturnedModel != "jev-1.13.0" ||
		result.Usage.InputTokens != 64 || result.Confidence != 0.8 || len(result.SourceRefs) != 2 ||
		result.SourceRefs[0].FactID != "fact-a" {
		t.Fatalf("unexpected result: %#v", result)
	}
	input.Categories[0], input.Categories[1] = input.Categories[1], input.Categories[0]
	input.Facts[0], input.Facts[1] = input.Facts[1], input.Facts[0]
	reordered, err := Organise(context.Background(), evaluate, input)
	if err != nil || reordered.InputSHA256 != result.InputSHA256 || !reflect.DeepEqual(reordered.SourceRefs, result.SourceRefs) {
		t.Fatalf("reordering changed canonical snapshot: %#v, %v", reordered, err)
	}
	input.Facts[0].SourceRevision = "rev-4"
	changed, err := Organise(context.Background(), evaluate, input)
	if err != nil || changed.InputSHA256 == result.InputSHA256 {
		t.Fatalf("revision change did not change digest: %#v, %v", changed, err)
	}
}

func TestOrganiseCanBeExplicitlyUncertain(t *testing.T) {
	evaluate := organisationEvaluator(func(context.Context, Request) (Result, error) {
		return organisationAnswer(organisationUncertainOption, map[string]float64{"research": 0.2, "operations": 0.1, organisationUncertainOption: 0.7}), nil
	})
	result, err := Organise(context.Background(), evaluate, organisationFixture())
	if err != nil || result.Disposition != OrganisationUncertain || result.CategoryID != "" || result.InputSHA256 == "" {
		t.Fatalf("uncertainty lost: %#v, %v", result, err)
	}
}

func TestOrganiseRejectsInvalidInputBeforeProviderCall(t *testing.T) {
	base := organisationFixture()
	tests := map[string]func(*OrganisationInput){
		"no version":         func(in *OrganisationInput) { in.CategorySetVersion = 0 },
		"no categories":      func(in *OrganisationInput) { in.Categories = nil },
		"duplicate category": func(in *OrganisationInput) { in.Categories[1].ID = in.Categories[0].ID },
		"reserved category":  func(in *OrganisationInput) { in.Categories[0].ID = organisationUncertainOption },
		"no facts":           func(in *OrganisationInput) { in.Facts = nil },
		"duplicate fact":     func(in *OrganisationInput) { in.Facts[1].ID = in.Facts[0].ID },
		"missing revision":   func(in *OrganisationInput) { in.Facts[0].SourceRevision = "" },
		"oversized excerpt":  func(in *OrganisationInput) { in.Facts[0].Excerpt = strings.Repeat("x", 2001) },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			input := OrganisationInput{CategorySetVersion: base.CategorySetVersion,
				Categories: append([]OrganisationCategory(nil), base.Categories...), Facts: append([]OrganisationFact(nil), base.Facts...)}
			change(&input)
			_, err := Organise(context.Background(), organisationEvaluator(func(context.Context, Request) (Result, error) {
				t.Fatal("provider called with invalid input")
				return Result{}, nil
			}), input)
			if !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
				t.Fatalf("got %v, want invalid request", err)
			}
		})
	}
}

func TestOrganiseRejectsMalformedEvaluatorResult(t *testing.T) {
	valid := map[string]float64{"research": 0.8, "operations": 0.15, organisationUncertainOption: 0.05}
	tests := map[string]func(*Result){
		"unknown answer": func(r *Result) { r.Answers = map[string]Answer{"elsewhere": r.Answers[organisationQuestionID]} },
		"unknown category": func(r *Result) {
			r.Answers[organisationQuestionID] = Answer{Type: "choice", Choice: &ChoiceAnswer{Choice: "other", Probabilities: valid, Confidence: 0.8}}
		},
		"chosen below maximum": func(r *Result) {
			r.Answers[organisationQuestionID] = Answer{Type: "choice", Choice: &ChoiceAnswer{Choice: "operations", Probabilities: valid, Confidence: 0.8}}
		},
		"missing probability": func(r *Result) {
			r.Answers[organisationQuestionID] = Answer{Type: "choice", Choice: &ChoiceAnswer{Choice: "research", Probabilities: map[string]float64{"research": 0.8, "operations": 0.2}, Confidence: 0.8}}
		},
		"negative usage": func(r *Result) { r.Usage.InputTokens = -1 },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			r := organisationAnswer("research", valid)
			change(&r)
			_, err := Organise(context.Background(), organisationEvaluator(func(context.Context, Request) (Result, error) { return r, nil }), organisationFixture())
			if !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
				t.Fatalf("got %v, want invalid response", err)
			}
		})
	}
}
