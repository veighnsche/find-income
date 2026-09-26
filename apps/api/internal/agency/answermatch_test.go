package agency

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// scriptEvaluator is a deterministic Jev fake: it picks the first
// criterion (sorted, excluding none_fits) whose description contains the
// marker, else none_fits. Every judged question carries none_fits, so the
// fake also proves the abstention is always offered.
type scriptEvaluator struct {
	marker   string
	requests []jev.Request
}

func (s *scriptEvaluator) Evaluate(_ context.Context, request jev.Request) (jev.Result, error) {
	s.requests = append(s.requests, request)
	result := jev.Result{RequestedModel: "fake-jev", ReturnedModel: "fake-jev",
		Answers: map[string]jev.Answer{}, Usage: jev.Usage{InputTokens: 10, OutputTokens: 5}}
	for key, item := range request.Questions {
		criteria, ok := item.(jev.ChoiceQuestion)
		if !ok {
			return jev.Result{}, fmt.Errorf("fake: want choice, got %T", item)
		}
		options := make([]string, 0, len(criteria.Criteria))
		for option := range criteria.Criteria {
			options = append(options, option)
		}
		sort.Strings(options)
		pick := jev.AnswerMatchNoneFits
		for _, option := range options {
			if option != jev.AnswerMatchNoneFits && strings.Contains(criteria.Criteria[option], s.marker) {
				pick = option
				break
			}
		}
		probabilities := map[string]float64{}
		rest := 0.3 / float64(len(options)-1)
		for _, option := range options {
			probabilities[option] = rest
		}
		probabilities[pick] = 0.7
		result.Answers[key] = jev.Answer{Type: "choice",
			Choice: &jev.ChoiceAnswer{Choice: pick, Probabilities: probabilities, Confidence: 0.7}}
	}
	return result, nil
}

type scriptJudge struct {
	evaluator *scriptEvaluator
	keys      []string
	inputs    []jev.AnswerMatchInput
}

func (s *scriptJudge) judge(ctx context.Context, key string, input jev.AnswerMatchInput) (jev.AnswerMatchResult, string, error) {
	s.keys = append(s.keys, key)
	s.inputs = append(s.inputs, input)
	result, err := jev.MatchAnswers(ctx, s.evaluator, input)
	if err != nil {
		return jev.AnswerMatchResult{}, "", err
	}
	return result, "attempt-" + key, nil
}

func testSHA(seed string) string {
	base := seed
	for len(base) < 64 {
		base += seed
	}
	return base[:64]
}

func testLibrary() []ApprovedAnswer {
	return []ApprovedAnswer{
		{ID: "ans-b", Version: 2, TextSHA256: testSHA("b"), Text: "I led a team. MATCHME", ScopeTags: []string{"leadership"}},
		{ID: "ans-a", Version: 1, TextSHA256: testSHA("a"), Text: "I write Go daily.", ScopeTags: []string{"go"}},
		{ID: "ans-c", Version: 1, TextSHA256: testSHA("c"), Text: "I enjoy mentoring.", ContextNote: "mentoring"},
	}
}

func testQuestions() []MatchQuestion {
	return []MatchQuestion{
		{ID: "q-1", Text: "Describe your leadership experience.", Required: "required", TextSHA256: testSHA("q1")},
		{ID: "q-2", Text: "What is your favorite color?", Required: "optional", TextSHA256: testSHA("q2")},
	}
}

