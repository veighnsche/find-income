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
		// A conflict may be a lost insert race: a concurrent commission
		// may have committed the identical authoring first. Replay the
		// stored row when it matches exactly; divergent content keeps
		// conflicting because rows are immutable.
		if errors.Is(err, ErrConflict) {
			if replay, rerr := s.authorConflictReplay(ctx, in); rerr == nil {
				return replay, nil
			}
		}
		return ReasonCatalog{}, err
	}
	return result, nil
}

// authorConflictReplay returns the stored catalog when it holds exactly
// the conflicting authoring, so concurrent identical commissions converge
// instead of failing. Any difference (or a still-missing row) is an error
// and the caller keeps the original conflict.
func (s *Store) authorConflictReplay(ctx context.Context, in ReasonCatalogInput) (ReasonCatalog, error) {
	existing, err := s.ReasonCatalog(ctx, in.ProfileVersion)
	if err != nil {
		return ReasonCatalog{}, err
	}
	rubricVersion, err := CriteriaRubricVersion(in.ProfileVersion, mustMarshalCriteria(existing.RoleCriteria))
	if err != nil {
		return ReasonCatalog{}, err
	}
	catalogVersion, err := ReasonCatalogVersion(in.ProfileVersion, in.Positive, in.Negative, in.MissingInformation)
	if err != nil {
		return ReasonCatalog{}, err
	}
	if !catalogMatches(existing, rubricVersion, catalogVersion, in.Rubric, in) {
		return ReasonCatalog{}, ErrConflict
	}
	return existing, nil
}

func mustMarshalCriteria(criteria []RoleCriterion) string {
	raw, _ := json.Marshal(criteria)
	return string(raw)
}

// CatalogInputForBrief derives the deterministic commission-time catalog
// input for one saved brief version (C2/D1). Every choice preserves the
// owner's actual requirements: require/prefer criteria become positive
// reasons, avoid criteria become negative reasons, and missing-information
// reasons cover only the pay/location/arrangement facts the brief sets.
// No model call happens here; concurrent commissions compute identical
// input, so AuthorReasonCatalog converges them on one accepted version.
// A brief with zero role criteria is ErrInvalid: there are no
// requirements to preserve, and inventing reasons is never allowed.
func CatalogInputForBrief(prefs Preferences) (ReasonCatalogInput, error) {
	if prefs.Version < 1 {
		return ReasonCatalogInput{}, fmt.Errorf("%w: brief profile version required", ErrInvalid)
	}
	var wants, avoids []RoleCriterion
	for _, c := range prefs.RoleCriteria {
		switch c.Mode {
		case "require", "prefer":
			wants = append(wants, c)
		case "avoid":
			avoids = append(avoids, c)
		default:
			return ReasonCatalogInput{}, fmt.Errorf("%w: role criterion %q has unknown mode %q", ErrInvalid, c.ID, c.Mode)
		}
	}
	if len(wants) == 0 && len(avoids) == 0 {
		return ReasonCatalogInput{}, fmt.Errorf("%w: brief v%d has no role criteria; save search wants before commissioning", ErrInvalid, prefs.Version)
	}
	positive := make([]ReasonChoice, 0, len(wants)+len(avoids))
	for _, c := range wants {
		positive = append(positive, ReasonChoice{
			ID:     catalogReasonID("want-", c.ID),
			Label:  c.Label,
			Detail: "Listing satisfies this " + wantAdjective(c.Mode) + " pattern: " + criterionDetailText(c),
		})
	}
	// A brief with only don't-wants still needs positive choices: restating
	// each exclusion as a supported absence preserves the requirement
	// without inventing a new one.
	for _, c := range avoids {
		if len(wants) > 0 {
			break
		}
		positive = append(positive, ReasonChoice{
			ID:     catalogReasonID("clear-", c.ID),
			Label:  "No " + c.Label,
			Detail: "Listing avoids this excluded pattern: " + criterionDetailText(c),
		})
	}
	negative := make([]ReasonChoice, 0, len(avoids)+len(wants))
	for _, c := range avoids {
		negative = append(negative, ReasonChoice{
			ID:     catalogReasonID("avoid-", c.ID),
			Label:  c.Label,
			Detail: "Listing shows this excluded pattern: " + criterionDetailText(c),
		})
	}
	// A brief with only wants still needs negative choices: the negation
	// of each want is exactly what a negative reason means.
	for _, c := range wants {
		if len(avoids) > 0 {
			break
		}
		negative = append(negative, ReasonChoice{
			ID:     catalogReasonID("miss-", c.ID),
			Label:  "No " + c.Label,
			Detail: "Listing lacks this " + wantAdjective(c.Mode) + " pattern: " + criterionDetailText(c),
		})
	}
	missing := catalogMissingChoices(prefs)
	in := ReasonCatalogInput{
		ProfileVersion: prefs.Version, Rubric: catalogRubric(prefs, wants, avoids),
		Positive: positive, Negative: negative, MissingInformation: missing,
	}
	if err := validateReasonCatalogInput(in); err != nil {
		return ReasonCatalogInput{}, err
	}
	return in, nil
}

// wantAdjective renders a want mode as an adjective. Callers only pass the
// validated want modes; anything else fails closed as "saved".
func wantAdjective(mode string) string {
	switch mode {
	case "require":
		return "required"
	case "prefer":
		return "preferred"
	default:
		return "saved"
	}
}

