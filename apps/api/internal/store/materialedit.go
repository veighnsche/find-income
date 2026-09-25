package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// Direct exact edits and explicit rewrites (D4). Each call commits one new
// immutable application pack plus one new immutable material version,
// atomically. Prior pack and material rows are never updated or deleted, so
// earlier bytes always round-trip and earlier review/send authorization —
// which the delivery service binds to pack identity and content digest —
// can never authorize a changed version: every edit or rewrite mints a new
// pack id with a new content digest.
//
// An edit carries one owner-supplied exact text (no LLM anywhere on this
// path). The text is stored byte-exact in the new pack manifest's material
// section and verified byte-for-byte at commit. The edit keeps the base
// version's answer resolution (drafted texts survive edits); only the text
// layer changes, so readiness is re-derived from the carried resolution.
//
// A rewrite carries fresh per-question texts authored by one explicit Codex
// turn (the turn itself runs in the service layer through the shared
// one-shot primitive; the store only commits). Refs pin the rewritten text
// shas as manifest-only bytes (answerVersion 0, like D3 drafts) and
// readiness is recomputed from the rewritten texts: an empty rewrite for a
// required question holds it honestly instead of failing.
//
// Both operations are fenced by expectedVersion against the latest version:
// any concurrent commit wins once and every other caller gets ErrConflict.
// The same request key replays the committed version only when the resolved
// input is identical; changed input under a reused key fails with
// ErrRoundIdempotencyConflict. A new version always needs a new key.
//
// Allowed only from workflow stages prepared and reviewing. A commit from
// reviewing returns the role to prepared: changed material always needs
// fresh review before any send. Sent roles never change through this path.

const materialMaxEditText = 100000

const materialMaxRewriteTexts = 64

// MaterialEditInput is one exact owner edit of the current material text.
// Pack is the fully rendered new pack mutation; its manifest material
// section must carry origin "direct_edit" plus the exact text. SourceShas
// are extra digests the service consumed; the store adds the prior version
// shas, the question-set sha, the text sha, and the carried text shas.
type MaterialEditInput struct {
	RequestKey      string                       `json:"requestKey"`
	ExpectedVersion int64                        `json:"expectedVersion"`
	Text            string                       `json:"text"`
	Pack            ApplicationPackMutationInput `json:"pack"`
	SourceShas      []string                     `json:"sourceShas,omitempty"`
}

// MaterialRewriteText is one rewritten per-question text. Empty text means
// blank; required questions left blank hold the new version.
type MaterialRewriteText struct {
	QuestionID string `json:"questionId"`
	Text       string `json:"text"`
	TextSHA256 string `json:"textSha256"`
}

// MaterialRewriteInput is one explicit rewrite commit. Texts must cover
// every pinned-check question exactly once. Pack is the fully rendered new
// pack mutation; its manifest material section must carry origin "rewrite"
// with one entry per pinned question holding the exact rewritten text.
type MaterialRewriteInput struct {
	RequestKey      string                       `json:"requestKey"`
	ExpectedVersion int64                        `json:"expectedVersion"`
	Texts           []MaterialRewriteText        `json:"texts"`
	Pack            ApplicationPackMutationInput `json:"pack"`
	SourceShas      []string                     `json:"sourceShas,omitempty"`
}

// materialEditManifestSection is the pack-manifest binding for a direct
// edit. The service renders it; the store verifies it byte-for-byte.
type materialEditManifestSection struct {
	CheckID           string `json:"checkId"`
	QuestionSetSHA256 string `json:"questionSetSha256"`
	Origin            string `json:"origin"`
	Text              string `json:"text"`
	Answers           []struct {
		QuestionID string `json:"questionId"`
	} `json:"answers"`
}

// materialRewriteManifestSection is the pack-manifest binding for a
// rewrite. Entry texts make the immutable pack self-contained.
type materialRewriteManifestSection struct {
	CheckID           string `json:"checkId"`
	QuestionSetSHA256 string `json:"questionSetSha256"`
	Origin            string `json:"origin"`
	Answers           []struct {
		QuestionID string `json:"questionId"`
		Text       string `json:"text"`
	} `json:"answers"`
}

