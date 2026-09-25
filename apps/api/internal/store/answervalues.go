package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"unicode/utf8"
)

// Exact per-question answer values (E3). The owner answers each actual
// employer question from the latest check with exact text or an explicit
// blank; there are zero Codex/LLM calls on this path. A value is one of
// unset (no row), answered (exact owner text) or blank (an explicit empty
// value distinct from unset). Saves carry an expectedAnswerVersion guard:
// 0 for unset, otherwise the row's current version; any drift fails with
// ErrConflict before anything is written.
//
// Provenance is re-resolved server-side from the latest check match on every
// save; the browser supplies only {expectedAnswerVersion, text} and can
// neither name a match nor author provenance. Only a fresh match pins: the
// newest run must still reference the latest check, its question set and the
// current answer catalog, and the question must hold a concrete suggestion
// (not none_fits). Stale or partial matches resolve as no suggestion, and
// the saved origin records exactly that. Blank input is only the empty
// string; whitespace-only text stays answered byte-identically.

// Answer value states. Unset is the absence of a row; answered and blank
// are the two stored states.
const (
	AnswerValueStateAnswered = "answered"
	AnswerValueStateBlank    = "blank"
	AnswerValueStateUnset    = "unset"
)

// Answer value origins. A answered value matching its fresh suggestion
// byte-identically is a jev_suggestion; answered text differing from a fresh
// suggestion is owner_edited; answered text without a fresh suggestion is
// owner_written; every explicit blank is a carried_blank.
const (
	AnswerValueOriginJevSuggestion = "jev_suggestion"
	AnswerValueOriginOwnerWritten  = "owner_written"
	AnswerValueOriginOwnerEdited   = "owner_edited"
	AnswerValueOriginCarriedBlank  = "carried_blank"
)

const answerValueMaxText = 20000

// AnswerValueProvenance is the saved save-time provenance of one value.
// MatchRunID and MatchChoice pin the fresh concrete suggestion the save
// resolved, and stay empty when the question had none.
type AnswerValueProvenance struct {
	Origin      string             `json:"origin"`
	MatchRunID  string             `json:"matchId,omitempty"`
	MatchChoice *AnswerMatchChoice `json:"matchChoice,omitempty"`
	EditedAt    string             `json:"editedAt"`
	EditedBy    Actor              `json:"editedBy"`
}

// QuestionAnswerValue is one saved exact answer or explicit blank.
type QuestionAnswerValue struct {
	QuestionID         string                `json:"questionId"`
	QuestionTextSHA256 string                `json:"questionTextSha256"`
	Required           string                `json:"required"`
	Version            int64                 `json:"version"`
	State              string                `json:"state"`
	Text               string                `json:"text"`
	TextSHA256         string                `json:"textSha256,omitempty"`
	Provenance         AnswerValueProvenance `json:"provenance"`
	UpdatedAt          string                `json:"updatedAt"`
}

// QuestionAnswerList is the saved-value read model for one selected role.
// Values holds answered and blank rows only; a latest-check question without
// a row is honestly unset.
type QuestionAnswerList struct {
	CheckID           string                `json:"checkId"`
	QuestionSetSHA256 string                `json:"questionSetSha256"`
	Values            []QuestionAnswerValue `json:"values"`
}

// AnswerValueSaveInput is the whole browser payload: the guarded version
// plus exact text. Empty text saves an explicit blank.
type AnswerValueSaveInput struct {
	ExpectedAnswerVersion int64  `json:"expectedAnswerVersion"`
	Text                  string `json:"text"`
}

func answerValueTextSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func validAnswerValueText(text string) bool {
	return len(text) <= answerValueMaxText && utf8.ValidString(text)
}

// answerSuggestion is one fresh concrete suggestion re-resolved for a save:
// the pinned approved text plus its match identity.
type answerSuggestion struct {
	runID  string
	choice AnswerMatchChoice
	text   string
}

