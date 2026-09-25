package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"unicode/utf8"
)

// Grounded preparation versions (D3). One material version recomposes a
// single answered role's saved check plus saved per-question answer values
// into one new immutable application pack plus one new immutable material
// row, committed atomically. Codex drafting, Typst rendering, citation
// validation, and Jev relevance happen in the service/tool layer before this
// store call; the store re-verifies every pin, partitions questions from the
// saved state, computes readiness, and enforces the hold rules. This path
// performs no research, capture, fetch, or send of any kind.
//
// Answer refs pin text by hash only: answered refs carry the E3 value
// version plus text sha; drafted required refs carry answerVersion 0 plus the
// draft text sha (draft bytes live only in the immutable pack manifest, never
// auto-written to owner answer state); blank refs carry the E3 value version
// (0 when never saved) plus sha256 of the empty string.

const (
	MaterialOriginPrepared   = "prepared"
	MaterialOriginDirectEdit = "direct_edit"
	MaterialOriginRewrite    = "rewrite"
)

const (
	MaterialStatusNotPrepared = "not_prepared"
	MaterialStatusPreparing   = "preparing"
	MaterialStatusPrepared    = "prepared"
	MaterialStatusHeld        = "held"
	MaterialStatusOutdated    = "outdated"
)

// materialEmptyTextSHA is sha256(""): the textSha256 of every blank ref,
// distinct from any draft sha because drafts are never empty.
const materialEmptyTextSHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

const materialMaxDrafts = 24
const materialMaxSourceShas = 64

// MaterialAnswerRef pins one pinned-check question to its prepared answer.
type MaterialAnswerRef struct {
	QuestionID         string `json:"questionId"`
	QuestionTextSHA256 string `json:"questionTextSha256"`
	AnswerVersion      int64  `json:"answerVersion"`
	TextSHA256         string `json:"textSha256"`
}

// MaterialReadiness reports required-field readiness of one version.
type MaterialReadiness struct {
	Ready           bool     `json:"ready"`
	MissingRequired []string `json:"missingRequired"`
	Held            []string `json:"held"`
}

// MaterialProvenance records how one version was produced.
type MaterialProvenance struct {
	Origin     string   `json:"origin"`
	SourceShas []string `json:"sourceShas"`
	RewriteOf  *int64   `json:"rewriteOf,omitempty"`
}

// MaterialVersionView is one immutable prepared version.
type MaterialVersionView struct {
	PackID              string              `json:"packId"`
	Version             int64               `json:"version"`
	OpportunityID       string              `json:"opportunityId"`
	OpportunityRevision int64               `json:"opportunityRevision"`
	ProfileRevision     int64               `json:"profileRevision"`
	CheckID             string              `json:"checkId"`
	QuestionSetSHA256   string              `json:"questionSetSha256"`
	Answers             []MaterialAnswerRef `json:"answers"`
	Readiness           MaterialReadiness   `json:"readiness"`
	Provenance          MaterialProvenance  `json:"provenance"`
	CreatedAt           string              `json:"createdAt"`
	CreatedBy           Actor               `json:"createdBy"`
}

// MaterialStatusView is the side-effect-free read model. Preparing is a
// reserved contract value never returned by D3 reads: prepare commits
// synchronously, so only the workflow stage is transiently preparing.
type MaterialStatusView struct {
	Status  string               `json:"status"`
	Current *MaterialVersionView `json:"current,omitempty"`
}

// MaterialDraft is one grounded required answer produced by the service
// layer from verified owner facts. Drafts are accepted only for
// required+unset questions; the question's draft text sha is pinned in the
// ref while the bytes stay in the pack manifest.
type MaterialDraft struct {
	QuestionID string `json:"questionId"`
	Text       string `json:"text"`
	TextSHA256 string `json:"textSha256"`
}

