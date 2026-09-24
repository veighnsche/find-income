package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Dynamic assessment result statuses (D §2.2).
const (
	DynamicAssessmentSucceeded       = "succeeded"
	DynamicAssessmentPartialAbstain  = "partial_abstain"
	DynamicAssessmentInvalidResponse = "invalid_response"
	DynamicAssessmentFailed          = "failed"
)

// DynamicAssessment is the queryable binding over one jev_attempts row.
type DynamicAssessment struct {
	ID               string
	ActorKind        string
	ActorID          string
	RoundID          string
	JevAttemptID     string
	Purpose          string
	QuestionsJSON    string
	EvidenceRefsJSON string
	ProfileVersion   int64
	RubricVersion    string
	CandidatesJSON   string
	CandidateSetHash string
	RequestedModel   string
	ReuseKey         string
	Status           string
	AnswersJSON      string
	SupersedesID     string
	CreatedAt        string
}

// DynamicAssessmentInput carries the caller-supplied assessment fields.
type DynamicAssessmentInput struct {
	RoundID          string
	JevAttemptID     string
	Purpose          string
	QuestionsJSON    string
	EvidenceRefsJSON string
	ProfileVersion   int64
	RubricVersion    string
	CandidatesJSON   string
	CandidateSetHash string
	RequestedModel   string
	ReuseKey         string
	Status           string
	AnswersJSON      string
	SupersedesID     string
}

// AssessmentCaptureLink binds one cited span of a capture to an assessment.
type AssessmentCaptureLink struct {
	AssessmentID string
	CaptureID    string
	SpanStart    int64
	SpanEnd      int64
}

const dynamicAssessmentColumns = `id,actor_kind,actor_id,round_id,jev_attempt_id,` +
	`purpose,questions_json,evidence_refs_json,profile_version,rubric_version,` +
	`candidates_json,candidate_set_hash,requested_model,reuse_key,status,` +
	`answers_json,supersedes_id,created_at`

