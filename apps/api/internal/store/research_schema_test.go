package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

func openResearchTestDB(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedResearchBase(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if err := SeedResearchRound(ctx, db, "round-1", "attempt-1"); err != nil {
			return err
		}
		if err := SeedResearchCompany(ctx, db, "company-1", "Example BV"); err != nil {
			return err
		}
		return SeedResearchOpportunity(ctx, db, "opp-1", "company-1", "Support Engineer")
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertFixtureCapture(t *testing.T, s *Store, seed string) SourceCapture {
	t.Helper()
	ctx := context.Background()
	var out SourceCapture
	err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		exec := FixtureExecutorIdentity()
		out, err = InsertSourceCapture(ctx, db, SourceCaptureInput{
			ContentSHA256: FixtureSHA256(seed),
			ArtifactRef:   "blobs/" + seed,
			ByteLength:    128,
			MediaType:     "text/html",
			OriginalURL:   "https://example.com/jobs/42",
			Provenance:    researchcontract.ProvenanceFetchedResponse,
			Completeness:  CaptureComplete,
			Executor:      exec,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestResearchSchemaFreshInit(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	tables := []string{"research_requests", "research_observations", "research_notes",
		"source_captures", "identity_decisions", "entity_identity_keys",
		"jev_assessments_dynamic", "jev_assessment_captures", "record_sightings",
		"run_events", "run_checkpoints", "research_notes_fts", "opportunities_fts"}
	for _, table := range tables {
		var name string
		if err := s.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE name=?`, table).Scan(&name); err != nil {
			t.Fatalf("missing %s: %v", table, err)
		}
	}
	triggers := []string{"source_captures_no_update", "source_captures_no_delete",
		"research_observations_no_update", "research_observations_no_delete",
		"jev_assessments_dynamic_no_update", "jev_assessments_dynamic_no_delete",
		"jev_assessment_captures_no_update", "jev_assessment_captures_no_delete",
		"record_sightings_no_update", "record_sightings_no_delete",
		"run_events_no_update", "run_events_no_delete",
		"research_notes_fts_insert", "research_notes_fts_update", "research_notes_fts_delete",
		"opportunities_fts_insert", "opportunities_fts_update", "opportunities_fts_delete"}
	for _, trigger := range triggers {
		var name string
		if err := s.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&name); err != nil {
			t.Fatalf("missing trigger %s: %v", trigger, err)
		}
	}
	indexes := []string{"research_requests_live_claim_idx", "entity_identity_strong_unique",
		"companies_name_idx", "opportunities_title_idx",
		"source_captures_content_idx", "research_observations_capture_idx",
		"jev_assessment_captures_by_capture", "run_events_round_idx"}
	for _, index := range indexes {
		var name string
		if err := s.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&name); err != nil {
			t.Fatalf("missing index %s: %v", index, err)
		}
	}
	var fts string
	if err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name='opportunities_fts'`).Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fts, "unicode61") {
		t.Fatalf("opportunities FTS tokenizer: %s", fts)
	}
}

func TestResearchRequestLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()

	var req ResearchRequest
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		req, err = CreateResearchRequest(ctx, db, actor,
			FixtureRequestDescriptor(), researchcontract.CacheStatelessReusable)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(req.Fingerprint) != 64 || req.State != ResearchStateFree || req.Lease != nil {
		t.Fatalf("bad created request: %+v", req)
	}
	want, err := FixtureRequestDescriptor().Fingerprint()
	if err != nil || want != req.Fingerprint {
		t.Fatalf("fingerprint not from contract: %q %v", req.Fingerprint, err)
	}
	got, err := GetResearchRequest(ctx, s.db, actor.Kind, actor.ID, req.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != req.ID || got.Request.URLOrQuery != "https://example.com/jobs/42" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if _, err := GetResearchRequest(ctx, s.db, actor.Kind, actor.ID, FixtureSHA256("nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing request: %v", err)
	}
	// Duplicate (actor, fingerprint) rejected.
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := CreateResearchRequest(ctx, db, actor,
			FixtureRequestDescriptor(), researchcontract.CacheStatelessReusable)
		return err
	}); err == nil {
		t.Fatal("duplicate fingerprint accepted")
	}
	// Secret-bearing descriptors rejected by the contract fingerprint.
	bad := FixtureRequestDescriptor()
	bad.Params = []researchcontract.Param{{Name: "api_key", Value: "x"}}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := CreateResearchRequest(ctx, db, actor, bad, researchcontract.CacheStatelessReusable)
		return err
	}); err == nil {
		t.Fatal("secret-bearing descriptor accepted")
	}
	// Claimed without a lease rejected (CHECK); proper claim sticks.
	claimed := ResearchRequestMutation{State: ResearchStateClaimed}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := UpdateResearchRequest(ctx, db, req.ID, claimed)
		return err
	}); err == nil {
		t.Fatal("leaseless claim accepted")
	}
	claimed.Lease = &ResearchLease{Owner: "attempt-1", Generation: 1, Until: "2026-09-24T01:00:00Z"}
	var updated ResearchRequest
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		updated, err = UpdateResearchRequest(ctx, db, req.ID, claimed)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if updated.State != ResearchStateClaimed || updated.Lease.Owner != "attempt-1" {
		t.Fatalf("claim not stored: %+v", updated)
	}
	// Fresh state must drop the lease.
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := UpdateResearchRequest(ctx, db, req.ID, ResearchRequestMutation{
			State: ResearchStateFresh, Lease: &ResearchLease{Owner: "x", Generation: 1, Until: "y"},
		})
		return err
	}); err == nil {
		t.Fatal("fresh-with-lease accepted")
	}
	// Expired-claim sweep finds it.
	expired, err := ListExpiredClaims(ctx, s.db, "2026-09-24T02:00:00Z", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != req.ID {
		t.Fatalf("expiry sweep: %+v", expired)
	}
	// Bad cache scope rejected.
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := CreateResearchRequest(ctx, db, actor, FixtureRequestDescriptor(), "nope")
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cache scope: %v", err)
	}
}

