// Package musewire composes the Muse discovery slice: session supervision,
// public-only tools, Jev classification and durable run records. It is the
// M-owned integration layer; lanes own the pieces it wires.
package musewire

import (
	"context"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// StoreCursors persists supervisor crash cursors in the private database.
type StoreCursors struct {
	DB *store.Store
}

// SaveCursor implements musecode.CursorStore.
func (c StoreCursors) SaveCursor(ctx context.Context, cursor musecode.Cursor) error {
	return c.DB.SaveMuseCursor(ctx, store.MuseCursor{
		RunRef: cursor.RunRef, Tier: string(cursor.Tier),
		LastSavedReceipt: cursor.LastSavedReceipt, SavedCount: cursor.SavedCount,
		SavedRefs: cursor.SavedRefs, UpdatedAt: cursor.UpdatedAt,
	})
}

// LoadCursor implements musecode.CursorStore.
func (c StoreCursors) LoadCursor(ctx context.Context, runRef string) (musecode.Cursor, error) {
	row, err := c.DB.LoadMuseCursor(ctx, runRef)
	if err != nil {
		return musecode.Cursor{}, err
	}
	return musecode.Cursor{
		RunRef: row.RunRef, Tier: musecode.Tier(row.Tier),
		LastSavedReceipt: row.LastSavedReceipt, SavedCount: row.SavedCount,
		SavedRefs: row.SavedRefs, UpdatedAt: row.UpdatedAt,
	}, nil
}
