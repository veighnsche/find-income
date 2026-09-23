package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ActionDue struct {
	Date     string // YYYY-MM-DD calendar date, without a timezone or instant
	At       string // RFC3339 instant with explicit offset, normalized to UTC on write
	Timezone string // IANA display timezone, required only for At
}

type ActionInput struct {
	OpportunityID string // optional; contact IDs are deferred until T14
	Description   string
	Due           ActionDue
}

type ActionPatch struct {
	ExpectedRevision int64
	Description      *string
	Due              *ActionDue
}

type Action struct {
	ID            string
	OpportunityID string
	Description   string
	DueDate       string
	DueAt         string // normalized UTC instant
	DueTimezone   string
	Status        string // open, completed, cancelled
	CompletedAt   string
	Revision      int64
	CreatedAt     string
	UpdatedAt     string
}

type ActionListOptions struct {
	Cursor        string
	Limit         int
	Status        string // empty means all statuses
	OpportunityID string // empty means all opportunities
}

type ActionDueListOptions struct {
	At               time.Time // required explicit clock instant
	CalendarTimezone string    // required IANA zone for date-only deadlines
	Cursor           string
	Limit            int
}

type ActionPage struct {
	Items      []Action
	NextCursor string
}

const actionColumns = `id,opportunity_id,description,due_date,due_at,due_timezone,
  status,completed_at,revision,created_at,updated_at`

func validActionTimezone(value string) (*time.Location, error) {
	if value == "" || value == "Local" || value != "UTC" && !strings.Contains(value, "/") ||
		len(value) > 100 || strings.ContainsAny(value, " \t\r\n") {
		return nil, fmt.Errorf("%w: valid IANA timezone required", ErrInvalid)
	}
	zone, err := time.LoadLocation(value)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid IANA timezone", ErrInvalid)
	}
	return zone, nil
}

func normalizeActionDue(value ActionDue) (ActionDue, error) {
	value.Date = strings.TrimSpace(value.Date)
	value.At = strings.TrimSpace(value.At)
	value.Timezone = strings.TrimSpace(value.Timezone)
	if value.Date != "" {
		if value.At != "" || value.Timezone != "" || !validCalendarDate(value.Date) {
			return ActionDue{}, fmt.Errorf("%w: date-only deadline must be one valid calendar date", ErrInvalid)
		}
		return value, nil
	}
	if value.At == "" || value.Timezone == "" {
		return ActionDue{}, fmt.Errorf("%w: timed deadline requires instant and timezone", ErrInvalid)
	}
	zone, err := validActionTimezone(value.Timezone)
	if err != nil {
		return ActionDue{}, err
	}
	// Parse RFC3339Nano requires Z or an explicit numeric offset. A bare local
	// wall time, including a repeated autumn 02:30, is never inferred.
	instant, err := time.Parse(time.RFC3339Nano, value.At)
	if err != nil {
		return ActionDue{}, fmt.Errorf("%w: timed deadline requires RFC3339 offset", ErrInvalid)
	}
	_, suppliedOffset := instant.Zone()
	_, zoneOffset := instant.In(zone).Zone()
	if suppliedOffset != zoneOffset {
		return ActionDue{}, fmt.Errorf("%w: timestamp offset disagrees with timezone at that instant", ErrInvalid)
	}
	value.At = instant.UTC().Format(recordTimeLayout)
	return value, nil
}

func actionDueFromRecord(record Action) ActionDue {
	return ActionDue{Date: record.DueDate, At: record.DueAt, Timezone: record.DueTimezone}
}

func validateActionDescription(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2000 {
		return "", fmt.Errorf("%w: bounded action description required", ErrInvalid)
	}
	return value, nil
}

func scanAction(row rowScanner) (Action, error) {
	var action Action
	var opportunityID, dueDate, dueAt, timezone, completed sql.NullString
	err := row.Scan(&action.ID, &opportunityID, &action.Description, &dueDate, &dueAt,
		&timezone, &action.Status, &completed, &action.Revision, &action.CreatedAt, &action.UpdatedAt)
	if err != nil {
		return Action{}, err
	}
	action.OpportunityID = opportunityID.String
	action.DueDate, action.DueAt, action.DueTimezone = dueDate.String, dueAt.String, timezone.String
	action.CompletedAt = completed.String
	return action, nil
}

func (s *Store) Action(ctx context.Context, id string) (Action, error) {
	action, err := scanAction(s.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM actions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, ErrNotFound
	}
	return action, err
}