func TestResearchObservationsImmutable(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()
	cap := insertFixtureCapture(t, s, "obs-body")
	var req ResearchRequest
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		req, err = CreateResearchRequest(ctx, db, actor,
			FixtureRequestDescriptor(), researchcontract.CacheStatelessReusable)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	insert := func(in ResearchObservationInput) (ResearchObservation, error) {
		var out ResearchObservation
		err := s.ResearchWrite(ctx, func(db ResearchDB) error {
			var err error
			out, err = InsertResearchObservation(ctx, db, actor, in)
			return err
		})
		return out, err
	}
	exec := FixtureExecutorIdentity()
	obs, err := insert(ResearchObservationInput{
		RequestID: req.ID, AttemptNo: 1, RoundID: "round-1", RoundAttemptID: "attempt-1",
		Operation: researchcontract.OperationFetch, ActualURLOrQuery: "https://example.com/jobs/42",
		Outcome: ObservationSuccess, Provenance: researchcontract.ProvenanceFetchedResponse,
		ReceiptRef: "receipt-1", Executor: &exec, CaptureID: cap.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Executor == nil || obs.Executor.Backend != "fixture-backend" {
		t.Fatalf("executor not stored: %+v", obs)
	}
	if _, err := insert(ResearchObservationInput{
		RequestID: req.ID, AttemptNo: 1, RoundID: "round-1",
		Operation: researchcontract.OperationFetch, ActualURLOrQuery: "https://example.com/x",
		Outcome: ObservationEmpty, Provenance: researchcontract.ProvenanceFetchedResponse,
	}); err == nil {
		t.Fatal("duplicate (request, attempt_no) accepted")
	}
	if _, err := insert(ResearchObservationInput{
		RequestID: req.ID, AttemptNo: 2, RoundID: "round-1",
		Operation: researchcontract.OperationFetch, ActualURLOrQuery: "https://example.com/x",
		Outcome: ObservationSuccess, Provenance: researchcontract.ProvenanceFetchedResponse,
	}); err == nil {
		t.Fatal("content outcome without capture accepted")
	}
	if _, err := insert(ResearchObservationInput{
		RequestID: req.ID, AttemptNo: 3, RoundID: "round-1",
		Operation: researchcontract.OperationFetch, ActualURLOrQuery: "https://example.com/x",
		Outcome: ObservationEmpty, Provenance: researchcontract.ProvenanceFetchedResponse,
	}); err != nil {
		t.Fatalf("non-content outcome without capture rejected: %v", err)
	}
	if _, err := insert(ResearchObservationInput{
		RequestID: req.ID, AttemptNo: 4, RoundID: "round-1",
		Operation: researchcontract.OperationFetch, ActualURLOrQuery: "https://example.com/x",
		Outcome: "bogus", Provenance: researchcontract.ProvenanceFetchedResponse,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad outcome: %v", err)
	}
	if _, err := insert(ResearchObservationInput{
		RequestID: "missing", AttemptNo: 5, RoundID: "round-1",
		Operation: researchcontract.OperationFetch, ActualURLOrQuery: "https://example.com/x",
		Outcome: ObservationEmpty, Provenance: researchcontract.ProvenanceFetchedResponse,
	}); err == nil {
		t.Fatal("missing request FK accepted")
	}
	if _, err := insert(ResearchObservationInput{
		RequestID: req.ID, AttemptNo: 5, RoundID: "round-1", RoundAttemptID: "missing",
		Operation: researchcontract.OperationFetch, ActualURLOrQuery: "https://example.com/x",
		Outcome: ObservationEmpty, Provenance: researchcontract.ProvenanceFetchedResponse,
	}); err == nil {
		t.Fatal("missing round_attempt FK accepted")
	}
	got, err := GetResearchObservation(ctx, s.db, obs.ID)
	if err != nil || got.AttemptNo != 1 {
		t.Fatalf("get observation: %+v %v", got, err)
	}
	listed, err := ListResearchObservations(ctx, s.db, req.ID)
	if err != nil || len(listed) != 2 || listed[0].AttemptNo != 1 || listed[1].AttemptNo != 3 {
		t.Fatalf("list observations: %+v %v", listed, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE research_observations SET error_code='x' WHERE id=?`, obs.ID); err == nil {
		t.Fatal("observation update accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM research_observations WHERE id=?`, obs.ID); err == nil {
		t.Fatal("observation delete accepted")
	}
}

func TestSourceCapturesImmutable(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	cap := insertFixtureCapture(t, s, "cap-body")
	// Row-per-retrieval: same bytes may be captured twice.
	dup := insertFixtureCapture(t, s, "cap-body")
	if dup.ID == cap.ID {
		t.Fatal("retrieval ids collided")
	}
	byContent, err := ListSourceCapturesByContent(ctx, s.db, cap.ContentSHA256)
	if err != nil || len(byContent) != 2 {
		t.Fatalf("content lookup: %+v %v", byContent, err)
	}
	byURL, err := ListSourceCapturesByURL(ctx, s.db, "https://example.com/jobs/42")
	if err != nil || len(byURL) != 2 {
		t.Fatalf("url lookup: %+v %v", byURL, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE source_captures SET byte_length=1 WHERE id=?`, cap.ID); err == nil {
		t.Fatal("capture update accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM source_captures WHERE id=?`, cap.ID); err == nil {
		t.Fatal("capture delete accepted")
	}
	if _, err := GetSourceCapture(ctx, s.db, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing capture: %v", err)
	}
}

func TestResearchNotesFTS(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()
	var first ResearchNote
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		first, err = InsertResearchNote(ctx, db, actor, ResearchNoteInput{
			RoundID: "round-1", BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
			Intent:       "survey Amsterdam support vacancies",
			CoverageJSON: `{"queries":["support engineer"]}`,
			Conclusion:   "two boards left to check",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := InsertResearchNote(ctx, db, actor, ResearchNoteInput{
			RoundID: "round-1", BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
			Intent:     "unrelated bookkeeping",
			Conclusion: "nothing to do with vacancies",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := SearchResearchNotes(ctx, s.db, "Amsterdam", 10)
	if err != nil || len(hits) != 1 || hits[0].ID != first.ID {
		t.Fatalf("FTS intent search: %+v %v", hits, err)
	}
	hits, err = SearchResearchNotes(ctx, s.db, "boards", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("FTS conclusion search: %+v %v", hits, err)
	}
	byRound, err := ListResearchNotesByRound(ctx, s.db, "round-1")
	if err != nil || len(byRound) != 2 {
		t.Fatalf("notes by round: %+v %v", byRound, err)
	}
	if _, err := SearchResearchNotes(ctx, s.db, "   ", 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty FTS query: %v", err)
	}
	// Single successor per note.
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if _, err := InsertResearchNote(ctx, db, actor, ResearchNoteInput{
			RoundID: "round-1", BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
			Intent: "follow-up", SupersedesID: first.ID,
		}); err != nil {
			return err
		}
		_, err := InsertResearchNote(ctx, db, actor, ResearchNoteInput{
			RoundID: "round-1", BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
			Intent: "second follow-up", SupersedesID: first.ID,
		})
		return err
	}); err == nil {
		t.Fatal("double successor accepted")
	}
	// Missing round FK rejected.
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := InsertResearchNote(ctx, db, actor, ResearchNoteInput{
			RoundID: "missing", BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
			Intent: "orphan",
		})
		return err
	}); err == nil {
		t.Fatal("orphan note accepted")
	}
}

func TestIdentityDecisionsC1C4(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()
	cands := []researchcontract.CandidateIdentity{
		{CandidateID: "opp-1", Kind: "opportunity", Revision: 1},
		{CandidateID: "company-1", Kind: "company", Revision: 1},
	}
	insert := func(in IdentityDecisionInput) (IdentityDecisionRecord, error) {
		var out IdentityDecisionRecord
		err := s.ResearchWrite(ctx, func(db ResearchDB) error {
			var err error
			out, err = InsertIdentityDecision(ctx, db, actor, in)
			return err
		})
		return out, err
	}
	same, err := insert(IdentityDecisionInput{
		RoundID: "round-1", SubjectKind: IdentitySubjectVacancy,
		Candidates: cands, Decision: IdentityDecisionSame,
		DecisionBasis: "same board record", SubjectOpportunityID: "opp-1", SubjectRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !same.BriefIndependent || same.SubjectRevision != 1 {
		t.Fatalf("bad same row: %+v", same)
	}
	// C2: canonical hash is order-independent.
	swapped := []researchcontract.CandidateIdentity{cands[1], cands[0]}
	_, hashA, err := CanonicalCandidateSet(cands)
	if err != nil {
		t.Fatal(err)
	}
	_, hashB, err := CanonicalCandidateSet(swapped)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB || hashA != same.CandidateSetHash {
		t.Fatalf("candidate hash not canonical: %q %q %q", hashA, hashB, same.CandidateSetHash)
	}
	if _, _, err := CanonicalCandidateSet([]researchcontract.CandidateIdentity{
		{CandidateID: "x", Kind: "person", Revision: 1},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad candidate kind: %v", err)
	}
	// C3: same needs exactly one subject + revision.
	for _, in := range []IdentityDecisionInput{
		{RoundID: "round-1", SubjectKind: IdentitySubjectVacancy, Decision: IdentityDecisionSame},
		{RoundID: "round-1", SubjectKind: IdentitySubjectVacancy, Decision: IdentityDecisionSame,
			SubjectCompanyID: "company-1", SubjectOpportunityID: "opp-1", SubjectRevision: 1},
		{RoundID: "round-1", SubjectKind: IdentitySubjectVacancy, Decision: IdentityDecisionSame,
			SubjectOpportunityID: "opp-1"},
		{RoundID: "round-1", SubjectKind: IdentitySubjectVacancy, Decision: IdentityDecisionNew,
			SubjectOpportunityID: "opp-1", SubjectRevision: 1},
	} {
		if _, err := insert(in); err == nil {
			t.Fatalf("bad subject shape accepted: %+v", in)
		}
	}
	if _, err := insert(IdentityDecisionInput{
		RoundID: "round-1", SubjectKind: IdentitySubjectVacancy,
		Decision: IdentityDecisionUnresolved,
	}); err != nil {
		t.Fatalf("unresolved rejected: %v", err)
	}
	got, err := GetIdentityDecision(ctx, s.db, same.ID)
	if err != nil || len(got.Candidates) != 2 || got.Candidates[0].Kind != "company" {
		t.Fatalf("decision round trip: %+v %v", got, err)
	}
	unresolved, err := ListIdentityDecisionsBySubject(ctx, s.db,
		IdentitySubjectVacancy, IdentityDecisionUnresolved, 10)
	if err != nil || len(unresolved) != 1 {
		t.Fatalf("unresolved list: %+v %v", unresolved, err)
	}
	// Missing subject FK rejected at the database.
	if _, err := insert(IdentityDecisionInput{
		RoundID: "round-1", SubjectKind: IdentitySubjectVacancy,
		Decision: IdentityDecisionSame, SubjectOpportunityID: "missing", SubjectRevision: 1,
	}); err == nil {
		t.Fatal("missing subject FK accepted")
	}
}

func TestEntityIdentityKeysC5C8(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()
	insert := func(in EntityIdentityKeyInput) (EntityIdentityKey, error) {
		var out EntityIdentityKey
		err := s.ResearchWrite(ctx, func(db ResearchDB) error {
			var err error
			out, err = InsertEntityIdentityKey(ctx, db, actor, in)
			return err
		})
		return out, err
	}
	strong, err := insert(EntityIdentityKeyInput{
		EntityKind: IdentitySubjectVacancy, Namespace: "board_record_id:ashby:example",
		KeyValue: "req-9", Strength: IdentityKeyStrong, OpportunityID: "opp-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// C5: duplicate current strong keys rejected; aliases and superseded rows
	// may share values.
	if _, err := insert(EntityIdentityKeyInput{
		EntityKind: IdentitySubjectVacancy, Namespace: strong.Namespace,
		KeyValue: "req-9", Strength: IdentityKeyStrong, OpportunityID: "opp-1",
	}); err == nil {
		t.Fatal("duplicate strong key accepted")
	}
	if _, err := insert(EntityIdentityKeyInput{
		EntityKind: IdentitySubjectVacancy, Namespace: strong.Namespace,
		KeyValue: "req-9", Strength: IdentityKeyAlias, OpportunityID: "opp-1",
	}); err != nil {
		t.Fatalf("alias reuse rejected: %v", err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return MarkEntityIdentityKeySuperseded(ctx, db, strong.ID)
	}); err != nil {
		t.Fatal(err)
	}
	reuse, err := insert(EntityIdentityKeyInput{
		EntityKind: IdentitySubjectVacancy, Namespace: strong.Namespace,
		KeyValue: "req-9", Strength: IdentityKeyStrong, OpportunityID: "opp-1",
		SupersedesID: strong.ID,
	})
	if err != nil {
		t.Fatalf("superseding strong key rejected: %v", err)
	}
	if reuse.SupersedesID != strong.ID || reuse.Status != IdentityKeyCurrent {
		t.Fatalf("bad reuse row: %+v", reuse)
	}
	if _, err := FindCurrentStrongKey(ctx, s.db, strong.Namespace, "req-9"); err != nil {
		t.Fatalf("current strong lookup: %v", err)
	}
	if _, err := FindCurrentStrongKey(ctx, s.db, strong.Namespace, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing strong lookup: %v", err)
	}
	// C6: open namespaces accepted, bad shapes rejected.
	if _, err := insert(EntityIdentityKeyInput{
		EntityKind: IdentitySubjectEmployer, Namespace: "brand.new_namespace:v2.x-1",
		KeyValue: "k", Strength: IdentityKeyAlias, CompanyID: "company-1",
	}); err != nil {
		t.Fatalf("novel namespace rejected: %v", err)
	}
	for _, ns := range []string{"", "UPPER", "has space", "slash/x", strings.Repeat("a", 129)} {
		if _, err := insert(EntityIdentityKeyInput{
			EntityKind: IdentitySubjectEmployer, Namespace: ns,
			KeyValue: "k", Strength: IdentityKeyAlias, CompanyID: "company-1",
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("namespace %q: %v", ns, err)
		}
	}
	// C8: exactly one record link.
	for _, in := range []EntityIdentityKeyInput{
		{EntityKind: IdentitySubjectVacancy, Namespace: "n", KeyValue: "k", Strength: IdentityKeyAlias},
		{EntityKind: IdentitySubjectVacancy, Namespace: "n", KeyValue: "k",
			Strength: IdentityKeyAlias, CompanyID: "company-1", OpportunityID: "opp-1"},
	} {
		if _, err := insert(in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad record link accepted: %+v", in)
		}
	}
	if _, err := insert(EntityIdentityKeyInput{
		EntityKind: IdentitySubjectVacancy, Namespace: "n", KeyValue: "k",
		Strength: IdentityKeyAlias, OpportunityID: "missing",
	}); err == nil {
		t.Fatal("missing opportunity FK accepted")
	}
	byOpp, err := ListEntityIdentityKeysByOpportunity(ctx, s.db, "opp-1")
	if err != nil || len(byOpp) != 3 {
		t.Fatalf("keys by opportunity: %+v %v", byOpp, err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return MarkEntityIdentityKeySuperseded(ctx, db, "missing")
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("supersede missing: %v", err)
	}
}

func TestDynamicAssessments(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()
	cap := insertFixtureCapture(t, s, "jev-body")
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return SeedResearchJevAttempt(ctx, db, "jev-1", "round-1", "attempt-1", 0)
	}); err != nil {
		t.Fatal(err)
	}
	evidence, err := CanonicalEvidenceRefsJSON([]researchcontract.EvidenceRef{
		{CaptureID: cap.ID, SpanStart: 4, SpanEnd: 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, setHash, err := CanonicalCandidateSet([]researchcontract.CandidateIdentity{
		{CandidateID: "opp-1", Kind: "opportunity", Revision: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := DynamicAssessmentInput{
		RoundID: "round-1", JevAttemptID: "jev-1", Purpose: "screening",
		QuestionsJSON: `[{"id":"q1"}]`, EvidenceRefsJSON: evidence,
		ProfileVersion: 1, RubricVersion: "rubric-v1",
		CandidatesJSON: candidates, CandidateSetHash: setHash,
		ReuseKey: FixtureSHA256("reuse-1"), Status: DynamicAssessmentSucceeded,
		AnswersJSON: `[{"questionId":"q1"}]`,
	}
	var a DynamicAssessment
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		a, err = InsertDynamicAssessment(ctx, db, actor, in)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return LinkAssessmentCapture(ctx, db, AssessmentCaptureLink{
			AssessmentID: a.ID, CaptureID: cap.ID, SpanStart: 4, SpanEnd: 9,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return LinkAssessmentCapture(ctx, db, AssessmentCaptureLink{
			AssessmentID: a.ID, CaptureID: cap.ID, SpanStart: 4, SpanEnd: 9,
		})
	}); err == nil {
		t.Fatal("duplicate link accepted")
	}
	if err := LinkAssessmentCapture(ctx, s.db, AssessmentCaptureLink{
		AssessmentID: a.ID, CaptureID: cap.ID, SpanStart: 9, SpanEnd: 9,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty span: %v", err)
	}
	links, err := ListAssessmentCaptures(ctx, s.db, a.ID)
	if err != nil || len(links) != 1 {
		t.Fatalf("links by assessment: %+v %v", links, err)
	}
	byCapture, err := ListAssessmentsByCapture(ctx, s.db, cap.ID)
	if err != nil || len(byCapture) != 1 || byCapture[0].AssessmentID != a.ID {
		t.Fatalf("links by capture: %+v %v", byCapture, err)
	}
	// A1: citations pin the content sha256, not the retrieval row id.
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return LinkAssessmentCapture(ctx, db, AssessmentCaptureLink{
			AssessmentID: a.ID, CaptureID: cap.ContentSHA256, SpanStart: 20, SpanEnd: 29,
		})
	}); err != nil {
		t.Fatal(err)
	}
	byContent, err := ListAssessmentsByCapture(ctx, s.db, cap.ContentSHA256)
	if err != nil || len(byContent) != 1 || byContent[0].AssessmentID != a.ID {
		t.Fatalf("links by content sha: %+v %v", byContent, err)
	}
	got, err := GetDynamicAssessmentByReuseKey(ctx, s.db,
		actor.Kind, actor.ID, FixtureSHA256("reuse-1"))
	if err != nil || got.ID != a.ID {
		t.Fatalf("reuse lookup: %+v %v", got, err)
	}
	byBrief, err := ListDynamicAssessmentsByBrief(ctx, s.db, actor.Kind, actor.ID, 1, 10)
	if err != nil || len(byBrief) != 1 {
		t.Fatalf("brief list: %+v %v", byBrief, err)
	}
	// 1:1 jev_attempt binding and per-account reuse key uniqueness.
	in2 := in
	in2.ReuseKey = FixtureSHA256("reuse-2")
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := InsertDynamicAssessment(ctx, db, actor, in2)
		return err
	}); err == nil {
		t.Fatal("second binding for one jev_attempt accepted")
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if err := SeedResearchJevAttempt(ctx, db, "jev-2", "round-1", "attempt-1", 1); err != nil {
			return err
		}
		dup := in
		dup.JevAttemptID = "jev-2"
		_, err = InsertDynamicAssessment(ctx, db, actor, dup)
		return err
	}); err == nil {
		t.Fatal("duplicate reuse key accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE jev_assessments_dynamic SET status='failed' WHERE id=?`, a.ID); err == nil {
		t.Fatal("assessment update accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM jev_assessments_dynamic WHERE id=?`, a.ID); err == nil {
		t.Fatal("assessment delete accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM jev_assessment_captures WHERE assessment_id=?`, a.ID); err == nil {
		t.Fatal("link delete accepted")
	}
}

func TestRecordSightings(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()
	cap := insertFixtureCapture(t, s, "sighting-body")
	var first RecordSighting
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		first, err = InsertRecordSighting(ctx, db, actor, RecordSightingInput{
			OpportunityID: "opp-1", CaptureID: cap.ID,
			ContentSHA256: cap.ContentSHA256, SightingKind: SightingFirst,
			ObservedURL: "https://example.com/jobs/42",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := InsertRecordSighting(ctx, db, actor, RecordSightingInput{
			OpportunityID: "opp-1", CaptureID: cap.ID,
			ContentSHA256: cap.ContentSHA256, SightingKind: SightingUnchanged,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := InsertRecordSighting(ctx, db, actor, RecordSightingInput{
			OpportunityID: "opp-1", CaptureID: cap.ID,
			ContentSHA256: cap.ContentSHA256, SightingKind: "bogus",
		})
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad sighting kind: %v", err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := InsertRecordSighting(ctx, db, actor, RecordSightingInput{
			CompanyID: "company-1", OpportunityID: "opp-1", CaptureID: cap.ID,
			ContentSHA256: cap.ContentSHA256, SightingKind: SightingFirst,
		})
		return err
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dual record link: %v", err)
	}
	byOpp, err := ListRecordSightingsByOpportunity(ctx, s.db, "opp-1")
	if err != nil || len(byOpp) != 2 || byOpp[0].SightingKind != SightingFirst {
		t.Fatalf("sightings by opportunity: %+v %v", byOpp, err)
	}
	byCapture, err := ListRecordSightingsByCapture(ctx, s.db, cap.ID)
	if err != nil || len(byCapture) != 2 {
		t.Fatalf("sightings by capture: %+v %v", byCapture, err)
	}
	// A1: citations pin the content sha256, not the retrieval row id.
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		_, err := InsertRecordSighting(ctx, db, actor, RecordSightingInput{
			OpportunityID: "opp-1", CaptureID: cap.ContentSHA256,
			ContentSHA256: cap.ContentSHA256, SightingKind: SightingUnchanged,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	byContent, err := ListRecordSightingsByCapture(ctx, s.db, cap.ContentSHA256)
	if err != nil || len(byContent) != 1 {
		t.Fatalf("sightings by content sha: %+v %v", byContent, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE record_sightings SET sighting_kind='changed' WHERE id=?`, first.ID); err == nil {
		t.Fatal("sighting update accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM record_sightings WHERE id=?`, first.ID); err == nil {
		t.Fatal("sighting delete accepted")
	}
}

func TestRunEventsAndCheckpoints(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()
	var req ResearchRequest
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		var err error
		req, err = CreateResearchRequest(ctx, db, actor,
			FixtureRequestDescriptor(), researchcontract.CacheStatelessReusable)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	appendEvent := func(id, kind string, at time.Time) error {
		return s.ResearchWrite(ctx, func(db ResearchDB) error {
			return AppendRunEvent(ctx, db, researchcontract.Event{
				ID: id, RunID: "round-1", AttemptID: "attempt-1", Kind: kind,
				RequestFingerprint: req.Fingerprint, Outcome: researchcontract.OutcomeOK,
				ObservedAt: at, RecordedAt: at,
			})
		})
	}
	if err := appendEvent("event-1", researchcontract.EventClaim, base); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent("event-2", researchcontract.EventObservation, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent("event-3", researchcontract.EventLateObservation, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent("event-1", researchcontract.EventClaim, base); err == nil {
		t.Fatal("duplicate event id accepted")
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return AppendRunEvent(ctx, db, researchcontract.Event{
			ID: "event-4", RunID: "round-1", Kind: researchcontract.EventNote,
			Outcome: "bogus", ObservedAt: base, RecordedAt: base,
		})
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad outcome: %v", err)
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return AppendRunEvent(ctx, db, researchcontract.Event{
			ID: "event-4", RunID: "round-1", Kind: researchcontract.EventNote,
			ObservationID: "missing", Outcome: researchcontract.OutcomeOK,
			ObservedAt: base, RecordedAt: base,
		})
	}); err == nil {
		t.Fatal("missing observation FK accepted")
	}
	page, next, err := ListRunEvents(ctx, s.db, "round-1", "", 2)
	if err != nil || len(page) != 2 || next != "event-2" {
		t.Fatalf("page one: %+v %q %v", page, next, err)
	}
	if page[0].Kind != researchcontract.EventClaim ||
		page[0].RequestFingerprint != req.Fingerprint ||
		!page[0].RecordedAt.Equal(base) {
		t.Fatalf("event fields: %+v", page[0])
	}
	page, next, err = ListRunEvents(ctx, s.db, "round-1", next, 2)
	if err != nil || len(page) != 1 || next != "" || page[0].ID != "event-3" {
		t.Fatalf("page two: %+v %q %v", page, next, err)
	}
	if _, _, err := ListRunEvents(ctx, s.db, "round-1", "missing", 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cursor: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE run_events SET kind='note' WHERE event_id='event-1'`); err == nil {
		t.Fatal("event update accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM run_events WHERE event_id='event-1'`); err == nil {
		t.Fatal("event delete accepted")
	}
	cp := researchcontract.Checkpoint{
		ProfileVersion: 1, RubricVersion: "rubric-v1",
		ActiveClaims: []researchcontract.ActiveClaim{
			{Fingerprint: req.Fingerprint, LeaseUntil: base.Add(time.Hour)},
		},
		EvidenceIDs:    []string{"cap-1"},
		SavedRecordIDs: []string{"opp-1"},
		UnresolvedAttempts: []researchcontract.UnresolvedAttempt{
			{AttemptID: "attempt-9", Reason: "uncertain"},
		},
		NextWork:   []string{"check board two"},
		Generation: 1,
		UpdatedAt:  base,
	}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return SaveRunCheckpoint(ctx, db, "round-1", cp)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := GetRunCheckpoint(ctx, s.db, "round-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ActiveClaims) != 1 || got.ActiveClaims[0].Fingerprint != req.Fingerprint ||
		len(got.UnresolvedAttempts) != 1 || len(got.NextWork) != 1 || got.Generation != 1 {
		t.Fatalf("checkpoint round trip: %+v", got)
	}
	cp.Generation = 2
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return SaveRunCheckpoint(ctx, db, "round-1", cp)
	}); err != nil {
		t.Fatal(err)
	}
	got, err = GetRunCheckpoint(ctx, s.db, "round-1")
	if err != nil || got.Generation != 2 {
		t.Fatalf("checkpoint upsert: %+v %v", got, err)
	}
	if _, err := GetRunCheckpoint(ctx, s.db, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing checkpoint: %v", err)
	}
}

func TestOpportunitiesFTS(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	if _, err := s.db.ExecContext(ctx, `UPDATE opportunities
  SET location_text='Amsterdam, North Holland' WHERE id='opp-1'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM opportunities_fts WHERE opportunities_fts MATCH 'Amsterdam'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("location FTS hits = %d", count)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM opportunities_fts WHERE opportunities_fts MATCH 'Support'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("title FTS hits = %d", count)
	}
}

func TestResearchWriteRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openResearchTestDB(t)
	seedResearchBase(t, s)
	actor := FixtureActor()
	boom := errors.New("boom")
	err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if _, err := CreateResearchRequest(ctx, db, actor,
			FixtureRequestDescriptor(), researchcontract.CacheStatelessReusable); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("write error: %v", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM research_requests`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back write left %d rows", count)
	}
	if err := s.ResearchWrite(ctx, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil write callback: %v", err)
	}
}