// MaterialPrepareInput is the whole prepare payload. Pack is the fully
// rendered pack mutation (manifest, Typst source, PDF); its manifest must
// carry a material section pinning this check. SourceShas are the career
// source digests the service consumed; the store appends the question-set
// sha plus carried/drafted answer text shas, then sorts and dedupes.
type MaterialPrepareInput struct {
	RequestKey                string                       `json:"requestKey"`
	ExpectedCheckID           string                       `json:"expectedCheckId"`
	ExpectedQuestionSetSHA256 string                       `json:"expectedQuestionSetSha256"`
	ExpectedWorkflowRevision  int64                        `json:"expectedWorkflowRevision"`
	Pack                      ApplicationPackMutationInput `json:"pack"`
	Drafts                    []MaterialDraft              `json:"drafts,omitempty"`
	SourceShas                []string                     `json:"sourceShas,omitempty"`
}

// materialManifestSection is the pack-manifest binding committed alongside
// the material row. The service renders it; the store verifies it.
type materialManifestSection struct {
	CheckID           string `json:"checkId"`
	QuestionSetSHA256 string `json:"questionSetSha256"`
	Origin            string `json:"origin"`
	Answers           []struct {
		QuestionID string `json:"questionId"`
	} `json:"answers"`
}

func validMaterialSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func materialTextSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// validateMaterialPrepareInput rejects malformed payloads before any read.
// Pin mismatches against saved state are ErrConflict, not ErrInvalid.
func validateMaterialPrepareInput(actor Actor, opportunityID string, input MaterialPrepareInput) error {
	if !ownerRoundActor(actor) || opportunityID == "" || !ownerRequestKey(input.RequestKey) ||
		input.ExpectedCheckID == "" || !validMaterialSHA(input.ExpectedQuestionSetSHA256) ||
		input.ExpectedWorkflowRevision < 0 || len(input.Drafts) > materialMaxDrafts ||
		len(input.SourceShas) > materialMaxSourceShas {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(input.Drafts))
	for _, draft := range input.Drafts {
		if draft.QuestionID == "" || draft.Text == "" || len(draft.Text) > answerValueMaxText ||
			!utf8.ValidString(draft.Text) || materialTextSHA(draft.Text) != draft.TextSHA256 {
			return ErrInvalid
		}
		if _, dup := seen[draft.QuestionID]; dup {
			return ErrInvalid
		}
		seen[draft.QuestionID] = struct{}{}
	}
	for _, sha := range input.SourceShas {
		if !validMaterialSHA(sha) {
			return ErrInvalid
		}
	}
	return nil
}

type materialQuestion struct {
	id       string
	textSHA  string
	required string
}

type materialValue struct {
	version int64
	state   string
	textSHA string
}

// partitionMaterialAnswers resolves every pinned-check question in ordinal
// order against saved values plus drafts. Owner state always wins: drafts
// for answered, blank, optional, or unknown questions are ErrInvalid, as
// are drafts for questions outside the pinned check.
func partitionMaterialAnswers(questions []materialQuestion, values map[string]materialValue, drafts []MaterialDraft) ([]MaterialAnswerRef, MaterialReadiness, error) {
	byID := make(map[string]MaterialDraft, len(drafts))
	for _, draft := range drafts {
		byID[draft.QuestionID] = draft
	}
	refs := make([]MaterialAnswerRef, 0, len(questions))
	readiness := MaterialReadiness{MissingRequired: []string{}, Held: []string{}}
	for _, question := range questions {
		ref := MaterialAnswerRef{QuestionID: question.id, QuestionTextSHA256: question.textSHA}
		draft, hasDraft := byID[question.id]
		value, hasValue := values[question.id]
		switch {
		case hasValue && value.state == AnswerValueStateAnswered:
			if hasDraft {
				return nil, MaterialReadiness{}, ErrInvalid
			}
			ref.AnswerVersion, ref.TextSHA256 = value.version, value.textSHA
		case hasValue && value.state == AnswerValueStateBlank:
			if hasDraft {
				return nil, MaterialReadiness{}, ErrInvalid
			}
			ref.AnswerVersion, ref.TextSHA256 = value.version, materialEmptyTextSHA
			if question.required == CheckRequired {
				readiness.MissingRequired = append(readiness.MissingRequired, question.id)
			}
		case question.required == CheckRequired && !hasValue:
			if !hasDraft {
				readiness.MissingRequired = append(readiness.MissingRequired, question.id)
				readiness.Held = append(readiness.Held, question.id)
				ref.TextSHA256 = materialEmptyTextSHA
				break
			}
			ref.TextSHA256 = draft.TextSHA256
		default:
			if hasDraft {
				return nil, MaterialReadiness{}, ErrInvalid
			}
			ref.TextSHA256 = materialEmptyTextSHA
		}
		delete(byID, question.id)
		refs = append(refs, ref)
	}
	if len(byID) > 0 {
		return nil, MaterialReadiness{}, ErrInvalid
	}
	readiness.Ready = len(readiness.MissingRequired) == 0 && len(readiness.Held) == 0
	return refs, readiness, nil
}

