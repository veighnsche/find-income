package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AdvertisedCompensation struct {
	Currency       string
	MinAmountCents *int64
	MaxAmountCents *int64
	Period         string
	ReferenceHours *int64
	Basis          string
	BenefitsText   string
}

// Opportunity is the current record. Historical source text and compensation
// snapshots are available through RecordChange events, not inferred from this
// mutable current view. Confirmed actual-hours pay is deliberately outside T08.
type Opportunity struct {
	ID           string
	CompanyID    string
	Title        string
	Kind         string
	SourceURL    string
	OriginalText string
	Notes        string
	Stage        string
	WorkPattern  string
	LocationText string
	PostedOn     string
	DeadlineOn   string
	ArchivedAt   string
	Revision     int64
	CreatedAt    string
	UpdatedAt    string
	Compensation AdvertisedCompensation
}

type OpportunityInput struct {
	CompanyID    string
	Title        string
	Kind         string
	SourceURL    string
	OriginalText string
	Notes        string
	Stage        string
	WorkPattern  string
	LocationText string
	PostedOn     string
	DeadlineOn   string
	Compensation AdvertisedCompensation
}

type OpportunityPatch struct {
	ExpectedRevision int64
	CompanyID        *string
	Title            *string
	Kind             *string
	SourceURL        *string
	OriginalText     *string
	Notes            *string
	Stage            *string
	WorkPattern      *string
	LocationText     *string
	PostedOn         *string
	DeadlineOn       *string
	Compensation     *AdvertisedCompensation // replaces advertised fields only
}

type OpportunityListOptions struct {
	Cursor          string
	Limit           int
	IncludeArchived bool
	CompanyID       string
	Stage           string
	Kind            string
}

type OpportunityPage struct {
	Items      []Opportunity
	NextCursor string
}

type OpportunityDuplicate struct {
	Opportunity Opportunity
	Reason      string // same_source_url or same_company_title
}

const opportunityColumns = `o.id,o.company_id,o.title,o.kind,o.source_url,o.original_text,o.notes,
  o.stage,o.work_pattern,o.location_text,o.posted_on,o.deadline_on,o.archived_at,
  o.revision,o.created_at,o.updated_at,c.currency,c.min_amount_cents,
  c.max_amount_cents,c.period,c.reference_hours,c.basis,c.benefits_text`

const opportunityFrom = ` FROM opportunities o LEFT JOIN compensation c ON c.opportunity_id=o.id`

func defaultCompensation() AdvertisedCompensation {
	return AdvertisedCompensation{Currency: "unknown", Period: "unknown", Basis: "unknown"}
}

func validateCompensation(input AdvertisedCompensation) (AdvertisedCompensation, error) {
	input.Currency = strings.TrimSpace(input.Currency)
	if input.Currency == "" {
		input.Currency = "unknown"
	}
	if input.Period == "" {
		input.Period = "unknown"
	}
	if input.Basis == "" {
		input.Basis = "unknown"
	}
	if input.Currency != "unknown" {
		if len(input.Currency) != 3 {
			return input, fmt.Errorf("%w: currency must be ISO-style uppercase code", ErrInvalid)
		}
		for _, r := range input.Currency {
			if r < 'A' || r > 'Z' {
				return input, fmt.Errorf("%w: currency must be ISO-style uppercase code", ErrInvalid)
			}
		}
	}
	if input.MinAmountCents != nil && *input.MinAmountCents < 0 ||
		input.MaxAmountCents != nil && (input.MinAmountCents == nil || *input.MaxAmountCents < *input.MinAmountCents) ||
		input.ReferenceHours != nil && (*input.ReferenceHours < 1 || *input.ReferenceHours > 168) ||
		len(input.BenefitsText) > 10000 {
		return input, fmt.Errorf("%w: invalid advertised compensation", ErrInvalid)
	}
	if (input.MinAmountCents != nil || input.MaxAmountCents != nil) && input.Currency == "unknown" {
		return input, fmt.Errorf("%w: advertised amount requires currency", ErrInvalid)
	}
	switch input.Period {
	case "month", "year", "hour", "project", "unknown":
	default:
		return input, fmt.Errorf("%w: invalid compensation period", ErrInvalid)
	}
	switch input.Basis {
	case "base", "inclusive", "unknown":
	default:
		return input, fmt.Errorf("%w: invalid compensation basis", ErrInvalid)
	}
	return input, nil
}

