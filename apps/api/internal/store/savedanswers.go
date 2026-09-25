package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SavedAnswerSourceRefKind enumerates the provenance kinds an approved answer
// version may cite. Refs are owner-supplied pointers, never fetched content.
const (
	SavedAnswerSourceOwnerStatement = "owner_statement"
	SavedAnswerSourceCapture        = "capture"
	SavedAnswerSourceCV             = "cv_source"
)

type SavedAnswerSourceRef struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Excerpt string `json:"excerpt,omitempty"`
}

type SavedAnswerApprover struct {
	ActorKind string `json:"actorKind"`
	ActorID   string `json:"actorId"`
}

// SavedAnswerVersion is one immutable approved text. Versions are append-only:
// approving a new version never rewrites an earlier row.
type SavedAnswerVersion struct {
	Version            int64                  `json:"version"`
	Text               string                 `json:"text"`
	TextSHA256         string                 `json:"textSha256"`
	ApprovedAt         string                 `json:"approvedAt"`
	ApprovedBy         SavedAnswerApprover    `json:"approvedBy"`
	ApprovalRequestKey string                 `json:"approvalRequestKey"`
	ChangeNote         string                 `json:"changeNote,omitempty"`
	SourceRefs         []SavedAnswerSourceRef `json:"sourceRefs,omitempty"`
	Supersedes         *int64                 `json:"supersedes,omitempty"`
}

// SavedAnswer is an owner-approved reusable answer with its full immutable
// version history ordered ascending. Only the two explicit approval methods
// below confer approved status; no other write path inserts these rows, so
// owner-written application text never silently enters the library.
type SavedAnswer struct {
	ID             string               `json:"id"`
	CurrentVersion int64                `json:"currentVersion"`
	ScopeTags      []string             `json:"scopeTags"`
	ContextNote    string               `json:"contextNote,omitempty"`
	Versions       []SavedAnswerVersion `json:"versions"`
	CreatedAt      string               `json:"createdAt"`
	UpdatedAt      string               `json:"updatedAt"`
}

type SavedAnswerCreateInput struct {
	RequestKey  string                 `json:"requestKey"`
	Text        string                 `json:"text"`
	ScopeTags   []string               `json:"scopeTags"`
	ContextNote string                 `json:"contextNote,omitempty"`
	SourceRefs  []SavedAnswerSourceRef `json:"sourceRefs,omitempty"`
}

type SavedAnswerVersionCreateInput struct {
	RequestKey      string `json:"requestKey"`
	ExpectedVersion int64  `json:"expectedVersion"`
	Text            string `json:"text"`
	ChangeNote      string `json:"changeNote,omitempty"`
}

type SavedAnswerListOptions struct {
	Cursor string
	Limit  int
	Scope  string
}

type SavedAnswerPage struct {
	Items      []SavedAnswer
	NextCursor string
}

func savedAnswerTextSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func validSavedAnswerText(text string) bool { return boundedNonempty(text, 20000) }

