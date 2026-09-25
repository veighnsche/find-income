package store

import (
	"context"
)

// SaveJobCheckBody completes one pending check with a deterministic
// server-side payload. Session tools save through round mutations with a
// minted capability; the deterministic E08 performer has no session, so it
// calls this direct writer with the owner actor instead. Selection guards,
// revision fencing and the completeness gate are identical.
func (s *Store) SaveJobCheckBody(ctx context.Context, actor Actor, input CheckSaveInput) (CheckView, error) {
	if !ownerRoundActor(actor) {
		return CheckView{}, ErrFenced
	}
	if input.OpportunityID == "" || input.CheckID == "" {
		return CheckView{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckView{}, err
	}
	defer tx.Rollback()
	var opportunityRev int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
		input.OpportunityID).Scan(&opportunityRev)
	if err != nil {
		return CheckView{}, err
	}
	decision, err := selectedDecisionTx(ctx, tx, input.OpportunityID)
	if err != nil {
		return CheckView{}, err
	}
	current, err := scanRoleWorkflow(tx.QueryRowContext(ctx, `SELECT stage,revision,blocked_reason,updated_at
	  FROM role_workflow WHERE opportunity_id=?`, input.OpportunityID),
		input.OpportunityID, opportunityRev, decision.CreatedAt)
	if err != nil {
		return CheckView{}, err
	}
	if _, _, err := writeCheckSaveTx(ctx, tx, actor, current.Revision, input); err != nil {
		return CheckView{}, err
	}
	view, err := loadCheckView(ctx, tx, input.CheckID)
	if err != nil {
		return CheckView{}, err
	}
	if err := tx.Commit(); err != nil {
		return CheckView{}, err
	}
	return view, nil
}
