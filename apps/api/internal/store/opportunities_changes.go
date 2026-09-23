package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// RecordChange is an immutable post-mutation snapshot. RevisionAfter belongs
// to this event, not to a later current record fetched by another request.
// Backfilled pre-005 audit metadata has SnapshotState unavailable_historical.
type RecordChange struct {
	Sequence       int64
	ChangeID       string
	EntityKind     string
	EntityID       string
	Operation      string
	Actor          Actor
	RevisionBefore *int64
	RevisionAfter  *int64
	OccurredAt     string
	SnapshotState  string
	Snapshot       json.RawMessage
}

type RecordChangeOptions struct {
	After      int64  // starting sequence for a new finite batch
	Cursor     string // next-page cursor; mutually exclusive with After
	Limit      int
	EntityKind string // empty, company, or opportunity
}

type RecordChangePage struct {
	Items      []RecordChange
	NextCursor string
	Watermark  int64 // use as After on the next poll after NextCursor is empty
}

type changeCursor struct {
	Version    int    `json:"v"`
	After      int64  `json:"after"`
	Upper      int64  `json:"upper"`
	EntityKind string `json:"kind"`
}

const changeColumns = `sequence,audit_id,entity_kind,entity_id,operation,actor_kind,
  actor_id,revision_before,revision_after,occurred_at,snapshot_state,snapshot_json`

func encodeChangeCursor(value changeCursor) string {
	b, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeChangeCursor(value string) (changeCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) > 256 {
		return changeCursor{}, fmt.Errorf("%w: invalid change cursor", ErrInvalid)
	}
	var cursor changeCursor
	if err := json.Unmarshal(b, &cursor); err != nil || cursor.Version != 1 ||
		cursor.After < 0 || cursor.Upper < cursor.After ||
		(cursor.EntityKind != "" && cursor.EntityKind != "company" && cursor.EntityKind != "opportunity") {
		return changeCursor{}, fmt.Errorf("%w: invalid change cursor", ErrInvalid)
	}
	return cursor, nil
}

func scanRecordChange(row rowScanner) (RecordChange, error) {
	var change RecordChange
	var before, after sql.NullInt64
	var snapshot sql.NullString
	err := row.Scan(&change.Sequence, &change.ChangeID, &change.EntityKind, &change.EntityID,
		&change.Operation, &change.Actor.Kind, &change.Actor.ID, &before, &after,
		&change.OccurredAt, &change.SnapshotState, &snapshot)
	if err != nil {
		return RecordChange{}, err
	}
	if before.Valid {
		change.RevisionBefore = &before.Int64
	}
	if after.Valid {
		change.RevisionAfter = &after.Int64
	}
	if snapshot.Valid {
		change.Snapshot = json.RawMessage(snapshot.String)
	}
	return change, nil
}

func (s *Store) RecordChange(ctx context.Context, changeID string) (RecordChange, error) {
	change, err := scanRecordChange(s.db.QueryRowContext(ctx,
		`SELECT `+changeColumns+` FROM record_changes WHERE audit_id=?`, changeID))
	if errors.Is(err, sql.ErrNoRows) {
		return RecordChange{}, ErrNotFound
	}
	return change, err
}

// ListRecordChanges freezes an upper sequence on page one. Mutations after
// that watermark remain for the next poll, even if they change the same record
// while this batch is being paged. The returned snapshot is historical at the
// event revision; the current record may already have a higher revision.
func (s *Store) ListRecordChanges(ctx context.Context, options RecordChangeOptions) (RecordChangePage, error) {
	limit, err := boundedListLimit(options.Limit)
	if err != nil {
		return RecordChangePage{}, err
	}
	if options.After < 0 || (options.EntityKind != "" && options.EntityKind != "company" && options.EntityKind != "opportunity") ||
		(options.Cursor != "" && options.After != 0) {
		return RecordChangePage{}, fmt.Errorf("%w: invalid change listing options", ErrInvalid)
	}
	cursor := changeCursor{Version: 1, After: options.After, EntityKind: options.EntityKind}
	if options.Cursor != "" {
		cursor, err = decodeChangeCursor(options.Cursor)
		if err != nil {
			return RecordChangePage{}, err
		}
		if cursor.EntityKind != options.EntityKind {
			return RecordChangePage{}, fmt.Errorf("%w: changed entity filter", ErrInvalid)
		}
	} else {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM record_changes`).Scan(&cursor.Upper); err != nil {
			return RecordChangePage{}, err
		}
		if cursor.Upper < cursor.After {
			cursor.Upper = cursor.After
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+changeColumns+` FROM record_changes
  WHERE sequence>? AND sequence<=? AND (?='' OR entity_kind=?)
  ORDER BY sequence LIMIT ?`, cursor.After, cursor.Upper, cursor.EntityKind,
		cursor.EntityKind, limit+1)
	if err != nil {
		return RecordChangePage{}, err
	}
	defer rows.Close()
	page := RecordChangePage{Items: make([]RecordChange, 0, limit), Watermark: cursor.Upper}
	for rows.Next() {
		change, err := scanRecordChange(rows)
		if err != nil {
			return RecordChangePage{}, err
		}
		page.Items = append(page.Items, change)
	}
	if err := rows.Err(); err != nil {
		return RecordChangePage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		cursor.After = page.Items[len(page.Items)-1].Sequence
		page.NextCursor = encodeChangeCursor(cursor)
	}
	return page, nil
}
