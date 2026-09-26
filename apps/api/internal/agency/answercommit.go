package agency

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Answer continuation (K3). The owner commits every visible answer choice
// — untouched kept suggestions, edited text, cleared boxes and explicit
// blanks — against the current job/check/question set, then advances the
// role exactly once. Choices persist first; the stage transitions only
// against their accepted versions. Stale pins, cross-job questions and
// concurrent writes conflict without false advancement, and failed or
// partial saves never advance. A zero-question route commits an empty
// answer set with zero Jev calls. Required blanks carrying an explicit
// draft request stay usable for grounded Standard drafting (C4); until
// the store records draft requests first-class (I-side), they hold the
// commit honestly instead of vanishing.

// AnswerChoiceOp is one exact accepted answer choice.
type AnswerChoiceOp string

const (
	// AnswerChoiceKeep persists the fresh Jev suggestion byte-identically.
	AnswerChoiceKeep AnswerChoiceOp = "keep"
	// AnswerChoiceEdit persists the owner's exact supplied text.
	AnswerChoiceEdit AnswerChoiceOp = "edit"
	// AnswerChoiceClear empties the box: a no-op when no value is saved,
	// otherwise an explicit blank, since answer rows are append-only
	// versions without delete.
	AnswerChoiceClear AnswerChoiceOp = "clear"
	// AnswerChoiceBlank persists an explicit blank.
	AnswerChoiceBlank AnswerChoiceOp = "blank"
)

// AnswerChoice is one accepted per-question choice with its guarded
// version. Text is set for edit only; DraftRequested marks a blank the
// owner asks Standard to draft from verified facts.
type AnswerChoice struct {
	QuestionID            string
	Op                    AnswerChoiceOp
	ExpectedAnswerVersion int64
	Text                  string
	DraftRequested        bool
}

// AnswerContinuationStore is the narrow persistence surface continuation
// needs. *store.Store satisfies it; tests supply fakes.
var _ AnswerContinuationStore = (*store.Store)(nil)

type AnswerContinuationStore interface {
	CurrentJobCheck(ctx context.Context, opportunityID string) (store.CheckStatusView, error)
	CurrentAnswerMatch(ctx context.Context, opportunityID string) (store.AnswerMatchView, error)
	CurrentQuestionAnswers(ctx context.Context, opportunityID string) (store.QuestionAnswerList, error)
	SavedAnswer(ctx context.Context, id string) (store.SavedAnswer, error)
	SaveAnswerValue(ctx context.Context, actor store.Actor, opportunityID, questionID string, input store.AnswerValueSaveInput) (store.QuestionAnswerValue, error)
	CommitRoleAnswers(ctx context.Context, actor store.Actor, opportunityID string) (store.RoleWorkflow, []string, error)
}

// Continuation hold/conflict reasons.
const (
	ContinueInvalidChoice    = "invalid_choice"
	ContinueOutdatedCheck    = "outdated_check"
	ContinueCheckNotComplete = "check_not_complete"
	ContinueUnknownQuestion  = "unknown_question"
	ContinueStaleSuggestion  = "stale_suggestion"
	ContinueSaveConflict     = "save_conflict"
	ContinueBasisMoved       = "basis_moved"
)

// ContinuationError is a typed continuation failure. Reason names the
// hold; QuestionID pins the affected question when there is one;
// CurrentVersion carries the accepted version for retry after a save
// conflict. The caller maps reasons to 400/409 without exposing store
// internals.
type ContinuationError struct {
	Reason         string
	QuestionID     string
	CurrentVersion int64
	Err            error
}

func (e *ContinuationError) Error() string {
	if e.QuestionID != "" {
		return fmt.Sprintf("agency: continue %s on %s: %s", e.Reason, e.QuestionID, e.Err)
	}
	return fmt.Sprintf("agency: continue %s: %s", e.Reason, e.Err)
}

func (e *ContinuationError) Unwrap() error { return e.Err }

// ChoiceResult is one persisted choice. Skipped marks a clear over an
// unsaved value; DraftRequested echoes the owner's drafting ask.
type ChoiceResult struct {
	QuestionID     string
	Saved          store.QuestionAnswerValue
	Skipped        bool
	DraftRequested bool
}

// ContinueResult is the continuation outcome. Committed reports the
// stage transition; Missing lists required questions with no usable
// value and no draft request; NeedsDraft lists required blanks whose
// explicit draft request holds the commit until drafted values land (or
// until the store commits draft requests first-class, I-side).
type ContinueResult struct {
	Choices    []ChoiceResult
	Workflow   store.RoleWorkflow
	Committed  bool
	Missing    []string
	NeedsDraft []string
}

