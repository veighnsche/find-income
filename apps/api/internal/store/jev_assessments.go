package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

type RoundAssessmentInput struct {
	Actor               Actor
	RoundID             string
	RoundGeneration     int64
	ResourceID          string
	OpportunityID       string
	OpportunityRevision int64
	SourceID            string
	SourceRevision      string
	ProfileVersion      int64
	JevAttemptIDs       []string
	OmittedBytes        int
}

type RoundJevAssessment struct {
	ID                  string
	RoundID             string
	OpportunityID       string
	Kind                string
	SourceID            string
	SourceRevision      string
	OpportunityRevision int64
	ProfileVersion      int64
	CategoryVersion     int64
	RoundGeneration     int64
	InputSHA256         string
	InputJSON           []byte
	ResultJSON          []byte
	JevAttemptIDs       []string
	OmittedBytes        int
	Status              string
	CreatedAt           string
}

// RoundOrganisationProjection is the current round proposal expressed in the
// existing organisation read vocabulary. Its category description is the
// definition captured with the assessment, not a later category edit.
type RoundOrganisationProjection struct {
	Assessment          RoundJevAssessment
	Status              string // selected or uncertain
	CategoryID          string
	CategoryDescription string
}

const roundJevAssessmentColumns = `id,round_id,opportunity_id,kind,source_id,source_revision,
 opportunity_revision,profile_version,COALESCE(category_version,0),round_generation,input_sha256,
 input_json,result_json,jev_attempt_ids_json,omitted_bytes,status,created_at`

func scanRoundJevAssessment(row rowScanner) (RoundJevAssessment, error) {
	var a RoundJevAssessment
	var input, result, attempts string
	err := row.Scan(&a.ID, &a.RoundID, &a.OpportunityID, &a.Kind, &a.SourceID, &a.SourceRevision,
		&a.OpportunityRevision, &a.ProfileVersion, &a.CategoryVersion, &a.RoundGeneration, &a.InputSHA256,
		&input, &result, &attempts, &a.OmittedBytes, &a.Status, &a.CreatedAt)
	if err != nil {
		return RoundJevAssessment{}, err
	}
	a.InputJSON, a.ResultJSON = []byte(input), []byte(result)
	if err := json.Unmarshal([]byte(attempts), &a.JevAttemptIDs); err != nil {
		return RoundJevAssessment{}, err
	}
	return a, nil
}

