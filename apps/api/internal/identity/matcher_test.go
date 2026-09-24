package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Capture bodies come from testdata/synthetic-captures.json: a verbatim,
// read-only T04 snapshot (copied 2026-09-24; never edited). Real-anchored
// items (D01/C01/B01) use minimal test-local bodies instead: doubles decide
// verdicts, so live source text adds nothing and stays out of the repo.

type fixtureFile struct {
	Fixtures []struct {
		CaptureID string `json:"capture_id"`
		Body      string `json:"body"`
	} `json:"fixtures"`
}

func loadFixtureBodies(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "synthetic-captures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed fixtureFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	for _, f := range parsed.Fixtures {
		bodies[f.CaptureID] = f.Body
	}
	for _, want := range []string{"syn/NW-117-careers", "syn/NW-117-aggregator", "syn/ENG-2041-backend", "syn/ENG-2041-frontend", "syn/slug-backend", "syn/slug-data", "syn/NW-204-v1", "syn/NW-204-v2", "syn/LOC-55-ams", "syn/LOC-55-rtm"} {
		if _, ok := bodies[want]; !ok {
			t.Fatalf("fixture snapshot missing %s", want)
		}
	}
	return bodies
}

type fakeAuthority struct{ checkErr error }

func (f *fakeAuthority) Check(context.Context, researchcontract.CheckInput) error {
	return f.checkErr
}

func (f *fakeAuthority) Reserve(context.Context, string, string, string, string) (researchcontract.Reservation, error) {
	return researchcontract.Reservation{}, nil
}

func (f *fakeAuthority) Release(context.Context, string) error { return nil }

func (f *fakeAuthority) Usage(context.Context, string) (researchcontract.UsageLedger, error) {
	return researchcontract.UsageLedger{}, nil
}

func (f *fakeAuthority) ReconciliationFor(context.Context, string) (researchcontract.Reconciliation, error) {
	return researchcontract.Reconciliation{}, nil
}

type fakeBriefs struct {
	profile int64
	rubric  string
	err     error
}

func (f *fakeBriefs) CurrentBrief(context.Context, string) (int64, string, error) {
	return f.profile, f.rubric, f.err
}

// fakeAssessor answers scripted verdicts per question id (default abstain).
// Unknown captures, out-of-range spans, and malformed comparison questions
// fail loudly, like the real handler's binding checks.
type fakeAssessor struct {
	bodies   map[string]string
	verdicts map[string]string // questionID -> same|distinct|abstain
	calls    int
	last     researchcontract.AssessInput
	err      error
}

func (f *fakeAssessor) Assess(_ context.Context, in researchcontract.AssessInput) (researchcontract.Assessment, error) {
	f.calls++
	f.last = in
	if f.err != nil {
		return researchcontract.Assessment{}, f.err
	}
	if in.Purpose != PurposeIdentityMatch {
		return researchcontract.Assessment{}, errors.New("purpose must be " + PurposeIdentityMatch)
	}
	for _, ref := range in.SourceRefs {
		body, ok := f.bodies[ref.CaptureID]
		if !ok {
			return researchcontract.Assessment{}, researchcontract.NewError(researchcontract.OutcomeNotFound,
				"sourceRefs", "unknown capture "+ref.CaptureID)
		}
		if ref.SpanEnd > int64(len(body)) {
			return researchcontract.Assessment{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"sourceRefs", "span exceeds capture "+ref.CaptureID)
		}
	}
	var results []researchcontract.AssessAnswer
	for _, q := range in.Questions {
		if !q.AbstainAllowed || len(q.Alternatives) != 2 {
			return researchcontract.Assessment{}, errors.New("comparison questions need abstention plus same/distinct")
		}
		seen := map[string]bool{}
		for _, a := range q.Alternatives {
			seen[a.ID] = true
		}
		if !seen[AnswerSame] || !seen[AnswerDistinct] {
			return researchcontract.Assessment{}, errors.New("comparison alternatives must be same/distinct")
		}
		switch f.verdicts[q.ID] {
		case AnswerSame:
			results = append(results, researchcontract.AssessAnswer{QuestionID: q.ID, AnswerID: AnswerSame})
		case AnswerDistinct:
			results = append(results, researchcontract.AssessAnswer{QuestionID: q.ID, AnswerID: AnswerDistinct})
		default:
			results = append(results, researchcontract.AssessAnswer{QuestionID: q.ID, Abstained: true})
		}
	}
	return researchcontract.Assessment{ID: "asm_fake", Results: results, ReuseKey: "fake"}, nil
}

func openMatchStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func matchHandler(s *store.Store, auth *fakeAuthority, briefs *fakeBriefs, assessor *fakeAssessor) *Handler {
	return &Handler{Authority: auth, Store: s, Briefs: briefs, Assessor: assessor}
}

func matchFixture(t *testing.T, s *store.Store) (*Handler, *fakeAuthority, *fakeBriefs, *fakeAssessor, map[string]string) {
	t.Helper()
	bodies := loadFixtureBodies(t)
	auth, briefs, assessor := &fakeAuthority{}, &fakeBriefs{profile: 1, rubric: "rubric-v1"}, &fakeAssessor{bodies: bodies, verdicts: map[string]string{}}
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		return store.SeedResearchRound(context.Background(), db, "round-1", "attempt-1")
	}); err != nil {
		t.Fatal(err)
	}
	return matchHandler(s, auth, briefs, assessor), auth, briefs, assessor, bodies
}

func fullRef(bodies map[string]string, id string) researchcontract.EvidenceRef {
	return researchcontract.EvidenceRef{CaptureID: id, SpanStart: 0, SpanEnd: int64(len(bodies[id]))}
}

func seedCompany(t *testing.T, s *store.Store, id, name string) {
	t.Helper()
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		return store.SeedResearchCompany(context.Background(), db, id, name)
	}); err != nil {
		t.Fatal(err)
	}
}

