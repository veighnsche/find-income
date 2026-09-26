package agency

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// Answer matching orchestration (K2). Jev Choice over owner-approved saved
// answers plus the explicit no-fitting-answer choice, with zero Codex/LLM
// involvement: matching depends only on the Jev Evaluator primitive,
// reached here through the caller's judge, and deterministic Go.
//
// Coverage replaces lexical exclusion: every approved current version
// reaches Jev for every question. Small libraries match in one round;
// larger ones rotate through coverage-preserving bounded recall batches
// whose per-question winners meet in a runoff, which itself rotates when
// needed. No batch silently drops a relevant candidate: an oversized
// library fails loudly instead. Selections persist through the caller's
// store writer; this file judges, it never stores.

// ApprovedAnswer is one current owner-approved library version. Only
// explicit approval confers membership; the loader below passes current
// versions and Jev can only choose among the offered criteria or no-fit.
type ApprovedAnswer struct {
	ID          string
	Version     int64
	TextSHA256  string
	Text        string
	ScopeTags   []string
	ContextNote string
}

// MatchQuestion is one actual saved employer question to match.
type MatchQuestion struct {
	ID         string
	Text       string
	Required   string
	Kind       string
	TextSHA256 string
}

// AnswerLibrary loads the pinned catalog. Production wires it over the
// store's saved answers; tests supply fakes.
type AnswerLibrary interface {
	CatalogDigest(ctx context.Context) (string, error)
	CurrentAnswers(ctx context.Context) ([]ApprovedAnswer, error)
}

// BatchJudge judges one Jev batch and reports the recorded attempt.
// batchKey distinguishes recall rounds ("recall/<level>/<slice>") from
// the final runoff ("runoff") so the caller records each exchange under
// its own request-key prefix. The orchestrator invokes the judge only
// when at least one question offers candidates; deterministic-only
// batches resolve in-process without a provider call.
type BatchJudge func(ctx context.Context, batchKey string, input jev.AnswerMatchInput) (jev.AnswerMatchResult, string, error)

// MatchBatch is one final per-question-chunk outcome for the caller to
// persist. Recall rounds are evidence only and never persist: only the
// runoff selections become saved matches.
type MatchBatch struct {
	Input     jev.AnswerMatchInput
	Result    jev.AnswerMatchResult
	AttemptID string
}

// maxMatchLevels bounds recall rotation: 24 candidates per Jev choice
// times four levels covers any plausible approved library, and anything
// beyond fails loudly instead of dropping candidates silently.
const maxMatchLevels = 4