// ContinueAnswers persists every accepted choice, then commits the role.
// The resolution phase validates all choices against the current
// check/match/values before any save; the save phase persists them in
// order and aborts on the first conflict; the commit phase transitions
// only against the accepted versions.
func ContinueAnswers(ctx context.Context, db AnswerContinuationStore, actor store.Actor,
	opportunityID, expectedCheckID, expectedQuestionSet string, choices []AnswerChoice) (ContinueResult, error) {
	if db == nil || opportunityID == "" || expectedCheckID == "" || len(expectedQuestionSet) != 64 {
		return ContinueResult{}, &ContinuationError{Reason: ContinueInvalidChoice,
			Err: fmt.Errorf("continuation needs a store, an opportunity and current check pins")}
	}
	seen := map[string]bool{}
	for _, choice := range choices {
		if err := validAnswerChoice(choice); err != nil {
			return ContinueResult{}, &ContinuationError{Reason: ContinueInvalidChoice,
				QuestionID: choice.QuestionID, Err: err}
		}
		if seen[choice.QuestionID] {
			return ContinueResult{}, &ContinuationError{Reason: ContinueInvalidChoice,
				QuestionID: choice.QuestionID, Err: fmt.Errorf("duplicate choice for one question")}
		}
		seen[choice.QuestionID] = true
	}
	check, err := db.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		return ContinueResult{}, &ContinuationError{Reason: ContinueOutdatedCheck, Err: err}
	}
	if check.Check == nil || check.Status != store.CheckOverallChecked {
		return ContinueResult{}, &ContinuationError{Reason: ContinueCheckNotComplete,
			Err: fmt.Errorf("answers need a completed check")}
	}
	if check.Check.ID != expectedCheckID || check.Check.QuestionSetSHA256 != expectedQuestionSet {
		return ContinueResult{}, &ContinuationError{Reason: ContinueOutdatedCheck,
			Err: fmt.Errorf("check moved; refresh before continuing")}
	}
	known := map[string]store.CheckQuestionView{}
	for _, question := range check.Check.Questions {
		known[question.ID] = question
	}
	for _, choice := range choices {
		if _, ok := known[choice.QuestionID]; !ok {
			return ContinueResult{}, &ContinuationError{Reason: ContinueUnknownQuestion,
				QuestionID: choice.QuestionID,
				Err:        fmt.Errorf("question is not on the current check")}
		}
	}
	if len(check.Check.Questions) == 0 {
		if len(choices) != 0 {
			return ContinueResult{}, &ContinuationError{Reason: ContinueUnknownQuestion,
				Err: fmt.Errorf("a questionless route commits an empty answer set")}
		}
		return commitContinuation(ctx, db, actor, opportunityID, nil)
	}
	values, err := db.CurrentQuestionAnswers(ctx, opportunityID)
	if err != nil {
		return ContinueResult{}, &ContinuationError{Reason: ContinueOutdatedCheck, Err: err}
	}
	if values.CheckID != expectedCheckID || values.QuestionSetSHA256 != expectedQuestionSet {
		return ContinueResult{}, &ContinuationError{Reason: ContinueOutdatedCheck,
			Err: fmt.Errorf("saved answers moved; refresh before continuing")}
	}
	savedVersion := map[string]int64{}
	for _, value := range values.Values {
		savedVersion[value.QuestionID] = value.Version
	}
	match, err := db.CurrentAnswerMatch(ctx, opportunityID)
	if err != nil {
		return ContinueResult{}, &ContinuationError{Reason: ContinueOutdatedCheck, Err: err}
	}
	resolved, err := resolveChoices(ctx, db, match, expectedCheckID, expectedQuestionSet, choices)
	if err != nil {
		return ContinueResult{}, err
	}
	results := make([]ChoiceResult, 0, len(resolved))
	for _, item := range resolved {
		if item.skip {
			results = append(results, ChoiceResult{QuestionID: item.choice.QuestionID,
				Skipped: true, DraftRequested: item.choice.DraftRequested})
			continue
		}
		saved, err := db.SaveAnswerValue(ctx, actor, opportunityID, item.choice.QuestionID,
			store.AnswerValueSaveInput{ExpectedAnswerVersion: item.choice.ExpectedAnswerVersion, Text: item.text})
		if err != nil {
			return ContinueResult{Choices: results}, &ContinuationError{Reason: ContinueSaveConflict,
				QuestionID: item.choice.QuestionID, CurrentVersion: savedVersion[item.choice.QuestionID], Err: err}
		}
		results = append(results, ChoiceResult{QuestionID: item.choice.QuestionID,
			Saved: saved, DraftRequested: item.choice.DraftRequested})
	}
	return commitContinuation(ctx, db, actor, opportunityID, results)
}

func validAnswerChoice(choice AnswerChoice) error {
	if choice.QuestionID == "" || choice.ExpectedAnswerVersion < 0 {
		return fmt.Errorf("choice needs a question id and a guarded version")
	}
	switch choice.Op {
	case AnswerChoiceKeep, AnswerChoiceClear, AnswerChoiceBlank:
		if choice.Text != "" {
			return fmt.Errorf("%s carries no text", choice.Op)
		}
	case AnswerChoiceEdit:
		if choice.Text == "" || len(choice.Text) > 20000 || !utf8.ValidString(choice.Text) {
			return fmt.Errorf("edit needs exact non-empty text within the value bound")
		}
	default:
		return fmt.Errorf("unknown choice op %q", choice.Op)
	}
	if choice.DraftRequested && choice.Op != AnswerChoiceBlank {
		return fmt.Errorf("draft requests ride explicit blanks only")
	}
	return nil
}