func seedOpportunity(t *testing.T, s *store.Store, id, companyID, title, location string) {
	t.Helper()
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		_, err := db.ExecContext(context.Background(), `INSERT INTO opportunities
  (id,company_id,title,kind,stage,location_text,created_at,updated_at) VALUES (?,?,?,'employment','new',?,?,?)`,
			id, companyID, title, location, store.FixtureTime, store.FixtureTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedCapture(t *testing.T, s *store.Store, id, url, body string) {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		_, err := db.ExecContext(context.Background(), `INSERT INTO source_captures
  (id,content_sha256,artifact_ref,byte_length,original_url,retrieved_at,provenance_kind,
   completeness,executor_identity_json,created_at) VALUES (?,?,?,?,?,?,'fetched_response','complete','{}',?)`,
			id, hex.EncodeToString(sum[:]), "fixture://"+id, len(body), url,
			"2026-09-24T00:00:00Z", store.FixtureTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedKey(t *testing.T, s *store.Store, in store.EntityIdentityKeyInput) store.EntityIdentityKey {
	t.Helper()
	var key store.EntityIdentityKey
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		var err error
		key, err = store.InsertEntityIdentityKey(context.Background(), db, store.FixtureActor(), in)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

func supersedeKey(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		return store.MarkEntityIdentityKeySuperseded(context.Background(), db, id)
	}); err != nil {
		t.Fatal(err)
	}
}

func seedSighting(t *testing.T, s *store.Store, in store.RecordSightingInput) {
	t.Helper()
	body := "sighting body"
	sum := sha256.Sum256([]byte(body))
	in.ContentSHA256 = hex.EncodeToString(sum[:])
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		_, err := store.InsertRecordSighting(context.Background(), db, store.FixtureActor(), in)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func canonicalURL(t *testing.T, raw string) string {
	t.Helper()
	out, err := store.CanonicalIdentityURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countOpportunities(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.Read(context.Background(), func(r store.Reader) error {
		return r.QueryRowContext(context.Background(), `SELECT count(*) FROM opportunities`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func exactIDs(out researchcontract.MatchOutput) map[string]string {
	got := map[string]string{}
	for _, m := range out.ExactMatches {
		got[m.RecordID] = m.Kind
	}
	return got
}

func possibleIDs(out researchcontract.MatchOutput) []string {
	var got []string
	for _, m := range out.PossibleMatches {
		got = append(got, m.RecordID)
	}
	return got
}

func matchInput(attrs researchcontract.MatchAttributes, refs ...researchcontract.EvidenceRef) researchcontract.MatchInput {
	return researchcontract.MatchInput{
		Attributes: attrs, EvidenceRefs: refs, RunID: "round-1", Generation: 1,
	}
}

func TestMatchCrossPostMerge(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	seedCompany(t, s, "co-northwind", "Northwind")
	seedOpportunity(t, s, "opp-nw117", "co-northwind", "Backend Engineer", "Amsterdam")
	seedCapture(t, s, "syn/NW-117-careers", "https://example-careers-northwind.test/jobs/NW-117", bodies["syn/NW-117-careers"])
	seedCapture(t, s, "syn/NW-117-aggregator", "https://example-aggregator.test/listings/88412-northwind-backend", bodies["syn/NW-117-aggregator"])
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: store.IdentityNamespaceCanonicalURL,
		KeyValue:  canonicalURL(t, "https://example-careers-northwind.test/jobs/NW-117"),
		Strength:  store.IdentityKeyStrong, OpportunityID: "opp-nw117"})
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: "issuer_req_id:northwind", KeyValue: "nw-117",
		Strength: store.IdentityKeyStrong, OpportunityID: "opp-nw117"})
	seedSighting(t, s, store.RecordSightingInput{OpportunityID: "opp-nw117",
		CaptureID: "syn/NW-117-careers", SightingKind: store.SightingFirst})
	before := countOpportunities(t, s)

	assessor.verdicts["cmp-opportunity-opp-nw117"] = AnswerSame
	assessor.verdicts["cmp-company-co-northwind"] = AnswerSame
	in := matchInput(researchcontract.MatchAttributes{
		Employer: "Northwind", Title: "Sr. Backend Engineer (Python)",
		URL:           "https://example-aggregator.test/listings/88412-northwind-backend",
		RequisitionID: "NW-117", Location: "Amsterdam",
	}, fullRef(bodies, "syn/NW-117-aggregator"))
	out, err := h.Match(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("outcome: %s", out.Outcome)
	}
	exact := exactIDs(out)
	if exact["opp-nw117"] != "opportunity" || exact["co-northwind"] != "company" {
		t.Fatalf("exact: %+v", out.ExactMatches)
	}
	if assessor.calls != 1 || assessor.last.Purpose != PurposeIdentityMatch {
		t.Fatalf("assessor calls=%d purpose=%s", assessor.calls, assessor.last.Purpose)
	}
	if len(assessor.last.CandidateIdentities) != 2 {
		t.Fatalf("pinned identities: %+v", assessor.last.CandidateIdentities)
	}
	firstKey := assessor.last.IdempotencyKey
	if _, err := h.Match(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if assessor.last.IdempotencyKey != firstKey {
		t.Fatal("identical match must replay a stable idempotency key")
	}
	if after := countOpportunities(t, s); after != before {
		t.Fatalf("match must not write records: %d -> %d", before, after)
	}
}

func TestMatchDistinctSameTitleSeparation(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	bodies["test/mt-863ede5e"] = "myTomorrows Backend Engineer posting body, platform team, Amsterdam."
	bodies["test/mt-52f27ead"] = "myTomorrows Senior Backend Engineer posting body, data platform scope, distinct board record."
	seedCompany(t, s, "co-mytomorrows", "myTomorrows")
	seedOpportunity(t, s, "opp-be", "co-mytomorrows", "Backend Engineer", "Amsterdam")
	seedCapture(t, s, "test/mt-863ede5e", "https://example-ashby.test/posting-a", bodies["test/mt-863ede5e"])
	seedCapture(t, s, "test/mt-52f27ead", "https://example-ashby.test/posting-b", bodies["test/mt-52f27ead"])
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: "board_record_id:ashby:mytomorrows", KeyValue: "863ede5e-b578-431e-b036-a5ba69e501c5",
		Strength: store.IdentityKeyStrong, OpportunityID: "opp-be"})

	assessor.verdicts["cmp-opportunity-opp-be"] = AnswerDistinct
	assessor.verdicts["cmp-company-co-mytomorrows"] = AnswerSame
	out, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Employer: "myTomorrows", Title: "Senior Backend Engineer",
		URL: "https://example-ashby.test/posting-b", Location: "Amsterdam",
	}, fullRef(bodies, "test/mt-52f27ead")))
	if err != nil {
		t.Fatal(err)
	}
	exact := exactIDs(out)
	if _, merged := exact["opp-be"]; merged {
		t.Fatalf("distinct same-title role merged: %+v", out.ExactMatches)
	}
	for _, id := range possibleIDs(out) {
		if id == "opp-be" {
			t.Fatalf("distinct role surfaced as possible: %+v", out.PossibleMatches)
		}
	}
	if exact["co-mytomorrows"] != "company" {
		t.Fatalf("same employer must still match: %+v", out.ExactMatches)
	}
}

func TestMatchReusedReqIDSeparatesBeforeReuse(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	seedCompany(t, s, "co-acme", "Acme")
	seedOpportunity(t, s, "opp-go", "co-acme", "Backend Engineer (Go)", "Eindhoven")
	seedCapture(t, s, "syn/ENG-2041-backend", "https://example.test/jobs/ENG-2041", bodies["syn/ENG-2041-backend"])
	seedCapture(t, s, "syn/ENG-2041-frontend", "https://example.test/jobs/ENG-2041", bodies["syn/ENG-2041-frontend"])
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: "issuer_req_id:acme", KeyValue: "eng-2041",
		Strength: store.IdentityKeyStrong, OpportunityID: "opp-go"})

	assessor.verdicts["cmp-opportunity-opp-go"] = AnswerDistinct
	assessor.verdicts["cmp-company-co-acme"] = AnswerDistinct
	out, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Employer: "Acme", Title: "Frontend Engineer (React)", RequisitionID: "ENG-2041",
		URL: "https://example.test/jobs/ENG-2041", Location: "Eindhoven",
	}, fullRef(bodies, "syn/ENG-2041-frontend")))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ExactMatches) != 0 || out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("reused req must not merge: %+v outcome=%s", out.ExactMatches, out.Outcome)
	}
	if assessor.calls != 1 {
		t.Fatalf("semantic comparison required, calls=%d", assessor.calls)
	}
}

