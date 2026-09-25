package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// answerValueMatchedFixture builds the E2 matched fixture, then judges one
// batch covering both questions: the first takes the remote suggestion, the
// second judges none_fits. It returns the fixture plus the offered remote
// candidate for exact-text saves.
func answerValueMatchedFixture(t *testing.T, key string) (answerMatchFixture, jev.AnswerMatchCandidate) {
	t.Helper()
	f := setupAnswerMatch(t, key)
	catalog := answerMatchCatalog(t, f.store)
	remote, start := answerMatchOffered(f.answers[0]), answerMatchOffered(f.answers[1])
	questions := f.check.Questions
	input := answerMatchBatch(f.check, catalog, questions, map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote, start},
		questions[1].ID: {remote, start},
	})
	view, created := runAnswerMatchBatch(t, f, key+"-match", key+"-match-attempt", input,
		map[string]string{questions[0].ID: remote.AnswerID, questions[1].ID: jev.AnswerMatchNoneFits})
	if !created || view.Status != AnswerMatchStatusMatched {
		t.Fatalf("match fixture: created=%v %+v", created, view)
	}
	return f, remote
}

func TestAnswerValueSaveSuggestedTextExact(t *testing.T) {
	f, remote := answerValueMatchedFixture(t, "exact")
	ctx := context.Background()
	question := f.check.Questions[0]
	suggested := f.answers[0].Versions[0].Text
	value, err := f.store.SaveAnswerValue(ctx, ownerActor(), f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: suggested})
	if err != nil {
		t.Fatal(err)
	}
	if value.QuestionID != question.ID || value.QuestionTextSHA256 != question.TextSHA256 ||
		value.Required != question.Required || value.Version != 1 ||
		value.State != AnswerValueStateAnswered || value.Text != suggested ||
		value.TextSHA256 != answerValueTextSHA(suggested) {
		t.Fatalf("saved value: %+v", value)
	}
	if value.Provenance.Origin != AnswerValueOriginJevSuggestion ||
		value.Provenance.MatchChoice == nil ||
		value.Provenance.MatchChoice.AnswerID != remote.AnswerID ||
		value.Provenance.MatchChoice.AnswerVersion != remote.AnswerVersion ||
		value.Provenance.MatchChoice.AnswerTextSHA256 != remote.TextSHA256 ||
		value.Provenance.MatchRunID == "" || value.Provenance.EditedAt == "" ||
		value.Provenance.EditedBy != ownerActor() || value.UpdatedAt == "" {
		t.Fatalf("saved provenance: %+v", value.Provenance)
	}
	read, err := f.store.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.CheckID != f.check.ID || read.QuestionSetSHA256 != f.check.QuestionSetSHA256 ||
		len(read.Values) != 1 || read.Values[0].QuestionID != question.ID ||
		read.Values[0].Text != suggested ||
		read.Values[0].Provenance.Origin != AnswerValueOriginJevSuggestion {
		t.Fatalf("re-read: %+v", read)
	}
}

func TestAnswerValueSaveEditedAndVersionGuard(t *testing.T) {
	f, _ := answerValueMatchedFixture(t, "edited")
	ctx := context.Background()
	owner := ownerActor()
	question := f.check.Questions[0]
	suggested := f.answers[0].Versions[0].Text
	first, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: suggested})
	if err != nil || first.Version != 1 {
		t.Fatalf("first save: %+v %v", first, err)
	}
	edited := suggested + " Also available for hybrid weeks."
	second, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: edited})
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 || second.State != AnswerValueStateAnswered || second.Text != edited ||
		second.Provenance.Origin != AnswerValueOriginOwnerEdited ||
		second.Provenance.MatchChoice == nil || second.Provenance.MatchRunID == "" {
		t.Fatalf("edited save keeps its resolved suggestion: %+v", second)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: edited}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: edited}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused zero version: %v", err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 9, Text: edited}); !errors.Is(err, ErrConflict) {
		t.Fatalf("future version: %v", err)
	}
}

