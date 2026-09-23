package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

type OrganisationSnapshot struct {
	IngestionID       string
	OpportunityID     string
	SourceID          string
	SourceURL         string
	OriginalText      string
	SourceRecordedAt  string
	SourceRevision    string
	SourceFingerprint string
	CategorySet       OrganisationCategorySet
}

func (s *Store) OrganisationSnapshotForJob(ctx context.Context, claim Job) (OrganisationSnapshot, error) {
	if claim.Kind != OrganisationJobKind || claim.ID == "" || claim.LeaseToken == "" {
		return OrganisationSnapshot{}, ErrInvalid
	}
	var payload organisationJobPayload
	if err := json.Unmarshal(claim.Payload, &payload); err != nil || payload.IngestionID == "" ||
		payload.OpportunityID == "" || payload.CategoryVersion < 1 || len(payload.SourceFingerprint) != 64 {
		return OrganisationSnapshot{}, ErrInvalid
	}
	var snapshot OrganisationSnapshot
	var companyID, kind, categoryJSON string
	err := s.db.QueryRowContext(ctx, `SELECT i.id,o.id,i.source_id,COALESCE(o.source_url,''),o.original_text,
  src.recorded_at,src.content_sha256,o.company_id,o.kind,v.version,v.categories_json,v.created_at,v.actor_kind,v.actor_id
  FROM ingestion_requests i JOIN opportunities o ON o.id=i.opportunity_id
  JOIN evidence_sources src ON src.id=i.source_id
  JOIN organisation_categories_current c ON c.singleton=1
  JOIN organisation_category_versions v ON v.version=c.version
  JOIN jobs j ON j.id=i.organisation_job_id
  WHERE i.id=? AND o.id=? AND i.source_id IS NOT NULL AND o.archived_at IS NULL
    AND j.id=? AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?`,
		payload.IngestionID, payload.OpportunityID, claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now())).
		Scan(&snapshot.IngestionID, &snapshot.OpportunityID, &snapshot.SourceID, &snapshot.SourceURL, &snapshot.OriginalText,
			&snapshot.SourceRecordedAt, &snapshot.SourceRevision, &companyID, &kind, &snapshot.CategorySet.Version,
			&categoryJSON, &snapshot.CategorySet.CreatedAt, &snapshot.CategorySet.Actor.Kind, &snapshot.CategorySet.Actor.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return OrganisationSnapshot{}, ErrConflict
	}
	if err != nil {
		return OrganisationSnapshot{}, err
	}
	if err = json.Unmarshal([]byte(categoryJSON), &snapshot.CategorySet.Categories); err != nil {
		return OrganisationSnapshot{}, err
	}
	if snapshot.CategorySet.Version != payload.CategoryVersion || len(snapshot.CategorySet.Categories) == 0 ||
		snapshot.SourceRevision != sourceDigest(snapshot.OriginalText) {
		return OrganisationSnapshot{}, ErrConflict
	}
	snapshot.SourceFingerprint = organisationSourceFingerprint(companyID, kind, snapshot.SourceURL, snapshot.OriginalText, snapshot.SourceID)
	if snapshot.SourceFingerprint != payload.SourceFingerprint {
		return OrganisationSnapshot{}, ErrConflict
	}
	return snapshot, nil
}

type OrganisationAssessment struct {
	ID                string
	OpportunityID     string
	IngestionID       string
	JobID             string
	CategoryVersion   int64
	SourceFingerprint string
	InputSHA256       string
	Disposition       string
	CategoryID        string
	RequestedModel    string
	ReturnedModel     string
	InputJSON         json.RawMessage
	ResultJSON        json.RawMessage
	SourceRefsJSON    json.RawMessage
	CreatedAt         string
}

func organisationInputDigest(input jev.OrganisationInput) (string, []byte, error) {
	canonical := input
	canonical.Categories = append([]jev.OrganisationCategory(nil), input.Categories...)
	canonical.Facts = append([]jev.OrganisationFact(nil), input.Facts...)
	sort.Slice(canonical.Categories, func(i, j int) bool { return canonical.Categories[i].ID < canonical.Categories[j].ID })
	sort.Slice(canonical.Facts, func(i, j int) bool { return canonical.Facts[i].ID < canonical.Facts[j].ID })
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), encoded, nil
}