// JevAttemptIDsForRequestPrefix returns at most the two separately charged
// calls (factual and dependent support) under exact stable request keys.
func (s *Store) JevAttemptIDsForRequestPrefix(ctx context.Context, roundID, prefix string) ([]string, error) {
	if !validJevText(roundID, 128) || !validJevText(prefix, 190) {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ja.id FROM jev_attempts ja JOIN round_attempts ra ON ra.id=ja.round_attempt_id
 WHERE ja.round_id=? AND ra.request_key IN (?,?) ORDER BY ra.request_key`, roundID, prefix+"/0", prefix+"/1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func validRoundAssessmentBinding(b RoundAssessmentInput) bool {
	if !requiredActor(b.Actor) || !validJevText(b.RoundID, 128) || b.RoundGeneration < 1 ||
		!validJevText(b.ResourceID, 300) || !validJevText(b.OpportunityID, 128) || b.OpportunityRevision < 1 ||
		!validJevText(b.SourceID, 128) || len(b.SourceRevision) != 64 || b.ProfileVersion < 1 ||
		b.OmittedBytes < 0 || b.OmittedBytes > 200000 || len(b.JevAttemptIDs) < 1 || len(b.JevAttemptIDs) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range b.JevAttemptIDs {
		if !validJevText(id, 128) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *Store) applyRoundJevAssessment(ctx context.Context, binding RoundAssessmentInput, kind, digest string,
	inputJSON, resultJSON, refsJSON, candidatesJSON []byte, excerpts []string, expectedCalls int, categoryVersion int64, status string) (RoundJevAssessment, error) {
	if !validRoundAssessmentBinding(binding) || (kind != "screening" && kind != "organisation") ||
		len(digest) != 64 || !validJevJSON(inputJSON, 256<<10) || !validJevJSON(resultJSON, 256<<10) ||
		!validJevJSON(refsJSON, 256<<10) || !validJevJSON(candidatesJSON, 256<<10) ||
		expectedCalls != len(binding.JevAttemptIDs) || status == "" {
		return RoundJevAssessment{}, ErrInvalid
	}
	tx, round, err := s.roundWriter(ctx, binding.RoundID)
	if err != nil {
		return RoundJevAssessment{}, err
	}
	defer tx.Rollback()
	if err := requireRoundState(round, RoundRunning); err != nil {
		return RoundJevAssessment{}, err
	}
	if round.Generation != binding.RoundGeneration || round.ProfileVersion != binding.ProfileVersion ||
		!scopeAllowsActor(round.Scope, binding.Actor) || !scopeAllows(round.Scope, RoundJevRequest, binding.ResourceID) {
		return RoundJevAssessment{}, ErrFenced
	}
	var currentProfile int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentProfile); err != nil {
		return RoundJevAssessment{}, err
	}
	if currentProfile != binding.ProfileVersion {
		return RoundJevAssessment{}, ErrConflict
	}
	if kind == "screening" {
		var definitions string
		if err := tx.QueryRowContext(ctx, `SELECT role_criteria_json FROM preferences_versions WHERE version=?`, currentProfile).Scan(&definitions); err != nil {
			return RoundJevAssessment{}, err
		}
		var current []RoleCriterion
		var supplied []jev.ScreeningCriterion
		if json.Unmarshal([]byte(definitions), &current) != nil || json.Unmarshal(candidatesJSON, &supplied) != nil || len(current) != len(supplied) {
			return RoundJevAssessment{}, ErrInvalid
		}
		byID := make(map[string]RoleCriterion, len(current))
		for _, c := range current {
			byID[c.ID] = c
		}
		for _, c := range supplied {
			have, ok := byID[c.ID]
			if !ok || have.Label != c.Label || have.Description != c.Description || have.Kind != c.Kind || have.Mode != c.Mode {
				return RoundJevAssessment{}, ErrConflict
			}
			delete(byID, c.ID)
		}
		if len(byID) != 0 {
			return RoundJevAssessment{}, ErrConflict
		}
	}
	var opportunityRevision int64
	var originalText string
	err = tx.QueryRowContext(ctx, `SELECT revision,original_text FROM opportunities WHERE id=? AND archived_at IS NULL`, binding.OpportunityID).Scan(&opportunityRevision, &originalText)
	if errors.Is(err, sql.ErrNoRows) {
		return RoundJevAssessment{}, ErrNotFound
	}
	if err != nil {
		return RoundJevAssessment{}, err
	}
	if opportunityRevision != binding.OpportunityRevision {
		return RoundJevAssessment{}, ErrConflict
	}
	var latestSourceID, sourceRevision, sourceText string
	err = tx.QueryRowContext(ctx, `SELECT id,content_sha256,original_text FROM evidence_sources
 WHERE opportunity_id=? AND source_kind='vacancy_snapshot' ORDER BY context_version DESC,recorded_at DESC,id DESC LIMIT 1`, binding.OpportunityID).Scan(&latestSourceID, &sourceRevision, &sourceText)
	if errors.Is(err, sql.ErrNoRows) {
		return RoundJevAssessment{}, ErrNotFound
	}
	if err != nil {
		return RoundJevAssessment{}, err
	}
	if latestSourceID != binding.SourceID || sourceRevision != binding.SourceRevision || sourceText != originalText {
		return RoundJevAssessment{}, ErrConflict
	}
	for _, excerpt := range excerpts {
		if excerpt == "" || !strings.Contains(sourceText, excerpt) {
			return RoundJevAssessment{}, ErrInvalid
		}
	}
	for _, id := range binding.JevAttemptIDs {
		var attemptRound, sourceRefs, candidates, rubric, purpose, status, operation, roundState string
		var attemptProfile, attemptGeneration int64
		err = tx.QueryRowContext(ctx, `SELECT ja.round_id,ja.source_refs_json,ja.candidate_set_json,ja.rubric_version,
 ja.purpose,ja.status,ja.profile_version,ra.operation,ra.state,ra.generation
 FROM jev_attempts ja JOIN round_attempts ra ON ra.id=ja.round_attempt_id WHERE ja.id=?`, id).Scan(
			&attemptRound, &sourceRefs, &candidates, &rubric, &purpose, &status, &attemptProfile, &operation, &roundState, &attemptGeneration)
		if errors.Is(err, sql.ErrNoRows) {
			return RoundJevAssessment{}, ErrNotFound
		}
		if err != nil {
			return RoundJevAssessment{}, err
		}
		if attemptRound != binding.RoundID || attemptProfile != binding.ProfileVersion || attemptGeneration != round.Generation ||
			operation != RoundJevRequest || roundState != string(AttemptSucceeded) || status != "succeeded" ||
			sourceRefs != string(refsJSON) || candidates != string(candidatesJSON) {
			return RoundJevAssessment{}, ErrFenced
		}
		if kind == "screening" && (rubric != "screening-v2" || purpose != "responsibility_screening") ||
			kind == "organisation" && (rubric != "organisation-v1" || purpose != "organisation") {
			return RoundJevAssessment{}, ErrInvalid
		}
	}
	if kind == "organisation" {
		var currentCategoryVersion int64
		var definitions string
		if err := tx.QueryRowContext(ctx, `SELECT v.version,v.categories_json FROM organisation_categories_current c JOIN organisation_category_versions v ON v.version=c.version WHERE c.singleton=1`).Scan(&currentCategoryVersion, &definitions); err != nil {
			return RoundJevAssessment{}, err
		}
		if currentCategoryVersion != categoryVersion {
			return RoundJevAssessment{}, ErrConflict
		}
		var current []OrganisationCategory
		var supplied []jev.OrganisationCategory
		if json.Unmarshal([]byte(definitions), &current) != nil || json.Unmarshal(candidatesJSON, &supplied) != nil || len(current) != len(supplied) {
			return RoundJevAssessment{}, ErrInvalid
		}
		byID := map[string]string{}
		for _, c := range current {
			byID[c.ID] = c.Description
		}
		for _, c := range supplied {
			if byID[c.ID] != c.Description {
				return RoundJevAssessment{}, ErrConflict
			}
			delete(byID, c.ID)
		}
		if len(byID) != 0 {
			return RoundJevAssessment{}, ErrConflict
		}
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM round_jev_assessments WHERE round_id=? AND opportunity_id=? AND kind=? AND input_sha256=? ORDER BY created_at,id LIMIT 1`, binding.RoundID, binding.OpportunityID, kind, digest).Scan(&existingID)
	if err == nil {
		a, scanErr := scanRoundJevAssessment(tx.QueryRowContext(ctx, `SELECT `+roundJevAssessmentColumns+` FROM round_jev_assessments WHERE id=?`, existingID))
		if scanErr != nil {
			return RoundJevAssessment{}, scanErr
		}
		if a.SourceID != binding.SourceID || a.SourceRevision != binding.SourceRevision ||
			a.OpportunityRevision != binding.OpportunityRevision || a.ProfileVersion != binding.ProfileVersion ||
			a.CategoryVersion != categoryVersion || a.RoundGeneration != binding.RoundGeneration ||
			a.OmittedBytes != binding.OmittedBytes || a.Status != status ||
			!reflect.DeepEqual(a.JevAttemptIDs, binding.JevAttemptIDs) ||
			!reflect.DeepEqual(a.InputJSON, inputJSON) || !reflect.DeepEqual(a.ResultJSON, resultJSON) {
			return RoundJevAssessment{}, ErrConflict
		}
		return a, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RoundJevAssessment{}, err
	}
	id, err := randomID()
	if err != nil {
		return RoundJevAssessment{}, err
	}
	attemptIDs, _ := json.Marshal(binding.JevAttemptIDs)
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO round_jev_assessments
 (id,round_id,opportunity_id,kind,source_id,source_revision,opportunity_revision,profile_version,
 category_version,round_generation,input_sha256,input_json,result_json,jev_attempt_ids_json,omitted_bytes,status,created_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, binding.RoundID, binding.OpportunityID, kind, binding.SourceID, binding.SourceRevision,
		binding.OpportunityRevision, binding.ProfileVersion, nullablePositive(categoryVersion), round.Generation, digest, string(inputJSON), string(resultJSON), string(attemptIDs), binding.OmittedBytes, status, now)
	if err != nil {
		return RoundJevAssessment{}, err
	}
	if status != "incomplete_source" {
		_, err = tx.ExecContext(ctx, `INSERT INTO round_jev_current(opportunity_id,kind,assessment_id) VALUES (?,?,?)
 ON CONFLICT(opportunity_id,kind) DO UPDATE SET assessment_id=excluded.assessment_id`, binding.OpportunityID, kind, id)
		if err != nil {
			return RoundJevAssessment{}, err
		}
	}
	a, err := scanRoundJevAssessment(tx.QueryRowContext(ctx, `SELECT `+roundJevAssessmentColumns+` FROM round_jev_assessments WHERE id=?`, id))
	if err != nil {
		return RoundJevAssessment{}, err
	}
	return a, tx.Commit()
}

