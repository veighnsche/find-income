package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// RW-A2 chain verification at the HTTP boundary: the served brief pins the
// effective saved version, runs pin their commissioning brief pair, one
// catalog serves its brief version, findings render versions/reasons/
// evidence exactly, and reads commission nothing.

func briefCatalogInput(profile int64) store.ReasonCatalogInput {
	return store.ReasonCatalogInput{
		ProfileVersion: profile,
		Rubric:         "Match senior support roles in Amsterdam; hybrid or remote; exclude on-call heavy rotations.",
		Positive: []store.ReasonChoice{
			{ID: "hybrid-ok", Label: "Hybrid friendly", Detail: "Listing offers hybrid or remote work in the owner's timezone."},
			{ID: "support-senior", Label: "Senior support scope", Detail: "Role centers on senior customer-support engineering."},
		},
		Negative: []store.ReasonChoice{
			{ID: "oncall-heavy", Label: "Heavy on-call", Detail: "Listing requires frequent night or weekend on-call rotations."},
		},
		MissingInformation: []store.ReasonChoice{
			{ID: "pay-unknown", Label: "Pay unstated", Detail: "Listing states no base pay, so the minimum cannot be checked."},
		},
	}
}

type briefFindingSeed struct {
	catalog     store.ReasonCatalog
	capture     store.SourceCapture
	assessment  store.DynamicAssessment
	saved       store.Finding
	opportunity store.Opportunity
}

