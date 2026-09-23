package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

//go:embed default-profile.json
var defaultProfileFS embed.FS

type RoleCriterion struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Kind        string `json:"kind"` // role, responsibility, technology
	Mode        string `json:"mode"` // require, avoid, prefer
}

// DefinitionHash binds evidence to a criterion's complete meaning, including
// its ID and mode. The JSON field order is fixed by this concrete struct.
func (c RoleCriterion) DefinitionHash() string {
	encoded, _ := json.Marshal(c)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

type Preferences struct {
	Version               int64
	PreferredLocation     string
	AllowRemote           bool
	AllowHybrid           bool
	TargetHoursHundredths int64
	MinMonthlyBaseCents   int64
	SalaryCurrency        string
	RoleCriteria          []RoleCriterion
	Timezone              string
	CreatedAt             string
	Actor                 Actor
}

func DefaultPreferences() Preferences {
	data, err := defaultProfileFS.ReadFile("default-profile.json")
	if err != nil {
		panic(err)
	}
	var p Preferences
	if err := json.Unmarshal(data, &p); err != nil {
		panic(err)
	}
	return p
}

func (p *Preferences) validate() error {
	if p.TargetHoursHundredths < 100 ||
		p.TargetHoursHundredths > 16800 || p.MinMonthlyBaseCents < 0 ||
		!currencyCode(p.SalaryCurrency) || strings.TrimSpace(p.Timezone) == "" {
		return fmt.Errorf("%w: invalid preferences", ErrInvalid)
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return fmt.Errorf("%w: invalid preference timezone: %v", ErrInvalid, err)
	}
	if len(p.RoleCriteria) > 32 {
		return fmt.Errorf("%w: too many role criteria", ErrInvalid)
	}
	seen := make(map[string]bool, len(p.RoleCriteria))
	for _, criterion := range p.RoleCriteria {
		if !validRoleCriterion(criterion) || seen[criterion.ID] {
			return fmt.Errorf("%w: invalid or duplicate role criterion", ErrInvalid)
		}
		seen[criterion.ID] = true
	}
	return nil
}

func validRoleCriterion(c RoleCriterion) bool {
	if len(c.ID) < 1 || len(c.ID) > 80 || len(c.Label) < 1 || len(c.Label) > 100 ||
		len(c.Description) > 1000 || strings.TrimSpace(c.Label) != c.Label ||
		c.Kind != "role" && c.Kind != "responsibility" && c.Kind != "technology" ||
		c.Mode != "require" && c.Mode != "avoid" && c.Mode != "prefer" {
		return false
	}
	for _, char := range c.ID {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func seedPreferences(ctx context.Context, tx *sql.Tx) error {
	p := DefaultPreferences()
	if err := p.validate(); err != nil {
		return err
	}
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
	criteria, err := json.Marshal(p.RoleCriteria)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO preferences_versions
  (version,preferred_location,allow_remote,allow_hybrid,target_hours_hundredths,
   min_monthly_base_cents,salary_currency,role_criteria_json,timezone,created_at,actor_kind,actor_id)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, p.Version, p.PreferredLocation, p.AllowRemote, p.AllowHybrid,
		p.TargetHoursHundredths, p.MinMonthlyBaseCents, p.SalaryCurrency,
		string(criteria), p.Timezone, p.CreatedAt, p.Actor.Kind, p.Actor.ID)
	return err
}

const preferenceColumns = `p.version, p.preferred_location, p.allow_remote, p.allow_hybrid,
 p.target_hours_hundredths, p.min_monthly_base_cents, p.salary_currency,
 p.role_criteria_json,p.timezone,p.created_at,p.actor_kind,p.actor_id`

type rowScanner interface{ Scan(...any) error }

func scanPreferences(row rowScanner) (Preferences, error) {
	var p Preferences
	var criteriaJSON string
	err := row.Scan(&p.Version, &p.PreferredLocation, &p.AllowRemote, &p.AllowHybrid,
		&p.TargetHoursHundredths, &p.MinMonthlyBaseCents, &p.SalaryCurrency,
		&criteriaJSON, &p.Timezone, &p.CreatedAt, &p.Actor.Kind, &p.Actor.ID)
	if err == nil {
		err = json.Unmarshal([]byte(criteriaJSON), &p.RoleCriteria)
	}
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
		// Reserve the SQLite writer before reading the version. Competing Store
		// handles then serialize and the stale caller receives ErrConflict.
		if _, err := tx.ExecContext(ctx, `UPDATE preferences_current SET version=version WHERE singleton=1`); err != nil {
			return Change{}, err
		}
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