func validCalendarDate(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validStage(value string) bool {
	if len(value) == 0 || len(value) > 40 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func validateOpportunity(input OpportunityInput) (OpportunityInput, error) {
	input.CompanyID = strings.TrimSpace(input.CompanyID)
	input.Title = strings.TrimSpace(input.Title)
	input.SourceURL = strings.TrimSpace(input.SourceURL)
	if input.WorkPattern == "" {
		input.WorkPattern = "unknown"
	}
	if input.CompanyID == "" || len(input.CompanyID) > 100 || input.Title == "" || len(input.Title) > 300 ||
		(input.Kind != "employment" && input.Kind != "project") || !validStage(input.Stage) ||
		(input.WorkPattern != "unknown" && input.WorkPattern != "onsite" &&
			input.WorkPattern != "hybrid" && input.WorkPattern != "remote") ||
		len(input.OriginalText) > 200000 || len(input.Notes) > 10000 || len(input.LocationText) > 500 ||
		(input.SourceURL == "" && strings.TrimSpace(input.OriginalText) == "") ||
		!validCalendarDate(input.PostedOn) || !validCalendarDate(input.DeadlineOn) ||
		(input.PostedOn != "" && input.DeadlineOn != "" && input.DeadlineOn < input.PostedOn) {
		return input, fmt.Errorf("%w: invalid opportunity fields", ErrInvalid)
	}
	if err := validateWebURL(input.SourceURL); err != nil {
		return input, err
	}
	var err error
	input.Compensation, err = validateCompensation(input.Compensation)
	return input, err
}

func scanOpportunity(row rowScanner) (Opportunity, error) {
	var opportunity Opportunity
	var source, posted, deadline, archived sql.NullString
	var currency, period, basis, benefits sql.NullString
	var minimum, maximum, hours sql.NullInt64
	err := row.Scan(&opportunity.ID, &opportunity.CompanyID, &opportunity.Title, &opportunity.Kind,
		&source, &opportunity.OriginalText, &opportunity.Notes, &opportunity.Stage, &opportunity.WorkPattern,
		&opportunity.LocationText, &posted, &deadline, &archived, &opportunity.Revision,
		&opportunity.CreatedAt, &opportunity.UpdatedAt, &currency, &minimum, &maximum,
		&period, &hours, &basis, &benefits)
	if err != nil {
		return Opportunity{}, err
	}
	opportunity.SourceURL, opportunity.PostedOn, opportunity.DeadlineOn, opportunity.ArchivedAt =
		source.String, posted.String, deadline.String, archived.String
	opportunity.Compensation = defaultCompensation()
	if currency.Valid {
		opportunity.Compensation.Currency = currency.String
	}
	if period.Valid {
		opportunity.Compensation.Period = period.String
	}
	if basis.Valid {
		opportunity.Compensation.Basis = basis.String
	}
	opportunity.Compensation.BenefitsText = benefits.String
	if minimum.Valid {
		opportunity.Compensation.MinAmountCents = &minimum.Int64
	}
	if maximum.Valid {
		opportunity.Compensation.MaxAmountCents = &maximum.Int64
	}
	if hours.Valid {
		opportunity.Compensation.ReferenceHours = &hours.Int64
	}
	return opportunity, nil
}

func (s *Store) Opportunity(ctx context.Context, id string) (Opportunity, error) {
	record, err := scanOpportunity(s.db.QueryRowContext(ctx, `SELECT `+opportunityColumns+opportunityFrom+` WHERE o.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Opportunity{}, ErrNotFound
	}
	return record, err
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func writeAdvertisedCompensation(ctx context.Context, tx *sql.Tx, opportunityID string, value AdvertisedCompensation) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO compensation
  (opportunity_id,currency,min_amount_cents,max_amount_cents,period,reference_hours,basis,benefits_text)
  VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(opportunity_id) DO UPDATE SET
  currency=excluded.currency,min_amount_cents=excluded.min_amount_cents,
  max_amount_cents=excluded.max_amount_cents,period=excluded.period,
  reference_hours=excluded.reference_hours,basis=excluded.basis,benefits_text=excluded.benefits_text`,
		opportunityID, value.Currency, nullableInt(value.MinAmountCents), nullableInt(value.MaxAmountCents),
		value.Period, nullableInt(value.ReferenceHours), value.Basis, value.BenefitsText)
	return err
}

func (s *Store) CreateOpportunity(ctx context.Context, actor Actor, input OpportunityInput) (Opportunity, string, error) {
	input, err := validateOpportunity(input)
	if err != nil {
		return Opportunity{}, "", err
	}
	id, err := randomID()
	if err != nil {
		return Opportunity{}, "", err
	}
	now := recordNow()
	record := Opportunity{ID: id, CompanyID: input.CompanyID, Title: input.Title, Kind: input.Kind,
		SourceURL: input.SourceURL, OriginalText: input.OriginalText, Notes: input.Notes, Stage: input.Stage,
		WorkPattern: input.WorkPattern, LocationText: input.LocationText, PostedOn: input.PostedOn,
		DeadlineOn: input.DeadlineOn, Revision: 1, CreatedAt: now, UpdatedAt: now,
		Compensation: input.Compensation}
	revision := int64(1)
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, `INSERT INTO opportunities
  (id,company_id,title,kind,source_url,original_text,notes,stage,work_pattern,location_text,
   posted_on,deadline_on,revision,created_at,updated_at)
  SELECT ?,?,?,?,?,?,?,?,?,?,?,?,1,?,? FROM companies
  WHERE id=? AND archived_at IS NULL`, id, input.CompanyID, input.Title, input.Kind,
			optionalText(input.SourceURL), input.OriginalText, input.Notes, input.Stage, input.WorkPattern,
			input.LocationText, optionalText(input.PostedOn), optionalText(input.DeadlineOn), now, now,
			input.CompanyID)
		if err != nil {
			return Change{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return Change{}, err
		}
		if count != 1 {
			return Change{}, fmt.Errorf("%w: active company required", ErrInvalid)
		}
		if err := writeAdvertisedCompensation(ctx, tx, id, input.Compensation); err != nil {
			return Change{}, err
		}
		return Change{Operation: "opportunity.create", EntityKind: "opportunity", EntityID: id,
			RevisionAfter: &revision}, nil
	})
	if err != nil {
		return Opportunity{}, "", err
	}
	return record, changeID, nil
}

func opportunityInputFromRecord(record Opportunity) OpportunityInput {
	return OpportunityInput{CompanyID: record.CompanyID, Title: record.Title, Kind: record.Kind,
		SourceURL: record.SourceURL, OriginalText: record.OriginalText, Notes: record.Notes, Stage: record.Stage,
		WorkPattern: record.WorkPattern, LocationText: record.LocationText, PostedOn: record.PostedOn,
		DeadlineOn: record.DeadlineOn, Compensation: record.Compensation}
}

func applyOpportunityPatch(input *OpportunityInput, patch OpportunityPatch) {
	if patch.CompanyID != nil {
		input.CompanyID = *patch.CompanyID
	}
	if patch.Title != nil {
		input.Title = *patch.Title
	}
	if patch.Kind != nil {
		input.Kind = *patch.Kind
	}
	if patch.SourceURL != nil {
		input.SourceURL = *patch.SourceURL
	}
	if patch.OriginalText != nil {
		input.OriginalText = *patch.OriginalText
	}
	if patch.Notes != nil {
		input.Notes = *patch.Notes
	}
	if patch.Stage != nil {
		input.Stage = *patch.Stage
	}
	if patch.WorkPattern != nil {
		input.WorkPattern = *patch.WorkPattern
	}
	if patch.LocationText != nil {
		input.LocationText = *patch.LocationText
	}
	if patch.PostedOn != nil {
		input.PostedOn = *patch.PostedOn
	}
	if patch.DeadlineOn != nil {
		input.DeadlineOn = *patch.DeadlineOn
	}
	if patch.Compensation != nil {
		input.Compensation = *patch.Compensation
	}
}

func emptyOpportunityPatch(patch OpportunityPatch) bool {
	return patch.CompanyID == nil && patch.Title == nil && patch.Kind == nil && patch.SourceURL == nil &&
		patch.OriginalText == nil && patch.Notes == nil && patch.Stage == nil && patch.WorkPattern == nil &&
		patch.LocationText == nil && patch.PostedOn == nil && patch.DeadlineOn == nil && patch.Compensation == nil
}

func (s *Store) PatchOpportunity(ctx context.Context, actor Actor, id string, patch OpportunityPatch) (Opportunity, string, error) {
	if id == "" || patch.ExpectedRevision < 1 || emptyOpportunityPatch(patch) {
		return Opportunity{}, "", fmt.Errorf("%w: opportunity patch and expected revision required", ErrInvalid)
	}
	current, err := s.Opportunity(ctx, id)
	if err != nil {
		return Opportunity{}, "", err
	}
	if current.Revision != patch.ExpectedRevision || current.ArchivedAt != "" {
		return Opportunity{}, "", ErrConflict
	}
	input := opportunityInputFromRecord(current)
	applyOpportunityPatch(&input, patch)
	input, err = validateOpportunity(input)
	if err != nil {
		return Opportunity{}, "", err
	}
	updated := Opportunity{ID: id, CompanyID: input.CompanyID, Title: input.Title, Kind: input.Kind,
		SourceURL: input.SourceURL, OriginalText: input.OriginalText, Notes: input.Notes, Stage: input.Stage,
		WorkPattern: input.WorkPattern, LocationText: input.LocationText, PostedOn: input.PostedOn,
		DeadlineOn: input.DeadlineOn, Revision: current.Revision + 1,
		CreatedAt: current.CreatedAt, UpdatedAt: recordNow(), Compensation: input.Compensation}
	companyChanged := input.CompanyID != current.CompanyID
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, `UPDATE opportunities SET company_id=?,title=?,kind=?,
  source_url=?,original_text=?,notes=?,stage=?,work_pattern=?,location_text=?,posted_on=?,deadline_on=?,
  revision=?,updated_at=? WHERE id=? AND revision=? AND archived_at IS NULL AND
  (?=0 OR EXISTS(SELECT 1 FROM companies WHERE id=? AND archived_at IS NULL))`,
			updated.CompanyID, updated.Title, updated.Kind, optionalText(updated.SourceURL),
			updated.OriginalText, updated.Notes, updated.Stage, updated.WorkPattern, updated.LocationText,
			optionalText(updated.PostedOn), optionalText(updated.DeadlineOn), updated.Revision,
			updated.UpdatedAt, id, current.Revision, companyChanged, input.CompanyID)
		if err != nil {
			return Change{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return Change{}, err
		}
		if count != 1 {
			return Change{}, ErrConflict
		}
		if err := writeAdvertisedCompensation(ctx, tx, id, input.Compensation); err != nil {
			return Change{}, err
		}
		return Change{Operation: "opportunity.patch", EntityKind: "opportunity", EntityID: id,
			RevisionBefore: &current.Revision, RevisionAfter: &updated.Revision}, nil
	})
	if err != nil {
		return Opportunity{}, "", err
	}
	return updated, changeID, nil
}

