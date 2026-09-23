package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type EvidenceSourceKind string

const (
	VacancySnapshot    EvidenceSourceKind = "vacancy_snapshot"
	EmployerStatement  EvidenceSourceKind = "employer_statement"
	RecruiterStatement EvidenceSourceKind = "recruiter_statement"
	OwnerObservation   EvidenceSourceKind = "owner_observation"
)

type StatementInput struct {
	SpeakerAffiliation  string // employer_representative or recruiter
	SpeakerName         string
	SpeakerRole         string
	SpeakerOrganisation string
	Channel             string
	OccurredAt          string
	OriginalText        string
	SourceURL           string
}

type OwnerInput struct {
	OccurredAt                 string
	OriginalText               string
	ExpectedPreferencesVersion int64
}

type SourceInput struct {
	OpportunityID          string
	ExpectedContextVersion int64
	VacancyChangeID        *string
	Statement              *StatementInput
	OwnerObservation       *OwnerInput
}

type EvidenceSource struct {
	ID                      string
	OpportunityID           string
	CompanyID               string
	OpportunityKind         string
	ContextVersion          int64
	SourceKind              EvidenceSourceKind
	RecordChangeAuditID     string
	SourceURL               string
	OriginalText            string
	ContentSHA256           string
	SpeakerName             string
	SpeakerRole             string
	SpeakerOrganisation     string
	Channel                 string
	OccurredAt              string
	OwnerPreferencesVersion int64
	RecordedAt              string
	Actor                   Actor
}

func sourceDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validInstant(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func boundedNonempty(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum && utf8.ValidString(value)
}

func validateSourceInput(input SourceInput, actor Actor) error {
	variants := 0
	if input.VacancyChangeID != nil {
		variants++
	}
	if input.Statement != nil {
		variants++
	}
	if input.OwnerObservation != nil {
		variants++
	}
	if input.OpportunityID == "" || input.ExpectedContextVersion < 1 || variants != 1 {
		return fmt.Errorf("%w: one evidence source variant required", ErrInvalid)
	}
	if input.VacancyChangeID != nil && *input.VacancyChangeID == "" {
		return fmt.Errorf("%w: vacancy change required", ErrInvalid)
	}
	if statement := input.Statement; statement != nil {
		if statement.SpeakerAffiliation != "employer_representative" && statement.SpeakerAffiliation != "recruiter" ||
			!boundedNonempty(statement.SpeakerName, 200) || !boundedNonempty(statement.SpeakerRole, 200) ||
			!boundedNonempty(statement.SpeakerOrganisation, 200) || !boundedNonempty(statement.Channel, 80) ||
			!boundedNonempty(statement.OriginalText, 20000) || !validInstant(statement.OccurredAt) {
			return fmt.Errorf("%w: attributable statement and date required", ErrInvalid)
		}
		if err := validateWebURL(statement.SourceURL); err != nil {
			return err
		}
	}
	if observation := input.OwnerObservation; observation != nil {
		if actor.Kind != "administrator" || !boundedNonempty(observation.OriginalText, 20000) ||
			!validInstant(observation.OccurredAt) || observation.ExpectedPreferencesVersion < 1 {
			return fmt.Errorf("%w: owner observation requires owner and dated text", ErrInvalid)
		}
	}
	return nil
}

func scanEvidenceSource(row rowScanner) (EvidenceSource, error) {
	var source EvidenceSource
	var auditID, sourceURL, name, role, organisation, channel, occurred sql.NullString
	var ownerPreference sql.NullInt64
	err := row.Scan(&source.ID, &source.OpportunityID, &source.CompanyID, &source.OpportunityKind,
		&source.ContextVersion, &source.SourceKind, &auditID, &sourceURL, &source.OriginalText,
		&source.ContentSHA256, &name, &role, &organisation, &channel, &occurred, &ownerPreference,
		&source.RecordedAt, &source.Actor.Kind, &source.Actor.ID)
	if err != nil {
		return EvidenceSource{}, err
	}
	source.RecordChangeAuditID, source.SourceURL = auditID.String, sourceURL.String
	source.SpeakerName, source.SpeakerRole, source.SpeakerOrganisation = name.String, role.String, organisation.String
	source.Channel, source.OccurredAt = channel.String, occurred.String
	source.OwnerPreferencesVersion = ownerPreference.Int64
	return source, nil
}

const evidenceSourceColumns = `id,opportunity_id,company_id,opportunity_kind,context_version,
  source_kind,record_change_audit_id,source_url,original_text,content_sha256,
  speaker_name,speaker_role,speaker_organisation,channel,occurred_at,
  owner_preferences_version,recorded_at,actor_kind,actor_id`

func (s *Store) EvidenceSource(ctx context.Context, id string) (EvidenceSource, error) {
	source, err := scanEvidenceSource(s.db.QueryRowContext(ctx,
		`SELECT `+evidenceSourceColumns+` FROM evidence_sources WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return EvidenceSource{}, ErrNotFound
	}
	return source, err
}

func (s *Store) AddEvidenceSource(ctx context.Context, actor Actor, input SourceInput) (EvidenceSource, string, error) {
	if err := validateSourceInput(input, actor); err != nil {
		return EvidenceSource{}, "", err
	}
	id, err := randomID()
	if err != nil {
		return EvidenceSource{}, "", err
	}
	source := EvidenceSource{ID: id, OpportunityID: input.OpportunityID, RecordedAt: utcNow(), Actor: actor}
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		if err := lockQualificationInput(ctx, tx, input.OpportunityID); err != nil {
			return Change{}, err
		}
		var revision int64
		var sourceURL string
		var originalText string
		err := tx.QueryRowContext(ctx, `SELECT o.company_id,o.kind,o.revision,
  COALESCE(o.source_url,''),o.original_text,v.context_version FROM opportunities o
  JOIN qualification_input_versions v ON v.opportunity_id=o.id
  WHERE o.id=? AND o.archived_at IS NULL`, input.OpportunityID).Scan(
			&source.CompanyID, &source.OpportunityKind, &revision, &sourceURL, &originalText, &source.ContextVersion)
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, err
		}
		if source.ContextVersion != input.ExpectedContextVersion {
			return Change{}, ErrConflict
		}
		switch {
		case input.VacancyChangeID != nil:
			var snapshotJSON string
			var eventRevision sql.NullInt64
			err := tx.QueryRowContext(ctx, `SELECT snapshot_json,revision_after FROM record_changes
  WHERE audit_id=? AND entity_kind='opportunity' AND entity_id=? AND snapshot_state='captured'`,
				*input.VacancyChangeID, input.OpportunityID).Scan(&snapshotJSON, &eventRevision)
			if errors.Is(err, sql.ErrNoRows) {
				return Change{}, ErrInvalid
			}
			if err != nil {
				return Change{}, err
			}
			var snapshot struct {
				CompanyID    string `json:"companyId"`
				Kind         string `json:"kind"`
				SourceURL    string `json:"sourceUrl"`
				OriginalText string `json:"originalText"`
			}
			if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
				return Change{}, err
			}
			// A new source must cite the current context's record-change event.
			// Existing source claims remain live through later stage/note edits.
			if !eventRevision.Valid || eventRevision.Int64 != revision || snapshot.CompanyID != source.CompanyID ||
				snapshot.Kind != source.OpportunityKind || snapshot.SourceURL != sourceURL ||
				snapshot.OriginalText != originalText {
				return Change{}, fmt.Errorf("%w: vacancy snapshot is not current", ErrConflict)
			}
			source.SourceKind, source.RecordChangeAuditID = VacancySnapshot, *input.VacancyChangeID
			source.SourceURL, source.OriginalText = sourceURL, originalText
		case input.Statement != nil:
			statement := input.Statement
			source.SourceKind = EmployerStatement
			if statement.SpeakerAffiliation == "recruiter" {
				source.SourceKind = RecruiterStatement
			}
			source.SourceURL, source.OriginalText = statement.SourceURL, statement.OriginalText
			source.SpeakerName, source.SpeakerRole = strings.TrimSpace(statement.SpeakerName), strings.TrimSpace(statement.SpeakerRole)
			source.SpeakerOrganisation, source.Channel = strings.TrimSpace(statement.SpeakerOrganisation), strings.TrimSpace(statement.Channel)
			source.OccurredAt = statement.OccurredAt
		case input.OwnerObservation != nil:
			source.SourceKind = OwnerObservation
			source.OriginalText = input.OwnerObservation.OriginalText
			source.OccurredAt = input.OwnerObservation.OccurredAt
			source.SpeakerName, source.SpeakerRole = "owner", "self"
			source.SpeakerOrganisation, source.Channel = "self", "owner_observation"
			if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(
				&source.OwnerPreferencesVersion); err != nil {
				return Change{}, err
			}
			if source.OwnerPreferencesVersion != input.OwnerObservation.ExpectedPreferencesVersion {
				return Change{}, ErrConflict
			}
		}
		if !utf8.ValidString(source.OriginalText) {
			return Change{}, fmt.Errorf("%w: source text must be UTF-8", ErrInvalid)
		}
		source.ContentSHA256 = sourceDigest(source.OriginalText)
		_, err = tx.ExecContext(ctx, `INSERT INTO evidence_sources
  (id,opportunity_id,company_id,opportunity_kind,context_version,source_kind,
   record_change_audit_id,source_url,original_text,content_sha256,speaker_name,
   speaker_role,speaker_organisation,channel,occurred_at,owner_preferences_version,
   recorded_at,actor_kind,actor_id)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, source.ID, source.OpportunityID,
			source.CompanyID, source.OpportunityKind, source.ContextVersion, source.SourceKind,
			optionalText(source.RecordChangeAuditID), optionalText(source.SourceURL), source.OriginalText,
			source.ContentSHA256, optionalText(source.SpeakerName), optionalText(source.SpeakerRole),
			optionalText(source.SpeakerOrganisation), optionalText(source.Channel),
			optionalText(source.OccurredAt), nullablePositive(source.OwnerPreferencesVersion),
			source.RecordedAt, actor.Kind, actor.ID)
		if err != nil {
			return Change{}, err
		}
		if _, err := evaluateCurrentTx(ctx, tx, actor, input.OpportunityID, ""); err != nil {
			return Change{}, err
		}
		return Change{Operation: "evidence.source.add", EntityKind: "evidence_source", EntityID: id}, nil
	})
	if err != nil {
		return EvidenceSource{}, "", err
	}
	return source, changeID, nil
}

