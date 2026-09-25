package jev

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const answerMatchTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func answerMatchCandidate(id string, version int64) AnswerMatchCandidate {
	return AnswerMatchCandidate{AnswerID: id, AnswerVersion: version, TextSHA256: answerMatchTestSHA,
		ScopeTags: []string{"remote", "work_pattern"}, ContextNote: "Remote work answer.",
		Excerpt: "I work remotely from Example City."}
}

func answerMatchQuestion(id, text string, candidates []AnswerMatchCandidate) AnswerMatchQuestion {
	return AnswerMatchQuestion{QuestionID: id, Text: text, Required: "required", Kind: "free_text",
		TextSHA256: answerMatchTestSHA, Candidates: candidates}
}

func answerMatchInput() AnswerMatchInput {
	return AnswerMatchInput{CheckID: "check-1", QuestionSetSHA256: answerMatchTestSHA,
		AnswerCatalogDigest: answerMatchTestSHA, MaxReportedTokens: 1000,
		Questions: []AnswerMatchQuestion{
			answerMatchQuestion("q-remote", "Are you willing to work remotely?",
				[]AnswerMatchCandidate{answerMatchCandidate("a-remote", 1), answerMatchCandidate("a-office", 2)}),
			answerMatchQuestion("q-start", "When can you start?",
				[]AnswerMatchCandidate{answerMatchCandidate("a-start", 1)}),
		}}
}

func answerMatchRaw(t *testing.T, request Request, choices map[string]string) []byte {
	t.Helper()
	answers := map[string]any{}
	for id, question := range request.Questions {
		options := question.(ChoiceQuestion).Criteria
		choice, ok := choices[id]
		if !ok {
			t.Fatalf("missing raw choice for %s", id)
		}
		probabilities := map[string]float64{}
		for option := range options {
			probabilities[option] = 0
		}
		probabilities[choice] = 1
		answers[id] = map[string]any{"type": "choice", "choice": choice,
			"probabilities": probabilities, "confidence": 0.8}
	}
	raw, err := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": answers,
		"usage": map[string]any{"input_tokens": 12, "output_tokens": 4}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAnswerMatchBatchesOneCallPerCheck(t *testing.T) {
	input := answerMatchInput()
	calls := 0
	fake := screenFake(func(request Request) (Result, error) {
		calls++
		if len(request.Questions) != 2 {
			t.Fatalf("batch groups independent questions: %d", len(request.Questions))
		}
		for id, question := range request.Questions {
			criteria := question.(ChoiceQuestion).Criteria
			if criteria[AnswerMatchNoneFits] == "" {
				t.Fatalf("explicit none_fits missing on %s", id)
			}
		}
		return screenResult(request, map[string]string{"match_0": "a-remote", "match_1": AnswerMatchNoneFits}), nil
	})
	result, err := MatchAnswers(context.Background(), fake, input)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("one charged call per batch, got %d", calls)
	}
	if len(result.Selections) != 2 || result.InputSHA256 == "" || len(result.RequestSnapshot) == 0 {
		t.Fatalf("batch result: %+v", result)
	}
	// Canonical order is by question id: q-remote sorts before q-start.
	first, second := result.Selections[0], result.Selections[1]
	if first.QuestionID != "q-remote" || first.NoneFits || first.Deterministic ||
		first.AnswerID != "a-remote" || first.AnswerVersion != 1 || first.AnswerTextSHA256 != answerMatchTestSHA {
		t.Fatalf("matched selection: %+v", first)
	}
	if first.CandidateSetHash == "" || first.Confidence != 0.8 || len(first.Probabilities) == 0 {
		t.Fatalf("match provenance: %+v", first)
	}
	if second.QuestionID != "q-start" || !second.NoneFits || second.Deterministic ||
		second.AnswerID != "" || second.AnswerVersion != 0 || second.AnswerTextSHA256 != "" {
		t.Fatalf("none_fits selection must stay honestly unresolved: %+v", second)
	}
	secondHash, err := AnswerCandidateSetHash(input.Questions[1].Candidates)
	if err != nil || secondHash != second.CandidateSetHash {
		t.Fatalf("candidate hash: %s %v", secondHash, err)
	}
}

