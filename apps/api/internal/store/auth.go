package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
)

var ErrAlreadyConfigured = errors.New("administrator already configured")

func (s *Store) AdministratorHash(ctx context.Context) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM administrator WHERE singleton=1").Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// SetAdministratorHash is deliberately one-time. Password changes need a
// separate authenticated or local recovery workflow with session revocation.
func (s *Store) SetAdministratorHash(ctx context.Context, hash string) error {
	if hash == "" {
		return fmt.Errorf("%w: password hash required", ErrInvalid)
	}
	_, err := s.WriteAudited(ctx, Actor{Kind: "system", ID: "local-admin-setup"}, func(tx *sql.Tx) (Change, error) {
		now := utcNow()
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO administrator
   (singleton,password_hash,credential_version,created_at,updated_at) VALUES (1,?,1,?,?)`, hash, now, now)
		if err != nil {
			return Change{}, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return Change{}, err
		}
		if n != 1 {
			return Change{}, ErrAlreadyConfigured
		}
		revision := int64(1)
		return Change{Operation: "admin.setup", EntityKind: "administrator", EntityID: "owner", RevisionAfter: &revision}, nil
	})
	return err
}

type SessionRecord struct {
	TokenHash string
	CSRFHash  string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

func (s *Store) CreateSession(ctx context.Context, tokenHash, csrfHash string, expiresAt time.Time) error {
	if tokenHash == "" || csrfHash == "" || !expiresAt.After(time.Now()) {
		return fmt.Errorf("%w: invalid session", ErrInvalid)
	}
	_, err := s.WriteAudited(ctx, Actor{Kind: "administrator", ID: "owner"}, func(tx *sql.Tx) (Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO auth_sessions
   (token_hash,csrf_hash,created_at,expires_at) VALUES (?,?,?,?)`, tokenHash, csrfHash, utcNow(), expiresAt.UTC().Format(time.RFC3339Nano))
		return Change{Operation: "auth.login", EntityKind: "session", EntityID: tokenHash}, err
	})
	return err
}

func (s *Store) SessionByHash(ctx context.Context, tokenHash string) (SessionRecord, error) {
	var record SessionRecord
	var expiry string
	var revoked sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT token_hash,csrf_hash,expires_at,revoked_at FROM auth_sessions WHERE token_hash=?`, tokenHash).
		Scan(&record.TokenHash, &record.CSRFHash, &expiry, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return record, err
	}
	if revoked.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, revoked.String)
		if parseErr != nil {
			return record, parseErr
		}
		record.RevokedAt = &value
	}
	return record, nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.WriteAudited(ctx, Actor{Kind: "administrator", ID: "owner"}, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, "UPDATE auth_sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL", utcNow(), tokenHash)
		if err != nil {
			return Change{}, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return Change{}, err
		}
		if n != 1 {
			return Change{}, ErrNotFound
		}
		return Change{Operation: "auth.logout", EntityKind: "session", EntityID: tokenHash}, nil
	})
	return err
}

type AgentCredentialRecord struct {
	ID         string
	Name       string
	TokenHash  string
	ScopesJSON string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

func (s *Store) CreateAgentCredential(ctx context.Context, name, tokenHash, scopesJSON string, expiresAt time.Time) (AgentCredentialRecord, error) {
	if name == "" || tokenHash == "" || scopesJSON == "" || !expiresAt.After(time.Now()) {
		return AgentCredentialRecord{}, fmt.Errorf("%w: invalid agent credential", ErrInvalid)
	}
	id, err := randomID()
	if err != nil {
		return AgentCredentialRecord{}, err
	}
	created := time.Now().UTC()
	record := AgentCredentialRecord{ID: id, Name: name, TokenHash: tokenHash, ScopesJSON: scopesJSON, CreatedAt: created, ExpiresAt: expiresAt.UTC()}
	_, err = s.WriteAudited(ctx, Actor{Kind: "administrator", ID: "owner"}, func(tx *sql.Tx) (Change, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_credentials
   (id,name,token_hash,scopes_json,created_at,expires_at,created_by_admin)
   VALUES (?,?,?,?,?,?,1)`, id, name, tokenHash, scopesJSON, created.Format(time.RFC3339Nano), expiresAt.UTC().Format(time.RFC3339Nano))
		return Change{Operation: "agent.create", EntityKind: "agent_credential", EntityID: id}, err
	})
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19 {
		return AgentCredentialRecord{}, ErrConflict
	}
	return record, err
}

func (s *Store) AgentCredentialByHash(ctx context.Context, tokenHash string) (AgentCredentialRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,name,token_hash,scopes_json,created_at,expires_at,revoked_at
  FROM agent_credentials WHERE token_hash=?`, tokenHash)
	record, err := scanAgentCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	return record, err
}

func (s *Store) ListAgentCredentials(ctx context.Context) ([]AgentCredentialRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,token_hash,scopes_json,created_at,expires_at,revoked_at
  FROM agent_credentials ORDER BY created_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]AgentCredentialRecord, 0)
	for rows.Next() {
		record, err := scanAgentCredential(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func scanAgentCredential(row rowScanner) (AgentCredentialRecord, error) {
	var record AgentCredentialRecord
	var created, expiry string
	var revoked sql.NullString
	err := row.Scan(&record.ID, &record.Name, &record.TokenHash, &record.ScopesJSON, &created, &expiry, &revoked)
	if err != nil {
		return record, err
	}
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return record, err
	}
	record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return record, err
	}
	if revoked.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, revoked.String)
		if parseErr != nil {
			return record, parseErr
		}
		record.RevokedAt = &value
	}
	return record, nil
}

func (s *Store) RevokeAgentCredential(ctx context.Context, id string) error {
	_, err := s.WriteAudited(ctx, Actor{Kind: "administrator", ID: "owner"}, func(tx *sql.Tx) (Change, error) {
		result, err := tx.ExecContext(ctx, "UPDATE agent_credentials SET revoked_at=? WHERE id=? AND revoked_at IS NULL", utcNow(), id)
		if err != nil {
			return Change{}, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return Change{}, err
		}
		if n != 1 {
			return Change{}, ErrNotFound
		}
		return Change{Operation: "agent.revoke", EntityKind: "agent_credential", EntityID: id}, nil
	})
	return err
}
