package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Company struct {
	ID         string
	Name       string
	Website    string
	Notes      string
	ArchivedAt string
	Revision   int64
	CreatedAt  string
	UpdatedAt  string
}

type CompanyInput struct {
	Name    string
	Website string
	Notes   string
}

// Nil patch fields are unchanged. A non-nil pointer to "" clears an optional
// field. ExpectedRevision is mandatory even for a single-user dashboard,
// because browser tabs and agents can edit concurrently.
type CompanyPatch struct {
	ExpectedRevision int64
	Name             *string
	Website          *string
	Notes            *string
}

type CompanyListOptions struct {
	Cursor          string
	Limit           int
	IncludeArchived bool
}

type CompanyPage struct {
	Items      []Company
	NextCursor string
}

type CompanyDuplicate struct {
	Company Company
	Reason  string // same_website or same_name
}

const companyColumns = `id,name,website,notes,archived_at,revision,created_at,updated_at`

// Record timestamps use fixed-width UTC nanoseconds so a (time, ID) keyset
// remains ordered even for records created within the same second.
const recordTimeLayout = "2006-01-02T15:04:05.000000000Z"

func recordNow() string { return time.Now().UTC().Format(recordTimeLayout) }

func validateWebURL(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 || strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("%w: invalid URL", ErrInvalid)
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("%w: URL must be HTTP(S) without credentials", ErrInvalid)
	}
	return nil
}

func validateCompany(input CompanyInput) (CompanyInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Website = strings.TrimSpace(input.Website)
	if input.Name == "" || len(input.Name) > 200 || len(input.Notes) > 10000 {
		return input, fmt.Errorf("%w: company name and bounded notes required", ErrInvalid)
	}
	if err := validateWebURL(input.Website); err != nil {
		return input, err
	}
	return input, nil
}

func scanCompany(row rowScanner) (Company, error) {
	var company Company
	var website, archived sql.NullString
	err := row.Scan(&company.ID, &company.Name, &website, &company.Notes, &archived,
		&company.Revision, &company.CreatedAt, &company.UpdatedAt)
	if err != nil {
		return Company{}, err
	}
	company.Website, company.ArchivedAt = website.String, archived.String
	return company, nil
}

func (s *Store) Company(ctx context.Context, id string) (Company, error) {
	company, err := scanCompany(s.db.QueryRowContext(ctx, `SELECT `+companyColumns+` FROM companies WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Company{}, ErrNotFound
	}
	return company, err
}

func (s *Store) CreateCompany(ctx context.Context, actor Actor, input CompanyInput) (Company, string, error) {
	input, err := validateCompany(input)
	if err != nil {
		return Company{}, "", err
	}
	id, err := randomID()
	if err != nil {
		return Company{}, "", err
	}
	now := recordNow()
	company := Company{ID: id, Name: input.Name, Website: input.Website, Notes: input.Notes,
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	revision := int64(1)
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO companies
  (id,name,website,notes,revision,created_at,updated_at) VALUES (?,?,?,?,1,?,?)`,
			id, input.Name, optionalText(input.Website), input.Notes, now, now)
		return Change{Operation: "company.create", EntityKind: "company", EntityID: id, RevisionAfter: &revision}, err
	})
	if err != nil {
		return Company{}, "", err
	}
	return company, changeID, nil
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) PatchCompany(ctx context.Context, actor Actor, id string, patch CompanyPatch) (Company, string, error) {
	if id == "" || patch.ExpectedRevision < 1 ||
		(patch.Name == nil && patch.Website == nil && patch.Notes == nil) {
		return Company{}, "", fmt.Errorf("%w: company patch and expected revision required", ErrInvalid)
	}
	current, err := s.Company(ctx, id)
	if err != nil {
		return Company{}, "", err
	}
	if current.Revision != patch.ExpectedRevision || current.ArchivedAt != "" {
		return Company{}, "", ErrConflict
	}
	input := CompanyInput{Name: current.Name, Website: current.Website, Notes: current.Notes}
	if patch.Name != nil {
		input.Name = *patch.Name
	}
	if patch.Website != nil {
		input.Website = *patch.Website
	}
	if patch.Notes != nil {
		input.Notes = *patch.Notes
	}
	input, err = validateCompany(input)
	if err != nil {
		return Company{}, "", err
	}
	updated := current
	updated.Name, updated.Website, updated.Notes = input.Name, input.Website, input.Notes
	updated.Revision++
	updated.UpdatedAt = recordNow()
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, `UPDATE companies SET name=?,website=?,notes=?,revision=?,updated_at=?
  WHERE id=? AND revision=? AND archived_at IS NULL`, updated.Name, optionalText(updated.Website),
			updated.Notes, updated.Revision, updated.UpdatedAt, id, current.Revision)
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
		return Change{Operation: "company.patch", EntityKind: "company", EntityID: id,
			RevisionBefore: &current.Revision, RevisionAfter: &updated.Revision}, nil
	})
	if err != nil {
		return Company{}, "", err
	}
	return updated, changeID, nil
}

