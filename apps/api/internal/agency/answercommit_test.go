package agency

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// fakeContinuationStore is an AnswerContinuationStore with scripted reads
// and recorded writes.
type fakeContinuationStore struct {
	check  store.CheckStatusView
	match  store.AnswerMatchView
	values store.QuestionAnswerList
	answer store.SavedAnswer

	saved     []savedCall
	commits   int
	commit    store.RoleWorkflow
	missing   []string
	commitErr error
	saveErr   map[string]error
	version   map[string]int64
}

type savedCall struct {
	questionID string
	input      store.AnswerValueSaveInput
}

func (f *fakeContinuationStore) CurrentJobCheck(context.Context, string) (store.CheckStatusView, error) {
	return f.check, nil
}

func (f *fakeContinuationStore) CurrentAnswerMatch(context.Context, string) (store.AnswerMatchView, error) {
	return f.match, nil
}

func (f *fakeContinuationStore) CurrentQuestionAnswers(context.Context, string) (store.QuestionAnswerList, error) {
	return f.values, nil
}

func (f *fakeContinuationStore) SavedAnswer(_ context.Context, id string) (store.SavedAnswer, error) {
	if id != f.answer.ID {
		return store.SavedAnswer{}, store.ErrNotFound
	}
	return f.answer, nil
}

func (f *fakeContinuationStore) SaveAnswerValue(_ context.Context, _ store.Actor, _ string, questionID string, input store.AnswerValueSaveInput) (store.QuestionAnswerValue, error) {
	if err, ok := f.saveErr[questionID]; ok {
		return store.QuestionAnswerValue{}, err
	}
	f.saved = append(f.saved, savedCall{questionID: questionID, input: input})
	next := f.version[questionID] + 1
	f.version[questionID] = next
	state := store.AnswerValueStateAnswered
	if input.Text == "" {
		state = store.AnswerValueStateBlank
	}
	return store.QuestionAnswerValue{QuestionID: questionID, Version: next, State: state, Text: input.Text}, nil
}

func (f *fakeContinuationStore) CommitRoleAnswers(context.Context, store.Actor, string) (store.RoleWorkflow, []string, error) {
	f.commits++
	return f.commit, f.missing, f.commitErr
}

func continuationFixture() *fakeContinuationStore {
	set := testSHA("set")
	return &fakeContinuationStore{
		check: store.CheckStatusView{Status: store.CheckOverallChecked, Check: &store.CheckView{
			ID: "check-1", QuestionSetSHA256: set, Questions: []store.CheckQuestionView{
				{ID: "q-1", Required: store.CheckRequired},
				{ID: "q-2", Required: store.CheckOptional},
				{ID: "q-3", Required: store.CheckRequired},
			}}},
		match: store.AnswerMatchView{Status: store.AnswerMatchStatusMatched,
			CheckID: "check-1", QuestionSetSHA256: set, AnswerCatalogDigest: testSHA("cat"),
			Matches: []store.AnswerMatch{{QuestionID: "q-1",
				Choice: store.AnswerMatchChoice{AnswerID: "ans-1", AnswerVersion: 1, AnswerTextSHA256: testSHA("t1")}}}},
		values: store.QuestionAnswerList{CheckID: "check-1", QuestionSetSHA256: set},
		answer: store.SavedAnswer{ID: "ans-1", CurrentVersion: 1, Versions: []store.SavedAnswerVersion{
			{Version: 1, Text: "approved leadership text", TextSHA256: testSHA("t1")}}},
		commit:  store.RoleWorkflow{Stage: store.RoleStageAnswered, Revision: 4},
		version: map[string]int64{},
		saveErr: map[string]error{},
	}
}

func TestContinueAnswersPersistsThenCommits(t *testing.T) {
	db := continuationFixture()
	actor := store.Actor{Kind: "owner", ID: "owner"}
	result, err := ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"),
		[]AnswerChoice{
			{QuestionID: "q-1", Op: AnswerChoiceKeep},
			{QuestionID: "q-2", Op: AnswerChoiceEdit, Text: "my exact words"},
			{QuestionID: "q-3", Op: AnswerChoiceBlank, DraftRequested: true},
		})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || db.commits != 1 {
		t.Fatalf("result=%+v commits=%d, want one commit", result, db.commits)
	}
	if len(db.saved) != 3 {
		t.Fatalf("saves=%v, want all three choices persisted", db.saved)
	}
	if db.saved[0].input.Text != "approved leadership text" {
		t.Fatalf("keep saved %q, want the fresh suggestion byte-identically", db.saved[0].input.Text)
	}
	if db.saved[1].input.Text != "my exact words" {
		t.Fatalf("edit saved %q", db.saved[1].input.Text)
	}
	if db.saved[2].input.Text != "" {
		t.Fatalf("blank saved %q, want empty", db.saved[2].input.Text)
	}
	if !result.Choices[2].DraftRequested {
		t.Fatalf("choices=%+v, want the draft request echoed", result.Choices)
	}
}

func TestContinueAnswersClearSemantics(t *testing.T) {
	db := continuationFixture()
	actor := store.Actor{Kind: "owner", ID: "owner"}
	// Clear over an unsaved value skips the write.
	result, err := ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"),
		[]AnswerChoice{{QuestionID: "q-2", Op: AnswerChoiceClear}})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.saved) != 0 || !result.Choices[0].Skipped {
		t.Fatalf("saves=%v choices=%+v, want a skipped clear", db.saved, result.Choices)
	}
	// Clear over a saved value persists an explicit blank.
	db = continuationFixture()
	db.version["q-2"] = 1
	_, err = ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"),
		[]AnswerChoice{{QuestionID: "q-2", Op: AnswerChoiceClear, ExpectedAnswerVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.saved) != 1 || db.saved[0].input.Text != "" {
		t.Fatalf("saves=%v, want one blank save", db.saved)
	}
}