func validateMaterialEditInput(actor Actor, opportunityID string, input MaterialEditInput) error {
	if !ownerRoundActor(actor) || opportunityID == "" || !ownerRequestKey(input.RequestKey) ||
		input.ExpectedVersion < 1 || input.Text == "" || len(input.Text) > materialMaxEditText ||
		!utf8.ValidString(input.Text) || len(input.SourceShas) > materialMaxSourceShas {
		return ErrInvalid
	}
	for _, sha := range input.SourceShas {
		if !validMaterialSHA(sha) {
			return ErrInvalid
		}
	}
	return nil
}

func validateMaterialRewriteInput(actor Actor, opportunityID string, input MaterialRewriteInput) error {
	if !ownerRoundActor(actor) || opportunityID == "" || !ownerRequestKey(input.RequestKey) ||
		input.ExpectedVersion < 1 || len(input.Texts) == 0 || len(input.Texts) > materialMaxRewriteTexts ||
		len(input.SourceShas) > materialMaxSourceShas {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(input.Texts))
	for _, text := range input.Texts {
		if text.QuestionID == "" || len(text.Text) > answerValueMaxText ||
			!utf8.ValidString(text.Text) || materialTextSHA(text.Text) != text.TextSHA256 {
			return ErrInvalid
		}
		if _, dup := seen[text.QuestionID]; dup {
			return ErrInvalid
		}
		seen[text.QuestionID] = struct{}{}
	}
	for _, sha := range input.SourceShas {
		if !validMaterialSHA(sha) {
			return ErrInvalid
		}
	}
	return nil
}

// materialCarriedReadiness re-derives a new version's readiness from the
// carried resolution: the same missing/held lists, freshly copied, with the
// Ready flag recomputed from them.
func materialCarriedReadiness(src MaterialReadiness) MaterialReadiness {
	out := MaterialReadiness{
		MissingRequired: append([]string{}, src.MissingRequired...),
		Held:            append([]string{}, src.Held...),
	}
	out.Ready = len(out.MissingRequired) == 0 && len(out.Held) == 0
	return out
}

// materialRevisionDigest pins the idempotency envelope for one edit or
// rewrite commit: origin, rewrite linkage, expected base, resolved check
// pins, resolved refs, readiness, and source shas. The edit text participates
// through its sha in sourceShas; rewrite texts participate through their
// ref shas. Expected pins beyond the base version are excluded on purpose so
// a retry after success still replays.
func materialRevisionDigest(origin string, rewriteOf *int64, expectedVersion int64, checkID, questionSet string, refs []MaterialAnswerRef, readiness MaterialReadiness, sourceShas []string) string {
	data, _ := json.Marshal(struct {
		Origin, CheckID, QuestionSet string
		RewriteOf                    *int64
		ExpectedVersion              int64
		Refs                         []MaterialAnswerRef
		Readiness                    MaterialReadiness
		SourceShas                   []string
	}{origin, checkID, questionSet, rewriteOf, expectedVersion, refs, readiness, sourceShas})
	return materialTextSHA(string(data))
}

