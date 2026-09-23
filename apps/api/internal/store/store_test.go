package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFreshPreferencesAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Version != 1 || initial.TargetHoursHundredths != 3200 || initial.MinMonthlyBaseCents != 450000 ||
		initial.SalaryCurrency != "EUR" || len(initial.RoleCriteria) != 3 ||
		initial.PreferredLocation != "Amsterdam" || initial.Timezone != "Europe/Amsterdam" {
		t.Fatalf("wrong default preferences: %+v", initial)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM opportunities").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fresh database has %d fictional opportunities", count)
	}
	var foreignKeys int
	var mode string
	var timeout int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || mode != "wal" || timeout != 5000 {
		t.Fatalf("SQLite settings: fk=%d mode=%s timeout=%d", foreignKeys, mode, timeout)
	}

	next := initial
	next.TargetHoursHundredths = 3000
	updated, auditID, err := s.UpdatePreferences(ctx, 1, next, Actor{Kind: "administrator", ID: "test-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || auditID == "" {
		t.Fatalf("update version/audit: %+v %q", updated, auditID)
	}
	if _, _, err := s.UpdatePreferences(ctx, 1, next, Actor{Kind: "administrator", ID: "test-owner"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale preference update: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.PreferenceVersion(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 2 || current.TargetHoursHundredths != 3000 || old.TargetHoursHundredths != 3200 {
		t.Fatalf("version history after restart: current=%+v old=%+v", current, old)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_changes WHERE id=? AND actor_id=?", auditID, "test-owner").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit changes after restart = %d", count)
	}
	if info, err := os.Stat(filepath.Join(dir, "jobseek.sqlite")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("database file permissions: %v %v", info, err)
	}
}

func TestForeignKeyFailureRollsBackMutationAndAudit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.WriteAudited(ctx, Actor{Kind: "administrator", ID: "test-owner"}, func(tx *sql.Tx) (Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO companies (id,name,created_at,updated_at)
   VALUES ('company-1','Example','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
		if err != nil {
			return Change{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO opportunities
   (id,company_id,title,kind,stage,created_at,updated_at)
   VALUES ('role-1','missing-company','Engineer','employment','new','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
		return Change{Operation: "test", EntityKind: "company", EntityID: "company-1"}, err
	})
	if err == nil {
		t.Fatal("foreign-key violation accepted")
	}
	for _, table := range []string{"companies", "opportunities", "audit_changes"} {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s has %d partial records", table, count)
		}
	}
}

func TestOpenRequiresPrivateAbsoluteDir(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, "relative/data"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative path: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("data directory mode = %o", info.Mode().Perm())
	}
}
