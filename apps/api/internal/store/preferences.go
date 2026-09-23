package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type Preferences struct {
	Version                int64
	PreferredLocation      string
	AllowRemote            bool
	AllowHybrid            bool
	TargetHours            int64
	MinMonthlyBaseCents    int64
	SalaryCurrency         string
	RequireBackendPlatform bool
	ExcludeFrontendDuties  bool
	ExcludePHPFocused      bool
	Timezone               string
	CreatedAt              string
	Actor                  Actor
}

func DefaultPreferences() Preferences {
	return Preferences{
		PreferredLocation: "Amsterdam", AllowRemote: true, AllowHybrid: true,
		TargetHours: 32, MinMonthlyBaseCents: 450000, SalaryCurrency: "EUR",
		RequireBackendPlatform: true, ExcludeFrontendDuties: true,
		ExcludePHPFocused: true, Timezone: "Europe/Amsterdam",
	}
}

func (p Preferences) validate() error {
	if strings.TrimSpace(p.PreferredLocation) == "" || p.TargetHours < 1 || p.TargetHours > 168 ||
		p.MinMonthlyBaseCents < 0 || p.SalaryCurrency != "EUR" || strings.TrimSpace(p.Timezone) == "" {
		return fmt.Errorf("%w: invalid preferences", ErrInvalid)
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return fmt.Errorf("%w: invalid preference timezone: %v", ErrInvalid, err)
	}
	return nil
}

func seedPreferences(ctx context.Context, tx *sql.Tx) error {
	p := DefaultPreferences()
	p.Version = 1
	p.CreatedAt = utcNow()
	p.Actor = Actor{Kind: "system", ID: "initial-preferences"}
	if err := insertPreferences(ctx, tx, p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO preferences_current (singleton, version) VALUES (1, 1)")
	return err
}

func insertPreferences(ctx context.Context, tx *sql.Tx, p Preferences) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO preferences_versions
  (version, preferred_location, allow_remote, allow_hybrid, target_hours,
   min_monthly_base_cents, salary_currency, require_backend_platform,
   exclude_frontend_duties, exclude_php_focused, timezone, created_at, actor_kind, actor_id)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Version, p.PreferredLocation, p.AllowRemote, p.AllowHybrid, p.TargetHours,
		p.MinMonthlyBaseCents, p.SalaryCurrency, p.RequireBackendPlatform,
		p.ExcludeFrontendDuties, p.ExcludePHPFocused, p.Timezone, p.CreatedAt,
		p.Actor.Kind, p.Actor.ID)
	return err
}

const preferenceColumns = `p.version, p.preferred_location, p.allow_remote, p.allow_hybrid,
 p.target_hours, p.min_monthly_base_cents, p.salary_currency,
 p.require_backend_platform, p.exclude_frontend_duties, p.exclude_php_focused,
 p.timezone, p.created_at, p.actor_kind, p.actor_id`

type rowScanner interface{ Scan(...any) error }

func scanPreferences(row rowScanner) (Preferences, error) {
	var p Preferences
	err := row.Scan(&p.Version, &p.PreferredLocation, &p.AllowRemote, &p.AllowHybrid,
		&p.TargetHours, &p.MinMonthlyBaseCents, &p.SalaryCurrency, &p.RequireBackendPlatform,
		&p.ExcludeFrontendDuties, &p.ExcludePHPFocused, &p.Timezone, &p.CreatedAt,
		&p.Actor.Kind, &p.Actor.ID)
	return p, err
}

func (s *Store) CurrentPreferences(ctx context.Context) (Preferences, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+preferenceColumns+`
  FROM preferences_versions p JOIN preferences_current c ON c.version=p.version WHERE c.singleton=1`)
	p, err := scanPreferences(row)
	if err == sql.ErrNoRows {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) PreferenceVersion(ctx context.Context, version int64) (Preferences, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+preferenceColumns+` FROM preferences_versions p WHERE p.version=?`, version)
	p, err := scanPreferences(row)
	if err == sql.ErrNoRows {
		return p, ErrNotFound
	}
	return p, err
}

// UpdatePreferences retains the old policy and atomically points current at
// the new version. The auth layer must derive actor from the session.
func (s *Store) UpdatePreferences(ctx context.Context, expectedVersion int64, next Preferences, actor Actor) (Preferences, string, error) {
	if err := next.validate(); err != nil {
		return Preferences{}, "", err
	}
	if expectedVersion < 1 {
		return Preferences{}, "", fmt.Errorf("%w: expected version required", ErrInvalid)
	}
	next.CreatedAt = utcNow()
	next.Actor = actor
	next.Version = expectedVersion + 1
	before, after := expectedVersion, next.Version
	auditID, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		var current int64
		if err := tx.QueryRowContext(ctx, "SELECT version FROM preferences_current WHERE singleton=1").Scan(&current); err != nil {
			return Change{}, err
		}
		if current != expectedVersion {
			return Change{}, ErrConflict
		}
		if err := insertPreferences(ctx, tx, next); err != nil {
			return Change{}, err
		}
		result, err := tx.ExecContext(ctx, "UPDATE preferences_current SET version=? WHERE singleton=1 AND version=?", next.Version, expectedVersion)
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
		return Change{Operation: "preferences.update", EntityKind: "preferences", EntityID: "current", RevisionBefore: &before, RevisionAfter: &after}, nil
	})
	if err != nil {
		return Preferences{}, "", err
	}
	return next, auditID, nil
}
