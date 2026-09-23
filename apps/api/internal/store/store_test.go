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
	if initial.Version != 1 || initial.TargetHours != 32 || initial.MinMonthlyBaseCents != 450000 ||
		initial.SalaryCurrency != "EUR" || !initial.RequireBackendPlatform || !initial.ExcludeFrontendDuties ||
		!initial.ExcludePHPFocused || initial.PreferredLocation != "Amsterdam" || initial.Timezone != "Europe/Amsterdam" {
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
	next.TargetHours = 30
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
	if current.Version != 2 || current.TargetHours != 30 || old.TargetHours != 32 {
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

func TestUpgradeFromVersionOneAndMigrationRollback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "jobseek.sqlite")
	db, err := sql.Open("sqlite", "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations
  (version INTEGER PRIMARY KEY, name TEXT NOT NULL, sha256 TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, db, migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO companies (id,name,created_at,updated_at)
  VALUES ('company-1','Older record','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	failed := migration{version: 2, name: "bad_fixture.sql", sql: "CREATE TABLE partial_table (id INTEGER); INSERT INTO no_such_table VALUES (1)", digest: "fixture"}
	if err := applyMigration(ctx, db, failed); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='partial_table'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left a partial table")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("upgraded versions=%d", count)
	}
	var name string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM companies WHERE id='company-1'").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Older record" {
		t.Fatalf("upgrade lost record: %q", name)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='index' AND name='opportunities_source_url_idx'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("version-two index missing")
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
