package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// MuseCursor is the durable crash-recovery record for one local CLI session
// run. The supervisor writes it on transport disconnect; resume reconciles
// it before continuing and never blindly replays.
type MuseCursor struct {
	RunRef           string
	Tier             string
	LastSavedReceipt string
	SavedCount       int
	SavedRefs        []string
	UpdatedAt        time.Time
}

// SaveMuseCursor upserts one cursor row.
func (s *Store) SaveMuseCursor(ctx context.Context, cursor MuseCursor) error {
	if cursor.RunRef == "" || (cursor.Tier != "contributor" && cursor.Tier != "standard") || cursor.SavedCount < 0 {
		return errors.New("musecursors: run ref, tier and non-negative count required")
	}
	refs, err := json.Marshal(cursor.SavedRefs)
	if err != nil {
		return err
	}
	if cursor.SavedRefs == nil {
		refs = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO muse_cursors
		(run_ref,tier,last_saved_receipt,saved_count,saved_refs_json,updated_at)
		VALUES (?,?,?,?,?,?) ON CONFLICT (run_ref) DO UPDATE SET
		tier=excluded.tier,last_saved_receipt=excluded.last_saved_receipt,
		saved_count=excluded.saved_count,saved_refs_json=excluded.saved_refs_json,
		updated_at=excluded.updated_at`,
		cursor.RunRef, cursor.Tier, cursor.LastSavedReceipt, cursor.SavedCount,
		string(refs), cursor.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// LoadMuseCursor reads one cursor row. Unknown runs return ErrNotFound.
func (s *Store) LoadMuseCursor(ctx context.Context, runRef string) (MuseCursor, error) {
	var cursor MuseCursor
	var refsJSON, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT run_ref,tier,last_saved_receipt,
		saved_count,saved_refs_json,updated_at FROM muse_cursors WHERE run_ref=?`, runRef).
		Scan(&cursor.RunRef, &cursor.Tier, &cursor.LastSavedReceipt,
			&cursor.SavedCount, &refsJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MuseCursor{}, ErrNotFound
	}
	if err != nil {
		return MuseCursor{}, err
	}
	if err := json.Unmarshal([]byte(refsJSON), &cursor.SavedRefs); err != nil {
		return MuseCursor{}, err
	}
	cursor.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	return cursor, err
}
