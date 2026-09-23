package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

const (
	RoundCreateCompany         = "company.create"
	RoundCreateOpportunity     = "opportunity.create"
	RoundSaveSourceOpportunity = "opportunity.source_save"
	RoundCorrectPreferences    = "preferences.correct"
	RoundCorrectEvidence       = "evidence.correct"
	RoundCorrectOpportunity    = "opportunity.owner_correction"
	RoundFetchSource           = "source.fetch"
	RoundCollectorPage         = "source.page"
	RoundSearchSource          = "source.search"
	RoundCodexTurn             = "codex.turn"
	RoundContextTool           = "round.context"
	RoundJevRequest            = "jev.request"
)

// The caller chooses an operation, never its charge. Later connectors can add
// reviewed operations here without changing the reservation ledger.
func RoundOperationCost(operation string) (RoundAllowance, bool) {
	switch operation {
	case RoundCreateCompany, RoundCreateOpportunity, RoundSaveSourceOpportunity, RoundCorrectPreferences, RoundCorrectEvidence, RoundCorrectOpportunity:
		return RoundAllowance{Requests: 1, Items: 1, Tools: 1}, true
	case RoundFetchSource, RoundSearchSource:
		return RoundAllowance{Requests: 1, Tools: 1}, true
	case RoundCodexTurn:
		return RoundAllowance{Tools: 1, Turns: 1}, true
	case RoundContextTool:
		return RoundAllowance{Tools: 1}, true
	case RoundJevRequest:
		return RoundAllowance{Requests: 1}, true
	default:
		return RoundAllowance{}, false
	}
}

type RoundMutationInput struct {
	RequestKey         string                          `json:"requestKey"`
	Operation          string                          `json:"operation"`
	ResourceID         string                          `json:"resourceId"`
	ExpectedRevision   int64                           `json:"expectedRevision"`
	Company            *CompanyInput                   `json:"company,omitempty"`
	Opportunity        *OpportunityInput               `json:"opportunity,omitempty"`
	SourceOpportunity  *SourceOpportunityMutationInput `json:"sourceOpportunity,omitempty"`
	OwnerInstructionID string                          `json:"ownerInstructionId,omitempty"`
	Preferences        *Preferences                    `json:"preferences,omitempty"`
	OpportunityPatch   *OpportunityPatch               `json:"opportunityPatch,omitempty"`
	Capability         string                          `json:"-"`
}

type SourceOpportunityMutationInput struct {
	SourceOpeningID  string           `json:"sourceOpeningId"`
	ExpectedRevision int64            `json:"expectedRevision"`
	CompanyID        string           `json:"companyId"`
	Opportunity      OpportunityInput `json:"opportunity"`
}

type RoundMutationResult struct {
	AttemptID  string `json:"attemptId"`
	EntityKind string `json:"entityKind"`
	EntityID   string `json:"entityId"`
	Revision   int64  `json:"revision"`
	AuditID    string `json:"auditId"`
}

