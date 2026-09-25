// Versioned reason catalog (lane C, C2): one Codex-authored rubric plus
// positive, negative and missing-information reason choices per owner-brief
// version. Each row preserves the exact owner requirements (role-criteria
// snapshot) it was authored against, so a later brief edit cannot silently
// rebind an older catalog. Rows are immutable: re-authoring identical
// content replays the stored row, differing content conflicts, and listing
// or explanation reads never regenerate anything.
//
// Change-my-search input is NOT a transcription form here. Brief changes
// flow through the existing steer-shaped Codex-owned records (rounds steer
// into a new preferences version); authoring only records the originating
// steer message as provenance on the new catalog row.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReasonChoice is one authored selectable reason: stable id plus the exact
// label/detail Jev selections render without an LLM writing pass.
type ReasonChoice struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// ReasonCatalog is the immutable authored catalog bound to one brief version.
type ReasonCatalog struct {
	ProfileVersion     int64
	RubricVersion      string
	CatalogVersion     string
	Rubric             string
	RoleCriteria       []RoleCriterion
	RoleCriteriaSHA256 string
	Positive           []ReasonChoice
	Negative           []ReasonChoice
	MissingInformation []ReasonChoice
	SteerRunID         string
	SteerMessageID     string
	CreatedAt          string
	Actor              Actor
}

// ReasonCatalogInput carries one authoring request. ProfileVersion selects
// the existing brief; requirements bind automatically from that brief's
// stored role criteria, never from caller-supplied copies.
type ReasonCatalogInput struct {
	ProfileVersion     int64
	Rubric             string
	Positive           []ReasonChoice
	Negative           []ReasonChoice
	MissingInformation []ReasonChoice
	// SteerRunID/SteerMessageID optionally cite the Codex-owned steer
	// record the owner used to request this search change.
	SteerRunID     string
	SteerMessageID string
}

// CriteriaRubricVersion derives "criteria-v<profile>-<12hex>" from stored
// role-criteria JSON. It MUST stay byte-identical to
// codexservice.CurrentOwnerBrief's rubric derivation: both hash
// encoding/json's marshal of []RoleCriterion. The input is unmarshalled
// and re-marshalled first, so stored bytes and fresh marshals hash the
// same even if whitespace ever differs.
func CriteriaRubricVersion(profile int64, criteriaJSON string) (string, error) {
	if profile < 1 {
		return "", fmt.Errorf("%w: profile version required", ErrInvalid)
	}
	var criteria []RoleCriterion
	if err := json.Unmarshal([]byte(criteriaJSON), &criteria); err != nil {
		return "", fmt.Errorf("%w: role criteria do not decode: %v", ErrInvalid, err)
	}
	raw, err := json.Marshal(criteria)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("criteria-v%d-%s", profile, hex.EncodeToString(sum[:])[:12]), nil
}

// ReasonCatalogVersion derives "catalog-v<profile>-<12hex>" from the
// authored choices. Same choices under the same brief always yield the
// same version; any choice edit yields a new one.
func ReasonCatalogVersion(profile int64, positive, negative, missing []ReasonChoice) (string, error) {
	if profile < 1 {
		return "", fmt.Errorf("%w: profile version required", ErrInvalid)
	}
	raw, err := json.Marshal(struct {
		Positive []ReasonChoice `json:"positive"`
		Negative []ReasonChoice `json:"negative"`
		Missing  []ReasonChoice `json:"missingInformation"`
	}{positive, negative, missing})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("catalog-v%d-%s", profile, hex.EncodeToString(sum[:])[:12]), nil
}

