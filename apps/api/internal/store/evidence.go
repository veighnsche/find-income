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
	MinHundredths int64
	MaxHundredths int64
	HardBounds    bool
}

type WorkArrangement struct {
	Pattern              string
	BaseLocation         string
	RemoteGeography      string
	OnsiteDaysHundredths *int64
}

type ActualSalaryFacts struct {
	Currency                    string
	Period                      string
	Basis                       string
	AmountCents                 int64
	ActualWeeklyHoursHundredths int64
	AnnualConversion            string
}

type EvidenceInput struct {
	OpportunityID              string
	SourceID                   string
	Criterion                  string
	CriterionID                string
	Presence                   string
	ExpectedPreferencesVersion int64
	OfferOptionID              string
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
	Criterion                  string
	RoleCriterionID            string
	RoleDefinitionHash         string
	RoleDefinition             *RoleCriterion
	RolePreferencesVersion     int64
	RolePresence               string
	OfferOptionID              string
	OwnerLocationFingerprint   string
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
	case "target_hours_available", "location_arrangement", "location_workable", "monthly_base_salary",
		"role_criterion":
		return true
	}
	return false
}

func validateEvidenceInput(input EvidenceInput) error {
	if input.OpportunityID == "" || input.SourceID == "" || !validCriterion(input.Criterion) ||
		input.ExpectedEvidenceVersion < 0 || input.ExpectedPreferencesVersion < 1 ||
		!boundedNonempty(input.ObservedValue, 1000) {
		return fmt.Errorf("%w: evidence identity, criterion, value and version required", ErrInvalid)
	}
	if input.Criterion == "role_criterion" {
		if input.Finding != "" || input.CriterionID == "" || input.ExpectedPreferencesVersion < 1 ||
			!validRolePresence(input.Presence) || input.Hours != nil || input.Arrangement != nil ||
			input.Salary != nil || input.OwnerWorkableForEvidenceID != nil {
			return fmt.Errorf("%w: role observation needs presence and current criterion", ErrInvalid)
		}
	} else {
		if input.CriterionID != "" || input.Presence != "" {
			return fmt.Errorf("%w: role fields on non-role evidence", ErrInvalid)
		}
		switch input.Finding {
		case "explicit_match", "explicit_mismatch", "mention_only", "ambiguous":
		default:
			return fmt.Errorf("%w: invalid evidence finding", ErrInvalid)
		}
	}
	if input.SpanStart < 0 || input.SpanEnd <= input.SpanStart || input.SpanEnd-input.SpanStart > 2000 {
		return fmt.Errorf("%w: exact bounded excerpt required", ErrInvalid)
	}
	switch input.Criterion {
	case "role_criterion":
		// The exact quoted span is checked against the source in the writer.
	case "target_hours_available":
		if input.Arrangement != nil || input.Salary != nil || input.OwnerWorkableForEvidenceID != nil ||
			input.Finding == "explicit_match" && (input.Hours == nil ||
				hoursMinimum(input.Hours) < 100 || hoursMaximum(input.Hours) > 16800 ||
				hoursMaximum(input.Hours) < hoursMinimum(input.Hours)) ||
			(input.Finding == "mention_only" || input.Finding == "ambiguous") && input.Hours != nil ||
			input.Finding == "explicit_mismatch" {
			return fmt.Errorf("%w: numeric hours availability required", ErrInvalid)
		}
	case "location_arrangement":
		if input.Hours != nil || input.Salary != nil || input.OwnerWorkableForEvidenceID != nil ||
			input.Finding == "explicit_match" && input.Arrangement == nil ||
			(input.Finding == "mention_only" || input.Finding == "ambiguous") && input.Arrangement != nil ||
			input.Finding == "explicit_mismatch" {
			return fmt.Errorf("%w: arrangement required", ErrInvalid)
		}
		if input.Arrangement != nil && (input.Arrangement.Pattern != "onsite" && input.Arrangement.Pattern != "hybrid" && input.Arrangement.Pattern != "remote" ||
			len(input.Arrangement.BaseLocation) > 500 || len(input.Arrangement.RemoteGeography) > 500 ||
			input.Arrangement.Pattern == "remote" && strings.TrimSpace(input.Arrangement.RemoteGeography) == "" ||
			(input.Arrangement.Pattern == "onsite" || input.Arrangement.Pattern == "hybrid") &&
				strings.TrimSpace(input.Arrangement.BaseLocation) == "" ||
			onsiteDaysHundredths(input.Arrangement) > 700) {
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
		if input.Hours != nil || input.Arrangement != nil || input.OwnerWorkableForEvidenceID != nil ||
			input.Finding == "explicit_match" && (input.Salary == nil ||
				input.Salary.AmountCents < 0 || salaryHoursHundredths(input.Salary) < 100 ||
				salaryHoursHundredths(input.Salary) > 16800 || !currencyCode(input.Salary.Currency) ||
				!validObservedValue(input.Salary.Period, "month", "year", "hour", "project") ||
				!validObservedValue(input.Salary.Basis, "base", "inclusive", "unknown") ||
				input.Salary.AnnualConversion != "" &&
					(input.Salary.AnnualConversion != "twelve_equal_monthly_base_payments" ||
						input.Salary.Period != "year" || input.Salary.Basis != "base")) ||
			(input.Finding == "mention_only" || input.Finding == "ambiguous") && input.Salary != nil ||
			input.Finding == "explicit_mismatch" {
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

func hoursMinimum(v *HoursAvailability) int64 {
	return v.MinHundredths
}
func hoursMaximum(v *HoursAvailability) int64 {
	return v.MaxHundredths
}
func salaryHoursHundredths(v *ActualSalaryFacts) int64 {
	return v.ActualWeeklyHoursHundredths
}
func onsiteDaysHundredths(v *WorkArrangement) int64 {
	if v.OnsiteDaysHundredths != nil {
		return *v.OnsiteDaysHundredths
	}
	return 0
}

// Workability depends on the location policy and the cited arrangement, not
// on unrelated role, hours, salary, or timezone edits to the profile.
func locationFingerprint(p Preferences, arrangement WorkArrangement) string {
	value, _ := json.Marshal(struct {
		PreferredLocation string
		AllowRemote       bool
		AllowHybrid       bool
		Arrangement       WorkArrangement
	}{p.PreferredLocation, p.AllowRemote, p.AllowHybrid, arrangement})
	return sourceDigest(string(value))
}

func validRolePresence(value string) bool {
	switch value {
	case "explicit_presence", "explicit_absence", "mention_only", "ambiguous":
		return true
	}
	return false
}

func findingForRolePresence(value string) string {
	switch value {
	case "explicit_presence":
		return "explicit_match"
	case "explicit_absence":
		return "explicit_mismatch"
	default:
		return value
	}
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
	// AmountCents is a fixed 1/100 unit representation. Keep the supported
	// set explicit until currency exponents are represented in the schema.
	switch value {
	case "EUR", "GBP", "USD", "CAD", "AUD", "CHF", "NZD":
		return true
	}
	return false
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
	return s.writeEvidence(ctx, actor, "", input, nil)
}

func (s *Store) SupersedeEvidence(ctx context.Context, actor Actor, priorID string, input EvidenceInput) (Evidence, string, error) {
	if priorID == "" {
		return Evidence{}, "", fmt.Errorf("%w: prior evidence required", ErrInvalid)
	}
	return s.writeEvidence(ctx, actor, priorID, input, nil)
}

func (s *Store) writeEvidence(ctx context.Context, actor Actor, priorID string, input EvidenceInput, guard func(*sql.Tx) error) (Evidence, string, error) {
	return s.writeEvidenceAfter(ctx, actor, priorID, input, guard, nil)
}

func (s *Store) writeEvidenceAfter(ctx context.Context, actor Actor, priorID string, input EvidenceInput,
	guard func(*sql.Tx) error, after func(*sql.Tx, string, Evidence) error) (Evidence, string, error) {
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
		Salary: input.Salary, SpanStart: input.SpanStart, SpanEnd: input.SpanEnd, HasSpan: true,
		OfferOptionID: input.OfferOptionID}
	if input.Criterion == "role_criterion" {
		item.Finding = findingForRolePresence(input.Presence)
		item.RolePresence = input.Presence
	}
	changeID, err := s.writeAuditedAfter(ctx, actor, func(tx *sql.Tx) (Change, error) {
		if err := lockQualificationInput(ctx, tx, input.OpportunityID); err != nil {
			return Change{}, err
		}
		if guard != nil {
			if err := guard(tx); err != nil {
				return Change{}, err
			}
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
		preferences, err := scanPreferences(tx.QueryRowContext(ctx, `SELECT `+preferenceColumns+`
  FROM preferences_versions p JOIN preferences_current c ON c.version=p.version WHERE c.singleton=1`))
		if err != nil {
			return Change{}, err
		}
		if preferences.Version != input.ExpectedPreferencesVersion {
			return Change{}, ErrConflict
		}
		if input.Criterion == "role_criterion" {
			for _, criterion := range preferences.RoleCriteria {
				if criterion.ID == input.CriterionID {
					item.RoleCriterionID = criterion.ID
					item.RoleDefinitionHash = criterion.DefinitionHash()
					criterionCopy := criterion
					item.RoleDefinition = &criterionCopy
					item.RolePreferencesVersion = preferences.Version
					break
				}
			}
			if item.RoleCriterionID == "" {
				return Change{}, fmt.Errorf("%w: criterion not in current profile", ErrInvalid)
			}
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
			input.Criterion == "monthly_base_salary" && opportunityKind != "employment" ||
			input.Criterion == "monthly_base_salary" && input.Salary != nil && !directSource(source.SourceKind) {
			return Change{}, fmt.Errorf("%w: source cannot establish this criterion", ErrInvalid)
		}
		if input.OfferOptionID != "" {
			var setOpportunityID, setSourceURL, setSourceHash, currentURL, currentText string
			var setSourceKind EvidenceSourceKind
			var setContext int64
			err := tx.QueryRowContext(ctx, `SELECT sets.opportunity_id,sets.context_version,
  set_source.source_kind,COALESCE(set_source.source_url,''),set_source.content_sha256,
  COALESCE(o.source_url,''),o.original_text
  FROM offer_options option JOIN offer_option_sets sets ON sets.id=option.set_id
  JOIN evidence_sources set_source ON set_source.id=sets.source_id
  JOIN opportunities o ON o.id=sets.opportunity_id
  WHERE option.id=? AND NOT EXISTS
    (SELECT 1 FROM offer_option_sets child WHERE child.supersedes_id=sets.id)`, input.OfferOptionID).
				Scan(&setOpportunityID, &setContext, &setSourceKind, &setSourceURL, &setSourceHash, &currentURL, &currentText)
			if errors.Is(err, sql.ErrNoRows) || setOpportunityID != input.OpportunityID || setContext != contextVersion {
				return Change{}, fmt.Errorf("%w: option is not current for this opportunity", ErrInvalid)
			}
			if err != nil {
				return Change{}, err
			}
			if setSourceKind == VacancySnapshot && (setSourceURL != currentURL || setSourceHash != sourceDigest(currentText)) {
				return Change{}, ErrConflict
			}
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
			if item.Finding == "explicit_match" || item.Finding == "explicit_mismatch" {
				item.ConfirmationState = "confirmed"
			}
		}
		if input.Criterion == "location_workable" {
			var arrangementSourceID string
			var pattern, baseLocation, remoteGeography string
			var days sql.NullInt64
			var optionID sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT e.source_id,e.arrangement_pattern,e.arrangement_location,
    e.arrangement_remote_geography,e.arrangement_onsite_days_hundredths,e.offer_option_id FROM evidence e
  JOIN evidence_sources s ON s.id=e.source_id
  WHERE e.id=? AND e.opportunity_id=? AND e.criterion='location_arrangement'
    AND s.source_kind IN ('vacancy_snapshot','employer_statement','recruiter_statement')
    AND s.context_version=? AND s.company_id=? AND s.opportunity_kind=?
    AND NOT EXISTS(SELECT 1 FROM evidence child WHERE child.supersedes_id=e.id)`,
				*input.OwnerWorkableForEvidenceID, input.OpportunityID, contextVersion,
				companyID, opportunityKind).Scan(&arrangementSourceID, &pattern, &baseLocation, &remoteGeography, &days, &optionID)
			if errors.Is(err, sql.ErrNoRows) {
				return Change{}, ErrInvalid
			}
			if err != nil {
				return Change{}, err
			}
			if optionID.String != input.OfferOptionID {
				return Change{}, fmt.Errorf("%w: owner assessment must cite the same option", ErrInvalid)
			}
			item.OwnerWorkableForEvidenceID = *input.OwnerWorkableForEvidenceID
			item.OwnerPreferencesVersion = preferences.Version
			if source.OwnerPreferencesVersion != item.OwnerPreferencesVersion {
				return Change{}, ErrConflict
			}
			arrangement := WorkArrangement{Pattern: pattern, BaseLocation: baseLocation, RemoteGeography: remoteGeography}
			if days.Valid {
				arrangement.OnsiteDaysHundredths = &days.Int64
			}
			item.OwnerLocationFingerprint = locationFingerprint(preferences, arrangement)
		}
		if priorID != "" {
			var oldOpportunity, oldCriterion string
			var oldRoleID, oldRoleHash, oldOptionID sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT opportunity_id,criterion,role_criterion_id,role_definition_hash,offer_option_id
			  FROM evidence WHERE id=?`, priorID).Scan(&oldOpportunity, &oldCriterion, &oldRoleID, &oldRoleHash, &oldOptionID)
			if errors.Is(err, sql.ErrNoRows) {
				return Change{}, ErrNotFound
			}
			if err != nil {
				return Change{}, err
			}
			if oldOpportunity != input.OpportunityID || oldCriterion != input.Criterion {
				return Change{}, ErrInvalid
			}
			if oldOptionID.String != item.OfferOptionID {
				return Change{}, ErrConflict
			}
			if input.Criterion == "role_criterion" && (oldRoleID.String != item.RoleCriterionID ||
				oldRoleHash.String != item.RoleDefinitionHash) {
				return Change{}, ErrConflict
			}
			var children int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM evidence WHERE supersedes_id=?`, priorID).Scan(&children); err != nil {
				return Change{}, err
			}
			if children != 0 {
				return Change{}, ErrConflict
			}
		}
		insertArgs := []any{
			item.ID, item.OpportunityID, item.Criterion, item.ObservedValue, item.ConfirmationState,
			source.SourceKind, optionalText(source.SourceURL), item.SourceExcerpt,
			optionalText(source.SpeakerName), item.ObservedAt, optionalText(priorID), item.CreatedAt,
			source.ID, item.Finding, input.SpanStart, input.SpanEnd, item.ExcerptSHA256,
			optionalHoursHard(input.Hours),
			optionalArrangementPattern(input.Arrangement), optionalArrangementLocation(input.Arrangement),
			optionalArrangementRemote(input.Arrangement),
			optionalText(item.OwnerWorkableForEvidenceID), nullablePositive(item.OwnerPreferencesVersion),
			optionalSalaryCurrency(input.Salary), optionalSalaryPeriod(input.Salary),
			optionalSalaryBasis(input.Salary), optionalSalaryAmount(input.Salary),
			optionalText(item.RoleCriterionID), optionalText(item.RoleDefinitionHash),
			optionalRoleDefinition(item.RoleDefinition),
			nullablePositive(item.RolePreferencesVersion), optionalText(item.RolePresence),
			optionalText(item.OfferOptionID), optionalText(item.OwnerLocationFingerprint),
			optionalHoursMinHundredths(input.Hours), optionalHoursMaxHundredths(input.Hours),
			optionalOnsiteDaysHundredths(input.Arrangement), optionalSalaryHoursHundredths(input.Salary),
			optionalSalaryAnnualConversion(input.Salary),
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(insertArgs)), ",")
		_, err = tx.ExecContext(ctx, `INSERT INTO evidence
  (id,opportunity_id,criterion,observed_value,confirmation_state,source_kind,source_url,
   source_excerpt,source_contact_text,observed_at,supersedes_id,created_at,source_id,
   finding,span_start,span_end,excerpt_sha256,hours_hard,arrangement_pattern,
   arrangement_location,arrangement_remote_geography,
   owner_arrangement_evidence_id,owner_preferences_version,salary_currency,salary_period,
   salary_basis,salary_amount_cents,role_criterion_id,
   role_definition_hash,role_definition_json,role_preferences_version,role_presence,offer_option_id,
   owner_location_fingerprint,hours_min_hundredths,hours_max_hundredths,
   arrangement_onsite_days_hundredths,salary_weekly_hours_hundredths,salary_annual_conversion)
  VALUES (`+placeholders+`)`, insertArgs...)
		if err != nil {
			return Change{}, err
		}
		if _, err := evaluateCurrentTx(ctx, tx, actor, input.OpportunityID, item.ID); err != nil {
			return Change{}, err
		}
		if guard != nil {
			if err := guard(tx); err != nil {
				return Change{}, err
			}
		}
		operation := "evidence.add"
		if priorID != "" {
			operation = "evidence.supersede"
		}
		return Change{Operation: operation, EntityKind: "evidence", EntityID: item.ID}, nil
	}, func(tx *sql.Tx, auditID string) error {
		if after != nil {
			return after(tx, auditID, item)
		}
		return nil
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
func optionalHoursMinHundredths(v *HoursAvailability) any {
	if v == nil {
		return nil
	}
	return v.MinHundredths
}
func optionalHoursMaxHundredths(v *HoursAvailability) any {
	if v == nil {
		return nil
	}
	return v.MaxHundredths
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
func optionalOnsiteDaysHundredths(v *WorkArrangement) any {
	if v == nil {
		return nil
	}
	if v.OnsiteDaysHundredths != nil {
		return *v.OnsiteDaysHundredths
	}
	return nil
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
func optionalSalaryHoursHundredths(v *ActualSalaryFacts) any {
	if v == nil {
		return nil
	}
	return v.ActualWeeklyHoursHundredths
}
func optionalSalaryAnnualConversion(v *ActualSalaryFacts) any {
	if v == nil {
		return nil
	}
	return optionalText(v.AnnualConversion)
}

func optionalRoleDefinition(value *RoleCriterion) any {
	if value == nil {
		return nil
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
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
	var hard, ownerPreference, salaryAmount, spanStart, spanEnd sql.NullInt64
	var minScaled, maxScaled, salaryHoursScaled, daysScaled, rolePreference sql.NullInt64
	var pattern, location, remote, arrangementID, salaryCurrency, salaryPeriod, salaryBasis sql.NullString
	var roleID, roleHash, roleJSON, rolePresence, optionID, locationFingerprint, annualConversion sql.NullString
	err := row.Scan(&item.ID, &item.OpportunityID, &item.Criterion, &item.ObservedValue,
		&item.ConfirmationState, &item.SourceExcerpt, &item.ObservedAt, &supersedes,
		&item.CreatedAt, &sourceID, &item.SourceKind, &sourceURL, &contact,
		&finding, &spanStart, &spanEnd, &excerptDigest, &hard, &pattern, &location,
		&remote, &arrangementID, &ownerPreference, &salaryCurrency,
		&salaryPeriod, &salaryBasis, &salaryAmount,
		&roleID, &roleHash, &roleJSON, &rolePreference, &rolePresence, &optionID, &locationFingerprint,
		&minScaled, &maxScaled, &daysScaled, &salaryHoursScaled, &annualConversion)
	if err != nil {
		return Evidence{}, err
	}
	item.SourceID, item.Finding, item.SupersedesID = sourceID.String, finding.String, supersedes.String
	item.SourceURL, item.SourceContactText = sourceURL.String, contact.String
	item.RoleCriterionID, item.RoleDefinitionHash = roleID.String, roleHash.String
	if roleJSON.Valid {
		var definition RoleCriterion
		if err := json.Unmarshal([]byte(roleJSON.String), &definition); err != nil {
			return Evidence{}, err
		}
		item.RoleDefinition = &definition
	}
	item.RolePreferencesVersion, item.RolePresence = rolePreference.Int64, rolePresence.String
	item.OfferOptionID, item.OwnerLocationFingerprint = optionID.String, locationFingerprint.String
	if spanStart.Valid && spanEnd.Valid {
		item.SpanStart, item.SpanEnd, item.HasSpan = int(spanStart.Int64), int(spanEnd.Int64), true
	}
	item.ExcerptSHA256 = excerptDigest.String
	if minScaled.Valid && maxScaled.Valid {
		item.Hours = &HoursAvailability{MinHundredths: minScaled.Int64, MaxHundredths: maxScaled.Int64,
			HardBounds: hard.Valid && hard.Int64 == 1}
	}
	if pattern.Valid {
		item.Arrangement = &WorkArrangement{Pattern: pattern.String, BaseLocation: location.String, RemoteGeography: remote.String}
		if daysScaled.Valid {
			item.Arrangement.OnsiteDaysHundredths = &daysScaled.Int64
		}
	}
	item.OwnerWorkableForEvidenceID, item.OwnerPreferencesVersion = arrangementID.String, ownerPreference.Int64
	if salaryAmount.Valid && salaryHoursScaled.Valid {
		item.Salary = &ActualSalaryFacts{Currency: salaryCurrency.String, Period: salaryPeriod.String,
			Basis: salaryBasis.String, AmountCents: salaryAmount.Int64,
			ActualWeeklyHoursHundredths: salaryHoursScaled.Int64,
			AnnualConversion:            annualConversion.String}
	}
	return item, nil
}

const evidenceColumns = `id,opportunity_id,criterion,observed_value,confirmation_state,
  COALESCE(source_excerpt,''),observed_at,supersedes_id,created_at,source_id,
  source_kind,source_url,source_contact_text,finding,span_start,span_end,excerpt_sha256,
  hours_hard,arrangement_pattern,arrangement_location,
  arrangement_remote_geography,owner_arrangement_evidence_id,
  owner_preferences_version,salary_currency,salary_period,salary_basis,
  salary_amount_cents,role_criterion_id,role_definition_hash,role_definition_json,
  role_preferences_version,role_presence,offer_option_id,owner_location_fingerprint,
  hours_min_hundredths,hours_max_hundredths,arrangement_onsite_days_hundredths,
  salary_weekly_hours_hundredths,salary_annual_conversion`

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