func (s *Store) CreateAction(ctx context.Context, actor Actor, input ActionInput) (Action, string, error) {
	var err error
	input.Description, err = validateActionDescription(input.Description)
	if err != nil {
		return Action{}, "", err
	}
	input.Due, err = normalizeActionDue(input.Due)
	if err != nil {
		return Action{}, "", err
	}
	if len(input.OpportunityID) > 100 {
		return Action{}, "", fmt.Errorf("%w: invalid opportunity reference", ErrInvalid)
	}
	id, err := randomID()
	if err != nil {
		return Action{}, "", err
	}
	now := recordNow()
	action := Action{ID: id, OpportunityID: input.OpportunityID, Description: input.Description,
		DueDate: input.Due.Date, DueAt: input.Due.At, DueTimezone: input.Due.Timezone,
		Status: "open", Revision: 1, CreatedAt: now, UpdatedAt: now}
	revision := int64(1)
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		var result sql.Result
		var err error
		if input.OpportunityID != "" {
			result, err = tx.ExecContext(ctx, `INSERT INTO actions
  (id,opportunity_id,description,due_date,due_at,due_timezone,status,revision,created_at,updated_at)
  SELECT ?,o.id,?,?,?,?, 'open',1,?,? FROM opportunities o
  WHERE o.id=? AND o.archived_at IS NULL`, id, input.Description,
				optionalText(input.Due.Date), optionalText(input.Due.At), optionalText(input.Due.Timezone),
				now, now, input.OpportunityID)
		} else {
			result, err = tx.ExecContext(ctx, `INSERT INTO actions
  (id,opportunity_id,description,due_date,due_at,due_timezone,status,revision,created_at,updated_at)
  VALUES (?,?,?,?,?,?,'open',1,?,?)`, id, nil, input.Description,
				optionalText(input.Due.Date), optionalText(input.Due.At), optionalText(input.Due.Timezone), now, now)
		}
		if err != nil {
			return Change{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return Change{}, err
		}
		if count != 1 {
			return Change{}, fmt.Errorf("%w: active opportunity required for new action link", ErrInvalid)
		}
		return Change{Operation: "action.create", EntityKind: "action", EntityID: id,
			RevisionAfter: &revision}, nil
	})
	if err != nil {
		return Action{}, "", err
	}
	return action, changeID, nil
}

func (s *Store) PatchAction(ctx context.Context, actor Actor, id string, patch ActionPatch) (Action, string, error) {
	if id == "" || patch.ExpectedRevision < 1 || patch.Description == nil && patch.Due == nil {
		return Action{}, "", fmt.Errorf("%w: action patch and expected revision required", ErrInvalid)
	}
	current, err := s.Action(ctx, id)
	if err != nil {
		return Action{}, "", err
	}
	if current.Revision != patch.ExpectedRevision || current.Status != "open" {
		return Action{}, "", ErrConflict
	}
	updated := current
	if patch.Description != nil {
		updated.Description, err = validateActionDescription(*patch.Description)
		if err != nil {
			return Action{}, "", err
		}
	}
	if patch.Due != nil {
		var due ActionDue
		due, err = normalizeActionDue(*patch.Due)
		if err != nil {
			return Action{}, "", err
		}
		updated.DueDate, updated.DueAt, updated.DueTimezone = due.Date, due.At, due.Timezone
	}
	updated.Revision++
	updated.UpdatedAt = recordNow()
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, `UPDATE actions SET description=?,due_date=?,due_at=?,due_timezone=?,
  revision=?,updated_at=? WHERE id=? AND revision=? AND status='open'`,
			updated.Description, optionalText(updated.DueDate), optionalText(updated.DueAt),
			optionalText(updated.DueTimezone), updated.Revision, updated.UpdatedAt, id, current.Revision)
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
		return Change{Operation: "action.patch", EntityKind: "action", EntityID: id,
			RevisionBefore: &current.Revision, RevisionAfter: &updated.Revision}, nil
	})
	if err != nil {
		return Action{}, "", err
	}
	return updated, changeID, nil
}

func (s *Store) RescheduleAction(ctx context.Context, actor Actor, id string, expectedRevision int64, due ActionDue) (Action, string, error) {
	return s.PatchAction(ctx, actor, id, ActionPatch{ExpectedRevision: expectedRevision, Due: &due})
}

func (s *Store) transitionAction(ctx context.Context, actor Actor, id string, expectedRevision int64, target string) (Action, string, error) {
	if id == "" || expectedRevision < 1 || target != "completed" && target != "cancelled" {
		return Action{}, "", fmt.Errorf("%w: action transition and expected revision required", ErrInvalid)
	}
	current, err := s.Action(ctx, id)
	if err != nil {
		return Action{}, "", err
	}
	if current.Revision != expectedRevision || current.Status != "open" {
		return Action{}, "", ErrConflict
	}
	updated := current
	updated.Status = target
	updated.Revision++
	updated.UpdatedAt = recordNow()
	if target == "completed" {
		updated.CompletedAt = updated.UpdatedAt
	}
	changeID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, `UPDATE actions SET status=?,completed_at=?,revision=?,updated_at=?
  WHERE id=? AND revision=? AND status='open'`, target, optionalText(updated.CompletedAt),
			updated.Revision, updated.UpdatedAt, id, current.Revision)
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
		return Change{Operation: "action." + target, EntityKind: "action", EntityID: id,
			RevisionBefore: &current.Revision, RevisionAfter: &updated.Revision}, nil
	})
	if err != nil {
		return Action{}, "", err
	}
	return updated, changeID, nil
}