// resolveAnswerSuggestionTx re-resolves the fresh concrete suggestion for one
// latest-check question. It returns nil without error when the newest run is
// absent, outdated, does not cover the question, or judged none_fits; only a
// fresh concrete suggestion pins. The caller must hold the write transaction.
func resolveAnswerSuggestionTx(ctx context.Context, tx *sql.Tx, opportunityID, checkID, questionSetSHA256, questionID string) (*answerSuggestion, error) {
	var runID, runCheckID, runQuestionSet, runCatalog string
	err := tx.QueryRowContext(ctx, `SELECT id,check_id,question_set_sha256,answer_catalog_digest
	  FROM answer_match_runs WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).
		Scan(&runID, &runCheckID, &runQuestionSet, &runCatalog)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if runCheckID != checkID || runQuestionSet != questionSetSHA256 {
		return nil, nil
	}
	catalog, err := answerCatalogDigestTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if runCatalog != catalog {
		return nil, nil
	}
	var answerID, answerSHA sql.NullString
	var answerVersion sql.NullInt64
	var noneFits int
	err = tx.QueryRowContext(ctx, `SELECT answer_id,answer_version,answer_text_sha256,none_fits
	  FROM answer_matches WHERE run_id=? AND question_id=?`, runID, questionID).
		Scan(&answerID, &answerVersion, &answerSHA, &noneFits)
	if errors.Is(err, sql.ErrNoRows) || noneFits == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var text, storedSHA string
	err = tx.QueryRowContext(ctx, `SELECT text,text_sha256 FROM saved_answer_versions
	  WHERE answer_id=? AND version=?`, answerID.String, answerVersion.Int64).
		Scan(&text, &storedSHA)
	if errors.Is(err, sql.ErrNoRows) || storedSHA != answerSHA.String {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &answerSuggestion{runID: runID,
		choice: AnswerMatchChoice{AnswerID: answerID.String,
			AnswerVersion: answerVersion.Int64, AnswerTextSHA256: answerSHA.String},
		text: text}, nil
}

// SaveAnswerValue saves the owner's exact text or an explicit blank for one
// latest-check question. The question must belong to the latest completed
// check on an unmoved basis; unknown questions fail with ErrNotFound while
// older-check questions and moved bases fail with ErrConflict. The expected
// version must match the stored row (0 when unset) or the save fails with
// ErrConflict. Every save snapshots requiredness, binds questionTextSha256,
// re-resolves the fresh suggestion server-side, and bumps the version.
func (s *Store) SaveAnswerValue(ctx context.Context, actor Actor, opportunityID, questionID string, input AnswerValueSaveInput) (QuestionAnswerValue, error) {
	if !ownerRoundActor(actor) || opportunityID == "" || questionID == "" ||
		input.ExpectedAnswerVersion < 0 || !validAnswerValueText(input.Text) {
		return QuestionAnswerValue{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return QuestionAnswerValue{}, err
	}
	var checkID, questionSet, status string
	var pinnedRevision int64
	err = tx.QueryRowContext(ctx, `SELECT id,question_set_sha256,status,opportunity_revision FROM job_checks
	  WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).
		Scan(&checkID, &questionSet, &status, &pinnedRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return QuestionAnswerValue{}, ErrNotFound
	}
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	if status != CheckStatusChecked && status != CheckStatusBlocked {
		return QuestionAnswerValue{}, ErrConflict
	}
	var currentRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
		opportunityID).Scan(&currentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return QuestionAnswerValue{}, ErrNotFound
	}
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	if pinnedRevision != currentRevision {
		return QuestionAnswerValue{}, ErrConflict
	}
	var questionCheckID, questionSHA, required string
	err = tx.QueryRowContext(ctx, `SELECT check_id,text_sha256,required FROM job_check_questions
	  WHERE id=?`, questionID).Scan(&questionCheckID, &questionSHA, &required)
	if errors.Is(err, sql.ErrNoRows) {
		return QuestionAnswerValue{}, ErrNotFound
	}
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	var questionOwner string
	err = tx.QueryRowContext(ctx, `SELECT opportunity_id FROM job_checks WHERE id=?`, questionCheckID).
		Scan(&questionOwner)
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	if questionOwner != opportunityID {
		return QuestionAnswerValue{}, ErrNotFound
	}
	if questionCheckID != checkID {
		return QuestionAnswerValue{}, ErrConflict
	}
	suggestion, err := resolveAnswerSuggestionTx(ctx, tx, opportunityID, checkID, questionSet, questionID)
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	var currentVersion int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM answer_values WHERE question_id=?`, questionID).
		Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		currentVersion = 0
	} else if err != nil {
		return QuestionAnswerValue{}, err
	}
	if input.ExpectedAnswerVersion != currentVersion {
		return QuestionAnswerValue{}, ErrConflict
	}
	state := AnswerValueStateAnswered
	textSHA := answerValueTextSHA(input.Text)
	origin := AnswerValueOriginOwnerWritten
	if input.Text == "" {
		state = AnswerValueStateBlank
		textSHA = ""
		origin = AnswerValueOriginCarriedBlank
	} else if suggestion != nil {
		origin = AnswerValueOriginOwnerEdited
		if input.Text == suggestion.text {
			origin = AnswerValueOriginJevSuggestion
		}
	}
	now := utcNow()
	next := currentVersion + 1
	var matchRun, matchID, matchSHA, storedSHA any
	var matchVersion any
	if suggestion != nil {
		matchRun, matchID = suggestion.runID, suggestion.choice.AnswerID
		matchVersion, matchSHA = suggestion.choice.AnswerVersion, suggestion.choice.AnswerTextSHA256
	}
	if textSHA != "" {
		storedSHA = textSHA
	}
	if currentVersion == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO answer_values
		  (opportunity_id,check_id,question_id,question_text_sha256,required,version,state,text,
		   text_sha256,origin,match_run_id,match_answer_id,match_answer_version,
		   match_answer_text_sha256,edited_at,edited_by_kind,edited_by_id,updated_at)
		  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, opportunityID, checkID, questionID,
			questionSHA, required, next, state, input.Text, storedSHA, origin,
			matchRun, matchID, matchVersion, matchSHA, now, actor.Kind, actor.ID, now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE answer_values SET check_id=?,question_text_sha256=?,
		  required=?,version=?,state=?,text=?,text_sha256=?,origin=?,match_run_id=?,
		  match_answer_id=?,match_answer_version=?,match_answer_text_sha256=?,
		  edited_at=?,edited_by_kind=?,edited_by_id=?,updated_at=? WHERE question_id=?`,
			checkID, questionSHA, required, next, state, input.Text, storedSHA, origin,
			matchRun, matchID, matchVersion, matchSHA, now, actor.Kind, actor.ID, now, questionID)
	}
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	auditID, err := randomID()
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "answer.value_save",
		"answer_value", questionID, currentVersion, next, now)
	if err != nil {
		return QuestionAnswerValue{}, err
	}
	if err := tx.Commit(); err != nil {
		return QuestionAnswerValue{}, err
	}
	value := QuestionAnswerValue{QuestionID: questionID, QuestionTextSHA256: questionSHA,
		Required: required, Version: next, State: state, Text: input.Text, TextSHA256: textSHA,
		Provenance: AnswerValueProvenance{Origin: origin, EditedAt: now,
			EditedBy: Actor{Kind: actor.Kind, ID: actor.ID}},
		UpdatedAt: now}
	if suggestion != nil {
		value.Provenance.MatchRunID = suggestion.runID
		choice := suggestion.choice
		value.Provenance.MatchChoice = &choice
	}
	return value, nil
}

