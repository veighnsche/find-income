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
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// Selected-role job checking (D1). The owner explicitly starts a check for
// one selected role; a Codex round saves the full check payload atomically
// through the scoped opportunity.check_save round mutation. Checks are
// immutable once completed: a recheck creates a new row and never edits a
// saved check. Every employer question must cite its source capture and span;
// invented (unsourced) questions fail validation and the check stays pending.

// Check statuses stored on a check row. The read model adds not_checked (no
// check yet) and outdated (basis moved past the pinned revision).
const (
	CheckStatusChecking = "checking"
	CheckStatusChecked  = "checked"
	CheckStatusBlocked  = "blocked"

	CheckOverallNotChecked = "not_checked"
	CheckOverallChecking   = "checking"
	CheckOverallChecked    = "checked"
	CheckOverallBlocked    = "blocked"
	CheckOverallOutdated   = "outdated"
)

// Coded blocked reasons. A blocked verdict is terminal for its check row; the
// owner starts a recheck with a new request key.
const (
	CheckBlockedSourceUnavailable   = "source_unavailable"
	CheckBlockedRouteAmbiguous      = "route_ambiguous"
	CheckBlockedRouteUnsupported    = "route_unsupported"
	CheckBlockedQuestionsUnresolved = "questions_unresolved"
	CheckBlockedOther               = "other"
)

const (
	CheckRequired        = "required"
	CheckOptional        = "optional"
	CheckRequiredUnknown = "unknown"
)

const (
	CheckRouteJudgmentApplication = "application_route"
	CheckRouteJudgmentOther       = "other_contact"
	CheckRouteJudgmentUnresolved  = "unresolved"
)

const (
	CheckRouteDirect      = "direct"
	CheckRouteReferral    = "referral"
	CheckRouteRecruiter   = "recruiter"
	CheckRouteUnsupported = "unsupported"
)

const (
	CheckGapMissingFact     = "missing_fact"
	CheckGapAmbiguousSource = "ambiguous_source"
	CheckGapUnverifiedClaim = "unverified_claim"
	CheckGapOther           = "other"
)

const (
	CheckQuestionFreeText   = "free_text"
	CheckQuestionChoice     = "choice"
	CheckQuestionAttachment = "attachment"
	CheckQuestionOther      = "other"
)

// Lifecycle activity kinds are server-issued; Codex progress entries must not
// use the check. prefix.
const (
	CheckActivityStarted   = "check.started"
	CheckActivityCompleted = "check.completed"
	CheckActivityBlocked   = "check.blocked"
)

const (
	checkMaxQuestions  = 200
	checkMaxDocuments  = 100
	checkMaxGaps       = 100
	checkMaxActivity   = 50
	checkMaxText       = 2000
	checkMaxLabel      = 200
	checkMaxPayload    = 8000
	checkMaxSpanWindow = 2000
)