type resolvedChoice struct {
	choice AnswerChoice
	text   string
	skip   bool
}

// resolveChoices maps every choice to its exact save text before any
// write. Keep resolves the fresh concrete suggestion; a stale or
// missing suggestion fails the whole continuation naming the question,
// so no partial save can strand the rest.
func resolveChoices(ctx context.Context, db AnswerContinuationStore, match store.AnswerMatchView,
	checkID, questionSet string, choices []AnswerChoice) ([]resolvedChoice, error) {
	fresh := match.CheckID == checkID && match.QuestionSetSHA256 == questionSet && match.Status != store.AnswerMatchStatusOutdated
	concrete := map[string]store.AnswerMatchChoice{}
	if fresh {
		for _, item := range match.Matches {
			if !item.Choice.NoneFits && item.Choice.AnswerID != "" {
				concrete[item.QuestionID] = item.Choice
			}
		}
	}
	out := make([]resolvedChoice, 0, len(choices))
	for _, choice := range choices {
		switch choice.Op {
		case AnswerChoiceEdit:
			out = append(out, resolvedChoice{choice: choice, text: choice.Text})
		case AnswerChoiceBlank:
			out = append(out, resolvedChoice{choice: choice})
		case AnswerChoiceClear:
			// Clear over an unsaved value is a no-op; over a saved one
			// it persists an explicit blank.
			out = append(out, resolvedChoice{choice: choice, skip: choice.ExpectedAnswerVersion == 0})
		case AnswerChoiceKeep:
			pick, ok := concrete[choice.QuestionID]
			if !ok {
				return nil, &ContinuationError{Reason: ContinueStaleSuggestion,
					QuestionID: choice.QuestionID,
					Err:        fmt.Errorf("no fresh suggestion to keep; edit or rematch instead")}
			}
			text, err := approvedVersionText(ctx, db, pick)
			if err != nil {
				return nil, &ContinuationError{Reason: ContinueStaleSuggestion,
					QuestionID: choice.QuestionID, Err: err}
			}
			out = append(out, resolvedChoice{choice: choice, text: text})
		}
	}
	return out, nil
}

// approvedVersionText reads the exact approved text behind a fresh
// concrete suggestion. The store re-resolves the same suggestion on
// save; a mismatch there conflicts rather than persisting drift.
func approvedVersionText(ctx context.Context, db AnswerContinuationStore, pick store.AnswerMatchChoice) (string, error) {
	answer, err := db.SavedAnswer(ctx, pick.AnswerID)
	if err != nil {
		return "", err
	}
	for _, version := range answer.Versions {
		if version.Version == pick.AnswerVersion && version.TextSHA256 == pick.AnswerTextSHA256 {
			return version.Text, nil
		}
	}
	return "", fmt.Errorf("approved answer %s version %d changed", pick.AnswerID, pick.AnswerVersion)
}

// commitContinuation transitions the role against the accepted versions.
// Missing required values partition into plain missing and explicit
// draft requests; any other conflict is a basis move that fails loudly.
func commitContinuation(ctx context.Context, db AnswerContinuationStore, actor store.Actor,
	opportunityID string, results []ChoiceResult) (ContinueResult, error) {
	workflow, missing, err := db.CommitRoleAnswers(ctx, actor, opportunityID)
	if err == nil {
		return ContinueResult{Choices: results, Workflow: workflow, Committed: true}, nil
	}
	if len(missing) == 0 {
		return ContinueResult{Choices: results}, &ContinuationError{Reason: ContinueBasisMoved, Err: err}
	}
	draftRequested := map[string]bool{}
	for _, item := range results {
		if item.DraftRequested {
			draftRequested[item.QuestionID] = true
		}
	}
	out := ContinueResult{Choices: results, Workflow: workflow, Missing: []string{}, NeedsDraft: []string{}}
	for _, id := range missing {
		if draftRequested[id] {
			out.NeedsDraft = append(out.NeedsDraft, id)
		} else {
			out.Missing = append(out.Missing, id)
		}
	}
	return out, nil
}

// ContinuationConflict holds the accepted versions for retry after a
// save conflict. It re-reads the saved values verbatim; the owner keeps
// their text and reconciles before retrying.
func ContinuationConflict(ctx context.Context, db AnswerContinuationStore, opportunityID string) (store.QuestionAnswerList, error) {
	return db.CurrentQuestionAnswers(ctx, opportunityID)
}

// IsBlankText reports whether a choice text is blank. Only the empty
// string is blank; whitespace-only text stays answered byte-identically,
// matching the store gate.
func IsBlankText(text string) bool { return text == "" }
