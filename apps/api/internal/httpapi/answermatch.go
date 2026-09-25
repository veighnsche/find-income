package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// AnswerMatcher judges one answer batch through Jev. Deterministic-only
// batches never touch it, so matching without approved answers (or with an
// unconfigured Jev client plus only deterministic questions) needs no model.
type AnswerMatcher interface {
	RunAnswerMatch(ctx context.Context, binding jevservice.Binding, input jev.AnswerMatchInput) (jev.AnswerMatchResult, error)
}

func failMatch(w http.ResponseWriter, err error, reason string) {
	switch {
	case errors.Is(err, store.ErrRoleNotSelected), errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role or match not found.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid match request.")
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrRoundIdempotencyConflict):
		failConflict(w, reason)
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Match request failed.")
	}
}

func failConflict(w http.ResponseWriter, reason string) {
	body := struct {
		Error struct {
			Code    generated.ApiErrorCode `json:"code"`
			Message string                 `json:"message"`
			Details map[string]string      `json:"details,omitempty"`
		} `json:"error"`
	}{}
	body.Error.Code = generated.ApiErrorCodeConflict
	body.Error.Message = "Match state changed; refresh before retrying."
	if reason != "" {
		body.Error.Details = map[string]string{"reason": reason}
	}
	writeJSON(w, http.StatusConflict, body)
}

func answerMatchModel(value store.AnswerMatchView) generated.AnswerMatchView {
	model := generated.AnswerMatchView{Status: generated.AnswerMatchViewStatus(value.Status),
		CheckId: value.CheckID, QuestionSetSha256: value.QuestionSetSHA256}
	model.AnswerCatalog.Digest = value.AnswerCatalogDigest
	model.AnswerCatalog.MatchedAt = recordedTime(value.CatalogMatchedAt)
	model.Matches = []generated.AnswerMatch{}
	for _, match := range value.Matches {
		item := generated.AnswerMatch{CandidateSetHash: match.CandidateSetHash,
			Confidence: float32(match.Confidence), JevAttemptId: match.JevAttemptID,
			MatchedAt: recordedTime(match.MatchedAt), QuestionId: match.QuestionID,
			QuestionTextSha256: match.QuestionTextSHA256}
		if match.Model != "" {
			item.Model = &match.Model
		}
		if match.Choice.NoneFits {
			none := true
			item.Choice.NoneFits = &none
		} else {
			item.Choice.AnswerId = &match.Choice.AnswerID
			item.Choice.AnswerVersion = &match.Choice.AnswerVersion
			item.Choice.TextSha256 = &match.Choice.AnswerTextSHA256
		}
		model.Matches = append(model.Matches, item)
	}
	return model
}

// answerExcerpt takes the leading bytes of approved text at a UTF-8 boundary,
// matching the store's excerpt verification rule.
func answerExcerpt(text string) string {
	const maxExcerpt = 2000
	if len(text) <= maxExcerpt {
		return text
	}
	cut := maxExcerpt
	for cut > 0 && !utf8.ValidString(text[:cut]) {
		cut--
	}
	return text[:cut]
}