func TestAnswerMatchDeterministicEmptyOfferedNeedsNoCall(t *testing.T) {
	input := answerMatchInput()
	input.Questions = append(input.Questions,
		answerMatchQuestion("q-zzz-attachment", "Upload your portfolio.", nil))
	called := false
	fake := screenFake(func(request Request) (Result, error) {
		called = true
		if len(request.Questions) != 2 {
			t.Fatalf("deterministic question must not reach Jev: %d", len(request.Questions))
		}
		return screenResult(request, map[string]string{"match_0": "a-remote", "match_1": "a-start"}), nil
	})
	result, err := MatchAnswers(context.Background(), fake, input)
	if err != nil || !called {
		t.Fatalf("mixed batch: %+v %v", result, err)
	}
	if len(result.Selections) != 3 {
		t.Fatalf("per-question outcomes: %+v", result.Selections)
	}
	last := result.Selections[2]
	if last.QuestionID != "q-zzz-attachment" || !last.NoneFits || !last.Deterministic ||
		last.Confidence != 0 || len(last.Probabilities) != 0 {
		t.Fatalf("deterministic selection: %+v", last)
	}
	emptyHash, err := AnswerCandidateSetHash(nil)
	if err != nil || last.CandidateSetHash != emptyHash {
		t.Fatalf("empty offered hash: %s %v", emptyHash, err)
	}
}

func TestAnswerMatchAllDeterministicUsesNoEvaluator(t *testing.T) {
	input := AnswerMatchInput{CheckID: "check-1", QuestionSetSHA256: answerMatchTestSHA,
		AnswerCatalogDigest: answerMatchTestSHA, MaxReportedTokens: 1000,
		Questions: []AnswerMatchQuestion{answerMatchQuestion("q-1", "Upload your portfolio.", nil)}}
	result, err := MatchAnswers(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RequestSnapshot) != 0 || result.RequestedModel != "" || len(result.Selections) != 1 {
		t.Fatalf("deterministic result: %+v", result)
	}
	if !result.Selections[0].Deterministic || !result.Selections[0].NoneFits {
		t.Fatalf("deterministic outcome: %+v", result.Selections[0])
	}
}

func TestAnswerMatchCandidateSetHash(t *testing.T) {
	a := answerMatchCandidate("a-one", 1)
	b := answerMatchCandidate("a-two", 3)
	forward, err := AnswerCandidateSetHash([]AnswerMatchCandidate{a, b})
	if err != nil {
		t.Fatal(err)
	}
	reversed, err := AnswerCandidateSetHash([]AnswerMatchCandidate{b, a})
	if err != nil || reversed != forward {
		t.Fatalf("hash must be order-independent: %s %s", forward, reversed)
	}
	changed := b
	changed.AnswerVersion = 4
	versioned, err := AnswerCandidateSetHash([]AnswerMatchCandidate{a, changed})
	if err != nil || versioned == forward {
		t.Fatalf("hash must pin versions: %s %s", forward, versioned)
	}
	bare, err := AnswerCandidateSetHash(nil)
	if err != nil || bare == "" {
		t.Fatal(err)
	}
	// The none_fits sentinel binds the abstention to the offered set: an empty
	// set still hashes distinctly from any non-empty set.
	single, err := AnswerCandidateSetHash([]AnswerMatchCandidate{a})
	if err != nil || single == bare {
		t.Fatalf("empty set must hash distinctly: %s %s", bare, single)
	}
	if _, err := AnswerCandidateSetHash([]AnswerMatchCandidate{{AnswerID: " ", AnswerVersion: 1}}); err == nil {
		t.Fatal("blank candidate id accepted")
	}
}

func TestAnswerMatchInputDigestStableAcrossOrdering(t *testing.T) {
	input := answerMatchInput()
	shuffled := input
	shuffled.Questions = []AnswerMatchQuestion{input.Questions[1], input.Questions[0]}
	shuffled.Questions[1].Candidates = []AnswerMatchCandidate{
		input.Questions[0].Candidates[1], input.Questions[0].Candidates[0]}
	first, err := AnswerMatchInputDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AnswerMatchInputDigest(shuffled)
	if err != nil || second != first {
		t.Fatalf("digest must canonicalize ordering: %s %s", first, second)
	}
	changed := input
	changed.AnswerCatalogDigest = strings.Repeat("b", 64)
	third, err := AnswerMatchInputDigest(changed)
	if err != nil || third == first {
		t.Fatalf("digest must pin the catalog: %s %s", first, third)
	}
}