type HoursAvailability struct {
	MinWeekly  int64
	MaxWeekly  int64
	HardBounds bool
}

type WorkArrangement struct {
	Pattern         string
	BaseLocation    string
	RemoteGeography string
	OnsiteDays      *int64
}

type ActualSalaryFacts struct {
	Currency          string
	Period            string
	Basis             string
	AmountCents       int64
	ActualWeeklyHours int64
}

type EvidenceInput struct {
	OpportunityID              string
	SourceID                   string
	Criterion                  string
	Finding                    string
	ObservedValue              string
	SpanStart                  int
	SpanEnd                    int
	Hours                      *HoursAvailability
	Arrangement                *WorkArrangement
	OwnerWorkableForEvidenceID *string
	Salary                     *ActualSalaryFacts
	ExpectedEvidenceVersion    int64
}

type Evidence struct {
	ID                         string
	OpportunityID              string
	SourceID                   string
	SourceKind                 string
	SourceURL                  string
	SourceContactText          string
	Legacy                     bool
	Criterion                  string
	Finding                    string
	ObservedValue              string
	ConfirmationState          string
	SourceExcerpt              string
	SpanStart                  int
	SpanEnd                    int
	HasSpan                    bool
	ExcerptSHA256              string
	ObservedAt                 string
	SupersedesID               string
	CreatedAt                  string
	Hours                      *HoursAvailability
	Arrangement                *WorkArrangement
	OwnerWorkableForEvidenceID string
	OwnerPreferencesVersion    int64
	Salary                     *ActualSalaryFacts
}

func validCriterion(value string) bool {
	switch value {
	case "backend_platform", "no_frontend_duties", "no_php_focused_duties",
		"target_hours_available", "location_arrangement", "location_workable", "monthly_base_salary":
		return true
	}
	return false
}

