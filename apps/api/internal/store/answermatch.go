package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// Answer matching persistence (E2). The owner matches a current check's actual
// employer questions against approved saved answers through one Jev Choice
// call per batch; this writer persists per-question outcomes with provenance.
// There are zero Codex/LLM calls on this path: the caller supplies a captured
// Jev exchange recorded through the jevservice recording evaluator, and this
// file re-verifies the exchange, the pinned check, the pinned answer catalog
// and every offered candidate against the database before writing. Reads never
// re-invoke Jev. Unusable batches persist nothing, so failed questions stay
// honestly unmatched until a later batch covers them.

// Answer match view statuses. A run pinned to an older check, an older
// question set or an older answer catalog reads as outdated with its saved
// matches intact; coverage of the current questions decides the rest.
const (
	AnswerMatchStatusMatched   = "matched"
	AnswerMatchStatusUnmatched = "unmatched"
	AnswerMatchStatusPartial   = "partial"
	AnswerMatchStatusOutdated  = "outdated"
)

// AnswerMatchChoice is one persisted per-question choice: either a pinned
// approved answer version or an explicit none_fits.
type AnswerMatchChoice struct {
	AnswerID         string `json:"answerId,omitempty"`
	AnswerVersion    int64  `json:"answerVersion,omitempty"`
	AnswerTextSHA256 string `json:"textSha256,omitempty"`
	NoneFits         bool   `json:"noneFits"`
}

// AnswerMatch is one persisted per-question outcome with its provenance.
// JevAttemptID and Model are empty for deterministic selections, which no Jev
// question judged. Confidence is audit-only and never gates display.
type AnswerMatch struct {
	QuestionID         string            `json:"questionId"`
	QuestionTextSHA256 string            `json:"questionTextSha256"`
	Choice             AnswerMatchChoice `json:"choice"`
	CandidateSetHash   string            `json:"candidateSetHash"`
	Deterministic      bool              `json:"deterministic"`
	Confidence         float64           `json:"confidence"`
	JevAttemptID       string            `json:"jevAttemptId"`
	Model              string            `json:"model,omitempty"`
	MatchedAt          string            `json:"matchedAt"`
}

// AnswerMatchView is the saved-match read model for one selected role.
type AnswerMatchView struct {
	Status              string        `json:"status"`
	CheckID             string        `json:"checkId"`
	QuestionSetSHA256   string        `json:"questionSetSha256"`
	AnswerCatalogDigest string        `json:"answerCatalogDigest"`
	CatalogMatchedAt    string        `json:"catalogMatchedAt"`
	Matches             []AnswerMatch `json:"matches"`
}

// AnswerCatalogDigest hashes the current approved-answer catalog: one
// id:version:text entry per answer ordered by id. Matching pins this digest;
// any later approval re-dates saved runs to outdated.
func (s *Store) AnswerCatalogDigest(ctx context.Context) (string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	digest, err := answerCatalogDigestTx(ctx, tx)
	if err != nil {
		return "", err
	}
	return digest, tx.Commit()
}