func (s *Store) CompleteAction(ctx context.Context, actor Actor, id string, expectedRevision int64) (Action, string, error) {
	return s.transitionAction(ctx, actor, id, expectedRevision, "completed")
}

func (s *Store) CancelAction(ctx context.Context, actor Actor, id string, expectedRevision int64) (Action, string, error) {
	return s.transitionAction(ctx, actor, id, expectedRevision, "cancelled")
}

func listActionRows(rows *sql.Rows, limit int, scope string) (ActionPage, error) {
	defer rows.Close()
	page := ActionPage{Items: make([]Action, 0, limit)}
	for rows.Next() {
		action, err := scanAction(rows)
		if err != nil {
			return ActionPage{}, err
		}
		page.Items = append(page.Items, action)
	}
	if err := rows.Err(); err != nil {
		return ActionPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeRecordCursor(recordCursor{Version: 1, CreatedAt: last.CreatedAt,
			ID: last.ID, Scope: scope})
	}
	return page, nil
}

func (s *Store) ListActions(ctx context.Context, options ActionListOptions) (ActionPage, error) {
	limit, err := boundedListLimit(options.Limit)
	if err != nil {
		return ActionPage{}, err
	}
	if options.Status != "" && options.Status != "open" && options.Status != "completed" && options.Status != "cancelled" ||
		len(options.OpportunityID) > 100 {
		return ActionPage{}, fmt.Errorf("%w: invalid action list filter", ErrInvalid)
	}
	scope := recordListScope("action", options.Status, options.OpportunityID)
	cursor, err := decodeRecordCursor(options.Cursor)
	if err != nil {
		return ActionPage{}, err
	}
	if options.Cursor != "" && cursor.Scope != scope {
		return ActionPage{}, fmt.Errorf("%w: changed action list filter", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+actionColumns+` FROM actions WHERE
  (?='' OR status=?) AND (?='' OR opportunity_id=?) AND
  (created_at>? OR (created_at=? AND id>?)) ORDER BY created_at,id LIMIT ?`,
		options.Status, options.Status, options.OpportunityID, options.OpportunityID,
		cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return ActionPage{}, err
	}
	return listActionRows(rows, limit, scope)
}

func (s *Store) listDueActions(ctx context.Context, options ActionDueListOptions, overdue bool) (ActionPage, error) {
	limit, err := boundedListLimit(options.Limit)
	if err != nil {
		return ActionPage{}, err
	}
	if options.At.IsZero() {
		return ActionPage{}, fmt.Errorf("%w: explicit clock required", ErrInvalid)
	}
	zone, err := validActionTimezone(options.CalendarTimezone)
	if err != nil {
		return ActionPage{}, err
	}
	localDate := options.At.In(zone).Format("2006-01-02")
	instant := options.At.UTC().Format(recordTimeLayout)
	scope := recordListScope("action-due", overdue, instant, options.CalendarTimezone)
	cursor, err := decodeRecordCursor(options.Cursor)
	if err != nil {
		return ActionPage{}, err
	}
	if options.Cursor != "" && cursor.Scope != scope {
		return ActionPage{}, fmt.Errorf("%w: changed due-list clock or timezone", ErrInvalid)
	}
	comparison := "<="
	if overdue {
		comparison = "<"
	}
	query := `SELECT ` + actionColumns + ` FROM actions WHERE status='open' AND
  ((due_date IS NOT NULL AND due_date ` + comparison + ` ?) OR
   (due_at IS NOT NULL AND due_at ` + comparison + ` ?)) AND
  (created_at>? OR (created_at=? AND id>?)) ORDER BY created_at,id LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, localDate, instant,
		cursor.CreatedAt, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return ActionPage{}, err
	}
	return listActionRows(rows, limit, scope)
}

// Date-only work becomes due at the start of the requested local calendar day
// and overdue only on a later local day. Timed work is compared by absolute
// instant. The caller provides both the clock and IANA zone on every page.
func (s *Store) ListDueActions(ctx context.Context, options ActionDueListOptions) (ActionPage, error) {
	return s.listDueActions(ctx, options, false)
}

func (s *Store) ListOverdueActions(ctx context.Context, options ActionDueListOptions) (ActionPage, error) {
	return s.listDueActions(ctx, options, true)
}

// DueDate and DueAt are mutually exclusive in the schema; this helper keeps
// their public replacement shape explicit for callers.
func (a Action) Due() ActionDue { return actionDueFromRecord(a) }