// materialRevisionBase loads the base version plus the pinned-check
// question ids in ordinal order for one edit/rewrite commit or replay.
func materialRevisionBase(ctx context.Context, tx *sql.Tx, opportunityID string, version int64) (MaterialVersionView, []materialQuestion, error) {
	base, _, err := scanMaterialVersion(tx.QueryRowContext(ctx, `SELECT `+materialVersionColumns+`
	  FROM opportunity_material_versions WHERE opportunity_id=? AND version=?`, opportunityID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return MaterialVersionView{}, nil, ErrNotFound
	}
	if err != nil {
		return MaterialVersionView{}, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,text_sha256,required FROM job_check_questions
	  WHERE check_id=? ORDER BY ordinal`, base.CheckID)
	if err != nil {
		return MaterialVersionView{}, nil, err
	}
	defer rows.Close()
	questions := make([]materialQuestion, 0, len(base.Answers))
	for rows.Next() {
		var question materialQuestion
		if err := rows.Scan(&question.id, &question.textSHA, &question.required); err != nil {
			return MaterialVersionView{}, nil, err
		}
		questions = append(questions, question)
	}
	if err := rows.Err(); err != nil {
		return MaterialVersionView{}, nil, err
	}
	return base, questions, nil
}

// resolveMaterialEditRefs carries the base resolution into the new version:
// identical refs, readiness re-derived. Drafted texts survive edits because
// the resolution — not just owner E3 state — is pinned per version.
func resolveMaterialEditRefs(base MaterialVersionView, textSHA string, extra []string) ([]MaterialAnswerRef, MaterialReadiness, []string) {
	refs := append([]MaterialAnswerRef{}, base.Answers...)
	readiness := materialCarriedReadiness(base.Readiness)
	career := make([]string, 0, len(base.Provenance.SourceShas)+len(extra)+1)
	career = append(career, base.Provenance.SourceShas...)
	career = append(career, extra...)
	career = append(career, textSHA)
	return refs, readiness, materialSourceShas(career, base.QuestionSetSHA256, refs)
}

// resolveMaterialRewriteRefs pins the rewritten texts as manifest-only
// bytes (answerVersion 0, like D3 drafts) and recomputes readiness from
// them. Texts must cover the pinned set exactly; anything else is ErrInvalid.
func resolveMaterialRewriteRefs(questions []materialQuestion, texts []MaterialRewriteText) ([]MaterialAnswerRef, error) {
	byID := make(map[string]MaterialRewriteText, len(texts))
	for _, text := range texts {
		byID[text.QuestionID] = text
	}
	refs := make([]MaterialAnswerRef, 0, len(questions))
	for _, question := range questions {
		text, ok := byID[question.id]
		if !ok {
			return nil, ErrInvalid
		}
		refs = append(refs, MaterialAnswerRef{QuestionID: question.id,
			QuestionTextSHA256: question.textSHA, TextSHA256: text.TextSHA256})
		delete(byID, question.id)
	}
	if len(byID) > 0 {
		return nil, ErrInvalid
	}
	return refs, nil
}

// rewriteReadiness recomputes readiness from rewritten texts: required
// questions left blank are both missing and held.
func rewriteReadiness(questions []materialQuestion, texts []MaterialRewriteText) MaterialReadiness {
	byID := make(map[string]string, len(texts))
	for _, text := range texts {
		byID[text.QuestionID] = text.Text
	}
	readiness := MaterialReadiness{MissingRequired: []string{}, Held: []string{}}
	for _, question := range questions {
		if question.required != CheckRequired || byID[question.id] != "" {
			continue
		}
		readiness.MissingRequired = append(readiness.MissingRequired, question.id)
		readiness.Held = append(readiness.Held, question.id)
	}
	readiness.Ready = len(readiness.MissingRequired) == 0 && len(readiness.Held) == 0
	return readiness
}

// replayMaterialRevision returns the already-committed version when the
// request key exists and the resolved input digest matches. It reports
// found=false when the key is new and the caller must proceed to fences.
func replayMaterialRevision(ctx context.Context, tx *sql.Tx, opportunityID, requestKey, digest string) (MaterialVersionView, bool, error) {
	var version int64
	var stored string
	err := tx.QueryRowContext(ctx, `SELECT version,request_sha256 FROM opportunity_material_versions
	  WHERE opportunity_id=? AND request_key=?`, opportunityID, requestKey).Scan(&version, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return MaterialVersionView{}, false, nil
	}
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	if stored != digest {
		return MaterialVersionView{}, false, ErrRoundIdempotencyConflict
	}
	view, _, err := scanMaterialVersion(tx.QueryRowContext(ctx, `SELECT `+materialVersionColumns+`
	  FROM opportunity_material_versions WHERE opportunity_id=? AND version=?`, opportunityID, version))
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	return view, true, nil
}

// fenceMaterialRevision enforces the live guards for a new revision: the
// base must be the latest version, its check must still be the latest
// checked check, its opportunity/profile pins must match current state, and
// the workflow must sit at prepared or reviewing.
func fenceMaterialRevision(ctx context.Context, tx *sql.Tx, opportunityID string, base MaterialVersionView, expectedVersion int64) (materialQuestionCount int, workflowRev int64, err error) {
	var latest int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM opportunity_material_versions
	  WHERE opportunity_id=?`, opportunityID).Scan(&latest); err != nil {
		return 0, 0, err
	}
	if expectedVersion != latest || base.Version != latest {
		return 0, 0, ErrConflict
	}
	var checkID, questionSet, checkStatus string
	err = tx.QueryRowContext(ctx, `SELECT id,question_set_sha256,status FROM job_checks
	  WHERE opportunity_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, opportunityID).
		Scan(&checkID, &questionSet, &checkStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrConflict
	}
	if err != nil {
		return 0, 0, err
	}
	if checkStatus != CheckStatusChecked || checkID != base.CheckID || questionSet != base.QuestionSetSHA256 {
		return 0, 0, ErrConflict
	}
	var opportunityRevision, profileRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM opportunities WHERE id=? AND archived_at IS NULL`,
		opportunityID).Scan(&opportunityRevision); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&profileRevision); err != nil {
		return 0, 0, err
	}
	if base.OpportunityRevision != opportunityRevision || base.ProfileRevision != profileRevision {
		return 0, 0, ErrConflict
	}
	workflow, err := scanRoleWorkflow(tx.QueryRowContext(ctx, `SELECT stage,revision,blocked_reason,updated_at
	  FROM role_workflow WHERE opportunity_id=?`, opportunityID), opportunityID, opportunityRevision, "")
	if err != nil {
		return 0, 0, err
	}
	if workflow.Stage != RoleStagePrepared && workflow.Stage != RoleStageReviewing {
		return 0, 0, ErrConflict
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_check_questions WHERE check_id=?`,
		base.CheckID).Scan(&materialQuestionCount); err != nil {
		return 0, 0, err
	}
	return materialQuestionCount, workflow.Revision, nil
}

// insertMaterialRevisionTx creates the new pack row and the new material
// version row, returns the role to prepared, and audits the commit. The new
// pack id is fresh, so no earlier review/send authorization can transfer.
func insertMaterialRevisionTx(ctx context.Context, tx *sql.Tx, actor Actor, opportunityID, requestKey string, pack ApplicationPackMutationInput, base MaterialVersionView, origin string, rewriteOf *int64, refs []MaterialAnswerRef, readiness MaterialReadiness, sourceShas []string, digest, auditOp string, workflowRev int64) (MaterialVersionView, error) {
	if pack.OpportunityID != opportunityID {
		return MaterialVersionView{}, ErrInvalid
	}
	packID, _, err := createApplicationPackTx(ctx, tx, pack)
	if err != nil {
		return MaterialVersionView{}, err
	}
	version := base.Version + 1
	answersJSON, _ := json.Marshal(refs)
	readinessJSON, _ := json.Marshal(readiness)
	sourceShasJSON, _ := json.Marshal(sourceShas)
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO opportunity_material_versions
	  (opportunity_id,version,pack_id,check_id,question_set_sha256,opportunity_revision,
	   profile_revision,workflow_revision,origin,rewrite_of,answers_json,readiness_json,
	   source_shas_json,request_key,request_sha256,actor_kind,actor_id,created_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, opportunityID, version, packID, base.CheckID,
		base.QuestionSetSHA256, base.OpportunityRevision, base.ProfileRevision, workflowRev, origin,
		rewriteOf, string(answersJSON), string(readinessJSON), string(sourceShasJSON),
		requestKey, digest, actor.Kind, actor.ID, now)
	if err != nil {
		return MaterialVersionView{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE role_workflow SET stage=?,revision=?,blocked_reason='',updated_at=?
	  WHERE opportunity_id=?`, RoleStagePrepared, workflowRev+1, now, opportunityID); err != nil {
		return MaterialVersionView{}, err
	}
	auditID, err := randomID()
	if err != nil {
		return MaterialVersionView{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, "system", "role-workflow", "role."+RoleStagePrepared,
		"role_workflow", opportunityID, workflowRev, workflowRev+1, now); err != nil {
		return MaterialVersionView{}, err
	}
	auditID, err = randomID()
	if err != nil {
		return MaterialVersionView{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_changes
	  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
	  VALUES (?,?,?,?,?,?,?,?,?)`, auditID, actor.Kind, actor.ID, auditOp,
		"opportunity_material", opportunityID, base.Version, version, now); err != nil {
		return MaterialVersionView{}, err
	}
	return MaterialVersionView{PackID: packID, Version: version, OpportunityID: opportunityID,
		OpportunityRevision: base.OpportunityRevision, ProfileRevision: base.ProfileRevision,
		CheckID: base.CheckID, QuestionSetSHA256: base.QuestionSetSHA256, Answers: refs,
		Readiness: readiness, Provenance: MaterialProvenance{Origin: origin, SourceShas: sourceShas, RewriteOf: rewriteOf},
		CreatedAt: now, CreatedBy: actor}, nil
}

// missingRevisionBase classifies a missing expected base: a known request
// key means genuinely changed input, existing versions mean a stale or
// wrong base, and no versions at all mean there is nothing to revise.
func missingRevisionBase(ctx context.Context, tx *sql.Tx, opportunityID, requestKey string) error {
	var existing string
	keyErr := tx.QueryRowContext(ctx, `SELECT request_sha256 FROM opportunity_material_versions
	  WHERE opportunity_id=? AND request_key=?`, opportunityID, requestKey).Scan(&existing)
	if keyErr == nil {
		return ErrRoundIdempotencyConflict
	}
	if !errors.Is(keyErr, sql.ErrNoRows) {
		return keyErr
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM opportunity_material_versions
	  WHERE opportunity_id=?`, opportunityID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrConflict
	}
	return ErrNotFound
}

// EditOpportunityMaterials saves one exact owner edit as a new immutable
// version. No LLM is involved: the text is stored and verified byte-for-byte.
// The base version's answer resolution is carried, readiness is re-derived,
// and the role returns to prepared so the changed version needs fresh review.
func (s *Store) EditOpportunityMaterials(ctx context.Context, actor Actor, opportunityID string, input MaterialEditInput) (MaterialVersionView, bool, error) {
	if err := validateMaterialEditInput(actor, opportunityID, input); err != nil {
		return MaterialVersionView{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return MaterialVersionView{}, false, err
	}
	base, _, err := materialRevisionBase(ctx, tx, opportunityID, input.ExpectedVersion)
	if errors.Is(err, ErrNotFound) {
		return MaterialVersionView{}, false, missingRevisionBase(ctx, tx, opportunityID, input.RequestKey)
	}
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	textSHA := materialTextSHA(input.Text)
	refs, readiness, sourceShas := resolveMaterialEditRefs(base, textSHA, input.SourceShas)
	digest := materialRevisionDigest(MaterialOriginDirectEdit, nil, input.ExpectedVersion,
		base.CheckID, base.QuestionSetSHA256, refs, readiness, sourceShas)
	if view, found, err := replayMaterialRevision(ctx, tx, opportunityID, input.RequestKey, digest); err != nil || found {
		return view, false, err
	}
	questionCount, workflowRev, err := fenceMaterialRevision(ctx, tx, opportunityID, base, input.ExpectedVersion)
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	if input.Pack.OpportunityID != opportunityID {
		return MaterialVersionView{}, false, ErrInvalid
	}
	var manifest struct {
		Material *materialEditManifestSection `json:"material"`
	}
	if err := json.Unmarshal(input.Pack.ManifestJSON, &manifest); err != nil || manifest.Material == nil ||
		manifest.Material.CheckID != base.CheckID || manifest.Material.QuestionSetSHA256 != base.QuestionSetSHA256 ||
		manifest.Material.Origin != MaterialOriginDirectEdit || manifest.Material.Text != input.Text ||
		len(manifest.Material.Answers) != questionCount {
		return MaterialVersionView{}, false, ErrInvalid
	}
	view, err := insertMaterialRevisionTx(ctx, tx, actor, opportunityID, input.RequestKey, input.Pack,
		base, MaterialOriginDirectEdit, nil, refs, readiness, sourceShas, digest, "material.edit", workflowRev)
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	return view, true, tx.Commit()
}

// RewriteOpportunityMaterials commits one explicit rewrite as a new
// immutable version. Texts must cover every pinned question exactly once;
// empty text blanks that question, and a required question left blank holds
// the new version. The rewrite linkage pins the rewritten base version.
func (s *Store) RewriteOpportunityMaterials(ctx context.Context, actor Actor, opportunityID string, input MaterialRewriteInput) (MaterialVersionView, bool, error) {
	if err := validateMaterialRewriteInput(actor, opportunityID, input); err != nil {
		return MaterialVersionView{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return MaterialVersionView{}, false, err
	}
	base, questions, err := materialRevisionBase(ctx, tx, opportunityID, input.ExpectedVersion)
	if errors.Is(err, ErrNotFound) {
		return MaterialVersionView{}, false, missingRevisionBase(ctx, tx, opportunityID, input.RequestKey)
	}
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	refs, err := resolveMaterialRewriteRefs(questions, input.Texts)
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	readiness := rewriteReadiness(questions, input.Texts)
	sourceShas := materialSourceShas(input.SourceShas, base.QuestionSetSHA256, refs)
	rewriteOf := base.Version
	digest := materialRevisionDigest(MaterialOriginRewrite, &rewriteOf, input.ExpectedVersion,
		base.CheckID, base.QuestionSetSHA256, refs, readiness, sourceShas)
	if view, found, err := replayMaterialRevision(ctx, tx, opportunityID, input.RequestKey, digest); err != nil || found {
		return view, false, err
	}
	_, workflowRev, err := fenceMaterialRevision(ctx, tx, opportunityID, base, input.ExpectedVersion)
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	if input.Pack.OpportunityID != opportunityID {
		return MaterialVersionView{}, false, ErrInvalid
	}
	var manifest struct {
		Material *materialRewriteManifestSection `json:"material"`
	}
	if err := json.Unmarshal(input.Pack.ManifestJSON, &manifest); err != nil || manifest.Material == nil ||
		manifest.Material.CheckID != base.CheckID || manifest.Material.QuestionSetSHA256 != base.QuestionSetSHA256 ||
		manifest.Material.Origin != MaterialOriginRewrite || len(manifest.Material.Answers) != len(questions) {
		return MaterialVersionView{}, false, ErrInvalid
	}
	byID := make(map[string]string, len(input.Texts))
	for _, text := range input.Texts {
		byID[text.QuestionID] = text.TextSHA256
	}
	for i, entry := range manifest.Material.Answers {
		if entry.QuestionID != questions[i].id || materialTextSHA(entry.Text) != byID[entry.QuestionID] {
			return MaterialVersionView{}, false, ErrInvalid
		}
	}
	view, err := insertMaterialRevisionTx(ctx, tx, actor, opportunityID, input.RequestKey, input.Pack,
		base, MaterialOriginRewrite, &rewriteOf, refs, readiness, sourceShas, digest, "material.rewrite", workflowRev)
	if err != nil {
		return MaterialVersionView{}, false, err
	}
	return view, true, tx.Commit()
}
