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
)

type OwnerInstructionInput struct {
	RequestKey       string `json:"requestKey"`
	TargetKind       string `json:"targetKind"`
	TargetID         string `json:"targetId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RoundID          string `json:"roundId,omitempty"`
	Text             string `json:"text"`
}

type OwnerInstruction struct {
	ID string `json:"id"`
	OwnerInstructionInput
	ActorID   string `json:"actorId"`
	CreatedAt string `json:"createdAt"`
	RevokedAt string `json:"revokedAt,omitempty"`
}

type OwnerDecisionInput struct {
	RequestKey                  string `json:"requestKey"`
	ExpectedOpportunityRevision int64  `json:"expectedOpportunityRevision"`
	ExpectedDecisionRevision    int64  `json:"expectedDecisionRevision"`
	Decision                    string `json:"decision"`
}

type OwnerDecision struct {
	ID                  string `json:"id"`
	OpportunityID       string `json:"opportunityId"`
	Decision            string `json:"decision"`
	Revision            int64  `json:"revision"`
	OpportunityRevision int64  `json:"opportunityRevision"`
	AuditID             string `json:"auditId"`
	CreatedAt           string `json:"createdAt"`
}

func ownerRequestKey(key string) bool {
	return key != "" && len(key) <= 200 && strings.TrimSpace(key) == key
}

func ownerDigest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func instructionTargetRevision(ctx context.Context, tx *sql.Tx, kind, id string) (int64, error) {
	var revision int64
	var err error
	switch kind {
	case "campaign":
		if id != "active" {
			return 0, ErrInvalid
		}
		fallthrough
	case "profile":
		if kind == "profile" && id != "current" {
			return 0, ErrInvalid
		}
		err = tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&revision)
	case "opportunity":
		err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, id).Scan(&revision)
	case "evidence":
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM evidence WHERE id=?`, id).Scan(&revision)
	case "relationship":
		err = tx.QueryRowContext(ctx, `SELECT revision FROM relationship_counterparties WHERE id=? UNION ALL SELECT revision FROM relationship_events WHERE id=? UNION ALL SELECT revision FROM opportunity_routes WHERE id=?`, id, id, id).Scan(&revision)
	case "application_pack":
		err = tx.QueryRowContext(ctx, `SELECT version FROM application_packs WHERE id=?`, id).Scan(&revision)
	default:
		return 0, ErrInvalid
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return revision, err
}

func scanOwnerInstruction(row rowScanner) (OwnerInstruction, error) {
	var value OwnerInstruction
	var round, revoked sql.NullString
	err := row.Scan(&value.ID, &value.ActorID, &value.RequestKey, &value.TargetKind,
		&value.TargetID, &value.ExpectedRevision, &round, &value.Text, &value.CreatedAt, &revoked)
	value.RoundID, value.RevokedAt = round.String, revoked.String
	return value, err
}

const ownerInstructionColumns = `id,actor_id,request_key,target_kind,target_id,expected_revision,round_id,text,created_at,revoked_at`

