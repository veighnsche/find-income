// Package jev provides a bounded, server-side System One HTTP adapter.
// It does not decide whether an opportunity is qualified or persist judgments.
package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Question is implemented only by the two System One primitives used here.
type Question interface {
	isQuestion()
}

type ChoiceQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

func (ChoiceQuestion) isQuestion() {}

func Choice(instructions string, criteria map[string]string) ChoiceQuestion {
	return ChoiceQuestion{Type: "choice", Instructions: instructions, Criteria: criteria}
}

type ScoreQuestion struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}

func (ScoreQuestion) isQuestion() {}

func Score(instructions string, criteria []string) ScoreQuestion {
	return ScoreQuestion{Type: "score", Instructions: instructions, Criteria: criteria}
}

// Request.State must be a JSON string, object, or array containing text data.
// The caller builds and snapshots the relevant source spans before submission.
type Request struct {
	State     any
	Questions map[string]Question
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type ChoiceAnswer struct {
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type ScoreAnswer struct {
	Score         float64            `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type Answer struct {
	Type   string
	Choice *ChoiceAnswer
	Score  *ScoreAnswer
}

type Result struct {
	RequestedModel string
	ReturnedModel  string
	Answers        map[string]Answer
	Usage          Usage
	RawResponse    json.RawMessage // Exact validated provider body; persist privately.
}

type ErrorKind string

const (
	ErrDisabled        ErrorKind = "disabled"
	ErrMissingKey      ErrorKind = "missing_key"
	ErrInvalidConfig   ErrorKind = "invalid_config"
	ErrInvalidRequest  ErrorKind = "invalid_request"
	ErrRequestTooLarge ErrorKind = "request_too_large"
	ErrBudgetExceeded  ErrorKind = "budget_exceeded"
	ErrUnauthorized    ErrorKind = "unauthorized"
	ErrRateLimited     ErrorKind = "rate_limited"
	ErrUnavailable     ErrorKind = "unavailable"
	ErrProviderRequest ErrorKind = "provider_request"
	ErrTimeout         ErrorKind = "timeout"
	ErrCanceled        ErrorKind = "canceled"
	ErrTransport       ErrorKind = "transport"
	ErrInvalidResponse ErrorKind = "invalid_response"
)

// Error deliberately omits request/response bodies, headers, URLs, and keys.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Attempts   int
	// RetryAfter is a safe suggested wait from the provider header or local
	// backoff for retryable statuses. It is not a promise of an automatic retry.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("jev %s (HTTP %d, attempts %d)", e.Kind, e.StatusCode, e.Attempts)
	}
	return fmt.Sprintf("jev %s (attempts %d)", e.Kind, e.Attempts)
}

func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && e.Kind == t.Kind
}

func validateRequest(r Request) error {
	if len(r.Questions) == 0 {
		return &Error{Kind: ErrInvalidRequest}
	}
	for id, question := range r.Questions {
		if strings.TrimSpace(id) == "" || question == nil {
			return &Error{Kind: ErrInvalidRequest}
		}
		switch q := question.(type) {
		case ChoiceQuestion:
			if q.Type != "choice" || strings.TrimSpace(q.Instructions) == "" || len(q.Criteria) < 2 || len(q.Criteria) > 255 {
				return &Error{Kind: ErrInvalidRequest}
			}
			for option, description := range q.Criteria {
				if strings.TrimSpace(option) == "" || strings.TrimSpace(description) == "" {
					return &Error{Kind: ErrInvalidRequest}
				}
			}
		case ScoreQuestion:
			if q.Type != "score" || strings.TrimSpace(q.Instructions) == "" || len(q.Criteria) < 2 || len(q.Criteria) > 10 {
				return &Error{Kind: ErrInvalidRequest}
			}
			for _, level := range q.Criteria {
				if strings.TrimSpace(level) == "" {
					return &Error{Kind: ErrInvalidRequest}
				}
			}
		default:
			return &Error{Kind: ErrInvalidRequest}
		}
	}
	state, err := json.Marshal(r.State)
	if err != nil || len(state) == 0 || string(state) == "null" || (state[0] != '"' && state[0] != '{' && state[0] != '[') {
		return &Error{Kind: ErrInvalidRequest}
	}
	return nil
}

func validProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validDistribution(probabilities map[string]float64, expected map[string]struct{}) bool {
	if len(probabilities) != len(expected) {
		return false
	}
	sum := 0.0
	for key, probability := range probabilities {
		if _, ok := expected[key]; !ok || !validProbability(probability) {
			return false
		}
		sum += probability
	}
	return math.Abs(sum-1) <= 0.01
}