func validateEvidenceInput(input EvidenceInput) error {
	if input.OpportunityID == "" || input.SourceID == "" || !validCriterion(input.Criterion) ||
		input.ExpectedEvidenceVersion < 0 || !boundedNonempty(input.ObservedValue, 1000) {
		return fmt.Errorf("%w: evidence identity, criterion, value and version required", ErrInvalid)
	}
	switch input.Finding {
	case "explicit_match", "explicit_mismatch", "mention_only", "ambiguous":
	default:
		return fmt.Errorf("%w: invalid evidence finding", ErrInvalid)
	}
	if input.SpanStart < 0 || input.SpanEnd <= input.SpanStart || input.SpanEnd-input.SpanStart > 2000 {
		return fmt.Errorf("%w: exact bounded excerpt required", ErrInvalid)
	}
	switch input.Criterion {
	case "backend_platform":
		if !validObservedValue(input.ObservedValue, "backend_primary", "backend_not_primary", "backend_mixed", "backend_mentioned") ||
			!findingMatchesValue(input.ObservedValue, input.Finding, map[string]string{
				"backend_primary": "explicit_match", "backend_not_primary": "explicit_mismatch",
				"backend_mixed": "ambiguous", "backend_mentioned": "mention_only"}) ||
			input.Hours != nil || input.Arrangement != nil || input.Salary != nil || input.OwnerWorkableForEvidenceID != nil {
			return fmt.Errorf("%w: invalid backend scope observation", ErrInvalid)
		}
	case "no_frontend_duties":
		if !validObservedValue(input.ObservedValue, "frontend_not_required", "frontend_required", "frontend_mentioned", "frontend_ambiguous") ||
			!findingMatchesValue(input.ObservedValue, input.Finding, map[string]string{
				"frontend_not_required": "explicit_match", "frontend_required": "explicit_mismatch",
				"frontend_mentioned": "mention_only", "frontend_ambiguous": "ambiguous"}) ||
			input.Hours != nil || input.Arrangement != nil || input.Salary != nil || input.OwnerWorkableForEvidenceID != nil {
			return fmt.Errorf("%w: invalid frontend observation", ErrInvalid)
		}
	case "no_php_focused_duties":
		if !validObservedValue(input.ObservedValue, "php_not_required", "php_required", "php_mentioned", "php_ambiguous") ||
			!findingMatchesValue(input.ObservedValue, input.Finding, map[string]string{
				"php_not_required": "explicit_match", "php_required": "explicit_mismatch",
				"php_mentioned": "mention_only", "php_ambiguous": "ambiguous"}) ||
			input.Hours != nil || input.Arrangement != nil || input.Salary != nil || input.OwnerWorkableForEvidenceID != nil {
			return fmt.Errorf("%w: invalid PHP observation", ErrInvalid)
		}
	case "target_hours_available":
		if input.Hours == nil || input.Hours.MinWeekly < 1 || input.Hours.MaxWeekly > 168 ||
			input.Hours.MaxWeekly < input.Hours.MinWeekly || input.Arrangement != nil || input.Salary != nil ||
			input.OwnerWorkableForEvidenceID != nil || input.Finding != "explicit_match" ||
			input.ObservedValue != "weekly_hours_available" {
			return fmt.Errorf("%w: numeric hours availability required", ErrInvalid)
		}
	case "location_arrangement":
		if input.Arrangement == nil || input.Hours != nil || input.Salary != nil ||
			input.OwnerWorkableForEvidenceID != nil || input.Finding != "explicit_match" ||
			input.ObservedValue != "work_arrangement" {
			return fmt.Errorf("%w: arrangement required", ErrInvalid)
		}
		if input.Arrangement.Pattern != "onsite" && input.Arrangement.Pattern != "hybrid" && input.Arrangement.Pattern != "remote" ||
			len(input.Arrangement.BaseLocation) > 500 || len(input.Arrangement.RemoteGeography) > 500 ||
			input.Arrangement.Pattern == "remote" && strings.TrimSpace(input.Arrangement.RemoteGeography) == "" ||
			(input.Arrangement.Pattern == "onsite" || input.Arrangement.Pattern == "hybrid") &&
				strings.TrimSpace(input.Arrangement.BaseLocation) == "" ||
			input.Arrangement.OnsiteDays != nil && (*input.Arrangement.OnsiteDays < 0 || *input.Arrangement.OnsiteDays > 7) {
			return fmt.Errorf("%w: invalid arrangement", ErrInvalid)
		}
	case "location_workable":
		if input.OwnerWorkableForEvidenceID == nil || *input.OwnerWorkableForEvidenceID == "" ||
			input.Hours != nil || input.Arrangement != nil || input.Salary != nil ||
			(input.ObservedValue != "workable" && input.ObservedValue != "not_workable") ||
			input.Finding != "explicit_match" && input.Finding != "explicit_mismatch" ||
			input.ObservedValue == "workable" && input.Finding != "explicit_match" ||
			input.ObservedValue == "not_workable" && input.Finding != "explicit_mismatch" {
			return fmt.Errorf("%w: owner workability and arrangement link required", ErrInvalid)
		}
	case "monthly_base_salary":
		if input.Salary == nil || input.Hours != nil || input.Arrangement != nil ||
			input.OwnerWorkableForEvidenceID != nil || input.Finding != "explicit_match" ||
			input.ObservedValue != "actual_pay_terms" || input.Salary.AmountCents < 0 || input.Salary.ActualWeeklyHours < 1 ||
			input.Salary.ActualWeeklyHours > 168 || input.Salary.Currency == "" ||
			!validObservedValue(input.Salary.Period, "month", "year", "hour", "project") ||
			!validObservedValue(input.Salary.Basis, "base", "inclusive", "unknown") ||
			!currencyCode(input.Salary.Currency) {
			return fmt.Errorf("%w: typed actual pay required", ErrInvalid)
		}
	default:
		if input.Hours != nil || input.Arrangement != nil || input.Salary != nil || input.OwnerWorkableForEvidenceID != nil {
			return fmt.Errorf("%w: unexpected criterion payload", ErrInvalid)
		}
	}
	if input.Criterion != "location_workable" && input.OwnerWorkableForEvidenceID != nil {
		return fmt.Errorf("%w: unexpected arrangement link", ErrInvalid)
	}
	return nil
}

