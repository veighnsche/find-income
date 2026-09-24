// Atomic research batch saves (lane D, T18): the records_save transaction
// body. One ResearchWrite (BEGIN IMMEDIATE) commits authority recheck,
// stable replay, capture/assessment/identity rechecks, allowance charge
// and all business writes — companies, opportunities, compensation,
// evidence snapshots, identity keys, sightings, identity decisions, audit
// rows, the attempt row and round links — or rolls everything back. No
// network or Jev call runs under the write lock; byte-dependent checks
// (excerpt hashes) are verified by the caller before the transaction and
// rebound here to the immutable capture rows by content sha.
//
// Match output stays advice until commit: every candidate revision pinned
// in an identity decision is re-read here, and any drift is a
// revision_conflict. Concurrent same-identity saves converge through the
// strong-key partial unique index: the loser observes the winner's
// committed key and reports identity_ambiguous instead of duplicating.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// RecordsSaveOperation and RecordsSaveResource identify batch-save attempt
// rows. The operation is descriptive only; the allowance charge is the sum
// of the per-item B-owned record-write costs read from RoundOperationCost.
const (
	RecordsSaveOperation = "records.save"
	RecordsSaveResource  = "records:batch"
)

// Transport bounds for one save batch. These cap a single call, never a
// run's total saved results.
const (
	MaxRecordsSaveItems       = 50
	MaxRecordsSaveEvidence    = 200
	MaxRecordsSaveAssessments = 20
)

// UnassessedStage labels opportunities saved without assessment backing.
// Assessed saves default to DiscoveredStage; an explicit stage field
// overrides either default on create and update alike.
const (
	UnassessedStage = "unassessed"
	DiscoveredStage = "discovered"
)

// VerifiedSaveCapture carries the byte-dependent facts the caller proved
// before the transaction (sha over stored bytes, span excerpt hashes).
// The transaction rebinds each entry to its immutable capture row by
// content sha and re-checks spans against the recorded byte length, so a
// row swap between verification and commit fails closed.
type VerifiedSaveCapture struct {
	ContentSHA256 string
	ByteLength    int64
	IsSnippet     bool
	Completeness  string
}

// RecordsSaveVerified is the pre-transaction proof bundle for one batch:
// the stable payload digest, the current brief versions, and the verified
// capture facts keyed by the exact link CaptureID string.
type RecordsSaveVerified struct {
	Digest         string
	ProfileVersion int64
	RubricVersion  string
	Captures       map[string]VerifiedSaveCapture
}

// saveItemDetail is the per-item audit detail persisted in the attempt
// result_json for T19/T31 explanation reads.
type saveItemDetail struct {
	Index       int                     `json:"index"`
	Op          researchcontract.SaveOp `json:"op"`
	RecordID    string                  `json:"recordId"`
	Revision    int64                   `json:"revision"`
	AuditID     string                  `json:"auditId"`
	Assessed    bool                    `json:"assessed"`
	Assessments []string                `json:"assessments,omitempty"`
	Sightings   []string                `json:"sightings,omitempty"`
	Keys        []string                `json:"keys,omitempty"`
	Decision    string                  `json:"decision,omitempty"`
}

// saveResultJSON is the persisted batch result behind replay.
type saveResultJSON struct {
	Saved []researchcontract.SavedRecord `json:"saved"`
	Items []saveItemDetail               `json:"items"`
}

// saveConflict carries item-specific conflicts out of the transaction; the
// write rolls back and the caller returns the output as a normal result.
type saveConflict struct{ out researchcontract.SaveOutput }

func (e *saveConflict) Error() string {
	if len(e.out.Items) > 0 {
		return fmt.Sprintf("records_save: %d item conflict(s), first: %s", len(e.out.Items), e.out.Items[0].Detail)
	}
	return fmt.Sprintf("records_save: outcome %s without item detail", e.out.Outcome)
}

// ResolveSourceCaptureRow loads a capture row by retrieval id or content
// sha256 (newest retrieval wins for content lookups), or ErrNotFound.
func ResolveSourceCaptureRow(ctx context.Context, r Reader, idOrSHA string) (SourceCapture, error) {
	if idOrSHA == "" {
		return SourceCapture{}, ErrNotFound
	}
	if capt, err := GetSourceCapture(ctx, r, idOrSHA); err == nil {
		return capt, nil
	} else if !errors.Is(err, ErrNotFound) {
		return SourceCapture{}, err
	}
	if len(idOrSHA) != 64 {
		return SourceCapture{}, ErrNotFound
	}
	for _, c := range idOrSHA {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return SourceCapture{}, ErrNotFound
		}
	}
	rows, err := ListSourceCapturesByContent(ctx, r, idOrSHA)
	if err != nil {
		return SourceCapture{}, err
	}
	if len(rows) == 0 {
		return SourceCapture{}, ErrNotFound
	}
	return rows[0], nil
}

// ApplyRecordsSave commits one records_save batch atomically. Batch-level
// failures (authority, replay conflict, malformed envelope) return errors;
// item-specific validation conflicts roll back and return a SaveOutput
// with per-item {index, code, detail, currentRevision}. A replay hit
// returns the prior saved IDs with outcome reused and charges nothing.
func (s *Store) ApplyRecordsSave(ctx context.Context, actor Actor, batch researchcontract.SaveBatch, verified RecordsSaveVerified) (researchcontract.SaveOutput, error) {
	if !requiredActor(actor) {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "actor", "actor required")
	}
	if batch.RunID == "" || batch.Generation < 1 {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "runId", "runId and generation >= 1 required")
	}
	if batch.IdempotencyKey == "" || len(batch.IdempotencyKey) > 200 ||
		strings.TrimSpace(batch.IdempotencyKey) != batch.IdempotencyKey {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"idempotencyKey", "idempotency key must be 1..200 chars without surrounding space")
	}
	if len(batch.Items) == 0 || len(batch.Items) > MaxRecordsSaveItems {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"batch", fmt.Sprintf("batch needs 1..%d items", MaxRecordsSaveItems))
	}
	if verified.Digest == "" || verified.ProfileVersion < 1 || verified.RubricVersion == "" {
		return researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"verified", "payload digest and current brief versions required")
	}
	var out researchcontract.SaveOutput
	err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if _, err := CheckRoundAuthorityTx(ctx, db, batch.RunID, batch.Generation,
			researchcontract.PermissionRecordWrite, actor, time.Now()); err != nil {
			return err
		}
		if replayed, replayOut, err := checkRecordsReplay(ctx, db, batch.RunID, batch.IdempotencyKey, verified.Digest); err != nil || replayed {
			if err == nil {
				out = replayOut
			}
			return err
		}
		plans, conflicts, err := validateRecordsBatch(ctx, db, batch, verified)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return &saveConflict{out: researchcontract.SaveOutput{Outcome: conflicts[0].Code, Items: conflicts}}
		}
		total, err := recordsSaveCost(batch)
		if err != nil {
			return err
		}
		if err := chargeRecordsSave(ctx, db, batch.RunID, batch.Generation, actor, total); err != nil {
			return err
		}
		saved, detail, err := executeRecordsBatch(ctx, db, actor, batch, plans)
		if err != nil {
			var itemErr *saveItemFailure
			if errors.As(err, &itemErr) {
				return &saveConflict{out: researchcontract.SaveOutput{
					Outcome: itemErr.item.Code, Items: []researchcontract.SaveItemError{itemErr.item}}}
			}
			return err
		}
		if err := persistRecordsReplay(ctx, db, batch, verified.Digest, total, saved, detail); err != nil {
			var conflict *researchcontract.Error
			if errors.As(err, &conflict) {
				return conflict
			}
			return err
		}
		out = researchcontract.SaveOutput{Outcome: researchcontract.OutcomeOK, Saved: saved}
		return nil
	})
	var sc *saveConflict
	if errors.As(err, &sc) {
		return sc.out, nil
	}
	if err != nil {
		return researchcontract.SaveOutput{}, err
	}
	return out, nil
}

