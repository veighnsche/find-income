package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Owner clarification persistence (K4). One genuinely unknown personal
// fact per row, tied to the sourced vacancy requirement that demands it
// and the affected work its answer resumes. Separate from employer
// questions: nothing on this path writes saved answers.

// Clarification statuses.
const (
	ClarificationOpen     = "open"
	ClarificationAnswered = "answered"
)

// ClarificationRequirement ties the question to the sourced vacancy
// requirement: the verbatim statement plus its capture span.
type ClarificationRequirement struct {
	Statement string `json:"statement"`
	CaptureID string `json:"captureId"`
	SpanStart int    `json:"spanStart"`
	SpanEnd   int    `json:"spanEnd"`
}

// ClarificationWorkRef names one affected work item the answer resumes.
type ClarificationWorkRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Clarification is one saved owner question with its exact answer.
type Clarification struct {
	ID            string                   `json:"id"`
	OpportunityID string                   `json:"opportunityId"`
	CheckID       string                   `json:"checkId"`
	Origin        string                   `json:"origin"`
	Requirement   ClarificationRequirement `json:"requirement"`
	Prompt        string                   `json:"prompt"`
	AffectedWork  []ClarificationWorkRef   `json:"affectedWork"`
	Status        string                   `json:"status"`
	Answer        string                   `json:"answer,omitempty"`
	AnsweredAt    string                   `json:"answeredAt,omitempty"`
	AnsweredBy    Actor                    `json:"answeredBy,omitempty"`
	CreatedAt     string                   `json:"createdAt"`
}

// ClarificationOpenInput carries one new owner question. Validation
// lives in agency; the store enforces selection, check binding and
// request-key idempotency.
type ClarificationOpenInput struct {
	RequestKey   string
	CheckID      string
	Requirement  ClarificationRequirement
	Prompt       string
	AffectedWork []ClarificationWorkRef
}

// ClarificationAnswer carries the owner's exact answer text.
type ClarificationAnswer struct {
	RequestKey string
	Text       string
}