// CurrentQuestionAnswers reads the saved values for one selected role's
// latest check in question order. The read is side-effect-free: it pins no
// matches and re-resolves nothing, returning stored provenance verbatim.
// Latest-check questions without a row are unset and absent from Values.
func (s *Store) CurrentQuestionAnswers(ctx context.Context, opportunityID string) (QuestionAnswerList, error) {
	if opportunityID == "" {
		return QuestionAnswerList{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return QuestionAnswerList{}, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return QuestionAnswerList{}, err
	}
	var checkID, questionSet string
	err = tx.QueryRowContext(ctx, `SELECT id,question_set_sha256 FROM job_checks
	  WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).
		Scan(&checkID, &questionSet)
	if errors.Is(err, sql.ErrNoRows) {
		return QuestionAnswerList{}, ErrNotFound
	}
	if err != nil {
		return QuestionAnswerList{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT q.id,q.text_sha256,
	  v.required,v.version,v.state,v.text,v.text_sha256,v.origin,
	  v.match_run_id,v.match_answer_id,v.match_answer_version,v.match_answer_text_sha256,
	  v.edited_at,v.edited_by_kind,v.edited_by_id,v.updated_at,v.question_text_sha256
	  FROM job_check_questions q LEFT JOIN answer_values v ON v.question_id=q.id
	  WHERE q.check_id=? ORDER BY q.ordinal`, checkID)
	if err != nil {
		return QuestionAnswerList{}, err
	}
	defer rows.Close()
	list := QuestionAnswerList{CheckID: checkID, QuestionSetSHA256: questionSet, Values: []QuestionAnswerValue{}}
	for rows.Next() {
		var questionID, questionSHA string
		var version sql.NullInt64
		var required, state, text, textSHA, origin, boundSHA sql.NullString
		var matchRun, matchID, matchSHA sql.NullString
		var matchVersion sql.NullInt64
		var editedAt, editedKind, editedID, updatedAt sql.NullString
		if err := rows.Scan(&questionID, &questionSHA,
			&required, &version, &state, &text, &textSHA, &origin,
			&matchRun, &matchID, &matchVersion, &matchSHA,
			&editedAt, &editedKind, &editedID, &updatedAt, &boundSHA); err != nil {
			return QuestionAnswerList{}, err
		}
		if !version.Valid {
			continue
		}
		if !boundSHA.Valid || boundSHA.String != questionSHA {
			return QuestionAnswerList{}, ErrInvalid
		}
		value := QuestionAnswerValue{QuestionID: questionID, QuestionTextSHA256: questionSHA,
			Required: required.String, Version: version.Int64, State: state.String, Text: text.String,
			TextSHA256: textSHA.String,
			Provenance: AnswerValueProvenance{Origin: origin.String, EditedAt: editedAt.String,
				EditedBy: Actor{Kind: editedKind.String, ID: editedID.String}},
			UpdatedAt: updatedAt.String}
		if matchRun.Valid {
			value.Provenance.MatchRunID = matchRun.String
		}
		if matchID.Valid {
			value.Provenance.MatchChoice = &AnswerMatchChoice{AnswerID: matchID.String,
				AnswerVersion: matchVersion.Int64, AnswerTextSHA256: matchSHA.String}
		}
		list.Values = append(list.Values, value)
	}
	if err := rows.Err(); err != nil {
		return QuestionAnswerList{}, err
	}
	return list, tx.Commit()
}