func roundMutationDigest(actor Actor, input RoundMutationInput) (string, error) {
	data, err := json.Marshal(struct {
		Actor     Actor
		Input     RoundMutationInput
		BoundHash string
	}{actor, input, func() string {
		if input.Capability == "" {
			return ""
		}
		return roundCapabilityHash(input.Capability)
	}()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validRoundMutation(input RoundMutationInput) bool {
	if input.RequestKey == "" || len(input.RequestKey) > 200 ||
		strings.TrimSpace(input.RequestKey) != input.RequestKey ||
		input.ResourceID == "" || len(input.ResourceID) > 300 ||
		input.ExpectedRevision < 1 {
		return false
	}
	switch input.Operation {
	case RoundCreateCompany:
		return input.Company != nil && input.Opportunity == nil && input.SourceOpportunity == nil && input.Preferences == nil && input.OpportunityPatch == nil && input.OwnerInstructionID == "" && input.ResourceID == "campaign:active"
	case RoundCreateOpportunity:
		return input.Opportunity != nil && input.Company == nil && input.SourceOpportunity == nil && input.Preferences == nil && input.OpportunityPatch == nil && input.OwnerInstructionID == "" &&
			input.ResourceID == "company:"+input.Opportunity.CompanyID
	case RoundSaveSourceOpportunity:
		return input.SourceOpportunity != nil && input.Company == nil && input.Opportunity == nil && input.Preferences == nil && input.OpportunityPatch == nil && input.OwnerInstructionID == "" &&
			input.SourceOpportunity.SourceOpeningID != "" && input.SourceOpportunity.CompanyID != "" &&
			input.SourceOpportunity.ExpectedRevision == input.ExpectedRevision &&
			input.ResourceID == "source-opening:"+input.SourceOpportunity.SourceOpeningID
	case RoundCorrectPreferences:
		return input.OwnerInstructionID != "" && input.Preferences != nil &&
			input.Company == nil && input.Opportunity == nil && input.SourceOpportunity == nil &&
			input.ResourceID == "profile:current"
	case RoundCorrectOpportunity:
		return input.OwnerInstructionID != "" && input.OpportunityPatch != nil &&
			input.Company == nil && input.Opportunity == nil && input.SourceOpportunity == nil && input.Preferences == nil &&
			input.OpportunityPatch.ExpectedRevision == input.ExpectedRevision &&
			strings.HasPrefix(input.ResourceID, "opportunity:") && len(input.ResourceID) > len("opportunity:")
	default:
		return false
	}
}

func scopeHas(items []string, item string) bool {
	for _, candidate := range items {
		if candidate == item {
			return true
		}
	}
	return false
}

func roundHasCompanyTx(ctx context.Context, tx *sql.Tx, round Round, companyID string) bool {
	if scopeHas(round.Scope.Resources, "company:"+companyID) {
		return true
	}
	var linked int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_record_changes rc
	  JOIN audit_changes ac ON ac.id=rc.audit_id WHERE rc.round_id=?
	  AND ac.entity_kind='company' AND ac.entity_id=?`, round.ID, companyID).Scan(&linked)
	return err == nil && linked > 0
}

// ApplyRoundMutation is the supported synchronous record-write boundary. The
// actor/scope/generation/revision check, server-owned charge, domain write,
// audit, attempt result and round change link commit or roll back together.
func (s *Store) ApplyRoundMutation(ctx context.Context, actor Actor, roundID string, input RoundMutationInput) (RoundMutationResult, bool, error) {
	if !validRoundMutation(input) || !requiredActor(actor) {
		return RoundMutationResult{}, false, ErrInvalid
	}
	if actor.Kind != "agent" || input.Capability == "" {
		return RoundMutationResult{}, false, ErrFenced
	}
	cost, allowed := RoundOperationCost(input.Operation)
	if !allowed {
		return RoundMutationResult{}, false, ErrInvalid
	}
	digest, err := roundMutationDigest(actor, input)
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	tx, round, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	defer tx.Rollback()
	authority, err := verifyRoundToolCapabilityTx(ctx, tx, input.Capability, roundID)
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	if authority.Actor != actor {
		return RoundMutationResult{}, false, ErrFenced
	}
	if !scopeAllowsActor(round.Scope, actor) {
		return RoundMutationResult{}, false, ErrFenced
	}
	if !scopeAllows(round.Scope, input.Operation, input.ResourceID) {
		// A company created and audited by this round becomes a scoped child
		// resource for its opportunity. An unrelated company never does.
		allowedOperation := false
		for _, operation := range round.Scope.Operations {
			if operation == input.Operation {
				allowedOperation = true
				break
			}
		}
		if !allowedOperation {
			return RoundMutationResult{}, false, ErrFenced
		}
		switch input.Operation {
		case RoundCreateOpportunity:
			if !roundHasCompanyTx(ctx, tx, round, input.Opportunity.CompanyID) {
				return RoundMutationResult{}, false, ErrFenced
			}
		case RoundSaveSourceOpportunity:
			if !roundHasCompanyTx(ctx, tx, round, input.SourceOpportunity.CompanyID) {
				return RoundMutationResult{}, false, ErrFenced
			}
			var staged int
			err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_sightings ss
			  JOIN source_openings so ON so.id=ss.source_opening_id AND so.current_ingestion_id=ss.ingestion_id
			  JOIN round_attempts a ON a.id=ss.collector_attempt_id
			  WHERE ss.source_opening_id=? AND a.round_id=? AND a.state='succeeded'`,
				input.SourceOpportunity.SourceOpeningID, roundID).Scan(&staged)
			if err != nil {
				return RoundMutationResult{}, false, err
			}
			if staged == 0 && !scopeHas(round.Scope.InputRefs, input.ResourceID) {
				return RoundMutationResult{}, false, ErrFenced
			}
		case RoundCorrectOpportunity:
			var companyID string
			err := tx.QueryRowContext(ctx, `SELECT company_id FROM opportunities WHERE id=? AND archived_at IS NULL`, strings.TrimPrefix(input.ResourceID, "opportunity:")).Scan(&companyID)
			if errors.Is(err, sql.ErrNoRows) {
				return RoundMutationResult{}, false, ErrNotFound
			}
			if err != nil {
				return RoundMutationResult{}, false, err
			}
			if !roundHasCompanyTx(ctx, tx, round, companyID) {
				return RoundMutationResult{}, false, ErrFenced
			}
		default:
			return RoundMutationResult{}, false, ErrFenced
		}
	}
	var oldDigest string
	var oldResult sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT request_sha256,result_json FROM round_attempts
  WHERE round_id=? AND request_key=?`, roundID, input.RequestKey).Scan(&oldDigest, &oldResult)
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
		return result, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RoundMutationResult{}, false, err
	}
	if err := requireRoundState(round, RoundRunning); err != nil {
		return RoundMutationResult{}, false, err
	}
	var profileVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&profileVersion); err != nil {
		return RoundMutationResult{}, false, err
	}
	if profileVersion != round.ProfileVersion {
		return RoundMutationResult{}, false, ErrConflict
	}
	if input.Operation == RoundCorrectPreferences {
		if input.ExpectedRevision != profileVersion {
			return RoundMutationResult{}, false, ErrConflict
		}
		var targetKind, targetID, instructionRound, instructionOwner string
		var expected int64
		var revoked sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT target_kind,target_id,expected_revision,COALESCE(round_id,''),revoked_at,actor_id
		  FROM owner_instructions WHERE id=?`, input.OwnerInstructionID).Scan(&targetKind, &targetID, &expected, &instructionRound, &revoked, &instructionOwner)
		if errors.Is(err, sql.ErrNoRows) {
			return RoundMutationResult{}, false, ErrFenced
		}
		if err != nil {
			return RoundMutationResult{}, false, err
		}
		if targetKind != "profile" || targetID != "current" || expected != profileVersion || revoked.Valid || instructionOwner != round.Actor.ID ||
			instructionRound != roundID && (instructionRound != "" || !scopeHas(round.Scope.InputRefs, "instruction:"+input.OwnerInstructionID)) {
			return RoundMutationResult{}, false, ErrFenced
		}
		current, err := scanPreferences(tx.QueryRowContext(ctx, `SELECT `+preferenceColumns+`
		  FROM preferences_versions p JOIN preferences_current c ON c.version=p.version WHERE c.singleton=1`))
		if err != nil {
			return RoundMutationResult{}, false, err
		}
		next := *input.Preferences
		next.Version, next.Actor, next.CreatedAt = current.Version, current.Actor, current.CreatedAt
		if reflect.DeepEqual(next, current) {
			return RoundMutationResult{}, false, ErrInvalid
		}
	}
	if input.Operation == RoundCorrectOpportunity {
		var kind, targetID, instructionRound, instructionOwner string
		var expected int64
		var revoked sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT target_kind,target_id,expected_revision,COALESCE(round_id,''),revoked_at,actor_id
		  FROM owner_instructions WHERE id=?`, input.OwnerInstructionID).Scan(&kind, &targetID, &expected, &instructionRound, &revoked, &instructionOwner)
		if errors.Is(err, sql.ErrNoRows) {
			return RoundMutationResult{}, false, ErrFenced
		}
		if err != nil {
			return RoundMutationResult{}, false, err
		}
		if kind != "opportunity" || "opportunity:"+targetID != input.ResourceID || expected != input.ExpectedRevision || revoked.Valid || instructionOwner != round.Actor.ID ||
			instructionRound != roundID && (instructionRound != "" || !scopeHas(round.Scope.InputRefs, "instruction:"+input.OwnerInstructionID)) {
			return RoundMutationResult{}, false, ErrFenced
		}
	}
	if input.Operation == RoundCreateCompany {
		if input.ExpectedRevision != profileVersion {
			return RoundMutationResult{}, false, ErrConflict
		}
	} else if input.Operation == RoundCreateOpportunity {
		var revision int64
		err = tx.QueryRowContext(ctx, `SELECT revision FROM companies WHERE id=? AND archived_at IS NULL`,
			input.Opportunity.CompanyID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			return RoundMutationResult{}, false, ErrNotFound
		}
		if err != nil {
			return RoundMutationResult{}, false, err
		}
		if revision != input.ExpectedRevision {
			return RoundMutationResult{}, false, ErrConflict
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE rounds SET requests_used=requests_used+?,
  items_used=items_used+?,tools_used=tools_used+?,turns_used=turns_used+?,
  revision=revision+1,updated_at=? WHERE id=? AND state='running' AND generation=?
  AND requests_used+?<=request_limit AND items_used+?<=item_limit
  AND tools_used+?<=tool_limit AND turns_used+?<=turn_limit`,
		cost.Requests, cost.Items, cost.Tools, cost.Turns, utcNow(), roundID, round.Generation,
		cost.Requests, cost.Items, cost.Tools, cost.Turns)
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	if count != 1 {
		return RoundMutationResult{}, false, ErrAllowance
	}
	attemptID, err := randomID()
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	var entityID, kind, auditID string
	var revision int64
	now := utcNow()
	if input.Operation == RoundSaveSourceOpportunity {
		entityID, revision, auditID, err = saveSourcedOpportunityTx(ctx, tx, actor, *input.SourceOpportunity)
		kind = "opportunity"
	} else if input.Operation == RoundCorrectPreferences {
		next := *input.Preferences
		next.Version, next.Actor, next.CreatedAt = profileVersion+1, actor, now
		if err = next.validate(); err == nil {
			err = insertPreferences(ctx, tx, next)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE preferences_current SET version=? WHERE singleton=1 AND version=?`, next.Version, profileVersion)
		}
		if err == nil {
			auditID, err = randomID()
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
			  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
			  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, input.Operation, "preferences",
				"current", profileVersion, next.Version, now)
		}
		entityID, kind, revision = "current", "preferences", next.Version
	} else if input.Operation == RoundCorrectOpportunity {
		entityID, kind = strings.TrimPrefix(input.ResourceID, "opportunity:"), "opportunity"
		revision, err = writeOwnerOpportunityCorrectionTx(ctx, tx, entityID, *input.OpportunityPatch)
	} else {
		entityID, kind, err = writeRoundRecordTx(ctx, tx, input)
		revision = 1
	}
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	if input.Operation != RoundSaveSourceOpportunity && input.Operation != RoundCorrectPreferences {
		auditID, err = randomID()
		if err != nil {
			return RoundMutationResult{}, false, err
		}
		if input.Operation == RoundCorrectOpportunity {
			_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
			  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
			  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, input.Operation, kind, entityID,
				input.ExpectedRevision, revision, now)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at)
  VALUES (?,?,?,?,?,?,1,?)`, auditID, actor.Kind, actor.ID, input.Operation, kind, entityID, now)
		}
		if err != nil {
			return RoundMutationResult{}, false, err
		}
	}
	if input.Operation == RoundCorrectPreferences || input.Operation == RoundCorrectOpportunity {
		if _, err = tx.ExecContext(ctx, `INSERT INTO owner_instruction_applications(audit_id,instruction_id) VALUES (?,?)`, auditID, input.OwnerInstructionID); err != nil {
			return RoundMutationResult{}, false, err
		}
	}
	result := RoundMutationResult{AttemptID: attemptID, EntityKind: kind, EntityID: entityID,
		Revision: revision, AuditID: auditID}
	resultJSON, _ := json.Marshal(result)
	_, err = tx.ExecContext(ctx, `INSERT INTO round_attempts
  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
   requests_reserved,items_reserved,tools_reserved,turns_reserved,result_json,
   created_at,updated_at,finished_at)
  VALUES (?,?,?,?,?,?,?,'succeeded',?,?,?,?,?,?,?,?)`, attemptID, roundID, input.RequestKey,
		digest, input.Operation, input.ResourceID, round.Generation,
		cost.Requests, cost.Items, cost.Tools, cost.Turns, string(resultJSON), now, now, now)
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO round_record_changes
  (round_id,attempt_id,audit_id,attached_at) VALUES (?,?,?,?)`, roundID, attemptID, auditID, now); err != nil {
		return RoundMutationResult{}, false, err
	}
	resultID, err := randomID()
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO round_results
  (id,round_id,attempt_id,result_json,created_at) VALUES (?,?,?,?,?)`, resultID, roundID,
		attemptID, string(resultJSON), now); err != nil {
		return RoundMutationResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return RoundMutationResult{}, false, err
	}
	return result, true, nil
}