func answerCatalogDigestTx(ctx context.Context, tx *sql.Tx) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.current_version,v.text_sha256
	  FROM saved_answers a JOIN saved_answer_versions v
	  ON v.answer_id=a.id AND v.version=a.current_version ORDER BY a.id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	entries := []string{}
	for rows.Next() {
		var id, textSHA string
		var version int64
		if err := rows.Scan(&id, &version, &textSHA); err != nil {
			return "", err
		}
		entries = append(entries, id+":"+strconv.FormatInt(version, 10)+":"+textSHA)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func answerMatchRunDigest(requestKey, checkID, questionSetSHA256, answerCatalogDigest string) string {
	return ownerDigest(struct {
		RequestKey          string
		CheckID             string
		QuestionSetSHA256   string
		AnswerCatalogDigest string
	}{requestKey, checkID, questionSetSHA256, answerCatalogDigest})
}

// SaveAnswerMatch persists one judged batch for the current check. The batch
// input must pin the current check id, its question set and the current answer
// catalog; any drift fails with ErrConflict before anything is written. Every
// offered candidate is re-verified against its immutable version row, and
// every judged selection is re-derived from the captured Jev exchange, so a
// caller cannot author a choice or substitute different questions, candidates
// or pins. Batches sharing one request key merge per question; replaying an
// identical batch is a no-op, while the same key with different pins fails
// with ErrRoundIdempotencyConflict and the same question with a different
// outcome fails with ErrRoundIdempotencyConflict. Created reports whether this
// call inserted the run or any new match row.
func (s *Store) SaveAnswerMatch(ctx context.Context, actor Actor, roundID, jevAttemptID, requestKey string,
	input jev.AnswerMatchInput, result jev.AnswerMatchResult) (AnswerMatchView, bool, error) {
	if !ownerRoundActor(actor) || roundID == "" || !ownerRequestKey(requestKey) {
		return AnswerMatchView{}, false, ErrInvalid
	}
	digest, err := jev.AnswerMatchInputDigest(input)
	if err != nil || digest != result.InputSHA256 ||
		result.AnswerCatalogDigest != input.AnswerCatalogDigest || len(result.Selections) == 0 {
		return AnswerMatchView{}, false, ErrInvalid
	}
	if len(result.Selections) != len(input.Questions) {
		return AnswerMatchView{}, false, ErrInvalid
	}
	selectionByQuestion := make(map[string]jev.AnswerMatchSelection, len(result.Selections))
	for _, selection := range result.Selections {
		if _, dup := selectionByQuestion[selection.QuestionID]; dup || selection.QuestionID == "" {
			return AnswerMatchView{}, false, ErrInvalid
		}
		selectionByQuestion[selection.QuestionID] = selection
	}
	for _, question := range input.Questions {
		if _, ok := selectionByQuestion[question.QuestionID]; !ok {
			return AnswerMatchView{}, false, ErrInvalid
		}
	}
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return AnswerMatchView{}, false, err
	}
	defer tx.Rollback()
	if err := requireRoundState(round, RoundRunning); err != nil {
		return AnswerMatchView{}, false, err
	}
	if !scopeAllowsActor(round.Scope, actor) {
		return AnswerMatchView{}, false, ErrFenced
	}
	var opportunityID, status, storedQuestionSet string
	var pinnedRevision int64
	err = tx.QueryRowContext(ctx, `SELECT opportunity_id,status,question_set_sha256,opportunity_revision
	  FROM job_checks WHERE id=?`, input.CheckID).Scan(&opportunityID, &status, &storedQuestionSet, &pinnedRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return AnswerMatchView{}, false, ErrNotFound
	}
	if err != nil {
		return AnswerMatchView{}, false, err
	}
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return AnswerMatchView{}, false, err
	}
	if !scopeAllows(round.Scope, RoundJevRequest, "opportunity:"+opportunityID) {
		return AnswerMatchView{}, false, ErrFenced
	}
	var latestCheckID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM job_checks WHERE opportunity_id=?
	  ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).Scan(&latestCheckID)
	if err != nil {
		return AnswerMatchView{}, false, err
	}
	var currentRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
		opportunityID).Scan(&currentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return AnswerMatchView{}, false, ErrNotFound
	}
	if err != nil {
		return AnswerMatchView{}, false, err
	}
	if latestCheckID != input.CheckID || storedQuestionSet != input.QuestionSetSHA256 ||
		pinnedRevision != currentRevision ||
		(status != CheckStatusChecked && status != CheckStatusBlocked) {
		return AnswerMatchView{}, false, ErrConflict
	}
	catalog, err := answerCatalogDigestTx(ctx, tx)
	if err != nil {
		return AnswerMatchView{}, false, err
	}
	if catalog != input.AnswerCatalogDigest {
		return AnswerMatchView{}, false, ErrConflict
	}
	questionSHA := map[string]string{}
	questionRows, err := tx.QueryContext(ctx, `SELECT id,text_sha256 FROM job_check_questions WHERE check_id=?`, input.CheckID)
	if err != nil {
		return AnswerMatchView{}, false, err
	}
	for questionRows.Next() {
		var id, sha string
		if err := questionRows.Scan(&id, &sha); err != nil {
			questionRows.Close()
			return AnswerMatchView{}, false, err
		}
		questionSHA[id] = sha
	}
	if err := questionRows.Err(); err != nil {
		questionRows.Close()
		return AnswerMatchView{}, false, err
	}
	questionRows.Close()
	for _, question := range input.Questions {
		stored, ok := questionSHA[question.QuestionID]
		if !ok || stored != question.TextSHA256 {
			return AnswerMatchView{}, false, ErrInvalid
		}
		selection := selectionByQuestion[question.QuestionID]
		if selection.QuestionTextSHA256 != stored {
			return AnswerMatchView{}, false, ErrInvalid
		}
		hash, herr := jev.AnswerCandidateSetHash(question.Candidates)
		if herr != nil || hash != selection.CandidateSetHash {
			return AnswerMatchView{}, false, ErrInvalid
		}
		if selection.Deterministic != (len(question.Candidates) == 0) {
			return AnswerMatchView{}, false, ErrInvalid
		}
		if selection.Confidence < 0 || selection.Confidence > 1 ||
			(selection.Deterministic && selection.Confidence != 0) {
			return AnswerMatchView{}, false, ErrInvalid
		}
		for _, candidate := range question.Candidates {
			if err := verifyAnswerMatchOfferedTx(ctx, tx, candidate); err != nil {
				return AnswerMatchView{}, false, err
			}
		}
		if selection.NoneFits {
			if selection.AnswerID != "" || selection.AnswerVersion != 0 || selection.AnswerTextSHA256 != "" {
				return AnswerMatchView{}, false, ErrInvalid
			}
		} else {
			offered := false
			for _, candidate := range question.Candidates {
				if candidate.AnswerID == selection.AnswerID &&
					candidate.AnswerVersion == selection.AnswerVersion &&
					candidate.TextSHA256 == selection.AnswerTextSHA256 {
					offered = true
					break
				}
			}
			if !offered {
				return AnswerMatchView{}, false, ErrInvalid
			}
		}
	}
	judgedCount := 0
	for _, selection := range result.Selections {
		if !selection.Deterministic {
			judgedCount++
		}
	}
	if judgedCount == 0 {
		if jevAttemptID != "" {
			return AnswerMatchView{}, false, ErrInvalid
		}
	} else {
		if jevAttemptID == "" {
			return AnswerMatchView{}, false, ErrInvalid
		}
		var purpose, attemptStatus, attemptRound, operation, resource string
		var requestedModel sql.NullString
		var attemptState RoundAttemptState
		var generation, profileVersion int64
		var logical, raw []byte
		err = tx.QueryRowContext(ctx, `SELECT j.purpose,j.status,j.round_id,j.logical_request_json,
		  j.raw_response_bytes,j.requested_model,a.operation,a.resource_id,a.state,a.generation,j.profile_version
		  FROM jev_attempts j JOIN round_attempts a ON a.id=j.round_attempt_id WHERE j.id=?`, jevAttemptID).
			Scan(&purpose, &attemptStatus, &attemptRound, &logical, &raw, &requestedModel,
				&operation, &resource, &attemptState, &generation, &profileVersion)
		if errors.Is(err, sql.ErrNoRows) {
			return AnswerMatchView{}, false, ErrNotFound
		}
		if err != nil {
			return AnswerMatchView{}, false, err
		}
		if purpose != jev.AnswerMatchPurpose || attemptStatus != "succeeded" || attemptRound != roundID ||
			operation != RoundJevRequest || resource != "opportunity:"+opportunityID ||
			(attemptState != AttemptSucceeded && attemptState != AttemptObservedSuccess) ||
			generation < 1 || generation > round.Generation || profileVersion != round.ProfileVersion {
			return AnswerMatchView{}, false, ErrFenced
		}
		if _, err := jev.ValidateCapturedAnswerMatch(input, result, logical, raw, requestedModel.String); err != nil {
			return AnswerMatchView{}, false, ErrFenced
		}
	}
	runDigest := answerMatchRunDigest(requestKey, input.CheckID, input.QuestionSetSHA256, input.AnswerCatalogDigest)
	var runID, oldDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM answer_match_runs
	  WHERE opportunity_id=? AND check_id=? AND request_key=?`, opportunityID, input.CheckID, requestKey).
		Scan(&runID, &oldDigest)
	runCreated := false
	if err == nil {
		if oldDigest != runDigest {
			return AnswerMatchView{}, false, ErrRoundIdempotencyConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		runID, err = randomID()
		if err != nil {
			return AnswerMatchView{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO answer_match_runs
		  (id,opportunity_id,check_id,request_key,request_sha256,question_set_sha256,
		   answer_catalog_digest,actor_kind,actor_id,created_at)
		  VALUES (?,?,?,?,?,?,?,?,?,?)`, runID, opportunityID, input.CheckID, requestKey, runDigest,
			input.QuestionSetSHA256, input.AnswerCatalogDigest, actor.Kind, actor.ID, utcNow()); err != nil {
			return AnswerMatchView{}, false, err
		}
		runCreated = true
	} else {
		return AnswerMatchView{}, false, err
	}
	matchedAt := utcNow()
	newRows := 0
	for _, selection := range result.Selections {
		var answerID, answerVersion, answerSHA any
		var noneFits, deterministic int
		if selection.NoneFits {
			noneFits = 1
		} else {
			answerID, answerVersion, answerSHA = selection.AnswerID, selection.AnswerVersion, selection.AnswerTextSHA256
		}
		var attempt any
		if selection.Deterministic {
			deterministic = 1
		} else {
			attempt = jevAttemptID
		}
		outcome, err := tx.ExecContext(ctx, `INSERT INTO answer_matches
		  (run_id,question_id,question_text_sha256,candidate_set_hash,answer_id,answer_version,
		   answer_text_sha256,none_fits,deterministic,confidence,jev_attempt_id,matched_at)
		  VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,question_id) DO NOTHING`,
			runID, selection.QuestionID, selection.QuestionTextSHA256, selection.CandidateSetHash,
			answerID, answerVersion, answerSHA, noneFits, deterministic, selection.Confidence, attempt, matchedAt)
		if err != nil {
			return AnswerMatchView{}, false, err
		}
		affected, err := outcome.RowsAffected()
		if err != nil {
			return AnswerMatchView{}, false, err
		}
		if affected == 1 {
			newRows++
			continue
		}
		existing, err := scanAnswerMatchTx(ctx, tx, runID, selection.QuestionID)
		if err != nil {
			return AnswerMatchView{}, false, err
		}
		if !answerMatchOutcomeEqual(existing, selection, jevAttemptID) {
			return AnswerMatchView{}, false, ErrRoundIdempotencyConflict
		}
	}
	if err := writeRoundAudit(ctx, tx, actor, "answer.match_save", runID); err != nil {
		return AnswerMatchView{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return AnswerMatchView{}, false, err
	}
	view, err := s.CurrentAnswerMatch(ctx, opportunityID)
	if err != nil {
		return AnswerMatchView{}, false, err
	}
	return view, runCreated || newRows > 0, nil
}