// BuildCandidates renders approved current versions as Jev candidates in
// deterministic id order. The excerpt is the leading approved text at a
// UTF-8 boundary, matching the store's substring verification rule.
func BuildCandidates(answers []ApprovedAnswer) []jev.AnswerMatchCandidate {
	ordered := append([]ApprovedAnswer(nil), answers...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	out := make([]jev.AnswerMatchCandidate, 0, len(ordered))
	for _, answer := range ordered {
		out = append(out, jev.AnswerMatchCandidate{AnswerID: answer.ID,
			AnswerVersion: answer.Version, TextSHA256: answer.TextSHA256,
			ScopeTags:   append([]string(nil), answer.ScopeTags...),
			ContextNote: answer.ContextNote, Excerpt: leadingExcerpt(answer.Text)})
	}
	return out
}

func leadingExcerpt(text string) string {
	if len(text) <= jev.AnswerMatchMaxExcerpt {
		return text
	}
	cut := jev.AnswerMatchMaxExcerpt
	for cut > 0 && !utf8.ValidString(text[:cut]) {
		cut--
	}
	return text[:cut]
}

// MatchQuestions judges every question against the approved catalog and
// returns the final persistable batches. perQuestionCap bounds one Jev
// choice; values outside 1..AnswerMatchMaxCandidates clamp to the Jev
// maximum. An empty catalog resolves every question to deterministic
// no-fit without invoking the judge: zero questions and zero answers
// both cost zero Jev calls.
func MatchQuestions(ctx context.Context, judge BatchJudge, checkID, questionSetSHA256, catalogDigest string,
	maxReportedTokens int64, questions []MatchQuestion, candidates []jev.AnswerMatchCandidate, perQuestionCap int) ([]MatchBatch, error) {
	if checkID == "" || len(questionSetSHA256) != 64 || len(catalogDigest) != 64 ||
		maxReportedTokens < 1 || len(questions) == 0 {
		return nil, fmt.Errorf("agency: invalid match pins")
	}
	if judge == nil {
		return nil, fmt.Errorf("agency: match needs a judge")
	}
	cap := perQuestionCap
	if cap < 1 || cap > jev.AnswerMatchMaxCandidates {
		cap = jev.AnswerMatchMaxCandidates
	}
	remaining := make(map[string][]jev.AnswerMatchCandidate, len(questions))
	for _, question := range questions {
		if question.ID == "" {
			return nil, fmt.Errorf("agency: match question needs an id")
		}
		remaining[question.ID] = candidates
	}
	pins := matchPins{checkID: checkID, questionSetSHA256: questionSetSHA256,
		catalogDigest: catalogDigest, maxReportedTokens: maxReportedTokens}
	for level := 0; ; level++ {
		if matchFits(remaining, cap) {
			return matchRunoff(ctx, judge, pins, questions, remaining)
		}
		if level >= maxMatchLevels {
			return nil, fmt.Errorf("agency: approved library exceeds %d recall levels; narrow its scope instead of dropping candidates", maxMatchLevels)
		}
		winners, err := matchRecallLevel(ctx, judge, pins, level, questions, remaining, cap)
		if err != nil {
			return nil, err
		}
		remaining = winners
	}
}

type matchPins struct {
	checkID           string
	questionSetSHA256 string
	catalogDigest     string
	maxReportedTokens int64
}

func matchFits(remaining map[string][]jev.AnswerMatchCandidate, cap int) bool {
	for _, candidates := range remaining {
		if len(candidates) > cap {
			return false
		}
	}
	return true
}

// matchRecallLevel rotates every per-question candidate list through Jev
// and collects the distinct per-question winners for the next level.
// Recall selections never persist; questions left without a winner
// resolve to no-fit in the runoff.
func matchRecallLevel(ctx context.Context, judge BatchJudge, pins matchPins, level int,
	questions []MatchQuestion, remaining map[string][]jev.AnswerMatchCandidate, cap int) (map[string][]jev.AnswerMatchCandidate, error) {
	slices := 0
	for _, candidates := range remaining {
		if need := (len(candidates) + cap - 1) / cap; need > slices {
			slices = need
		}
	}
	byID := make(map[string]jev.AnswerMatchCandidate, len(questions)*cap)
	for _, candidates := range remaining {
		for _, candidate := range candidates {
			byID[candidate.AnswerID] = candidate
		}
	}
	winnerIDs := make(map[string]map[string]bool, len(questions))
	for _, question := range questions {
		winnerIDs[question.ID] = map[string]bool{}
	}
	for slice := 0; slice < slices; slice++ {
		for _, chunk := range chunkQuestions(questions) {
			input := jev.AnswerMatchInput{CheckID: pins.checkID,
				QuestionSetSHA256: pins.questionSetSHA256, AnswerCatalogDigest: pins.catalogDigest,
				MaxReportedTokens: pins.maxReportedTokens}
			judged := false
			for _, question := range chunk {
				offered := candidateSlice(remaining[question.ID], slice, cap)
				if len(offered) > 0 {
					judged = true
				}
				input.Questions = append(input.Questions, jev.AnswerMatchQuestion{
					QuestionID: question.ID, Text: question.Text, Required: question.Required,
					Kind: question.Kind, TextSHA256: question.TextSHA256, Candidates: offered})
			}
			var result jev.AnswerMatchResult
			var err error
			if !judged {
				result, err = jev.MatchAnswers(ctx, nil, input)
			} else {
				var ignored string
				result, ignored, err = judge(ctx, fmt.Sprintf("recall/%d/%d", level, slice), input)
				_ = ignored
			}
			if err != nil {
				return nil, err
			}
			for _, selection := range result.Selections {
				if selection.NoneFits || selection.Deterministic {
					continue
				}
				winnerIDs[selection.QuestionID][selection.AnswerID] = true
			}
		}
	}
	next := make(map[string][]jev.AnswerMatchCandidate, len(questions))
	for _, question := range questions {
		ids := make([]string, 0, len(winnerIDs[question.ID]))
		for id := range winnerIDs[question.ID] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			next[question.ID] = append(next[question.ID], byID[id])
		}
	}
	return next, nil
}