func (s *Store) ArchiveOpportunity(ctx context.Context, actor Actor, id string, expectedRevision int64) (Opportunity, string, error) {
	if id == "" || expectedRevision < 1 {
		return Opportunity{}, "", fmt.Errorf("%w: opportunity and expected revision required", ErrInvalid)
	}
	current, err := s.Opportunity(ctx, id)
	if err != nil {
		return Opportunity{}, "", err
	}
	if current.Revision != expectedRevision || current.ArchivedAt != "" {
		return Opportunity{}, "", ErrConflict
	}
	archived := current
	archived.Revision++
	archived.ArchivedAt = recordNow()
	archived.UpdatedAt = archived.ArchivedAt
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, `UPDATE opportunities SET archived_at=?,revision=?,updated_at=?
  WHERE id=? AND revision=? AND archived_at IS NULL`, archived.ArchivedAt, archived.Revision,
			archived.UpdatedAt, id, current.Revision)
		if err != nil {
			return Change{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return Change{}, err
		}
		if count != 1 {
			return Change{}, ErrConflict
		}
		return Change{Operation: "opportunity.archive", EntityKind: "opportunity", EntityID: id,
			RevisionBefore: &current.Revision, RevisionAfter: &archived.Revision}, nil
	})
	if err != nil {
		return Opportunity{}, "", err
	}
	return archived, changeID, nil
}