// checkRecordsReplay resolves stable replay: same key + same payload
// returns the prior saved IDs; same key + different payload conflicts.
func checkRecordsReplay(ctx context.Context, db ResearchDB, runID, key, digest string) (bool, researchcontract.SaveOutput, error) {
	var priorDigest, state string
	var result sql.NullString
	err := db.QueryRowContext(ctx, `SELECT request_sha256,state,result_json FROM round_attempts
  WHERE round_id=? AND request_key=?`, runID, key).Scan(&priorDigest, &state, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return false, researchcontract.SaveOutput{}, nil
	}
	if err != nil {
		return false, researchcontract.SaveOutput{}, err
	}
	if priorDigest != digest {
		return false, researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeConflict,
			"idempotencyKey", fmt.Sprintf("key %q already saved a different payload; retry with a new key", key))
	}
	if state != string(AttemptSucceeded) || !result.Valid {
		return false, researchcontract.SaveOutput{}, researchcontract.NewError(researchcontract.OutcomeConflict,
			"idempotencyKey", fmt.Sprintf("key %q is held by a non-final attempt in state %s", key, state))
	}
	var stored saveResultJSON
	if err := json.Unmarshal([]byte(result.String), &stored); err != nil {
		return false, researchcontract.SaveOutput{}, err
	}
	ids := make([]string, 0, len(stored.Saved))
	for _, s := range stored.Saved {
		ids = append(ids, s.RecordID)
	}
	return true, researchcontract.SaveOutput{
		Outcome: researchcontract.OutcomeReused, Saved: stored.Saved, ReusedIDs: ids,
	}, nil
}

// recordsSaveCost sums the per-item B-owned record-write costs. Updates
// cost the same unit as creates: one durable record write each.
func recordsSaveCost(batch researchcontract.SaveBatch) (RoundAllowance, error) {
	var total RoundAllowance
	for _, item := range batch.Items {
		op := RoundCreateOpportunity
		if item.Op == researchcontract.SaveCreateCompany || item.Op == researchcontract.SaveUpdateCompany {
			op = RoundCreateCompany
		}
		cost, ok := RoundOperationCost(op)
		if !ok {
			return RoundAllowance{}, fmt.Errorf("records_save: no allowance cost for %s", op)
		}
		total.Requests += cost.Requests
		total.Items += cost.Items
		total.Tools += cost.Tools
		total.Turns += cost.Turns
	}
	return total, nil
}

// chargeRecordsSave atomically charges the batch cost under the running +
// current-generation predicate. A lost charge re-runs the authority check
// so the error reports exactly which bound failed.
func chargeRecordsSave(ctx context.Context, db ResearchDB, runID string, generation int64, actor Actor, total RoundAllowance) error {
	updated, err := db.ExecContext(ctx, `UPDATE rounds SET requests_used=requests_used+?,
  items_used=items_used+?,tools_used=tools_used+?,turns_used=turns_used+?,
  revision=revision+1,updated_at=? WHERE id=? AND state='running' AND generation=?
  AND requests_used+?<=request_limit AND items_used+?<=item_limit
  AND tools_used+?<=tool_limit AND turns_used+?<=turn_limit`,
		total.Requests, total.Items, total.Tools, total.Turns, utcNow(), runID, generation,
		total.Requests, total.Items, total.Tools, total.Turns)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	_, checkErr := CheckRoundAuthorityTx(ctx, db, runID, generation,
		researchcontract.PermissionRecordWrite, actor, time.Now())
	if checkErr != nil {
		return checkErr
	}
	return researchcontract.NewError(researchcontract.OutcomeBudgetExhausted,
		string(researchcontract.AuthorityAllowance), "allowance exhausted by a concurrent charge")
}