func validReasonChoiceID(id string) bool {
	if len(id) < 1 || len(id) > 80 || strings.TrimSpace(id) != id {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validReasonChoice(c ReasonChoice) bool {
	return validReasonChoiceID(c.ID) &&
		len(c.Label) >= 1 && len(c.Label) <= 200 && strings.TrimSpace(c.Label) == c.Label &&
		len(c.Detail) >= 1 && len(c.Detail) <= 2000 && strings.TrimSpace(c.Detail) == c.Detail
}

func validateReasonCatalogInput(in ReasonCatalogInput) error {
	if in.ProfileVersion < 1 {
		return fmt.Errorf("%w: brief profile version required", ErrInvalid)
	}
	if strings.TrimSpace(in.Rubric) == "" || len(in.Rubric) > 20000 {
		return fmt.Errorf("%w: authored rubric text required (1..20000 chars)", ErrInvalid)
	}
	if len(in.Positive) < 1 || len(in.Positive) > 64 ||
		len(in.Negative) < 1 || len(in.Negative) > 64 ||
		len(in.MissingInformation) > 64 {
		return fmt.Errorf("%w: catalog needs 1..64 positive, 1..64 negative and 0..64 missing-information choices", ErrInvalid)
	}
	seen := map[string]string{}
	for _, group := range []struct {
		name    string
		choices []ReasonChoice
	}{
		{"positive", in.Positive},
		{"negative", in.Negative},
		{"missingInformation", in.MissingInformation},
	} {
		for _, c := range group.choices {
			if !validReasonChoice(c) {
				return fmt.Errorf("%w: invalid %s reason choice %q", ErrInvalid, group.name, c.ID)
			}
			if first, dup := seen[c.ID]; dup {
				return fmt.Errorf("%w: reason id %q in both %s and %s", ErrInvalid, c.ID, first, group.name)
			}
			seen[c.ID] = group.name
		}
	}
	if len(in.SteerRunID) > 128 || len(in.SteerMessageID) > 128 {
		return fmt.Errorf("%w: steer provenance refs limited to 128 chars", ErrInvalid)
	}
	return nil
}

const reasonCatalogColumns = `profile_version,rubric_version,catalog_version,rubric_text,
 role_criteria_json,role_criteria_sha256,positive_json,negative_json,missing_information_json,
 steer_run_id,steer_message_id,created_at,actor_kind,actor_id`

func scanReasonCatalog(row rowScanner) (ReasonCatalog, error) {
	var c ReasonCatalog
	var criteriaJSON, positiveJSON, negativeJSON, missingJSON string
	err := row.Scan(&c.ProfileVersion, &c.RubricVersion, &c.CatalogVersion, &c.Rubric,
		&criteriaJSON, &c.RoleCriteriaSHA256, &positiveJSON, &negativeJSON, &missingJSON,
		&c.SteerRunID, &c.SteerMessageID, &c.CreatedAt, &c.Actor.Kind, &c.Actor.ID)
	if err != nil {
		return ReasonCatalog{}, err
	}
	if err := json.Unmarshal([]byte(criteriaJSON), &c.RoleCriteria); err != nil {
		return ReasonCatalog{}, err
	}
	if err := json.Unmarshal([]byte(positiveJSON), &c.Positive); err != nil {
		return ReasonCatalog{}, err
	}
	if err := json.Unmarshal([]byte(negativeJSON), &c.Negative); err != nil {
		return ReasonCatalog{}, err
	}
	if err := json.Unmarshal([]byte(missingJSON), &c.MissingInformation); err != nil {
		return ReasonCatalog{}, err
	}
	return c, nil
}

func catalogChoicesEqual(a, b []ReasonChoice) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// catalogMatches reports whether stored already holds exactly this
// authoring: same versions, rubric and choices.
func catalogMatches(stored ReasonCatalog, rubricVersion, catalogVersion, rubric string, in ReasonCatalogInput) bool {
	return stored.ProfileVersion == in.ProfileVersion &&
		stored.RubricVersion == rubricVersion &&
		stored.CatalogVersion == catalogVersion &&
		stored.Rubric == rubric &&
		catalogChoicesEqual(stored.Positive, in.Positive) &&
		catalogChoicesEqual(stored.Negative, in.Negative) &&
		catalogChoicesEqual(stored.MissingInformation, in.MissingInformation)
}

// AuthorReasonCatalog persists the one authored rubric + reason catalog for
// a brief version. Requirements bind from the stored brief, never from the
// caller. Identical re-authoring replays the stored row; any difference
// conflicts because rows are immutable. The actor is the authoring agent
// (Codex); the auth layer must derive it from the session.
func (s *Store) AuthorReasonCatalog(ctx context.Context, actor Actor, in ReasonCatalogInput) (ReasonCatalog, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return ReasonCatalog{}, fmt.Errorf("%w: actor required", ErrInvalid)
	}
	if err := validateReasonCatalogInput(in); err != nil {
		return ReasonCatalog{}, err
	}
	// Fast path: identical re-authoring replays without a write.
	if existing, err := s.ReasonCatalog(ctx, in.ProfileVersion); err == nil {
		rubricVersion, rerr := CriteriaRubricVersion(in.ProfileVersion, mustMarshalCriteria(existing.RoleCriteria))
		catalogVersion, cerr := ReasonCatalogVersion(in.ProfileVersion, in.Positive, in.Negative, in.MissingInformation)
		if rerr != nil || cerr != nil {
			return ReasonCatalog{}, errors.Join(rerr, cerr)
		}
		if !catalogMatches(existing, rubricVersion, catalogVersion, in.Rubric, in) {
			return ReasonCatalog{}, ErrConflict
		}
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return ReasonCatalog{}, err
	}
	var result ReasonCatalog
	profile := in.ProfileVersion
	_, err := s.WriteAudited(ctx, actor, func(tx *sql.Tx) (Change, error) {
		var criteriaJSON string
		if err := tx.QueryRowContext(ctx, `SELECT role_criteria_json FROM preferences_versions WHERE version=?`,
			in.ProfileVersion).Scan(&criteriaJSON); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Change{}, ErrNotFound
			}
			return Change{}, err
		}
		var criteria []RoleCriterion
		if err := json.Unmarshal([]byte(criteriaJSON), &criteria); err != nil {
			return Change{}, err
		}
		canonical, err := json.Marshal(criteria)
		if err != nil {
			return Change{}, err
		}
		rubricVersion, err := CriteriaRubricVersion(in.ProfileVersion, criteriaJSON)
		if err != nil {
			return Change{}, err
		}
		catalogVersion, err := ReasonCatalogVersion(in.ProfileVersion, in.Positive, in.Negative, in.MissingInformation)
		if err != nil {
			return Change{}, err
		}
		criteriaSum := sha256.Sum256(canonical)
		positiveJSON, _ := json.Marshal(in.Positive)
		negativeJSON, _ := json.Marshal(in.Negative)
		missingJSON, _ := json.Marshal(in.MissingInformation)
		if existing, err := scanReasonCatalog(tx.QueryRowContext(ctx, `SELECT `+reasonCatalogColumns+
			` FROM reason_catalogs WHERE profile_version=?`, in.ProfileVersion)); err == nil {
			if !catalogMatches(existing, rubricVersion, catalogVersion, in.Rubric, in) {
				return Change{}, ErrConflict
			}
			result = existing
			return Change{Operation: "reason_catalog.author", EntityKind: "reason_catalog",
				EntityID: fmt.Sprintf("v%d", in.ProfileVersion), RevisionAfter: &profile}, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Change{}, err
		}
		now := utcNow()
		if _, err := tx.ExecContext(ctx, `INSERT INTO reason_catalogs
   (profile_version,rubric_version,catalog_version,rubric_text,role_criteria_json,role_criteria_sha256,
    positive_json,negative_json,missing_information_json,steer_run_id,steer_message_id,
    created_at,actor_kind,actor_id)
   VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.ProfileVersion, rubricVersion, catalogVersion, in.Rubric,
			criteriaJSON, hex.EncodeToString(criteriaSum[:]),
			string(positiveJSON), string(negativeJSON), string(missingJSON),
			in.SteerRunID, in.SteerMessageID, now, actor.Kind, actor.ID); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				return Change{}, ErrConflict
			}
			return Change{}, err
		}
		stored, err := scanReasonCatalog(tx.QueryRowContext(ctx, `SELECT `+reasonCatalogColumns+
			` FROM reason_catalogs WHERE profile_version=?`, in.ProfileVersion))
		if err != nil {
			return Change{}, err
		}
		result = stored
		return Change{Operation: "reason_catalog.author", EntityKind: "reason_catalog",
			EntityID: fmt.Sprintf("v%d", in.ProfileVersion), RevisionAfter: &profile}, nil
	})
	if err != nil {
		return ReasonCatalog{}, err
	}
	return result, nil
}

func mustMarshalCriteria(criteria []RoleCriterion) string {
	raw, _ := json.Marshal(criteria)
	return string(raw)
}

// ReasonCatalog reads the authored catalog for one brief version. Pure
// read: a version with no authored catalog is ErrNotFound, never
// generated on the fly.
func (s *Store) ReasonCatalog(ctx context.Context, profileVersion int64) (ReasonCatalog, error) {
	if profileVersion < 1 {
		return ReasonCatalog{}, fmt.Errorf("%w: profile version required", ErrInvalid)
	}
	c, err := scanReasonCatalog(s.db.QueryRowContext(ctx, `SELECT `+reasonCatalogColumns+
		` FROM reason_catalogs WHERE profile_version=?`, profileVersion))
	if errors.Is(err, sql.ErrNoRows) {
		return ReasonCatalog{}, ErrNotFound
	}
	return c, err
}

// CurrentReasonCatalog reads the authored catalog for the current brief.
// Missing preferences or a not-yet-authored catalog is ErrNotFound, so
// callers keep reporting honest unavailable states.
func (s *Store) CurrentReasonCatalog(ctx context.Context) (ReasonCatalog, error) {
	c, err := scanReasonCatalog(s.db.QueryRowContext(ctx, `SELECT `+reasonCatalogColumns+`
  FROM reason_catalogs JOIN preferences_current ON preferences_current.version=reason_catalogs.profile_version
  WHERE preferences_current.singleton=1`))
	if errors.Is(err, sql.ErrNoRows) {
		return ReasonCatalog{}, ErrNotFound
	}
	return c, err
}