func (s *Store) ApplyRoundScreening(ctx context.Context, binding RoundAssessmentInput, input jev.ScreeningInput, result jev.ScreeningResult) (RoundJevAssessment, error) {
	digest, err := jev.ScreeningInputSHA256(input)
	if err != nil {
		return RoundJevAssessment{}, ErrInvalid
	}
	if digest != result.InputSHA256 || result.PreferenceVersion != binding.ProfileVersion || result.RubricVersion != jev.ScreeningRubricVersion ||
		input.PreferenceVersion != binding.ProfileVersion || len(result.Observations) != len(input.Criteria) || len(input.Spans) == 0 {
		return RoundJevAssessment{}, ErrInvalid
	}
	criteria := map[string]jev.ScreeningCriterion{}
	for _, c := range input.Criteria {
		criteria[c.ID] = c
	}
	for _, o := range result.Observations {
		criterion, ok := criteria[o.CriterionID]
		if !ok {
			return RoundJevAssessment{}, ErrInvalid
		}
		encoded, _ := json.Marshal(criterion)
		if sourceDigest(string(encoded)) != o.DefinitionSHA256 {
			return RoundJevAssessment{}, ErrInvalid
		}
		delete(criteria, o.CriterionID)
		if o.Scope != jev.ScopePresent && o.Scope != jev.ScopeExplicitAbsent && o.Scope != jev.ScopeMentionOnly &&
			o.Scope != jev.ScopeAmbiguous && o.Scope != jev.ScopeConflicting && o.Scope != jev.ScopeNoRelevantEvidence {
			return RoundJevAssessment{}, ErrInvalid
		}
		if o.SupportState != jev.SupportProposed && o.SupportState != jev.SupportMissing && o.SupportState != jev.SupportNotRequested ||
			o.Scope == jev.ScopeNoRelevantEvidence && o.SupportState != jev.SupportNotRequested ||
			o.Scope != jev.ScopeNoRelevantEvidence && o.SupportState == jev.SupportNotRequested ||
			o.SupportState == jev.SupportProposed && len(o.ProposedSupport) == 0 ||
			len(o.ProposedSupport) > 2 || len(o.ProposedSupport) == 2 && o.Scope != jev.ScopeConflicting ||
			o.SupportState == jev.SupportMissing && len(o.ProposedSupport) != 0 ||
			o.SupportState == jev.SupportNotRequested && (o.Scope != jev.ScopeNoRelevantEvidence || len(o.ProposedSupport) != 0) {
			return RoundJevAssessment{}, ErrInvalid
		}
		seenSupport := map[string]bool{}
		for _, ref := range o.ProposedSupport {
			if seenSupport[ref.SpanID] {
				return RoundJevAssessment{}, ErrInvalid
			}
			seenSupport[ref.SpanID] = true
			found := false
			for _, span := range input.Spans {
				if ref.SpanID == span.ID && ref.SourceID == span.SourceID && ref.SourceRevision == span.SourceRevision && ref.SourceKind == span.SourceKind {
					found = true
					break
				}
			}
			if !found {
				return RoundJevAssessment{}, ErrInvalid
			}
		}
	}
	if len(criteria) != 0 {
		return RoundJevAssessment{}, ErrInvalid
	}
	for _, span := range input.Spans {
		if span.SourceID != binding.SourceID || span.SourceRevision != binding.SourceRevision {
			return RoundJevAssessment{}, ErrInvalid
		}
	}
	if len(result.InputSpans) != len(input.Spans) {
		return RoundJevAssessment{}, ErrInvalid
	}
	inputRefs := make(map[string]jev.ScreeningSupport, len(input.Spans))
	for _, span := range input.Spans {
		ref := jev.ScreeningSupport{SpanID: span.ID, SourceID: span.SourceID, SourceRevision: span.SourceRevision, SourceKind: span.SourceKind}
		inputRefs[span.ID] = ref
	}
	for _, ref := range result.InputSpans {
		if inputRefs[ref.SpanID] != ref {
			return RoundJevAssessment{}, ErrInvalid
		}
		delete(inputRefs, ref.SpanID)
	}
	if len(inputRefs) != 0 {
		return RoundJevAssessment{}, ErrInvalid
	}
	inputJSON, _ := json.Marshal(input)
	resultJSON, _ := json.Marshal(result)
	refs, _ := json.Marshal(input.Spans)
	candidates, _ := json.Marshal(input.Criteria)
	status := "proposed"
	if binding.OmittedBytes > 0 {
		status = "incomplete_source"
	} else {
		for _, o := range result.Observations {
			if o.SupportState != jev.SupportProposed || o.Scope == jev.ScopeAmbiguous || o.Scope == jev.ScopeConflicting {
				status = "unresolved"
				break
			}
		}
	}
	expected := 1
	for _, o := range result.Observations {
		if o.Scope != jev.ScopeNoRelevantEvidence {
			expected = 2
			break
		}
	}
	excerpts := make([]string, 0, len(input.Spans))
	for _, span := range input.Spans {
		excerpts = append(excerpts, span.Excerpt)
	}
	return s.applyRoundJevAssessment(ctx, binding, "screening", digest, inputJSON, resultJSON, refs, candidates, excerpts, expected, 0, status)
}