func TestMatchReusedReqIDSurfacesHistoryAfterReuse(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	seedCompany(t, s, "co-acme", "Acme")
	seedOpportunity(t, s, "opp-go", "co-acme", "Backend Engineer (Go)", "Eindhoven")
	seedOpportunity(t, s, "opp-react", "co-acme", "Frontend Engineer (React)", "Eindhoven")
	seedCapture(t, s, "syn/ENG-2041-frontend", "https://example.test/jobs/ENG-2041", bodies["syn/ENG-2041-frontend"])
	old := seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: "issuer_req_id:acme", KeyValue: "eng-2041",
		Strength: store.IdentityKeyStrong, OpportunityID: "opp-go"})
	supersedeKey(t, s, old.ID)
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: "issuer_req_id:acme", KeyValue: "eng-2041",
		Strength: store.IdentityKeyAlias, OpportunityID: "opp-react", SupersedesID: old.ID})

	assessor.verdicts["cmp-opportunity-opp-react"] = AnswerSame
	assessor.verdicts["cmp-company-co-acme"] = AnswerSame
	out, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Employer: "Acme", Title: "Frontend Engineer (React)", RequisitionID: "ENG-2041",
		URL: "https://example.test/jobs/ENG-2041", Location: "Eindhoven",
	}, fullRef(bodies, "syn/ENG-2041-frontend")))
	if err != nil {
		t.Fatal(err)
	}
	exact := exactIDs(out)
	if exact["opp-react"] != "opportunity" {
		t.Fatalf("exact: %+v", out.ExactMatches)
	}
	if _, merged := exact["opp-go"]; merged {
		t.Fatalf("historical record must never go exact: %+v", out.ExactMatches)
	}
	foundHistory := false
	for _, id := range possibleIDs(out) {
		if id == "opp-go" {
			foundHistory = true
		}
	}
	if !foundHistory {
		t.Fatalf("retained history must surface as possible: %+v", out.PossibleMatches)
	}
}

func TestMatchReusedURLSeparates(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	seedCompany(t, s, "co-initech", "Initech")
	seedOpportunity(t, s, "opp-be102", "co-initech", "Backend Engineer", "Utrecht")
	seedCapture(t, s, "syn/slug-backend", "https://example-careers-initech.test/openings/senior-engineer", bodies["syn/slug-backend"])
	seedCapture(t, s, "syn/slug-data", "https://example-careers-initech.test/openings/senior-engineer", bodies["syn/slug-data"])
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: store.IdentityNamespaceCanonicalURL,
		KeyValue:  canonicalURL(t, "https://example-careers-initech.test/openings/senior-engineer"),
		Strength:  store.IdentityKeyStrong, OpportunityID: "opp-be102"})
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: "issuer_req_id:initech", KeyValue: "be-102",
		Strength: store.IdentityKeyStrong, OpportunityID: "opp-be102"})

	assessor.verdicts["cmp-opportunity-opp-be102"] = AnswerDistinct
	assessor.verdicts["cmp-company-co-initech"] = AnswerSame
	out, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Employer: "Initech", Title: "Senior Data Engineer", RequisitionID: "DA-330",
		URL: "https://example-careers-initech.test/openings/senior-engineer", Location: "Utrecht",
	}, fullRef(bodies, "syn/slug-data")))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out.ExactMatches {
		if m.RecordID == "opp-be102" {
			t.Fatalf("URL equality merged distinct roles: %+v", out.ExactMatches)
		}
	}
}