// An instruction records owner-authored context with an explicit target. It
// grants no new scope to a running round or to text discovered on the web.
func (s *Store) AddOwnerInstruction(ctx context.Context, actor Actor, input OwnerInstructionInput) (OwnerInstruction, bool, error) {
	if !ownerRoundActor(actor) || !ownerRequestKey(input.RequestKey) ||
		!boundedNonempty(input.Text, 20000) || input.ExpectedRevision < 1 ||
		input.TargetID == "" || len(input.TargetID) > 300 {
		return OwnerInstruction{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OwnerInstruction{}, false, err
	}
	defer tx.Rollback()
	digest := ownerDigest(input)
	var oldDigest string
	var oldID string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM owner_instructions WHERE actor_id=? AND request_key=?`, actor.ID, input.RequestKey).Scan(&oldID, &oldDigest)
	if err == nil {
		if oldDigest != digest {
			return OwnerInstruction{}, false, ErrRoundIdempotencyConflict
		}
		value, err := scanOwnerInstruction(tx.QueryRowContext(ctx, `SELECT `+ownerInstructionColumns+` FROM owner_instructions WHERE id=?`, oldID))
		return value, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OwnerInstruction{}, false, err
	}
	revision, err := instructionTargetRevision(ctx, tx, input.TargetKind, input.TargetID)
	if err != nil {
		return OwnerInstruction{}, false, err
	}
	if revision != input.ExpectedRevision {
		return OwnerInstruction{}, false, ErrConflict
	}
	if input.RoundID != "" {
		round, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, input.RoundID))
		if errors.Is(err, sql.ErrNoRows) {
			return OwnerInstruction{}, false, ErrNotFound
		}
		if err != nil {
			return OwnerInstruction{}, false, err
		}
		if round.Actor != actor || round.State != RoundRunning && round.State != RoundAwaitingInput && round.State != RoundPaused {
			return OwnerInstruction{}, false, ErrFenced
		}
		if input.TargetKind == "profile" && round.ProfileVersion != revision {
			return OwnerInstruction{}, false, ErrFenced
		}
	}
	id, err := randomID()
	if err != nil {
		return OwnerInstruction{}, false, err
	}
	value := OwnerInstruction{ID: id, OwnerInstructionInput: input, ActorID: actor.ID, CreatedAt: utcNow()}
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_instructions
	  (id,actor_id,request_key,request_sha256,target_kind,target_id,expected_revision,round_id,text,created_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?)`, id, actor.ID, input.RequestKey, digest, input.TargetKind,
		input.TargetID, input.ExpectedRevision, nullableString(input.RoundID), input.Text, value.CreatedAt)
	if err != nil {
		return OwnerInstruction{}, false, err
	}
	auditID, err := randomID()
	if err != nil {
		return OwnerInstruction{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,1,?)`, auditID, actor.Kind, actor.ID, "owner.instruction", "owner_instruction", id, value.CreatedAt)
	if err != nil {
		return OwnerInstruction{}, false, err
	}
	return value, true, tx.Commit()
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) OwnerInstructions(ctx context.Context, roundID string) ([]OwnerInstruction, error) {
	query := `SELECT ` + ownerInstructionColumns + ` FROM owner_instructions WHERE revoked_at IS NULL`
	args := []any{}
	if roundID != "" {
		query += ` AND round_id=?`
		args = append(args, roundID)
	}
	query += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []OwnerInstruction{}
	for rows.Next() {
		value, err := scanOwnerInstruction(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	return items, rows.Err()
}

func (s *Store) RevokeOwnerInstruction(ctx context.Context, actor Actor, id string) error {
	if !ownerRoundActor(actor) || id == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE owner_instructions SET revoked_at=? WHERE id=? AND actor_id=? AND revoked_at IS NULL`, utcNow(), id, actor.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	auditID, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes (id,actor_kind,actor_id,operation,entity_kind,entity_id,occurred_at)
	  VALUES (?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "owner.instruction_revoke", "owner_instruction", id, utcNow())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func scanOwnerDecision(row rowScanner) (OwnerDecision, error) {
	var value OwnerDecision
	err := row.Scan(&value.ID, &value.OpportunityID, &value.Decision, &value.Revision,
		&value.OpportunityRevision, &value.AuditID, &value.CreatedAt)
	return value, err
}

const ownerDecisionColumns = `id,opportunity_id,decision,revision,opportunity_revision,audit_id,created_at`

func (s *Store) OwnerOpportunityDecision(ctx context.Context, opportunityID string) (OwnerDecision, error) {
	value, err := scanOwnerDecision(s.db.QueryRowContext(ctx, `SELECT `+ownerDecisionColumns+`
	  FROM owner_opportunity_decisions WHERE opportunity_id=? ORDER BY revision DESC LIMIT 1`, opportunityID))
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerDecision{}, ErrNotFound
	}
	return value, err
}

// The owner's direct decision is an audited local write; no model call or
// active round is needed to save an explicit select, dismiss, or acknowledge.
func (s *Store) SetOwnerOpportunityDecision(ctx context.Context, actor Actor, opportunityID string, input OwnerDecisionInput) (OwnerDecision, bool, error) {
	if !ownerRoundActor(actor) || opportunityID == "" || !ownerRequestKey(input.RequestKey) ||
		input.ExpectedOpportunityRevision < 1 || input.ExpectedDecisionRevision < 0 ||
		input.Decision != "selected" && input.Decision != "dismissed" && input.Decision != "acknowledged" {
		return OwnerDecision{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OwnerDecision{}, false, err
	}
	defer tx.Rollback()
	digest := ownerDigest(struct {
		OpportunityID string
		Input         OwnerDecisionInput
	}{opportunityID, input})
	var oldID, oldDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM owner_opportunity_decisions WHERE actor_id=? AND request_key=?`, actor.ID, input.RequestKey).Scan(&oldID, &oldDigest)
	if err == nil {
		if oldDigest != digest {
			return OwnerDecision{}, false, ErrRoundIdempotencyConflict
		}
		value, err := scanOwnerDecision(tx.QueryRowContext(ctx, `SELECT `+ownerDecisionColumns+` FROM owner_opportunity_decisions WHERE id=?`, oldID))
		return value, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OwnerDecision{}, false, err
	}
	var opportunityRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&opportunityRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerDecision{}, false, ErrNotFound
	}
	if err != nil {
		return OwnerDecision{}, false, err
	}
	if opportunityRevision != input.ExpectedOpportunityRevision {
		return OwnerDecision{}, false, ErrConflict
	}
	var decisionRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM owner_opportunity_decisions WHERE opportunity_id=? ORDER BY revision DESC LIMIT 1`, opportunityID).Scan(&decisionRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return OwnerDecision{}, false, err
	}
	if decisionRevision != input.ExpectedDecisionRevision {
		return OwnerDecision{}, false, ErrConflict
	}
	id, err := randomID()
	if err != nil {
		return OwnerDecision{}, false, err
	}
	auditID, err := randomID()
	if err != nil {
		return OwnerDecision{}, false, err
	}
	now := utcNow()
	value := OwnerDecision{ID: id, OpportunityID: opportunityID, Decision: input.Decision,
		Revision: decisionRevision + 1, OpportunityRevision: opportunityRevision, AuditID: auditID, CreatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "opportunity."+input.Decision,
		"opportunity_decision", opportunityID, decisionRevision, value.Revision, now)
	if err != nil {
		return OwnerDecision{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_opportunity_decisions
	  (id,opportunity_id,actor_id,request_key,request_sha256,revision,opportunity_revision,decision,audit_id,created_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?)`, id, opportunityID, actor.ID, input.RequestKey, digest,
		value.Revision, opportunityRevision, input.Decision, auditID, now)
	if err != nil {
		return OwnerDecision{}, false, fmt.Errorf("write owner decision: %w", err)
	}
	return value, true, tx.Commit()
}