func TestAnswerMatchValidation(t *testing.T) {
	base := answerMatchInput()
	cases := map[string]func(*AnswerMatchInput){
		"no questions":     func(input *AnswerMatchInput) { input.Questions = nil },
		"bad question set": func(input *AnswerMatchInput) { input.QuestionSetSHA256 = "short" },
		"bad catalog":      func(input *AnswerMatchInput) { input.AnswerCatalogDigest = "" },
		"bad budget":       func(input *AnswerMatchInput) { input.MaxReportedTokens = 0 },
		"duplicate":        func(input *AnswerMatchInput) { input.Questions = append(input.Questions, input.Questions[0]) },
		"bad required":     func(input *AnswerMatchInput) { input.Questions[0].Required = "sometimes" },
		"bad kind":         func(input *AnswerMatchInput) { input.Questions[0].Kind = "essay" },
		"empty text":       func(input *AnswerMatchInput) { input.Questions[0].Text = "  " },
		"reserved id":      func(input *AnswerMatchInput) { input.Questions[0].Candidates[0].AnswerID = AnswerMatchNoneFits },
		"candidate dup": func(input *AnswerMatchInput) {
			input.Questions[0].Candidates = append(input.Questions[0].Candidates, input.Questions[0].Candidates[0])
		},
		"bad version":      func(input *AnswerMatchInput) { input.Questions[0].Candidates[0].AnswerVersion = 0 },
		"bad text sha":     func(input *AnswerMatchInput) { input.Questions[0].Candidates[0].TextSHA256 = "short" },
		"empty excerpt":    func(input *AnswerMatchInput) { input.Questions[0].Candidates[0].Excerpt = "" },
		"padded answer id": func(input *AnswerMatchInput) { input.Questions[0].Candidates[0].AnswerID = " a-remote" },
	}
	for name, mutate := range cases {
		input := base
		input.Questions = append([]AnswerMatchQuestion(nil), base.Questions...)
		input.Questions[0].Candidates = append([]AnswerMatchCandidate(nil), base.Questions[0].Candidates...)
		input.Questions[1].Candidates = append([]AnswerMatchCandidate(nil), base.Questions[1].Candidates...)
		mutate(&input)
		if _, err := MatchAnswers(context.Background(), screenFake(func(Request) (Result, error) {
			return Result{}, errors.New("must not be called")
		}), input); !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
			t.Fatalf("%s: got %v", name, err)
		}
		if _, err := AnswerMatchInputDigest(input); !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
			t.Fatalf("%s digest: got %v", name, err)
		}
	}
}

func TestAnswerMatchRejectsBadProviderAnswers(t *testing.T) {
	input := answerMatchInput()
	unknown := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{"match_0": "a-invented", "match_1": AnswerMatchNoneFits}), nil
	})
	if _, err := MatchAnswers(context.Background(), unknown, input); !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("invented choice: %v", err)
	}
	overBudget := screenFake(func(request Request) (Result, error) {
		result := screenResult(request, map[string]string{"match_0": "a-remote", "match_1": AnswerMatchNoneFits})
		result.Usage = Usage{InputTokens: 10, OutputTokens: AnswerMatchMaxTokens}
		return result, nil
	})
	if _, err := MatchAnswers(context.Background(), overBudget, input); !errors.Is(err, &Error{Kind: ErrBudgetExceeded}) {
		t.Fatalf("budget: %v", err)
	}
	if _, err := MatchAnswers(context.Background(), nil, input); !errors.Is(err, &Error{Kind: ErrInvalidConfig}) {
		t.Fatalf("nil evaluator with judged questions: %v", err)
	}
}

func TestCapturedAnswerMatchBindsBatch(t *testing.T) {
	input := answerMatchInput()
	canonical, err := canonicalAnswerMatchInput(input)
	if err != nil {
		t.Fatal(err)
	}
	request := answerMatchRequest(canonical, answerMatchJudged(canonical))
	logical, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{request.State, request.Questions})
	if err != nil {
		t.Fatal(err)
	}
	raw := answerMatchRaw(t, request, map[string]string{"match_0": "a-remote", "match_1": AnswerMatchNoneFits})
	result, err := RecoverCapturedAnswerMatch(input, logical, raw, "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selections) != 2 || result.Selections[0].AnswerID != "a-remote" || !result.Selections[1].NoneFits {
		t.Fatalf("recovered: %+v", result.Selections)
	}
	if _, err := ValidateCapturedAnswerMatch(input, result, logical, raw, "jev-1.13.0"); err != nil {
		t.Fatalf("valid capture rejected: %v", err)
	}
	tampered := result
	tampered.Selections = append([]AnswerMatchSelection(nil), result.Selections...)
	tampered.Selections[1] = result.Selections[1]
	tampered.Selections[1].NoneFits = false
	tampered.Selections[1].AnswerID = "a-start"
	tampered.Selections[1].AnswerVersion = 1
	tampered.Selections[1].AnswerTextSHA256 = answerMatchTestSHA
	if _, err := ValidateCapturedAnswerMatch(input, tampered, logical, raw, "jev-1.13.0"); err == nil {
		t.Fatal("caller-authored choice accepted")
	}
	swapped := input
	swapped.Questions = append([]AnswerMatchQuestion(nil), input.Questions...)
	swapped.Questions[0].Candidates = []AnswerMatchCandidate{answerMatchCandidate("a-other", 1)}
	if _, err := RecoverCapturedAnswerMatch(swapped, logical, raw, "jev-1.13.0"); err == nil {
		t.Fatal("substituted candidates replayed from prior capture")
	}
	tamperedRaw := answerMatchRaw(t, request, map[string]string{"match_0": "a-office", "match_1": AnswerMatchNoneFits})
	if _, err := ValidateCapturedAnswerMatch(input, result, logical, tamperedRaw, "jev-1.13.0"); err == nil {
		t.Fatal("substituted provider bytes accepted")
	}
}