func (s *Store) ApplyRoundOrganisation(ctx context.Context, binding RoundAssessmentInput, input jev.OrganisationInput, result jev.OrganisationResult) (RoundJevAssessment, error) {
	digest, inputJSON, err := organisationInputDigest(input)
	if err != nil {
		return RoundJevAssessment{}, ErrInvalid
	}
	if digest != result.InputSHA256 || result.CategorySetVersion != input.CategorySetVersion || result.CategorySetVersion < 1 ||
		len(input.Facts) == 0 || len(result.SourceRefs) != len(input.Facts) ||
		(result.Disposition != jev.OrganisationCategorySelected && result.Disposition != jev.OrganisationUncertain) {
		return RoundJevAssessment{}, ErrInvalid
	}
	if result.Disposition == jev.OrganisationUncertain && result.CategoryID != "" || result.Disposition == jev.OrganisationCategorySelected && result.CategoryID == "" {
		return RoundJevAssessment{}, ErrInvalid
	}
	for _, fact := range input.Facts {
		if fact.SourceID != binding.SourceID || fact.SourceRevision != binding.SourceRevision {
			return RoundJevAssessment{}, ErrInvalid
		}
	}
	categoryFound := result.CategoryID == ""
	for _, category := range input.Categories {
		if category.ID == result.CategoryID {
			categoryFound = true
			break
		}
	}
	if !categoryFound {
		return RoundJevAssessment{}, ErrInvalid
	}
	refByID := map[string]jev.OrganisationSourceRef{}
	for _, ref := range result.SourceRefs {
		if _, exists := refByID[ref.FactID]; exists {
			return RoundJevAssessment{}, ErrInvalid
		}
		refByID[ref.FactID] = ref
	}
	for _, fact := range input.Facts {
		expected := jev.OrganisationSourceRef{FactID: fact.ID, SourceID: fact.SourceID, SourceRevision: fact.SourceRevision, SourceKind: fact.SourceKind}
		if refByID[fact.ID] != expected {
			return RoundJevAssessment{}, ErrInvalid
		}
	}
	resultJSON, _ := json.Marshal(result)
	refs, _ := json.Marshal(input.Facts)
	candidates, _ := json.Marshal(input.Categories)
	status := "selected"
	if result.Disposition == jev.OrganisationUncertain {
		status = "unresolved"
	}
	if binding.OmittedBytes > 0 {
		status = "incomplete_source"
	}
	excerpts := make([]string, 0, len(input.Facts))
	for _, fact := range input.Facts {
		excerpts = append(excerpts, fact.Excerpt)
	}
	return s.applyRoundJevAssessment(ctx, binding, "organisation", digest, inputJSON, resultJSON, refs, candidates, excerpts, 1, input.CategorySetVersion, status)
}