// ApplyOrganisation rechecks category definitions, source digest and job lease
// after the provider call. Only category organisation changes; qualification,
// application stage and evidence are untouched.
func (s *Store) ApplyOrganisation(ctx context.Context, claim Job, input jev.OrganisationInput, result jev.OrganisationResult) (OrganisationAssessment, error) {
	if claim.Kind != OrganisationJobKind || claim.ID == "" || claim.LeaseToken == "" {
		return OrganisationAssessment{}, ErrInvalid
	}
	digest, inputJSON, err := organisationInputDigest(input)
	if err != nil {
		return OrganisationAssessment{}, err
	}
	if result.InputSHA256 != digest || result.CategorySetVersion != input.CategorySetVersion ||
		result.RequestedModel == "" || result.ReturnedModel == "" ||
		(result.Disposition != jev.OrganisationCategorySelected && result.Disposition != jev.OrganisationUncertain) ||
		(result.Disposition == jev.OrganisationUncertain) != (result.CategoryID == "") {
		return OrganisationAssessment{}, ErrInvalid
	}
	snapshot, err := s.OrganisationSnapshotForJob(ctx, claim)
	if err != nil {
		return OrganisationAssessment{}, err
	}
	if input.CategorySetVersion != snapshot.CategorySet.Version || len(input.Categories) != len(snapshot.CategorySet.Categories) ||
		len(input.Facts) < 1 || len(input.Facts) > 16 || len(result.SourceRefs) != len(input.Facts) {
		return OrganisationAssessment{}, ErrConflict
	}
	currentCategories := map[string]string{}
	for _, category := range snapshot.CategorySet.Categories {
		currentCategories[category.ID] = category.Description
	}
	for _, category := range input.Categories {
		if currentCategories[category.ID] != category.Description {
			return OrganisationAssessment{}, ErrConflict
		}
	}
	if result.CategoryID != "" {
		if _, ok := currentCategories[result.CategoryID]; !ok {
			return OrganisationAssessment{}, ErrInvalid
		}
	}
	refMap := map[string]jev.OrganisationSourceRef{}
	for _, ref := range result.SourceRefs {
		if _, exists := refMap[ref.FactID]; exists {
			return OrganisationAssessment{}, ErrInvalid
		}
		refMap[ref.FactID] = ref
	}
	for _, fact := range input.Facts {
		if fact.SourceID != snapshot.SourceID || fact.SourceRevision != snapshot.SourceRevision ||
			fact.SourceKind != "vacancy_snapshot" || !strings.Contains(snapshot.OriginalText, fact.Excerpt) ||
			refMap[fact.ID] != (jev.OrganisationSourceRef{FactID: fact.ID, SourceID: fact.SourceID,
				SourceRevision: fact.SourceRevision, SourceKind: fact.SourceKind}) {
			return OrganisationAssessment{}, ErrInvalid
		}
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return OrganisationAssessment{}, err
	}
	refsJSON, err := json.Marshal(result.SourceRefs)
	if err != nil {
		return OrganisationAssessment{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OrganisationAssessment{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET id=id WHERE id=?`, snapshot.IngestionID); err != nil {
		return OrganisationAssessment{}, err
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM organisation_assessments WHERE job_id=?`, claim.ID).Scan(&existingID)
	if err == nil {
		return scanOrganisationAssessment(tx.QueryRowContext(ctx, `SELECT `+organisationAssessmentColumns+` FROM organisation_assessments WHERE id=?`, existingID))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OrganisationAssessment{}, err
	}
	// Recheck against the current record and owner category set while holding the writer.
	var companyID, kind, url, text, sourceID string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT o.company_id,o.kind,COALESCE(o.source_url,''),o.original_text,
  i.source_id,c.version FROM ingestion_requests i JOIN opportunities o ON o.id=i.opportunity_id
  JOIN organisation_categories_current c ON c.singleton=1 WHERE i.id=? AND o.id=?
  AND i.organisation_job_id=?`, snapshot.IngestionID, snapshot.OpportunityID, claim.ID).
		Scan(&companyID, &kind, &url, &text, &sourceID, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return OrganisationAssessment{}, ErrConflict
	}
	if err != nil {
		return OrganisationAssessment{}, err
	}
	if version != snapshot.CategorySet.Version || organisationSourceFingerprint(companyID, kind, url, text, sourceID) != snapshot.SourceFingerprint {
		return OrganisationAssessment{}, ErrConflict
	}
	var live int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id=? AND state='running' AND lease_token=?
  AND attempt_count=? AND lease_until>?`, claim.ID, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now())).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) {
		return OrganisationAssessment{}, ErrConflict
	}
	if err != nil {
		return OrganisationAssessment{}, err
	}
	id, err := randomID()
	if err != nil {
		return OrganisationAssessment{}, err
	}
	assessment := OrganisationAssessment{ID: id, OpportunityID: snapshot.OpportunityID, IngestionID: snapshot.IngestionID,
		JobID: claim.ID, CategoryVersion: version, SourceFingerprint: snapshot.SourceFingerprint, InputSHA256: digest,
		Disposition: string(result.Disposition), CategoryID: result.CategoryID, RequestedModel: result.RequestedModel,
		ReturnedModel: result.ReturnedModel, InputJSON: inputJSON, ResultJSON: resultJSON, SourceRefsJSON: refsJSON, CreatedAt: utcNow()}
	_, err = tx.ExecContext(ctx, `INSERT INTO organisation_assessments
  (id,opportunity_id,ingestion_id,job_id,category_version,source_fingerprint,input_sha256,
   disposition,category_id,requested_model,returned_model,input_json,result_json,source_refs_json,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, assessment.ID, assessment.OpportunityID, assessment.IngestionID,
		assessment.JobID, assessment.CategoryVersion, assessment.SourceFingerprint, assessment.InputSHA256,
		assessment.Disposition, optionalText(assessment.CategoryID), assessment.RequestedModel, assessment.ReturnedModel,
		string(assessment.InputJSON), string(assessment.ResultJSON), string(assessment.SourceRefsJSON), assessment.CreatedAt)
	if err != nil {
		return OrganisationAssessment{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO organisation_current(opportunity_id,assessment_id) VALUES (?,?)
  ON CONFLICT(opportunity_id) DO UPDATE SET assessment_id=excluded.assessment_id`, assessment.OpportunityID, assessment.ID)
	if err != nil {
		return OrganisationAssessment{}, err
	}
	auditID, err := randomID()
	if err != nil {
		return OrganisationAssessment{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,occurred_at)
  VALUES (?,?,?,?,?,?,?)`, auditID, "system", "organisation", "organisation.apply", "organisation_assessment", assessment.ID, utcNow())
	if err != nil {
		return OrganisationAssessment{}, err
	}
	if err = tx.Commit(); err != nil {
		return OrganisationAssessment{}, err
	}
	return assessment, nil
}

const organisationAssessmentColumns = `id,opportunity_id,ingestion_id,job_id,category_version,
  source_fingerprint,input_sha256,disposition,category_id,requested_model,returned_model,
  input_json,result_json,source_refs_json,created_at`

func scanOrganisationAssessment(row rowScanner) (OrganisationAssessment, error) {
	var assessment OrganisationAssessment
	var categoryID sql.NullString
	var inputJSON, resultJSON, refsJSON string
	err := row.Scan(&assessment.ID, &assessment.OpportunityID, &assessment.IngestionID, &assessment.JobID,
		&assessment.CategoryVersion, &assessment.SourceFingerprint, &assessment.InputSHA256, &assessment.Disposition,
		&categoryID, &assessment.RequestedModel, &assessment.ReturnedModel, &inputJSON, &resultJSON, &refsJSON, &assessment.CreatedAt)
	if err != nil {
		return OrganisationAssessment{}, err
	}
	assessment.CategoryID = categoryID.String
	assessment.InputJSON, assessment.ResultJSON, assessment.SourceRefsJSON = json.RawMessage(inputJSON), json.RawMessage(resultJSON), json.RawMessage(refsJSON)
	return assessment, nil
}

type OrganisationView struct {
	Status           string // unconfigured, pending, processing, selected, uncertain, failed, outdated
	Current          *OrganisationAssessment
	LatestHistorical *OrganisationAssessment
	JobID            string
}

func (s *Store) Organisation(ctx context.Context, opportunityID string) (OrganisationView, error) {
	if opportunityID == "" {
		return OrganisationView{}, ErrInvalid
	}
	view := OrganisationView{Status: "unconfigured"}
	set, err := s.CurrentOrganisationCategories(ctx)
	if err != nil {
		return view, err
	}
	if set.Version == 0 || len(set.Categories) == 0 {
		return view, nil
	}
	view.Status = "pending"
	latest, err := scanOrganisationAssessment(s.db.QueryRowContext(ctx, `SELECT `+organisationAssessmentColumns+`
  FROM organisation_assessments WHERE opportunity_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, opportunityID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	if err == nil {
		view.LatestHistorical = &latest
		view.Status = "outdated"
	}
	var sourceID, companyID, kind, url, text, jobID, jobState, jobResult string
	err = s.db.QueryRowContext(ctx, `SELECT i.source_id,o.company_id,o.kind,COALESCE(o.source_url,''),o.original_text,
  COALESCE(i.organisation_job_id,''),COALESCE(j.state,''),COALESCE(j.result_ref,'') FROM ingestion_requests i
  JOIN opportunities o ON o.id=i.opportunity_id LEFT JOIN jobs j ON j.id=i.organisation_job_id
  WHERE o.id=? AND i.source_id IS NOT NULL
  ORDER BY i.created_at DESC,i.id DESC LIMIT 1`, opportunityID).
		Scan(&sourceID, &companyID, &kind, &url, &text, &jobID, &jobState, &jobResult)
	if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	view.JobID = jobID
	fingerprint := organisationSourceFingerprint(companyID, kind, url, text, sourceID)
	current, err := scanOrganisationAssessment(s.db.QueryRowContext(ctx, `SELECT a.id,a.opportunity_id,a.ingestion_id,a.job_id,
  a.category_version,a.source_fingerprint,a.input_sha256,a.disposition,a.category_id,
  a.requested_model,a.returned_model,a.input_json,a.result_json,a.source_refs_json,a.created_at
  FROM organisation_current c JOIN organisation_assessments a ON a.id=c.assessment_id
  WHERE c.opportunity_id=? AND a.category_version=? AND a.source_fingerprint=?`, opportunityID, set.Version, fingerprint))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	if err == nil {
		view.Current = &current
		if current.Disposition == "uncertain" {
			view.Status = "uncertain"
		} else {
			view.Status = "selected"
		}
		return view, nil
	}
	switch JobState(jobState) {
	case JobRunning:
		view.Status = "processing"
	case JobFailed, JobCancelled:
		view.Status = "failed"
	case JobQueued:
		view.Status = "pending"
	case JobSucceeded:
		if jobResult == "outdated" {
			view.Status = "outdated"
		}
	}
	return view, nil
}