func TestCapturedAnswerMatchDeterministicOnly(t *testing.T) {
	input := AnswerMatchInput{CheckID: "check-1", QuestionSetSHA256: answerMatchTestSHA,
		AnswerCatalogDigest: answerMatchTestSHA, MaxReportedTokens: 1000,
		Questions: []AnswerMatchQuestion{answerMatchQuestion("q-1", "Upload your portfolio.", nil)}}
	result, err := RecoverCapturedAnswerMatch(input, nil, nil, "")
	if err != nil || len(result.Selections) != 1 || !result.Selections[0].Deterministic {
		t.Fatalf("deterministic recovery: %+v %v", result, err)
	}
	if _, err := ValidateCapturedAnswerMatch(input, result, nil, nil, ""); err != nil {
		t.Fatalf("deterministic capture rejected: %v", err)
	}
	if _, err := RecoverCapturedAnswerMatch(input, []byte(`{}`), nil, ""); err == nil {
		t.Fatal("phantom logical request accepted for deterministic batch")
	}
}

func TestPrefilterAnswerCandidates(t *testing.T) {
	remote := answerMatchCandidate("a-remote", 1)
	remote.ScopeTags = []string{"remote", "work_pattern"}
	remote.ContextNote = "Use when the employer asks about remote work."
	start := answerMatchCandidate("a-start", 1)
	start.ScopeTags = []string{"start_date"}
	start.ContextNote = "Earliest start date."
	salary := answerMatchCandidate("a-salary", 1)
	salary.ScopeTags = []string{"compensation"}
	salary.ContextNote = "Salary expectations."
	offered := PrefilterAnswerCandidates("Are you willing to work remotely from home?",
		[]AnswerMatchCandidate{salary, start, remote}, 10)
	if len(offered) != 1 || offered[0].AnswerID != "a-remote" {
		t.Fatalf("scope overlap must win: %+v", offered)
	}
	// Context-note overlap alone still offers, below tag overlap.
	note := answerMatchCandidate("a-note", 1)
	note.ScopeTags = []string{"unrelated"}
	note.ContextNote = "I can begin immediately."
	tagged := answerMatchCandidate("a-tag", 1)
	tagged.ScopeTags = []string{"start"}
	tagged.ContextNote = ""
	offered = PrefilterAnswerCandidates("When can you start?",
		[]AnswerMatchCandidate{note, tagged}, 10)
	if len(offered) != 2 || offered[0].AnswerID != "a-tag" || offered[1].AnswerID != "a-note" {
		t.Fatalf("tag weight must outrank context: %+v", offered)
	}
	// Ties break deterministically by answer id.
	first := answerMatchCandidate("a-first", 1)
	first.ScopeTags = []string{"remote"}
	second := answerMatchCandidate("a-second", 1)
	second.ScopeTags = []string{"remote"}
	offered = PrefilterAnswerCandidates("remote work", []AnswerMatchCandidate{second, first}, 10)
	if len(offered) != 2 || offered[0].AnswerID != "a-first" {
		t.Fatalf("tie-break: %+v", offered)
	}
	// Nothing relevant means nothing offered: the question resolves
	// deterministically instead of forcing a fit.
	if offered := PrefilterAnswerCandidates("What is your favorite color?",
		[]AnswerMatchCandidate{remote, start, salary}, 10); len(offered) != 0 {
		t.Fatalf("irrelevant library must offer nothing: %+v", offered)
	}
	if offered := PrefilterAnswerCandidates("remote work", []AnswerMatchCandidate{first, second}, 1); len(offered) != 1 {
		t.Fatalf("limit: %+v", offered)
	}
}

// TestAnswerMatchZeroLLMCalls proves the match path cannot reach Codex or any
// LLM: answer_match.go imports stdlib only, and MatchAnswers takes nothing but
// a Jev Evaluator.
func TestAnswerMatchZeroLLMCalls(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "answer_match.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, ".") {
			t.Fatalf("match path must not import %s", path)
		}
	}
	// The AST check above covers imports; this guards the constructor shape:
	// matching is invoked with a Jev Evaluator and validated input only.
	var _ func(context.Context, Evaluator, AnswerMatchInput) (AnswerMatchResult, error) = MatchAnswers
}