// persistRecordsReplay stores the attempt row, round result and per-audit
// round links. A concurrent same-key insert (only possible across separate
// writer paths) falls back to a re-read: same digest replays, else conflict.
func persistRecordsReplay(ctx context.Context, db ResearchDB, batch researchcontract.SaveBatch, digest string,
	total RoundAllowance, saved []researchcontract.SavedRecord, detail []saveItemDetail) error {
	resultRaw, err := json.Marshal(saveResultJSON{Saved: saved, Items: detail})
	if err != nil {
		return err
	}
	var generation int64
	if err := db.QueryRowContext(ctx, `SELECT generation FROM rounds WHERE id=?`, batch.RunID).Scan(&generation); err != nil {
		return err
	}
	attemptID, err := randomID()
	if err != nil {
		return err
	}
	now := utcNow()
	_, err = db.ExecContext(ctx, `INSERT INTO round_attempts
  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
   requests_reserved,items_reserved,tools_reserved,turns_reserved,result_json,
   created_at,updated_at,finished_at)
  VALUES (?,?,?,?,?,?,?,'succeeded',?,?,?,?,?,?,?,?)`, attemptID, batch.RunID, batch.IdempotencyKey,
		digest, RecordsSaveOperation, RecordsSaveResource, generation,
		total.Requests, total.Items, total.Tools, total.Turns, string(resultRaw), now, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: round_attempts") {
			replayed, replayOut, rerr := checkRecordsReplay(ctx, db, batch.RunID, batch.IdempotencyKey, digest)
			if rerr != nil {
				return rerr
			}
			if replayed {
				return &saveConflict{out: replayOut}
			}
		}
		return err
	}
	resultID, err := randomID()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO round_results
  (id,round_id,attempt_id,result_json,created_at) VALUES (?,?,?,?,?)`,
		resultID, batch.RunID, attemptID, string(resultRaw), now); err != nil {
		return err
	}
	for _, s := range saved {
		if _, err := db.ExecContext(ctx, `INSERT INTO round_record_changes
  (round_id,attempt_id,audit_id,attached_at) VALUES (?,?,?,?)`,
			batch.RunID, attemptID, s.AuditID, now); err != nil {
			return err
		}
	}
	return nil
}

// saveItemFailure wraps one item conflict raised during the write phase so
// the transaction rolls back and the caller reports it item-specifically.
type saveItemFailure struct {
	item researchcontract.SaveItemError
}

func (e *saveItemFailure) Error() string {
	return fmt.Sprintf("records_save item %d %s: %s", e.item.Index, e.item.Code, e.item.Detail)
}

func saveItemErr(index int, code researchcontract.Outcome, detail string, currentRevision *int64) researchcontract.SaveItemError {
	return researchcontract.SaveItemError{Index: index, Code: code, Detail: detail, CurrentRevision: currentRevision}
}

func saveItemErrPtr(index int, code researchcontract.Outcome, detail string, currentRevision int64) *researchcontract.SaveItemError {
	out := saveItemErr(index, code, detail, &currentRevision)
	return &out
}

// Field vocabularies per record kind. Unknown fields are invalid: a typo
// must fail loudly, never silently drop an identity signal.
var recordsCompanyFields = map[string]bool{
	"name": true, "website": true, "notes": true, "reusedIdentifier": true,
}

var recordsOpportunityFields = map[string]bool{
	"companyId": true, "title": true, "kind": true, "sourceUrl": true,
	"originalText": true, "notes": true, "stage": true, "workPattern": true,
	"locationText": true, "postedOn": true, "deadlineOn": true,
	"requisitionId": true, "reqIssuer": true,
	"boardProvider": true, "board": true, "boardRecordId": true,
	"employerDomain": true, "reusedIdentifier": true, "vacancyComplete": true,
	"compCurrency": true, "compMinCents": true, "compMaxCents": true,
	"compPeriod": true, "compRefHoursHundredths": true, "compBasis": true,
	"compBenefitsText": true, "compAnnualConversion": true,
	"compAnnualConvSpanStart": true, "compAnnualConvSpanEnd": true,
}

type saveKeyRef struct{ namespace, value string }
type saveRecordRef struct{ kind, id string }

type plannedEvidence struct {
	rowID        string
	contentSHA   string
	spanStart    int64
	spanEnd      int64
	sightingKind string
}

type plannedKey struct {
	namespace    string
	value        string
	strength     string
	entityKind   string
	supersedeOld string
}

// savePlan is one fully validated item: every recheck passed and the write
// inputs are built. Validation covers the whole batch before any write so
// one bad item reports alongside the rest and nothing commits partially.
type savePlan struct {
	index            int
	op               researchcontract.SaveOp
	kind             string // company|opportunity
	create           bool
	recordID         string
	expectedRevision int64
	company          CompanyInput
	opportunity      OpportunityInput
	companyRef       int // batch:ref target index, -1 when explicit/absent
	evidence         []plannedEvidence
	evidenceSHA      map[string]bool
	assessmentIDs    []string
	decision         *researchcontract.IdentityDecision
	subjectKind      string // employer|vacancy
	keys             []plannedKey
	reuse            bool
	assessed         bool
	newRevision      int64
}

func validateRecordsBatch(ctx context.Context, db ResearchDB, batch researchcontract.SaveBatch, verified RecordsSaveVerified) ([]*savePlan, []researchcontract.SaveItemError, error) {
	plans := make([]*savePlan, len(batch.Items))
	var conflicts []researchcontract.SaveItemError
	pendingKeys := map[saveKeyRef]string{}
	pendingRevision := map[saveRecordRef]int64{}
	createdKind := map[int]string{}
	for i := range batch.Items {
		plan, errs, err := validateSaveItem(ctx, db, batch, verified, i, pendingKeys, pendingRevision, createdKind)
		if err != nil {
			return nil, nil, err
		}
		if len(errs) > 0 {
			conflicts = append(conflicts, errs...)
			continue
		}
		plans[i] = plan
		if plan.create {
			createdKind[i] = plan.kind
		}
	}
	return plans, conflicts, nil
}

func validateSaveItem(ctx context.Context, db ResearchDB, batch researchcontract.SaveBatch, verified RecordsSaveVerified,
	itemIndex int, pendingKeys map[saveKeyRef]string, pendingRevision map[saveRecordRef]int64,
	createdKind map[int]string) (*savePlan, []researchcontract.SaveItemError, error) {
	item := batch.Items[itemIndex]
	invalid := func(detail string) []researchcontract.SaveItemError {
		return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid, detail, nil)}
	}
	plan := &savePlan{index: itemIndex, op: item.Op, companyRef: -1, evidenceSHA: map[string]bool{}}
	switch item.Op {
	case researchcontract.SaveCreateCompany:
		plan.kind, plan.create, plan.subjectKind = "company", true, IdentitySubjectEmployer
	case researchcontract.SaveUpdateCompany:
		plan.kind, plan.create, plan.subjectKind = "company", false, IdentitySubjectEmployer
	case researchcontract.SaveCreateOpportunity:
		plan.kind, plan.create, plan.subjectKind = "opportunity", true, IdentitySubjectVacancy
	case researchcontract.SaveUpdateOpportunity:
		plan.kind, plan.create, plan.subjectKind = "opportunity", false, IdentitySubjectVacancy
	default:
		return nil, invalid(fmt.Sprintf("unknown op %q", item.Op)), nil
	}
	if plan.create {
		if item.RecordID != "" {
			return nil, invalid("recordId is server-assigned on create"), nil
		}
		if item.ExpectedRevision != 0 {
			return nil, invalid("expectedRevision is for updates, not creates"), nil
		}
	} else {
		if item.RecordID == "" || item.ExpectedRevision < 1 {
			return nil, invalid("update needs recordId and expectedRevision >= 1"), nil
		}
		plan.recordID, plan.expectedRevision = item.RecordID, item.ExpectedRevision
	}
	allowed := recordsCompanyFields
	if plan.kind == "opportunity" {
		allowed = recordsOpportunityFields
	}
	for key := range item.Fields {
		if !allowed[key] {
			return nil, invalid(fmt.Sprintf("unknown field %q for %s", key, item.Op)), nil
		}
	}
	if len(item.EvidenceLinks) == 0 || len(item.EvidenceLinks) > MaxRecordsSaveEvidence {
		return nil, invalid(fmt.Sprintf("item needs 1..%d evidence links", MaxRecordsSaveEvidence)), nil
	}
	if len(item.AssessmentIDs) > MaxRecordsSaveAssessments {
		return nil, invalid(fmt.Sprintf("item needs at most %d assessments", MaxRecordsSaveAssessments)), nil
	}
	plan.assessed = len(item.AssessmentIDs) > 0
	plan.assessmentIDs = append([]string(nil), item.AssessmentIDs...)
	plan.decision = item.IdentityDecision
	complete, reuse, err := parseSaveFlags(item.Fields)
	if err != nil {
		return nil, invalid(err.Error()), nil
	}
	if complete && plan.kind != "opportunity" {
		return nil, invalid("vacancyComplete is opportunity-only"), nil
	}
	plan.reuse = reuse
	for _, link := range item.EvidenceLinks {
		proved, ok := verified.Captures[link.CaptureID]
		if !ok {
			return nil, invalid(fmt.Sprintf("capture %q was not verified before the transaction", link.CaptureID)), nil
		}
		row, err := ResolveSourceCaptureRow(ctx, db, link.CaptureID)
		if errors.Is(err, ErrNotFound) {
			return nil, invalid(fmt.Sprintf("unknown capture %q", link.CaptureID)), nil
		}
		if err != nil {
			return nil, nil, err
		}
		if row.ContentSHA256 != proved.ContentSHA256 || row.IsSnippet != proved.IsSnippet ||
			row.Completeness != proved.Completeness || row.ByteLength != proved.ByteLength {
			return nil, invalid(fmt.Sprintf("capture %q binding changed under verification", link.CaptureID)), nil
		}
		if link.SpanStart < 0 || link.SpanEnd <= link.SpanStart || link.SpanEnd > row.ByteLength {
			return nil, invalid(fmt.Sprintf("span [%d,%d) exceeds capture %q (%d bytes)",
				link.SpanStart, link.SpanEnd, link.CaptureID, row.ByteLength)), nil
		}
		if complete && (row.IsSnippet || row.Completeness != CaptureComplete) {
			return nil, []researchcontract.SaveItemError{saveItemErr(itemIndex,
				researchcontract.OutcomeCaptureIncomplete,
				fmt.Sprintf("complete-vacancy claim needs complete captures; %q is snippet=%v completeness=%s",
					link.CaptureID, row.IsSnippet, row.Completeness), nil)}, nil
		}
		plan.evidence = append(plan.evidence, plannedEvidence{
			rowID: row.ID, contentSHA: row.ContentSHA256,
			spanStart: link.SpanStart, spanEnd: link.SpanEnd,
		})
		plan.evidenceSHA[row.ContentSHA256] = true
	}
	if errs, err := recheckSaveAssessments(ctx, db, itemIndex, plan, verified); err != nil {
		return nil, nil, err
	} else if len(errs) > 0 {
		return nil, errs, nil
	}
	if errs, err := recheckSaveDecision(ctx, db, itemIndex, plan); err != nil {
		return nil, nil, err
	} else if len(errs) > 0 {
		return nil, errs, nil
	}
	if itemErr, err := planSaveRecord(ctx, db, itemIndex, plan, batch, createdKind, pendingRevision); err != nil {
		return nil, nil, err
	} else if itemErr != nil {
		return nil, []researchcontract.SaveItemError{*itemErr}, nil
	}
	keys, itemErr, err := deriveSaveKeys(itemIndex, plan, item.Fields)
	if err != nil {
		return nil, nil, err
	}
	if itemErr != nil {
		return nil, []researchcontract.SaveItemError{*itemErr}, nil
	}
	kept, errs, err := recheckSaveKeys(ctx, db, itemIndex, plan, keys, pendingKeys)
	if err != nil {
		return nil, nil, err
	}
	if len(errs) > 0 {
		return nil, errs, nil
	}
	plan.keys = kept
	if err := classifySaveSightings(ctx, db, plan); err != nil {
		return nil, nil, err
	}
	if !plan.create {
		pendingRevision[saveRecordRef{plan.kind, plan.recordID}] = plan.newRevision
	}
	return plan, nil, nil
}

// parseSaveFlags reads the boolean signal fields.
func parseSaveFlags(fields map[string]string) (complete, reuse bool, err error) {
	for key, target := range map[string]*bool{"vacancyComplete": &complete, "reusedIdentifier": &reuse} {
		raw, ok := fields[key]
		if !ok || raw == "" {
			continue
		}
		if raw == "true" {
			*target = true
			continue
		}
		if raw == "false" {
			continue
		}
		return false, false, fmt.Errorf("%w: field %q must be true|false", ErrInvalid, key)
	}
	return complete, reuse, nil
}

// recheckSaveAssessments enforces assessment binding: the row exists and
// ended usable, its brief is still current (else stale), and every capture
// it binds is cited by this item (rebinding to different evidence is a
// revision_conflict).
func recheckSaveAssessments(ctx context.Context, db ResearchDB, itemIndex int, plan *savePlan, verified RecordsSaveVerified) ([]researchcontract.SaveItemError, error) {
	for _, id := range plan.assessmentIDs {
		row, err := GetDynamicAssessment(ctx, db, id)
		if errors.Is(err, ErrNotFound) {
			return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
				fmt.Sprintf("unknown assessment %q", id), nil)}, nil
		}
		if err != nil {
			return nil, err
		}
		if row.Status != DynamicAssessmentSucceeded && row.Status != DynamicAssessmentPartialAbstain {
			return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
				fmt.Sprintf("assessment %q ended %s; only succeeded/partial_abstain support saves", id, row.Status), nil)}, nil
		}
		if row.ProfileVersion != verified.ProfileVersion || row.RubricVersion != verified.RubricVersion {
			return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeStale,
				fmt.Sprintf("assessment %q judged brief (profile %d, rubric %s); current is (profile %d, rubric %s): reassess under the current brief",
					id, row.ProfileVersion, row.RubricVersion, verified.ProfileVersion, verified.RubricVersion), nil)}, nil
		}
		links, err := ListAssessmentCaptures(ctx, db, id)
		if err != nil {
			return nil, err
		}
		if len(links) == 0 {
			return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
				fmt.Sprintf("assessment %q binds no evidence", id), nil)}, nil
		}
		bound := map[string]bool{}
		for _, link := range links {
			capt, err := ResolveSourceCaptureRow(ctx, db, link.CaptureID)
			if errors.Is(err, ErrNotFound) {
				return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
					fmt.Sprintf("assessment %q binds unknown capture %q", id, link.CaptureID), nil)}, nil
			}
			if err != nil {
				return nil, err
			}
			bound[capt.ContentSHA256] = true
		}
		for _, ref := range parseAssessmentEvidenceRefs(row.EvidenceRefsJSON) {
			capt, err := ResolveSourceCaptureRow(ctx, db, ref)
			if errors.Is(err, ErrNotFound) {
				return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
					fmt.Sprintf("assessment %q binds unknown capture %q", id, ref), nil)}, nil
			}
			if err != nil {
				return nil, err
			}
			bound[capt.ContentSHA256] = true
		}
		for sha := range bound {
			if !plan.evidenceSHA[sha] {
				return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeRevisionConflict,
					fmt.Sprintf("assessment %q binds evidence this item does not cite; assessments cannot be rebound to different evidence", id), nil)}, nil
			}
		}
	}
	return nil, nil
}

// parseAssessmentEvidenceRefs extracts capture ids from an assessment's
// evidence_refs_json, tolerating both the contract camelCase shape and the
// handler snake_case shape. Unparseable JSON yields no ids; the link table
// (checked separately) stays authoritative for spans.
func parseAssessmentEvidenceRefs(raw string) []string {
	var refs []struct {
		CaptureID    string `json:"captureId"`
		CaptureIDAlt string `json:"capture_id"`
	}
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil
	}
	var out []string
	for _, ref := range refs {
		id := ref.CaptureID
		if id == "" {
			id = ref.CaptureIDAlt
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// recheckSaveDecision recomputes the candidate set over live revisions; any
// drift since match/assess is a revision_conflict. decision=same requires
// an update whose target sits in the candidate set; unresolved never saves.
func recheckSaveDecision(ctx context.Context, db ResearchDB, itemIndex int, plan *savePlan) ([]researchcontract.SaveItemError, error) {
	dec := plan.decision
	if dec == nil {
		return nil, nil
	}
	if dec.Decision != IdentityDecisionSame && dec.Decision != IdentityDecisionNew &&
		dec.Decision != IdentityDecisionUnresolved {
		return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
			fmt.Sprintf("decision must be same|new|unresolved, got %q", dec.Decision), nil)}, nil
	}
	if _, _, err := CanonicalCandidateSet(dec.Candidates); err != nil {
		return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
			"decision candidates need candidateId, kind company|opportunity and revision >= 1", nil)}, nil
	}
	var conflicts []researchcontract.SaveItemError
	live := make([]researchcontract.CandidateIdentity, 0, len(dec.Candidates))
	for _, cand := range dec.Candidates {
		current, err := GetIdentityCandidate(ctx, db, cand.Kind, cand.CandidateID)
		if errors.Is(err, ErrNotFound) {
			conflicts = append(conflicts, saveItemErr(itemIndex, researchcontract.OutcomeRevisionConflict,
				fmt.Sprintf("candidate %s %q no longer exists", cand.Kind, cand.CandidateID), nil))
			continue
		}
		if err != nil {
			return nil, err
		}
		live = append(live, researchcontract.CandidateIdentity{
			CandidateID: cand.CandidateID, Kind: cand.Kind, Revision: current.Revision,
		})
		if current.Revision != cand.Revision {
			conflicts = append(conflicts, *saveItemErrPtr(itemIndex, researchcontract.OutcomeRevisionConflict,
				fmt.Sprintf("candidate %s %q moved revision %d -> %d since the decision",
					cand.Kind, cand.CandidateID, cand.Revision, current.Revision), current.Revision))
		}
	}
	if len(conflicts) > 0 {
		return conflicts, nil
	}
	if dec.Decision == IdentityDecisionUnresolved {
		return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeIdentityAmbiguous,
			"identity is unresolved; keep the discovery in research memory instead of saving", nil)}, nil
	}
	if dec.Decision == IdentityDecisionSame {
		if plan.create {
			return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
				"decision=same needs an update of its subject, not a create", nil)}, nil
		}
		found := false
		for _, cand := range dec.Candidates {
			if cand.Kind == plan.kind && cand.CandidateID == plan.recordID {
				found = true
				break
			}
		}
		if !found {
			return []researchcontract.SaveItemError{saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
				fmt.Sprintf("decision=same target %s %q is not in the candidate set", plan.kind, plan.recordID), nil)}, nil
		}
	}
	return nil, nil
}

// planSaveRecord builds the validated record write: revision fencing for
// updates (archived or moved records are revision_conflicts), company
// resolution for opportunity creates (explicit ids or batch:ref), and full
// domain validation of the merged inputs.
func planSaveRecord(ctx context.Context, db ResearchDB, itemIndex int, plan *savePlan, batch researchcontract.SaveBatch,
	createdKind map[int]string, pendingRevision map[saveRecordRef]int64) (*researchcontract.SaveItemError, error) {
	item := batch.Items[itemIndex]
	fields := item.Fields
	invalid := func(detail string) (*researchcontract.SaveItemError, error) {
		out := saveItemErr(itemIndex, researchcontract.OutcomeInvalid, detail, nil)
		return &out, nil
	}
	conflict := func(detail string, current int64) (*researchcontract.SaveItemError, error) {
		return saveItemErrPtr(itemIndex, researchcontract.OutcomeRevisionConflict, detail, current), nil
	}
	if !plan.create {
		if pending, ok := pendingRevision[saveRecordRef{plan.kind, plan.recordID}]; ok {
			return conflict(fmt.Sprintf("%s %q is already updated by this batch (pending revision %d)",
				plan.kind, plan.recordID, pending), pending-1)
		}
	}
	switch {
	case plan.kind == "company" && plan.create:
		input := CompanyInput{Name: fields["name"], Website: fields["website"], Notes: fields["notes"]}
		validated, err := validateCompany(input)
		if err != nil {
			return invalid(err.Error())
		}
		plan.company = validated
		plan.newRevision = 1
	case plan.kind == "company":
		current, err := scanCompany(db.QueryRowContext(ctx, `SELECT `+companyColumns+` FROM companies WHERE id=?`, plan.recordID))
		if errors.Is(err, sql.ErrNoRows) {
			return invalid(fmt.Sprintf("unknown company %q", plan.recordID))
		}
		if err != nil {
			return nil, err
		}
		if current.ArchivedAt != "" || current.Revision != plan.expectedRevision {
			return conflict(fmt.Sprintf("company %q is at revision %d (archived=%v), expected %d",
				plan.recordID, current.Revision, current.ArchivedAt != "", plan.expectedRevision), current.Revision)
		}
		input := CompanyInput{Name: current.Name, Website: current.Website, Notes: current.Notes}
		if v, ok := fields["name"]; ok {
			input.Name = v
		}
		if v, ok := fields["website"]; ok {
			input.Website = v
		}
		if v, ok := fields["notes"]; ok {
			input.Notes = v
		}
		validated, err := validateCompany(input)
		if err != nil {
			return invalid(err.Error())
		}
		plan.company = validated
		plan.newRevision = current.Revision + 1
	case plan.kind == "opportunity" && plan.create:
		companyID, ref, itemErr, err := resolveSaveCompany(itemIndex, fields, createdKind)
		if err != nil || itemErr != nil {
			return itemErr, err
		}
		// Batch-referenced companies resolve to real ids at execution; a
		// placeholder keeps domain validation honest about the other fields.
		validatedCompanyID := companyID
		if ref >= 0 {
			validatedCompanyID = "batch-pending"
		}
		if ref < 0 {
			var archived sql.NullString
			if err := db.QueryRowContext(ctx, `SELECT archived_at FROM companies WHERE id=?`,
				companyID).Scan(&archived); errors.Is(err, sql.ErrNoRows) {
				return invalid(fmt.Sprintf("unknown company %q", companyID))
			} else if err != nil {
				return nil, err
			} else if archived.Valid && archived.String != "" {
				return invalid(fmt.Sprintf("company %q is archived", companyID))
			}
			plan.opportunity.CompanyID = companyID
		} else {
			plan.companyRef = ref
		}
		stage := fields["stage"]
		if stage == "" {
			stage = UnassessedStage
			if plan.assessed {
				stage = DiscoveredStage
			}
		}
		kind := fields["kind"]
		if kind == "" {
			kind = "employment"
		}
		comp, _, err := parseSaveComp(itemIndex, fields, defaultCompensation())
		if err != nil {
			var failure *saveItemFailure
			if errors.As(err, &failure) {
				return &failure.item, nil
			}
			return nil, err
		}
		input := OpportunityInput{
			CompanyID: validatedCompanyID, Title: fields["title"], Kind: kind,
			SourceURL: fields["sourceUrl"], OriginalText: fields["originalText"],
			Notes: fields["notes"], Stage: stage, WorkPattern: fields["workPattern"],
			LocationText: fields["locationText"], PostedOn: fields["postedOn"],
			DeadlineOn: fields["deadlineOn"], Compensation: comp,
		}
		validated, err := validateOpportunity(input)
		if err != nil {
			return invalid(err.Error())
		}
		plan.opportunity = validated
		plan.newRevision = 1
	default: // update_opportunity
		if _, ok := fields["companyId"]; ok {
			return invalid("company reassignment is not a research save; re-match and save under the right employer")
		}
		current, err := scanOpportunity(db.QueryRowContext(ctx, opportunityByID, plan.recordID))
		if errors.Is(err, sql.ErrNoRows) {
			return invalid(fmt.Sprintf("unknown opportunity %q", plan.recordID))
		}
		if err != nil {
			return nil, err
		}
		if current.ArchivedAt != "" || current.Revision != plan.expectedRevision {
			return conflict(fmt.Sprintf("opportunity %q is at revision %d (archived=%v), expected %d",
				plan.recordID, current.Revision, current.ArchivedAt != "", plan.expectedRevision), current.Revision)
		}
		input := opportunityInputFromRecord(current)
		for key, target := range map[string]*string{
			"title": &input.Title, "kind": &input.Kind, "sourceUrl": &input.SourceURL,
			"originalText": &input.OriginalText, "notes": &input.Notes, "stage": &input.Stage,
			"workPattern": &input.WorkPattern, "locationText": &input.LocationText,
			"postedOn": &input.PostedOn, "deadlineOn": &input.DeadlineOn,
		} {
			if v, ok := fields[key]; ok {
				*target = v
			}
		}
		comp, provided, err := parseSaveComp(itemIndex, fields, current.Compensation)
		if err != nil {
			var failure *saveItemFailure
			if errors.As(err, &failure) {
				return &failure.item, nil
			}
			return nil, err
		}
		if input.OriginalText != current.OriginalText && !provided && current.Compensation.AnnualConversion != "" {
			return invalid("changed source text requires annual conversion reattestation")
		}
		input.Compensation = comp
		validated, err := validateOpportunity(input)
		if err != nil {
			return invalid(err.Error())
		}
		plan.opportunity = validated
		plan.newRevision = current.Revision + 1
	}
	return nil, nil
}

// resolveSaveCompany resolves an opportunity create's company: an explicit
// id, or batch:<index> referencing an earlier create_company item.
func resolveSaveCompany(itemIndex int, fields map[string]string, createdKind map[int]string) (string, int, *researchcontract.SaveItemError, error) {
	raw := fields["companyId"]
	if raw == "" {
		out := saveItemErr(itemIndex, researchcontract.OutcomeInvalid, "companyId required", nil)
		return "", -1, &out, nil
	}
	if !strings.HasPrefix(raw, "batch:") {
		return raw, -1, nil, nil
	}
	ref, err := strconv.Atoi(strings.TrimPrefix(raw, "batch:"))
	if err != nil || ref < 0 || ref >= itemIndex {
		out := saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
			fmt.Sprintf("companyId %q must reference an earlier batch item", raw), nil)
		return "", -1, &out, nil
	}
	if createdKind[ref] != "company" {
		out := saveItemErr(itemIndex, researchcontract.OutcomeInvalid,
			fmt.Sprintf("companyId %q does not reference a validated create_company item", raw), nil)
		return "", -1, &out, nil
	}
	return "", ref, nil, nil
}

// parseSaveComp merges compensation string fields over a base value.
// Empty numeric strings count as absent; provided reports whether any
// compensation field was present (annual-conversion reattestation).
func parseSaveComp(itemIndex int, fields map[string]string, base AdvertisedCompensation) (AdvertisedCompensation, bool, error) {
	fail := func(detail string) (AdvertisedCompensation, bool, error) {
		return AdvertisedCompensation{}, false,
			&saveItemFailure{item: saveItemErr(itemIndex, researchcontract.OutcomeInvalid, detail, nil)}
	}
	out := base
	provided := false
	intField := func(key string, target **int64) error {
		raw, ok := fields[key]
		if !ok || raw == "" {
			return nil
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("field %q must be an integer", key)
		}
		provided = true
		*target = &n
		return nil
	}
	if v, ok := fields["compCurrency"]; ok {
		provided = true
		out.Currency = v
	}
	if err := intField("compMinCents", &out.MinAmountCents); err != nil {
		return fail(err.Error())
	}
	if err := intField("compMaxCents", &out.MaxAmountCents); err != nil {
		return fail(err.Error())
	}
	if v, ok := fields["compPeriod"]; ok {
		provided = true
		out.Period = v
	}
	if err := intField("compRefHoursHundredths", &out.ReferenceHoursHundredths); err != nil {
		return fail(err.Error())
	}
	if v, ok := fields["compBasis"]; ok {
		provided = true
		out.Basis = v
	}
	if v, ok := fields["compBenefitsText"]; ok {
		provided = true
		out.BenefitsText = v
	}
	if v, ok := fields["compAnnualConversion"]; ok {
		provided = true
		out.AnnualConversion = v
		if v == "" {
			out.AnnualConversionSpanStart, out.AnnualConversionSpanEnd = nil, nil
			out.AnnualConversionExcerpt, out.AnnualConversionSHA256 = "", ""
		}
	}
	intPtrField := func(key string, target **int) error {
		raw, ok := fields[key]
		if !ok || raw == "" {
			return nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("field %q must be an integer", key)
		}
		provided = true
		*target = &n
		return nil
	}
	if err := intPtrField("compAnnualConvSpanStart", &out.AnnualConversionSpanStart); err != nil {
		return fail(err.Error())
	}
	if err := intPtrField("compAnnualConvSpanEnd", &out.AnnualConversionSpanEnd); err != nil {
		return fail(err.Error())
	}
	return out, provided, nil
}

// deriveSaveKeys derives the identity keys one item establishes: a strong
// canonical URL / req-id / board record per vacancy, a strong employer
// domain per company website, and an alias employer domain per explicit
// vacancy field. Strength is server-derived, never caller-chosen.
func deriveSaveKeys(itemIndex int, plan *savePlan, fields map[string]string) ([]plannedKey, *researchcontract.SaveItemError, error) {
	invalid := func(detail string) ([]plannedKey, *researchcontract.SaveItemError, error) {
		out := saveItemErr(itemIndex, researchcontract.OutcomeInvalid, detail, nil)
		return nil, &out, nil
	}
	switch plan.kind {
	case "company":
		if plan.company.Website != "" {
			host := IdentityURLHost(plan.company.Website)
			if host == "" {
				return invalid("company website host is not a usable employer domain")
			}
			return []plannedKey{{namespace: IdentityNamespaceEmployerDomain, value: host,
				strength: IdentityKeyStrong, entityKind: IdentitySubjectEmployer}}, nil, nil
		}
		return nil, nil, nil
	default:
		opp := plan.opportunity
		var keys []plannedKey
		if opp.SourceURL != "" {
			canonical, err := CanonicalIdentityURL(opp.SourceURL)
			if err != nil {
				return invalid("sourceUrl is not a usable canonical identity URL")
			}
			keys = append(keys, plannedKey{namespace: IdentityNamespaceCanonicalURL, value: canonical,
				strength: IdentityKeyStrong, entityKind: IdentitySubjectVacancy})
		}
		_, hasReqID := fields["requisitionId"]
		_, hasIssuer := fields["reqIssuer"]
		if hasReqID != hasIssuer {
			return invalid("requisitionId and reqIssuer go together")
		}
		if hasReqID {
			namespace, err := IssuerReqIDNamespace(fields["reqIssuer"])
			if err != nil {
				return invalid("reqIssuer is not a usable req-id namespace")
			}
			value := CanonicalReqID(fields["requisitionId"])
			if value == "" || len(value) > 1024 {
				return invalid("requisitionId is empty after normalization")
			}
			keys = append(keys, plannedKey{namespace: namespace, value: value,
				strength: IdentityKeyStrong, entityKind: IdentitySubjectVacancy})
		}
		_, hasProvider := fields["boardProvider"]
		_, hasBoard := fields["board"]
		_, hasRecord := fields["boardRecordId"]
		if hasProvider != hasBoard || hasBoard != hasRecord {
			return invalid("boardProvider, board and boardRecordId go together")
		}
		if hasRecord {
			namespace, err := BoardRecordNamespace(fields["boardProvider"], fields["board"])
			if err != nil {
				return invalid("boardProvider/board is not a usable board namespace")
			}
			value := strings.TrimSpace(fields["boardRecordId"])
			if value == "" || len(value) > 1024 {
				return invalid("boardRecordId is empty")
			}
			keys = append(keys, plannedKey{namespace: namespace, value: value,
				strength: IdentityKeyStrong, entityKind: IdentitySubjectVacancy})
		}
		if domain, ok := fields["employerDomain"]; ok {
			normal := CanonicalEmployerDomain(domain)
			if normal == "" || len(normal) > 253 || strings.ContainsAny(normal, " \t\r\n/@:") {
				return invalid("employerDomain is not a usable domain")
			}
			keys = append(keys, plannedKey{namespace: IdentityNamespaceEmployerDomain, value: normal,
				strength: IdentityKeyAlias, entityKind: IdentitySubjectVacancy})
		}
		return keys, nil, nil
	}
}

// recheckSaveKeys enforces strong-key convergence: a key already
// identifying another record is identity_ambiguous unless the item declares
// reusedIdentifier, which supersedes the old row (R02/R03) instead of
// overwriting it. Keys already bound to an update target are skipped.
func recheckSaveKeys(ctx context.Context, db ResearchDB, itemIndex int, plan *savePlan,
	keys []plannedKey, pendingKeys map[saveKeyRef]string) ([]plannedKey, []researchcontract.SaveItemError, error) {
	self := plan.recordID
	if plan.create {
		self = fmt.Sprintf("new:%d", itemIndex)
	}
	var kept []plannedKey
	for _, key := range keys {
		ref := saveKeyRef{key.namespace, key.value}
		if owner, ok := pendingKeys[ref]; ok && owner != self {
			return nil, []researchcontract.SaveItemError{saveItemErr(itemIndex,
				researchcontract.OutcomeIdentityAmbiguous,
				fmt.Sprintf("identity key %s=%q is already claimed by this batch; merge into one item",
					key.namespace, key.value), nil)}, nil
		}
		if key.strength != IdentityKeyStrong {
			kept = append(kept, key)
			continue
		}
		row, err := FindCurrentStrongKey(ctx, db, key.namespace, key.value)
		if errors.Is(err, ErrNotFound) {
			kept = append(kept, key)
			pendingKeys[ref] = self
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if (plan.kind == "company" && row.CompanyID == plan.recordID && !plan.create) ||
			(plan.kind == "opportunity" && row.OpportunityID == plan.recordID && !plan.create) {
			continue // already bound to this record: idempotent re-save
		}
		if plan.reuse {
			key.supersedeOld = row.ID
			kept = append(kept, key)
			pendingKeys[ref] = self
			continue
		}
		establishedKind, establishedID := "company", row.CompanyID
		if row.OpportunityID != "" {
			establishedKind, establishedID = "opportunity", row.OpportunityID
		}
		var current *int64
		if live, err := GetIdentityCandidate(ctx, db, establishedKind, establishedID); err == nil {
			current = &live.Revision
		} else if !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
		return nil, []researchcontract.SaveItemError{saveItemErr(itemIndex,
			researchcontract.OutcomeIdentityAmbiguous,
			fmt.Sprintf("identity key %s=%q already identifies %s %q; re-match and save as an update, or declare reusedIdentifier for a materially different opening",
				key.namespace, key.value, establishedKind, establishedID), current)}, nil
	}
	return kept, nil, nil
}

// classifySaveSightings labels each evidence link: reused_identifier under
// the reuse signal, else first/unchanged/changed against the record's
// prior sightings by content sha.
func classifySaveSightings(ctx context.Context, db ResearchDB, plan *savePlan) error {
	var priors []RecordSighting
	var err error
	if !plan.create {
		if plan.kind == "company" {
			priors, err = ListRecordSightingsByCompany(ctx, db, plan.recordID)
		} else {
			priors, err = ListRecordSightingsByOpportunity(ctx, db, plan.recordID)
		}
		if err != nil {
			return err
		}
	}
	seenSHA := map[string]bool{}
	for _, prior := range priors {
		seenSHA[prior.ContentSHA256] = true
	}
	for i := range plan.evidence {
		switch {
		case plan.reuse:
			plan.evidence[i].sightingKind = SightingReusedIdentifier
		case seenSHA[plan.evidence[i].contentSHA]:
			plan.evidence[i].sightingKind = SightingUnchanged
		case len(priors) > 0:
			plan.evidence[i].sightingKind = SightingChanged
		default:
			plan.evidence[i].sightingKind = SightingFirst
		}
	}
	return nil
}

// executeRecordsBatch writes every planned item in index order. Any write
// failure aborts the whole transaction; revision races surface as item
// conflicts, never partial commits.
func executeRecordsBatch(ctx context.Context, db ResearchDB, actor Actor, batch researchcontract.SaveBatch,
	plans []*savePlan) ([]researchcontract.SavedRecord, []saveItemDetail, error) {
	created := map[int]string{}
	saved := make([]researchcontract.SavedRecord, 0, len(plans))
	detail := make([]saveItemDetail, 0, len(plans))
	for _, plan := range plans {
		record, det, err := executeSavePlan(ctx, db, actor, batch.RunID, plan, created)
		if err != nil {
			return nil, nil, err
		}
		if plan.create {
			created[plan.index] = record.RecordID
		}
		saved = append(saved, record)
		detail = append(detail, det)
	}
	return saved, detail, nil
}

func executeSavePlan(ctx context.Context, db ResearchDB, actor Actor, runID string, plan *savePlan,
	created map[int]string) (researchcontract.SavedRecord, saveItemDetail, error) {
	fail := func(code researchcontract.Outcome, detail string, current *int64) (researchcontract.SavedRecord, saveItemDetail, error) {
		return researchcontract.SavedRecord{}, saveItemDetail{},
			&saveItemFailure{item: saveItemErr(plan.index, code, detail, current)}
	}
	recordID := plan.recordID
	now := recordNow()
	if plan.create {
		id, err := randomID()
		if err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
		recordID = id
	}
	op := ""
	var revisionBefore *int64
	switch {
	case plan.kind == "company" && plan.create:
		op = "company.create"
		if _, err := db.ExecContext(ctx, `INSERT INTO companies
  (id,name,website,notes,revision,created_at,updated_at) VALUES (?,?,?,?,1,?,?)`,
			recordID, plan.company.Name, optionalText(plan.company.Website),
			plan.company.Notes, now, now); err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
	case plan.kind == "company":
		op = "company.patch"
		before := plan.expectedRevision
		revisionBefore = &before
		updated, err := db.ExecContext(ctx, `UPDATE companies SET name=?,website=?,notes=?,revision=?,updated_at=?
  WHERE id=? AND revision=? AND archived_at IS NULL`, plan.company.Name,
			optionalText(plan.company.Website), plan.company.Notes, plan.newRevision, now,
			recordID, plan.expectedRevision)
		if err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
		if ok, err := oneRowAffected(updated); err != nil || !ok {
			if err != nil {
				return researchcontract.SavedRecord{}, saveItemDetail{}, err
			}
			return fail(revisionConflictFor(ctx, db, plan))
		}
	case plan.kind == "opportunity" && plan.create:
		op = "opportunity.create"
		companyID := plan.opportunity.CompanyID
		if plan.companyRef >= 0 {
			companyID = created[plan.companyRef]
			if companyID == "" {
				return fail(researchcontract.OutcomeInvalid,
					fmt.Sprintf("companyId batch:%d did not resolve", plan.companyRef), nil)
			}
		}
		plan.opportunity.CompanyID = companyID
		inserted, err := db.ExecContext(ctx, `INSERT INTO opportunities
  (id,company_id,title,kind,source_url,original_text,notes,stage,work_pattern,location_text,
   posted_on,deadline_on,revision,created_at,updated_at)
  SELECT ?,?,?,?,?,?,?,?,?,?,?,?,1,?,? FROM companies
  WHERE id=? AND archived_at IS NULL`, recordID, companyID, plan.opportunity.Title,
			plan.opportunity.Kind, optionalText(plan.opportunity.SourceURL), plan.opportunity.OriginalText,
			plan.opportunity.Notes, plan.opportunity.Stage, plan.opportunity.WorkPattern,
			plan.opportunity.LocationText, optionalText(plan.opportunity.PostedOn),
			optionalText(plan.opportunity.DeadlineOn), now, now, companyID)
		if err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
		if ok, err := oneRowAffected(inserted); err != nil || !ok {
			if err != nil {
				return researchcontract.SavedRecord{}, saveItemDetail{}, err
			}
			return fail(researchcontract.OutcomeInvalid,
				fmt.Sprintf("company %q became unavailable", companyID), nil)
		}
		if err := writeAdvertisedCompensation(ctx, db, recordID, plan.opportunity.Compensation); err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
	default: // update_opportunity
		op = "opportunity.patch"
		before := plan.expectedRevision
		revisionBefore = &before
		updated, err := db.ExecContext(ctx, `UPDATE opportunities SET title=?,kind=?,source_url=?,original_text=?,
  notes=?,stage=?,work_pattern=?,location_text=?,posted_on=?,deadline_on=?,revision=?,updated_at=?
  WHERE id=? AND revision=? AND archived_at IS NULL`, plan.opportunity.Title, plan.opportunity.Kind,
			optionalText(plan.opportunity.SourceURL), plan.opportunity.OriginalText, plan.opportunity.Notes,
			plan.opportunity.Stage, plan.opportunity.WorkPattern, plan.opportunity.LocationText,
			optionalText(plan.opportunity.PostedOn), optionalText(plan.opportunity.DeadlineOn),
			plan.newRevision, now, recordID, plan.expectedRevision)
		if err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
		if ok, err := oneRowAffected(updated); err != nil || !ok {
			if err != nil {
				return researchcontract.SavedRecord{}, saveItemDetail{}, err
			}
			return fail(revisionConflictFor(ctx, db, plan))
		}
		if err := writeAdvertisedCompensation(ctx, db, recordID, plan.opportunity.Compensation); err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
	}
	after := plan.newRevision
	auditID, err := randomID()
	if err != nil {
		return researchcontract.SavedRecord{}, saveItemDetail{}, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, op, plan.kind,
		recordID, revisionBefore, after, utcNow()); err != nil {
		return researchcontract.SavedRecord{}, saveItemDetail{}, err
	}
	det := saveItemDetail{Index: plan.index, Op: plan.op, RecordID: recordID,
		Revision: plan.newRevision, AuditID: auditID,
		Assessed: plan.assessed, Assessments: append([]string(nil), plan.assessmentIDs...)}
	if plan.kind == "opportunity" {
		if err := insertSaveEvidenceSource(ctx, db, actor, recordID, auditID, plan.opportunity); err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
	}
	rowIDs := make([]string, 0, len(plan.evidence))
	for _, ev := range plan.evidence {
		rowIDs = append(rowIDs, ev.rowID)
	}
	evidenceRefs, _ := json.Marshal(rowIDs)
	for _, key := range plan.keys {
		if key.supersedeOld != "" {
			if err := MarkEntityIdentityKeySuperseded(ctx, db, key.supersedeOld); err != nil {
				return researchcontract.SavedRecord{}, saveItemDetail{}, err
			}
		}
		in := EntityIdentityKeyInput{EntityKind: key.entityKind, Namespace: key.namespace,
			KeyValue: key.value, Strength: key.strength, EvidenceRefs: string(evidenceRefs),
			SupersedesID: key.supersedeOld}
		if plan.kind == "company" {
			in.CompanyID = recordID
		} else {
			in.OpportunityID = recordID
		}
		stored, err := InsertEntityIdentityKey(ctx, db, actor, in)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: entity_identity_keys") {
				return fail(researchcontract.OutcomeIdentityAmbiguous,
					fmt.Sprintf("identity key %s=%q was established concurrently; re-match and retry",
						key.namespace, key.value), nil)
			}
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
		det.Keys = append(det.Keys, stored.ID)
	}
	for _, ev := range plan.evidence {
		row, err := ResolveSourceCaptureRow(ctx, db, ev.rowID)
		if err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
		in := RecordSightingInput{CaptureID: ev.rowID, ObservedURL: row.OriginalURL,
			FinalURL: row.FinalURL, ContentSHA256: ev.contentSHA, SightingKind: ev.sightingKind}
		if plan.kind == "company" {
			in.CompanyID = recordID
		} else {
			in.OpportunityID = recordID
		}
		sighting, err := InsertRecordSighting(ctx, db, actor, in)
		if err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
		det.Sightings = append(det.Sightings, sighting.ID)
	}
	if plan.decision != nil {
		decisionID, err := insertSaveDecision(ctx, db, actor, runID, recordID, plan)
		if err != nil {
			return researchcontract.SavedRecord{}, saveItemDetail{}, err
		}
		det.Decision = decisionID
	}
	return researchcontract.SavedRecord{RecordID: recordID, Revision: plan.newRevision, AuditID: auditID}, det, nil
}

// revisionConflictFor reports a write-phase revision race with the live
// revision for the conflicting item.
func revisionConflictFor(ctx context.Context, db ResearchDB, plan *savePlan) (researchcontract.Outcome, string, *int64) {
	live, err := GetIdentityCandidate(ctx, db, plan.kind, plan.recordID)
	if err != nil {
		return researchcontract.OutcomeRevisionConflict,
			fmt.Sprintf("%s %q changed under this batch", plan.kind, plan.recordID), nil
	}
	return researchcontract.OutcomeRevisionConflict,
		fmt.Sprintf("%s %q is at revision %d, expected %d",
			plan.kind, plan.recordID, live.Revision, plan.expectedRevision), &live.Revision
}

func oneRowAffected(result sql.Result) (bool, error) {
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return count == 1, nil
}

// insertSaveEvidenceSource cites the saved vacancy text as an immutable
// vacancy snapshot so downstream qualification and review read cited facts
// with source history instead of bare record fields.
func insertSaveEvidenceSource(ctx context.Context, db ResearchDB, actor Actor, opportunityID, auditID string, input OpportunityInput) error {
	var contextVersion int64
	if err := db.QueryRowContext(ctx, `SELECT context_version FROM qualification_input_versions
  WHERE opportunity_id=?`, opportunityID).Scan(&contextVersion); err != nil {
		return err
	}
	sourceID, err := randomID()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO evidence_sources
  (id,opportunity_id,company_id,opportunity_kind,context_version,source_kind,
   record_change_audit_id,source_url,original_text,content_sha256,recorded_at,actor_kind,actor_id)
  VALUES (?,?,?,?,?,'vacancy_snapshot',?,?,?,?,?,?,?)`, sourceID, opportunityID,
		input.CompanyID, input.Kind, contextVersion, auditID,
		optionalText(input.SourceURL), input.OriginalText, sourceDigest(input.OriginalText),
		utcNow(), actor.Kind, actor.ID)
	return err
}

