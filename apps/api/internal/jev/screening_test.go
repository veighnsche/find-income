package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

type screenFake func(Request) (Result, error)

func (f screenFake) Evaluate(_ context.Context, r Request) (Result, error) { return f(r) }
func screenInput() ScreeningInput {
	return ScreeningInput{PreferenceVersion: 2, MaxTotalTokens: 1000, Criteria: []ScreeningCriterion{{ID: "avoid", Label: "On call", Description: "On-call as a required duty.", Kind: "responsibility", Mode: "avoid"}, {ID: "want", Label: "Research", Description: "Primary responsibility for user research.", Kind: "role", Mode: "require"}}, Spans: []ScreeningSpan{{ID: "b", SourceID: "src", SourceRevision: "rev-1", SourceKind: "vacancy", Excerpt: "Research interviews are the primary work."}, {ID: "a", SourceID: "src", SourceRevision: "rev-1", SourceKind: "vacancy", Excerpt: "Our team also has on-call engineers."}}}
}
func screenResult(r Request, choices map[string]string) Result {
	a := map[string]Answer{}
	for id, q := range r.Questions {
		options := q.(ChoiceQuestion).Criteria
		p := map[string]float64{}
		for k := range options {
			p[k] = 0
		}
		p[choices[id]] = 1
		a[id] = Answer{Type: "choice", Choice: &ChoiceAnswer{Choice: choices[id], Probabilities: p, Confidence: .8}}
	}
	return Result{RequestedModel: "jev-1.13.0", ReturnedModel: "jev-1.13.0", Usage: Usage{5, 2}, Answers: a}
}
func TestScreenResponsibilitiesGenericScopesAndSupport(t *testing.T) {
	in := screenInput()
	calls := 0
	fake := screenFake(func(r Request) (Result, error) {
		calls++
		if calls == 1 {
			if len(r.Questions) != 2 || r.Questions["scope_0"].(ChoiceQuestion).Criteria[ScopeNoRelevantEvidence] == "" {
				t.Fatal("scope options missing")
			}
			return screenResult(r, map[string]string{"scope_0": ScopeMentionOnly, "scope_1": ScopePresent}), nil
		}
		if len(r.Questions["support_0"].(ChoiceQuestion).Criteria) != 3 || len(r.Questions["support_1"].(ChoiceQuestion).Criteria) != 3 {
			t.Fatal("ordinary support choices should be one per span plus none")
		}
		return screenResult(r, map[string]string{"support_0": "span_0", "support_1": "span_1"}), nil
	})
	got, err := ScreenResponsibilities(context.Background(), fake, in)
	if err != nil || calls != 2 || got.Usage != (Usage{10, 4}) || got.Observations[0].Scope != ScopeMentionOnly || got.Observations[0].ProposedSupport[0].SpanID != "a" || got.Observations[1].ProposedSupport[0].SpanID != "b" || got.InputSHA256 == "" || got.Observations[0].DefinitionSHA256 == "" || got.Observations[0].SupportState != SupportProposed {
		t.Fatalf("got %#v, %v", got, err)
	}
	in.Spans[0], in.Spans[1] = in.Spans[1], in.Spans[0]
	in.Criteria[0], in.Criteria[1] = in.Criteria[1], in.Criteria[0]
	calls = 0
	again, err := ScreenResponsibilities(context.Background(), fake, in)
	if err != nil || again.InputSHA256 != got.InputSHA256 {
		t.Fatalf("reordering changed digest: %#v %v", again, err)
	}
	in.Criteria[0].Description += " New definition."
	calls = 0
	changed, err := ScreenResponsibilities(context.Background(), fake, in)
	if err != nil || changed.InputSHA256 == got.InputSHA256 {
		t.Fatalf("definition did not change digest: %#v %v", changed, err)
	}
}
func TestScreenResponsibilitiesSilentAvoidanceAndConflict(t *testing.T) {
	in := screenInput()
	in.Criteria = in.Criteria[:1]
	calls := 0
	silent := screenFake(func(r Request) (Result, error) {
		calls++
		return screenResult(r, map[string]string{"scope_0": ScopeNoRelevantEvidence}), nil
	})
	got, err := ScreenResponsibilities(context.Background(), silent, in)
	if err != nil || calls != 1 || got.Observations[0].Scope != ScopeNoRelevantEvidence || len(got.Observations[0].ProposedSupport) != 0 || got.Observations[0].SupportState != SupportNotRequested {
		t.Fatalf("silence mishandled: %#v %v", got, err)
	}
	conflict := screenFake(func(r Request) (Result, error) {
		if _, ok := r.Questions["scope_0"]; ok {
			return screenResult(r, map[string]string{"scope_0": ScopeConflicting}), nil
		}
		if len(r.Questions["support_0"].(ChoiceQuestion).Criteria) != 4 {
			t.Fatal("conflict should allow the two-span pair")
		}
		return screenResult(r, map[string]string{"support_0": "pair_0_1"}), nil
	})
	got, err = ScreenResponsibilities(context.Background(), conflict, in)
	if err != nil || got.Observations[0].Scope != ScopeConflicting || len(got.Observations[0].ProposedSupport) != 2 {
		t.Fatalf("conflict mishandled: %#v %v", got, err)
	}
}