// materialRequestDigest pins the idempotency envelope: resolved check pins,
// resolved refs, readiness, and source shas. Expected pins are excluded on
// purpose: a retry re-reads them after the first commit moved the workflow,
// and must still replay the committed version. Pack bytes are excluded too:
// a retried key replays the committed pack without re-render, and any new
// version requires a new request key.
func materialRequestDigest(checkID, questionSet string, refs []MaterialAnswerRef, readiness MaterialReadiness, sourceShas []string) string {
	data, _ := json.Marshal(struct {
		CheckID, QuestionSet string
		Refs                 []MaterialAnswerRef
		Readiness            MaterialReadiness
		SourceShas           []string
	}{checkID, questionSet, refs, readiness, sourceShas})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func materialSourceShas(career []string, questionSet string, refs []MaterialAnswerRef) []string {
	seen := make(map[string]struct{}, len(career)+len(refs)+1)
	out := make([]string, 0, len(seen))
	add := func(sha string) {
		if sha == "" || sha == materialEmptyTextSHA {
			return
		}
		if _, dup := seen[sha]; dup {
			return
		}
		seen[sha] = struct{}{}
		out = append(out, sha)
	}
	for _, sha := range career {
		add(sha)
	}
	add(questionSet)
	for _, ref := range refs {
		add(ref.TextSHA256)
	}
	sort.Strings(out)
	return out
}

func scanMaterialVersion(row *sql.Row) (MaterialVersionView, string, error) {
	var view MaterialVersionView
	var answersJSON, readinessJSON, sourceShasJSON, requestSHA, actorKind, actorID string
	var rewriteOf sql.NullInt64
	var origin string
	err := row.Scan(&view.OpportunityID, &view.Version, &view.PackID, &view.CheckID,
		&view.QuestionSetSHA256, &view.OpportunityRevision, &view.ProfileRevision,
		&origin, &rewriteOf, &answersJSON, &readinessJSON, &sourceShasJSON,
		&requestSHA, &actorKind, &actorID, &view.CreatedAt)
	if err != nil {
		return MaterialVersionView{}, "", err
	}
	view.Provenance.Origin = origin
	if rewriteOf.Valid {
		value := rewriteOf.Int64
		view.Provenance.RewriteOf = &value
	}
	if err := json.Unmarshal([]byte(answersJSON), &view.Answers); err != nil {
		return MaterialVersionView{}, "", err
	}
	if err := json.Unmarshal([]byte(readinessJSON), &view.Readiness); err != nil {
		return MaterialVersionView{}, "", err
	}
	if err := json.Unmarshal([]byte(sourceShasJSON), &view.Provenance.SourceShas); err != nil {
		return MaterialVersionView{}, "", err
	}
	if view.Answers == nil {
		view.Answers = []MaterialAnswerRef{}
	}
	if view.Readiness.MissingRequired == nil {
		view.Readiness.MissingRequired = []string{}
	}
	if view.Readiness.Held == nil {
		view.Readiness.Held = []string{}
	}
	if view.Provenance.SourceShas == nil {
		view.Provenance.SourceShas = []string{}
	}
	view.CreatedBy = Actor{Kind: actorKind, ID: actorID}
	return view, requestSHA, nil
}

const materialVersionColumns = `opportunity_id,version,pack_id,check_id,question_set_sha256,
  opportunity_revision,profile_revision,origin,rewrite_of,answers_json,readiness_json,
  source_shas_json,request_sha256,actor_kind,actor_id,created_at`

// materialLiveStatus recomputes staleness only: pinned check, question set,
// opportunity, and profile revisions must all match current state.
// Readiness is never recomputed; it describes the immutable version.
func materialLiveStatus(ctx context.Context, tx *sql.Tx, opportunityID string, view MaterialVersionView) (string, error) {
	var checkID, questionSet string
	var checkPinned int64
	err := tx.QueryRowContext(ctx, `SELECT id,question_set_sha256,opportunity_revision FROM job_checks
	  WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).
		Scan(&checkID, &questionSet, &checkPinned)
	if errors.Is(err, sql.ErrNoRows) {
		return MaterialStatusOutdated, nil
	}
	if err != nil {
		return "", err
	}
	var opportunityRevision, profileRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
		opportunityID).Scan(&opportunityRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&profileRevision); err != nil {
		return "", err
	}
	if view.CheckID != checkID || view.QuestionSetSHA256 != questionSet ||
		view.OpportunityRevision != opportunityRevision || view.ProfileRevision != profileRevision {
		return MaterialStatusOutdated, nil
	}
	if !view.Readiness.Ready {
		return MaterialStatusHeld, nil
	}
	return MaterialStatusPrepared, nil
}

// CurrentOpportunityMaterials reads the latest material version with live
// staleness. The read is side-effect-free.
func (s *Store) CurrentOpportunityMaterials(ctx context.Context, opportunityID string) (MaterialStatusView, error) {
	if opportunityID == "" {
		return MaterialStatusView{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MaterialStatusView{}, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return MaterialStatusView{}, err
	}
	view, _, err := scanMaterialVersion(tx.QueryRowContext(ctx, `SELECT `+materialVersionColumns+`
	  FROM opportunity_material_versions WHERE opportunity_id=? ORDER BY version DESC LIMIT 1`, opportunityID))
	if errors.Is(err, sql.ErrNoRows) {
		return MaterialStatusView{Status: MaterialStatusNotPrepared}, tx.Commit()
	}
	if err != nil {
		return MaterialStatusView{}, err
	}
	status, err := materialLiveStatus(ctx, tx, opportunityID, view)
	if err != nil {
		return MaterialStatusView{}, err
	}
	return MaterialStatusView{Status: status, Current: &view}, tx.Commit()
}

// OpportunityMaterialVersion reads one exact immutable version.
func (s *Store) OpportunityMaterialVersion(ctx context.Context, opportunityID string, version int64) (MaterialVersionView, error) {
	if opportunityID == "" || version < 1 {
		return MaterialVersionView{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MaterialVersionView{}, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return MaterialVersionView{}, err
	}
	view, _, err := scanMaterialVersion(tx.QueryRowContext(ctx, `SELECT `+materialVersionColumns+`
	  FROM opportunity_material_versions WHERE opportunity_id=? AND version=?`, opportunityID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return MaterialVersionView{}, ErrNotFound
	}
	if err != nil {
		return MaterialVersionView{}, err
	}
	return view, tx.Commit()
}

// PrepareOpportunityMaterials commits one new immutable pack plus one new
// immutable material version for an answered role. The role must be
// selected, its latest check checked and pinned to the current opportunity
// revision, the workflow revision must match at stage answered, preparing,
// or prepared, and the pack manifest must carry a material section pinning
// this check with one entry per pinned question.
//
// The same request key replays the committed version only when the resolved
// input is byte-identical; changed input under a reused key fails with
// ErrRoundIdempotencyConflict, and a new version always needs a new key.
// On success the workflow advances to prepared and the returned status is
// prepared when ready, held otherwise. Starting stages are answered (first
// prepare), preparing (retry), and prepared (new version after more owner
// answers); reviewing and sent roles change through D4 edit/rewrite, not
// re-prepare.
func (s *Store) PrepareOpportunityMaterials(ctx context.Context, actor Actor, opportunityID string, input MaterialPrepareInput) (MaterialStatusView, bool, error) {
	if err := validateMaterialPrepareInput(actor, opportunityID, input); err != nil {
		return MaterialStatusView{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MaterialStatusView{}, false, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return MaterialStatusView{}, false, err
	}
	var checkID, questionSet, checkStatus string
	var checkPinned int64
	err = tx.QueryRowContext(ctx, `SELECT id,question_set_sha256,status,opportunity_revision FROM job_checks
	  WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).
		Scan(&checkID, &questionSet, &checkStatus, &checkPinned)
	if errors.Is(err, sql.ErrNoRows) {
		return MaterialStatusView{}, false, ErrNotFound
	}
	if err != nil {
		return MaterialStatusView{}, false, err
	}
	questionRows, err := tx.QueryContext(ctx, `SELECT q.id,q.text_sha256,q.required,
	  v.version,v.state,v.text_sha256
	  FROM job_check_questions q LEFT JOIN answer_values v ON v.question_id=q.id
	  WHERE q.check_id=? ORDER BY q.ordinal`, checkID)
	if err != nil {
		return MaterialStatusView{}, false, err
	}
	questions := make([]materialQuestion, 0)
	values := make(map[string]materialValue)
	for questionRows.Next() {
		var question materialQuestion
		var version sql.NullInt64
		var state, textSHA sql.NullString
		if err := questionRows.Scan(&question.id, &question.textSHA, &question.required,
			&version, &state, &textSHA); err != nil {
			questionRows.Close()
			return MaterialStatusView{}, false, err
		}
		questions = append(questions, question)
		if version.Valid {
			values[question.id] = materialValue{version: version.Int64, state: state.String, textSHA: textSHA.String}
		}
	}
	questionRows.Close()
	if err := questionRows.Err(); err != nil {
		return MaterialStatusView{}, false, err
	}
	refs, readiness, err := partitionMaterialAnswers(questions, values, input.Drafts)
	if err != nil {
		return MaterialStatusView{}, false, err
	}
	sourceShas := materialSourceShas(input.SourceShas, questionSet, refs)
	digest := materialRequestDigest(checkID, questionSet, refs, readiness, sourceShas)
	var existingVersion int64
	var existingDigest string
	err = tx.QueryRowContext(ctx, `SELECT version,request_sha256 FROM opportunity_material_versions
	  WHERE opportunity_id=? AND request_key=?`, opportunityID, input.RequestKey).
		Scan(&existingVersion, &existingDigest)
	if err == nil {
		if existingDigest != digest {
			return MaterialStatusView{}, false, ErrRoundIdempotencyConflict
		}
		view, _, err := scanMaterialVersion(tx.QueryRowContext(ctx, `SELECT `+materialVersionColumns+`
		  FROM opportunity_material_versions WHERE opportunity_id=? AND version=?`, opportunityID, existingVersion))
		if err != nil {
			return MaterialStatusView{}, false, err
		}
		status, err := materialLiveStatus(ctx, tx, opportunityID, view)
		if err != nil {
			return MaterialStatusView{}, false, err
		}
		return MaterialStatusView{Status: status, Current: &view}, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MaterialStatusView{}, false, err
	}
	var opportunityRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
		opportunityID).Scan(&opportunityRevision); err != nil {
		return MaterialStatusView{}, false, err
	}
	var profileRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&profileRevision); err != nil {
		return MaterialStatusView{}, false, err
	}
	workflow, err := scanRoleWorkflow(tx.QueryRowContext(ctx, `SELECT stage,revision,blocked_reason,updated_at
	  FROM role_workflow WHERE opportunity_id=?`, opportunityID), opportunityID, opportunityRevision, "")
	if err != nil {
		return MaterialStatusView{}, false, err
	}
	if checkStatus != CheckStatusChecked || checkID != input.ExpectedCheckID ||
		questionSet != input.ExpectedQuestionSetSHA256 || checkPinned != opportunityRevision ||
		workflow.Revision != input.ExpectedWorkflowRevision ||
		(workflow.Stage != RoleStageAnswered && workflow.Stage != RoleStagePreparing &&
			workflow.Stage != RoleStagePrepared) {
		return MaterialStatusView{}, false, ErrConflict
	}
	if input.Pack.OpportunityID != opportunityID {
		return MaterialStatusView{}, false, ErrInvalid
	}
	var manifest struct {
		Material *materialManifestSection `json:"material"`
	}
	if err := json.Unmarshal(input.Pack.ManifestJSON, &manifest); err != nil || manifest.Material == nil ||
		manifest.Material.CheckID != checkID || manifest.Material.QuestionSetSHA256 != questionSet ||
		manifest.Material.Origin != MaterialOriginPrepared ||
		len(manifest.Material.Answers) != len(questions) {
		return MaterialStatusView{}, false, ErrInvalid
	}
	packID, _, err := createApplicationPackTx(ctx, tx, input.Pack)
	if err != nil {
		return MaterialStatusView{}, false, err
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM opportunity_material_versions
	  WHERE opportunity_id=?`, opportunityID).Scan(&version); err != nil {
		return MaterialStatusView{}, false, err
	}
	answersJSON, _ := json.Marshal(refs)
	readinessJSON, _ := json.Marshal(readiness)
	sourceShasJSON, _ := json.Marshal(sourceShas)
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO opportunity_material_versions
	  (opportunity_id,version,pack_id,check_id,question_set_sha256,opportunity_revision,
	   profile_revision,workflow_revision,origin,rewrite_of,answers_json,readiness_json,
	   source_shas_json,request_key,request_sha256,actor_kind,actor_id,created_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, opportunityID, version, packID, checkID,
		questionSet, opportunityRevision, profileRevision, workflow.Revision, MaterialOriginPrepared,
		nil, string(answersJSON), string(readinessJSON), string(sourceShasJSON),
		input.RequestKey, digest, actor.Kind, actor.ID, now)
	if err != nil {
		return MaterialStatusView{}, false, err
	}
	stages := []string{RoleStagePreparing, RoleStagePrepared}
	if workflow.Stage == RoleStagePreparing {
		stages = []string{RoleStagePrepared}
	}
	revision := workflow.Revision
	for _, stage := range stages {
		revision++
		if _, err := tx.ExecContext(ctx, `UPDATE role_workflow SET stage=?,revision=?,blocked_reason='',updated_at=?
		  WHERE opportunity_id=?`, stage, revision, now, opportunityID); err != nil {
			return MaterialStatusView{}, false, err
		}
		auditID, err := randomID()
		if err != nil {
			return MaterialStatusView{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_changes
		  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
		  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, "system", "role-workflow", "role."+stage,
			"role_workflow", opportunityID, revision-1, revision, now); err != nil {
			return MaterialStatusView{}, false, err
		}
	}
	auditID, err := randomID()
	if err != nil {
		return MaterialStatusView{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, "material.prepare",
		"opportunity_material", opportunityID, 0, version, now); err != nil {
		return MaterialStatusView{}, false, err
	}
	view := MaterialVersionView{PackID: packID, Version: version, OpportunityID: opportunityID,
		OpportunityRevision: opportunityRevision, ProfileRevision: profileRevision,
		CheckID: checkID, QuestionSetSHA256: questionSet, Answers: refs, Readiness: readiness,
		Provenance: MaterialProvenance{Origin: MaterialOriginPrepared, SourceShas: sourceShas},
		CreatedAt:  now, CreatedBy: actor}
	status := MaterialStatusPrepared
	if !readiness.Ready {
		status = MaterialStatusHeld
	}
	return MaterialStatusView{Status: status, Current: &view}, true, tx.Commit()
}