type OrganisationSummary struct {
	OpportunityID string
	Status        string
	CategoryID    string
}

// OrganisationSummaries returns current category/status for at most one page
// of opportunities with one bulk record query. Missing IDs are omitted.
func (s *Store) OrganisationSummaries(ctx context.Context, opportunityIDs []string) (map[string]OrganisationSummary, error) {
	if len(opportunityIDs) == 0 || len(opportunityIDs) > 100 {
		return nil, ErrInvalid
	}
	for _, id := range opportunityIDs {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, ErrInvalid
		}
	}
	encodedIDs, err := json.Marshal(opportunityIDs)
	if err != nil {
		return nil, err
	}
	set, err := s.CurrentOrganisationCategories(ctx)
	if err != nil {
		return nil, err
	}
	configured := set.Version > 0 && len(set.Categories) > 0
	rows, err := s.db.QueryContext(ctx, `SELECT o.id,o.company_id,o.kind,COALESCE(o.source_url,''),o.original_text,
  COALESCE(i.source_id,''),COALESCE(j.state,''),COALESCE(j.result_ref,''),
  COALESCE(a.category_version,0),COALESCE(a.source_fingerprint,''),COALESCE(a.disposition,''),
  COALESCE(a.category_id,''),EXISTS(SELECT 1 FROM organisation_assessments h WHERE h.opportunity_id=o.id)
  FROM opportunities o
  LEFT JOIN ingestion_requests i ON i.id=(SELECT i2.id FROM ingestion_requests i2
    WHERE i2.opportunity_id=o.id AND i2.source_id IS NOT NULL
    ORDER BY i2.created_at DESC,i2.id DESC LIMIT 1)
  LEFT JOIN jobs j ON j.id=i.organisation_job_id
  LEFT JOIN organisation_current c ON c.opportunity_id=o.id
  LEFT JOIN organisation_assessments a ON a.id=c.assessment_id
  WHERE o.id IN (SELECT value FROM json_each(?))`, string(encodedIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make(map[string]OrganisationSummary, len(opportunityIDs))
	for rows.Next() {
		var summary OrganisationSummary
		var companyID, kind, url, originalText, sourceID, jobState, jobResult string
		var categoryVersion int64
		var fingerprint, disposition, categoryID string
		var historical bool
		if err := rows.Scan(&summary.OpportunityID, &companyID, &kind, &url, &originalText,
			&sourceID, &jobState, &jobResult, &categoryVersion, &fingerprint, &disposition,
			&categoryID, &historical); err != nil {
			return nil, err
		}
		summary.Status = "unconfigured"
		if configured {
			summary.Status = "pending"
			if historical {
				summary.Status = "outdated"
			}
			if sourceID != "" && categoryVersion == set.Version &&
				fingerprint == organisationSourceFingerprint(companyID, kind, url, originalText, sourceID) {
				summary.Status = "selected"
				if disposition == "uncertain" {
					summary.Status = "uncertain"
				} else {
					summary.CategoryID = categoryID
				}
			} else {
				switch JobState(jobState) {
				case JobRunning:
					summary.Status = "processing"
				case JobFailed, JobCancelled:
					summary.Status = "failed"
				case JobQueued:
					summary.Status = "pending"
				case JobSucceeded:
					if jobResult == "outdated" {
						summary.Status = "outdated"
					}
				}
			}
		}
		summaries[summary.OpportunityID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}
