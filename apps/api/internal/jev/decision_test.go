package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func decisionFixture() DecisionInput {
	return DecisionInput{
		Kind: DecisionNextOutcome, CampaignIntent: "Find substantive platform work within this commissioned search.", MaxReportedTokens: 1000,
		Capabilities:       []DecisionCapability{{ID: "fetch", Description: "Fetch a supported public employer careers source."}, {ID: "inspect", Description: "Inspect a saved candidate within the round."}},
		Sources:            []DecisionSource{{ID: "source-1", SourceRevision: "rev-2", SourceKind: "employer_page", URL: "https://example.test/jobs", ObservedAt: "2026-09-23", Excerpt: "Current public role list."}},
		PreviousOutcomes:   []PreviousDecisionOutcome{{ID: "previous-1", Description: "One earlier source had no matching roles."}},
		RemainingAllowance: []DecisionAllowance{{Operation: "fetch", Remaining: 2}, {Operation: "inspection", Remaining: 1}},
		Candidates:         []DecisionCandidate{{ID: "candidate-a", Description: "Inspect the current platform opening.", Scope: "One saved opening, no outreach", CapabilityID: "inspect", SourceIDs: []string{"source-1"}}, {ID: "candidate-b", Description: "Check another supported employer careers source.", Scope: "One source request", CapabilityID: "fetch"}},
	}
}

func TestSelectDecisionUsesSuppliedContextAndRetainsOutput(t *testing.T) {
	input := decisionFixture()
	fake := screenFake(func(request Request) (Result, error) {
		if len(request.Questions) != 1 {
			t.Fatal("expected one independent Choice question")
		}
		question := request.Questions[decisionQuestionID].(ChoiceQuestion)
		if len(question.Criteria) != 3 || question.Criteria["candidate-b"] == "" || question.Criteria[decisionUnresolved] == "" {
			t.Fatalf("candidate set lost: %#v", question.Criteria)
		}
		state := request.State.(DecisionInput)
		if state.CampaignIntent != input.CampaignIntent || state.Capabilities[0].ID != "fetch" || state.Sources[0].SourceRevision != "rev-2" || state.PreviousOutcomes[0].ID != "previous-1" || state.RemainingAllowance[0].Remaining != 2 || state.Candidates[0].SourceIDs[0] != "source-1" {
			t.Fatalf("context lost: %#v", state)
		}
		out := screenResult(request, map[string]string{decisionQuestionID: "candidate-b"})
		out.RawResponse = json.RawMessage(`{"provider":"exact validated response bytes"}`)
		return out, nil
	})
	got, err := SelectDecision(context.Background(), fake, input)
	if err != nil || got.Disposition != DecisionSelected || got.SelectedID != "candidate-b" || got.InputSHA256 == "" || got.ProviderResult.Usage != (Usage{5, 2}) || got.ProviderResult.Answers[decisionQuestionID].Choice.Probabilities["candidate-b"] != 1 || len(got.RequestSnapshot) == 0 || string(got.ProviderResult.RawResponse) != `{"provider":"exact validated response bytes"}` {
		t.Fatalf("selection output lost: %#v %v", got, err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(got.RequestSnapshot, &snapshot); err != nil || len(snapshot["questions"]) == 0 || len(snapshot["state"]) == 0 {
		t.Fatalf("request snapshot malformed: %s %v", got.RequestSnapshot, err)
	}
	preflight, err := DecisionLogicalRequest(input)
	if err != nil || !reflect.DeepEqual(preflight, []byte(got.RequestSnapshot)) {
		t.Fatalf("decision preflight differs from dispatched logical request: %v", err)
	}
	input.Candidates[0], input.Candidates[1] = input.Candidates[1], input.Candidates[0]
	input.Capabilities[0], input.Capabilities[1] = input.Capabilities[1], input.Capabilities[0]
	reordered, err := SelectDecision(context.Background(), fake, input)
	if err != nil || reordered.InputSHA256 != got.InputSHA256 || !reflect.DeepEqual(reordered.RequestSnapshot, got.RequestSnapshot) {
		t.Fatalf("input order changed canonical decision: %#v %v", reordered, err)
	}
}

func TestSelectDecisionNoCandidatesAndExplicitUncertainty(t *testing.T) {
	input := decisionFixture()
	input.Candidates = nil
	input.Capabilities = nil
	input.RemainingAllowance = nil
	got, err := SelectDecision(context.Background(), nil, input)
	if err != nil || got.Disposition != DecisionNoCandidates || got.SelectedID != "" || len(got.RequestSnapshot) != 0 || got.InputSHA256 == "" {
		t.Fatalf("empty candidate set misrepresented: %#v %v", got, err)
	}
	input = decisionFixture()
	input.Candidates = input.Candidates[:1]
	input.Kind = DecisionNextOutcome
	fake := screenFake(func(request Request) (Result, error) {
		if !strings.Contains(request.Questions[decisionQuestionID].(ChoiceQuestion).Instructions, "next outcome") || len(request.Questions[decisionQuestionID].(ChoiceQuestion).Criteria) != 2 {
			t.Fatal("next outcome must use only supplied option and uncertainty")
		}
		return screenResult(request, map[string]string{decisionQuestionID: decisionUnresolved}), nil
	})
	got, err = SelectDecision(context.Background(), fake, input)
	if err != nil || got.Disposition != DecisionUnresolved || got.SelectedID != "" || got.ProviderResult.Answers[decisionQuestionID].Choice.Choice != decisionUnresolved {
		t.Fatalf("uncertainty lost: %#v %v", got, err)
	}
}

func TestSelectDecisionCandidateBounds(t *testing.T) {
	input := decisionFixture()
	for len(input.Candidates) <= 16 {
		input.Candidates = append(input.Candidates, DecisionCandidate{ID: fmt.Sprintf("candidate-%02d", len(input.Candidates)), Description: "One supported research candidate.", Scope: "One source request", CapabilityID: "fetch"})
	}
	_, err := SelectDecision(context.Background(), screenFake(func(Request) (Result, error) { t.Fatal("provider called beyond candidate bound"); return Result{}, nil }), input)
	if !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("oversized candidate list accepted: %v", err)
	}
}

func TestSelectDecisionRejectsOmittedOrUnsupportedCandidates(t *testing.T) {
	input := decisionFixture()
	input.Candidates = input.Candidates[:1]
	for _, selected := range []string{"candidate-b", "invented"} {
		t.Run(selected, func(t *testing.T) {
			fake := screenFake(func(request Request) (Result, error) {
				return screenResult(request, map[string]string{decisionQuestionID: selected}), nil
			})
			_, err := SelectDecision(context.Background(), fake, input)
			if !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
				t.Fatalf("omitted/unsupported candidate accepted: %v", err)
			}
		})
	}
	input = decisionFixture()
	input.Candidates[0].CapabilityID = "not_available"
	_, err := SelectDecision(context.Background(), screenFake(func(Request) (Result, error) {
		t.Fatal("provider called with unavailable capability")
		return Result{}, nil
	}), input)
	if !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("unsupported capability accepted: %v", err)
	}
}