// verifyAnswerMatchOfferedTx re-verifies one offered candidate against its
// immutable version row: the version must exist with the exact text hash, the
// scope tags and context note must match the answer, and the excerpt must be a
// substring of the approved text. Failures are caller errors, never 404s.
func verifyAnswerMatchOfferedTx(ctx context.Context, tx *sql.Tx, candidate jev.AnswerMatchCandidate) error {
	if candidate.AnswerID == jev.AnswerMatchNoneFits {
		return ErrInvalid
	}
	var text, storedSHA, contextNote string
	err := tx.QueryRowContext(ctx, `SELECT v.text,v.text_sha256,a.context_note FROM saved_answer_versions v
	  JOIN saved_answers a ON a.id=v.answer_id
	  WHERE v.answer_id=? AND v.version=?`, candidate.AnswerID, candidate.AnswerVersion).
		Scan(&text, &storedSHA, &contextNote)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	if storedSHA != candidate.TextSHA256 || contextNote != candidate.ContextNote ||
		candidate.Excerpt == "" || !strings.Contains(text, candidate.Excerpt) {
		return ErrInvalid
	}
	tagRows, err := tx.QueryContext(ctx, `SELECT tag FROM saved_answer_scope_tags
	  WHERE answer_id=? ORDER BY ordinal`, candidate.AnswerID)
	if err != nil {
		return err
	}
	defer tagRows.Close()
	stored := []string{}
	for tagRows.Next() {
		var tag string
		if err := tagRows.Scan(&tag); err != nil {
			return err
		}
		stored = append(stored, tag)
	}
	if err := tagRows.Err(); err != nil {
		return err
	}
	if len(stored) != len(candidate.ScopeTags) {
		return ErrInvalid
	}
	for i, tag := range stored {
		if tag != candidate.ScopeTags[i] {
			return ErrInvalid
		}
	}
	return nil
}