func TestContinueAnswersZeroQuestionsCommitEmpty(t *testing.T) {
	db := continuationFixture()
	db.check.Check.Questions = nil
	actor := store.Actor{Kind: "owner", ID: "owner"}
	result, err := ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || len(db.saved) != 0 || db.commits != 1 {
		t.Fatalf("result=%+v saves=%d commits=%d, want an empty-set commit", result, len(db.saved), db.commits)
	}
	_, err = ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"),
		[]AnswerChoice{{QuestionID: "q-1", Op: AnswerChoiceBlank}})
	if err == nil {
		t.Fatal("choices on a questionless route must fail")
	}
}

func TestContinueAnswersConflicts(t *testing.T) {
	actor := store.Actor{Kind: "owner", ID: "owner"}
	newCall := func(db *fakeContinuationStore, choices []AnswerChoice) error {
		_, err := ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"), choices)
		return err
	}
	// Stale pins.
	db := continuationFixture()
	var continuation *ContinuationError
	err := error(nil)
	_, err = ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("other"),
		[]AnswerChoice{{QuestionID: "q-1", Op: AnswerChoiceBlank}})
	if continuation, _ = err.(*ContinuationError); continuation == nil || continuation.Reason != ContinueOutdatedCheck {
		t.Fatalf("stale pins err=%v, want outdated_check", err)
	}
	// Cross-job question.
	db = continuationFixture()
	err = newCall(db, []AnswerChoice{{QuestionID: "q-zzz", Op: AnswerChoiceBlank}})
	if continuation, _ = err.(*ContinuationError); continuation == nil || continuation.Reason != ContinueUnknownQuestion {
		t.Fatalf("unknown question err=%v, want unknown_question", err)
	}
	// Stale suggestion: keep with no fresh concrete match.
	db = continuationFixture()
	db.match.Status = store.AnswerMatchStatusOutdated
	err = newCall(db, []AnswerChoice{{QuestionID: "q-1", Op: AnswerChoiceKeep}})
	if continuation, _ = err.(*ContinuationError); continuation == nil || continuation.Reason != ContinueStaleSuggestion {
		t.Fatalf("stale keep err=%v, want stale_suggestion", err)
	}
	if len(db.saved) != 0 || db.commits != 0 {
		t.Fatalf("saves=%d commits=%d, want nothing written before resolution", len(db.saved), db.commits)
	}
	// Failed save blocks the commit.
	db = continuationFixture()
	db.saveErr["q-2"] = store.ErrConflict
	_, err = ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"),
		[]AnswerChoice{
			{QuestionID: "q-1", Op: AnswerChoiceKeep},
			{QuestionID: "q-2", Op: AnswerChoiceEdit, Text: "loses the race"},
		})
	if continuation, _ = err.(*ContinuationError); continuation == nil || continuation.Reason != ContinueSaveConflict {
		t.Fatalf("save race err=%v, want save_conflict", err)
	}
	if db.commits != 0 {
		t.Fatalf("commits=%d, want no commit after a failed save", db.commits)
	}
	// Held commit partitions missing from draft requests without error.
	db = continuationFixture()
	db.commitErr = store.ErrConflict
	db.missing = []string{"q-1", "q-3"}
	result, err := ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"),
		[]AnswerChoice{
			{QuestionID: "q-1", Op: AnswerChoiceKeep},
			{QuestionID: "q-3", Op: AnswerChoiceBlank, DraftRequested: true},
		})
	if err != nil {
		t.Fatalf("held commit err=%v, want a held result", err)
	}
	if result.Committed || len(result.Missing) != 1 || result.Missing[0] != "q-1" {
		t.Fatalf("result=%+v, want q-1 plain missing", result)
	}
	if len(result.NeedsDraft) != 1 || result.NeedsDraft[0] != "q-3" {
		t.Fatalf("result=%+v, want q-3 awaiting its draft", result)
	}
}

func TestContinueAnswersValidation(t *testing.T) {
	db := continuationFixture()
	actor := store.Actor{Kind: "owner", ID: "owner"}
	cases := [][]AnswerChoice{
		{{QuestionID: "q-1", Op: AnswerChoiceKeep, Text: "smuggled"}},
		{{QuestionID: "q-1", Op: AnswerChoiceEdit}},
		{{QuestionID: "q-1", Op: "invent"}},
		{{QuestionID: "q-1", Op: AnswerChoiceEdit, Text: "ok", DraftRequested: true}},
		{{QuestionID: "q-1", Op: AnswerChoiceBlank}, {QuestionID: "q-1", Op: AnswerChoiceBlank}},
	}
	for i, choices := range cases {
		_, err := ContinueAnswers(context.Background(), db, actor, "opp-1", "check-1", testSHA("set"), choices)
		continuation, ok := err.(*ContinuationError)
		if !ok || continuation.Reason != ContinueInvalidChoice {
			t.Errorf("case %d err=%v, want invalid_choice", i, err)
		}
	}
	if IsBlankText("  ") || !IsBlankText("") {
		t.Fatal("only the empty string is blank")
	}
	if !strings.Contains((&ContinuationError{Reason: "x", QuestionID: "q", Err: fmt.Errorf("y")}).Error(), "q") {
		t.Fatal("continuation errors name the question")
	}
}