func (s *Store) ArchiveCompany(ctx context.Context, actor Actor, id string, expectedRevision int64) (Company, string, error) {
	if id == "" || expectedRevision < 1 {
		return Company{}, "", fmt.Errorf("%w: company and expected revision required", ErrInvalid)
	}
	current, err := s.Company(ctx, id)
	if err != nil {
		return Company{}, "", err
	}
	if current.Revision != expectedRevision || current.ArchivedAt != "" {
		return Company{}, "", ErrConflict
	}
	archived := current
	archived.Revision++
	archived.ArchivedAt = recordNow()
	archived.UpdatedAt = archived.ArchivedAt
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, `UPDATE companies SET archived_at=?,revision=?,updated_at=?
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
		return Change{Operation: "company.archive", EntityKind: "company", EntityID: id,
			RevisionBefore: &current.Revision, RevisionAfter: &archived.Revision}, nil
	})
	if err != nil {
		return Company{}, "", err
	}
	return archived, changeID, nil
}

type recordCursor struct {
	Version   int    `json:"v"`
	CreatedAt string `json:"t"`
	ID        string `json:"id"`
	Scope     string `json:"scope"`
}

func encodeRecordCursor(value recordCursor) string {
	b, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeRecordCursor(value string) (recordCursor, error) {
	if value == "" {
		return recordCursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) > 512 {
		return recordCursor{}, fmt.Errorf("%w: invalid list cursor", ErrInvalid)
	}
	var cursor recordCursor
	if err := json.Unmarshal(b, &cursor); err != nil || cursor.Version != 1 ||
		cursor.CreatedAt == "" || cursor.ID == "" || cursor.Scope == "" {
		return recordCursor{}, fmt.Errorf("%w: invalid list cursor", ErrInvalid)
	}
	if _, err := time.Parse(recordTimeLayout, cursor.CreatedAt); err != nil {
		return recordCursor{}, fmt.Errorf("%w: invalid list cursor time", ErrInvalid)
	}
	return cursor, nil
}

// A list cursor is bound to its entity and filters. Changing filters during
// pagination could otherwise silently skip or repeat records.
func recordListScope(kind string, filters ...any) string {
	b, _ := json.Marshal(append([]any{kind}, filters...))
	return string(b)
}

func boundedListLimit(value int) (int, error) {
	if value == 0 {
		return 25, nil
	}
	if value < 1 || value > 100 {
		return 0, fmt.Errorf("%w: list limit must be 1–100", ErrInvalid)
	}
	return value, nil
}

func (s *Store) ListCompanies(ctx context.Context, options CompanyListOptions) (CompanyPage, error) {
	limit, err := boundedListLimit(options.Limit)
	if err != nil {
		return CompanyPage{}, err
	}
	cursor, err := decodeRecordCursor(options.Cursor)
	if err != nil {
		return CompanyPage{}, err
	}
	scope := recordListScope("company", options.IncludeArchived)
	if options.Cursor != "" && cursor.Scope != scope {
		return CompanyPage{}, fmt.Errorf("%w: changed company list filter", ErrInvalid)
	}
	query := `SELECT ` + companyColumns + ` FROM companies WHERE
  (? OR archived_at IS NULL) AND (created_at>? OR (created_at=? AND id>?))
  ORDER BY created_at,id LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, options.IncludeArchived, cursor.CreatedAt,
		cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return CompanyPage{}, err
	}
	defer rows.Close()
	page := CompanyPage{Items: make([]Company, 0, limit)}
	for rows.Next() {
		company, err := scanCompany(rows)
		if err != nil {
			return CompanyPage{}, err
		}
		page.Items = append(page.Items, company)
	}
	if err := rows.Err(); err != nil {
		return CompanyPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeRecordCursor(recordCursor{Version: 1, CreatedAt: last.CreatedAt,
			ID: last.ID, Scope: scope})
	}
	return page, nil
}

// LikelyDuplicateCompanies is advisory only. Exact normalized website or
// case-folded name is a warning, never an automatic merge.
func (s *Store) LikelyDuplicateCompanies(ctx context.Context, input CompanyInput, excludeID string) ([]CompanyDuplicate, error) {
	input, err := validateCompany(input)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+companyColumns+` FROM companies
  WHERE id<>? AND archived_at IS NULL AND
  ((?<>'' AND lower(website)=lower(?)) OR lower(name)=lower(?))
  ORDER BY created_at,id LIMIT 20`, excludeID, input.Website, input.Website, input.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var duplicates []CompanyDuplicate
	for rows.Next() {
		company, err := scanCompany(rows)
		if err != nil {
			return nil, err
		}
		reason := "same_name"
		if input.Website != "" && strings.EqualFold(input.Website, company.Website) {
			reason = "same_website"
		}
		duplicates = append(duplicates, CompanyDuplicate{Company: company, Reason: reason})
	}
	return duplicates, rows.Err()
}