func TestAnswerValueBlankDistinctFromUnset(t *testing.T) {
	f, _ := answerValueMatchedFixture(t, "blank")
	ctx := context.Background()
	owner := ownerActor()
	question := f.check.Questions[0]
	empty, err := f.store.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil || len(empty.Values) != 0 {
		t.Fatalf("unset questions stay absent: %+v %v", empty, err)
	}
	blank, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: ""})
	if err != nil {
		t.Fatal(err)
	}
	if blank.Version != 1 || blank.State != AnswerValueStateBlank || blank.Text != "" ||
		blank.TextSHA256 != "" || blank.Provenance.Origin != AnswerValueOriginCarriedBlank {
		t.Fatalf("explicit blank: %+v", blank)
	}
	if blank.Provenance.MatchChoice == nil || blank.Provenance.MatchRunID == "" {
		t.Fatalf("blank keeps its resolved suggestion pin: %+v", blank.Provenance)
	}
	read, err := f.store.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil || len(read.Values) != 1 || read.Values[0].State != AnswerValueStateBlank {
		t.Fatalf("blank re-read: %+v %v", read, err)
	}
	second, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: "Now answered."})
	if err != nil || second.Version != 2 || second.State != AnswerValueStateAnswered {
		t.Fatalf("blank to answered: %+v %v", second, err)
	}
	third, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 2, Text: ""})
	if err != nil || third.Version != 3 || third.State != AnswerValueStateBlank {
		t.Fatalf("answered back to blank: %+v %v", third, err)
	}
	pad, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 3, Text: "   "})
	if err != nil || pad.State != AnswerValueStateAnswered || pad.Text != "   " {
		t.Fatalf("whitespace-only text stays answered byte-identically: %+v %v", pad, err)
	}
}

func TestAnswerValueOwnerWrittenWithoutSuggestion(t *testing.T) {
	f, _ := answerValueMatchedFixture(t, "written")
	ctx := context.Background()
	owner := ownerActor()
	noneFits := f.check.Questions[1]
	text := "Hybrid works for me: 2–3 days on-site, Können wir starten? ✅\nSecond line."
	value, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, noneFits.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if value.State != AnswerValueStateAnswered || value.Text != text ||
		value.Provenance.Origin != AnswerValueOriginOwnerWritten ||
		value.Provenance.MatchChoice != nil || value.Provenance.MatchRunID != "" {
		t.Fatalf("none_fits save pins nothing: %+v", value)
	}
	unmatched := setupAnswerMatch(t, "writtenbare")
	bare := unmatched.check.Questions[0]
	bareValue, err := unmatched.store.SaveAnswerValue(ctx, owner, unmatched.opportunity.ID, bare.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: "Written with no match run at all."})
	if err != nil {
		t.Fatal(err)
	}
	if bareValue.Provenance.Origin != AnswerValueOriginOwnerWritten ||
		bareValue.Provenance.MatchChoice != nil {
		t.Fatalf("no-run save: %+v", bareValue)
	}
}

func TestAnswerValueStaleMatchNeverPins(t *testing.T) {
	f, _ := answerValueMatchedFixture(t, "stalematch")
	ctx := context.Background()
	owner := ownerActor()
	question := f.check.Questions[0]
	suggested := f.answers[0].Versions[0].Text
	updated, _, err := f.store.ApproveSavedAnswerVersion(ctx, owner, f.answers[0].ID,
		SavedAnswerVersionCreateInput{RequestKey: "stalematch-v2", ExpectedVersion: 1,
			Text: "I work remotely from Example City, full time."})
	if err != nil {
		t.Fatal(err)
	}
	_ = updated
	value, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: suggested})
	if err != nil {
		t.Fatal(err)
	}
	if value.Provenance.Origin != AnswerValueOriginOwnerWritten ||
		value.Provenance.MatchChoice != nil || value.Provenance.MatchRunID != "" {
		t.Fatalf("catalog drift must not pin: %+v", value.Provenance)
	}
	if _, err := f.store.db.ExecContext(ctx, `UPDATE opportunities SET revision=revision+1 WHERE id=?`,
		f.opportunity.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: "After basis moved."}); !errors.Is(err, ErrConflict) {
		t.Fatalf("moved basis: %v", err)
	}
}

func TestAnswerValueBlockedCheckAllowedAtStore(t *testing.T) {
	f, _ := answerValueMatchedFixture(t, "blockedstore")
	ctx := context.Background()
	if _, err := f.store.db.ExecContext(ctx, `UPDATE job_checks SET status='blocked' WHERE id=?`,
		f.check.ID); err != nil {
		t.Fatal(err)
	}
	value, err := f.store.SaveAnswerValue(ctx, ownerActor(), f.opportunity.ID,
		f.check.Questions[1].ID, AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: "Blocked but writable."})
	if err != nil {
		t.Fatal(err)
	}
	if value.State != AnswerValueStateAnswered || value.Version != 1 {
		t.Fatalf("blocked save: %+v", value)
	}
}