// matchRunoff judges the final per-question candidates and returns the
// persistable batches. Questions without candidates resolve
// deterministically; chunks without any judged question never invoke the
// judge.
func matchRunoff(ctx context.Context, judge BatchJudge, pins matchPins,
	questions []MatchQuestion, remaining map[string][]jev.AnswerMatchCandidate) ([]MatchBatch, error) {
	batches := []MatchBatch{}
	for _, chunk := range chunkQuestions(questions) {
		input := jev.AnswerMatchInput{CheckID: pins.checkID,
			QuestionSetSHA256: pins.questionSetSHA256, AnswerCatalogDigest: pins.catalogDigest,
			MaxReportedTokens: pins.maxReportedTokens}
		judged := false
		for _, question := range chunk {
			candidates := remaining[question.ID]
			if len(candidates) > 0 {
				judged = true
			}
			input.Questions = append(input.Questions, jev.AnswerMatchQuestion{
				QuestionID: question.ID, Text: question.Text, Required: question.Required,
				Kind: question.Kind, TextSHA256: question.TextSHA256, Candidates: candidates})
		}
		if !judged {
			result, err := jev.MatchAnswers(ctx, nil, input)
			if err != nil {
				return nil, err
			}
			batches = append(batches, MatchBatch{Input: input, Result: result})
			continue
		}
		result, attemptID, err := judge(ctx, "runoff", input)
		if err != nil {
			return nil, err
		}
		batches = append(batches, MatchBatch{Input: input, Result: result, AttemptID: attemptID})
	}
	return batches, nil
}

func chunkQuestions(questions []MatchQuestion) [][]MatchQuestion {
	var chunks [][]MatchQuestion
	for start := 0; start < len(questions); start += jev.AnswerMatchMaxQuestions {
		end := start + jev.AnswerMatchMaxQuestions
		if end > len(questions) {
			end = len(questions)
		}
		chunks = append(chunks, questions[start:end])
	}
	return chunks
}

func candidateSlice(candidates []jev.AnswerMatchCandidate, slice, cap int) []jev.AnswerMatchCandidate {
	start := slice * cap
	if start >= len(candidates) {
		return nil
	}
	end := start + cap
	if end > len(candidates) {
		end = len(candidates)
	}
	return candidates[start:end]
}

// OfferedCandidateIDs reports every candidate id offered across the given
// inputs in first-seen order. Tests use it to prove coverage; production
// tracing can log it as match evidence.
func OfferedCandidateIDs(inputs []jev.AnswerMatchInput) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, input := range inputs {
		for _, question := range input.Questions {
			for _, candidate := range question.Candidates {
				if seen[candidate.AnswerID] {
					continue
				}
				seen[candidate.AnswerID] = true
				ids = append(ids, candidate.AnswerID)
			}
		}
	}
	return ids
}

// MatchChoiceSummary renders one persisted selection for honest display.
// It reuses saved match text only and performs no model call; confidence
// stays audit-only and never gates what is shown.
func MatchChoiceSummary(questionID string, selection jev.AnswerMatchSelection) string {
	if selection.NoneFits {
		return "question " + questionID + ": no saved answer fits"
	}
	return "question " + questionID + ": saved answer " + selection.AnswerID +
		" version " + fmt.Sprintf("%d", selection.AnswerVersion)
}

// ValidateMatchQuestionInputs rejects blank or duplicate question ids
// before a match commission charges anything.
func ValidateMatchQuestionInputs(questions []MatchQuestion) error {
	seen := map[string]bool{}
	for _, question := range questions {
		if strings.TrimSpace(question.ID) == "" || seen[question.ID] {
			return fmt.Errorf("agency: match questions need distinct ids")
		}
		seen[question.ID] = true
	}
	return nil
}
