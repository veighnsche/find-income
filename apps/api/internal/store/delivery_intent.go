package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) ReserveDeliveryAttempt(ctx context.Context, owner Actor, reviewID, itemID, roundID string) (RoundAttempt, error) {
	if !ownerRoundActor(owner) || reviewID == "" || itemID == "" || roundID == "" {
		return RoundAttempt{}, ErrInvalid
	}
	input := RoundAttemptInput{RequestKey: itemID, Operation: RoundDeliverApplication, ResourceID: "delivery:" + itemID,
		Cost: RoundAllowance{Requests: 1, Items: 1, Tools: 1}}
	attempt, _, err := s.reserveRoundAttempt(ctx, owner, roundID, input, func(ctx context.Context, tx *sql.Tx, round Round) error {
		if round.Outcome != "deliver" || len(round.Scope.InputRefs) != 1 || round.Scope.InputRefs[0] != "delivery_review:"+reviewID {
			return ErrFenced
		}
		var approved, material, ownerID string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(approved_sha256,''),material_sha256,owner_id FROM delivery_reviews WHERE id=?`, reviewID).
			Scan(&approved, &material, &ownerID); err != nil {
			return err
		}
		if approved == "" || approved != material || ownerID != owner.ID {
			return ErrFenced
		}
		item, err := scanDeliveryItem(tx.QueryRowContext(ctx, `SELECT `+deliveryItemColumns+` FROM delivery_items WHERE id=? AND review_id=?`, itemID, reviewID))
		if err != nil {
			return err
		}
		return checkDeliveryItemsCurrentTx(ctx, tx, []DeliveryItem{item})
	})
	return attempt, err
}

// ClaimDeliveryItem commits an irreversible one-shot intent before SMTP is
// invoked. A crashed, stopped or concurrently claimed item is never resent.
func (s *Store) ClaimDeliveryItem(ctx context.Context, owner Actor, reviewID, itemID, roundID, attemptID string) (DeliveryItem, error) {
	if !ownerRoundActor(owner) || reviewID == "" || itemID == "" || roundID == "" || attemptID == "" {
		return DeliveryItem{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryItem{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE preferences_current SET version=version WHERE singleton=1`); err != nil {
		return DeliveryItem{}, err
	}
	var approved, material, approvedOwner string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(approved_sha256,''),material_sha256,owner_id FROM delivery_reviews WHERE id=?`, reviewID).
		Scan(&approved, &material, &approvedOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryItem{}, ErrNotFound
	}
	if err != nil {
		return DeliveryItem{}, err
	}
	if approved == "" || approved != material || approvedOwner != owner.ID {
		return DeliveryItem{}, ErrFenced
	}
	item, err := scanDeliveryItem(tx.QueryRowContext(ctx, `SELECT `+deliveryItemColumns+` FROM delivery_items WHERE id=? AND review_id=?`, itemID, reviewID))
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryItem{}, ErrNotFound
	}
	if err != nil {
		return DeliveryItem{}, err
	}
	if item.State != "prepared" {
		return DeliveryItem{}, ErrFenced
	}
	if err := checkDeliveryItemsCurrentTx(ctx, tx, []DeliveryItem{item}); err != nil {
		return DeliveryItem{}, err
	}
	var attemptState RoundAttemptState
	var operation, resource string
	var generation, roundGeneration int64
	var roundState RoundState
	err = tx.QueryRowContext(ctx, `SELECT a.state,a.operation,a.resource_id,a.generation,r.generation,r.state FROM round_attempts a JOIN rounds r ON r.id=a.round_id WHERE a.id=? AND a.round_id=?`, attemptID, roundID).
		Scan(&attemptState, &operation, &resource, &generation, &roundGeneration, &roundState)
	if err != nil {
		return DeliveryItem{}, err
	}
	if attemptState != AttemptReserved || operation != RoundDeliverApplication || resource != "delivery:"+itemID || generation != roundGeneration || roundState != RoundRunning {
		return DeliveryItem{}, ErrFenced
	}
	updated, err := tx.ExecContext(ctx, `UPDATE delivery_items SET state='sending',round_id=?,attempt_id=?,updated_at=? WHERE id=? AND state='prepared'`, roundID, attemptID, utcNow(), itemID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return DeliveryItem{}, ErrConflict
		}
		return DeliveryItem{}, err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return DeliveryItem{}, err
	}
	if n != 1 {
		return DeliveryItem{}, ErrFenced
	}
	if err := writeRoundAudit(ctx, tx, owner, "delivery.intent", itemID); err != nil {
		return DeliveryItem{}, err
	}
	if err := tx.Commit(); err != nil {
		if strings.Contains(err.Error(), "delivery_one_possible_submission") {
			return DeliveryItem{}, ErrConflict
		}
		return DeliveryItem{}, err
	}
	item.State, item.RoundID, item.AttemptID = "sending", roundID, attemptID
	return item, nil
}

// SaveDeliveryOutcome records even a late response after Stop or a generation
// fence. This record is submission evidence, never employer receipt evidence.
func (s *Store) SaveDeliveryOutcome(ctx context.Context, itemID, attemptID, state, stage string, code int, detail string) error {
	if itemID == "" || attemptID == "" || (state != "accepted_by_smtp" && state != "failed" && state != "uncertain") ||
		len(stage) > 100 || code < 0 || code > 599 || len(detail) > 500 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `UPDATE delivery_items SET state=?,smtp_stage=?,smtp_code=?,outcome_detail=?,updated_at=?
	 WHERE id=? AND attempt_id=? AND state='sending'`, state, stage, code, detail, utcNow(), itemID, attemptID)
	if err != nil {
		return err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "delivery-outcome"}, "delivery.outcome", itemID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PauseUncertainDelivery(ctx context.Context, roundID, attemptID string, generation int64) error {
	if roundID == "" || attemptID == "" || generation < 1 {
		return ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.State != RoundRunning || r.Generation != generation || r.Outcome != "deliver" {
		return ErrFenced
	}
	updated, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='uncertain',error_code='delivery_outcome_unknown',updated_at=?
	 WHERE id=? AND round_id=? AND operation=? AND state IN ('reserved','dispatched') AND generation=?`, utcNow(), attemptID, roundID, RoundDeliverApplication, generation)
	if err != nil {
		return err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='paused',generation=generation+1,revision=revision+1,
	 reconciliation_required=1,stop_reason='delivery_outcome_unknown',updated_at=? WHERE id=?`, utcNow(), roundID); err != nil {
		return err
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "delivery-outcome"}, "delivery.uncertain", roundID); err != nil {
		return err
	}
	return tx.Commit()
}

func recoverDelivery(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `UPDATE delivery_items SET state='uncertain',smtp_stage='interrupted',
	 outcome_detail='send intent existed at restart; submission outcome unknown',updated_at=?
	 WHERE state='sending' AND round_id IN (SELECT id FROM rounds WHERE state='paused')`, utcNow())
	return err
}