func TestAnswerValueReadBeforeAnySave(t *testing.T) {
	f := setupAnswerMatch(t, "readempty")
	read, err := f.store.CurrentQuestionAnswers(context.Background(), f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.CheckID != f.check.ID || read.QuestionSetSHA256 != f.check.QuestionSetSHA256 ||
		read.Values == nil || len(read.Values) != 0 {
		t.Fatalf("empty read: %+v", read)
	}
}

func TestAnswerValueReadReturnsQuestionOrder(t *testing.T) {
	f, _ := answerValueMatchedFixture(t, "order")
	ctx := context.Background()
	owner := ownerActor()
	questions := f.check.Questions
	// Save in reverse ordinal order; the read must still follow the check.
	for i := len(questions) - 1; i >= 0; i-- {
		if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, questions[i].ID,
			AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: "Answer."}); err != nil {
			t.Fatal(err)
		}
	}
	read, err := f.store.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil || len(read.Values) != len(questions) {
		t.Fatalf("ordered read: %+v %v", read, err)
	}
	for i, question := range questions {
		if read.Values[i].QuestionID != question.ID ||
			read.Values[i].QuestionTextSHA256 != question.TextSHA256 {
			t.Fatalf("position %d: %+v", i, read.Values[i])
		}
	}
}

func TestAnswerValueSelectionAndIdentity(t *testing.T) {
	f, _ := answerValueMatchedFixture(t, "identity")
	ctx := context.Background()
	owner := ownerActor()
	question := f.check.Questions[0]
	input := AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: "Hello."}

	unselected, _, err := f.store.CreateOpportunity(ctx, owner, fixtureOpportunity(createFixtureCompany(t, f.store).ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, unselected.ID, question.ID, input); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected save: %v", err)
	}
	if _, err := f.store.CurrentQuestionAnswers(ctx, unselected.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected read: %v", err)
	}
	if _, err := f.store.CurrentQuestionAnswers(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing read: %v", err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, "missing-question", input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown question: %v", err)
	}

	// Same-store cross-role save: the other question exists, but under
	// another role, so the save must not leak its existence. A pending
	// check plus one directly inserted question row is enough to reach the
	// owner-mismatch branch; no second round is started.
	company := createFixtureCompany(t, f.store)
	otherOpportunity := selectFixtureOpportunity(t, f.store, company.ID, "identity-other-select")
	otherStarted, _, err := f.store.StartJobCheck(ctx, owner, otherOpportunity.ID,
		CheckStartInput{RequestKey: "identity-other-check", ExpectedOpportunityRevision: otherOpportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	otherCapture := insertCheckCapture(t, f.store, "https://harbour.example/jobs/other")
	otherText := "What is your notice period?"
	otherSum := sha256.Sum256([]byte(otherText))
	otherSHA := hex.EncodeToString(otherSum[:])
	if _, err := f.store.db.ExecContext(ctx, `INSERT INTO job_check_questions
	  (id,check_id,ordinal,text,required,kind,capture_id,span_start,span_end,source_excerpt,text_sha256)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?)`, "identity-other-q", otherStarted.ID, 0, otherText,
		CheckRequired, CheckQuestionFreeText, otherCapture.ID, 10, 40, otherText, otherSHA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID,
		"identity-other-q", input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-role question must not leak: %v", err)
	}

	if _, err := f.store.SaveAnswerValue(ctx, Actor{Kind: "agent", ID: "codex"}, f.opportunity.ID, question.ID, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-owner actor: %v", err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, "", question.ID, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty opportunity: %v", err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, "", input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty question: %v", err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: -1, Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative version: %v", err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: strings.Repeat("x", 20001)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize text: %v", err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, question.ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: "bad\xfftext"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid utf8: %v", err)
	}
	if _, err := f.store.CurrentQuestionAnswers(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty read: %v", err)
	}
}

func TestAnswerValueOlderCheckQuestionConflicts(t *testing.T) {
	f, _ := answerValueMatchedFixture(t, "oldcheck")
	ctx := context.Background()
	owner := ownerActor()
	if _, err := f.store.db.ExecContext(ctx, `UPDATE opportunities SET revision=revision+1 WHERE id=?`,
		f.opportunity.ID); err != nil {
		t.Fatal(err)
	}
	workflow, err := f.store.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	recheck, created, err := f.store.StartJobCheck(ctx, owner, f.opportunity.ID,
		CheckStartInput{RequestKey: "oldcheck-recheck",
			ExpectedOpportunityRevision: f.opportunity.Revision + 1, ExpectedWorkflowRevision: workflow.Revision})
	if err != nil || !created {
		t.Fatalf("recheck: %+v %v", recheck, err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, f.check.Questions[0].ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: "Late answer."}); !errors.Is(err, ErrConflict) {
		t.Fatalf("older-check question: %v", err)
	}
}