// CheckSourceSpan cites the exact capture bytes a statement came from.
type CheckSourceSpan struct {
	CaptureID string `json:"captureId"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
}

// CheckQuestionView is one saved actual employer question with identity,
// requiredness and source provenance.
type CheckQuestionView struct {
	ID            string          `json:"id"`
	CheckID       string          `json:"checkId"`
	Ordinal       int             `json:"ordinal"`
	Text          string          `json:"text"`
	Required      string          `json:"required"`
	Kind          string          `json:"kind,omitempty"`
	SourceSpan    CheckSourceSpan `json:"sourceSpan"`
	SourceExcerpt string          `json:"sourceExcerpt"`
	TextSHA256    string          `json:"textSha256"`
}

// RequestedDocumentView is one employer-requested document, sourced.
type RequestedDocumentView struct {
	Label         string          `json:"label"`
	Required      bool            `json:"required"`
	SourceExcerpt string          `json:"sourceExcerpt"`
	SourceSpan    CheckSourceSpan `json:"sourceSpan"`
}

// CheckRouteView records the observed application route judgment, including
// honest ambiguous (unresolved) and unsupported states.
type CheckRouteView struct {
	RouteID         string `json:"routeId,omitempty"`
	Kind            string `json:"kind,omitempty"`
	DestinationText string `json:"destinationText,omitempty"`
	Judgment        string `json:"judgment"`
	SourceExcerpt   string `json:"sourceExcerpt"`
	ObservedAt      string `json:"observedAt"`
}

// CheckGapView names one consequential gap in the checked evidence.
type CheckGapView struct {
	ID            string `json:"id"`
	Description   string `json:"description"`
	Consequential bool   `json:"consequential"`
	Kind          string `json:"kind"`
}

// CheckVacancyView references the full vacancy/source evidence by id. Bytes
// stay in captures and evidence sources; the check pins ids plus an explicit
// completeness flag, never silent truncation.
type CheckVacancyView struct {
	CaptureIDs        []string `json:"captureIds"`
	EvidenceSourceIDs []string `json:"evidenceSourceIds"`
	Completeness      string   `json:"completeness"`
	SourceURL         string   `json:"sourceUrl"`
	RetrievedAt       string   `json:"retrievedAt"`
}

// CheckBlockedReasonView carries the coded reason a check was blocked.
type CheckBlockedReasonView struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// CheckView is the immutable saved check. While status is checking the
// sections are empty (not yet reported); completedAt and blockedReason appear
// only on finished rows.
type CheckView struct {
	ID                  string                  `json:"id"`
	OpportunityID       string                  `json:"opportunityId"`
	OpportunityRevision int64                   `json:"opportunityRevision"`
	WorkflowRevision    int64                   `json:"workflowRevision"`
	Status              string                  `json:"status"`
	BlockedReason       *CheckBlockedReasonView `json:"blockedReason,omitempty"`
	Vacancy             CheckVacancyView        `json:"vacancy"`
	RequestedDocuments  []RequestedDocumentView `json:"requestedDocuments"`
	Route               CheckRouteView          `json:"route"`
	Gaps                []CheckGapView          `json:"gaps"`
	Questions           []CheckQuestionView     `json:"questions"`
	QuestionSetSHA256   string                  `json:"questionSetSha256"`
	QuestionSetVersion  int64                   `json:"questionSetVersion"`
	ActivityCursor      string                  `json:"activityCursor,omitempty"`
	CreatedAt           string                  `json:"createdAt"`
	CompletedAt         string                  `json:"completedAt,omitempty"`
	CreatedBy           Actor                   `json:"createdBy"`
}

// CheckStatusView is the saved-proposal-with-staleness read model. Reads never
// trigger work.
type CheckStatusView struct {
	Status string     `json:"status"`
	Check  *CheckView `json:"check,omitempty"`
}

// CheckStartInput starts an explicit detail check for one selected role.
type CheckStartInput struct {
	RequestKey                  string `json:"requestKey"`
	ExpectedOpportunityRevision int64  `json:"expectedOpportunityRevision"`
	ExpectedWorkflowRevision    int64  `json:"expectedWorkflowRevision"`
}

// CheckVacancyInput carries vacancy capture refs plus completeness.
type CheckVacancyInput struct {
	CaptureIDs        []string `json:"captureIds"`
	EvidenceSourceIDs []string `json:"evidenceSourceIds"`
	Completeness      string   `json:"completeness"`
	SourceURL         string   `json:"sourceUrl"`
	RetrievedAt       string   `json:"retrievedAt"`
}

// RequestedDocumentInput is one sourced requested-document statement.
type RequestedDocumentInput struct {
	Label         string          `json:"label"`
	Required      bool            `json:"required"`
	SourceExcerpt string          `json:"sourceExcerpt"`
	SourceSpan    CheckSourceSpan `json:"sourceSpan"`
}

// CheckRouteInput is the observed route judgment with honest states.
type CheckRouteInput struct {
	RouteID         string `json:"routeId,omitempty"`
	Kind            string `json:"kind,omitempty"`
	DestinationText string `json:"destinationText,omitempty"`
	Judgment        string `json:"judgment"`
	SourceExcerpt   string `json:"sourceExcerpt"`
	ObservedAt      string `json:"observedAt"`
}

// CheckGapInput names one gap; the server assigns the id.
type CheckGapInput struct {
	Description   string `json:"description"`
	Consequential bool   `json:"consequential"`
	Kind          string `json:"kind"`
}

// CheckQuestionInput is one actual sourced employer question; the server
// assigns id, ordinal and hashes.
type CheckQuestionInput struct {
	Text          string          `json:"text"`
	Required      string          `json:"required"`
	Kind          string          `json:"kind,omitempty"`
	SourceSpan    CheckSourceSpan `json:"sourceSpan"`
	SourceExcerpt string          `json:"sourceExcerpt"`
}

// CheckBlockedInput declares the check cannot complete, with a coded reason.
type CheckBlockedInput struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// CheckActivityInput is one honest Codex progress entry journaled atomically
// with the save. Lifecycle kinds (check.*) are server-issued and rejected.
type CheckActivityInput struct {
	Kind               string          `json:"kind"`
	Outcome            string          `json:"outcome,omitempty"`
	CaptureID          string          `json:"captureId,omitempty"`
	ObservationID      string          `json:"observationId,omitempty"`
	RequestFingerprint string          `json:"requestFingerprint,omitempty"`
	Payload            json.RawMessage `json:"payload,omitempty"`
}

// CheckSaveInput is the full Codex check payload. A completing save carries
// every section; a blocked save carries the coded reason plus whatever
// evidence was gathered honestly.
type CheckSaveInput struct {
	OpportunityID      string                   `json:"opportunityId"`
	CheckID            string                   `json:"checkId"`
	Vacancy            CheckVacancyInput        `json:"vacancy"`
	RequestedDocuments []RequestedDocumentInput `json:"requestedDocuments"`
	Route              CheckRouteInput          `json:"route"`
	Gaps               []CheckGapInput          `json:"gaps"`
	Questions          []CheckQuestionInput     `json:"questions"`
	Blocked            *CheckBlockedInput       `json:"blocked,omitempty"`
	Activity           []CheckActivityInput     `json:"activity,omitempty"`
}

type jobCheckRow struct {
	ID                  string
	OpportunityID       string
	OpportunityRevision int64
	WorkflowRevision    int64
	Status              string
	BlockedCode         string
	BlockedDetail       string
	VacancyCaptures     string
	VacancyEvidence     string
	VacancyCompleteness string
	VacancySourceURL    string
	VacancyRetrievedAt  string
	Documents           string
	Route               string
	Gaps                string
	QuestionSetSHA256   string
	QuestionSetVersion  int64
	ActorKind           string
	ActorID             string
	CreatedAt           string
	CompletedAt         sql.NullString
}

const jobCheckColumns = `id,opportunity_id,opportunity_revision,workflow_revision,status,` +
	`blocked_code,blocked_detail,vacancy_capture_ids_json,vacancy_evidence_ids_json,` +
	`vacancy_completeness,vacancy_source_url,vacancy_retrieved_at,documents_json,` +
	`route_json,gaps_json,question_set_sha256,question_set_version,` +
	`actor_kind,actor_id,created_at,completed_at`

func scanJobCheck(row rowScanner) (jobCheckRow, error) {
	var value jobCheckRow
	err := row.Scan(&value.ID, &value.OpportunityID, &value.OpportunityRevision,
		&value.WorkflowRevision, &value.Status, &value.BlockedCode, &value.BlockedDetail,
		&value.VacancyCaptures, &value.VacancyEvidence, &value.VacancyCompleteness,
		&value.VacancySourceURL, &value.VacancyRetrievedAt, &value.Documents,
		&value.Route, &value.Gaps, &value.QuestionSetSHA256, &value.QuestionSetVersion,
		&value.ActorKind, &value.ActorID, &value.CreatedAt, &value.CompletedAt)
	return value, err
}

func validCheckBlockedCode(code string) bool {
	switch code {
	case CheckBlockedSourceUnavailable, CheckBlockedRouteAmbiguous,
		CheckBlockedRouteUnsupported, CheckBlockedQuestionsUnresolved, CheckBlockedOther:
		return true
	}
	return false
}

func checkText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum && utf8.ValidString(value)
}

// StartJobCheck starts an explicit detail check for one selected role. Only
// selected, blocked, or checked roles may start; unselected roles fail with
// ErrRoleNotSelected. A start from checked converges to the current check
// when its basis is unchanged, or opens a recheck when outdated.
// The start is idempotent on requestKey: replays return
// the same check, and a reused key with different input fails with
// ErrRoundIdempotencyConflict. A new key while a current pending check exists
// converges to that check without starting a second run; when the pending
// check's pinned opportunity revision moved, the new key supersedes it with a
// fresh pending check. Selection alone never starts a check.
func (s *Store) StartJobCheck(ctx context.Context, actor Actor, opportunityID string, input CheckStartInput) (CheckView, bool, error) {
	if !ownerRoundActor(actor) || opportunityID == "" || !ownerRequestKey(input.RequestKey) ||
		input.ExpectedOpportunityRevision < 1 || input.ExpectedWorkflowRevision < 0 {
		return CheckView{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckView{}, false, err
	}
	defer tx.Rollback()
	var opportunityRev int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&opportunityRev)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckView{}, false, ErrNotFound
	}
	if err != nil {
		return CheckView{}, false, err
	}
	decision, err := selectedDecisionTx(ctx, tx, opportunityID)
	if err != nil {
		return CheckView{}, false, err
	}
	current, err := scanRoleWorkflow(tx.QueryRowContext(ctx, `SELECT stage,revision,blocked_reason,updated_at
	  FROM role_workflow WHERE opportunity_id=?`, opportunityID), opportunityID, opportunityRev, decision.CreatedAt)
	if err != nil {
		return CheckView{}, false, err
	}
	digest := ownerDigest(struct {
		Actor         Actor
		OpportunityID string
		Input         CheckStartInput
	}{actor, opportunityID, input})
	var checkID, oldDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM job_checks WHERE opportunity_id=? AND request_key=?`,
		opportunityID, input.RequestKey).Scan(&checkID, &oldDigest)
	if err == nil {
		if oldDigest != digest {
			return CheckView{}, false, ErrRoundIdempotencyConflict
		}
		view, err := loadCheckView(ctx, tx, checkID)
		if err != nil {
			return CheckView{}, false, err
		}
		return view, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CheckView{}, false, err
	}
	var pendingID string
	var pendingPin int64
	err = tx.QueryRowContext(ctx, `SELECT id,opportunity_revision FROM job_checks
	  WHERE opportunity_id=? AND status='checking' ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).Scan(&pendingID, &pendingPin)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CheckView{}, false, err
	}
	if err == nil && current.Stage == RoleStageChecking && pendingPin == opportunityRev {
		view, err := loadCheckView(ctx, tx, pendingID)
		if err != nil {
			return CheckView{}, false, err
		}
		return view, false, tx.Commit()
	}
	supersede := err == nil && pendingPin != opportunityRev
	advance := true
	if current.Stage == RoleStageChecked {
		var latestID, latestStatus string
		var latestPin int64
		latestErr := tx.QueryRowContext(ctx, `SELECT id,status,opportunity_revision FROM job_checks
		  WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).Scan(&latestID, &latestStatus, &latestPin)
		if latestErr != nil && !errors.Is(latestErr, sql.ErrNoRows) {
			return CheckView{}, false, latestErr
		}
		if latestErr == nil && latestStatus == CheckStatusChecked && latestPin == opportunityRev {
			view, err := loadCheckView(ctx, tx, latestID)
			if err != nil {
				return CheckView{}, false, err
			}
			return view, false, tx.Commit()
		}
	} else if supersede && current.Stage == RoleStageChecking {
		advance = false
	} else if current.Stage != RoleStageSelected && current.Stage != RoleStageBlocked {
		return CheckView{}, false, ErrInvalid
	}
	if opportunityRev != input.ExpectedOpportunityRevision || current.Revision != input.ExpectedWorkflowRevision {
		return CheckView{}, false, ErrConflict
	}
	id, err := randomID()
	if err != nil {
		return CheckView{}, false, err
	}
	now := utcNow()
	workflowRev := current.Revision
	if advance {
		workflowRev = current.Revision + 1
		_, err = tx.ExecContext(ctx, `INSERT INTO role_workflow (opportunity_id,stage,revision,blocked_reason,updated_at)
		  VALUES (?,?,?,?,?) ON CONFLICT(opportunity_id) DO UPDATE SET stage=excluded.stage,
		  revision=excluded.revision, blocked_reason=excluded.blocked_reason, updated_at=excluded.updated_at`,
			opportunityID, RoleStageChecking, workflowRev, "", now)
		if err != nil {
			return CheckView{}, false, err
		}
		auditID, err := randomID()
		if err != nil {
			return CheckView{}, false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
		  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
		  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, "system", "role-workflow", "role."+RoleStageChecking,
			"role_workflow", opportunityID, current.Revision, workflowRev, now)
		if err != nil {
			return CheckView{}, false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO job_checks
	  (id,opportunity_id,request_key,request_sha256,opportunity_revision,workflow_revision,
	   status,actor_kind,actor_id,created_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?)`, id, opportunityID, input.RequestKey, digest,
		opportunityRev, workflowRev, CheckStatusChecking, actor.Kind, actor.ID, now)
	if err != nil {
		return CheckView{}, false, err
	}
	startAuditID, err := randomID()
	if err != nil {
		return CheckView{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?)`, startAuditID, actor.Kind, actor.ID, "check.start",
		"job_check", id, workflowRev, now)
	if err != nil {
		return CheckView{}, false, err
	}
	startPayload := map[string]any{"checkId": id, "opportunityRevision": opportunityRev, "workflowRevision": workflowRev}
	if supersede {
		startPayload["supersedesCheckId"] = pendingID
	}
	if err := appendCheckActivityTx(ctx, tx, checkActivityEntry{OpportunityID: opportunityID,
		CheckID: id, Actor: actor, Kind: CheckActivityStarted, Outcome: string(researchcontract.OutcomeOK),
		Payload: startPayload}); err != nil {
		return CheckView{}, false, err
	}
	view, err := loadCheckView(ctx, tx, id)
	if err != nil {
		return CheckView{}, false, err
	}
	return view, true, tx.Commit()
}