func TestScreenResponsibilitiesPreservesExactWhitespaceAtSpanBoundary(t *testing.T) {
	in := screenInput()
	in.Criteria = in.Criteria[:1]
	in.Spans[0].Excerpt = " Research interviews are the primary work. "
	fake := screenFake(func(request Request) (Result, error) {
		spans := request.State.(map[string]any)["source_spans"].([]ScreeningSpan)
		if spans[1].Excerpt != " Research interviews are the primary work. " {
			t.Fatalf("exact source span changed: %#v", spans)
		}
		return screenResult(request, map[string]string{"scope_0": ScopeNoRelevantEvidence}), nil
	})
	if _, err := ScreenResponsibilities(context.Background(), fake, in); err != nil {
		t.Fatal(err)
	}
}

func TestScreenResponsibilitiesBoundedWindowAndUsageOverflow(t *testing.T) {
	in := screenInput()
	in.Criteria = in.Criteria[:1]
	for i := len(in.Spans); i < 12; i++ {
		in.Spans = append(in.Spans, ScreeningSpan{ID: fmt.Sprintf("span-%02d", i), SourceID: "src", SourceRevision: "rev-1", SourceKind: "vacancy", Excerpt: "Further exact source excerpt."})
	}
	fake := screenFake(func(r Request) (Result, error) {
		if _, ok := r.Questions["scope_0"]; ok {
			return screenResult(r, map[string]string{"scope_0": ScopePresent}), nil
		}
		if count := len(r.Questions["support_0"].(ChoiceQuestion).Criteria); count != 13 {
			t.Fatalf("ordinary twelve-span choice count = %d, want 13", count)
		}
		out := screenResult(r, map[string]string{"support_0": "span_0"})
		out.Usage.InputTokens = math.MaxInt64
		return out, nil
	})
	_, err := ScreenResponsibilities(context.Background(), fake, in)
	if !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("input usage overflow accepted: %v", err)
	}
	outputOverflow := screenFake(func(r Request) (Result, error) {
		if _, ok := r.Questions["scope_0"]; ok {
			return screenResult(r, map[string]string{"scope_0": ScopePresent}), nil
		}
		out := screenResult(r, map[string]string{"support_0": "span_0"})
		out.Usage.OutputTokens = math.MaxInt64
		return out, nil
	})
	_, err = ScreenResponsibilities(context.Background(), outputOverflow, in)
	if !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("output usage overflow accepted: %v", err)
	}
}

func TestScreenResponsibilitiesMissingSupportStaysUnresolved(t *testing.T) {
	in := screenInput()
	in.Criteria = in.Criteria[:1]
	fake := screenFake(func(r Request) (Result, error) {
		if _, ok := r.Questions["scope_0"]; ok {
			return screenResult(r, map[string]string{"scope_0": ScopePresent}), nil
		}
		return screenResult(r, map[string]string{"support_0": "none"}), nil
	})
	got, err := ScreenResponsibilities(context.Background(), fake, in)
	if err != nil || got.Observations[0].Scope != ScopePresent || got.Observations[0].SupportState != SupportMissing || len(got.Observations[0].ProposedSupport) != 0 {
		t.Fatalf("unsupported judgment acquired evidence: %#v %v", got, err)
	}
}

func TestScreenResponsibilitiesProbabilityBoundaryPreservesRawValues(t *testing.T) {
	in := screenInput()
	in.Criteria = in.Criteria[:1]
	for _, tc := range []struct {
		name string
		sum  float64
		ok   bool
	}{
		{"within adapter tolerance", 0.991, true},
		{"consultation 0.99 case", 0.99, false},
		{"outside adapter tolerance", 0.989, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := screenFake(func(r Request) (Result, error) {
				if _, ok := r.Questions["scope_0"]; ok {
					out := screenResult(r, map[string]string{"scope_0": ScopeNoRelevantEvidence})
					out.Answers["scope_0"].Choice.Probabilities[ScopeNoRelevantEvidence] = tc.sum
					return out, nil
				}
				t.Fatal("no support call expected")
				return Result{}, nil
			})
			got, err := ScreenResponsibilities(context.Background(), fake, in)
			if tc.ok {
				if err != nil || got.Observations[0].Probabilities[ScopeNoRelevantEvidence] != tc.sum {
					t.Fatalf("raw distribution changed: %#v %v", got, err)
				}
			} else if !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
				t.Fatalf("invalid raw distribution accepted: %#v %v", got, err)
			}
		})
	}
}

