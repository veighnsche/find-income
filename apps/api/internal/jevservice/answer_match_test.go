package jevservice

import (
	"context"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// In-process fake Jev client: no HTTP, no network, no credentials and no live
// model. It answers each judged Choice question from a preset choice map and
// defaults every unmapped question to the reserved none_fits abstention.
type fakeAnswerClient struct {
	calls   int
	choices map[string]string // match_<n> key -> chosen answer id
}

func (f *fakeAnswerClient) RequestedModel() string { return "jev-1.13.0" }

func (f *fakeAnswerClient) EncodedRequest(jev.Request) ([]byte, error) {
	return []byte(`{"model":"jev-1.13.0"}`), nil
}

func (f *fakeAnswerClient) EvaluateOnceCaptured(_ context.Context, req jev.Request) (jev.Result, jev.CapturedExchange, error) {
	f.calls++
	body := []byte(`{"model":"jev-1.13.0"}`)
	answers := make(map[string]jev.Answer, len(req.Questions))
	for key, question := range req.Questions {
		choice := f.choices[key]
		if choice == "" {
			choice = jev.AnswerMatchNoneFits
		}
		options := question.(jev.ChoiceQuestion).Criteria
		probabilities := make(map[string]float64, len(options))
		for option := range options {
			probabilities[option] = 0
		}
		probabilities[choice] = 1
		answers[key] = jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{
			Choice: choice, Probabilities: probabilities, Confidence: 0.8}}
	}
	return jev.Result{RequestedModel: "jev-1.13.0", ReturnedModel: "jev-1.13.0",
			Answers: answers, Usage: jev.Usage{InputTokens: 5, OutputTokens: 1}},
		jev.CapturedExchange{RequestBytes: []byte(`{"model":"jev-1.13.0"}`),
			ResponseBytes: body, HTTPStatus: 200, ReturnedModel: "jev-1.13.0"}, nil
}

func answerMatchCandidate() jev.AnswerMatchCandidate {
	return jev.AnswerMatchCandidate{AnswerID: "ans-1", AnswerVersion: 3,
		TextSHA256: strings.Repeat("a", 64), ScopeTags: []string{"go"},
		Excerpt: "Built a Go service for account administration."}
}

func answerMatchQuestion(id string, candidates []jev.AnswerMatchCandidate) jev.AnswerMatchQuestion {
	return jev.AnswerMatchQuestion{QuestionID: id, Text: "Describe Go experience.",
		Required: "required", Kind: "free_text",
		TextSHA256: strings.Repeat("c", 64), Candidates: candidates}
}

func answerMatchInput(questions []jev.AnswerMatchQuestion) jev.AnswerMatchInput {
	return jev.AnswerMatchInput{CheckID: "check-1",
		QuestionSetSHA256: strings.Repeat("d", 64), AnswerCatalogDigest: strings.Repeat("e", 64),
		MaxReportedTokens: 100, Questions: questions}
}

// One batch costs exactly one charged Jev call: a pinned approved answer
// survives with its id/version, and an unfitting question resolves to the
// reserved none_fits choice with empty answer fields.
func TestRunAnswerMatchNoneFitsSingleCharge(t *testing.T) {
	s, binding := serviceRound(t, 2)
	defer s.Close()
	client := &fakeAnswerClient{choices: map[string]string{"match_0": "ans-1"}}
	result, err := (Service{Store: s, Client: client}).RunAnswerMatch(context.Background(), binding,
		answerMatchInput([]jev.AnswerMatchQuestion{
			answerMatchQuestion("q-match", []jev.AnswerMatchCandidate{answerMatchCandidate()}),
			answerMatchQuestion("q-nomatch", []jev.AnswerMatchCandidate{answerMatchCandidate()}),
		}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selections) != 2 || len(result.RequestSnapshot) == 0 {
		t.Fatalf("batch must carry two judged selections and a request snapshot: %+v", result)
	}
	matched := result.Selections[0]
	if matched.QuestionID != "q-match" || matched.AnswerID != "ans-1" || matched.AnswerVersion != 3 ||
		matched.AnswerTextSHA256 != strings.Repeat("a", 64) || matched.NoneFits || matched.Deterministic ||
		matched.CandidateSetHash == "" {
		t.Fatalf("matched selection lost its pinned answer: %+v", matched)
	}
	none := result.Selections[1]
	if none.QuestionID != "q-nomatch" || !none.NoneFits || none.Deterministic ||
		none.AnswerID != "" || none.AnswerVersion != 0 || none.AnswerTextSHA256 != "" {
		t.Fatalf("unfitting question must resolve to judged none_fits: %+v", none)
	}
	if client.calls != 1 {
		t.Fatalf("one batch allows exactly one provider call, got %d", client.calls)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 1 || attempts[0].Purpose != jev.AnswerMatchPurpose ||
		attempts[0].Status != "succeeded" {
		t.Fatalf("answer_match evidence: %+v %v", attempts, err)
	}
	round, err := s.Round(context.Background(), binding.RoundID)
	if err != nil || round.Used.Requests != 1 {
		t.Fatalf("round charge: %+v %v", round, err)
	}
}

// Questions with no offered candidates resolve deterministically without
// touching the provider: zero calls, zero attempts, zero charges.
func TestRunAnswerMatchDeterministicNeedsNoProviderCall(t *testing.T) {
	s, binding := serviceRound(t, 1)
	defer s.Close()
	client := &fakeAnswerClient{}
	result, err := (Service{Store: s, Client: client}).RunAnswerMatch(context.Background(), binding,
		answerMatchInput([]jev.AnswerMatchQuestion{answerMatchQuestion("q-lonely", nil)}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selections) != 1 || len(result.RequestSnapshot) != 0 {
		t.Fatalf("deterministic batch must carry one selection and no snapshot: %+v", result)
	}
	selection := result.Selections[0]
	if !selection.NoneFits || !selection.Deterministic || selection.AnswerID != "" {
		t.Fatalf("unoffered question must resolve to deterministic none_fits: %+v", selection)
	}
	if client.calls != 0 {
		t.Fatalf("deterministic batch called the provider %d times", client.calls)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("deterministic batch must leave no attempt: %+v %v", attempts, err)
	}
	round, err := s.Round(context.Background(), binding.RoundID)
	if err != nil || round.Used.Requests != 0 {
		t.Fatalf("deterministic batch must leave no charge: %+v %v", round, err)
	}
}