func loadCheckView(ctx context.Context, q Reader, checkID string) (CheckView, error) {
	row, err := scanJobCheck(q.QueryRowContext(ctx, `SELECT `+jobCheckColumns+` FROM job_checks WHERE id=?`, checkID))
	if errors.Is(err, sql.ErrNoRows) {
		return CheckView{}, ErrNotFound
	}
	if err != nil {
		return CheckView{}, err
	}
	return assembleCheckView(ctx, q, row)
}

func assembleCheckView(ctx context.Context, q Reader, row jobCheckRow) (CheckView, error) {
	view := CheckView{ID: row.ID, OpportunityID: row.OpportunityID,
		OpportunityRevision: row.OpportunityRevision, WorkflowRevision: row.WorkflowRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt.String,
		CreatedBy:          Actor{Kind: row.ActorKind, ID: row.ActorID},
		RequestedDocuments: []RequestedDocumentView{}, Gaps: []CheckGapView{}, Questions: []CheckQuestionView{}}
	if row.Status == CheckStatusBlocked {
		view.BlockedReason = &CheckBlockedReasonView{Code: row.BlockedCode, Detail: row.BlockedDetail}
	}
	var captures, evidence []string
	if err := json.Unmarshal([]byte(orJSON(row.VacancyCaptures, "[]")), &captures); err != nil {
		return CheckView{}, err
	}
	if err := json.Unmarshal([]byte(orJSON(row.VacancyEvidence, "[]")), &evidence); err != nil {
		return CheckView{}, err
	}
	if captures == nil {
		captures = []string{}
	}
	if evidence == nil {
		evidence = []string{}
	}
	view.Vacancy = CheckVacancyView{CaptureIDs: captures, EvidenceSourceIDs: evidence,
		Completeness: row.VacancyCompleteness, SourceURL: row.VacancySourceURL, RetrievedAt: row.VacancyRetrievedAt}
	if err := json.Unmarshal([]byte(orJSON(row.Documents, "[]")), &view.RequestedDocuments); err != nil {
		return CheckView{}, err
	}
	if row.Route != "" {
		if err := json.Unmarshal([]byte(row.Route), &view.Route); err != nil {
			return CheckView{}, err
		}
	}
	if err := json.Unmarshal([]byte(orJSON(row.Gaps, "[]")), &view.Gaps); err != nil {
		return CheckView{}, err
	}
	view.QuestionSetSHA256, view.QuestionSetVersion = row.QuestionSetSHA256, row.QuestionSetVersion
	rows, err := q.QueryContext(ctx, `SELECT id,check_id,ordinal,text,required,kind,capture_id,
	  span_start,span_end,source_excerpt,text_sha256 FROM job_check_questions
	  WHERE check_id=? ORDER BY ordinal`, row.ID)
	if err != nil {
		return CheckView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var question CheckQuestionView
		if err := rows.Scan(&question.ID, &question.CheckID, &question.Ordinal, &question.Text,
			&question.Required, &question.Kind, &question.SourceSpan.CaptureID,
			&question.SourceSpan.Start, &question.SourceSpan.End, &question.SourceExcerpt,
			&question.TextSHA256); err != nil {
			return CheckView{}, err
		}
		view.Questions = append(view.Questions, question)
	}
	if err := rows.Err(); err != nil {
		return CheckView{}, err
	}
	var cursor sql.NullString
	err = q.QueryRowContext(ctx, `SELECT event_id FROM job_check_activity
	  WHERE opportunity_id=? ORDER BY recorded_at, event_id LIMIT 1`, row.OpportunityID).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CheckView{}, err
	}
	view.ActivityCursor = cursor.String
	return view, nil
}