// CurrentRoundJevAssessment returns only a current-revision/profile/source
// proposal. A missing or stale pointer is unresolved, not a negative finding.
func (s *Store) CurrentRoundJevAssessment(ctx context.Context, opportunityID, kind string) (RoundJevAssessment, error) {
	if !validJevText(opportunityID, 128) || (kind != "screening" && kind != "organisation") {
		return RoundJevAssessment{}, ErrInvalid
	}
	a, err := scanRoundJevAssessment(s.db.QueryRowContext(ctx, `SELECT `+roundJevAssessmentColumns+` FROM round_jev_assessments
 WHERE id=(SELECT assessment_id FROM round_jev_current WHERE opportunity_id=? AND kind=?)`, opportunityID, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return RoundJevAssessment{}, ErrNotFound
	}
	if err != nil {
		return RoundJevAssessment{}, err
	}
	var revision, profileVersion int64
	var text, sourceID, digest string
	err = s.db.QueryRowContext(ctx, `SELECT o.revision,p.version,o.original_text,
 COALESCE((SELECT id FROM evidence_sources WHERE opportunity_id=o.id AND source_kind='vacancy_snapshot' ORDER BY context_version DESC,recorded_at DESC,id DESC LIMIT 1),''),
 COALESCE((SELECT content_sha256 FROM evidence_sources WHERE opportunity_id=o.id AND source_kind='vacancy_snapshot' ORDER BY context_version DESC,recorded_at DESC,id DESC LIMIT 1),'')
 FROM opportunities o JOIN preferences_current p ON p.singleton=1 WHERE o.id=? AND o.archived_at IS NULL`, opportunityID).Scan(&revision, &profileVersion, &text, &sourceID, &digest)
	if err != nil {
		return RoundJevAssessment{}, err
	}
	if revision != a.OpportunityRevision || profileVersion != a.ProfileVersion || sourceID != a.SourceID || digest != a.SourceRevision || sourceDigest(text) != digest {
		return RoundJevAssessment{}, ErrConflict
	}
	if kind == "organisation" {
		set, err := s.CurrentOrganisationCategories(ctx)
		if err != nil {
			return RoundJevAssessment{}, err
		}
		if set.Version != a.CategoryVersion {
			return RoundJevAssessment{}, ErrConflict
		}
	}
	return a, nil
}

// CurrentRoundOrganisation projects only a guarded current assessment. A
// missing pointer is ErrNotFound; a changed source, profile or category set is
// ErrConflict, so neither can appear as a current selected category.
func (s *Store) CurrentRoundOrganisation(ctx context.Context, opportunityID string) (RoundOrganisationProjection, error) {
	a, err := s.CurrentRoundJevAssessment(ctx, opportunityID, "organisation")
	if err != nil {
		return RoundOrganisationProjection{}, err
	}
	var input jev.OrganisationInput
	var result jev.OrganisationResult
	if err := json.Unmarshal(a.InputJSON, &input); err != nil {
		return RoundOrganisationProjection{}, err
	}
	if err := json.Unmarshal(a.ResultJSON, &result); err != nil {
		return RoundOrganisationProjection{}, err
	}
	projection := RoundOrganisationProjection{Assessment: a}
	switch {
	case a.Status == "selected" && result.Disposition == jev.OrganisationCategorySelected && result.CategoryID != "":
		projection.Status = "selected"
		projection.CategoryID = result.CategoryID
		for _, category := range input.Categories {
			if category.ID == result.CategoryID {
				projection.CategoryDescription = category.Description
				break
			}
		}
		if projection.CategoryDescription == "" {
			return RoundOrganisationProjection{}, ErrConflict
		}
	case a.Status == "unresolved" && result.Disposition == jev.OrganisationUncertain && result.CategoryID == "":
		projection.Status = "uncertain"
	default:
		return RoundOrganisationProjection{}, ErrConflict
	}
	return projection, nil
}