func TestMatchChangedPostingStaysSame(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	seedCompany(t, s, "co-northwind", "Northwind")
	seedOpportunity(t, s, "opp-nw204", "co-northwind", "Backend Engineer", "Amsterdam")
	seedCapture(t, s, "syn/NW-204-v1", "https://example-careers-northwind.test/jobs/NW-204", bodies["syn/NW-204-v1"])
	seedCapture(t, s, "syn/NW-204-v2", "https://example-careers-northwind.test/jobs/NW-204", bodies["syn/NW-204-v2"])
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: store.IdentityNamespaceCanonicalURL,
		KeyValue:  canonicalURL(t, "https://example-careers-northwind.test/jobs/NW-204"),
		Strength:  store.IdentityKeyStrong, OpportunityID: "opp-nw204"})
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: "issuer_req_id:northwind", KeyValue: "nw-204",
		Strength: store.IdentityKeyStrong, OpportunityID: "opp-nw204"})

	assessor.verdicts["cmp-opportunity-opp-nw204"] = AnswerSame
	assessor.verdicts["cmp-company-co-northwind"] = AnswerSame
	out, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Employer: "Northwind", Title: "Backend Engineer", RequisitionID: "NW-204",
		URL: "https://example-careers-northwind.test/jobs/NW-204", Location: "Remote within EU",
	}, fullRef(bodies, "syn/NW-204-v2")))
	if err != nil {
		t.Fatal(err)
	}
	if exactIDs(out)["opp-nw204"] != "opportunity" {
		t.Fatalf("changed posting must stay same: %+v", out.ExactMatches)
	}
}

func TestMatchConflictingLocationStaysSame(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	seedCompany(t, s, "co-northwind", "Northwind")
	seedOpportunity(t, s, "opp-loc55", "co-northwind", "Backend Engineer", "Amsterdam")
	seedCapture(t, s, "syn/LOC-55-ams", "https://example-careers-northwind.test/jobs/LOC-55", bodies["syn/LOC-55-ams"])
	seedCapture(t, s, "syn/LOC-55-rtm", "https://example-aggregator.test/listings/99120-northwind", bodies["syn/LOC-55-rtm"])
	seedKey(t, s, store.EntityIdentityKeyInput{EntityKind: store.IdentitySubjectVacancy,
		Namespace: "issuer_req_id:northwind", KeyValue: "loc-55",
		Strength: store.IdentityKeyStrong, OpportunityID: "opp-loc55"})

	assessor.verdicts["cmp-opportunity-opp-loc55"] = AnswerSame
	assessor.verdicts["cmp-company-co-northwind"] = AnswerSame
	out, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Employer: "Northwind", Title: "Backend Engineer", RequisitionID: "LOC-55",
		URL: "https://example-aggregator.test/listings/99120-northwind", Location: "Rotterdam",
	}, fullRef(bodies, "syn/LOC-55-rtm")))
	if err != nil {
		t.Fatal(err)
	}
	if exactIDs(out)["opp-loc55"] != "opportunity" {
		t.Fatalf("location conflict must not split identity: %+v", out.ExactMatches)
	}
}