func TestScreenResponsibilitiesReportedTokenBudget(t *testing.T) {
	in := screenInput()
	in.Criteria = in.Criteria[:1]
	in.MaxTotalTokens = 6
	calls := 0
	fake := screenFake(func(r Request) (Result, error) {
		calls++
		return screenResult(r, map[string]string{"scope_0": ScopePresent}), nil
	})
	_, err := ScreenResponsibilities(context.Background(), fake, in)
	if !errors.Is(err, &Error{Kind: ErrBudgetExceeded}) || calls != 1 {
		t.Fatalf("first-stage budget not enforced: %v, calls %d", err, calls)
	}
	in.MaxTotalTokens = 7
	calls = 0
	_, err = ScreenResponsibilities(context.Background(), fake, in)
	if !errors.Is(err, &Error{Kind: ErrBudgetExceeded}) || calls != 1 {
		t.Fatalf("exhausted first-stage budget dispatched support: %v, calls %d", err, calls)
	}
	in.MaxTotalTokens = 10
	calls = 0
	fake = screenFake(func(r Request) (Result, error) {
		calls++
		if calls == 1 {
			return screenResult(r, map[string]string{"scope_0": ScopePresent}), nil
		}
		return screenResult(r, map[string]string{"support_0": "span_0"}), nil
	})
	_, err = ScreenResponsibilities(context.Background(), fake, in)
	if !errors.Is(err, &Error{Kind: ErrBudgetExceeded}) || calls != 2 {
		t.Fatalf("combined-stage budget not enforced: %v, calls %d", err, calls)
	}
}

func TestScreenResponsibilitiesProfileDefinitionsDriveQuestions(t *testing.T) {
	input := screenInput()
	input.Criteria = []ScreeningCriterion{{ID: "different-role", Label: "Service design", Description: "Primary service design work, including journey mapping.", Kind: "role", Mode: "prefer"}}
	input.PreferenceVersion = 7
	called := 0
	fake := screenFake(func(request Request) (Result, error) {
		called++
		encoded, err := json.Marshal(request)
		if err != nil || strings.Contains(string(encoded), `"mode"`) || strings.Contains(string(encoded), `"prefer"`) {
			t.Fatalf("policy mode leaked into factual request: %s, %v", encoded, err)
		}
		if called == 1 {
			state := request.State.(map[string]any)
			criteria := state["criteria"].([]screeningSubject)
			if criteria[0].ID != "different-role" || criteria[0].Description != input.Criteria[0].Description || strings.Contains(request.Questions["scope_0"].(ChoiceQuestion).Instructions, "prefer") {
				t.Fatalf("profile definition lost: %#v", criteria)
			}
			return screenResult(request, map[string]string{"scope_0": ScopeNoRelevantEvidence}), nil
		}
		t.Fatal("silent preferred role should not request support")
		return Result{}, nil
	})
	got, err := ScreenResponsibilities(context.Background(), fake, input)
	if err != nil || called != 1 || got.PreferenceVersion != 7 || got.Observations[0].CriterionID != "different-role" {
		t.Fatalf("different profile not screened: %#v %v", got, err)
	}
	input.Criteria[0].Mode = "require"
	called = 0
	again, err := ScreenResponsibilities(context.Background(), fake, input)
	if err != nil || got.Observations[0].DefinitionSHA256 == again.Observations[0].DefinitionSHA256 || got.InputSHA256 == again.InputSHA256 {
		t.Fatalf("mode edit failed to change stored identity: %#v %v", again, err)
	}
}
func TestScreenResponsibilitiesRejectsBadOutputAndInput(t *testing.T) {
	in := screenInput()
	in.Criteria = in.Criteria[:1]
	for _, bad := range []string{"invented", "missing", "model", "nonmaximum"} {
		t.Run(bad, func(t *testing.T) {
			fake := screenFake(func(r Request) (Result, error) {
				if _, ok := r.Questions["scope_0"]; ok {
					return screenResult(r, map[string]string{"scope_0": ScopePresent}), nil
				}
				out := screenResult(r, map[string]string{"support_0": "span_0"})
				switch bad {
				case "invented":
					out.Answers["support_0"].Choice.Choice = "span_99"
				case "missing":
					delete(out.Answers, "support_0")
				case "model":
					out.ReturnedModel = "different"
				case "nonmaximum":
					out.Answers["support_0"].Choice.Probabilities["span_0"] = 0
					out.Answers["support_0"].Choice.Probabilities["none"] = 1
				}
				return out, nil
			})
			_, err := ScreenResponsibilities(context.Background(), fake, in)
			if !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
				t.Fatalf("got %v", err)
			}
		})
	}
	in.Spans[0].SourceRevision = ""
	_, err := ScreenResponsibilities(context.Background(), screenFake(func(Request) (Result, error) { t.Fatal("called provider"); return Result{}, nil }), in)
	if !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("got %v", err)
	}
}
