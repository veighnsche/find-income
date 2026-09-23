package jev

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These are exact saved HTTP 200 request/response bodies from two measured
// System One calls. This test makes no network request and says nothing about
// the quality of either returned __unresolved__ judgment.
func TestRecordedDiscoveryDecisionsFitFiniteReportedBudget(t *testing.T) {
	for _, sample := range []struct {
		name  string
		usage Usage
	}{
		{"next-outcome", Usage{InputTokens: 2819, OutputTokens: 142}},
		{"neutral-lead", Usage{InputTokens: 1585, OutputTokens: 99}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			requestBytes, err := os.ReadFile(filepath.Join("testdata", "i10", sample.name+"-request.json"))
			if err != nil {
				t.Fatal(err)
			}
			responseBytes, err := os.ReadFile(filepath.Join("testdata", "i10", sample.name+"-response.json"))
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				State DecisionInput `json:"state"`
				Model string        `json:"model"`
			}
			var response struct {
				Model   string `json:"model"`
				Answers map[string]struct {
					Type          string             `json:"type"`
					Choice        string             `json:"choice"`
					Probabilities map[string]float64 `json:"probabilities"`
					Confidence    float64            `json:"confidence"`
				} `json:"answers"`
				Usage Usage `json:"usage"`
			}
			if json.Unmarshal(requestBytes, &request) != nil || json.Unmarshal(responseBytes, &response) != nil || response.Usage != sample.usage || request.Model != response.Model {
				t.Fatalf("saved exchange malformed: %s", sample.name)
			}
			answer := response.Answers[decisionQuestionID]
			if answer.Choice != decisionUnresolved {
				t.Fatalf("saved choice changed: %s", answer.Choice)
			}
			evaluator := screenFake(func(Request) (Result, error) {
				return Result{RequestedModel: request.Model, ReturnedModel: response.Model, Usage: response.Usage,
					Answers:     map[string]Answer{decisionQuestionID: {Type: answer.Type, Choice: &ChoiceAnswer{Choice: answer.Choice, Probabilities: answer.Probabilities, Confidence: answer.Confidence}}},
					RawResponse: responseBytes}, nil
			})
			request.State.MaxReportedTokens = 1200
			if _, err := SelectDecision(context.Background(), evaluator, request.State); !errors.Is(err, &Error{Kind: ErrBudgetExceeded}) {
				t.Fatalf("old cap did not reject saved usage %v: %v", response.Usage, err)
			}
			request.State.MaxReportedTokens = 10000
			logical, err := DecisionLogicalRequest(request.State)
			if err != nil || len(logical) > 24<<10 {
				t.Fatalf("bounded logical context: %d bytes %v", len(logical), err)
			}
			selected, err := SelectDecision(context.Background(), evaluator, request.State)
			if err != nil || selected.Disposition != DecisionUnresolved || selected.ProviderResult.Usage != sample.usage {
				t.Fatalf("measured usage not accepted as unresolved: %+v %v", selected, err)
			}
		})
	}
}