func (h *Handler) matchCandidates(ctx context.Context) ([]jev.AnswerMatchCandidate, string, error) {
	digest, err := h.database.AnswerCatalogDigest(ctx)
	if err != nil {
		return nil, "", err
	}
	candidates := []jev.AnswerMatchCandidate{}
	cursor := ""
	for {
		page, err := h.database.ListSavedAnswers(ctx, store.SavedAnswerListOptions{Limit: 100, Cursor: cursor})
		if err != nil {
			return nil, "", err
		}
		for _, answer := range page.Items {
			for _, version := range answer.Versions {
				if version.Version != answer.CurrentVersion {
					continue
				}
				candidates = append(candidates, jev.AnswerMatchCandidate{AnswerID: answer.ID,
					AnswerVersion: version.Version, TextSHA256: version.TextSHA256,
					ScopeTags: answer.ScopeTags, ContextNote: answer.ContextNote,
					Excerpt: answerExcerpt(version.Text)})
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return candidates, digest, nil
}

// startMatchRound opens (or reuses, after a crash) the scoped round for one
// match POST. The key derives from the match request key so retries replay
// instead of colliding; a terminal round from an earlier call falls through
// to a fresh suffixed key.
func (h *Handler) startMatchRound(ctx context.Context, actor store.Actor, opportunityID, requestKey string, batches int64, profileVersion int64) (store.Round, error) {
	open := func(key string) (store.Round, bool, error) {
		return h.database.StartRound(ctx, actor, store.StartRoundInput{RequestKey: key,
			Intent: "Match saved employer questions to approved answers.", Outcome: "answer_match",
			ProfileVersion: profileVersion,
			Scope:          store.RoundScope{Resources: []string{"opportunity:" + opportunityID}, Operations: []string{store.RoundJevRequest}},
			Limits:         store.RoundAllowance{Requests: batches + 8, Items: 100, Tools: 4, Turns: 8},
			Deadline:       time.Now().UTC().Add(10 * time.Minute)})
	}
	round, created, err := open("answer-match:" + requestKey)
	if err != nil {
		return store.Round{}, err
	}
	if !created && (round.State == store.RoundCompleted || round.State == store.RoundFailed) {
		round, _, err = open(fmt.Sprintf("answer-match:%s:%d", requestKey, time.Now().UTC().UnixNano()))
		if err != nil {
			return store.Round{}, err
		}
	}
	if round.State != store.RoundRunning {
		round, err = h.database.ActivateRound(ctx, actor, round.ID)
		if err != nil {
			return store.Round{}, err
		}
	}
	return round, nil
}

func (h *Handler) matchOpportunityAnswers(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.AnswerMatchRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	opportunityID := r.PathValue("id")
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	check, err := h.database.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		failMatch(w, err, "")
		return
	}
	if check.Check == nil {
		failConflict(w, "check_not_started")
		return
	}
	if check.Status != string(store.CheckOverallChecked) {
		failConflict(w, "check_not_complete")
		return
	}
	if body.ExpectedCheckId != check.Check.ID || body.ExpectedQuestionSetSha256 != check.Check.QuestionSetSHA256 {
		failConflict(w, "outdated_check")
		return
	}
	candidates, digest, err := h.matchCandidates(ctx)
	if err != nil {
		failMatch(w, err, "")
		return
	}
	covered := map[string]bool{}
	if current, err := h.database.CurrentAnswerMatch(ctx, opportunityID); err == nil &&
		current.CheckID == check.Check.ID && current.QuestionSetSHA256 == check.Check.QuestionSetSHA256 &&
		current.AnswerCatalogDigest == digest &&
		(current.Status == "matched" || current.Status == "unmatched") {
		writeJSON(w, http.StatusOK, answerMatchModel(current))
		return
	} else if err == nil && current.CheckID == check.Check.ID &&
		current.QuestionSetSHA256 == check.Check.QuestionSetSHA256 && current.AnswerCatalogDigest == digest {
		for _, match := range current.Matches {
			covered[match.QuestionID] = true
		}
	}
	questions := []jev.AnswerMatchQuestion{}
	for _, question := range check.Check.Questions {
		if covered[question.ID] {
			continue
		}
		questions = append(questions, jev.AnswerMatchQuestion{QuestionID: question.ID,
			Text: question.Text, Required: question.Required, Kind: question.Kind,
			TextSHA256: question.TextSHA256,
			Candidates: jev.PrefilterAnswerCandidates(question.Text, candidates, 8)})
	}
	if len(questions) == 0 {
		current, err := h.database.CurrentAnswerMatch(ctx, opportunityID)
		if err != nil {
			failMatch(w, err, "")
			return
		}
		writeJSON(w, http.StatusOK, answerMatchModel(current))
		return
	}
	prefs, err := h.database.CurrentPreferences(ctx)
	if err != nil {
		failMatch(w, err, "")
		return
	}
	batches := (len(questions) + jev.AnswerMatchMaxQuestions - 1) / jev.AnswerMatchMaxQuestions
	round, err := h.startMatchRound(ctx, actor, opportunityID, body.RequestKey, int64(batches), prefs.Version)
	if err != nil {
		failMatch(w, err, "")
		return
	}
	finish := func(terminal store.RoundState, reason string) {
		_, _ = h.database.FinishRound(ctx, actor, round.ID, terminal, reason, "answer_match", []byte(`{}`))
	}
	createdAny := false
	for batch := 0; batch < batches; batch++ {
		slice := questions[batch*jev.AnswerMatchMaxQuestions:]
		if len(slice) > jev.AnswerMatchMaxQuestions {
			slice = slice[:jev.AnswerMatchMaxQuestions]
		}
		input := jev.AnswerMatchInput{CheckID: check.Check.ID,
			QuestionSetSHA256: check.Check.QuestionSetSHA256, AnswerCatalogDigest: digest,
			MaxReportedTokens: jev.AnswerMatchMaxTokens, Questions: slice}
		needsJev := false
		for _, question := range slice {
			if len(question.Candidates) > 0 {
				needsJev = true
				break
			}
		}
		var result jev.AnswerMatchResult
		attemptID := ""
		if !needsJev {
			result, err = jev.MatchAnswers(ctx, nil, input)
		} else {
			if h.answerMatcher == nil {
				finish(store.RoundFailed, "jev_unavailable")
				fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable,
					"Answer matching needs Jev; partial batches stay saved.")
				return
			}
			prefix := fmt.Sprintf("answer-match/%s/%d", body.RequestKey, batch)
			result, err = h.answerMatcher.RunAnswerMatch(ctx, jevservice.Binding{Actor: actor,
				RoundID: round.ID, ResourceID: "opportunity:" + opportunityID,
				RequestKeyPrefix: prefix, ProfileVersion: prefs.Version}, input)
			if err == nil {
				var ids []string
				ids, err = h.database.JevAttemptIDsForRequestPrefix(ctx, round.ID, prefix)
				if err == nil {
					if len(ids) != 1 {
						err = store.ErrConflict
					} else {
						attemptID = ids[0]
					}
				}
			}
		}
		if err != nil {
			finish(store.RoundFailed, "match_failed")
			if _, isJevErr := err.(*jev.Error); isJevErr {
				fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable,
					"Jev matching failed; partial batches stay saved.")
			} else {
				failMatch(w, err, "outdated_check")
			}
			return
		}
		_, created, err := h.database.SaveAnswerMatch(ctx, actor, round.ID, attemptID, body.RequestKey, input, result)
		if err != nil {
			finish(store.RoundFailed, "match_failed")
			failMatch(w, err, "outdated_check")
			return
		}
		createdAny = createdAny || created
	}
	finish(store.RoundCompleted, "match_saved")
	final, err := h.database.CurrentAnswerMatch(ctx, opportunityID)
	if err != nil {
		failMatch(w, err, "")
		return
	}
	status := http.StatusOK
	if createdAny {
		status = http.StatusCreated
	}
	writeJSON(w, status, answerMatchModel(final))
}

func (h *Handler) getCurrentAnswerMatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	value, err := h.database.CurrentAnswerMatch(r.Context(), r.PathValue("id"))
	if err != nil {
		failMatch(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, answerMatchModel(value))
}