func (s *Store) ListOpportunities(ctx context.Context, options OpportunityListOptions) (OpportunityPage, error) {
	limit, err := boundedListLimit(options.Limit)
	if err != nil {
		return OpportunityPage{}, err
	}
	cursor, err := decodeRecordCursor(options.Cursor)
	if err != nil {
		return OpportunityPage{}, err
	}
	if options.Kind != "" && options.Kind != "employment" && options.Kind != "project" ||
		options.Stage != "" && !validStage(options.Stage) || len(options.CompanyID) > 100 {
		return OpportunityPage{}, fmt.Errorf("%w: invalid opportunity list filter", ErrInvalid)
	}
	scope := recordListScope("opportunity", options.IncludeArchived, options.CompanyID, options.Stage, options.Kind)
	if options.Cursor != "" && cursor.Scope != scope {
		return OpportunityPage{}, fmt.Errorf("%w: changed opportunity list filter", ErrInvalid)
	}
	query := `SELECT ` + opportunityColumns + opportunityFrom + ` WHERE
  (? OR o.archived_at IS NULL) AND (?='' OR o.company_id=?) AND
  (?='' OR o.stage=?) AND (?='' OR o.kind=?) AND
  (o.created_at>? OR (o.created_at=? AND o.id>?))
  ORDER BY o.created_at,o.id LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, options.IncludeArchived, options.CompanyID,
		options.CompanyID, options.Stage, options.Stage, options.Kind, options.Kind,
		cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return OpportunityPage{}, err
	}
	defer rows.Close()
	page := OpportunityPage{Items: make([]Opportunity, 0, limit)}
	for rows.Next() {
		record, err := scanOpportunity(rows)
		if err != nil {
			return OpportunityPage{}, err
		}
		page.Items = append(page.Items, record)
	}
	if err := rows.Err(); err != nil {
		return OpportunityPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeRecordCursor(recordCursor{Version: 1, CreatedAt: last.CreatedAt,
			ID: last.ID, Scope: scope})
	}
	return page, nil
}

// LikelyDuplicateOpportunities warns for an exact source URL or a same-company
// title match. Later source ingestion can add stronger canonical identities.
func (s *Store) LikelyDuplicateOpportunities(ctx context.Context, input OpportunityInput, excludeID string) ([]OpportunityDuplicate, error) {
	input, err := validateOpportunity(input)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+opportunityColumns+opportunityFrom+` WHERE
  o.id<>? AND o.archived_at IS NULL AND
  ((?<>'' AND lower(o.source_url)=lower(?)) OR
   (o.company_id=? AND lower(o.title)=lower(?)))
  ORDER BY o.created_at,o.id LIMIT 20`, excludeID, input.SourceURL, input.SourceURL,
		input.CompanyID, input.Title)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var duplicates []OpportunityDuplicate
	for rows.Next() {
		record, err := scanOpportunity(rows)
		if err != nil {
			return nil, err
		}
		reason := "same_company_title"
		if input.SourceURL != "" && strings.EqualFold(input.SourceURL, record.SourceURL) {
			reason = "same_source_url"
		}
		duplicates = append(duplicates, OpportunityDuplicate{Opportunity: record, Reason: reason})
	}
	return duplicates, rows.Err()
}
