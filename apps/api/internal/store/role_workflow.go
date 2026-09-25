package store

import (
	"context"
	"database/sql"
	"errors"
)

// ErrRoleNotSelected reports a workflow read or transition for an opportunity
// whose latest owner decision is not selected.
var ErrRoleNotSelected = errors.New("role not selected")

const (
	RoleStageSelected  = "selected"
	RoleStageChecking  = "checking"
	RoleStageChecked   = "checked"
	RoleStageAnswering = "answering"
	RoleStageAnswered  = "answered"
	RoleStagePreparing = "preparing"
	RoleStagePrepared  = "prepared"
	RoleStageReviewing = "reviewing"
	RoleStageSent      = "sent"
	RoleStageBlocked   = "blocked"
)

// roleStageTransitions is the single server-owned transition table for the
// per-role journey. Feature operations advance stages through
// AdvanceRoleWorkflow; no browser, selection, or discovery action may move a
// role. Later contracts extend this table only through the contract owner.
var roleStageTransitions = map[string][]string{
	RoleStageSelected:  {RoleStageChecking},
	RoleStageChecking:  {RoleStageChecked, RoleStageBlocked},
	RoleStageBlocked:   {RoleStageChecking, RoleStagePreparing},
	RoleStageChecked:   {RoleStageAnswering, RoleStageChecking},
	RoleStageAnswering: {RoleStageAnswered},
	RoleStageAnswered:  {RoleStagePreparing},
	RoleStagePreparing: {RoleStagePrepared, RoleStageBlocked},
	RoleStagePrepared:  {RoleStageReviewing, RoleStagePreparing},
	RoleStageReviewing: {RoleStageSent, RoleStagePrepared},
	RoleStageSent:      {},
}

func roleStageKnown(stage string) bool {
	_, ok := roleStageTransitions[stage]
	return ok
}

func roleStageAllowed(from, to string) bool {
	for _, next := range roleStageTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// RoleWorkflow is the authoritative per-role stage. Revision 0 with stage
// selected is the virtual initial state of a selected role without a saved
// row; every transition persists a row and increments the revision.
type RoleWorkflow struct {
	OpportunityID  string `json:"opportunityId"`
	Stage          string `json:"stage"`
	Revision       int64  `json:"revision"`
	BlockedReason  string `json:"blockedReason,omitempty"`
	UpdatedAt      string `json:"updatedAt"`
	DecisionAt     string `json:"decisionAt"`
	OpportunityRev int64  `json:"opportunityRevision"`
}

func selectedDecisionTx(ctx context.Context, tx *sql.Tx, opportunityID string) (OwnerDecision, error) {
	value, err := scanOwnerDecision(tx.QueryRowContext(ctx, `SELECT `+ownerDecisionColumns+`
	  FROM owner_opportunity_decisions WHERE opportunity_id=? ORDER BY revision DESC LIMIT 1`, opportunityID))
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerDecision{}, ErrRoleNotSelected
	}
	if err != nil {
		return OwnerDecision{}, err
	}
	if value.Decision != "selected" {
		return OwnerDecision{}, ErrRoleNotSelected
	}
	return value, nil
}

func scanRoleWorkflow(row rowScanner, opportunityID string, opportunityRev int64, decisionAt string) (RoleWorkflow, error) {
	var value RoleWorkflow
	var blocked sql.NullString
	var updated string
	err := row.Scan(&value.Stage, &value.Revision, &blocked, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleWorkflow{OpportunityID: opportunityID, Stage: RoleStageSelected,
			OpportunityRev: opportunityRev, DecisionAt: decisionAt, UpdatedAt: decisionAt}, nil
	}
	if err != nil {
		return RoleWorkflow{}, err
	}
	value.OpportunityID, value.BlockedReason = opportunityID, blocked.String
	value.UpdatedAt, value.DecisionAt, value.OpportunityRev = updated, decisionAt, opportunityRev
	return value, nil
}

// RoleWorkflow reads one selected role's stage. Unselected or missing roles
// return ErrRoleNotSelected or ErrNotFound; selection alone never advances a
// role past the virtual selected state.
func (s *Store) RoleWorkflow(ctx context.Context, opportunityID string) (RoleWorkflow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RoleWorkflow{}, err
	}
	defer tx.Rollback()
	var rev int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleWorkflow{}, ErrNotFound
	}
	if err != nil {
		return RoleWorkflow{}, err
	}
	decision, err := selectedDecisionTx(ctx, tx, opportunityID)
	if err != nil {
		return RoleWorkflow{}, err
	}
	value, err := scanRoleWorkflow(tx.QueryRowContext(ctx, `SELECT stage,revision,blocked_reason,updated_at
	  FROM role_workflow WHERE opportunity_id=?`, opportunityID), opportunityID, rev, decision.CreatedAt)
	if err != nil {
		return RoleWorkflow{}, err
	}
	return value, tx.Commit()
}

