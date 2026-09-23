package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
)

const qualificationRulesVersion = "qualification-v2"

// A no-op conditional write takes SQLite's writer reservation before any
// snapshot reads. Two Store handles then serialize and stale evidence writers
// observe the new version rather than failing with SQLITE_BUSY_SNAPSHOT.
func lockQualificationInput(ctx context.Context, tx *sql.Tx, opportunityID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE qualification_input_versions
  SET material_version=material_version WHERE opportunity_id=?`, opportunityID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

type Evaluation struct {
	ID                  string
	OpportunityID       string
	OpportunityRevision int64
	MaterialVersion     int64
	EvidenceVersion     int64
	ContextVersion      int64
	PreferencesVersion  int64
	RulesVersion        string
	Overall             fit.OverallState
	Criteria            []fit.CriterionResult
	Salary              fit.SalaryResult
	SourceRefs          []EvidenceRef
	SourceClaimIDs      []string
	CreatedAt           string
	Actor               Actor
	Legacy              bool
	LegacyCriteriaJSON  string
}

type EvidenceRef struct {
	EvidenceID string `json:"evidenceId"`
	SourceID   string `json:"sourceId"`
}

type QualificationView struct {
	Status           string // not_assessed, current, or outdated
	Current          *Evaluation
	LatestHistorical *Evaluation
}

// QualificationInputVersions is a current, single-statement projection for
// optimistic source/evidence writes and conflict responses. It is not the
// immutable tuple captured by a completed mutation or evaluation.
type QualificationInputVersions struct {
	OpportunityID       string
	OpportunityRevision int64
	CompanyID           string
	OpportunityKind     string
	MaterialVersion     int64
	EvidenceVersion     int64
	ContextVersion      int64
	PreferencesVersion  int64
	RulesVersion        string
}

func (s *Store) QualificationInputVersions(ctx context.Context, opportunityID string) (QualificationInputVersions, error) {
	if opportunityID == "" {
		return QualificationInputVersions{}, ErrInvalid
	}
	var value QualificationInputVersions
	err := s.db.QueryRowContext(ctx, `SELECT o.id,o.revision,o.company_id,o.kind,
  v.material_version,v.evidence_version,v.context_version,p.version
  FROM opportunities o JOIN qualification_input_versions v ON v.opportunity_id=o.id
  JOIN preferences_current p ON p.singleton=1 WHERE o.id=?`, opportunityID).Scan(
		&value.OpportunityID, &value.OpportunityRevision, &value.CompanyID, &value.OpportunityKind,
		&value.MaterialVersion, &value.EvidenceVersion, &value.ContextVersion, &value.PreferencesVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return QualificationInputVersions{}, ErrNotFound
	}
	if err != nil {
		return QualificationInputVersions{}, err
	}
	value.RulesVersion = qualificationRulesVersion
	return value, nil
}

type qualClaim struct {
	ID, Criterion, Finding, ObservedValue                      string
	SourceKind                                                 EvidenceSourceKind
	SourceID                                                   string
	HoursMin, HoursMax, HoursHard                              sql.NullInt64
	ArrangementPattern, ArrangementLocation, ArrangementRemote sql.NullString
	ArrangementDays                                            sql.NullInt64
	OwnerArrangementID                                         sql.NullString
	OwnerPreferencesVersion                                    sql.NullInt64
	SalaryCurrency, SalaryPeriod, SalaryBasis                  sql.NullString
	SalaryAmount, SalaryHours                                  sql.NullInt64
}

func activeClaimsTx(ctx context.Context, tx *sql.Tx, opportunity Opportunity, contextVersion int64) ([]qualClaim, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.criterion,e.finding,e.observed_value,
  s.source_kind,s.id,e.hours_min,e.hours_max,e.hours_hard,
  e.arrangement_pattern,e.arrangement_location,e.arrangement_remote_geography,
  e.arrangement_onsite_days,e.owner_arrangement_evidence_id,e.owner_preferences_version,
  e.salary_currency,e.salary_period,e.salary_basis,e.salary_amount_cents,e.salary_weekly_hours
  FROM evidence e JOIN evidence_sources s ON s.id=e.source_id
  WHERE e.opportunity_id=? AND s.company_id=? AND s.opportunity_kind=? AND
    s.context_version=? AND NOT EXISTS(SELECT 1 FROM evidence child WHERE child.supersedes_id=e.id)
  ORDER BY e.created_at,e.id`, opportunity.ID, opportunity.CompanyID, opportunity.Kind, contextVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []qualClaim
	for rows.Next() {
		var claim qualClaim
		if err := rows.Scan(&claim.ID, &claim.Criterion, &claim.Finding, &claim.ObservedValue,
			&claim.SourceKind, &claim.SourceID, &claim.HoursMin, &claim.HoursMax, &claim.HoursHard,
			&claim.ArrangementPattern, &claim.ArrangementLocation, &claim.ArrangementRemote,
			&claim.ArrangementDays, &claim.OwnerArrangementID, &claim.OwnerPreferencesVersion,
			&claim.SalaryCurrency, &claim.SalaryPeriod, &claim.SalaryBasis,
			&claim.SalaryAmount, &claim.SalaryHours); err != nil {
			return nil, err
		}
		out = append(out, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Publication claims are only current while the cited vacancy source
	// matches current source identity/text. Other direct statements survive a
	// text edit in the same company/kind context.
	var filtered []qualClaim
	for _, claim := range out {
		if claim.SourceKind == VacancySnapshot {
			var url, digest string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(source_url,''),content_sha256
  FROM evidence_sources WHERE id=?`, claim.SourceID).Scan(&url, &digest); err != nil {
				return nil, err
			}
			if url != opportunity.SourceURL || digest != sourceDigest(opportunity.OriginalText) {
				continue
			}
		}
		filtered = append(filtered, claim)
	}
	return filtered, nil
}

func directSource(kind EvidenceSourceKind) bool {
	return kind == EmployerStatement || kind == RecruiterStatement
}

func criterionFromClaims(claims []qualClaim, name string) fit.CriterionEvidence {
	match, mismatch, mention, ambiguous := false, false, false, false
	matchAuthority, mismatchAuthority := fit.UserInference, fit.UserInference
	for _, claim := range claims {
		if claim.Criterion != name {
			continue
		}
		authority := fit.PublishedVacancy
		if claim.SourceKind == EmployerStatement {
			authority = fit.Employer
		} else if claim.SourceKind == RecruiterStatement {
			authority = fit.Recruiter
		}
		switch claim.Finding {
		case "explicit_match":
			match = true
			if directSource(claim.SourceKind) {
				matchAuthority = authority
			}
		case "explicit_mismatch":
			mismatch = true
			mismatchAuthority = authority
		case "mention_only":
			mention = true
		case "ambiguous":
			ambiguous = true
		}
	}
	if match && mismatch {
		return fit.CriterionEvidence{Finding: fit.Conflicting, Authority: fit.UserInference}
	}
	if mismatch {
		return fit.CriterionEvidence{Finding: fit.ConfirmedMismatch, Authority: mismatchAuthority}
	}
	if ambiguous {
		return fit.CriterionEvidence{Finding: fit.Ambiguous, Authority: fit.UserInference}
	}
	if match {
		return fit.CriterionEvidence{Finding: fit.ConfirmedMatch, Authority: matchAuthority}
	}
	if mention {
		return fit.CriterionEvidence{Finding: fit.MentionOnly, Authority: fit.UserInference}
	}
	return fit.CriterionEvidence{Finding: fit.Missing, Authority: fit.UserInference}
}

func hoursFromClaims(claims []qualClaim, target int64) fit.CriterionEvidence {
	match, mismatch, mention := false, false, false
	matchAuthority, mismatchAuthority := fit.UserInference, fit.UserInference
	for _, claim := range claims {
		if claim.Criterion != "target_hours_available" || !claim.HoursMin.Valid || !claim.HoursMax.Valid {
			continue
		}
		authority := fit.PublishedVacancy
		if claim.SourceKind == EmployerStatement {
			authority = fit.Employer
		} else if claim.SourceKind == RecruiterStatement {
			authority = fit.Recruiter
		}
		if claim.Finding == "mention_only" || claim.Finding == "ambiguous" {
			mention = true
			continue
		}
		if target >= claim.HoursMin.Int64 && target <= claim.HoursMax.Int64 {
			match = true
			if directSource(claim.SourceKind) {
				matchAuthority = authority
			}
		} else if claim.HoursHard.Valid && claim.HoursHard.Int64 == 1 {
			mismatch = true
			mismatchAuthority = authority
		} else {
			mention = true
		}
	}
	if match && mismatch {
		return fit.CriterionEvidence{Finding: fit.Conflicting}
	}
	if mismatch {
		return fit.CriterionEvidence{Finding: fit.ConfirmedMismatch, Authority: mismatchAuthority}
	}
	if match {
		return fit.CriterionEvidence{Finding: fit.ConfirmedMatch, Authority: matchAuthority}
	}
	if mention {
		return fit.CriterionEvidence{Finding: fit.MentionOnly}
	}
	return fit.CriterionEvidence{Finding: fit.Missing}
}

func arrangementKey(claim qualClaim) string {
	days := ""
	if claim.ArrangementDays.Valid {
		days = fmt.Sprint(claim.ArrangementDays.Int64)
	}
	return strings.Join([]string{claim.ArrangementPattern.String, claim.ArrangementLocation.String,
		claim.ArrangementRemote.String, days}, "\x00")
}

func locationFromClaims(claims []qualClaim, preferences Preferences) fit.CriterionEvidence {
	var arrangements []qualClaim
	owners := map[string][]qualClaim{}
	for _, claim := range claims {
		switch claim.Criterion {
		case "location_arrangement":
			if claim.ArrangementPattern.Valid && claim.Finding != "mention_only" && claim.Finding != "ambiguous" {
				arrangements = append(arrangements, claim)
			}
		case "location_workable":
			if claim.SourceKind == OwnerObservation && claim.OwnerArrangementID.Valid &&
				claim.OwnerPreferencesVersion.Valid && claim.OwnerPreferencesVersion.Int64 == preferences.Version {
				owners[claim.OwnerArrangementID.String] = append(owners[claim.OwnerArrangementID.String], claim)
			}
		}
	}
	if len(arrangements) == 0 {
		return fit.CriterionEvidence{Finding: fit.Missing}
	}
	key := arrangementKey(arrangements[0])
	for _, arrangement := range arrangements[1:] {
		if arrangementKey(arrangement) != key {
			return fit.CriterionEvidence{Finding: fit.Conflicting}
		}
	}
	if arrangements[0].ArrangementPattern.String == "remote" && !preferences.AllowRemote ||
		arrangements[0].ArrangementPattern.String == "hybrid" && !preferences.AllowHybrid {
		return fit.CriterionEvidence{Finding: fit.ConfirmedMismatch, Authority: fit.OwnerVerified}
	}
	match, mismatch := false, false
	for _, arrangement := range arrangements {
		if !directSource(arrangement.SourceKind) {
			continue
		}
		for _, owner := range owners[arrangement.ID] {
			if owner.ObservedValue == "workable" {
				match = true
			} else if owner.ObservedValue == "not_workable" {
				mismatch = true
			}
		}
	}
	if match && mismatch {
		return fit.CriterionEvidence{Finding: fit.Conflicting}
	}
	if mismatch {
		return fit.CriterionEvidence{Finding: fit.ConfirmedMismatch, Authority: fit.OwnerVerified}
	}
	if match {
		return fit.CriterionEvidence{Finding: fit.ConfirmedMatch, Authority: fit.OwnerVerified}
	}
	return fit.CriterionEvidence{Finding: fit.Missing}
}

func actualSalaryFromClaims(claims []qualClaim) (*fit.ActualPay, bool) {
	var actual *fit.ActualPay
	conflicting := false
	for _, claim := range claims {
		if claim.Criterion != "monthly_base_salary" || !directSource(claim.SourceKind) ||
			!claim.SalaryAmount.Valid || !claim.SalaryHours.Valid {
			continue
		}
		candidate := fit.ActualPay{Currency: claim.SalaryCurrency.String,
			Period: fit.PayPeriod(claim.SalaryPeriod.String), Basis: fit.PayBasis(claim.SalaryBasis.String),
			AmountCents: claim.SalaryAmount.Int64, WeeklyHours: claim.SalaryHours.Int64}
		if actual == nil {
			actual = &candidate
		} else if *actual != candidate {
			conflicting = true
		}
	}
	return actual, conflicting
}

func policyFromPreferences(p Preferences) fit.Policy {
	return fit.Policy{TargetHours: p.TargetHours, MinMonthlyBaseCents: p.MinMonthlyBaseCents,
		SalaryCurrency: p.SalaryCurrency, PreferredLocation: p.PreferredLocation,
		RequireBackendPlatform: p.RequireBackendPlatform, ExcludeFrontendDuties: p.ExcludeFrontendDuties,
		ExcludePHPFocusedDuties: p.ExcludePHPFocused}
}

func compensationForFit(o Opportunity, claims []qualClaim) fit.Compensation {
	actual, conflicting := actualSalaryFromClaims(claims)
	return fit.Compensation{Kind: fit.OpportunityKind(o.Kind), Currency: o.Compensation.Currency,
		Period: fit.PayPeriod(o.Compensation.Period), Basis: fit.PayBasis(o.Compensation.Basis),
		MinCents: o.Compensation.MinAmountCents, MaxCents: o.Compensation.MaxAmountCents,
		ReferenceHours: o.Compensation.ReferenceHours, SourcedActual: actual,
		ActualPayConflicting: conflicting}
}

func scanEvaluation(row rowScanner) (Evaluation, error) {
	var result Evaluation
	var criteriaJSON, salaryJSON, claimJSON string
	err := row.Scan(&result.ID, &result.OpportunityID, &result.OpportunityRevision,
		&result.MaterialVersion, &result.EvidenceVersion, &result.ContextVersion,
		&result.PreferencesVersion, &result.RulesVersion, &result.Overall, &criteriaJSON, &salaryJSON,
		&claimJSON, &result.CreatedAt, &result.Actor.Kind, &result.Actor.ID)
	if err != nil {
		return Evaluation{}, err
	}
	result.Legacy = result.MaterialVersion == 0
	if result.Legacy {
		result.LegacyCriteriaJSON = criteriaJSON
	}
	if err := json.Unmarshal([]byte(criteriaJSON), &result.Criteria); err != nil {
		if !result.Legacy {
			return Evaluation{}, err
		}
	}
	if err := json.Unmarshal([]byte(salaryJSON), &result.Salary); err != nil {
		if !result.Legacy {
			return Evaluation{}, err
		}
	}
	if err := json.Unmarshal([]byte(claimJSON), &result.SourceRefs); err != nil {
		if !result.Legacy {
			return Evaluation{}, err
		}
	}
	for _, ref := range result.SourceRefs {
		result.SourceClaimIDs = append(result.SourceClaimIDs, ref.EvidenceID)
	}
	return result, nil
}

const evaluationColumns = `id,opportunity_id,opportunity_revision,material_version,
  evidence_version,context_version,preferences_version,rules_version,overall_state,
  criterion_results_json,salary_json,source_claims_json,created_at,actor_kind,actor_id`

func evaluateCurrentTx(ctx context.Context, tx *sql.Tx, actor Actor, opportunityID, causeEvidenceID string) (Evaluation, error) {
	var result Evaluation
	var err error
	if err := lockQualificationInput(ctx, tx, opportunityID); err != nil {
		return Evaluation{}, err
	}
	opportunity, err := scanOpportunity(tx.QueryRowContext(ctx,
		`SELECT `+opportunityColumns+opportunityFrom+` WHERE o.id=?`, opportunityID))
	if errors.Is(err, sql.ErrNoRows) {
		return Evaluation{}, ErrNotFound
	}
	if err != nil {
		return Evaluation{}, err
	}
	var materialVersion, evidenceVersion, contextVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT material_version,evidence_version,context_version
  FROM qualification_input_versions WHERE opportunity_id=?`, opportunityID).Scan(
		&materialVersion, &evidenceVersion, &contextVersion); err != nil {
		return Evaluation{}, err
	}
	preference, err := scanPreferences(tx.QueryRowContext(ctx, `SELECT `+preferenceColumns+`
  FROM preferences_versions p JOIN preferences_current c ON c.version=p.version WHERE c.singleton=1`))
	if err != nil {
		return Evaluation{}, err
	}
	claims, err := activeClaimsTx(ctx, tx, opportunity, contextVersion)
	if err != nil {
		return Evaluation{}, err
	}
	var previouslyQualified int
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qualification_evaluations
  WHERE opportunity_id=? AND context_version=? AND material_version IS NOT NULL
    AND overall_state='qualified')`, opportunityID, contextVersion).Scan(&previouslyQualified)
	if err != nil {
		return Evaluation{}, err
	}
	criteria := fit.Criteria{BackendPlatform: criterionFromClaims(claims, "backend_platform"),
		NoFrontendDuties:   criterionFromClaims(claims, "no_frontend_duties"),
		NoPHPFocusedDuties: criterionFromClaims(claims, "no_php_focused_duties"),
		HoursAvailable:     hoursFromClaims(claims, preference.TargetHours),
		LocationWorkable:   locationFromClaims(claims, preference)}
	report, err := fit.Evaluate(policyFromPreferences(preference), compensationForFit(opportunity, claims),
		criteria, previouslyQualified == 1)
	if err != nil {
		return Evaluation{}, err
	}
	refs := make([]EvidenceRef, 0, len(claims))
	for _, claim := range claims {
		refs = append(refs, EvidenceRef{EvidenceID: claim.ID, SourceID: claim.SourceID})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].EvidenceID < refs[j].EvidenceID })
	claimIDs := make([]string, 0, len(refs))
	for _, ref := range refs {
		claimIDs = append(claimIDs, ref.EvidenceID)
	}
	criteriaJSON, _ := json.Marshal(report.Criteria)
	salaryJSON, _ := json.Marshal(report.Salary)
	claimsJSON, _ := json.Marshal(refs)
	id, err := randomID()
	if err != nil {
		return Evaluation{}, err
	}
	result = Evaluation{ID: id, OpportunityID: opportunityID, OpportunityRevision: opportunity.Revision,
		MaterialVersion: materialVersion, EvidenceVersion: evidenceVersion, ContextVersion: contextVersion,
		PreferencesVersion: preference.Version, RulesVersion: qualificationRulesVersion,
		Overall: report.Overall, Criteria: report.Criteria,
		Salary: report.Salary, SourceRefs: refs, SourceClaimIDs: claimIDs, CreatedAt: utcNow(), Actor: actor}
	_, err = tx.ExecContext(ctx, `INSERT INTO qualification_evaluations
  (id,opportunity_id,opportunity_revision,preferences_version,overall_state,
   criterion_results_json,created_at,material_version,evidence_version,context_version,
   rules_version,source_claims_json,salary_json,actor_kind,actor_id,cause_evidence_id)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, opportunityID, opportunity.Revision,
		preference.Version, report.Overall, string(criteriaJSON), result.CreatedAt,
		materialVersion, evidenceVersion, contextVersion, qualificationRulesVersion,
		string(claimsJSON), string(salaryJSON), actor.Kind, actor.ID, optionalText(causeEvidenceID))
	if err != nil {
		return Evaluation{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO qualification_current(opportunity_id,evaluation_id)
  VALUES (?,?) ON CONFLICT(opportunity_id) DO UPDATE SET evaluation_id=excluded.evaluation_id`, opportunityID, id)
	if err != nil {
		return Evaluation{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM qualification_refresh_queue WHERE opportunity_id=?`, opportunityID); err != nil {
		return Evaluation{}, err
	}
	return result, nil
}

func (s *Store) EvaluateCurrent(ctx context.Context, actor Actor, opportunityID string) (Evaluation, string, error) {
	if opportunityID == "" {
		return Evaluation{}, "", fmt.Errorf("%w: opportunity required", ErrInvalid)
	}
	var evaluation Evaluation
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		var err error
		evaluation, err = evaluateCurrentTx(ctx, tx, actor, opportunityID, "")
		if err != nil {
			return Change{}, err
		}
		return Change{Operation: "qualification.evaluate", EntityKind: "qualification",
			EntityID: evaluation.ID}, nil
	})
	if err != nil {
		return Evaluation{}, "", err
	}
	return evaluation, changeID, nil
}

