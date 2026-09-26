package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// findingFixture bundles one classified role: the run, a full opportunity
// (so revision bumps travel the real patch path), the authored catalog, one
// capture and the bound dynamic assessment.
type findingFixture struct {
	catalog       ReasonCatalog
	assessment    DynamicAssessment
	capture       SourceCapture
	opportunityID string
}

// seedFindingBase seeds a run, company, full opportunity, authored catalog,
// capture, jev attempt and dynamic assessment for SaveFinding binding tests.
func seedFindingBase(t *testing.T, s *Store) findingFixture {
	t.Helper()
	ctx := context.Background()
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if err := SeedResearchRound(ctx, db, "round-1", "attempt-1"); err != nil {
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
	catalog, err := s.AuthorReasonCatalog(ctx, Actor{Kind: "agent", ID: "codex"}, testCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	capture := insertFixtureCapture(t, s, "finding-body")
	assessment := insertFindingAssessment(t, s, "jev-1", 0, FixtureSHA256("finding-reuse-1"), catalog)
	return findingFixture{catalog: catalog, assessment: assessment, capture: capture, opportunityID: opp.ID}
}

func findingCandidatesJSON(t *testing.T, catalog ReasonCatalog) (string, string) {
	t.Helper()
	raw, err := json.Marshal(struct {
		Positive []ReasonChoice `json:"positive"`
		Negative []ReasonChoice `json:"negative"`
		Missing  []ReasonChoice `json:"missingInformation"`
	}{catalog.Positive, catalog.Negative, catalog.MissingInformation})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:])
}