func scanAnswerMatchTx(ctx context.Context, tx *sql.Tx, runID, questionID string) (AnswerMatch, error) {
	var match AnswerMatch
	var answerID, answerSHA sql.NullString
	var answerVersion sql.NullInt64
	var noneFits, deterministic int
	var attempt sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT question_id,question_text_sha256,candidate_set_hash,
	  answer_id,answer_version,answer_text_sha256,none_fits,deterministic,confidence,
	  jev_attempt_id,matched_at FROM answer_matches WHERE run_id=? AND question_id=?`,
		runID, questionID).Scan(&match.QuestionID, &match.QuestionTextSHA256, &match.CandidateSetHash,
		&answerID, &answerVersion, &answerSHA, &noneFits, &deterministic,
		&match.Confidence, &attempt, &match.MatchedAt)
	if err != nil {
		return AnswerMatch{}, err
	}
	match.Choice = AnswerMatchChoice{AnswerID: answerID.String, AnswerTextSHA256: answerSHA.String,
		NoneFits: noneFits == 1}
	if answerVersion.Valid {
		match.Choice.AnswerVersion = answerVersion.Int64
	}
	match.Deterministic = deterministic == 1
	match.JevAttemptID = attempt.String
	return match, nil
}

func answerMatchOutcomeEqual(existing AnswerMatch, selection jev.AnswerMatchSelection, jevAttemptID string) bool {
	if existing.QuestionTextSHA256 != selection.QuestionTextSHA256 ||
		existing.CandidateSetHash != selection.CandidateSetHash ||
		existing.Choice.NoneFits != selection.NoneFits ||
		existing.Deterministic != selection.Deterministic ||
		existing.Confidence != selection.Confidence {
		return false
	}
	if selection.NoneFits {
		return existing.Choice.AnswerID == "" && existing.Choice.AnswerVersion == 0 && existing.Choice.AnswerTextSHA256 == ""
	}
	if existing.Choice.AnswerID != selection.AnswerID ||
		existing.Choice.AnswerVersion != selection.AnswerVersion ||
		existing.Choice.AnswerTextSHA256 != selection.AnswerTextSHA256 {
		return false
	}
	if selection.Deterministic {
		return existing.JevAttemptID == ""
	}
	return existing.JevAttemptID == jevAttemptID
}

// CurrentAnswerMatch reads the saved-match view for one selected role. It
// reports the newest run: outdated when its pins no longer match the current
// check, question set or answer catalog; otherwise matched, unmatched or
// partial by coverage. The read is side-effect-free and never re-invokes Jev.
func (s *Store) CurrentAnswerMatch(ctx context.Context, opportunityID string) (AnswerMatchView, error) {
	if opportunityID == "" {
		return AnswerMatchView{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AnswerMatchView{}, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return AnswerMatchView{}, err
	}
	var latestCheckID, latestQuestionSet, latestStatus string
	var latestPinned int64
	err = tx.QueryRowContext(ctx, `SELECT id,question_set_sha256,status,opportunity_revision FROM job_checks
	  WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).
		Scan(&latestCheckID, &latestQuestionSet, &latestStatus, &latestPinned)
	if errors.Is(err, sql.ErrNoRows) {
		return AnswerMatchView{}, ErrNotFound
	}
	if err != nil {
		return AnswerMatchView{}, err
	}
	var runID, runCheckID, runQuestionSet, runCatalog, runCreated string
	err = tx.QueryRowContext(ctx, `SELECT id,check_id,question_set_sha256,answer_catalog_digest,created_at
	  FROM answer_match_runs WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).
		Scan(&runID, &runCheckID, &runQuestionSet, &runCatalog, &runCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return AnswerMatchView{}, ErrNotFound
	}
	if err != nil {
		return AnswerMatchView{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.question_id,m.question_text_sha256,m.candidate_set_hash,
	  m.answer_id,m.answer_version,m.answer_text_sha256,m.none_fits,m.deterministic,
	  m.confidence,m.jev_attempt_id,j.returned_model,m.matched_at
	  FROM answer_matches m JOIN job_check_questions q ON q.id=m.question_id AND q.check_id=?
	  LEFT JOIN jev_attempts j ON j.id=m.jev_attempt_id
	  WHERE m.run_id=? ORDER BY q.ordinal`, runCheckID, runID)
	if err != nil {
		return AnswerMatchView{}, err
	}
	defer rows.Close()
	view := AnswerMatchView{CheckID: runCheckID, QuestionSetSHA256: runQuestionSet,
		AnswerCatalogDigest: runCatalog, Matches: []AnswerMatch{}}
	for rows.Next() {
		var match AnswerMatch
		var answerID, answerSHA, attempt, model sql.NullString
		var answerVersion sql.NullInt64
		var noneFits, deterministic int
		if err := rows.Scan(&match.QuestionID, &match.QuestionTextSHA256, &match.CandidateSetHash,
			&answerID, &answerVersion, &answerSHA, &noneFits, &deterministic,
			&match.Confidence, &attempt, &model, &match.MatchedAt); err != nil {
			return AnswerMatchView{}, err
		}
		match.Choice = AnswerMatchChoice{AnswerID: answerID.String, AnswerTextSHA256: answerSHA.String,
			NoneFits: noneFits == 1}
		if answerVersion.Valid {
			match.Choice.AnswerVersion = answerVersion.Int64
		}
		match.Deterministic = deterministic == 1
		match.JevAttemptID = attempt.String
		match.Model = model.String
		if view.CatalogMatchedAt == "" || match.MatchedAt > view.CatalogMatchedAt {
			view.CatalogMatchedAt = match.MatchedAt
		}
		view.Matches = append(view.Matches, match)
	}
	if err := rows.Err(); err != nil {
		return AnswerMatchView{}, err
	}
	if view.CatalogMatchedAt == "" {
		view.CatalogMatchedAt = runCreated
	}
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
		opportunityID).Scan(&currentRevision); err != nil {
		return AnswerMatchView{}, err
	}
	catalog, err := answerCatalogDigestTx(ctx, tx)
	if err != nil {
		return AnswerMatchView{}, err
	}
	if runCheckID != latestCheckID || runQuestionSet != latestQuestionSet || runCatalog != catalog ||
		(latestPinned != currentRevision &&
			(latestStatus == CheckStatusChecking || latestStatus == CheckStatusChecked)) {
		view.Status = AnswerMatchStatusOutdated
		return view, tx.Commit()
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM job_check_questions WHERE check_id=?`,
		latestCheckID).Scan(&total); err != nil {
		return AnswerMatchView{}, err
	}
	if len(view.Matches) >= total && total > 0 {
		view.Status = AnswerMatchStatusUnmatched
		for _, match := range view.Matches {
			if !match.Choice.NoneFits {
				view.Status = AnswerMatchStatusMatched
				break
			}
		}
		return view, tx.Commit()
	}
	view.Status = AnswerMatchStatusPartial
	return view, tx.Commit()
}
