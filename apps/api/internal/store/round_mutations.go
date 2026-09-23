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

const (
	RoundCreateCompany     = "company.create"
	RoundCreateOpportunity = "opportunity.create"
	RoundFetchSource       = "source.fetch"
	RoundCollectorPage     = "source.page"
	RoundSearchSource      = "source.search"
	RoundCodexTurn         = "codex.turn"
	RoundContextTool       = "round.context"
	RoundJevRequest        = "jev.request"
)

// The caller chooses an operation, never its charge. Later connectors can add
// reviewed operations here without changing the reservation ledger.
func RoundOperationCost(operation string) (RoundAllowance, bool) {
	switch operation {
	case RoundCreateCompany, RoundCreateOpportunity:
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
	RequestKey       string            `json:"requestKey"`
	Operation        string            `json:"operation"`
	ResourceID       string            `json:"resourceId"`
	ExpectedRevision int64             `json:"expectedRevision"`
	Company          *CompanyInput     `json:"company,omitempty"`
	Opportunity      *OpportunityInput `json:"opportunity,omitempty"`
	Capability       string            `json:"-"`
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
		return input.Company != nil && input.Opportunity == nil && input.ResourceID == "campaign:active"
	case RoundCreateOpportunity:
		return input.Opportunity != nil && input.Company == nil &&
			input.ResourceID == "company:"+input.Opportunity.CompanyID
	default:
		return false
	}
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
		if input.Operation != RoundCreateOpportunity || !allowedOperation {
			return RoundMutationResult{}, false, ErrFenced
		}
		var linked int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_record_changes rc
		  JOIN audit_changes ac ON ac.id=rc.audit_id WHERE rc.round_id=?
		  AND ac.entity_kind='company' AND ac.entity_id=?`, roundID, input.Opportunity.CompanyID).Scan(&linked)
		if err != nil {
			return RoundMutationResult{}, false, err
		}
		if linked == 0 {
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
	if input.Operation == RoundCreateCompany {
		if input.ExpectedRevision != profileVersion {
			return RoundMutationResult{}, false, ErrConflict
		}
	} else {
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
	entityID, kind, err := writeRoundRecordTx(ctx, tx, input)
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	auditID, err := randomID()
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at)
  VALUES (?,?,?,?,?,?,1,?)`, auditID, actor.Kind, actor.ID, input.Operation, kind, entityID, now)
	if err != nil {
		return RoundMutationResult{}, false, err
	}
	result := RoundMutationResult{AttemptID: attemptID, EntityKind: kind, EntityID: entityID,
		Revision: 1, AuditID: auditID}
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