func TestMatchUnresolvedPreservation(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	thin, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Title: "Backend Engineer",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if thin.Outcome != researchcontract.OutcomeOK || thin.Unresolved == nil || thin.Unresolved.NextQuestion == "" {
		t.Fatalf("thin input must stay unresolved: %+v", thin)
	}
	if assessor.calls != 0 {
		t.Fatalf("no candidates means no Jev spend, calls=%d", assessor.calls)
	}

	seedCompany(t, s, "co-acme", "Acme")
	seedOpportunity(t, s, "opp-x", "co-acme", "Backend Engineer", "Eindhoven")
	seedCapture(t, s, "syn/acme-backend", "https://example.test/acme", bodies["syn/acme-backend"])
	// Default verdicts abstain: the candidate stays possible, never forced.
	amb, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Employer: "Acme", Title: "Backend Engineer", Location: "Eindhoven",
	}, fullRef(bodies, "syn/acme-backend")))
	if err != nil {
		t.Fatal(err)
	}
	if amb.Outcome != researchcontract.OutcomeIdentityAmbiguous || amb.Unresolved == nil {
		t.Fatalf("abstention must stay ambiguous: %+v", amb)
	}
	if len(amb.ExactMatches) != 0 || len(amb.PossibleMatches) == 0 {
		t.Fatalf("abstention keeps possibles, never exact: %+v", amb)
	}
}