func TestMatchQuestionsEmptyCatalogCostsNoJev(t *testing.T) {
	judge := func(context.Context, string, jev.AnswerMatchInput) (jev.AnswerMatchResult, string, error) {
		t.Fatal("judge invoked with an empty catalog")
		return jev.AnswerMatchResult{}, "", nil
	}
	batches, err := MatchQuestions(context.Background(), judge, "check-1",
		testSHA("set"), testSHA("cat"), 1000, testQuestions(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || len(batches[0].Result.Selections) != 2 {
		t.Fatalf("batches=%+v, want one batch with two selections", batches)
	}
	for _, selection := range batches[0].Result.Selections {
		if !selection.NoneFits || !selection.Deterministic {
			t.Fatalf("selection=%+v, want deterministic no-fit", selection)
		}
	}
	if batches[0].AttemptID != "" {
		t.Fatalf("attempt=%q, want empty for deterministic batches", batches[0].AttemptID)
	}
}

func TestMatchQuestionsSingleRound(t *testing.T) {
	evaluator := &scriptEvaluator{marker: "MATCHME"}
	judge := &scriptJudge{evaluator: evaluator}
	batches, err := MatchQuestions(context.Background(), judge.judge, "check-1",
		testSHA("set"), testSHA("cat"), 1000, testQuestions(), BuildCandidates(testLibrary()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches=%d, want 1", len(batches))
	}
	if len(judge.keys) != 1 || judge.keys[0] != "runoff" {
		t.Fatalf("judge keys=%v, want one runoff", judge.keys)
	}
	byQuestion := map[string]jev.AnswerMatchSelection{}
	for _, selection := range batches[0].Result.Selections {
		byQuestion[selection.QuestionID] = selection
	}
	if byQuestion["q-1"].AnswerID != "ans-b" || byQuestion["q-1"].NoneFits {
		t.Fatalf("q-1=%+v, want the marked answer", byQuestion["q-1"])
	}
	// Both questions see the same catalog, so both match the marked
	// answer: the fake judges wording overlap, not the question.
	if byQuestion["q-2"].AnswerID != "ans-b" {
		t.Fatalf("q-2=%+v, want the marked answer", byQuestion["q-2"])
	}
	if batches[0].AttemptID == "" {
		t.Fatal("runoff batch needs its attempt id for persistence")
	}
	// none_fits is offered on every judged question.
	for _, request := range evaluator.requests {
		for key, item := range request.Questions {
			criteria := item.(jev.ChoiceQuestion).Criteria
			if _, ok := criteria[jev.AnswerMatchNoneFits]; !ok {
				t.Fatalf("question %s lacks the no-fit choice", key)
			}
		}
	}
	// Every choice refers to an approved current version or no-fit.
	approved := map[string]bool{"ans-a:1": true, "ans-b:2": true, "ans-c:1": true}
	for _, selection := range batches[0].Result.Selections {
		if selection.NoneFits {
			continue
		}
		key := selection.AnswerID + ":" + fmt.Sprint(selection.AnswerVersion)
		if !approved[key] {
			t.Fatalf("selection=%+v, want an approved current version", selection)
		}
	}
}

func TestMatchQuestionsRecallCoversLargeLibraries(t *testing.T) {
	answers := make([]ApprovedAnswer, 0, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("ans-%d", i)
		answers = append(answers, ApprovedAnswer{ID: id, Version: 1,
			TextSHA256: testSHA(id), Text: "approved text " + id})
	}
	evaluator := &scriptEvaluator{marker: "approved text ans-0"}
	judge := &scriptJudge{evaluator: evaluator}
	questions := []MatchQuestion{{ID: "q-1", Text: "Tell us anything.", Required: "unknown", TextSHA256: testSHA("q1")}}
	batches, err := MatchQuestions(context.Background(), judge.judge, "check-1",
		testSHA("set"), testSHA("cat"), 1000, questions, BuildCandidates(answers), 2)
	if err != nil {
		t.Fatal(err)
	}
	recallInputs := []jev.AnswerMatchInput{}
	for i, key := range judge.keys {
		if strings.HasPrefix(key, "recall/") {
			recallInputs = append(recallInputs, judge.inputs[i])
		}
	}
	if len(recallInputs) != 3 {
		t.Fatalf("recall batches=%d, want 3 for five answers at cap two", len(recallInputs))
	}
	offered := OfferedCandidateIDs(recallInputs)
	if len(offered) != 5 {
		t.Fatalf("offered=%v, want every approved answer to reach Jev", offered)
	}
	if len(batches) != 1 || len(batches[0].Result.Selections) != 1 {
		t.Fatalf("batches=%+v, want one final batch", batches)
	}
	selection := batches[0].Result.Selections[0]
	if selection.AnswerID != "ans-0" || selection.NoneFits {
		t.Fatalf("selection=%+v, want the recall winner after runoff", selection)
	}
}

func TestMatchQuestionsQuestionChunks(t *testing.T) {
	questions := make([]MatchQuestion, 0, 33)
	for i := 0; i < 33; i++ {
		id := fmt.Sprintf("q-%d", i)
		questions = append(questions, MatchQuestion{ID: id, Text: "Question " + id,
			Required: "optional", TextSHA256: testSHA(id)})
	}
	evaluator := &scriptEvaluator{marker: "MATCHME"}
	judge := &scriptJudge{evaluator: evaluator}
	batches, err := MatchQuestions(context.Background(), judge.judge, "check-1",
		testSHA("set"), testSHA("cat"), 100000, questions, BuildCandidates(testLibrary()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches=%d, want 2 for 33 questions", len(batches))
	}
	total := len(batches[0].Result.Selections) + len(batches[1].Result.Selections)
	if total != 33 {
		t.Fatalf("selections=%d, want 33", total)
	}
}

func TestMatchQuestionsOversizedLibraryFailsLoudly(t *testing.T) {
	// cap 1 with an always-picking fake never shrinks the winner set, so
	// rotation must fail loudly instead of dropping candidates silently.
	evaluator := &scriptEvaluator{marker: "approved"}
	judge := &scriptJudge{evaluator: evaluator}
	answers := make([]ApprovedAnswer, 0, 3)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("ans-%d", i)
		answers = append(answers, ApprovedAnswer{ID: id, Version: 1,
			TextSHA256: testSHA(id), Text: "approved " + id})
	}
	questions := []MatchQuestion{{ID: "q-1", Text: "Anything.", Required: "unknown", TextSHA256: testSHA("q1")}}
	_, err := MatchQuestions(context.Background(), judge.judge, "check-1",
		testSHA("set"), testSHA("cat"), 1000, questions, BuildCandidates(answers), 1)
	if err == nil || !strings.Contains(err.Error(), "recall levels") {
		t.Fatalf("err=%v, want a loud recall-level failure", err)
	}
}

func TestBuildCandidates(t *testing.T) {
	candidates := BuildCandidates(testLibrary())
	if len(candidates) != 3 || candidates[0].AnswerID != "ans-a" || candidates[1].AnswerID != "ans-b" {
		t.Fatalf("order=%v, want deterministic id order", candidates)
	}
	long := ApprovedAnswer{ID: "ans-long", Version: 1, TextSHA256: testSHA("l"),
		Text: strings.Repeat("é", 1500)}
	excerpt := BuildCandidates([]ApprovedAnswer{long})[0].Excerpt
	if len(excerpt) > jev.AnswerMatchMaxExcerpt || !strings.HasPrefix(long.Text, excerpt) {
		t.Fatalf("excerpt=%d bytes, want a leading excerpt within the cap", len(excerpt))
	}
	for i := range excerpt {
		_ = i
	}
}

func TestMatchChoiceSummary(t *testing.T) {
	fit := MatchChoiceSummary("q-1", jev.AnswerMatchSelection{QuestionID: "q-1", AnswerID: "ans-a", AnswerVersion: 3})
	if !strings.Contains(fit, "ans-a") || !strings.Contains(fit, "3") {
		t.Fatalf("summary=%q", fit)
	}
	none := MatchChoiceSummary("q-2", jev.AnswerMatchSelection{QuestionID: "q-2", NoneFits: true})
	if !strings.Contains(none, "no saved answer fits") {
		t.Fatalf("summary=%q", none)
	}
	if err := ValidateMatchQuestionInputs(testQuestions()); err != nil {
		t.Fatalf("valid questions: %v", err)
	}
	if err := ValidateMatchQuestionInputs([]MatchQuestion{{ID: "q-1"}, {ID: "q-1"}}); err == nil {
		t.Fatal("duplicate ids must fail")
	}
}