// insertSaveDecision persists Codex's same/new call with its pinned
// candidate set, the chosen subject for same, the cited spans as
// distinguishing refs, and the first supporting assessment.
func insertSaveDecision(ctx context.Context, db ResearchDB, actor Actor, runID, recordID string, plan *savePlan) (string, error) {
	refs := make([]researchcontract.EvidenceRef, 0, len(plan.evidence))
	for _, ev := range plan.evidence {
		refs = append(refs, researchcontract.EvidenceRef{
			CaptureID: ev.rowID, SpanStart: ev.spanStart, SpanEnd: ev.spanEnd,
		})
	}
	distinguishing, err := CanonicalEvidenceRefsJSON(refs)
	if err != nil {
		return "", err
	}
	in := IdentityDecisionInput{RoundID: runID, SubjectKind: plan.subjectKind,
		Candidates: append([]researchcontract.CandidateIdentity(nil), plan.decision.Candidates...),
		Decision:   plan.decision.Decision, DecisionBasis: "records_save:" + string(plan.op),
		DistinguishingRefs: distinguishing}
	if plan.decision.Decision == IdentityDecisionSame {
		in.SubjectRevision = plan.expectedRevision
		if plan.kind == "company" {
			in.SubjectCompanyID = recordID
		} else {
			in.SubjectOpportunityID = recordID
		}
	}
	if len(plan.assessmentIDs) > 0 {
		in.JevAssessmentID = plan.assessmentIDs[0]
	}
	stored, err := InsertIdentityDecision(ctx, db, actor, in)
	if err != nil {
		return "", err
	}
	return stored.ID, nil
}