func TestSelectDecisionRejectsMalformedOutputAndOverBudget(t *testing.T) {
	input := decisionFixture()
	for _, bad := range []string{"wrong_answer", "invalid_probability", "nonmaximum", "negative_usage", "over_budget"} {
		t.Run(bad, func(t *testing.T) {
			fake := screenFake(func(request Request) (Result, error) {
				out := screenResult(request, map[string]string{decisionQuestionID: "candidate-a"})
				switch bad {
				case "wrong_answer":
					out.Answers = map[string]Answer{"other": out.Answers[decisionQuestionID]}
				case "invalid_probability":
					out.Answers[decisionQuestionID].Choice.Probabilities["candidate-a"] = .2
				case "nonmaximum":
					out.Answers[decisionQuestionID].Choice.Probabilities["candidate-a"] = 0
					out.Answers[decisionQuestionID].Choice.Probabilities["candidate-b"] = 1
				case "negative_usage":
					out.Usage.InputTokens = -1
				case "over_budget":
					out.Usage.InputTokens = 1000
				}
				return out, nil
			})
			_, err := SelectDecision(context.Background(), fake, input)
			want := ErrInvalidResponse
			if bad == "over_budget" {
				want = ErrBudgetExceeded
			}
			if !errors.Is(err, &Error{Kind: want}) {
				t.Fatalf("got %v, want %s", err, want)
			}
		})
	}
}