// briefSeedRunFinding commissions the full discovery chain beneath the HTTP
// reads: run, saved role, authored catalog, capture, Jev assessment and one
// classified finding with reasons, conflict, missing fact and provenance.
func briefSeedRunFinding(t *testing.T, h *harness) briefFindingSeed {
	t.Helper()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		if err := store.SeedResearchRound(ctx, db, "round-1", "attempt-1"); err != nil {
			return err
		}
		return store.SeedResearchCompany(ctx, db, "company-1", "Example BV")
	}); err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := h.db.CreateOpportunity(ctx, owner, store.OpportunityInput{
		CompanyID: "company-1", Title: "Backend Engineer", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/1", OriginalText: "Build Go services.",
		Stage: "new", WorkPattern: "hybrid",
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := h.db.AuthorReasonCatalog(ctx, store.Actor{Kind: "agent", ID: "codex"}, briefCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	var capture store.SourceCapture
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		var err error
		capture, err = store.InsertSourceCapture(ctx, db, store.SourceCaptureInput{
			ContentSHA256: store.FixtureSHA256("brief-http-body"), ArtifactRef: "blobs/brief-http-body",
			ByteLength: 128, MediaType: "text/html", OriginalURL: "https://example.com/jobs/42",
			Provenance: researchcontract.ProvenanceFetchedResponse, Completeness: store.CaptureComplete,
			Executor: store.FixtureExecutorIdentity(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SeedResearchJevAttempt(ctx, db, "jev-1", "round-1", "attempt-1", 0)
	}); err != nil {
		t.Fatal(err)
	}
	// The candidate set is the authored catalog itself: same choices, same
	// derivation the store tests use.
	raw, err := json.Marshal(struct {
		Positive []store.ReasonChoice `json:"positive"`
		Negative []store.ReasonChoice `json:"negative"`
		Missing  []store.ReasonChoice `json:"missingInformation"`
	}{catalog.Positive, catalog.Negative, catalog.MissingInformation})
	if err != nil {
		t.Fatal(err)
	}
	setSum := sha256.Sum256(raw)
	var assessment store.DynamicAssessment
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		var err error
		assessment, err = store.InsertDynamicAssessment(ctx, db, store.Actor{Kind: store.FixtureActorKind, ID: store.FixtureActorID}, store.DynamicAssessmentInput{
			RoundID: "round-1", JevAttemptID: "jev-1", Purpose: "classification",
			QuestionsJSON: `[{"id":"q-group"}]`, EvidenceRefsJSON: `[{"capture_id":"cap","span_start":0,"span_end":4}]`,
			ProfileVersion: catalog.ProfileVersion, RubricVersion: catalog.RubricVersion,
			CandidatesJSON: string(raw), CandidateSetHash: hex.EncodeToString(setSum[:]),
			ReuseKey: store.FixtureSHA256("brief-http-reuse-1"), Status: store.DynamicAssessmentSucceeded,
			AnswersJSON: `[{"questionId":"q-group"}]`,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := h.db.SaveFinding(ctx, store.FindingSaveInput{
		RunID: "round-1", OpportunityID: opportunity.ID, OpportunityRevision: 1,
		AssessmentID: assessment.ID, Group: store.FindingGroupRecommended,
		Reasons: []store.FindingReasonInput{{
			ReasonID: "hybrid-ok", Kind: store.FindingReasonPositive,
			Label:      "Hybrid friendly",
			Detail:     "Listing offers hybrid or remote work in the owner's timezone.",
			JevSupport: 0.81,
		}, {
			ReasonID: "pay-unknown", Kind: store.FindingReasonMissingInformation,
			Label: "Pay unstated", Detail: "Listing states no base pay, so the minimum cannot be checked.",
			JevSupport: 0.62,
		}},
		Conflict: &store.ReasonChoice{ID: "oncall-heavy", Label: "Heavy on-call",
			Detail: "Listing requires frequent night or weekend on-call rotations."},
		MissingFact: &store.ReasonChoice{ID: "pay-unknown", Label: "Pay unstated",
			Detail: "Listing states no base pay, so the minimum cannot be checked."},
		EvidenceLinks: []store.FindingEvidenceLinkInput{{
			CaptureID: capture.ID, SpanStart: 12, SpanEnd: 214,
			ExcerptSHA256: store.FixtureSHA256("brief-http-excerpt-1"),
		}},
		SourceRef: &store.FindingSourceRef{SourceID: "source-1",
			SourceRevision: store.FixtureSHA256("brief-http-source-rev-1"),
			ObservedURL:    "https://example.invalid/jobs/research"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return briefFindingSeed{catalog: catalog, capture: capture, assessment: assessment, saved: saved, opportunity: opportunity}
}

func briefTableCounts(t *testing.T, h *harness) map[string]int {
	t.Helper()
	ctx := context.Background()
	out := map[string]int{}
	for _, table := range []string{"rounds", "round_attempts", "research_requests",
		"jev_attempts", "job_checks", "reason_catalogs", "findings"} {
		var n int
		if err := h.db.Read(ctx, func(reader store.Reader) error {
			return reader.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

type briefView struct {
	ProfileVersion int64  `json:"profileVersion"`
	RubricVersion  string `json:"rubricVersion"`
	RubricSource   string `json:"rubricSource"`
	CatalogVersion string `json:"catalogVersion"`
	Facts          []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"facts"`
	Requirements []struct {
		ID             string `json:"id"`
		Label          string `json:"label"`
		Description    string `json:"description"`
		Kind           string `json:"kind"`
		Mode           string `json:"mode"`
		DefinitionHash string `json:"definitionHash"`
	} `json:"requirements"`
}

// The served brief pins the effective saved version: profile, rubric,
// source and requirements match the stored brief exactly, a pending
// correction leaves it untouched, and a completed correction moves it to
// the new version without presenting the stale catalog as current.
func TestBriefCatalogBindingHTTP(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}

	getBrief := func() (briefView, string) {
		t.Helper()
		response := h.request("GET", "/api/v1/research/brief", "", cookie, "", "", "")
		if response.Code != 200 {
			t.Fatalf("brief: %d %s", response.Code, response.Body.String())
		}
		var view briefView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view, response.Body.String()
	}

	prefs, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	criteriaJSON, err := json.Marshal(prefs.RoleCriteria)
	if err != nil {
		t.Fatal(err)
	}
	wantRubric, err := store.CriteriaRubricVersion(prefs.Version, string(criteriaJSON))
	if err != nil {
		t.Fatal(err)
	}
	served, firstBody := getBrief()
	if served.ProfileVersion != prefs.Version || served.RubricVersion != wantRubric ||
		served.RubricSource != "preferences_versions:current:v1" || served.CatalogVersion != "" {
		t.Fatalf("unauthored brief view: %+v", served)
	}
	// The HTTP brief is the same brief discovery commissions against.
	commissionBrief, err := codexservice.CurrentOwnerBrief(ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	if commissionBrief.ProfileVersion != served.ProfileVersion ||
		commissionBrief.RubricVersion != served.RubricVersion ||
		commissionBrief.Source != served.RubricSource {
		t.Fatalf("served %+v != commission brief %+v", served, commissionBrief)
	}
	if len(served.Requirements) != len(prefs.RoleCriteria) {
		t.Fatalf("brief requirements %d, stored %d", len(served.Requirements), len(prefs.RoleCriteria))
	}
	for i, criterion := range prefs.RoleCriteria {
		got := served.Requirements[i]
		if got.ID != criterion.ID || got.Label != criterion.Label ||
			got.Description != criterion.Description || got.Kind != string(criterion.Kind) ||
			got.Mode != string(criterion.Mode) || got.DefinitionHash != criterion.DefinitionHash() {
			t.Fatalf("requirement %d: %+v vs %+v", i, got, criterion)
		}
	}

	v1catalog, err := h.db.AuthorReasonCatalog(ctx, store.Actor{Kind: "agent", ID: "codex"}, briefCatalogInput(1))
	if err != nil {
		t.Fatal(err)
	}
	served, _ = getBrief()
	if served.CatalogVersion != v1catalog.CatalogVersion {
		t.Fatalf("brief catalog = %q, want %q", served.CatalogVersion, v1catalog.CatalogVersion)
	}
	response := h.request("GET", "/api/v1/research/briefs/1/catalog", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("catalog: %d %s", response.Code, response.Body.String())
	}
	var catalogView struct {
		ProfileVersion int64  `json:"profileVersion"`
		RubricVersion  string `json:"rubricVersion"`
		CatalogVersion string `json:"catalogVersion"`
		Positive       []struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Detail string `json:"detail"`
		} `json:"positive"`
		Negative []struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Detail string `json:"detail"`
		} `json:"negative"`
		MissingInformation []struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Detail string `json:"detail"`
		} `json:"missingInformation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &catalogView); err != nil {
		t.Fatal(err)
	}
	if catalogView.CatalogVersion != v1catalog.CatalogVersion ||
		catalogView.RubricVersion != v1catalog.RubricVersion ||
		len(catalogView.Positive) != 2 || len(catalogView.Negative) != 1 ||
		len(catalogView.MissingInformation) != 1 ||
		catalogView.Positive[0].Detail != "Listing offers hybrid or remote work in the owner's timezone." ||
		catalogView.Negative[0].Label != "Heavy on-call" ||
		catalogView.MissingInformation[0].Detail != "Listing states no base pay, so the minimum cannot be checked." {
		t.Fatalf("catalog view: %s", response.Body.String())
	}

	// Pending correction: a commissioned round alone is not the new brief.
	if _, created, err := h.db.StartRound(ctx, owner, store.StartRoundInput{
		RequestKey: "rw-a2-http-pending", Intent: "Apply the owner's correction to the saved search.",
		Outcome: "process_input", ProfileVersion: prefs.Version,
		Scope:    store.RoundScope{Operations: []string{"preferences.correct"}, Resources: []string{"profile:current"}},
		Limits:   store.RoundAllowance{Requests: 4, Items: 2, Tools: 2, Turns: 2},
		Deadline: time.Now().Add(time.Hour).UTC().Round(0),
	}); err != nil || !created {
		t.Fatalf("pending correction round: created=%v err=%v", created, err)
	}
	served, _ = getBrief()
	if served.ProfileVersion != 1 || served.RubricVersion != wantRubric ||
		served.CatalogVersion != v1catalog.CatalogVersion {
		t.Fatalf("brief during pending correction: %+v", served)
	}

	// Completed correction: the served brief rebinds; the stale v1 catalog
	// is retained under its own version but no longer presented as current.
	next := prefs
	next.PreferredLocation = "Rotterdam"
	next.RoleCriteria = append(next.RoleCriteria, store.RoleCriterion{
		ID: "weekend-cover", Label: "Weekend cover", Description: "Occasional weekend cover is acceptable.",
		Kind: "responsibility", Mode: "prefer",
	})
	updated, _, err := h.db.UpdatePreferences(ctx, 1, next, owner)
	if err != nil {
		t.Fatal(err)
	}
	v2criteriaJSON, err := json.Marshal(updated.RoleCriteria)
	if err != nil {
		t.Fatal(err)
	}
	wantRubricV2, err := store.CriteriaRubricVersion(2, string(v2criteriaJSON))
	if err != nil {
		t.Fatal(err)
	}
	served, v2Body := getBrief()
	if served.ProfileVersion != 2 || served.RubricVersion != wantRubricV2 ||
		served.RubricSource != "preferences_versions:current:v2" || served.CatalogVersion != "" {
		t.Fatalf("brief after correction: %+v", served)
	}
	if strings.Contains(v2Body, v1catalog.CatalogVersion) {
		t.Fatalf("corrected brief still presents the stale catalog: %s", v2Body)
	}
	response = h.request("GET", "/api/v1/research/briefs/1/catalog", "", cookie, "", "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), v1catalog.CatalogVersion) {
		t.Fatalf("retained v1 catalog: %d %s", response.Code, response.Body.String())
	}
	response = h.request("GET", "/api/v1/research/briefs/2/catalog", "", cookie, "", "", "")
	if response.Code != 404 {
		t.Fatalf("unauthored v2 catalog: got %d, want honest 404", response.Code)
	}
	v2in := briefCatalogInput(2)
	v2in.Rubric = "Match senior support roles; occasional weekend cover now acceptable."
	v2catalog, err := h.db.AuthorReasonCatalog(ctx, store.Actor{Kind: "agent", ID: "codex"}, v2in)
	if err != nil {
		t.Fatal(err)
	}
	served, _ = getBrief()
	if served.CatalogVersion != v2catalog.CatalogVersion {
		t.Fatalf("brief after v2 authoring: %+v", served)
	}

	// Reads are byte-stable: the same brief renders identically every time.
	_, againBody := getBrief()
	if _, reread := getBrief(); reread != againBody {
		t.Fatalf("brief reads differ:\n%s\n%s", againBody, reread)
	}
	if firstBody == v2Body {
		t.Fatal("corrected brief renders identically to the pre-correction brief")
	}
}

// A run pins the brief pair it was commissioned against: the stored
// checkpoint keeps serving (profile, rubric) after a later correction,
// while the commission-time brief moves on for subsequent discovery.
func TestBriefCatalogRunBindingHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SeedResearchRound(ctx, db, "round-1", "attempt-1")
	}); err != nil {
		t.Fatal(err)
	}
	commissioned, err := codexservice.CurrentOwnerBrief(ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SaveRunCheckpoint(ctx, db, "round-1", researchcontract.Checkpoint{
			ProfileVersion: commissioned.ProfileVersion, RubricVersion: commissioned.RubricVersion,
			Generation: 1, UpdatedAt: time.Now().UTC(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	briefs := codexservice.RunBriefs{DB: h.db}
	profile, rubric, err := briefs.CurrentBrief(ctx, "round-1")
	if err != nil {
		t.Fatal(err)
	}
	if profile != commissioned.ProfileVersion || rubric != commissioned.RubricVersion {
		t.Fatalf("run binding = (%d,%q), commission brief = (%d,%q)",
			profile, rubric, commissioned.ProfileVersion, commissioned.RubricVersion)
	}

	prefs, err := h.db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := prefs
	next.PreferredLocation = "Utrecht"
	if _, _, err := h.db.UpdatePreferences(ctx, prefs.Version, next, owner); err != nil {
		t.Fatal(err)
	}
	profile, rubric, err = briefs.CurrentBrief(ctx, "round-1")
	if err != nil {
		t.Fatal(err)
	}
	if profile != commissioned.ProfileVersion || rubric != commissioned.RubricVersion {
		t.Fatalf("run binding moved with the correction: (%d,%q)", profile, rubric)
	}
	fresh, err := codexservice.CurrentOwnerBrief(ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ProfileVersion != commissioned.ProfileVersion+1 ||
		fresh.Source == commissioned.Source {
		t.Fatalf("subsequent brief did not rebind: %+v vs %+v", fresh, commissioned)
	}
	if _, _, err := briefs.CurrentBrief(ctx, "round-unknown"); err == nil {
		t.Fatal("unknown run resolved a brief")
	}
}

// Finding reads render the pinned versions, group, verbatim reasons and
// exact evidence without commissioning anything; Unknown renders its
// honest basis with an empty reason list.
func TestBriefCatalogFindingsHTTP(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login()
	ctx := context.Background()
	seed := briefSeedRunFinding(t, h)
	before := briefTableCounts(t, h)

	response := h.request("GET", "/api/v1/opportunities/"+seed.opportunity.ID+"/finding", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("finding: %d %s", response.Code, response.Body.String())
	}
	singleBody := response.Body.String()
	var single struct {
		AssessmentID        string `json:"assessmentId"`
		CatalogVersion      string `json:"catalogVersion"`
		RubricVersion       string `json:"rubricVersion"`
		ProfileVersion      int64  `json:"profileVersion"`
		Group               string `json:"group"`
		OpportunityID       string `json:"opportunityId"`
		OpportunityRevision int64  `json:"opportunityRevision"`
		Stale               bool   `json:"stale"`
		Reasons             []struct {
			ReasonID   string  `json:"reasonId"`
			Kind       string  `json:"kind"`
			Label      string  `json:"label"`
			Detail     string  `json:"detail"`
			JevSupport float64 `json:"jevSupport"`
		} `json:"reasons"`
		EvidenceLinks []struct {
			CaptureID     string `json:"captureId"`
			SpanStart     int    `json:"spanStart"`
			SpanEnd       int    `json:"spanEnd"`
			ExcerptSHA256 string `json:"excerptSha256"`
		} `json:"evidenceLinks"`
		Conflict *struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Detail string `json:"detail"`
		} `json:"conflict"`
		MissingFact *struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Detail string `json:"detail"`
		} `json:"missingFact"`
		SourceRef *struct {
			SourceID       string  `json:"sourceId"`
			SourceRevision string  `json:"sourceRevision"`
			ObservedURL    *string `json:"observedUrl"`
		} `json:"sourceRef"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.CatalogVersion != seed.catalog.CatalogVersion ||
		single.RubricVersion != seed.catalog.RubricVersion ||
		single.ProfileVersion != seed.catalog.ProfileVersion ||
		single.AssessmentID != seed.assessment.ID ||
		single.Group != string(store.FindingGroupRecommended) ||
		single.OpportunityID != seed.opportunity.ID || single.OpportunityRevision != 1 ||
		single.Stale {
		t.Fatalf("finding versions: %s", singleBody)
	}
	if len(single.Reasons) != 2 ||
		single.Reasons[0].ReasonID != "hybrid-ok" || single.Reasons[0].Kind != "positive" ||
		single.Reasons[0].Label != "Hybrid friendly" ||
		single.Reasons[0].Detail != "Listing offers hybrid or remote work in the owner's timezone." ||
		single.Reasons[0].JevSupport != 0.81 ||
		single.Reasons[1].ReasonID != "pay-unknown" || single.Reasons[1].Kind != "missing_information" ||
		single.Reasons[1].JevSupport != 0.62 {
		t.Fatalf("finding reasons: %s", singleBody)
	}
	// Wire-exact support decimals: the JSON text carries the authored
	// values with no float narrowing artifacts.
	if !strings.Contains(singleBody, `"jevSupport":0.81`) ||
		!strings.Contains(singleBody, `"jevSupport":0.62`) {
		t.Fatalf("support decimals not wire-exact: %s", singleBody)
	}
	if len(single.EvidenceLinks) != 1 ||
		single.EvidenceLinks[0].CaptureID != seed.capture.ID ||
		single.EvidenceLinks[0].SpanStart != 12 || single.EvidenceLinks[0].SpanEnd != 214 ||
		single.EvidenceLinks[0].ExcerptSHA256 != store.FixtureSHA256("brief-http-excerpt-1") {
		t.Fatalf("finding evidence: %s", singleBody)
	}
	if single.Conflict == nil || single.Conflict.ID != "oncall-heavy" ||
		single.Conflict.Detail != "Listing requires frequent night or weekend on-call rotations." ||
		single.MissingFact == nil || single.MissingFact.ID != "pay-unknown" ||
		single.SourceRef == nil || single.SourceRef.SourceID != "source-1" ||
		single.SourceRef.SourceRevision != store.FixtureSHA256("brief-http-source-rev-1") ||
		single.SourceRef.ObservedURL == nil ||
		*single.SourceRef.ObservedURL != "https://example.invalid/jobs/research" {
		t.Fatalf("finding singletons/provenance: %s", singleBody)
	}
	if strings.Contains(singleBody, "unknownBasis") {
		t.Fatalf("recommended finding carries an unknown basis: %s", singleBody)
	}

	response = h.request("GET", "/api/v1/research/runs/round-1/findings", "", cookie, "", "", "")
	if response.Code != 200 {
		t.Fatalf("run findings: %d %s", response.Code, response.Body.String())
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("run findings: %d items", len(list.Items))
	}
	var singleMap, listMap map[string]any
	if err := json.Unmarshal([]byte(singleBody), &singleMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(list.Items[0], &listMap); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(singleMap, listMap) {
		t.Fatalf("run-list entry differs from single read:\n%s\n%s", list.Items[0], singleBody)
	}
	response = h.request("GET", "/api/v1/research/runs/round-1/findings?group=not_recommended", "", cookie, "", "", "")
	var filtered struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(filtered.Items) != 0 {
		t.Fatalf("group filter: %d %+v", response.Code, filtered)
	}
	response = h.request("GET", "/api/v1/research/runs/round-1/findings?group=recommended", "", cookie, "", "", "")
	if err := json.Unmarshal(response.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(filtered.Items) != 1 {
		t.Fatalf("group match: %d %+v", response.Code, filtered)
	}

	// An unusable-evidence classification renders its honest basis with an
	// empty (never null) reason list.
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	opp2, _, err := h.db.CreateOpportunity(ctx, owner, store.OpportunityInput{
		CompanyID: "company-1", Title: "Night Owl Operator", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/2", OriginalText: "Night shifts.",
		Stage: "new", WorkPattern: "onsite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SeedResearchJevAttempt(ctx, db, "jev-2", "round-1", "attempt-1", 1)
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(struct {
		Positive []store.ReasonChoice `json:"positive"`
		Negative []store.ReasonChoice `json:"negative"`
		Missing  []store.ReasonChoice `json:"missingInformation"`
	}{seed.catalog.Positive, seed.catalog.Negative, seed.catalog.MissingInformation})
	if err != nil {
		t.Fatal(err)
	}
	setSum := sha256.Sum256(raw)
	var assessment2 store.DynamicAssessment
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		var err error
		assessment2, err = store.InsertDynamicAssessment(ctx, db, store.Actor{Kind: store.FixtureActorKind, ID: store.FixtureActorID}, store.DynamicAssessmentInput{
			RoundID: "round-1", JevAttemptID: "jev-2", Purpose: "classification",
			QuestionsJSON: `[{"id":"q-group"}]`, EvidenceRefsJSON: `[{"capture_id":"cap","span_start":0,"span_end":4}]`,
			ProfileVersion: seed.catalog.ProfileVersion, RubricVersion: seed.catalog.RubricVersion,
			CandidatesJSON: string(raw), CandidateSetHash: hex.EncodeToString(setSum[:]),
			ReuseKey: store.FixtureSHA256("brief-http-reuse-2"), Status: store.DynamicAssessmentSucceeded,
			AnswersJSON: `[{"questionId":"q-group"}]`,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SaveFinding(ctx, store.FindingSaveInput{
		RunID: "round-1", OpportunityID: opp2.ID, OpportunityRevision: 1,
		AssessmentID: assessment2.ID, Group: store.FindingGroupUnknown,
		UnknownBasis: "Capture truncated after 40 bytes; no role text survived.",
		EvidenceLinks: []store.FindingEvidenceLinkInput{{
			CaptureID: seed.capture.ID, SpanStart: 300, SpanEnd: 340,
			ExcerptSHA256: store.FixtureSHA256("brief-http-excerpt-2"),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	// The unknown seeding above is explicit store writes (one Jev attempt,
	// one finding), not read side effects; re-snapshot before the reads.
	mid := briefTableCounts(t, h)
	if mid["findings"] != before["findings"]+1 || mid["jev_attempts"] != before["jev_attempts"]+1 {
		t.Fatalf("unknown seeding deltas: %+v vs %+v", mid, before)
	}
	for table, n := range before {
		if table == "findings" || table == "jev_attempts" {
			continue
		}
		if mid[table] != n {
			t.Fatalf("%s rows %d->%d during seeding", table, n, mid[table])
		}
	}
	response = h.request("GET", "/api/v1/opportunities/"+opp2.ID+"/finding", "", cookie, "", "", "")
	unknownBody := response.Body.String()
	if response.Code != 200 || !strings.Contains(unknownBody, `"group":"unknown"`) ||
		!strings.Contains(unknownBody, `"reasons":[]`) ||
		!strings.Contains(unknownBody, "no role text survived") {
		t.Fatalf("unknown finding: %d %s", response.Code, unknownBody)
	}

	// Repeated reads are byte-stable and commission nothing.
	response = h.request("GET", "/api/v1/opportunities/"+seed.opportunity.ID+"/finding", "", cookie, "", "", "")
	if response.Body.String() != singleBody {
		t.Fatalf("finding reads differ:\n%s\n%s", response.Body.String(), singleBody)
	}
	after := briefTableCounts(t, h)
	for table, n := range mid {
		if after[table] != n {
			t.Fatalf("%s rows %d->%d after reads", table, n, after[table])
		}
	}
}