func canonicalScopeTags(tags []string) ([]string, bool) {
	if len(tags) > 32 {
		return nil, false
	}
	out := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if tag == "" || len(tag) > 64 || !utf8.ValidString(tag) || strings.TrimSpace(tag) != tag {
			return nil, false
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out, true
}

func validSavedAnswerSourceRefs(refs []SavedAnswerSourceRef) bool {
	if len(refs) > 20 {
		return false
	}
	for _, ref := range refs {
		if ref.Kind != SavedAnswerSourceOwnerStatement && ref.Kind != SavedAnswerSourceCapture && ref.Kind != SavedAnswerSourceCV {
			return false
		}
		if ref.Ref == "" || len(ref.Ref) > 500 || strings.TrimSpace(ref.Ref) != ref.Ref || !utf8.ValidString(ref.Ref) {
			return false
		}
		if len(ref.Excerpt) > 2000 || !utf8.ValidString(ref.Excerpt) {
			return false
		}
	}
	return true
}

func validSavedAnswerNote(note string) bool {
	return len(note) <= 2000 && utf8.ValidString(note)
}

// CreateSavedAnswer approves a reusable answer: creation confers approved
// status as version 1. There are no drafts; unapproved content stays
// ineligible until this explicit owner call. Idempotent on the owner's
// request key: a replay with identical input returns the same answer,
// while a reused key with different input reports ErrRoundIdempotencyConflict.
func (s *Store) CreateSavedAnswer(ctx context.Context, actor Actor, input SavedAnswerCreateInput) (SavedAnswer, bool, error) {
	if !ownerRoundActor(actor) || !ownerRequestKey(input.RequestKey) ||
		!validSavedAnswerText(input.Text) || !validSavedAnswerNote(input.ContextNote) ||
		!validSavedAnswerSourceRefs(input.SourceRefs) {
		return SavedAnswer{}, false, ErrInvalid
	}
	tags, ok := canonicalScopeTags(input.ScopeTags)
	if !ok {
		return SavedAnswer{}, false, ErrInvalid
	}
	canonical := SavedAnswerCreateInput{RequestKey: input.RequestKey, Text: input.Text,
		ScopeTags: tags, ContextNote: input.ContextNote, SourceRefs: input.SourceRefs}
	digest := ownerDigest(canonical)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SavedAnswer{}, false, err
	}
	defer tx.Rollback()
	var oldID, oldDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM saved_answers WHERE actor_id=? AND request_key=?`,
		actor.ID, input.RequestKey).Scan(&oldID, &oldDigest)
	if err == nil {
		if oldDigest != digest {
			return SavedAnswer{}, false, ErrRoundIdempotencyConflict
		}
		value, err := loadSavedAnswerTx(ctx, tx, oldID)
		return value, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SavedAnswer{}, false, err
	}
	id, err := randomID()
	if err != nil {
		return SavedAnswer{}, false, err
	}
	now := recordNow()
	var sourceRefsJSON any
	if len(input.SourceRefs) > 0 {
		encoded, err := json.Marshal(input.SourceRefs)
		if err != nil {
			return SavedAnswer{}, false, err
		}
		sourceRefsJSON = string(encoded)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO saved_answers
	  (id,actor_id,request_key,request_sha256,context_note,current_version,created_at,updated_at)
	  VALUES (?,?,?,?,?,?,?,?)`, id, actor.ID, input.RequestKey, digest, input.ContextNote, 1, now, now); err != nil {
		return SavedAnswer{}, false, fmt.Errorf("insert saved answer: %w", err)
	}
	for ordinal, tag := range tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO saved_answer_scope_tags (answer_id,tag,ordinal) VALUES (?,?,?)`,
			id, tag, ordinal); err != nil {
			return SavedAnswer{}, false, fmt.Errorf("insert saved answer tags: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO saved_answer_versions
	  (answer_id,version,text,text_sha256,approved_at,approved_by_kind,approved_by_id,
	   approval_request_key,request_sha256,change_note,source_refs_json,supersedes)
	  VALUES (?,1,?,?,?,?,?,?,?,'',?,NULL)`, id, input.Text, savedAnswerTextSHA(input.Text),
		now, actor.Kind, actor.ID, input.RequestKey, digest, sourceRefsJSON); err != nil {
		return SavedAnswer{}, false, fmt.Errorf("insert saved answer version: %w", err)
	}
	auditID, err := randomID()
	if err != nil {
		return SavedAnswer{}, false, err
	}
	after := int64(1)
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "answer.approve",
		"saved_answer", id, after, now); err != nil {
		return SavedAnswer{}, false, err
	}
	value, err := loadSavedAnswerTx(ctx, tx, id)
	if err != nil {
		return SavedAnswer{}, false, err
	}
	return value, true, tx.Commit()
}