func clarificationDigest(input ClarificationOpenInput) string {
	raw, _ := json.Marshal(struct {
		CheckID      string
		Requirement  ClarificationRequirement
		Prompt       string
		AffectedWork []ClarificationWorkRef
	}{input.CheckID, input.Requirement, input.Prompt, input.AffectedWork})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func scanClarification(row rowScanner) (Clarification, string, error) {
	var item Clarification
	var affectedJSON, answeredKind, answeredID, digest string
	var status string
	err := row.Scan(&item.ID, &item.OpportunityID, &item.CheckID, &item.Origin,
		&item.Requirement.Statement, &item.Requirement.CaptureID,
		&item.Requirement.SpanStart, &item.Requirement.SpanEnd,
		&item.Prompt, &affectedJSON, &status,
		&item.Answer, &item.AnsweredAt, &answeredKind, &answeredID,
		&digest, &item.CreatedAt)
	if err != nil {
		return Clarification{}, "", err
	}
	item.Status = status
	if answeredKind != "" || answeredID != "" {
		item.AnsweredBy = Actor{Kind: answeredKind, ID: answeredID}
	}
	if err := json.Unmarshal([]byte(affectedJSON), &item.AffectedWork); err != nil {
		return Clarification{}, "", err
	}
	if item.AffectedWork == nil {
		item.AffectedWork = []ClarificationWorkRef{}
	}
	return item, digest, nil
}

const clarificationColumns = `id,opportunity_id,check_id,origin,requirement_statement,` +
	`requirement_capture_id,span_start,span_end,prompt,affected_work_json,status,` +
	`answer_text,answered_at,answered_by_kind,answered_by_id,input_digest,created_at`

func checkBelongsToOpportunity(ctx context.Context, tx *sql.Tx, checkID, opportunityID string) error {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT opportunity_id FROM job_checks WHERE id=?`, checkID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != opportunityID {
		return ErrNotFound
	}
	return nil
}

// OpenOwnerClarification saves one owner question. The role must be
// selected and the check must belong to it. Replays of the same request
// key return the accepted clarification; a reused key with different
// input conflicts.
func (s *Store) OpenOwnerClarification(ctx context.Context, actor Actor, opportunityID string, input ClarificationOpenInput) (Clarification, bool, error) {
	if !ownerRoundActor(actor) || opportunityID == "" || input.RequestKey == "" || input.CheckID == "" {
		return Clarification{}, false, ErrInvalid
	}
	affected, err := json.Marshal(input.AffectedWork)
	if err != nil {
		return Clarification{}, false, ErrInvalid
	}
	digest := clarificationDigest(input)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Clarification{}, false, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return Clarification{}, false, err
	}
	if err := checkBelongsToOpportunity(ctx, tx, input.CheckID, opportunityID); err != nil {
		return Clarification{}, false, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM owner_clarifications
	  WHERE opportunity_id=? AND request_key=?`, opportunityID, input.RequestKey).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Clarification{}, false, err
	}
	if err == nil {
		existing, stored, err := scanClarification(tx.QueryRowContext(ctx,
			`SELECT `+clarificationColumns+` FROM owner_clarifications WHERE id=?`, id))
		if err != nil {
			return Clarification{}, false, err
		}
		if stored != digest {
			return Clarification{}, false, ErrRoundIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return Clarification{}, false, err
		}
		return existing, false, nil
	}
	id, err = randomID()
	if err != nil {
		return Clarification{}, false, err
	}
	now := utcNow()
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_clarifications
	  (id,opportunity_id,check_id,origin,requirement_statement,requirement_capture_id,
	   span_start,span_end,prompt,affected_work_json,status,request_key,input_digest,
	   created_at,updated_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,'open',?,?,?,?)`, id, opportunityID, input.CheckID,
		"owner_clarification", input.Requirement.Statement, input.Requirement.CaptureID,
		input.Requirement.SpanStart, input.Requirement.SpanEnd, input.Prompt, string(affected),
		input.RequestKey, digest, now, now); err != nil {
		return Clarification{}, false, err
	}
	item := Clarification{ID: id, OpportunityID: opportunityID, CheckID: input.CheckID,
		Origin: "owner_clarification", Requirement: input.Requirement, Prompt: input.Prompt,
		AffectedWork: append([]ClarificationWorkRef(nil), input.AffectedWork...),
		Status:       ClarificationOpen, CreatedAt: now}
	if err := tx.Commit(); err != nil {
		return Clarification{}, false, err
	}
	return item, true, nil
}

// AnswerOwnerClarification saves the owner's exact answer verbatim. The
// answer resolves exactly once: the same text replays the accepted
// answer while different text on an answered clarification conflicts.
func (s *Store) AnswerOwnerClarification(ctx context.Context, actor Actor, id string, input ClarificationAnswer) (Clarification, error) {
	if !ownerRoundActor(actor) || id == "" || input.RequestKey == "" || input.Text == "" {
		return Clarification{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Clarification{}, err
	}
	defer tx.Rollback()
	item, _, err := scanClarification(tx.QueryRowContext(ctx,
		`SELECT `+clarificationColumns+` FROM owner_clarifications WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Clarification{}, ErrNotFound
	}
	if err != nil {
		return Clarification{}, err
	}
	if err := checkSelectedRoleTx(ctx, tx, item.OpportunityID); err != nil {
		return Clarification{}, err
	}
	if item.Status == ClarificationAnswered {
		if item.Answer != input.Text {
			return Clarification{}, ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return Clarification{}, err
		}
		return item, nil
	}
	now := utcNow()
	if _, err := tx.ExecContext(ctx, `UPDATE owner_clarifications
	  SET status='answered',answer_text=?,answered_at=?,answered_by_kind=?,answered_by_id=?,
	      answer_request_key=?,updated_at=?
	  WHERE id=? AND status='open'`, input.Text, now, actor.Kind, actor.ID,
		input.RequestKey, now, id); err != nil {
		return Clarification{}, err
	}
	item.Status = ClarificationAnswered
	item.Answer = input.Text
	item.AnsweredAt = now
	item.AnsweredBy = actor
	if err := tx.Commit(); err != nil {
		return Clarification{}, err
	}
	return item, nil
}

// GetOwnerClarification reads one clarification by id.
func (s *Store) GetOwnerClarification(ctx context.Context, id string) (Clarification, error) {
	if id == "" {
		return Clarification{}, ErrInvalid
	}
	item, _, err := scanClarification(s.db.QueryRowContext(ctx,
		`SELECT `+clarificationColumns+` FROM owner_clarifications WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Clarification{}, ErrNotFound
	}
	return item, err
}

// ListOwnerClarifications reads one job's clarifications in id order.
func (s *Store) ListOwnerClarifications(ctx context.Context, opportunityID string) ([]Clarification, error) {
	if opportunityID == "" {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+clarificationColumns+
		` FROM owner_clarifications WHERE opportunity_id=? ORDER BY id`, opportunityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Clarification{}
	for rows.Next() {
		item, _, err := scanClarification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