func orJSON(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// GetJobCheck reads one immutable check by id. Unselected roles fail with
// ErrRoleNotSelected; a check from another role reads as ErrNotFound.
func (s *Store) GetJobCheck(ctx context.Context, opportunityID, checkID string) (CheckView, error) {
	if opportunityID == "" || checkID == "" {
		return CheckView{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckView{}, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return CheckView{}, err
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT opportunity_id FROM job_checks WHERE id=?`, checkID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || owner != opportunityID {
		return CheckView{}, ErrNotFound
	}
	if err != nil {
		return CheckView{}, err
	}
	view, err := loadCheckView(ctx, tx, checkID)
	if err != nil {
		return CheckView{}, err
	}
	return view, tx.Commit()
}

// CurrentJobCheck reads the current saved check view. No check yet reports
// not_checked; a pending or completed check whose pinned opportunity revision
// moved reports outdated; a blocked verdict stands until the owner rechecks.
func (s *Store) CurrentJobCheck(ctx context.Context, opportunityID string) (CheckStatusView, error) {
	if opportunityID == "" {
		return CheckStatusView{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckStatusView{}, err
	}
	defer tx.Rollback()
	var opportunityRev int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&opportunityRev)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckStatusView{}, ErrNotFound
	}
	if err != nil {
		return CheckStatusView{}, err
	}
	if _, err := selectedDecisionTx(ctx, tx, opportunityID); err != nil {
		return CheckStatusView{}, err
	}
	row, err := scanJobCheck(tx.QueryRowContext(ctx, `SELECT `+jobCheckColumns+` FROM job_checks
	  WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID))
	if errors.Is(err, sql.ErrNoRows) {
		return CheckStatusView{Status: CheckOverallNotChecked}, tx.Commit()
	}
	if err != nil {
		return CheckStatusView{}, err
	}
	view, err := assembleCheckView(ctx, tx, row)
	if err != nil {
		return CheckStatusView{}, err
	}
	status := view.Status
	if row.OpportunityRevision != opportunityRev &&
		(row.Status == CheckStatusChecking || row.Status == CheckStatusChecked) {
		status = CheckOverallOutdated
	}
	return CheckStatusView{Status: status, Check: &view}, tx.Commit()
}

func checkSelectedRoleTx(ctx context.Context, tx *sql.Tx, opportunityID string) error {
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, opportunityID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = selectedDecisionTx(ctx, tx, opportunityID)
	return err
}

func validCheckSpan(span CheckSourceSpan) bool {
	return span.CaptureID != "" && span.Start >= 0 && span.End > span.Start &&
		span.End-span.Start <= checkMaxSpanWindow
}

func checkCaptureExistsTx(ctx context.Context, tx *sql.Tx, captureID string) error {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM source_captures WHERE id=?`, captureID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: unknown capture %q", ErrInvalid, captureID)
	}
	return err
}

func checkEvidenceOwnedTx(ctx context.Context, tx *sql.Tx, opportunityID, evidenceID string) error {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT opportunity_id FROM evidence_sources WHERE id=?`, evidenceID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || owner != opportunityID {
		return fmt.Errorf("%w: unknown evidence source %q", ErrInvalid, evidenceID)
	}
	return err
}

func checkRouteOwnedTx(ctx context.Context, tx *sql.Tx, opportunityID, routeID string) error {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT opportunity_id FROM opportunity_routes WHERE id=?`, routeID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || owner != opportunityID {
		return fmt.Errorf("%w: unknown route %q", ErrInvalid, routeID)
	}
	return err
}

func validCheckVacancy(value CheckVacancyInput) bool {
	if len(value.CaptureIDs)+len(value.EvidenceSourceIDs) < 1 ||
		!validEnum(value.Completeness, CaptureComplete, CaptureTruncated, CapturePaginated, CapturePartial) ||
		value.SourceURL == "" || len(value.SourceURL) > 2048 || !validInstant(value.RetrievedAt) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, value.CaptureIDs...), value.EvidenceSourceIDs...) {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return validateWebURL(value.SourceURL) == nil
}

func validCheckRoute(value CheckRouteInput) bool {
	switch value.Judgment {
	case CheckRouteJudgmentApplication, CheckRouteJudgmentOther, CheckRouteJudgmentUnresolved:
	default:
		return false
	}
	switch value.Kind {
	case "", CheckRouteDirect, CheckRouteReferral, CheckRouteRecruiter, CheckRouteUnsupported:
	default:
		return false
	}
	if !checkText(value.SourceExcerpt, checkMaxText) || !validInstant(value.ObservedAt) ||
		len(value.DestinationText) > 1000 || len(value.RouteID) > 64 {
		return false
	}
	if value.Kind == CheckRouteUnsupported && value.Judgment != CheckRouteJudgmentUnresolved {
		return false
	}
	if value.Judgment == CheckRouteJudgmentApplication &&
		((value.Kind != CheckRouteDirect && value.Kind != CheckRouteReferral && value.Kind != CheckRouteRecruiter) ||
			strings.TrimSpace(value.DestinationText) == "") {
		return false
	}
	return true
}

func validCheckGap(value CheckGapInput) bool {
	switch value.Kind {
	case CheckGapMissingFact, CheckGapAmbiguousSource, CheckGapUnverifiedClaim, CheckGapOther:
	default:
		return false
	}
	return checkText(value.Description, checkMaxText)
}

func validCheckQuestion(value CheckQuestionInput) bool {
	switch value.Required {
	case CheckRequired, CheckOptional, CheckRequiredUnknown:
	default:
		return false
	}
	switch value.Kind {
	case "", CheckQuestionFreeText, CheckQuestionChoice, CheckQuestionAttachment, CheckQuestionOther:
	default:
		return false
	}
	return checkText(value.Text, checkMaxText) && validCheckSpan(value.SourceSpan) &&
		checkText(value.SourceExcerpt, checkMaxText)
}

func validCheckDocument(value RequestedDocumentInput) bool {
	return checkText(value.Label, checkMaxLabel) && validCheckSpan(value.SourceSpan) &&
		checkText(value.SourceExcerpt, checkMaxText)
}

func vacancyPresent(value CheckVacancyInput) bool {
	return len(value.CaptureIDs) > 0 || len(value.EvidenceSourceIDs) > 0 ||
		value.Completeness != "" || value.SourceURL != "" || value.RetrievedAt != ""
}

func routePresent(value CheckRouteInput) bool {
	return value.RouteID != "" || value.Kind != "" || value.DestinationText != "" ||
		value.Judgment != "" || value.SourceExcerpt != "" || value.ObservedAt != ""
}

// validateCheckSave enforces the completeness gate. A completing save must
// carry every section with sourced questions; a blocked save must carry the
// coded reason, and any section it includes must still validate.
func validateCheckSave(ctx context.Context, tx *sql.Tx, input CheckSaveInput) error {
	if input.OpportunityID == "" || input.CheckID == "" ||
		len(input.Questions) > checkMaxQuestions || len(input.RequestedDocuments) > checkMaxDocuments ||
		len(input.Gaps) > checkMaxGaps || len(input.Activity) > checkMaxActivity {
		return ErrInvalid
	}
	if input.Blocked != nil {
		if !validCheckBlockedCode(input.Blocked.Code) || !checkText(input.Blocked.Detail, checkMaxText) {
			return ErrInvalid
		}
		if vacancyPresent(input.Vacancy) && !validCheckVacancy(input.Vacancy) {
			return ErrInvalid
		}
		if routePresent(input.Route) && !validCheckRoute(input.Route) {
			return ErrInvalid
		}
	} else {
		if input.RequestedDocuments == nil || input.Gaps == nil || len(input.Questions) < 1 ||
			!validCheckVacancy(input.Vacancy) || !validCheckRoute(input.Route) {
			return ErrInvalid
		}
	}
	for _, document := range input.RequestedDocuments {
		if !validCheckDocument(document) {
			return ErrInvalid
		}
	}
	for _, gap := range input.Gaps {
		if !validCheckGap(gap) {
			return ErrInvalid
		}
	}
	for _, question := range input.Questions {
		if !validCheckQuestion(question) {
			return ErrInvalid
		}
	}
	for _, entry := range input.Activity {
		if err := validCheckActivity(entry); err != nil {
			return err
		}
	}
	refs := map[string]bool{}
	for _, id := range input.Vacancy.CaptureIDs {
		refs[id] = true
	}
	for _, document := range input.RequestedDocuments {
		refs[document.SourceSpan.CaptureID] = true
	}
	for _, question := range input.Questions {
		refs[question.SourceSpan.CaptureID] = true
	}
	for _, entry := range input.Activity {
		if entry.CaptureID != "" {
			refs[entry.CaptureID] = true
		}
	}
	for id := range refs {
		if err := checkCaptureExistsTx(ctx, tx, id); err != nil {
			return err
		}
	}
	for _, id := range input.Vacancy.EvidenceSourceIDs {
		if err := checkEvidenceOwnedTx(ctx, tx, input.OpportunityID, id); err != nil {
			return err
		}
	}
	if input.Route.RouteID != "" {
		if err := checkRouteOwnedTx(ctx, tx, input.OpportunityID, input.Route.RouteID); err != nil {
			return err
		}
	}
	return nil
}

func validCheckActivity(entry CheckActivityInput) error {
	if trimmed := strings.TrimSpace(entry.Kind); len(trimmed) < 1 || len(trimmed) > 64 {
		return fmt.Errorf("%w: event kind length 1..64 required", ErrInvalid)
	}
	if strings.HasPrefix(entry.Kind, "check.") {
		return fmt.Errorf("%w: lifecycle activity is server-issued", ErrInvalid)
	}
	if entry.Outcome != "" && !researchcontract.Outcome(entry.Outcome).Valid() {
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalid, entry.Outcome)
	}
	if len(entry.Payload) > 0 {
		if len(entry.Payload) > checkMaxPayload || !json.Valid(entry.Payload) {
			return fmt.Errorf("%w: event payload must be valid JSON", ErrInvalid)
		}
	}
	if len(entry.ObservationID) > 300 || len(entry.RequestFingerprint) > 300 {
		return fmt.Errorf("%w: activity reference too long", ErrInvalid)
	}
	return nil
}

func questionSetSHA256(questions []CheckQuestionView) string {
	canonical := make([]struct {
		Text     string          `json:"text"`
		Required string          `json:"required"`
		Kind     string          `json:"kind"`
		Span     CheckSourceSpan `json:"span"`
	}, len(questions))
	for i, question := range questions {
		canonical[i].Text, canonical[i].Required = question.Text, question.Required
		canonical[i].Kind, canonical[i].Span = question.Kind, question.SourceSpan
	}
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// writeCheckSaveTx completes one pending check inside the round-mutation
// transaction. The caller owns capability, scope, allowance, audit and attempt
// linkage; this writer owns selection guards, revision fencing, the
// completeness gate and the workflow transition. A completing save moves
// checking to checked; a declared blocked save moves checking to blocked with
// the coded reason. Anything else leaves the check pending with an error.
func writeCheckSaveTx(ctx context.Context, tx *sql.Tx, actor Actor, expectedRevision int64, input CheckSaveInput) (string, int64, error) {
	if expectedRevision < 1 {
		return "", 0, ErrInvalid
	}
	var opportunityRev int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`, input.OpportunityID).Scan(&opportunityRev)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	decision, err := selectedDecisionTx(ctx, tx, input.OpportunityID)
	if err != nil {
		return "", 0, err
	}
	row, err := scanJobCheck(tx.QueryRowContext(ctx, `SELECT `+jobCheckColumns+` FROM job_checks WHERE id=?`, input.CheckID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	if row.OpportunityID != input.OpportunityID {
		return "", 0, ErrInvalid
	}
	if row.Status != CheckStatusChecking {
		return "", 0, ErrConflict
	}
	workflow, err := scanRoleWorkflow(tx.QueryRowContext(ctx, `SELECT stage,revision,blocked_reason,updated_at
	  FROM role_workflow WHERE opportunity_id=?`, input.OpportunityID),
		input.OpportunityID, opportunityRev, decision.CreatedAt)
	if err != nil {
		return "", 0, err
	}
	if workflow.Stage != RoleStageChecking || workflow.Revision != expectedRevision {
		return "", 0, ErrConflict
	}
	if opportunityRev != row.OpportunityRevision {
		return "", 0, fmt.Errorf("%w: check basis moved", ErrConflict)
	}
	if err := validateCheckSave(ctx, tx, input); err != nil {
		return "", 0, err
	}
	now := utcNow()
	documents := make([]RequestedDocumentView, 0, len(input.RequestedDocuments))
	for _, document := range input.RequestedDocuments {
		documents = append(documents, RequestedDocumentView{Label: document.Label,
			Required: document.Required, SourceExcerpt: document.SourceExcerpt, SourceSpan: document.SourceSpan})
	}
	gaps := make([]CheckGapView, 0, len(input.Gaps))
	for _, gap := range input.Gaps {
		id, err := randomID()
		if err != nil {
			return "", 0, err
		}
		gaps = append(gaps, CheckGapView{ID: id, Description: gap.Description,
			Consequential: gap.Consequential, Kind: gap.Kind})
	}
	questions := make([]CheckQuestionView, 0, len(input.Questions))
	for ordinal, question := range input.Questions {
		id, err := randomID()
		if err != nil {
			return "", 0, err
		}
		questions = append(questions, CheckQuestionView{ID: id, CheckID: row.ID,
			Ordinal: ordinal, Text: question.Text, Required: question.Required, Kind: question.Kind,
			SourceSpan: question.SourceSpan, SourceExcerpt: question.SourceExcerpt,
			TextSHA256: sourceDigest(question.Text)})
	}
	status := CheckStatusChecked
	blockedCode, blockedDetail := "", ""
	if input.Blocked != nil {
		status, blockedCode, blockedDetail = CheckStatusBlocked, input.Blocked.Code, input.Blocked.Detail
	}
	capturesJSON, _ := json.Marshal(input.Vacancy.CaptureIDs)
	if input.Vacancy.CaptureIDs == nil {
		capturesJSON = []byte("[]")
	}
	evidenceJSON, _ := json.Marshal(input.Vacancy.EvidenceSourceIDs)
	if input.Vacancy.EvidenceSourceIDs == nil {
		evidenceJSON = []byte("[]")
	}
	documentsJSON, _ := json.Marshal(documents)
	gapsJSON, _ := json.Marshal(gaps)
	routeJSON := ""
	if routePresent(input.Route) {
		var route CheckRouteView
		route.RouteID, route.Kind, route.DestinationText = input.Route.RouteID, input.Route.Kind, input.Route.DestinationText
		route.Judgment, route.SourceExcerpt, route.ObservedAt = input.Route.Judgment, input.Route.SourceExcerpt, input.Route.ObservedAt
		raw, _ := json.Marshal(route)
		routeJSON = string(raw)
	}
	setSHA := ""
	var setVersion int64
	if len(questions) > 0 {
		setSHA, setVersion = questionSetSHA256(questions), 1
	}
	result, err := tx.ExecContext(ctx, `UPDATE job_checks SET status=?,blocked_code=?,blocked_detail=?,
	  vacancy_capture_ids_json=?,vacancy_evidence_ids_json=?,vacancy_completeness=?,
	  vacancy_source_url=?,vacancy_retrieved_at=?,documents_json=?,route_json=?,gaps_json=?,
	  question_set_sha256=?,question_set_version=?,completed_at=?
	  WHERE id=? AND status='checking'`,
		status, blockedCode, blockedDetail, string(capturesJSON), string(evidenceJSON),
		input.Vacancy.Completeness, input.Vacancy.SourceURL, input.Vacancy.RetrievedAt,
		string(documentsJSON), routeJSON, string(gapsJSON), setSHA, setVersion, now, row.ID)
	if err != nil {
		return "", 0, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err == nil {
			err = ErrConflict
		}
		return "", 0, err
	}
	for _, question := range questions {
		_, err = tx.ExecContext(ctx, `INSERT INTO job_check_questions
		  (id,check_id,ordinal,text,required,kind,capture_id,span_start,span_end,source_excerpt,text_sha256)
		  VALUES (?,?,?,?,?,?,?,?,?,?,?)`, question.ID, question.CheckID, question.Ordinal,
			question.Text, question.Required, question.Kind, question.SourceSpan.CaptureID,
			question.SourceSpan.Start, question.SourceSpan.End, question.SourceExcerpt, question.TextSHA256)
		if err != nil {
			return "", 0, err
		}
	}
	for _, entry := range input.Activity {
		if err := appendCheckActivityTx(ctx, tx, checkActivityEntry{OpportunityID: input.OpportunityID,
			CheckID: row.ID, Actor: actor, Kind: entry.Kind, Outcome: entry.Outcome,
			CaptureID: entry.CaptureID, ObservationID: entry.ObservationID,
			RequestFingerprint: entry.RequestFingerprint, RawPayload: entry.Payload}); err != nil {
			return "", 0, err
		}
	}
	terminalKind, terminalOutcome := CheckActivityCompleted, string(researchcontract.OutcomeOK)
	if input.Blocked != nil {
		terminalKind, terminalOutcome = CheckActivityBlocked, string(researchcontract.OutcomeInvalid)
	}
	terminalPayload := map[string]any{"checkId": row.ID, "questionCount": len(questions),
		"questionSetSha256": setSHA, "opportunityRevision": opportunityRev}
	if input.Blocked != nil {
		terminalPayload["blockedCode"] = blockedCode
		terminalPayload["blockedDetail"] = blockedDetail
	}
	if err := appendCheckActivityTx(ctx, tx, checkActivityEntry{OpportunityID: input.OpportunityID,
		CheckID: row.ID, Actor: actor, Kind: terminalKind, Outcome: terminalOutcome, Payload: terminalPayload}); err != nil {
		return "", 0, err
	}
	toStage, workflowReason := RoleStageChecked, ""
	if input.Blocked != nil {
		toStage, workflowReason = RoleStageBlocked, blockedCode+": "+blockedDetail
	}
	_, err = tx.ExecContext(ctx, `UPDATE role_workflow SET stage=?,revision=?,blocked_reason=?,updated_at=?
	  WHERE opportunity_id=? AND revision=?`, toStage, workflow.Revision+1, workflowReason, now,
		input.OpportunityID, workflow.Revision)
	if err != nil {
		return "", 0, err
	}
	auditID, err := randomID()
	if err != nil {
		return "", 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, "system", "role-workflow", "role."+toStage,
		"role_workflow", input.OpportunityID, workflow.Revision, workflow.Revision+1, now)
	if err != nil {
		return "", 0, err
	}
	return row.ID, workflow.Revision + 1, nil
}

type checkActivityEntry struct {
	OpportunityID      string
	CheckID            string
	Actor              Actor
	Kind               string
	Outcome            string
	CaptureID          string
	ObservationID      string
	RequestFingerprint string
	Payload            map[string]any
	RawPayload         json.RawMessage
}

// appendCheckActivityTx journals one role-scoped activity record reusing the
// research Event shape. RunID carries the opportunity id; role scope outlives
// any single round.
func appendCheckActivityTx(ctx context.Context, tx *sql.Tx, entry checkActivityEntry) error {
	if strings.TrimSpace(entry.OpportunityID) == "" {
		return fmt.Errorf("%w: opportunity id required", ErrInvalid)
	}
	if trimmed := strings.TrimSpace(entry.Kind); len(trimmed) < 1 || len(trimmed) > 64 {
		return fmt.Errorf("%w: event kind length 1..64 required", ErrInvalid)
	}
	if entry.Outcome != "" && !researchcontract.Outcome(entry.Outcome).Valid() {
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalid, entry.Outcome)
	}
	payload := entry.RawPayload
	if payload == nil && entry.Payload != nil {
		raw, err := json.Marshal(entry.Payload)
		if err != nil {
			return err
		}
		payload = raw
	}
	if len(payload) > 0 && !json.Valid(payload) {
		return fmt.Errorf("%w: event payload must be valid JSON", ErrInvalid)
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	now := recordNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO job_check_activity
	  (event_id,opportunity_id,check_id,kind,request_fingerprint,observation_id,
	   capture_id,outcome,payload_json,actor_kind,actor_id,observed_at,recorded_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, entry.OpportunityID, nullString(entry.CheckID),
		entry.Kind, nullString(entry.RequestFingerprint), nullString(entry.ObservationID),
		nullString(entry.CaptureID), nullString(entry.Outcome), nullString(string(payload)),
		entry.Actor.Kind, entry.Actor.ID, now, now)
	return err
}

// ListCheckActivity pages one role's check activity in record order. Cursor is
// the last seen event id ("" starts at the head); nextCursor is "" at the
// tail. Events reuse the research Event shape with RunID set to the
// opportunity id.
func (s *Store) ListCheckActivity(ctx context.Context, opportunityID, cursor string, limit int) ([]researchcontract.Event, string, error) {
	if opportunityID == "" {
		return nil, "", fmt.Errorf("%w: opportunity id required", ErrInvalid)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	guard, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	guardErr := checkSelectedRoleTx(ctx, guard, opportunityID)
	_ = guard.Rollback()
	if guardErr != nil {
		return nil, "", guardErr
	}
	const base = `SELECT event_id,opportunity_id,check_id,kind,request_fingerprint,` +
		`observation_id,capture_id,outcome,payload_json,observed_at,recorded_at ` +
		`FROM job_check_activity WHERE opportunity_id=?`
	var rows *sql.Rows
	if cursor == "" {
		rows, err = s.db.QueryContext(ctx, base+` ORDER BY recorded_at,event_id LIMIT ?`, opportunityID, limit)
	} else {
		var recorded string
		if err := s.db.QueryRowContext(ctx, `SELECT recorded_at FROM job_check_activity
		  WHERE event_id=? AND opportunity_id=?`, cursor, opportunityID).Scan(&recorded); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", fmt.Errorf("%w: unknown event cursor", ErrInvalid)
			}
			return nil, "", err
		}
		rows, err = s.db.QueryContext(ctx, base+` AND (recorded_at,event_id) > (?,?)
		  ORDER BY recorded_at,event_id LIMIT ?`, opportunityID, recorded, cursor, limit)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	events := []researchcontract.Event{}
	for rows.Next() {
		var e researchcontract.Event
		var checkID, fingerprint, observation, capture, outcome, payload sql.NullString
		var observed, recorded string
		if err := rows.Scan(&e.ID, &e.RunID, &checkID, &e.Kind, &fingerprint,
			&observation, &capture, &outcome, &payload, &observed, &recorded); err != nil {
			return nil, "", err
		}
		e.RequestFingerprint = fingerprint.String
		e.ObservationID = observation.String
		e.CaptureID = capture.String
		e.Outcome = researchcontract.Outcome(outcome.String)
		if payload.Valid {
			e.Payload = json.RawMessage(payload.String)
		}
		if e.ObservedAt, err = parseResearchTime(observed); err != nil {
			return nil, "", err
		}
		if e.RecordedAt, err = parseResearchTime(recorded); err != nil {
			return nil, "", err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(events) == limit {
		next = events[len(events)-1].ID
	}
	return events, next, nil
}