// ApproveSavedAnswerVersion approves a new exact-text version of an existing
// answer. The expected version must match the current version; the new row
// supersedes it while every earlier version stays byte-identical. Idempotent
// on the request key within the answer: replays return the same answer,
// reused keys with different input report ErrRoundIdempotencyConflict.
func (s *Store) ApproveSavedAnswerVersion(ctx context.Context, actor Actor, answerID string, input SavedAnswerVersionCreateInput) (SavedAnswer, bool, error) {
	if !ownerRoundActor(actor) || answerID == "" || !ownerRequestKey(input.RequestKey) ||
		input.ExpectedVersion < 1 || !validSavedAnswerText(input.Text) || !validSavedAnswerNote(input.ChangeNote) {
		return SavedAnswer{}, false, ErrInvalid
	}
	digest := ownerDigest(struct {
		Actor Actor
		Input SavedAnswerVersionCreateInput
	}{actor, input})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SavedAnswer{}, false, err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT current_version FROM saved_answers WHERE id=?`, answerID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SavedAnswer{}, false, ErrNotFound
		}
		return SavedAnswer{}, false, err
	}
	var oldDigest string
	var oldVersion int64
	err = tx.QueryRowContext(ctx, `SELECT version,request_sha256 FROM saved_answer_versions
	  WHERE answer_id=? AND approval_request_key=?`, answerID, input.RequestKey).Scan(&oldVersion, &oldDigest)
	if err == nil {
		if oldDigest != digest {
			return SavedAnswer{}, false, ErrRoundIdempotencyConflict
		}
		value, err := loadSavedAnswerTx(ctx, tx, answerID)
		return value, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SavedAnswer{}, false, err
	}
	if input.ExpectedVersion != current {
		return SavedAnswer{}, false, ErrConflict
	}
	next := current + 1
	now := recordNow()
	if _, err := tx.ExecContext(ctx, `INSERT INTO saved_answer_versions
	  (answer_id,version,text,text_sha256,approved_at,approved_by_kind,approved_by_id,
	   approval_request_key,request_sha256,change_note,source_refs_json,supersedes)
	  VALUES (?,?,?,?,?,?,?,?,?,?,NULL,?)`, answerID, next, input.Text, savedAnswerTextSHA(input.Text),
		now, actor.Kind, actor.ID, input.RequestKey, digest, input.ChangeNote, current); err != nil {
		return SavedAnswer{}, false, fmt.Errorf("insert saved answer version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE saved_answers SET current_version=?,updated_at=? WHERE id=?`,
		next, now, answerID); err != nil {
		return SavedAnswer{}, false, err
	}
	auditID, err := randomID()
	if err != nil {
		return SavedAnswer{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "answer.approve_version",
		"saved_answer", answerID, current, next, now); err != nil {
		return SavedAnswer{}, false, err
	}
	value, err := loadSavedAnswerTx(ctx, tx, answerID)
	if err != nil {
		return SavedAnswer{}, false, err
	}
	return value, true, tx.Commit()
}

func finishSavedAnswerVersion(value SavedAnswerVersion, changeNote, sourceRefsJSON sql.NullString, supersedes sql.NullInt64) (SavedAnswerVersion, error) {
	value.ChangeNote = changeNote.String
	if supersedes.Valid {
		value.Supersedes = &supersedes.Int64
	}
	if sourceRefsJSON.Valid && sourceRefsJSON.String != "" {
		if err := json.Unmarshal([]byte(sourceRefsJSON.String), &value.SourceRefs); err != nil {
			return SavedAnswerVersion{}, err
		}
	}
	if savedAnswerTextSHA(value.Text) != value.TextSHA256 {
		return SavedAnswerVersion{}, fmt.Errorf("%w: saved answer digest mismatch", ErrInvalid)
	}
	return value, nil
}

func scanSavedAnswerVersion(row rowScanner) (SavedAnswerVersion, error) {
	var value SavedAnswerVersion
	var changeNote, sourceRefsJSON sql.NullString
	var supersedes sql.NullInt64
	err := row.Scan(&value.Version, &value.Text, &value.TextSHA256, &value.ApprovedAt,
		&value.ApprovedBy.ActorKind, &value.ApprovedBy.ActorID, &value.ApprovalRequestKey,
		&changeNote, &sourceRefsJSON, &supersedes)
	if err != nil {
		return SavedAnswerVersion{}, err
	}
	return finishSavedAnswerVersion(value, changeNote, sourceRefsJSON, supersedes)
}

