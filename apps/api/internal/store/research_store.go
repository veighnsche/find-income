package store

import (
	"context"
	"database/sql"
	"time"

	"fmt"
)

// ResearchDB is the query surface for the T07 research store helpers. Both
// *sql.Tx and *sql.Conn satisfy it, so helpers compose inside larger
// transactions (including D-owned save transactions) as well as standalone
// ResearchWrite calls.
type ResearchDB interface {
	Reader
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// ResearchWrite runs fn inside a BEGIN IMMEDIATE transaction: writers take
// the write lock up front (T03 §4) and fn must do no network or long-running
// work. Do not call Store.Read/WriteAudited inside fn; use the passed handle.
func (s *Store) ResearchWrite(ctx context.Context, fn func(ResearchDB) error) (err error) {
	if fn == nil {
		return fmt.Errorf("%w: write callback required", ErrInvalid)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	if err = fn(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func validEnum(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

func formatResearchTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseResearchTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(recordTimeLayout, s)
}