func validObservedValue(value string, accepted ...string) bool {
	for _, candidate := range accepted {
		if value == candidate {
			return true
		}
	}
	return false
}

func findingMatchesValue(value, finding string, meanings map[string]string) bool {
	return meanings[value] == finding
}

func currencyCode(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, letter := range value {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return true
}

func exactExcerpt(source string, start, end int) (string, error) {
	if !utf8.ValidString(source) || start < 0 || end > len(source) || end <= start ||
		start < len(source) && !utf8.RuneStart(source[start]) || end < len(source) && !utf8.RuneStart(source[end]) {
		return "", fmt.Errorf("%w: invalid UTF-8 excerpt boundary", ErrInvalid)
	}
	excerpt := source[start:end]
	if !boundedNonempty(excerpt, 2000) {
		return "", fmt.Errorf("%w: nonempty excerpt required", ErrInvalid)
	}
	return excerpt, nil
}

func (s *Store) AddEvidence(ctx context.Context, actor Actor, input EvidenceInput) (Evidence, string, error) {
	return s.writeEvidence(ctx, actor, "", input)
}

func (s *Store) SupersedeEvidence(ctx context.Context, actor Actor, priorID string, input EvidenceInput) (Evidence, string, error) {
	if priorID == "" {
		return Evidence{}, "", fmt.Errorf("%w: prior evidence required", ErrInvalid)
	}
	return s.writeEvidence(ctx, actor, priorID, input)
}

func (s *Store) writeEvidence(ctx context.Context, actor Actor, priorID string, input EvidenceInput) (Evidence, string, error) {
	if err := validateEvidenceInput(input); err != nil {
		return Evidence{}, "", err
	}
	id, err := randomID()
	if err != nil {
		return Evidence{}, "", err
	}
	item := Evidence{ID: id, OpportunityID: input.OpportunityID, SourceID: input.SourceID,
		Criterion: input.Criterion, Finding: input.Finding, ObservedValue: strings.TrimSpace(input.ObservedValue),
		SupersedesID: priorID, CreatedAt: utcNow(), Hours: input.Hours, Arrangement: input.Arrangement,
		Salary: input.Salary, SpanStart: input.SpanStart, SpanEnd: input.SpanEnd, HasSpan: true}
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		if err := lockQualificationInput(ctx, tx, input.OpportunityID); err != nil {
			return Change{}, err
		}
		var evidenceVersion, contextVersion int64
		var companyID, opportunityKind string
		err := tx.QueryRowContext(ctx, `SELECT v.evidence_version,v.context_version,o.company_id,o.kind
  FROM qualification_input_versions v JOIN opportunities o ON o.id=v.opportunity_id
  WHERE o.id=? AND o.archived_at IS NULL`, input.OpportunityID).Scan(
			&evidenceVersion, &contextVersion, &companyID, &opportunityKind)
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, err
		}
		if evidenceVersion != input.ExpectedEvidenceVersion {
			return Change{}, ErrConflict
		}
		var source EvidenceSource
		source, err = scanEvidenceSource(tx.QueryRowContext(ctx,
			`SELECT `+evidenceSourceColumns+` FROM evidence_sources WHERE id=?`, input.SourceID))
		if errors.Is(err, sql.ErrNoRows) {
			return Change{}, ErrInvalid
		}
		if err != nil {
			return Change{}, err
		}
		if source.OpportunityID != input.OpportunityID || source.ContextVersion != contextVersion ||
			source.CompanyID != companyID || source.OpportunityKind != opportunityKind {
			return Change{}, ErrConflict
		}
		item.SourceKind, item.SourceURL, item.SourceContactText = string(source.SourceKind), source.SourceURL, source.SpeakerName
		if source.SourceKind == OwnerObservation && input.Criterion != "location_workable" ||
			input.Criterion == "location_workable" && (source.SourceKind != OwnerObservation || actor.Kind != "administrator") ||
			input.Criterion == "monthly_base_salary" &&
				(source.SourceKind != EmployerStatement && source.SourceKind != RecruiterStatement || opportunityKind != "employment") {
			return Change{}, fmt.Errorf("%w: source cannot establish this criterion", ErrInvalid)
		}
		if source.SourceKind == VacancySnapshot {
			var currentURL, currentText string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(source_url,''),original_text FROM opportunities WHERE id=?`,
				input.OpportunityID).Scan(&currentURL, &currentText); err != nil {
				return Change{}, err
			}
			if source.SourceURL != currentURL || source.ContentSHA256 != sourceDigest(currentText) {
				return Change{}, ErrConflict
			}
		}
		item.SourceExcerpt, err = exactExcerpt(source.OriginalText, input.SpanStart, input.SpanEnd)
		if err != nil {
			return Change{}, err
		}
		item.ExcerptSHA256 = sourceDigest(item.SourceExcerpt)
		item.ObservedAt = source.OccurredAt
		if item.ObservedAt == "" {
			item.ObservedAt = source.RecordedAt
		}
		item.ConfirmationState = "unknown"
		if source.SourceKind == EmployerStatement || source.SourceKind == RecruiterStatement {
			if input.Finding == "explicit_match" || input.Finding == "explicit_mismatch" {
				item.ConfirmationState = "confirmed"
			}
		}
		if input.Criterion == "location_workable" {
			var arrangementSourceID string
			err := tx.QueryRowContext(ctx, `SELECT e.source_id FROM evidence e
  JOIN evidence_sources s ON s.id=e.source_id
  WHERE e.id=? AND e.opportunity_id=? AND e.criterion='location_arrangement'
    AND s.source_kind IN ('employer_statement','recruiter_statement')
    AND s.context_version=? AND s.company_id=? AND s.opportunity_kind=?
    AND NOT EXISTS(SELECT 1 FROM evidence child WHERE child.supersedes_id=e.id)`,
				*input.OwnerWorkableForEvidenceID, input.OpportunityID, contextVersion,
				companyID, opportunityKind).Scan(&arrangementSourceID)
			if errors.Is(err, sql.ErrNoRows) {
				return Change{}, ErrInvalid
			}
			if err != nil {
				return Change{}, err
			}
			item.OwnerWorkableForEvidenceID = *input.OwnerWorkableForEvidenceID
			if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(
				&item.OwnerPreferencesVersion); err != nil {
				return Change{}, err
			}
			if source.OwnerPreferencesVersion != item.OwnerPreferencesVersion {
				return Change{}, ErrConflict
			}
		}
		if priorID != "" {
			var oldOpportunity, oldCriterion string
			err := tx.QueryRowContext(ctx, `SELECT opportunity_id,criterion FROM evidence WHERE id=?`, priorID).Scan(
				&oldOpportunity, &oldCriterion)
			if errors.Is(err, sql.ErrNoRows) {
				return Change{}, ErrNotFound
			}
			if err != nil {
				return Change{}, err
			}
			if oldOpportunity != input.OpportunityID || oldCriterion != input.Criterion {
				return Change{}, ErrInvalid
			}
			var children int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM evidence WHERE supersedes_id=?`, priorID).Scan(&children); err != nil {
				return Change{}, err
			}
			if children != 0 {
				return Change{}, ErrConflict
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO evidence
  (id,opportunity_id,criterion,observed_value,confirmation_state,source_kind,source_url,
   source_excerpt,source_contact_text,observed_at,supersedes_id,created_at,source_id,
   finding,span_start,span_end,excerpt_sha256,hours_min,hours_max,hours_hard,arrangement_pattern,
   arrangement_location,arrangement_remote_geography,arrangement_onsite_days,
   owner_arrangement_evidence_id,owner_preferences_version,salary_currency,salary_period,
   salary_basis,salary_amount_cents,salary_weekly_hours)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			item.ID, item.OpportunityID, item.Criterion, item.ObservedValue, item.ConfirmationState,
			source.SourceKind, optionalText(source.SourceURL), item.SourceExcerpt,
			optionalText(source.SpeakerName), item.ObservedAt, optionalText(priorID), item.CreatedAt,
			source.ID, item.Finding, input.SpanStart, input.SpanEnd, item.ExcerptSHA256,
			optionalHoursMin(input.Hours), optionalHoursMax(input.Hours), optionalHoursHard(input.Hours),
			optionalArrangementPattern(input.Arrangement), optionalArrangementLocation(input.Arrangement),
			optionalArrangementRemote(input.Arrangement), optionalArrangementDays(input.Arrangement),
			optionalText(item.OwnerWorkableForEvidenceID), nullablePositive(item.OwnerPreferencesVersion),
			optionalSalaryCurrency(input.Salary), optionalSalaryPeriod(input.Salary),
			optionalSalaryBasis(input.Salary), optionalSalaryAmount(input.Salary), optionalSalaryHours(input.Salary))
		if err != nil {
			return Change{}, err
		}
		if _, err := evaluateCurrentTx(ctx, tx, actor, input.OpportunityID, item.ID); err != nil {
			return Change{}, err
		}
		return Change{Operation: "evidence.add", EntityKind: "evidence", EntityID: item.ID}, nil
	})
	if err != nil {
		return Evidence{}, "", err
	}
	return item, changeID, nil
}

