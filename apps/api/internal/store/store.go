// Package store owns the private SQLite connection, forward migrations and
// transaction boundaries. HTTP authentication must supply Actor values; this
// package does not establish their identity.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var (
	ErrInvalid  = errors.New("invalid storage input")
	ErrConflict = errors.New("stale revision")
	ErrNotFound = errors.New("record not found")
)

type Store struct{ db *sql.DB }

// Open requires an explicit absolute private data directory. Callers must keep
// it outside source, build caches and container images. Files are inaccessible
// to other local users through the directory's 0700 permissions.
func Open(ctx context.Context, privateDataDir string) (_ *Store, err error) {
	if !filepath.IsAbs(privateDataDir) || privateDataDir == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: private data directory must be an absolute non-root path", ErrInvalid)
	}
	dir := filepath.Clean(privateDataDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create private data directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("protect private data directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%w: private data directory must be a directory with mode 0700", ErrInvalid)
	}
	path := filepath.Join(dir, "jobseek.sqlite")
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_foreign_keys", "on")
	q.Set("_journal_mode", "WAL")
	q.Set("_busy_timeout", "5000")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	if err = db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect SQLite: %w", err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, fmt.Errorf("protect SQLite file: %w", err)
	}
	var foreignKeys int
	var mode string
	if err = db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return nil, err
	}
	if err = db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return nil, err
	}
	if foreignKeys != 1 || !strings.EqualFold(mode, "wal") {
		return nil, fmt.Errorf("SQLite connection settings failed: foreign_keys=%d journal_mode=%s", foreignKeys, mode)
	}
	if err = migrate(ctx, db); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Read exposes only query methods to repository services. Use WriteAudited
// for mutations so the domain write and attribution commit together.
func (s *Store) Read(ctx context.Context, fn func(Reader) error) error {
	if fn == nil {
		return fmt.Errorf("%w: read callback required", ErrInvalid)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(conn)
}

type Actor struct {
	Kind string // system, administrator, or named agent; verified by the auth layer
	ID   string
}

type Change struct {
	Operation      string
	EntityKind     string
	EntityID       string
	RevisionBefore *int64
	RevisionAfter  *int64
}

// WriteAudited commits the domain write and one attributable audit record in
// the same transaction. Callers must derive Actor from authentication, never
// from a request body. The callback must not do network or long-running work.
func (s *Store) WriteAudited(ctx context.Context, actor Actor, fn func(*sql.Tx) (Change, error)) (auditID string, err error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" || fn == nil {
		return "", fmt.Errorf("%w: actor and mutation required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	change, err := fn(tx)
	if err != nil {
		return "", err
	}
	if change.Operation == "" || change.EntityKind == "" || change.EntityID == "" {
		return "", fmt.Errorf("%w: audit change required", ErrInvalid)
	}
	auditID, err = randomID()
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id, actor_kind, actor_id, operation, entity_kind, entity_id, revision_before, revision_after, occurred_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, auditID, actor.Kind, actor.ID, change.Operation,
		change.EntityKind, change.EntityID, change.RevisionBefore, change.RevisionAfter, utcNow())
	if err != nil {
		return "", fmt.Errorf("write audit change: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return auditID, nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func utcNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

type migration struct {
	version           int
	name, sql, digest string
}

func embeddedMigrations() ([]migration, error) {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]migration, 0, len(names))
	for _, name := range names {
		basename := filepath.Base(name)
		parts := strings.SplitN(basename, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("bad migration name %q", name)
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version != len(out)+1 {
			return nil, fmt.Errorf("migration sequence error at %q", name)
		}
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(body)
		out = append(out, migration{version, basename, string(body), hex.EncodeToString(digest[:])})
	}
	if len(out) == 0 {
		return nil, errors.New("no embedded migrations")
	}
	return out, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	migrations, err := embeddedMigrations()
	if err != nil {
		return err
	}
	// This bootstrap transaction is also safe to roll back on a new database.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY, name TEXT NOT NULL, sha256 TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("bootstrap migrations: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return err
	}

	rows, err := db.QueryContext(ctx, "SELECT version, name, sha256 FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	applied := map[int]migration{}
	for rows.Next() {
		var m migration
		if err := rows.Scan(&m.version, &m.name, &m.digest); err != nil {
			rows.Close()
			return err
		}
		applied[m.version] = m
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for version, old := range applied {
		if version < 1 || version > len(migrations) {
			return fmt.Errorf("database schema %d is newer than this binary", version)
		}
		current := migrations[version-1]
		if old.name != current.name || old.digest != current.digest {
			return fmt.Errorf("migration %d has changed since application", version)
		}
	}
	for _, m := range migrations {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if m.version > 1 {
			if _, ok := applied[m.version-1]; !ok {
				return fmt.Errorf("missing prior migration %d", m.version-1)
			}
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
		applied[m.version] = m
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, m.sql); err == nil && m.version == 1 {
		err = seedPreferences(ctx, tx)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, sha256, applied_at) VALUES (?, ?, ?, ?)", m.version, m.name, m.digest, utcNow())
	}
	if err != nil {
		return fmt.Errorf("apply migration %s: %w", m.name, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", m.name, err)
	}
	return nil
}
