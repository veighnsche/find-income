package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// The pack service checks this before rendering; this check is repeated in
// the mutation writer transaction so a changed target cannot be committed.
func checkRoundPackCorrectionTx(ctx context.Context, tx *sql.Tx, round Round, input RoundMutationInput) error {
	pack := input.ApplicationPack
	var manifest struct {
		Correction *struct {
			PriorPackID                      string `json:"priorPackId"`
			PriorVersion                     int64  `json:"priorVersion"`
			PriorContentSHA256               string `json:"priorContentSha256"`
			OwnerInstructionID               string `json:"ownerInstructionId"`
			OwnerInstructionRequestKey       string `json:"ownerInstructionRequestKey"`
			OwnerInstructionExpectedRevision int64  `json:"ownerInstructionExpectedRevision"`
			OwnerInstructionText             string `json:"ownerInstructionText"`
		} `json:"correction"`
	}
	if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil {
		return ErrInvalid
	}
	if pack.PriorPackID == "" {
		if round.Outcome != "prepare" || input.OwnerInstructionID != "" || manifest.Correction != nil {
			return ErrFenced
		}
		return nil
	}
	if round.Outcome != "process_input" || !correctionOpportunityScope(round.Scope.Resources, pack.OpportunityID) ||
		!scopeHas(round.Scope.InputRefs, "instruction:"+input.OwnerInstructionID) || manifest.Correction == nil {
		return ErrFenced
	}
	var priorOpportunityID, priorSHA string
	var priorOpportunityRevision, priorProfileRevision, priorVersion int64
	err := tx.QueryRowContext(ctx, `SELECT opportunity_id,opportunity_revision,profile_revision,version,content_sha256
	  FROM application_packs WHERE id=?`, pack.PriorPackID).Scan(
		&priorOpportunityID, &priorOpportunityRevision, &priorProfileRevision, &priorVersion, &priorSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}
	if priorOpportunityID != pack.OpportunityID || priorOpportunityRevision != pack.ExpectedOpportunityRevision ||
		priorProfileRevision != pack.ExpectedProfileRevision {
		return ErrConflict
	}
	var instructionKind, instructionTargetID, instructionRequestKey, instructionText, instructionOwner, instructionRound string
	var instructionRevision int64
	var revoked sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT target_kind,target_id,expected_revision,request_key,text,actor_id,COALESCE(round_id,''),revoked_at
	  FROM owner_instructions WHERE id=?`, input.OwnerInstructionID).Scan(&instructionKind, &instructionTargetID,
		&instructionRevision, &instructionRequestKey, &instructionText, &instructionOwner, &instructionRound, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}
	if instructionKind != "application_pack" || instructionTargetID != pack.PriorPackID ||
		instructionRevision != priorVersion || instructionOwner != round.Actor.ID || revoked.Valid ||
		instructionRound != "" && instructionRound != round.ID {
		return ErrFenced
	}
	correction := manifest.Correction
	if correction.PriorPackID != pack.PriorPackID || correction.PriorVersion != priorVersion || correction.PriorContentSHA256 != priorSHA ||
		correction.OwnerInstructionID != input.OwnerInstructionID || correction.OwnerInstructionRequestKey != instructionRequestKey ||
		correction.OwnerInstructionExpectedRevision != instructionRevision || correction.OwnerInstructionText != instructionText {
		return ErrConflict
	}
	var applied int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM owner_instruction_applications app
	  JOIN audit_changes ac ON ac.id=app.audit_id
	  WHERE app.instruction_id=? AND ac.operation=?`, input.OwnerInstructionID, RoundPrepareApplicationPack).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return ErrConflict
	}
	var selectedRevision int64
	var decision string
	err = tx.QueryRowContext(ctx, `SELECT decision,opportunity_revision FROM owner_opportunity_decisions
	  WHERE opportunity_id=? ORDER BY revision DESC LIMIT 1`, pack.OpportunityID).Scan(&decision, &selectedRevision)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (decision != "selected" || selectedRevision != pack.ExpectedOpportunityRevision) {
		return ErrFenced
	}
	return err
}

func correctionOpportunityScope(resources []string, opportunityID string) bool {
	return len(resources) == 1 && resources[0] == "opportunity:"+opportunityID ||
		len(resources) == 2 && resources[0] == "opportunity:"+opportunityID && resources[1] == "campaign:active"
}