func (s *Store) qualificationView(ctx context.Context, opportunityID string) (QualificationView, error) {
	view := QualificationView{Status: "not_assessed"}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM qualification_input_versions
  WHERE opportunity_id=?`, opportunityID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return view, ErrNotFound
		}
		return view, err
	}
	latest, err := scanEvaluation(s.db.QueryRowContext(ctx, `SELECT `+evaluationColumns+`
  FROM qualification_evaluations WHERE opportunity_id=? AND material_version IS NOT NULL
  ORDER BY created_at DESC,id DESC LIMIT 1`, opportunityID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	if err == nil {
		view.LatestHistorical = &latest
		view.Status = "outdated"
	}
	// The tuple equality and evaluation are selected in one SQLite statement.
	// A version read followed by a separate current lookup could combine states
	// from opposite sides of a concurrent mutation.
	current, err := scanEvaluation(s.db.QueryRowContext(ctx, `SELECT e.id,e.opportunity_id,e.opportunity_revision,
  e.material_version,e.evidence_version,e.context_version,e.preferences_version,
  e.rules_version,e.overall_state,e.criterion_results_json,e.salary_json,e.source_claims_json,
  e.created_at,e.actor_kind,e.actor_id
  FROM qualification_current c JOIN qualification_evaluations e ON e.id=c.evaluation_id
  JOIN qualification_input_versions v ON v.opportunity_id=c.opportunity_id
  JOIN preferences_current p ON p.singleton=1
  WHERE c.opportunity_id=? AND e.material_version=v.material_version
    AND e.evidence_version=v.evidence_version AND e.context_version=v.context_version
    AND e.preferences_version=p.version AND e.rules_version=?`, opportunityID, qualificationRulesVersion))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	if err == nil {
		view.Status, view.Current = "current", &current
	}
	return view, nil
}

func (s *Store) Qualification(ctx context.Context, opportunityID string) (QualificationView, error) {
	view, err := s.qualificationView(ctx, opportunityID)
	if err != nil || view.Status == "current" {
		return view, err
	}
	_, _, err = s.EvaluateCurrent(ctx, Actor{Kind: "system", ID: "qualification-refresh"}, opportunityID)
	if err != nil {
		view.Status, view.Current = "outdated", nil
		return view, err
	}
	return s.qualificationView(ctx, opportunityID)
}

func (s *Store) ReevaluatePending(ctx context.Context, limit int) (int, error) {
	limit, err := boundedListLimit(limit)
	if err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT opportunity_id FROM qualification_refresh_queue
  ORDER BY requested_at,opportunity_id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	processed := 0
	for _, id := range ids {
		view, err := s.Qualification(ctx, id)
		if err != nil {
			return processed, err
		}
		if view.Status == "current" {
			// Delete only while that exact tuple is still current; a concurrent
			// material/evidence/preference change keeps the queue item.
			_, err = s.db.ExecContext(ctx, `DELETE FROM qualification_refresh_queue
  WHERE opportunity_id=? AND EXISTS (
    SELECT 1 FROM qualification_current c
    JOIN qualification_evaluations e ON e.id=c.evaluation_id
    JOIN qualification_input_versions v ON v.opportunity_id=c.opportunity_id
    JOIN preferences_current p ON p.singleton=1
    WHERE c.opportunity_id=? AND e.material_version=v.material_version
      AND e.evidence_version=v.evidence_version AND e.context_version=v.context_version
      AND e.preferences_version=p.version AND e.rules_version=?)`, id, id, qualificationRulesVersion)
			if err != nil {
				return processed, err
			}
		}
		processed++
	}
	return processed, nil
}

type EvaluationPage struct {
	Items      []Evaluation
	NextCursor string
}

// ListQualificationHistory includes pre-007 rows as legacy historical results.
// Their absent source/version provenance never makes them current.
func (s *Store) ListQualificationHistory(ctx context.Context, opportunityID, cursorValue string, requestedLimit int) (EvaluationPage, error) {
	if opportunityID == "" {
		return EvaluationPage{}, ErrInvalid
	}
	limit, err := boundedListLimit(requestedLimit)
	if err != nil {
		return EvaluationPage{}, err
	}
	scope := recordListScope("qualification-history", opportunityID)
	cursor, err := decodeEvidenceCursor(cursorValue, scope)
	if err != nil {
		return EvaluationPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,opportunity_id,opportunity_revision,
  COALESCE(material_version,0),COALESCE(evidence_version,0),COALESCE(context_version,0),
  preferences_version,COALESCE(rules_version,''),overall_state,criterion_results_json,COALESCE(salary_json,'{}'),
  COALESCE(source_claims_json,'[]'),created_at,COALESCE(actor_kind,''),COALESCE(actor_id,'')
  FROM qualification_evaluations WHERE opportunity_id=? AND
  (created_at>? OR (created_at=? AND id>?)) ORDER BY created_at,id LIMIT ?`,
		opportunityID, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return EvaluationPage{}, err
	}
	defer rows.Close()
	page := EvaluationPage{Items: make([]Evaluation, 0, limit)}
	for rows.Next() {
		item, err := scanEvaluation(rows)
		if err != nil {
			return EvaluationPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return EvaluationPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeEvidenceCursor(evidenceCursor{Version: 1, CreatedAt: last.CreatedAt,
			ID: last.ID, Scope: scope})
	}
	return page, nil
}