// InsertDynamicAssessment stores one immutable assessment binding. Corrections
// are new rows via SupersedesID (reassessment chain, D §2.2).
func InsertDynamicAssessment(ctx context.Context, db ResearchDB, actor Actor, in DynamicAssessmentInput) (DynamicAssessment, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if in.RoundID == "" || in.JevAttemptID == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: round/jev_attempt required", ErrInvalid)
	}
	if strings.TrimSpace(in.Purpose) == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: purpose required", ErrInvalid)
	}
	if in.QuestionsJSON == "" || in.EvidenceRefsJSON == "" || in.AnswersJSON == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: questions/evidence/answers JSON required", ErrInvalid)
	}
	if in.ProfileVersion <= 0 || in.RubricVersion == "" {
		return DynamicAssessment{}, fmt.Errorf("%w: profile/rubric version required", ErrInvalid)
	}
	if in.CandidatesJSON == "" || len(in.CandidateSetHash) != 64 {
		return DynamicAssessment{}, fmt.Errorf("%w: candidates/set hash required", ErrInvalid)
	}
	if len(in.ReuseKey) != 64 {
		return DynamicAssessment{}, fmt.Errorf("%w: reuse key required", ErrInvalid)
	}
	if !validEnum(in.Status, DynamicAssessmentSucceeded, DynamicAssessmentPartialAbstain,
		DynamicAssessmentInvalidResponse, DynamicAssessmentFailed) {
		return DynamicAssessment{}, fmt.Errorf("%w: unknown assessment status %q", ErrInvalid, in.Status)
	}
	id, err := randomID()
	if err != nil {
		return DynamicAssessment{}, err
	}
	now := recordNow()
	a := DynamicAssessment{
		ID: id, ActorKind: actor.Kind, ActorID: actor.ID, RoundID: in.RoundID,
		JevAttemptID: in.JevAttemptID, Purpose: in.Purpose,
		QuestionsJSON: in.QuestionsJSON, EvidenceRefsJSON: in.EvidenceRefsJSON,
		ProfileVersion: in.ProfileVersion, RubricVersion: in.RubricVersion,
		CandidatesJSON: in.CandidatesJSON, CandidateSetHash: in.CandidateSetHash,
		RequestedModel: in.RequestedModel, ReuseKey: in.ReuseKey,
		Status: in.Status, AnswersJSON: in.AnswersJSON,
		SupersedesID: in.SupersedesID, CreatedAt: now,
	}
	_, err = db.ExecContext(ctx, `INSERT INTO jev_assessments_dynamic
  (id,actor_kind,actor_id,round_id,jev_attempt_id,purpose,questions_json,
   evidence_refs_json,profile_version,rubric_version,candidates_json,
   candidate_set_hash,requested_model,reuse_key,status,answers_json,
   supersedes_id,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ActorKind, a.ActorID, a.RoundID, a.JevAttemptID, a.Purpose,
		a.QuestionsJSON, a.EvidenceRefsJSON, a.ProfileVersion, a.RubricVersion,
		a.CandidatesJSON, a.CandidateSetHash, nullString(a.RequestedModel),
		a.ReuseKey, a.Status, a.AnswersJSON, nullString(a.SupersedesID), a.CreatedAt)
	if err != nil {
		return DynamicAssessment{}, err
	}
	return a, nil
}

// GetDynamicAssessment loads one assessment by id.
func GetDynamicAssessment(ctx context.Context, r Reader, id string) (DynamicAssessment, error) {
	row := r.QueryRowContext(ctx, `SELECT `+dynamicAssessmentColumns+`
  FROM jev_assessments_dynamic WHERE id=?`, id)
	a, err := scanDynamicAssessmentRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DynamicAssessment{}, ErrNotFound
	}
	return a, err
}

// GetDynamicAssessmentByReuseKey resolves one reusable assessment within an
// account, or ErrNotFound.
func GetDynamicAssessmentByReuseKey(ctx context.Context, r Reader, actorKind, actorID, reuseKey string) (DynamicAssessment, error) {
	row := r.QueryRowContext(ctx, `SELECT `+dynamicAssessmentColumns+`
  FROM jev_assessments_dynamic
  WHERE actor_kind=? AND actor_id=? AND reuse_key=?`, actorKind, actorID, reuseKey)
	a, err := scanDynamicAssessmentRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DynamicAssessment{}, ErrNotFound
	}
	return a, err
}

// ListDynamicAssessmentsByBrief returns an account's assessments under one
// profile version, newest first (brief-change invalidation reads only
// dependent judgments).
func ListDynamicAssessmentsByBrief(ctx context.Context, r Reader, actorKind, actorID string, profileVersion int64, limit int) ([]DynamicAssessment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.QueryContext(ctx, `SELECT `+dynamicAssessmentColumns+`
  FROM jev_assessments_dynamic
  WHERE actor_kind=? AND actor_id=? AND profile_version=?
  ORDER BY created_at DESC,id DESC LIMIT ?`, actorKind, actorID, profileVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DynamicAssessment
	for rows.Next() {
		var a DynamicAssessment
		if err := scanDynamicAssessmentInto(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanDynamicAssessmentRow(row *sql.Row) (DynamicAssessment, error) {
	var a DynamicAssessment
	return a, scanDynamicAssessmentInto(row, &a)
}

func scanDynamicAssessmentInto(s researchRequestScanner, a *DynamicAssessment) error {
	var requested, supersedes sql.NullString
	if err := s.Scan(&a.ID, &a.ActorKind, &a.ActorID, &a.RoundID, &a.JevAttemptID,
		&a.Purpose, &a.QuestionsJSON, &a.EvidenceRefsJSON, &a.ProfileVersion,
		&a.RubricVersion, &a.CandidatesJSON, &a.CandidateSetHash, &requested,
		&a.ReuseKey, &a.Status, &a.AnswersJSON, &supersedes, &a.CreatedAt); err != nil {
		return err
	}
	a.RequestedModel = requested.String
	a.SupersedesID = supersedes.String
	return nil
}

// LinkAssessmentCapture binds one cited span to an assessment (D §2.3).
func LinkAssessmentCapture(ctx context.Context, db ResearchDB, link AssessmentCaptureLink) error {
	if link.AssessmentID == "" || link.CaptureID == "" {
		return fmt.Errorf("%w: assessment/capture required", ErrInvalid)
	}
	if link.SpanStart < 0 || link.SpanEnd <= link.SpanStart {
		return fmt.Errorf("%w: span_end must exceed span_start", ErrInvalid)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO jev_assessment_captures
  (assessment_id,capture_id,span_start,span_end) VALUES (?,?,?,?)`,
		link.AssessmentID, link.CaptureID, link.SpanStart, link.SpanEnd)
	return err
}

// ListAssessmentCaptures returns one assessment's cited spans.
func ListAssessmentCaptures(ctx context.Context, r Reader, assessmentID string) ([]AssessmentCaptureLink, error) {
	return listAssessmentCaptures(ctx, r,
		`WHERE assessment_id=? ORDER BY capture_id,span_start,span_end`, assessmentID)
}

// ListAssessmentsByCapture walks capture→assessments: which judgments depend
// on this source.
func ListAssessmentsByCapture(ctx context.Context, r Reader, captureID string) ([]AssessmentCaptureLink, error) {
	return listAssessmentCaptures(ctx, r,
		`WHERE capture_id=? ORDER BY assessment_id,span_start,span_end`, captureID)
}

func listAssessmentCaptures(ctx context.Context, r Reader, suffix string, args ...any) ([]AssessmentCaptureLink, error) {
	rows, err := r.QueryContext(ctx, `SELECT assessment_id,capture_id,span_start,span_end
  FROM jev_assessment_captures `+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AssessmentCaptureLink
	for rows.Next() {
		var link AssessmentCaptureLink
		if err := rows.Scan(&link.AssessmentID, &link.CaptureID, &link.SpanStart, &link.SpanEnd); err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}