func insertFindingAssessment(t *testing.T, s *Store, jevID string, step int64, reuseKey string, catalog ReasonCatalog) DynamicAssessment {
	t.Helper()
	ctx := context.Background()
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return SeedResearchJevAttempt(ctx, db, jevID, "round-1", "attempt-1", step)
	}); err != nil {
		t.Fatal(err)
	}
	candidates, setHash := findingCandidatesJSON(t, catalog)
	var assessment DynamicAssessment
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		assessment, err = InsertDynamicAssessment(ctx, db, FixtureActor(), DynamicAssessmentInput{
			RoundID: "round-1", JevAttemptID: jevID, Purpose: "classification",
			QuestionsJSON: `[{"id":"q-group"}]`, EvidenceRefsJSON: `[{"capture_id":"cap","span_start":0,"span_end":4}]`,
			ProfileVersion: catalog.ProfileVersion, RubricVersion: catalog.RubricVersion,
			CandidatesJSON: candidates, CandidateSetHash: setHash,
			ReuseKey: reuseKey, Status: DynamicAssessmentSucceeded,
			AnswersJSON: `[{"questionId":"q-group"}]`,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return assessment
}

func findingSaveInput(fix findingFixture) FindingSaveInput {
	return FindingSaveInput{
		RunID: "round-1", OpportunityID: fix.opportunityID, OpportunityRevision: 1,
		AssessmentID: fix.assessment.ID, Group: FindingGroupRecommended,
		Reasons: []FindingReasonInput{{
			ReasonID: "hybrid-ok", Kind: FindingReasonPositive,
			Label:      "Hybrid friendly",
			Detail:     "Listing offers hybrid or remote work in the owner's timezone.",
			JevSupport: 0.81,
		}},
		EvidenceLinks: []FindingEvidenceLinkInput{{
			CaptureID: fix.capture.ID, SpanStart: 12, SpanEnd: 214, ExcerptSHA256: FixtureSHA256("excerpt-1"),
		}},
		SourceRef: &FindingSourceRef{SourceID: "source-1",
			SourceRevision: FixtureSHA256("source-rev-1"), ObservedURL: "https://example.invalid/jobs/research"},
	}
}

func TestJevGroupNames(t *testing.T) {
	groups := []string{FindingGroupRecommended, FindingGroupCouldBeRecommended,
		FindingGroupProbablyNotRecommended, FindingGroupNotRecommended, FindingGroupUnknown}
	want := map[string]bool{"recommended": true, "could_be_recommended": true,
		"probably_not_recommended": true, "not_recommended": true, "unknown": true}
	if len(groups) != len(want) {
		t.Fatalf("groups = %v", groups)
	}
	for _, g := range groups {
		if !want[g] {
			t.Fatalf("group %q is not a FindingEntry.group contract value", g)
		}
	}
}

func TestFindingSaveAndReadRoundTrip(t *testing.T) {
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
	in.VacancyRef = "vac-1"

	saved, err := s.SaveFinding(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || len(saved.ReuseKey) != 64 || saved.CreatedAt == "" {
		t.Fatalf("saved identity incomplete: %+v", saved)
	}
	if saved.VacancyRef != "vac-1" {
		t.Fatalf("saved vacancy ref = %q, want vac-1", saved.VacancyRef)
	}
	if saved.RunID != "round-1" || saved.OpportunityID != fix.opportunityID || saved.OpportunityRevision != 1 ||
		saved.AssessmentID != fix.assessment.ID || saved.ProfileVersion != fix.catalog.ProfileVersion ||
		saved.RubricVersion != fix.catalog.RubricVersion || saved.CatalogVersion != fix.catalog.CatalogVersion ||
		saved.CandidateSetHash != fix.assessment.CandidateSetHash || saved.Group != FindingGroupRecommended {
		t.Fatalf("saved bindings wrong: %+v", saved)
	}
	if len(saved.Reasons) != 2 || saved.Reasons[0].JevSupport != 0.81 || saved.Reasons[1].JevSupport != 0.62 {
		t.Fatalf("saved reasons wrong: %+v", saved.Reasons)
	}
	if saved.Conflict == nil || saved.Conflict.ID != "oncall-heavy" ||
		saved.MissingFact == nil || saved.MissingFact.ID != "pay-unknown" {
		t.Fatalf("saved singletons wrong: %+v %+v", saved.Conflict, saved.MissingFact)
	}
	if len(saved.EvidenceLinks) != 1 || saved.EvidenceLinks[0].CaptureID != fix.capture.ID ||
		saved.SourceRef == nil || saved.SourceRef.SourceID != "source-1" {
		t.Fatalf("saved provenance wrong: %+v %+v", saved.EvidenceLinks, saved.SourceRef)
	}
	if saved.Stale || saved.StaleBasis != "" {
		t.Fatalf("fresh save flagged stale: %+v", saved)
	}

	got, err := s.GetOpportunityFinding(ctx, fix.opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != saved.ID || got.ReuseKey != saved.ReuseKey || got.CreatedAt != saved.CreatedAt ||
		got.CatalogVersion != saved.CatalogVersion || len(got.Reasons) != 2 || got.Stale {
		t.Fatalf("read mismatch: %+v vs %+v", got, saved)
	}
	if got.VacancyRef != "vac-1" {
		t.Fatalf("read vacancy ref = %q, want vac-1", got.VacancyRef)
	}
	// Verbatim text: catalog wording survives the JSON round trip byte-for-byte.
	if got.Reasons[0].Label != "Hybrid friendly" ||
		got.Reasons[0].Detail != "Listing offers hybrid or remote work in the owner's timezone." ||
		got.Reasons[1].Detail != "Listing states no base pay, so the minimum cannot be checked." ||
		got.Conflict.Detail != "Listing requires frequent night or weekend on-call rotations." {
		t.Fatalf("reason text not verbatim: %+v", got.Reasons)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM job_checks").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("saving a finding started %d job checks", count)
	}
}

func TestFindingReasonTextVerbatim(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if err := SeedResearchRound(ctx, db, "round-1", "attempt-1"); err != nil {
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
	tricky := "Line one.\nLine \"two\" — déjà vu, naïve façade (100%)."
	in := testCatalogInput(1)
	in.Positive[0].Detail = tricky
	catalog, err := s.AuthorReasonCatalog(ctx, Actor{Kind: "agent", ID: "codex"}, in)
	if err != nil {
		t.Fatal(err)
	}
	capture := insertFixtureCapture(t, s, "verbatim-body")
	assessment := insertFindingAssessment(t, s, "jev-1", 0, FixtureSHA256("verbatim-reuse-1"), catalog)
	saved, err := s.SaveFinding(ctx, FindingSaveInput{
		RunID: "round-1", OpportunityID: opp.ID, OpportunityRevision: 1,
		AssessmentID: assessment.ID, Group: FindingGroupCouldBeRecommended,
		Reasons: []FindingReasonInput{{ReasonID: "hybrid-ok", Kind: FindingReasonPositive,
			Label: "Hybrid friendly", Detail: tricky, JevSupport: 0.5}},
		EvidenceLinks: []FindingEvidenceLinkInput{{
			CaptureID: capture.ID, SpanStart: 0, SpanEnd: 9, ExcerptSHA256: FixtureSHA256("excerpt-v"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Reasons[0].Detail != tricky {
		t.Fatalf("detail = %q, want %q", saved.Reasons[0].Detail, tricky)
	}
	got, err := s.GetOpportunityFinding(ctx, opp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reasons[0].Detail != tricky {
		t.Fatalf("reread detail = %q, want %q", got.Reasons[0].Detail, tricky)
	}
}

func TestJevGroupUnknownBasisRule(t *testing.T) {
	groups := []string{FindingGroupRecommended, FindingGroupCouldBeRecommended,
		FindingGroupProbablyNotRecommended, FindingGroupNotRecommended}
	for _, group := range groups {
		t.Run(group, func(t *testing.T) {
			ctx := context.Background()
			s := openResearchTestDB(t)
			fix := seedFindingBase(t, s)
			in := findingSaveInput(fix)
			in.Group = group
			saved, err := s.SaveFinding(ctx, in)
			if err != nil {
				t.Fatalf("save %s: %v", group, err)
			}
			if saved.Group != group || saved.UnknownBasis != "" {
				t.Fatalf("group row = %+v", saved)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		ctx := context.Background()
		s := openResearchTestDB(t)
		fix := seedFindingBase(t, s)
		in := findingSaveInput(fix)
		in.Group = FindingGroupUnknown
		in.UnknownBasis = "Capture truncated after 40 bytes; no role text survived."
		in.Reasons = nil
		in.Conflict, in.MissingFact, in.SourceRef = nil, nil, nil
		saved, err := s.SaveFinding(ctx, in)
		if err != nil {
			t.Fatalf("save unknown: %v", err)
		}
		if saved.Group != FindingGroupUnknown || saved.UnknownBasis == "" || len(saved.Reasons) != 0 {
			t.Fatalf("unknown row = %+v", saved)
		}
	})

	violations := map[string]func(*FindingSaveInput){
		"unknown without basis": func(in *FindingSaveInput) {
			in.Group, in.Reasons, in.UnknownBasis = FindingGroupUnknown, nil, ""
		},
		"known group with basis": func(in *FindingSaveInput) {
			in.UnknownBasis = "stray basis"
		},
		"unknown with reasons": func(in *FindingSaveInput) {
			in.Group, in.UnknownBasis = FindingGroupUnknown, "unusable"
		},
		"unknown with conflict": func(in *FindingSaveInput) {
			in.Group, in.Reasons, in.UnknownBasis = FindingGroupUnknown, nil, "unusable"
			in.Conflict = &ReasonChoice{ID: "oncall-heavy", Label: "Heavy on-call",
				Detail: "Listing requires frequent night or weekend on-call rotations."}
		},
		"unknown with missing": func(in *FindingSaveInput) {
			in.Group, in.Reasons, in.UnknownBasis = FindingGroupUnknown, nil, "unusable"
			in.MissingFact = &ReasonChoice{ID: "pay-unknown", Label: "Pay unstated",
				Detail: "Listing states no base pay, so the minimum cannot be checked."}
		},
		"bad group":       func(in *FindingSaveInput) { in.Group = "maybe" },
		"bad vacancy ref": func(in *FindingSaveInput) { in.VacancyRef = " padded " },
	}
	for name, mutate := range violations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := openResearchTestDB(t)
			fix := seedFindingBase(t, s)
			in := findingSaveInput(fix)
			mutate(&in)
			if _, err := s.SaveFinding(ctx, in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s: %v, want ErrInvalid", name, err)
			}
		})
	}
}

func TestFindingReasonCatalogBinding(t *testing.T) {
	mutateCases := map[string]func(*FindingSaveInput){
		"unknown reason id": func(in *FindingSaveInput) { in.Reasons[0].ReasonID = "nope" },
		"kind mismatch":     func(in *FindingSaveInput) { in.Reasons[0].Kind = FindingReasonNegative },
		"tampered label":    func(in *FindingSaveInput) { in.Reasons[0].Label += "!" },
		"tampered detail":   func(in *FindingSaveInput) { in.Reasons[0].Detail += " " },
		"too many reasons": func(in *FindingSaveInput) {
			in.Reasons = []FindingReasonInput{in.Reasons[0], in.Reasons[0], in.Reasons[0], in.Reasons[0]}
		},
		"duplicate reasons": func(in *FindingSaveInput) {
			in.Reasons = []FindingReasonInput{in.Reasons[0], in.Reasons[0]}
		},
		"conflict unknown id": func(in *FindingSaveInput) {
			in.Conflict = &ReasonChoice{ID: "nope", Label: "x", Detail: "y"}
		},
		"conflict tampered": func(in *FindingSaveInput) {
			in.Conflict = &ReasonChoice{ID: "oncall-heavy", Label: "Heavy on-call", Detail: "rewritten"}
		},
		"missing wrong kind": func(in *FindingSaveInput) {
			in.MissingFact = &ReasonChoice{ID: "hybrid-ok", Label: "Hybrid friendly",
				Detail: "Listing offers hybrid or remote work in the owner's timezone."}
		},
		"missing tampered": func(in *FindingSaveInput) {
			in.MissingFact = &ReasonChoice{ID: "pay-unknown", Label: "Pay unstated", Detail: "rewritten"}
		},
	}
	for name, mutate := range mutateCases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := openResearchTestDB(t)
			fix := seedFindingBase(t, s)
			in := findingSaveInput(fix)
			mutate(&in)
			if _, err := s.SaveFinding(ctx, in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s: %v, want ErrInvalid", name, err)
			}
		})
	}

	t.Run("rubric drift conflicts", func(t *testing.T) {
		ctx := context.Background()
		s := openResearchTestDB(t)
		if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
			if err := SeedResearchRound(ctx, db, "round-1", "attempt-1"); err != nil {
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
		catalog, err := s.AuthorReasonCatalog(ctx, Actor{Kind: "agent", ID: "codex"}, testCatalogInput(1))
		if err != nil {
			t.Fatal(err)
		}
		capture := insertFixtureCapture(t, s, "drift-body")
		if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
			return SeedResearchJevAttempt(ctx, db, "jev-1", "round-1", "attempt-1", 0)
		}); err != nil {
			t.Fatal(err)
		}
		candidates, setHash := findingCandidatesJSON(t, catalog)
		var assessment DynamicAssessment
		if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
			var err error
			assessment, err = InsertDynamicAssessment(ctx, db, FixtureActor(), DynamicAssessmentInput{
				RoundID: "round-1", JevAttemptID: "jev-1", Purpose: "classification",
				QuestionsJSON: `[{"id":"q-group"}]`, EvidenceRefsJSON: `[{"capture_id":"cap","span_start":0,"span_end":4}]`,
				ProfileVersion: 1, RubricVersion: "criteria-v1-driftedrubric",
				CandidatesJSON: candidates, CandidateSetHash: setHash,
				ReuseKey: FixtureSHA256("drift-reuse-1"), Status: DynamicAssessmentSucceeded,
				AnswersJSON: `[{"questionId":"q-group"}]`,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		in := findingSaveInput(findingFixture{catalog: catalog, assessment: assessment, capture: capture, opportunityID: opp.ID})
		if _, err := s.SaveFinding(ctx, in); !errors.Is(err, ErrConflict) {
			t.Fatalf("rubric drift: %v, want ErrConflict", err)
		}
	})

	t.Run("missing assessment", func(t *testing.T) {
		ctx := context.Background()
		s := openResearchTestDB(t)
		fix := seedFindingBase(t, s)
		fix.assessment.ID = "missing"
		in := findingSaveInput(fix)
		if _, err := s.SaveFinding(ctx, in); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing assessment: %v, want ErrNotFound", err)
		}
	})

	t.Run("stale opportunity revision conflicts", func(t *testing.T) {
		ctx := context.Background()
		s := openResearchTestDB(t)
		fix := seedFindingBase(t, s)
		in := findingSaveInput(fix)
		in.OpportunityRevision = 99
		if _, err := s.SaveFinding(ctx, in); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale revision: %v, want ErrConflict", err)
		}
	})
}

func TestFindingEvidenceAcceptsContentSHA(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	fix := seedFindingBase(t, s)
	in := findingSaveInput(fix)
	// Production receipts bind the content sha256, not the retrieval row
	// id; both forms must validate.
	in.EvidenceLinks[0].CaptureID = fix.capture.ContentSHA256
	saved, err := s.SaveFinding(ctx, in)
	if err != nil {
		t.Fatalf("sha-form evidence: %v", err)
	}
	if len(saved.EvidenceLinks) != 1 || saved.EvidenceLinks[0].CaptureID != fix.capture.ContentSHA256 {
		t.Fatalf("links = %+v, want the sha-form link stored as given", saved.EvidenceLinks)
	}
}

func TestFindingEvidenceValidation(t *testing.T) {
	cases := map[string]func(*FindingSaveInput){
		"no links": func(in *FindingSaveInput) { in.EvidenceLinks = nil },
		"unknown capture": func(in *FindingSaveInput) {
			in.EvidenceLinks[0].CaptureID = "missing"
		},
		"negative start": func(in *FindingSaveInput) { in.EvidenceLinks[0].SpanStart = -1 },
		"empty span": func(in *FindingSaveInput) {
			in.EvidenceLinks[0].SpanStart, in.EvidenceLinks[0].SpanEnd = 9, 9
		},
		"short hash": func(in *FindingSaveInput) { in.EvidenceLinks[0].ExcerptSHA256 = "abc" },
		"nonhex hash": func(in *FindingSaveInput) {
			in.EvidenceLinks[0].ExcerptSHA256 = strings.Repeat("z", 64)
		},
		"duplicate links": func(in *FindingSaveInput) {
			in.EvidenceLinks = []FindingEvidenceLinkInput{in.EvidenceLinks[0], in.EvidenceLinks[0]}
		},
		"source without revision": func(in *FindingSaveInput) { in.SourceRef.SourceRevision = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := openResearchTestDB(t)
			fix := seedFindingBase(t, s)
			in := findingSaveInput(fix)
			mutate(&in)
			if _, err := s.SaveFinding(ctx, in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s: %v, want ErrInvalid", name, err)
			}
		})
	}
}

func TestFindingReuseKeyDedup(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	fix := seedFindingBase(t, s)
	in := findingSaveInput(fix)

	first, err := s.SaveFinding(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.SaveFinding(ctx, in)
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if replay.ID != first.ID || replay.CreatedAt != first.CreatedAt || replay.ReuseKey != first.ReuseKey {
		t.Fatalf("replay forked the finding: %+v vs %+v", replay, first)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM findings").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("findings rows = %d, want exactly 1", count)
	}

	changed := in
	changed.Group = FindingGroupNotRecommended
	if _, err := s.SaveFinding(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same evidence new group: %v, want ErrConflict", err)
	}
	changed = in
	changed.Reasons = []FindingReasonInput{in.Reasons[0]}
	changed.Reasons[0].JevSupport = 0.12
	if _, err := s.SaveFinding(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same evidence new support: %v, want ErrConflict", err)
	}

	links := []FindingEvidenceLink{{CaptureID: fix.capture.ID, SpanStart: 12, SpanEnd: 214,
		ExcerptSHA256: FixtureSHA256("excerpt-1")}}
	keyA, err := FindingReuseKey("round-1", fix.opportunityID, 1, 1, fix.catalog.CatalogVersion, fix.assessment.CandidateSetHash, links)
	if err != nil {
		t.Fatal(err)
	}
	if keyA != first.ReuseKey {
		t.Fatalf("derived key %q != stored %q", keyA, first.ReuseKey)
	}
	keyB, err := FindingReuseKey("round-1", fix.opportunityID, 1, 1, fix.catalog.CatalogVersion, fix.assessment.CandidateSetHash,
		[]FindingEvidenceLink{{CaptureID: fix.capture.ID, SpanStart: 12, SpanEnd: 215, ExcerptSHA256: FixtureSHA256("excerpt-1")}})
	if err != nil {
		t.Fatal(err)
	}
	if keyA == keyB {
		t.Fatal("changed evidence kept the reuse key")
	}
	if _, err := FindingReuseKey("round-1", fix.opportunityID, 1, 1, fix.catalog.CatalogVersion, "short", links); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short candidate hash: %v, want ErrInvalid", err)
	}
}

func TestFindingStaleOnVersionDrift(t *testing.T) {
	t.Run("brief change", func(t *testing.T) {
		ctx := context.Background()
		s := openResearchTestDB(t)
		fix := seedFindingBase(t, s)
		if _, err := s.SaveFinding(ctx, findingSaveInput(fix)); err != nil {
			t.Fatal(err)
		}
		prefs, err := s.CurrentPreferences(ctx)
		if err != nil {
			t.Fatal(err)
		}
		next := prefs
		next.PreferredLocation = "Rotterdam"
		if _, _, err := s.UpdatePreferences(ctx, prefs.Version, next, FixtureActor()); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOpportunityFinding(ctx, fix.opportunityID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Stale || got.StaleBasis != FindingStaleBriefChanged {
			t.Fatalf("brief drift stale=%v basis=%q", got.Stale, got.StaleBasis)
		}
	})

	t.Run("opportunity revision", func(t *testing.T) {
		ctx := context.Background()
		s := openResearchTestDB(t)
		fix := seedFindingBase(t, s)
		if _, err := s.SaveFinding(ctx, findingSaveInput(fix)); err != nil {
			t.Fatal(err)
		}
		notes := "Owner note after classification"
		if _, _, err := s.PatchOpportunity(ctx, FixtureActor(), fix.opportunityID,
			OpportunityPatch{ExpectedRevision: 1, Notes: &notes}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOpportunityFinding(ctx, fix.opportunityID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Stale || got.StaleBasis != FindingStaleOpportunityRevised {
			t.Fatalf("revision drift stale=%v basis=%q", got.Stale, got.StaleBasis)
		}
	})

	t.Run("combined drift", func(t *testing.T) {
		ctx := context.Background()
		s := openResearchTestDB(t)
		fix := seedFindingBase(t, s)
		if _, err := s.SaveFinding(ctx, findingSaveInput(fix)); err != nil {
			t.Fatal(err)
		}
		prefs, err := s.CurrentPreferences(ctx)
		if err != nil {
			t.Fatal(err)
		}
		next := prefs
		next.PreferredLocation = "Utrecht"
		if _, _, err := s.UpdatePreferences(ctx, prefs.Version, next, FixtureActor()); err != nil {
			t.Fatal(err)
		}
		notes := "Owner note after classification"
		if _, _, err := s.PatchOpportunity(ctx, FixtureActor(), fix.opportunityID,
			OpportunityPatch{ExpectedRevision: 1, Notes: &notes}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOpportunityFinding(ctx, fix.opportunityID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Stale || got.StaleBasis != FindingStaleBriefChanged+","+FindingStaleOpportunityRevised {
			t.Fatalf("combined drift stale=%v basis=%q", got.Stale, got.StaleBasis)
		}
	})
}

func TestFindingListRunFindings(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	fix := seedFindingBase(t, s)
	// D2: a genuinely different vacancy needs its own source URL; the same
	// URL would reconcile to the seeded opportunity instead of forking.
	opp2Input := fixtureOpportunity("company-1")
	opp2Input.SourceURL = "https://harbour.example/jobs/2"
	opp2, _, err := s.CreateOpportunity(ctx, FixtureActor(), opp2Input)
	if err != nil {
		t.Fatal(err)
	}
	assessment2 := insertFindingAssessment(t, s, "jev-2", 1, FixtureSHA256("finding-reuse-2"), fix.catalog)

	in1 := findingSaveInput(fix)
	if _, err := s.SaveFinding(ctx, in1); err != nil {
		t.Fatal(err)
	}
	in2 := findingSaveInput(fix)
	in2.AssessmentID = assessment2.ID
	in2.OpportunityID = opp2.ID
	in2.Group = FindingGroupNotRecommended
	if _, err := s.SaveFinding(ctx, in2); err != nil {
		t.Fatal(err)
	}
	firstID, secondID := fix.opportunityID, opp2.ID
	if secondID < firstID {
		firstID, secondID = secondID, firstID
	}

	page, err := s.ListRunFindings(ctx, "round-1", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor != "" {
		t.Fatalf("unfiltered list: %d items cursor %q", len(page.Items), page.NextCursor)
	}
	if page.Items[0].OpportunityID != firstID || page.Items[1].OpportunityID != secondID {
		t.Fatalf("list order wrong: %q %q", page.Items[0].OpportunityID, page.Items[1].OpportunityID)
	}

	filtered, err := s.ListRunFindings(ctx, "round-1", FindingGroupNotRecommended, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].OpportunityID != opp2.ID {
		t.Fatalf("group filter: %+v", filtered.Items)
	}

	first, err := s.ListRunFindings(ctx, "round-1", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor != firstID {
		t.Fatalf("first page: %+v cursor %q", first.Items, first.NextCursor)
	}
	second, err := s.ListRunFindings(ctx, "round-1", "", first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].OpportunityID != secondID {
		t.Fatalf("second page: %+v", second.Items)
	}

	// A re-classification after an opportunity revision supersedes the older
	// row in the per-run list without mutating it.
	notes := "Owner note"
	if _, _, err := s.PatchOpportunity(ctx, FixtureActor(), fix.opportunityID,
		OpportunityPatch{ExpectedRevision: 1, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	assessment3 := insertFindingAssessment(t, s, "jev-3", 2, FixtureSHA256("finding-reuse-3"), fix.catalog)
	in3 := findingSaveInput(fix)
	in3.AssessmentID = assessment3.ID
	in3.OpportunityRevision = 2
	in3.Group = FindingGroupCouldBeRecommended
	if _, err := s.SaveFinding(ctx, in3); err != nil {
		t.Fatal(err)
	}
	latest, err := s.ListRunFindings(ctx, "round-1", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Items) != 2 {
		t.Fatalf("latest-per-opportunity list: %d items", len(latest.Items))
	}
	var current *Finding
	for i := range latest.Items {
		if latest.Items[i].OpportunityID == fix.opportunityID {
			current = &latest.Items[i]
		}
	}
	if current == nil || current.OpportunityRevision != 2 ||
		current.Group != FindingGroupCouldBeRecommended || current.Stale {
		t.Fatalf("latest-per-opportunity list: %+v", latest.Items)
	}

	if _, err := s.ListRunFindings(ctx, "missing", "", "", 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run: %v, want ErrNotFound", err)
	}
	if _, err := s.ListRunFindings(ctx, "round-1", "maybe", "", 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad group filter: %v, want ErrInvalid", err)
	}
	if _, err := s.ListRunFindings(ctx, "round-1", "", "", -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative limit: %v, want ErrInvalid", err)
	}
}

func TestFindingReadsSideEffectFree(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	fix := seedFindingBase(t, s)
	if _, err := s.SaveFinding(ctx, findingSaveInput(fix)); err != nil {
		t.Fatal(err)
	}
	counts := func() (findings, audits, checks int) {
		t.Helper()
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM findings").Scan(&findings); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_changes").Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM job_checks").Scan(&checks); err != nil {
			t.Fatal(err)
		}
		return findings, audits, checks
	}
	beforeFindings, beforeAudits, beforeChecks := counts()
	for i := 0; i < 3; i++ {
		if _, err := s.GetOpportunityFinding(ctx, fix.opportunityID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListRunFindings(ctx, "round-1", "", "", 10); err != nil {
			t.Fatal(err)
		}
	}
	afterFindings, afterAudits, afterChecks := counts()
	if afterFindings != beforeFindings || afterAudits != beforeAudits || afterChecks != beforeChecks {
		t.Fatalf("reads wrote state: findings %d->%d audits %d->%d checks %d->%d",
			beforeFindings, afterFindings, beforeAudits, afterAudits, beforeChecks, afterChecks)
	}
	if _, err := s.GetOpportunityFinding(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing opportunity: %v, want ErrNotFound", err)
	}
	if _, err := s.GetOpportunityFinding(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty opportunity: %v, want ErrInvalid", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE findings SET group_name='unknown' WHERE opportunity_id=?`, fix.opportunityID); err == nil {
		t.Fatal("finding update accepted")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM findings WHERE opportunity_id=?`, fix.opportunityID); err == nil {
		t.Fatal("finding delete accepted")
	}
}
