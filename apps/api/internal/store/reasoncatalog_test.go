package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func testCatalogInput(profile int64) ReasonCatalogInput {
	return ReasonCatalogInput{
		ProfileVersion: profile,
		Rubric:         "Match senior support roles in Amsterdam; hybrid or remote; exclude on-call heavy rotations.",
		Positive: []ReasonChoice{
			{ID: "hybrid-ok", Label: "Hybrid friendly", Detail: "Listing offers hybrid or remote work in the owner's timezone."},
			{ID: "support-senior", Label: "Senior support scope", Detail: "Role centers on senior customer-support engineering."},
		},
		Negative: []ReasonChoice{
			{ID: "oncall-heavy", Label: "Heavy on-call", Detail: "Listing requires frequent night or weekend on-call rotations."},
		},
		MissingInformation: []ReasonChoice{
			{ID: "pay-unknown", Label: "Pay unstated", Detail: "Listing states no base pay, so the minimum cannot be checked."},
		},
		SteerRunID:     "round-1",
		SteerMessageID: "steer.round-1.change-search-1",
	}
}

func TestReasonCatalogAuthorOncePerVersion(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	actor := Actor{Kind: "agent", ID: "codex"}

	first, err := s.AuthorReasonCatalog(ctx, actor, testCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.RubricVersion, "criteria-v1-") || len(first.RubricVersion) != len("criteria-v1-")+12 {
		t.Fatalf("rubric version %q must be criteria-v1-<12hex>", first.RubricVersion)
	}
	if !strings.HasPrefix(first.CatalogVersion, "catalog-v1-") || len(first.CatalogVersion) != len("catalog-v1-")+12 {
		t.Fatalf("catalog version %q must be catalog-v1-<12hex>", first.CatalogVersion)
	}
	if first.Actor != actor || first.CreatedAt == "" {
		t.Fatalf("catalog attribution missing: %+v", first)
	}

	// Identical re-authoring replays the stored row instead of forking it.
	replay, err := s.AuthorReasonCatalog(ctx, actor, testCatalogInput(1))
	if err != nil {
		t.Fatalf("identical re-authoring: %v", err)
	}
	if replay.CatalogVersion != first.CatalogVersion || replay.CreatedAt != first.CreatedAt {
		t.Fatalf("replay forked the catalog: %+v vs %+v", replay, first)
	}

	// Any differing content conflicts: rows are immutable.
	changed := testCatalogInput(1)
	changed.Positive = append(changed.Positive, ReasonChoice{ID: "extra", Label: "Extra", Detail: "An added reason."})
	if _, err := s.AuthorReasonCatalog(ctx, actor, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("differing re-authoring: %v, want ErrConflict", err)
	}
	changed = testCatalogInput(1)
	changed.Rubric = "A rewritten rubric."
	if _, err := s.AuthorReasonCatalog(ctx, actor, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("rubric rewrite: %v, want ErrConflict", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM reason_catalogs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reason_catalogs rows = %d, want exactly 1", count)
	}
}

func TestBriefCatalogChangePreservesRequirements(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	owner := Actor{Kind: "administrator", ID: "test-owner"}
	agent := Actor{Kind: "agent", ID: "codex"}

	v1, err := s.AuthorReasonCatalog(ctx, agent, testCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v1.RoleCriteria) != len(prefs.RoleCriteria) {
		t.Fatalf("v1 catalog kept %d criteria, brief has %d", len(v1.RoleCriteria), len(prefs.RoleCriteria))
	}
	for i, c := range prefs.RoleCriteria {
		if v1.RoleCriteria[i] != c {
			t.Fatalf("v1 catalog criterion %d = %+v, want %+v", i, v1.RoleCriteria[i], c)
		}
	}

	// The owner changes the search: new brief version, new catalog.
	next := prefs
	next.RoleCriteria = append(next.RoleCriteria, RoleCriterion{
		ID: "weekend-cover", Label: "Weekend cover", Description: "Occasional weekend cover is acceptable.",
		Kind: "responsibility", Mode: "prefer",
	})
	updated, _, err := s.UpdatePreferences(ctx, 1, next, owner)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 {
		t.Fatalf("brief version = %d, want 2", updated.Version)
	}
	v2in := testCatalogInput(2)
	v2in.Rubric = "Match senior support roles; occasional weekend cover now acceptable."
	v2, err := s.AuthorReasonCatalog(ctx, agent, v2in)
	if err != nil {
		t.Fatal(err)
	}
	if v2.RubricVersion == v1.RubricVersion {
		t.Fatalf("brief change kept rubric version %q", v2.RubricVersion)
	}
	if v2.CatalogVersion == v1.CatalogVersion {
		t.Fatalf("brief change kept catalog version %q", v2.CatalogVersion)
	}
	if len(v2.RoleCriteria) != len(prefs.RoleCriteria)+1 {
		t.Fatalf("v2 catalog kept %d criteria, want %d", len(v2.RoleCriteria), len(prefs.RoleCriteria)+1)
	}

	// The old catalog still classifies against the old brief.
	kept, err := s.ReasonCatalog(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if kept.CatalogVersion != v1.CatalogVersion || kept.RubricVersion != v1.RubricVersion ||
		len(kept.RoleCriteria) != len(prefs.RoleCriteria) || kept.Rubric != v1.Rubric {
		t.Fatalf("v1 catalog mutated by the brief change: %+v", kept)
	}
	current, err := s.CurrentReasonCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.CatalogVersion != v2.CatalogVersion {
		t.Fatalf("current catalog = %q, want %q", current.CatalogVersion, v2.CatalogVersion)
	}
}

func TestReasonCatalogReadsSideEffectFree(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)

	if _, err := s.CurrentReasonCatalog(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("current catalog before authoring: %v, want ErrNotFound", err)
	}
	if _, err := s.ReasonCatalog(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown version: %v, want ErrNotFound", err)
	}
	if _, err := s.ReasonCatalog(ctx, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("version 0: %v, want ErrInvalid", err)
	}

	stored, err := s.AuthorReasonCatalog(ctx, Actor{Kind: "agent", ID: "codex"}, testCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	counts := func() (catalogs, audits int) {
		t.Helper()
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM reason_catalogs").Scan(&catalogs); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_changes").Scan(&audits); err != nil {
			t.Fatal(err)
		}
		return catalogs, audits
	}
	beforeCatalogs, beforeAudits := counts()
	for i := 0; i < 3; i++ {
		got, err := s.ReasonCatalog(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got.CatalogVersion != stored.CatalogVersion {
			t.Fatalf("read %d returned %q, want %q", i, got.CatalogVersion, stored.CatalogVersion)
		}
		cur, err := s.CurrentReasonCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if cur.CatalogVersion != stored.CatalogVersion {
			t.Fatalf("current read %d returned %q", i, cur.CatalogVersion)
		}
	}
	afterCatalogs, afterAudits := counts()
	if afterCatalogs != beforeCatalogs || afterAudits != beforeAudits {
		t.Fatalf("reads wrote state: catalogs %d->%d audits %d->%d",
			beforeCatalogs, afterCatalogs, beforeAudits, afterAudits)
	}
}

func TestReasonCatalogAuthorValidation(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	agent := Actor{Kind: "agent", ID: "codex"}

	if _, err := s.AuthorReasonCatalog(ctx, Actor{}, testCatalogInput(1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing actor: %v, want ErrInvalid", err)
	}
	in := testCatalogInput(99)
	if _, err := s.AuthorReasonCatalog(ctx, agent, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown brief version: %v, want ErrNotFound", err)
	}
	cases := map[string]func(*ReasonCatalogInput){
		"empty rubric":       func(in *ReasonCatalogInput) { in.Rubric = "  " },
		"no positive":        func(in *ReasonCatalogInput) { in.Positive = nil },
		"no negative":        func(in *ReasonCatalogInput) { in.Negative = nil },
		"bad id":             func(in *ReasonCatalogInput) { in.Positive[0].ID = "Has Space" },
		"blank label":        func(in *ReasonCatalogInput) { in.Negative[0].Label = "" },
		"blank detail":       func(in *ReasonCatalogInput) { in.MissingInformation[0].Detail = " " },
		"duplicate id":       func(in *ReasonCatalogInput) { in.Negative[0].ID = in.Positive[0].ID },
		"steer ref too long": func(in *ReasonCatalogInput) { in.SteerMessageID = strings.Repeat("x", 129) },
	}
	for name, mutate := range cases {
		in := testCatalogInput(1)
		mutate(&in)
		if _, err := s.AuthorReasonCatalog(ctx, agent, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v, want ErrInvalid", name, err)
		}
	}
	// Empty missing-information list is valid: some briefs state no unknowns.
	in = testCatalogInput(1)
	in.MissingInformation = nil
	if _, err := s.AuthorReasonCatalog(ctx, agent, in); err != nil {
		t.Fatalf("empty missing-information: %v", err)
	}
}

func TestReasonCatalogRubricMatchesBriefFormula(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	prefs, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Inline copy of the codexservice.CurrentOwnerBrief rubric derivation
	// (store cannot import codexservice: it would cycle). Any drift here
	// fails loudly so the two stay byte-identical.
	raw, err := json.Marshal(prefs.RoleCriteria)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	want := fmt.Sprintf("criteria-v%d-%s", prefs.Version, hex.EncodeToString(sum[:])[:12])

	got, err := CriteriaRubricVersion(prefs.Version, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("CriteriaRubricVersion = %q, want %q", got, want)
	}
	stored, err := s.AuthorReasonCatalog(ctx, Actor{Kind: "agent", ID: "codex"}, testCatalogInput(prefs.Version))
	if err != nil {
		t.Fatal(err)
	}
	if stored.RubricVersion != want {
		t.Fatalf("stored rubric = %q, want %q", stored.RubricVersion, want)
	}
	// Whitespace-tolerant input hashes the same: the derivation remarshals.
	spaced := strings.Replace(string(raw), ",", ", ", -1)
	again, err := CriteriaRubricVersion(prefs.Version, spaced)
	if err != nil {
		t.Fatal(err)
	}
	if again != want {
		t.Fatalf("spaced criteria hashed to %q, want %q", again, want)
	}
}

// D1: the deterministic commission-time derivation preserves the owner's
// actual requirements: wants become positive reasons, don't-wants become
// negative reasons, and missing-information reasons cover only the facts
// the brief sets. The derivation is stable for an unchanged brief.
func TestCatalogInputForBriefPreservesRequirements(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	prefs, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := CatalogInputForBrief(prefs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CatalogInputForBrief(prefs)
	if err != nil {
		t.Fatal(err)
	}
	rawFirst, _ := json.Marshal(first)
	if rawSecond, _ := json.Marshal(second); string(rawFirst) != string(rawSecond) {
		t.Fatal("commission-time derivation is not deterministic")
	}
	byID := func(choices []ReasonChoice) map[string]ReasonChoice {
		out := map[string]ReasonChoice{}
		for _, c := range choices {
			out[c.ID] = c
		}
		return out
	}
	positive, negative, missing := byID(first.Positive), byID(first.Negative), byID(first.MissingInformation)
	if got := positive["want-backend-platform"]; got.Label != "Backend and platform work" ||
		!strings.Contains(got.Detail, "Backend or platform engineering is a substantive role responsibility.") {
		t.Fatalf("want reason does not preserve the criterion: %+v", got)
	}
	if got := negative["avoid-frontend-duties"]; got.Label != "Frontend implementation duties" ||
		!strings.Contains(got.Detail, "Frontend UI implementation is an assigned responsibility in this role.") {
		t.Fatalf("avoid reason does not preserve the criterion: %+v", got)
	}
	if _, ok := missing["pay-unstated"]; !ok {
		t.Fatalf("pay threshold %d sets no pay-unstated reason: %+v", prefs.MinMonthlyBaseCents, first.MissingInformation)
	}
	if _, ok := missing["location-unstated"]; !ok {
		t.Fatalf("location %q sets no location-unstated reason", prefs.PreferredLocation)
	}
	if !strings.Contains(first.Rubric, "brief v1") ||
		!strings.Contains(first.Rubric, "Backend and platform work") ||
		!strings.Contains(first.Rubric, "PHP-focused work") {
		t.Fatalf("rubric omits brief identity or requirements: %q", first.Rubric)
	}
	if err := validateReasonCatalogInput(first); err != nil {
		t.Fatalf("derived input fails catalog validation: %v", err)
	}
}

// D1: one-sided briefs still yield both polarities by negating/restating
// the saved requirements; a brief with zero criteria fails closed instead
// of inventing reasons.
func TestCatalogInputForBriefOneSidedAndEmpty(t *testing.T) {
	brief := func(criteria ...RoleCriterion) Preferences {
		return Preferences{Version: 1, PreferredLocation: "", AllowRemote: false, AllowHybrid: false,
			TargetHoursHundredths: 3200, MinMonthlyBaseCents: 0, SalaryCurrency: "EUR",
			Timezone: "Europe/Amsterdam", RoleCriteria: criteria}
	}
	want := RoleCriterion{ID: "backend", Label: "Backend work", Description: "Backend engineering.", Kind: "role", Mode: "require"}
	avoid := RoleCriterion{ID: "nights", Label: "Night shifts", Description: "Night shifts.", Kind: "responsibility", Mode: "avoid"}

	wantsOnly, err := CatalogInputForBrief(brief(want))
	if err != nil {
		t.Fatal(err)
	}
	if len(wantsOnly.Positive) != 1 || len(wantsOnly.Negative) != 1 ||
		wantsOnly.Positive[0].ID != "want-backend" || wantsOnly.Negative[0].ID != "miss-backend" {
		t.Fatalf("wants-only brief: %+v", wantsOnly)
	}
	avoidsOnly, err := CatalogInputForBrief(brief(avoid))
	if err != nil {
		t.Fatal(err)
	}
	if len(avoidsOnly.Positive) != 1 || len(avoidsOnly.Negative) != 1 ||
		avoidsOnly.Positive[0].ID != "clear-nights" || avoidsOnly.Negative[0].ID != "avoid-nights" {
		t.Fatalf("avoids-only brief: %+v", avoidsOnly)
	}
	if _, err := CatalogInputForBrief(brief()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero-criteria brief: got %v, want ErrInvalid (never invent reasons)", err)
	}
}

// D1: commission-time ensure authors once per brief version and reuses
// across commissions; a new goal version authors exactly one new catalog
// while the old one stays retained under its own version.
func TestEnsureReasonCatalogAuthorsOncePerVersion(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	owner := ownerActor()

	first, err := s.EnsureReasonCatalog(ctx, owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := s.EnsureReasonCatalog(ctx, owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	if reused.CatalogVersion != first.CatalogVersion || reused.CreatedAt != first.CreatedAt {
		t.Fatalf("second commission forked the catalog: %+v vs %+v", reused, first)
	}
	prefs, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefs.RoleCriteria = append(prefs.RoleCriteria, RoleCriterion{
		ID: "go-services", Label: "Go services", Description: "Go service ownership.",
		Kind: "technology", Mode: "prefer",
	})
	updated, _, err := s.UpdatePreferences(ctx, prefs.Version, prefs, owner)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnsureReasonCatalog(ctx, owner, updated.Version)
	if err != nil {
		t.Fatal(err)
	}
	if second.CatalogVersion == first.CatalogVersion || second.ProfileVersion != updated.Version {
		t.Fatalf("new goal version reused the old catalog: %+v", second)
	}
	retained, err := s.ReasonCatalog(ctx, 1)
	if err != nil || retained.CatalogVersion != first.CatalogVersion {
		t.Fatalf("v1 catalog not retained: %+v err=%v", retained, err)
	}
	if _, err := s.EnsureReasonCatalog(ctx, owner, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown brief version: got %v, want ErrNotFound", err)
	}
	emptied := updated
	emptied.RoleCriteria = nil
	empty, _, err := s.UpdatePreferences(ctx, updated.Version, emptied, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureReasonCatalog(ctx, owner, empty.Version); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero-criteria brief: got %v, want ErrInvalid", err)
	}
}

// D1/C2: concurrent commissions converge on one accepted brief version:
// every ensure returns the same catalog version without errors.
func TestEnsureReasonCatalogConvergesConcurrent(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	const commissions = 8
	versions := make([]string, commissions)
	errs := make([]error, commissions)
	var wg sync.WaitGroup
	for i := 0; i < commissions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			catalog, err := s.EnsureReasonCatalog(ctx, ownerActor(), 1)
			if err == nil {
				versions[i] = catalog.CatalogVersion
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent commission %d: %v", i, err)
		}
		if versions[i] != versions[0] {
			t.Fatalf("concurrent commissions forked: %q vs %q", versions[i], versions[0])
		}
	}
	var count int
	if err := s.Read(ctx, func(r Reader) error {
		return r.QueryRowContext(ctx, "SELECT count(*) FROM reason_catalogs WHERE profile_version=1").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent commissions stored %d catalog rows, want 1", count)
	}
}

// D1: first-accepted wins at commission time. A catalog authored by other
// means (a seed or a future drafting pass) is reused as-is instead of
// failing the commission, while direct divergent authoring still
// conflicts.
func TestEnsureReasonCatalogReusesSeededDivergent(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	agent := Actor{Kind: "agent", ID: "codex"}
	seeded, err := s.AuthorReasonCatalog(ctx, agent, testCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	ensured, err := s.EnsureReasonCatalog(ctx, ownerActor(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if ensured.CatalogVersion != seeded.CatalogVersion {
		t.Fatalf("commission did not reuse the seeded catalog: %+v vs %+v", ensured, seeded)
	}
	changed := testCatalogInput(1)
	changed.Positive[0].Label = "Changed label"
	if _, err := s.AuthorReasonCatalog(ctx, agent, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("divergent direct authoring: got %v, want ErrConflict", err)
	}
}