func nullablePositive(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func optionalHoursMin(v *HoursAvailability) any {
	if v == nil {
		return nil
	}
	return v.MinWeekly
}
func optionalHoursMax(v *HoursAvailability) any {
	if v == nil {
		return nil
	}
	return v.MaxWeekly
}
func optionalHoursHard(v *HoursAvailability) any {
	if v == nil {
		return nil
	}
	return v.HardBounds
}
func optionalArrangementPattern(v *WorkArrangement) any {
	if v == nil {
		return nil
	}
	return v.Pattern
}
func optionalArrangementLocation(v *WorkArrangement) any {
	if v == nil {
		return nil
	}
	return v.BaseLocation
}
func optionalArrangementRemote(v *WorkArrangement) any {
	if v == nil {
		return nil
	}
	return v.RemoteGeography
}
func optionalArrangementDays(v *WorkArrangement) any {
	if v == nil {
		return nil
	}
	return nullableInt(v.OnsiteDays)
}
func optionalSalaryCurrency(v *ActualSalaryFacts) any {
	if v == nil {
		return nil
	}
	return v.Currency
}
func optionalSalaryPeriod(v *ActualSalaryFacts) any {
	if v == nil {
		return nil
	}
	return v.Period
}
func optionalSalaryBasis(v *ActualSalaryFacts) any {
	if v == nil {
		return nil
	}
	return v.Basis
}
func optionalSalaryAmount(v *ActualSalaryFacts) any {
	if v == nil {
		return nil
	}
	return v.AmountCents
}
func optionalSalaryHours(v *ActualSalaryFacts) any {
	if v == nil {
		return nil
	}
	return v.ActualWeeklyHours
}

type EvidencePage struct {
	Items      []Evidence
	NextCursor string
}

type EvidenceSourcePage struct {
	Items      []EvidenceSource
	NextCursor string
}

type evidenceCursor struct {
	Version   int    `json:"v"`
	CreatedAt string `json:"t"`
	ID        string `json:"id"`
	Scope     string `json:"scope"`
}

func encodeEvidenceCursor(cursor evidenceCursor) string {
	b, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeEvidenceCursor(value, scope string) (evidenceCursor, error) {
	if value == "" {
		return evidenceCursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) > 512 {
		return evidenceCursor{}, ErrInvalid
	}
	var cursor evidenceCursor
	if err := json.Unmarshal(b, &cursor); err != nil || cursor.Version != 1 ||
		cursor.CreatedAt == "" || cursor.ID == "" || cursor.Scope != scope {
		return evidenceCursor{}, ErrInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt); err != nil {
		return evidenceCursor{}, ErrInvalid
	}
	return cursor, nil
}

func scanEvidence(row rowScanner) (Evidence, error) {
	var item Evidence
	var sourceID, finding, supersedes, excerptDigest, sourceURL, contact sql.NullString
	var min, max, hard, days, ownerPreference, salaryAmount, salaryHours, spanStart, spanEnd sql.NullInt64
	var pattern, location, remote, arrangementID, salaryCurrency, salaryPeriod, salaryBasis sql.NullString
	err := row.Scan(&item.ID, &item.OpportunityID, &item.Criterion, &item.ObservedValue,
		&item.ConfirmationState, &item.SourceExcerpt, &item.ObservedAt, &supersedes,
		&item.CreatedAt, &sourceID, &item.SourceKind, &sourceURL, &contact,
		&finding, &spanStart, &spanEnd, &excerptDigest, &min, &max, &hard, &pattern, &location,
		&remote, &days, &arrangementID, &ownerPreference, &salaryCurrency,
		&salaryPeriod, &salaryBasis, &salaryAmount, &salaryHours)
	if err != nil {
		return Evidence{}, err
	}
	item.SourceID, item.Finding, item.SupersedesID = sourceID.String, finding.String, supersedes.String
	item.SourceURL, item.SourceContactText, item.Legacy = sourceURL.String, contact.String, !sourceID.Valid
	if spanStart.Valid && spanEnd.Valid {
		item.SpanStart, item.SpanEnd, item.HasSpan = int(spanStart.Int64), int(spanEnd.Int64), true
	}
	item.ExcerptSHA256 = excerptDigest.String
	if min.Valid && max.Valid {
		item.Hours = &HoursAvailability{MinWeekly: min.Int64, MaxWeekly: max.Int64, HardBounds: hard.Valid && hard.Int64 == 1}
	}
	if pattern.Valid {
		item.Arrangement = &WorkArrangement{Pattern: pattern.String, BaseLocation: location.String, RemoteGeography: remote.String}
		if days.Valid {
			item.Arrangement.OnsiteDays = &days.Int64
		}
	}
	item.OwnerWorkableForEvidenceID, item.OwnerPreferencesVersion = arrangementID.String, ownerPreference.Int64
	if salaryAmount.Valid && salaryHours.Valid {
		item.Salary = &ActualSalaryFacts{Currency: salaryCurrency.String, Period: salaryPeriod.String, Basis: salaryBasis.String, AmountCents: salaryAmount.Int64, ActualWeeklyHours: salaryHours.Int64}
	}
	return item, nil
}

const evidenceColumns = `id,opportunity_id,criterion,observed_value,confirmation_state,
  COALESCE(source_excerpt,''),observed_at,supersedes_id,created_at,source_id,
  source_kind,source_url,source_contact_text,finding,span_start,span_end,excerpt_sha256,
  hours_min,hours_max,hours_hard,arrangement_pattern,arrangement_location,
  arrangement_remote_geography,arrangement_onsite_days,owner_arrangement_evidence_id,
  owner_preferences_version,salary_currency,salary_period,salary_basis,
  salary_amount_cents,salary_weekly_hours`

func (s *Store) Evidence(ctx context.Context, id string) (Evidence, error) {
	item, err := scanEvidence(s.db.QueryRowContext(ctx, `SELECT `+evidenceColumns+` FROM evidence WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Evidence{}, ErrNotFound
	}
	return item, err
}

func (s *Store) ListEvidence(ctx context.Context, opportunityID string, includeSuperseded bool, cursorValue string, requestedLimit int) (EvidencePage, error) {
	if opportunityID == "" {
		return EvidencePage{}, ErrInvalid
	}
	limit, err := boundedListLimit(requestedLimit)
	if err != nil {
		return EvidencePage{}, err
	}
	scope := recordListScope("evidence", opportunityID, includeSuperseded)
	cursor, err := decodeEvidenceCursor(cursorValue, scope)
	if err != nil {
		return EvidencePage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+evidenceColumns+` FROM evidence
  WHERE opportunity_id=? AND (? OR NOT EXISTS(SELECT 1 FROM evidence child WHERE child.supersedes_id=evidence.id))
    AND (created_at>? OR (created_at=? AND id>?))
  ORDER BY created_at,id LIMIT ?`, opportunityID, includeSuperseded, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return EvidencePage{}, err
	}
	defer rows.Close()
	page := EvidencePage{Items: make([]Evidence, 0, limit)}
	for rows.Next() {
		item, err := scanEvidence(rows)
		if err != nil {
			return EvidencePage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return EvidencePage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeEvidenceCursor(evidenceCursor{1, last.CreatedAt, last.ID, scope})
	}
	return page, nil
}

func (s *Store) ListEvidenceSources(ctx context.Context, opportunityID, cursorValue string, requestedLimit int) (EvidenceSourcePage, error) {
	if opportunityID == "" {
		return EvidenceSourcePage{}, ErrInvalid
	}
	limit, err := boundedListLimit(requestedLimit)
	if err != nil {
		return EvidenceSourcePage{}, err
	}
	scope := recordListScope("evidence-source", opportunityID)
	cursor, err := decodeEvidenceCursor(cursorValue, scope)
	if err != nil {
		return EvidenceSourcePage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+evidenceSourceColumns+` FROM evidence_sources
  WHERE opportunity_id=? AND (recorded_at>? OR (recorded_at=? AND id>?))
  ORDER BY recorded_at,id LIMIT ?`, opportunityID, cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return EvidenceSourcePage{}, err
	}
	defer rows.Close()
	page := EvidenceSourcePage{Items: make([]EvidenceSource, 0, limit)}
	for rows.Next() {
		item, err := scanEvidenceSource(rows)
		if err != nil {
			return EvidenceSourcePage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return EvidenceSourcePage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeEvidenceCursor(evidenceCursor{1, last.RecordedAt, last.ID, scope})
	}
	return page, nil
}