const savedAnswerVersionColumns = `version,text,text_sha256,approved_at,approved_by_kind,approved_by_id,
  approval_request_key,change_note,source_refs_json,supersedes`

func loadSavedAnswerTx(ctx context.Context, tx *sql.Tx, id string) (SavedAnswer, error) {
	var value SavedAnswer
	var contextNote string
	err := tx.QueryRowContext(ctx, `SELECT id,current_version,context_note,created_at,updated_at
	  FROM saved_answers WHERE id=?`, id).Scan(&value.ID, &value.CurrentVersion, &contextNote,
		&value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedAnswer{}, ErrNotFound
	}
	if err != nil {
		return SavedAnswer{}, err
	}
	value.ContextNote = contextNote
	value.ScopeTags = []string{}
	rows, err := tx.QueryContext(ctx, `SELECT tag FROM saved_answer_scope_tags WHERE answer_id=? ORDER BY ordinal`, id)
	if err != nil {
		return SavedAnswer{}, err
	}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			return SavedAnswer{}, err
		}
		value.ScopeTags = append(value.ScopeTags, tag)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SavedAnswer{}, err
	}
	rows.Close()
	versionRows, err := tx.QueryContext(ctx, `SELECT `+savedAnswerVersionColumns+`
	  FROM saved_answer_versions WHERE answer_id=? ORDER BY version`, id)
	if err != nil {
		return SavedAnswer{}, err
	}
	defer versionRows.Close()
	value.Versions = []SavedAnswerVersion{}
	for versionRows.Next() {
		version, err := scanSavedAnswerVersion(versionRows)
		if err != nil {
			return SavedAnswer{}, err
		}
		value.Versions = append(value.Versions, version)
	}
	if err := versionRows.Err(); err != nil {
		return SavedAnswer{}, err
	}
	if len(value.Versions) == 0 || value.Versions[len(value.Versions)-1].Version != value.CurrentVersion {
		return SavedAnswer{}, fmt.Errorf("%w: saved answer version chain broken", ErrInvalid)
	}
	return value, nil
}

