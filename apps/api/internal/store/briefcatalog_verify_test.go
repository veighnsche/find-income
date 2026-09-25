package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// RW-A2 chain verification: saved brief → reason catalog → run/finding
// versions. A pending correction never presents as the effective brief,
// a completed correction rebinds the brief while retaining earlier work,
// one catalog serves every run on its brief version, findings carry the
// pinned versions with verbatim reasons, classification commissions no
// deeper work, and reads preserve every version, date and evidence byte.

// briefCatalogTableCounts snapshots row counts for the commission-sensitive
// tables plus the brief/catalog/finding stores.
func briefCatalogTableCounts(t *testing.T, s *Store) map[string]int {
	t.Helper()
	ctx := context.Background()
	out := map[string]int{}
	for _, table := range []string{"rounds", "round_attempts", "research_requests",
		"jev_attempts", "jev_assessments_dynamic", "run_checkpoints", "job_checks",
		"reason_catalogs", "findings"} {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func briefCatalogRequireCounts(t *testing.T, before, after map[string]int, findingsDelta int) {
	t.Helper()
	for table, n := range before {
		want := n
		if table == "findings" {
			want += findingsDelta
		}
		if after[table] != want {
			t.Fatalf("%s rows %d->%d, want %d", table, n, after[table], want)
		}
	}
}

// A commissioned-but-incomplete correction round moves nothing: the brief,
// the current catalog and a saved classification still read the old
// version. Completing the correction bumps the brief, stales the old
// finding, retains the old brief/catalog byte-identically, and leaves the
// new brief honestly unauthored until its own catalog exists.
func TestBriefCatalogPendingCorrectionNotEffective(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	owner := FixtureActor()
	agent := Actor{Kind: "agent", ID: "codex"}

	v1prefs, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v1prefs.Version != 1 {
		t.Fatalf("fresh brief version = %d, want 1", v1prefs.Version)
	}
	v1catalog, err := s.AuthorReasonCatalog(ctx, agent, testCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}

	// Pending correction: accepted work, not a saved correction.
	pending, created, err := s.StartRound(ctx, owner, StartRoundInput{
		RequestKey: "rw-a2-pending-correction", Intent: "Apply the owner's correction to the saved search.",
		Outcome: "process_input", ProfileVersion: v1prefs.Version,
		Scope:    RoundScope{Operations: []string{"preferences.correct"}, Resources: []string{"profile:current"}},
		Limits:   RoundAllowance{Requests: 4, Items: 2, Tools: 2, Turns: 2},
		Deadline: time.Now().Add(time.Hour).UTC().Round(0),
	})
	if err != nil || !created || pending.State != RoundQueued {
		t.Fatalf("pending correction round: %+v created=%v err=%v", pending, created, err)
	}
	if cur, err := s.CurrentPreferences(ctx); err != nil || cur.Version != 1 {
		t.Fatalf("brief during pending correction: %+v %v", cur, err)
	}
	if cur, err := s.CurrentReasonCatalog(ctx); err != nil || cur.CatalogVersion != v1catalog.CatalogVersion {
		t.Fatalf("current catalog during pending correction: %+v %v", cur, err)
	}

	// A run commissioned against v1 pins the v1 binding; the active slot
	// is held by the pending correction, so the run row rides in a
	// terminal state (classification only needs the row, not the slot).
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if err := SeedResearchRoundState(ctx, db, "round-1", "attempt-1", "completed"); err != nil {
			return err
		}
		return SeedResearchCompany(ctx, db, "company-1", "Example BV")
	}); err != nil {
		t.Fatal(err)
	}
	opp, _, err := s.CreateOpportunity(ctx, FixtureActor(), fixtureOpportunity("company-1"))
	if err != nil {
		t.Fatal(err)
	}
	capture := insertFixtureCapture(t, s, "brief-a2-body")
	assessment := insertFindingAssessment(t, s, "jev-1", 0, FixtureSHA256("brief-a2-reuse-1"), v1catalog)
	saved, err := s.SaveFinding(ctx, findingSaveInput(findingFixture{
		catalog: v1catalog, assessment: assessment, capture: capture, opportunityID: opp.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Stale || saved.ProfileVersion != 1 || saved.CatalogVersion != v1catalog.CatalogVersion {
		t.Fatalf("v1 finding bindings: %+v", saved)
	}

	// Completed correction: the durable effect is a new brief version.
	next := v1prefs
	next.PreferredLocation = "Rotterdam"
	next.RoleCriteria = append(next.RoleCriteria, RoleCriterion{
		ID: "weekend-cover", Label: "Weekend cover", Description: "Occasional weekend cover is acceptable.",
		Kind: "responsibility", Mode: "prefer",
	})
	v2prefs, _, err := s.UpdatePreferences(ctx, 1, next, owner)
	if err != nil {
		t.Fatal(err)
	}
	if v2prefs.Version != 2 {
		t.Fatalf("brief after correction = %d, want 2", v2prefs.Version)
	}

	// Earlier work is retained byte-identically.
	keptPrefs, err := s.PreferenceVersion(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keptPrefs.RoleCriteria, v1prefs.RoleCriteria) ||
		keptPrefs.PreferredLocation != v1prefs.PreferredLocation {
		t.Fatalf("v1 brief mutated by the correction: %+v", keptPrefs)
	}
	keptCatalog, err := s.ReasonCatalog(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if keptCatalog.CatalogVersion != v1catalog.CatalogVersion ||
		keptCatalog.RubricVersion != v1catalog.RubricVersion ||
		keptCatalog.Rubric != v1catalog.Rubric ||
		keptCatalog.CreatedAt != v1catalog.CreatedAt ||
		!reflect.DeepEqual(keptCatalog.RoleCriteria, v1catalog.RoleCriteria) ||
		!reflect.DeepEqual(keptCatalog.Positive, v1catalog.Positive) {
		t.Fatalf("v1 catalog mutated by the correction: %+v", keptCatalog)
	}

	// The new brief has no catalog yet: honest unavailable, never a
	// silent rebind of the stale v1 catalog.
	if _, err := s.CurrentReasonCatalog(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("current catalog after correction: %v, want ErrNotFound", err)
	}
	reread, err := s.GetOpportunityFinding(ctx, opp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reread.Stale || reread.StaleBasis != FindingStaleBriefChanged {
		t.Fatalf("v1 finding after correction: stale=%v basis=%q", reread.Stale, reread.StaleBasis)
	}
	if reread.CatalogVersion != v1catalog.CatalogVersion || reread.CreatedAt != saved.CreatedAt {
		t.Fatalf("stale finding lost its pinned versions: %+v", reread)
	}

	// The v2 catalog authors once against the new brief formula.
	v2in := testCatalogInput(2)
	v2in.Rubric = "Match senior support roles; occasional weekend cover now acceptable."
	v2catalog, err := s.AuthorReasonCatalog(ctx, agent, v2in)
	if err != nil {
		t.Fatal(err)
	}
	if v2catalog.RubricVersion == v1catalog.RubricVersion {
		t.Fatalf("brief change kept rubric version %q", v2catalog.RubricVersion)
	}
	wantRubric, err := CriteriaRubricVersion(2, mustMarshalCriteria(v2prefs.RoleCriteria))
	if err != nil {
		t.Fatal(err)
	}
	if v2catalog.RubricVersion != wantRubric {
		t.Fatalf("v2 rubric = %q, want brief formula %q", v2catalog.RubricVersion, wantRubric)
	}
	if current, err := s.CurrentReasonCatalog(ctx); err != nil || current.CatalogVersion != v2catalog.CatalogVersion {
		t.Fatalf("current catalog after v2 authoring: %+v %v", current, err)
	}
	if kept, err := s.ReasonCatalog(ctx, 1); err != nil || kept.CatalogVersion != v1catalog.CatalogVersion {
		t.Fatalf("v1 catalog after v2 authoring: %+v %v", kept, err)
	}
}

// Two runs on one brief version share the one catalog: the second run's
// classifications cite the same catalog version with no re-authoring, and
// each run lists only its own findings.
func TestBriefCatalogTwoRunsShareOneCatalog(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if err := SeedResearchRound(ctx, db, "round-1", "attempt-1"); err != nil {
			return err
		}
		if err := SeedResearchRoundState(ctx, db, "round-2", "attempt-2", "completed"); err != nil {
			return err
		}
		return SeedResearchCompany(ctx, db, "company-1", "Example BV")
	}); err != nil {
		t.Fatal(err)
	}
	opp1, _, err := s.CreateOpportunity(ctx, FixtureActor(), fixtureOpportunity("company-1"))
	if err != nil {
		t.Fatal(err)
	}
	opp2, _, err := s.CreateOpportunity(ctx, FixtureActor(), fixtureOpportunity("company-1"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.AuthorReasonCatalog(ctx, Actor{Kind: "agent", ID: "codex"}, testCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	capture := insertFixtureCapture(t, s, "brief-a2-shared-body")
	assessment1 := insertFindingAssessment(t, s, "jev-1", 0, FixtureSHA256("brief-a2-run1"), catalog)

	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return SeedResearchJevAttempt(ctx, db, "jev-2", "round-2", "attempt-2", 1)
	}); err != nil {
		t.Fatal(err)
	}
	candidates, setHash := findingCandidatesJSON(t, catalog)
	var assessment2 DynamicAssessment
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		assessment2, err = InsertDynamicAssessment(ctx, db, FixtureActor(), DynamicAssessmentInput{
			RoundID: "round-2", JevAttemptID: "jev-2", Purpose: "classification",
			QuestionsJSON: `[{"id":"q-group"}]`, EvidenceRefsJSON: `[{"capture_id":"cap","span_start":0,"span_end":4}]`,
			ProfileVersion: catalog.ProfileVersion, RubricVersion: catalog.RubricVersion,
			CandidatesJSON: candidates, CandidateSetHash: setHash,
			ReuseKey: FixtureSHA256("brief-a2-run2"), Status: DynamicAssessmentSucceeded,
			AnswersJSON: `[{"questionId":"q-group"}]`,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	first, err := s.SaveFinding(ctx, findingSaveInput(findingFixture{
		catalog: catalog, assessment: assessment1, capture: capture, opportunityID: opp1.ID}))
	if err != nil {
		t.Fatal(err)
	}
	secondIn := findingSaveInput(findingFixture{
		catalog: catalog, assessment: assessment2, capture: capture, opportunityID: opp2.ID})
	secondIn.RunID = "round-2"
	secondIn.Group = FindingGroupCouldBeRecommended
	secondIn.EvidenceLinks[0].SpanStart, secondIn.EvidenceLinks[0].SpanEnd = 300, 480
	secondIn.EvidenceLinks[0].ExcerptSHA256 = FixtureSHA256("brief-a2-excerpt-2")
	second, err := s.SaveFinding(ctx, secondIn)
	if err != nil {
		t.Fatal(err)
	}

	if second.CatalogVersion != catalog.CatalogVersion || second.CatalogVersion != first.CatalogVersion ||
		second.RubricVersion != catalog.RubricVersion || second.ProfileVersion != 1 {
		t.Fatalf("run-2 finding bindings: %+v", second)
	}
	if second.ReuseKey == first.ReuseKey {
		t.Fatal("distinct classifications share a reuse key")
	}
	var catalogs, findings int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM reason_catalogs").Scan(&catalogs); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM findings").Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if catalogs != 1 || findings != 2 {
		t.Fatalf("catalogs=%d findings=%d, want 1 catalog and 2 findings", catalogs, findings)
	}
	// No re-authoring: an identical authoring call replays the stored row.
	replay, err := s.AuthorReasonCatalog(ctx, Actor{Kind: "agent", ID: "codex"}, testCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	if replay.CatalogVersion != catalog.CatalogVersion || replay.CreatedAt != catalog.CreatedAt {
		t.Fatalf("catalog replay forked: %+v vs %+v", replay, catalog)
	}

	run1, err := s.ListRunFindings(ctx, "round-1", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	run2, err := s.ListRunFindings(ctx, "round-2", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(run1.Items) != 1 || run1.Items[0].ID != first.ID {
		t.Fatalf("run-1 list: %+v", run1.Items)
	}
	if len(run2.Items) != 1 || run2.Items[0].ID != second.ID {
		t.Fatalf("run-2 list: %+v", run2.Items)
	}
}

// A saved classification carries the pinned brief/rubric/catalog versions
// plus the group and verbatim saved reasons, and every read path returns
// the identical row.
func TestBriefCatalogFindingCarriesVersionsAndReasons(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	fix := seedFindingBase(t, s)
	in := findingSaveInput(fix)
	in.Reasons = append(in.Reasons, FindingReasonInput{
		ReasonID: "pay-unknown", Kind: FindingReasonMissingInformation,
		Label: "Pay unstated", Detail: "Listing states no base pay, so the minimum cannot be checked.",
		JevSupport: 0.62,
	})
	in.Conflict = &ReasonChoice{ID: "oncall-heavy", Label: "Heavy on-call",
		Detail: "Listing requires frequent night or weekend on-call rotations."}
	in.MissingFact = &ReasonChoice{ID: "pay-unknown", Label: "Pay unstated",
		Detail: "Listing states no base pay, so the minimum cannot be checked."}

	saved, err := s.SaveFinding(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ProfileVersion != fix.catalog.ProfileVersion ||
		saved.RubricVersion != fix.catalog.RubricVersion ||
		saved.CatalogVersion != fix.catalog.CatalogVersion ||
		saved.CandidateSetHash != fix.assessment.CandidateSetHash ||
		saved.Group != FindingGroupRecommended || saved.CreatedAt == "" {
		t.Fatalf("saved versions: %+v", saved)
	}
	if len(saved.Reasons) != 2 || saved.Reasons[0].ReasonID != "hybrid-ok" ||
		saved.Reasons[0].Kind != FindingReasonPositive || saved.Reasons[0].JevSupport != 0.81 ||
		saved.Reasons[1].ReasonID != "pay-unknown" || saved.Reasons[1].JevSupport != 0.62 {
		t.Fatalf("saved reasons: %+v", saved.Reasons)
	}

	byOpportunity, err := s.GetOpportunityFinding(ctx, fix.opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListRunFindings(ctx, "round-1", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("run list: %d items", len(page.Items))
	}
	if !reflect.DeepEqual(byOpportunity, saved) {
		t.Fatalf("opportunity read differs from save:\n%+v\n%+v", byOpportunity, saved)
	}
	if !reflect.DeepEqual(page.Items[0], saved) {
		t.Fatalf("run-list read differs from save:\n%+v\n%+v", page.Items[0], saved)
	}
}

// Saving a classification and reading it back commissions nothing: no new
// rounds, attempts, research requests, Jev attempts, checkpoints, job
// checks or catalogs — only the finding row itself.
func TestBriefCatalogClassificationCommissionsNothing(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	fix := seedFindingBase(t, s)
	before := briefCatalogTableCounts(t, s)

	if _, err := s.SaveFinding(ctx, findingSaveInput(fix)); err != nil {
		t.Fatal(err)
	}
	afterSave := briefCatalogTableCounts(t, s)
	briefCatalogRequireCounts(t, before, afterSave, 1)

	for i := 0; i < 2; i++ {
		if _, err := s.GetOpportunityFinding(ctx, fix.opportunityID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListRunFindings(ctx, "round-1", "", "", 10); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListRunFindings(ctx, "round-1", FindingGroupRecommended, "", 10); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReasonCatalog(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CurrentReasonCatalog(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CurrentPreferences(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PreferenceVersion(ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	afterReads := briefCatalogTableCounts(t, s)
	briefCatalogRequireCounts(t, afterSave, afterReads, 0)
}

// Repeated reads preserve every version, date and evidence byte: the same
// finding, catalog and brief rows come back identical every time.
func TestBriefCatalogReadsPreserveExactness(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	fix := seedFindingBase(t, s)
	saved, err := s.SaveFinding(ctx, findingSaveInput(fix))
	if err != nil {
		t.Fatal(err)
	}

	var first Finding
	for i := 0; i < 3; i++ {
		got, err := s.GetOpportunityFinding(ctx, fix.opportunityID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = got
		} else if !reflect.DeepEqual(got, first) {
			t.Fatalf("read %d differs:\n%+v\n%+v", i, got, first)
		}
		page, err := s.ListRunFindings(ctx, "round-1", "", "", 10)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("run list read %d: %+v %v", i, page, err)
		}
		if !reflect.DeepEqual(page.Items[0], first) {
			t.Fatalf("run list read %d differs:\n%+v\n%+v", i, page.Items[0], first)
		}
	}
	if first.CreatedAt != saved.CreatedAt || first.ReuseKey != saved.ReuseKey ||
		first.CatalogVersion != saved.CatalogVersion ||
		!reflect.DeepEqual(first.EvidenceLinks, saved.EvidenceLinks) ||
		!reflect.DeepEqual(first.SourceRef, saved.SourceRef) ||
		!reflect.DeepEqual(first.Reasons, saved.Reasons) {
		t.Fatalf("reads lost exactness:\n%+v\n%+v", first, saved)
	}

	var firstCatalog ReasonCatalog
	for i := 0; i < 3; i++ {
		catalog, err := s.ReasonCatalog(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		current, err := s.CurrentReasonCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstCatalog = catalog
		} else if !reflect.DeepEqual(catalog, firstCatalog) || !reflect.DeepEqual(current, firstCatalog) {
			t.Fatalf("catalog read %d differs", i)
		}
	}
	if firstCatalog.CreatedAt != fix.catalog.CreatedAt ||
		firstCatalog.CatalogVersion != fix.catalog.CatalogVersion {
		t.Fatalf("catalog reads lost exactness: %+v vs %+v", firstCatalog, fix.catalog)
	}

	brief, err := s.PreferenceVersion(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.PreferenceVersion(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(brief, again) || brief.CreatedAt == "" {
		t.Fatalf("brief reads differ or lack dates: %+v %+v", brief, again)
	}
}
