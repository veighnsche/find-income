package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type RoundEvidenceCorrectionInput struct {
	RequestKey         string        `json:"requestKey"`
	OwnerInstructionID string        `json:"ownerInstructionId"`
	PriorEvidenceID    string        `json:"priorEvidenceId"`
	Evidence           EvidenceInput `json:"evidence"`
	Capability         string        `json:"-"`
}

// CorrectRoundEvidence supersedes exactly one owner-selected evidence record.
// The domain row, qualification evaluation, audit, allowance and round link
// are committed by one transaction in writeEvidenceAfter.
func (s *Store) CorrectRoundEvidence(ctx context.Context, actor Actor, roundID string, input RoundEvidenceCorrectionInput) (RoundMutationResult, bool, error) {
	if actor.Kind != "agent" || actor.ID == "" || roundID == "" || !ownerRequestKey(input.RequestKey) ||
		input.OwnerInstructionID == "" || input.PriorEvidenceID == "" || input.Capability == "" {
		return RoundMutationResult{}, false, ErrInvalid
	}
	authority, err := s.VerifyRoundToolCapability(ctx, input.Capability, roundID)
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	if authority.Actor != actor {
		return RoundMutationResult{}, false, ErrFenced
	}
	digest := ownerDigest(struct {
		Actor     Actor
		Input     RoundEvidenceCorrectionInput
		BoundHash string
	}{
		actor, input, roundCapabilityHash(input.Capability)})
	var oldDigest string
	var oldResult sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT request_sha256,result_json FROM round_attempts WHERE round_id=? AND request_key=?`, roundID, input.RequestKey).Scan(&oldDigest, &oldResult)
	if err == nil {
		if oldDigest != digest {
			return RoundMutationResult{}, false, ErrRoundIdempotencyConflict
		}
		if !oldResult.Valid {
			return RoundMutationResult{}, false, ErrFenced
		}
		var result RoundMutationResult
		if err := json.Unmarshal([]byte(oldResult.String), &result); err != nil {
			return RoundMutationResult{}, false, err
		}
		return result, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RoundMutationResult{}, false, err
	}
	if input.Evidence.OpportunityID == "" {
		return RoundMutationResult{}, false, ErrInvalid
	}
	attemptID, err := randomID()
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	var result RoundMutationResult
	cost, _ := RoundOperationCost(RoundCorrectEvidence)
	charged := false
	_, auditID, err := s.writeEvidenceAfter(ctx, actor, input.PriorEvidenceID, input.Evidence,
		func(tx *sql.Tx) error {
			r, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, roundID))
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if requireRoundState(r, RoundRunning) != nil || !scopeAllowsActor(r.Scope, actor) ||
				!scopeHas(r.Scope.Operations, RoundCorrectEvidence) {
				return ErrFenced
			}
			if _, err := verifyRoundToolCapabilityTx(ctx, tx, input.Capability, roundID); err != nil {
				return err
			}
			var currentVersion int64
			if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentVersion); err != nil {
				return err
			}
			if currentVersion != r.ProfileVersion || input.Evidence.ExpectedPreferencesVersion != currentVersion {
				return ErrConflict
			}
			var priorOpportunityID, companyID string
			err = tx.QueryRowContext(ctx, `SELECT e.opportunity_id,o.company_id FROM evidence e
			  JOIN opportunities o ON o.id=e.opportunity_id WHERE e.id=?`, input.PriorEvidenceID).Scan(&priorOpportunityID, &companyID)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if priorOpportunityID != input.Evidence.OpportunityID || !roundHasCompanyTx(ctx, tx, r, companyID) {
				return ErrFenced
			}
			var kind, targetID, instructionRound, instructionOwner string
			var expected int64
			var revoked sql.NullString
			err = tx.QueryRowContext(ctx, `SELECT target_kind,target_id,expected_revision,COALESCE(round_id,''),revoked_at,actor_id
			  FROM owner_instructions WHERE id=?`, input.OwnerInstructionID).Scan(&kind, &targetID, &expected, &instructionRound, &revoked, &instructionOwner)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrFenced
			}
			if err != nil {
				return err
			}
			if kind != "evidence" || targetID != input.PriorEvidenceID || expected != 1 || revoked.Valid || instructionOwner != r.Actor.ID ||
				instructionRound != roundID && (instructionRound != "" || !scopeHas(r.Scope.InputRefs, "instruction:"+input.OwnerInstructionID)) {
				return ErrFenced
			}
			var used int
			err = tx.QueryRowContext(ctx, `SELECT count(*) FROM round_attempts WHERE round_id=? AND request_key=?`, roundID, input.RequestKey).Scan(&used)
			if err != nil {
				return err
			}
			if used != 0 {
				return ErrRoundIdempotencyConflict
			}
			if charged {
				return nil
			}
			update, err := tx.ExecContext(ctx, `UPDATE rounds SET requests_used=requests_used+?,items_used=items_used+?,
			  tools_used=tools_used+?,revision=revision+1,updated_at=? WHERE id=? AND state='running'
			  AND generation=? AND requests_used+?<=request_limit AND items_used+?<=item_limit AND tools_used+?<=tool_limit`,
				cost.Requests, cost.Items, cost.Tools, utcNow(), roundID, r.Generation, cost.Requests, cost.Items, cost.Tools)
			if err != nil {
				return err
			}
			count, err := update.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return ErrAllowance
			}
			charged = true
			return nil
		}, func(tx *sql.Tx, auditID string, item Evidence) error {
			now := utcNow()
			if _, err := tx.ExecContext(ctx, `INSERT INTO owner_instruction_applications(audit_id,instruction_id) VALUES (?,?)`, auditID, input.OwnerInstructionID); err != nil {
				return err
			}
			result = RoundMutationResult{AttemptID: attemptID, EntityKind: "evidence", EntityID: item.ID, Revision: 1, AuditID: auditID}
			encoded, _ := json.Marshal(result)
			_, err := tx.ExecContext(ctx, `INSERT INTO round_attempts
			  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
			   requests_reserved,items_reserved,tools_reserved,turns_reserved,result_json,created_at,updated_at,finished_at)
			  SELECT ?,?,?,?,?,?,generation,'succeeded',?,?,?,?,?,?,?,? FROM rounds WHERE id=?`,
				attemptID, roundID, input.RequestKey, digest, RoundCorrectEvidence, "evidence:"+input.PriorEvidenceID,
				cost.Requests, cost.Items, cost.Tools, cost.Turns, string(encoded), now, now, now, roundID)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO round_record_changes(round_id,attempt_id,audit_id,attached_at)
			  VALUES (?,?,?,?)`, roundID, attemptID, auditID, now)
			if err != nil {
				return err
			}
			id, err := randomID()
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO round_results(id,round_id,attempt_id,result_json,created_at)
			  VALUES (?,?,?,?,?)`, id, roundID, attemptID, string(encoded), now)
			return err
		})
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	result.AuditID = auditID
	return result, true, nil
}