// ListRoleWorkflows returns one entry per selected role. Roles without a
// saved row report the virtual selected state; each role advances
// independently, so one blocked role never constrains another.
func (s *Store) ListRoleWorkflows(ctx context.Context) ([]RoleWorkflow, error) {
	rows, err := s.db.QueryContext(ctx, `
	  SELECT o.id, o.revision, d.created_at, w.stage, w.revision, w.blocked_reason, w.updated_at
	  FROM opportunities o
	  JOIN owner_opportunity_decisions d ON d.opportunity_id=o.id AND d.decision='selected'
	    AND d.revision=(SELECT MAX(revision) FROM owner_opportunity_decisions WHERE opportunity_id=o.id)
	  LEFT JOIN role_workflow w ON w.opportunity_id=o.id
	  WHERE o.archived_at IS NULL ORDER BY o.created_at, o.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RoleWorkflow{}
	for rows.Next() {
		var id, decisionAt string
		var rev int64
		var stage, blocked, updated sql.NullString
		var wfRev sql.NullInt64
		if err := rows.Scan(&id, &rev, &decisionAt, &stage, &wfRev, &blocked, &updated); err != nil {
			return nil, err
		}
		value := RoleWorkflow{OpportunityID: id, Stage: RoleStageSelected,
			OpportunityRev: rev, DecisionAt: decisionAt, UpdatedAt: decisionAt}
		if stage.Valid {
			value.Stage, value.Revision = stage.String, wfRev.Int64
			value.BlockedReason, value.UpdatedAt = blocked.String, updated.String
		}
		items = append(items, value)
	}
	return items, rows.Err()
}

// AdvanceRoleWorkflow persists one guarded stage transition for a selected
// role. The caller supplies the revision it read; a mismatch fails with
// ErrConflict so reloads and concurrent sessions cannot silently diverge.
// blockedReason is required when entering blocked and cleared otherwise.
func (s *Store) AdvanceRoleWorkflow(ctx context.Context, opportunityID string, expectedRevision int64, toStage, blockedReason string) (RoleWorkflow, error) {
	if opportunityID == "" || !roleStageKnown(toStage) ||
		(toStage == RoleStageBlocked) == (blockedReason == "") {
		return RoleWorkflow{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RoleWorkflow{}, err
	}
	defer tx.Rollback()
	var rev int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleWorkflow{}, ErrNotFound
	}
	if err != nil {
		return RoleWorkflow{}, err
	}
	decision, err := selectedDecisionTx(ctx, tx, opportunityID)
	if err != nil {
		return RoleWorkflow{}, err
	}
	current, err := scanRoleWorkflow(tx.QueryRowContext(ctx, `SELECT stage,revision,blocked_reason,updated_at
	  FROM role_workflow WHERE opportunity_id=?`, opportunityID), opportunityID, rev, decision.CreatedAt)
	if err != nil {
		return RoleWorkflow{}, err
	}
	if current.Revision != expectedRevision {
		return RoleWorkflow{}, ErrConflict
	}
	if !roleStageAllowed(current.Stage, toStage) {
		return RoleWorkflow{}, ErrInvalid
	}
	now := utcNow()
	value := RoleWorkflow{OpportunityID: opportunityID, Stage: toStage, Revision: current.Revision + 1,
		OpportunityRev: rev, DecisionAt: decision.CreatedAt, UpdatedAt: now}
	if toStage == RoleStageBlocked {
		value.BlockedReason = blockedReason
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO role_workflow (opportunity_id,stage,revision,blocked_reason,updated_at)
	  VALUES (?,?,?,?,?) ON CONFLICT(opportunity_id) DO UPDATE SET stage=excluded.stage,
	  revision=excluded.revision, blocked_reason=excluded.blocked_reason, updated_at=excluded.updated_at`,
		opportunityID, value.Stage, value.Revision, value.BlockedReason, now)
	if err != nil {
		return RoleWorkflow{}, err
	}
	auditID, err := randomID()
	if err != nil {
		return RoleWorkflow{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, "system", "role-workflow", "role."+toStage,
		"role_workflow", opportunityID, current.Revision, value.Revision, now)
	if err != nil {
		return RoleWorkflow{}, err
	}
	return value, tx.Commit()
}