// SavedAnswer reads one approved answer with its full immutable version
// history. Stored text hashes are re-verified on read.
func (s *Store) SavedAnswer(ctx context.Context, id string) (SavedAnswer, error) {
	if id == "" {
		return SavedAnswer{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SavedAnswer{}, err
	}
	defer tx.Rollback()
	value, err := loadSavedAnswerTx(ctx, tx, id)
	if err != nil {
		return SavedAnswer{}, err
	}
	return value, tx.Commit()
}

// ListSavedAnswers pages approved answers oldest first with an optional exact
// scope-tag filter. The cursor binds the filter: reusing a cursor with a
// changed scope is rejected.
func (s *Store) ListSavedAnswers(ctx context.Context, options SavedAnswerListOptions) (SavedAnswerPage, error) {
	limit, err := boundedListLimit(options.Limit)
	if err != nil {
		return SavedAnswerPage{}, err
	}
	cursor, err := decodeRecordCursor(options.Cursor)
	if err != nil {
		return SavedAnswerPage{}, err
	}
	if options.Scope != "" && (len(options.Scope) > 500 || !utf8.ValidString(options.Scope)) {
		return SavedAnswerPage{}, fmt.Errorf("%w: invalid answer scope filter", ErrInvalid)
	}
	scope := recordListScope("saved_answer", options.Scope)
	if options.Cursor != "" && cursor.Scope != scope {
		return SavedAnswerPage{}, fmt.Errorf("%w: changed answer list filter", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.current_version,a.context_note,a.created_at,a.updated_at
	  FROM saved_answers a
	  WHERE (?='' OR EXISTS (SELECT 1 FROM saved_answer_scope_tags t WHERE t.answer_id=a.id AND t.tag=?))
	  AND (a.created_at>? OR (a.created_at=? AND a.id>?))
	  ORDER BY a.created_at,a.id LIMIT ?`,
		options.Scope, options.Scope, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return SavedAnswerPage{}, err
	}
	defer rows.Close()
	page := SavedAnswerPage{Items: make([]SavedAnswer, 0, limit)}
	ids := make([]string, 0, limit+1)
	summaries := make([]SavedAnswer, 0, limit+1)
	for rows.Next() {
		var value SavedAnswer
		var contextNote string
		if err := rows.Scan(&value.ID, &value.CurrentVersion, &contextNote, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return SavedAnswerPage{}, err
		}
		value.ContextNote = contextNote
		ids = append(ids, value.ID)
		summaries = append(summaries, value)
	}
	if err := rows.Err(); err != nil {
		return SavedAnswerPage{}, err
	}
	truncated := len(summaries) > limit
	if truncated {
		summaries = summaries[:limit]
		ids = ids[:limit]
	}
	tagsByAnswer := make(map[string][]string, len(ids))
	versionsByAnswer := make(map[string][]SavedAnswerVersion, len(ids))
	if len(ids) > 0 {
		placeholders := strings.Repeat("?,", len(ids))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		tagRows, err := s.db.QueryContext(ctx, `SELECT answer_id,tag FROM saved_answer_scope_tags
		  WHERE answer_id IN (`+placeholders+`) ORDER BY answer_id,ordinal`, args...)
		if err != nil {
			return SavedAnswerPage{}, err
		}
		for tagRows.Next() {
			var answerID, tag string
			if err := tagRows.Scan(&answerID, &tag); err != nil {
				tagRows.Close()
				return SavedAnswerPage{}, err
			}
			tagsByAnswer[answerID] = append(tagsByAnswer[answerID], tag)
		}
		if err := tagRows.Err(); err != nil {
			tagRows.Close()
			return SavedAnswerPage{}, err
		}
		tagRows.Close()
		versionRows, err := s.db.QueryContext(ctx, `SELECT answer_id,`+savedAnswerVersionColumns+`
		  FROM saved_answer_versions WHERE answer_id IN (`+placeholders+`) ORDER BY answer_id,version`, args...)
		if err != nil {
			return SavedAnswerPage{}, err
		}
		for versionRows.Next() {
			var answerID string
			var version SavedAnswerVersion
			var changeNote, sourceRefsJSON sql.NullString
			var supersedes sql.NullInt64
			if err := versionRows.Scan(&answerID, &version.Version, &version.Text, &version.TextSHA256,
				&version.ApprovedAt, &version.ApprovedBy.ActorKind, &version.ApprovedBy.ActorID,
				&version.ApprovalRequestKey, &changeNote, &sourceRefsJSON, &supersedes); err != nil {
				versionRows.Close()
				return SavedAnswerPage{}, err
			}
			finished, err := finishSavedAnswerVersion(version, changeNote, sourceRefsJSON, supersedes)
			if err != nil {
				versionRows.Close()
				return SavedAnswerPage{}, err
			}
			versionsByAnswer[answerID] = append(versionsByAnswer[answerID], finished)
		}
		if err := versionRows.Err(); err != nil {
			versionRows.Close()
			return SavedAnswerPage{}, err
		}
		versionRows.Close()
	}
	for _, summary := range summaries {
		summary.ScopeTags = tagsByAnswer[summary.ID]
		if summary.ScopeTags == nil {
			summary.ScopeTags = []string{}
		}
		summary.Versions = versionsByAnswer[summary.ID]
		if len(summary.Versions) == 0 || summary.Versions[len(summary.Versions)-1].Version != summary.CurrentVersion {
			return SavedAnswerPage{}, fmt.Errorf("%w: saved answer version chain broken", ErrInvalid)
		}
		page.Items = append(page.Items, summary)
	}
	if truncated {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeRecordCursor(recordCursor{Version: 1, CreatedAt: last.CreatedAt, ID: last.ID, Scope: scope})
	}
	return page, nil
}