// criterionDetailText renders the owner's own description (or label when no
// description was saved). Stored descriptions are untrimmed and unbounded
// in length, so the text is trimmed; the rubric truncates separately.
func criterionDetailText(c RoleCriterion) string {
	if desc := strings.TrimSpace(c.Description); desc != "" {
		return desc
	}
	return c.Label
}

// catalogReasonID prefixes a criterion id while keeping a valid reason id
// (1..80 chars of [a-z0-9-]). Overlong ids truncate with a digest suffix so
// distinct criteria keep distinct deterministic ids.
func catalogReasonID(prefix, criterionID string) string {
	if len(prefix)+len(criterionID) <= 80 {
		return prefix + criterionID
	}
	sum := sha256.Sum256([]byte(criterionID))
	keep := 80 - len(prefix) - 9
	if keep < 1 {
		keep = 1
	}
	if keep > len(criterionID) {
		keep = len(criterionID)
	}
	return prefix + criterionID[:keep] + "-" + hex.EncodeToString(sum[:])[:8]
}

// catalogMissingChoices covers the brief's pay/location/arrangement facts.
// Each reason is conditional on its fact being set; an unset fact yields no
// reason rather than a generic filler.
func catalogMissingChoices(prefs Preferences) []ReasonChoice {
	var missing []ReasonChoice
	if prefs.MinMonthlyBaseCents > 0 {
		missing = append(missing, ReasonChoice{
			ID: "pay-unstated", Label: "Pay unstated",
			Detail: fmt.Sprintf("Listing states no base pay, so the owner's minimum of %d %s/month cannot be checked.",
				prefs.MinMonthlyBaseCents, prefs.SalaryCurrency),
		})
	}
	if strings.TrimSpace(prefs.PreferredLocation) != "" {
		missing = append(missing, ReasonChoice{
			ID: "location-unstated", Label: "Location unstated",
			Detail: "Listing states no work location, so the " +
				truncateRunes(strings.TrimSpace(prefs.PreferredLocation), 200) + " preference cannot be checked.",
		})
	}
	if prefs.AllowRemote || prefs.AllowHybrid {
		missing = append(missing, ReasonChoice{
			ID: "arrangement-unstated", Label: "Arrangement unstated",
			Detail: "Listing states no remote/hybrid/onsite arrangement, so the work-location preference cannot be checked.",
		})
	}
	return missing
}

// catalogRubric renders the deterministic matching rubric: brief identity,
// the saved wants and don't-wants in saved order, and the saved facts.
// Criterion descriptions truncate to 160 runes so 32 maximal criteria stay
// far below the 20000-char rubric limit.
func catalogRubric(prefs Preferences, wants, avoids []RoleCriterion) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Search rubric for saved brief v%d (%d wants, %d don't-wants).\n",
		prefs.Version, len(wants), len(avoids))
	out.WriteString("Wants, in saved order:\n")
	for _, c := range wants {
		fmt.Fprintf(&out, "- %s (%s): %s\n", c.Label, c.Mode, truncateRunes(criterionDetailText(c), 160))
	}
	out.WriteString("Don't-wants, in saved order:\n")
	for _, c := range avoids {
		fmt.Fprintf(&out, "- %s (%s): %s\n", c.Label, c.Mode, truncateRunes(criterionDetailText(c), 160))
	}
	location := "unset"
	if loc := strings.TrimSpace(prefs.PreferredLocation); loc != "" {
		location = truncateRunes(loc, 200)
	}
	fmt.Fprintf(&out, "Saved facts: location %s; remote %s; hybrid %s; %.2f h/wk target; minimum %d %s/month; timezone %s.\n",
		location, allowedText(prefs.AllowRemote), allowedText(prefs.AllowHybrid),
		float64(prefs.TargetHoursHundredths)/100, prefs.MinMonthlyBaseCents, prefs.SalaryCurrency,
		truncateRunes(strings.TrimSpace(prefs.Timezone), 100))
	out.WriteString("Judge each listing against these saved requirements only; " +
		"select the verbatim catalog reason best supported by the cited vacancy capture, or abstain.")
	return out.String()
}

func allowedText(allowed bool) string {
	if allowed {
		return "allowed"
	}
	return "not allowed"
}

func truncateRunes(value string, max int) string {
	if max < 1 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// EnsureReasonCatalog returns the brief version's catalog, authoring the
// deterministic commission-time catalog when none exists yet. Reads stay
// pure: only explicit commissions call this, never passive GETs.
// First-accepted wins: a concurrent or earlier authoring is reused as-is,
// so retried/concurrent commissions converge on one accepted version even
// when another author (a seed or a future drafting pass) wrote different
// content for the same brief. Direct AuthorReasonCatalog callers keep the
// strict identical-or-conflict rule.
func (s *Store) EnsureReasonCatalog(ctx context.Context, actor Actor, profileVersion int64) (ReasonCatalog, error) {
	if existing, err := s.ReasonCatalog(ctx, profileVersion); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return ReasonCatalog{}, err
	}
	prefs, err := s.PreferenceVersion(ctx, profileVersion)
	if err != nil {
		return ReasonCatalog{}, err
	}
	in, err := CatalogInputForBrief(prefs)
	if err != nil {
		return ReasonCatalog{}, err
	}
	catalog, err := s.AuthorReasonCatalog(ctx, actor, in)
	if err == nil {
		return catalog, nil
	}
	if errors.Is(err, ErrConflict) {
		if existing, rerr := s.ReasonCatalog(ctx, profileVersion); rerr == nil {
			return existing, nil
		} else if !errors.Is(rerr, ErrNotFound) {
			return ReasonCatalog{}, rerr
		}
	}
	return ReasonCatalog{}, err
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