func TestMatchOwnerCorrectionPinning(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	seedCompany(t, s, "co-acme", "Acme")
	seedOpportunity(t, s, "opp-pin", "co-acme", "Backend Engineer", "Eindhoven")
	seedCapture(t, s, "syn/acme-backend", "https://example.test/acme", bodies["syn/acme-backend"])
	if err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		_, err := db.ExecContext(context.Background(),
			`UPDATE opportunities SET revision=2 WHERE id='opp-pin'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	assessor.verdicts["cmp-opportunity-opp-pin"] = AnswerSame
	in := matchInput(researchcontract.MatchAttributes{Title: "Janitor", Location: "Nowhere"},
		fullRef(bodies, "syn/acme-backend"))
	in.ExtraCandidates = []researchcontract.CandidateIdentity{
		{CandidateID: "opp-pin", Kind: "opportunity", Revision: 1}, // stale pin: live revision wins
	}
	out, err := h.Match(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if exactIDs(out)["opp-pin"] != "opportunity" {
		t.Fatalf("pinned candidate must compare: %+v", out.ExactMatches)
	}
	if len(assessor.last.CandidateIdentities) != 1 || assessor.last.CandidateIdentities[0].Revision != 2 {
		t.Fatalf("comparison must pin the live revision: %+v", assessor.last.CandidateIdentities)
	}

	in.ExtraCandidates = []researchcontract.CandidateIdentity{
		{CandidateID: "missing", Kind: "opportunity", Revision: 1},
	}
	if _, err := h.Match(context.Background(), in); err == nil {
		t.Fatal("unknown pin must fail loudly, never drop silently")
	}
}

func TestMatchSameCaptureSightingJoins(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, bodies := matchFixture(t, s)
	seedCompany(t, s, "co-acme", "Acme")
	seedOpportunity(t, s, "opp-s", "co-acme", "Backend Engineer", "Eindhoven")
	seedCapture(t, s, "syn/acme-backend", "https://example.test/acme", bodies["syn/acme-backend"])
	seedSighting(t, s, store.RecordSightingInput{OpportunityID: "opp-s",
		CaptureID: "syn/acme-backend", SightingKind: store.SightingFirst})

	assessor.verdicts["cmp-opportunity-opp-s"] = AnswerSame
	out, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Title: "Something Else Entirely", // matches nothing textually: only the sighting joins
	}, fullRef(bodies, "syn/acme-backend")))
	if err != nil {
		t.Fatal(err)
	}
	if exactIDs(out)["opp-s"] != "opportunity" {
		t.Fatalf("sighted record must join retrieval: %+v calls=%d", out, assessor.calls)
	}
	if len(assessor.last.Questions) != 1 {
		t.Fatalf("only the sighted candidate compares: %+v", assessor.last.Questions)
	}
}

func TestMatchNoEvidenceDefersToPossible(t *testing.T) {
	s := openMatchStore(t)
	h, _, _, assessor, _ := matchFixture(t, s)
	seedCompany(t, s, "co-acme", "Acme")
	seedOpportunity(t, s, "opp-x", "co-acme", "Backend Engineer", "Eindhoven")
	out, err := h.Match(context.Background(), matchInput(researchcontract.MatchAttributes{
		Employer: "Acme", Title: "Backend Engineer", Location: "Eindhoven",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != researchcontract.OutcomeIdentityAmbiguous || len(out.PossibleMatches) == 0 {
		t.Fatalf("evidence-less retrieval stays possible: %+v", out)
	}
	if assessor.calls != 0 {
		t.Fatalf("no evidence means no comparison, calls=%d", assessor.calls)
	}
}

func TestSplitCompareCapAndKeyStability(t *testing.T) {
	var cands []candidate
	for i := 0; i < MaxCompareCandidates+3; i++ {
		cands = append(cands, candidate{IdentityCandidate: store.IdentityCandidate{
			Kind: "opportunity", ID: string(rune('a' + i)), Revision: 1,
		}})
	}
	cands = append(cands, candidate{history: true}) // history never compares
	compare, deferred := splitCompare(cands, true)
	if len(compare) != MaxCompareCandidates || len(deferred) != 4 {
		t.Fatalf("compare=%d deferred=%d", len(compare), len(deferred))
	}
	compare, deferred = splitCompare(cands, false)
	if len(compare) != 0 || len(deferred) != len(cands) {
		t.Fatalf("no evidence must defer all: compare=%d", len(compare))
	}
	questions := []researchcontract.AssessQuestion{{ID: "q", Text: "t", AbstainAllowed: true,
		Alternatives: []researchcontract.AssessAlternative{{ID: "same", Label: "s"}, {ID: "distinct", Label: "d"}}}}
	refs := []researchcontract.EvidenceRef{{CaptureID: "c", SpanStart: 0, SpanEnd: 1}}
	idents := []researchcontract.CandidateIdentity{{CandidateID: "o", Kind: "opportunity", Revision: 1}}
	a := compareIdempotencyKey("run-1", 1, "r", questions, refs, idents)
	b := compareIdempotencyKey("run-1", 1, "r", questions, refs, idents)
	c := compareIdempotencyKey("run-2", 1, "r", questions, refs, idents)
	if a != b || a == c {
		t.Fatal("comparison key must be deterministic per run inputs")
	}
}

func TestMatchValidationAndErrors(t *testing.T) {
	s := openMatchStore(t)
	h, auth, _, assessor, bodies := matchFixture(t, s)
	ctx := context.Background()
	if _, err := (&Handler{}).Match(ctx, matchInput(researchcontract.MatchAttributes{})); err == nil {
		t.Fatal("nil dependencies must fail")
	}
	bad := matchInput(researchcontract.MatchAttributes{})
	bad.RunID = ""
	if _, err := h.Match(ctx, bad); err == nil {
		t.Fatal("empty run id must fail")
	}
	bad = matchInput(researchcontract.MatchAttributes{})
	bad.Generation = 0
	if _, err := h.Match(ctx, bad); err == nil {
		t.Fatal("zero generation must fail")
	}
	bad = matchInput(researchcontract.MatchAttributes{})
	bad.ExtraCandidates = []researchcontract.CandidateIdentity{{CandidateID: "x", Kind: "planet", Revision: 1}}
	if _, err := h.Match(ctx, bad); err == nil {
		t.Fatal("bad extra kind must fail")
	}
	bad = matchInput(researchcontract.MatchAttributes{}, researchcontract.EvidenceRef{CaptureID: "c", SpanStart: 5, SpanEnd: 5})
	if _, err := h.Match(ctx, bad); err == nil {
		t.Fatal("empty span must fail")
	}
	auth.checkErr = researchcontract.NewError(researchcontract.OutcomeStale, "generation", "rotated")
	if _, err := h.Match(ctx, matchInput(researchcontract.MatchAttributes{Title: "x"})); err != auth.checkErr {
		t.Fatalf("authority error must pass through: %v", err)
	}
	auth.checkErr = nil

	seedCompany(t, s, "co-acme", "Acme")
	seedOpportunity(t, s, "opp-x", "co-acme", "Backend Engineer", "Eindhoven")
	seedCapture(t, s, "syn/acme-backend", "https://example.test/acme", bodies["syn/acme-backend"])
	withRefs := matchInput(researchcontract.MatchAttributes{Title: "Backend Engineer"},
		fullRef(bodies, "syn/acme-backend"))
	assessor.err = researchcontract.NewError(researchcontract.OutcomeRateLimited, "", "slow down")
	if _, err := h.Match(ctx, withRefs); err == nil {
		t.Fatal("assessor error must propagate")
	}
	assessor.err = nil
	assessor.bodies = map[string]string{} // refs no longer bound
	if _, err := h.Match(ctx, withRefs); err == nil {
		t.Fatal("unbound evidence must fail")
	}
}
