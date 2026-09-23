package store

import (
	"context"
	"database/sql"
	"errors"
)

// RebindInputProfile keeps the original snapshot for audit while moving a
// process_input round to the profile version it just corrected. Earlier turn
// capabilities are fenced by the generation change; spent allowance stays.
func (s *Store) RebindInputProfile(ctx context.Context, actor Actor, roundID string) (Round, error) {
	if !ownerRoundActor(actor) || roundID == "" {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.Actor != actor || r.Outcome != "process_input" || r.State != RoundRunning && r.State != RoundPaused {
		return Round{}, ErrFenced
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&current); err != nil {
		return Round{}, err
	}
	if current == r.ProfileVersion {
		return r, tx.Commit()
	}
	var instructionID string
	err = tx.QueryRowContext(ctx, `SELECT app.instruction_id FROM round_record_changes rc
	  JOIN audit_changes ac ON ac.id=rc.audit_id
	  JOIN owner_instruction_applications app ON app.audit_id=ac.id
	  WHERE rc.round_id=? AND ac.operation=? AND ac.entity_kind='preferences'
	    AND ac.revision_before=? AND ac.revision_after=? LIMIT 1`, roundID, RoundCorrectPreferences, r.ProfileVersion, current).Scan(&instructionID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !scopeHas(r.Scope.InputRefs, "instruction:"+instructionID) {
		return Round{}, ErrConflict
	}
	if err != nil {
		return Round{}, err
	}
	var inflight int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_attempts WHERE round_id=? AND state IN ('dispatched','uncertain')`, roundID).Scan(&inflight); err != nil {
		return Round{}, err
	}
	if inflight != 0 {
		return Round{}, ErrUncertain
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET profile_version=?,generation=generation+1,
	  revision=revision+1,step='profile_rebound',updated_at=? WHERE id=? AND revision=?`, current, utcNow(), roundID, r.Revision); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.profile_rebind", roundID); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, roundID))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}