func writeRoundRecordTx(ctx context.Context, tx *sql.Tx, input RoundMutationInput) (string, string, error) {
	id, err := randomID()
	if err != nil {
		return "", "", err
	}
	now := recordNow()
	switch input.Operation {
	case RoundCreateCompany:
		company, err := validateCompany(*input.Company)
		if err != nil {
			return "", "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO companies
  (id,name,website,notes,revision,created_at,updated_at)
  VALUES (?,?,?,?,1,?,?)`, id, company.Name, optionalText(company.Website), company.Notes, now, now)
		return id, "company", err
	case RoundCreateOpportunity:
		opening, err := validateOpportunity(*input.Opportunity)
		if err != nil {
			return "", "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO opportunities
  (id,company_id,title,kind,source_url,original_text,notes,stage,work_pattern,location_text,
   posted_on,deadline_on,revision,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, opening.CompanyID, opening.Title, opening.Kind,
			optionalText(opening.SourceURL), opening.OriginalText, opening.Notes, opening.Stage,
			opening.WorkPattern, opening.LocationText, optionalText(opening.PostedOn),
			optionalText(opening.DeadlineOn), now, now)
		if err != nil {
			return "", "", err
		}
		if err := writeAdvertisedCompensation(ctx, tx, id, opening.Compensation); err != nil {
			return "", "", err
		}
		return id, "opportunity", nil
	default:
		return "", "", fmt.Errorf("%w: unsupported round mutation", ErrInvalid)
	}
}

func writeOwnerOpportunityCorrectionTx(ctx context.Context, tx *sql.Tx, id string, patch OpportunityPatch) (int64, error) {
	if id == "" || patch.ExpectedRevision < 1 || patch.CompanyID != nil || patch.SourceURL != nil || patch.OriginalText != nil {
		return 0, ErrInvalid
	}
	current, err := scanOpportunity(tx.QueryRowContext(ctx, `SELECT `+opportunityColumns+opportunityFrom+` WHERE o.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if current.ArchivedAt != "" || current.Revision != patch.ExpectedRevision {
		return 0, ErrConflict
	}
	input := opportunityInputFromRecord(current)
	applyOpportunityPatch(&input, patch)
	input, err = validateOpportunity(input)
	if err != nil {
		return 0, err
	}
	before := opportunityInputFromRecord(current)
	if reflect.DeepEqual(input, before) {
		return 0, ErrInvalid
	}
	revision := current.Revision + 1
	updated, err := tx.ExecContext(ctx, `UPDATE opportunities SET title=?,kind=?,notes=?,stage=?,work_pattern=?,
	  location_text=?,posted_on=?,deadline_on=?,revision=?,updated_at=?
	  WHERE id=? AND revision=? AND archived_at IS NULL`, input.Title, input.Kind, input.Notes, input.Stage,
		input.WorkPattern, input.LocationText, optionalText(input.PostedOn), optionalText(input.DeadlineOn),
		revision, recordNow(), id, current.Revision)
	if err != nil {
		return 0, err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, ErrConflict
	}
	if err := writeAdvertisedCompensation(ctx, tx, id, input.Compensation); err != nil {
		return 0, err
	}
	return revision, nil
}
