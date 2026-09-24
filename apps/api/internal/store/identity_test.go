package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCanonicalIdentityURL(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://Example.COM/jobs/NW-117", "https://example.com/jobs/NW-117"},
		{"HTTPS://example.com./jobs", "https://example.com/jobs"},
		{"https://example.com:443/jobs", "https://example.com/jobs"},
		{"http://example.com:80/jobs", "http://example.com/jobs"},
		{"https://example.com:8443/jobs", "https://example.com:8443/jobs"},
		{"http://example.com:8080/", "http://example.com:8080/"},
		{"https://example.com/jobs#section", "https://example.com/jobs"},
		{"https://example.com/Jobs/%2FEncoded?A=B&C=c", "https://example.com/Jobs/%2FEncoded?A=B&C=c"},
		{"https://example.com", "https://example.com"},
		{"https://example.com/", "https://example.com/"},
		{"  https://example.com/jobs  ", "https://example.com/jobs"},
		{"http://[::1]:80/x", "http://[::1]/x"},
		{"HTTP://EXAMPLE.COM./A?B=C#D", "http://example.com/A?B=C"},
	}
	for _, c := range cases {
		got, err := CanonicalIdentityURL(c.raw)
		if err != nil || got != c.want {
			t.Errorf("CanonicalIdentityURL(%q) = %q, %v; want %q", c.raw, got, err, c.want)
		}
	}
	invalid := []string{
		"", "example.com/jobs", "://example.com/", "1http://example.com/",
		"https://user@example.com/j", "https://example.com:abc/",
		"https:///jobs", "https://", "https://[::1/jobs",
	}
	for _, raw := range invalid {
		if got, err := CanonicalIdentityURL(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("CanonicalIdentityURL(%q) = %q, %v; want ErrInvalid", raw, got, err)
		}
	}
}

func TestCanonicalEmployerDomainAndReqID(t *testing.T) {
	if got := CanonicalEmployerDomain("Example.COM."); got != "example.com" {
		t.Fatalf("domain: %q", got)
	}
	if got := CanonicalReqID("  NW-117 "); got != "nw-117" {
		t.Fatalf("req: %q", got)
	}
	if got := IdentityURLHost("https://Example.COM:8443/jobs"); got != "example.com" {
		t.Fatalf("host: %q", got)
	}
	if got := IdentityURLHost("not a url"); got != "" {
		t.Fatalf("bad host: %q", got)
	}
	namespace, err := IssuerReqIDNamespace(" NorthWind ")
	if err != nil || namespace != "issuer_req_id:northwind" {
		t.Fatalf("issuer namespace: %q %v", namespace, err)
	}
	if _, err := IssuerReqIDNamespace("  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty issuer: %v", err)
	}
	if _, err := IssuerReqIDNamespace("has space"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("spaced issuer: %v", err)
	}
	namespace, err = BoardRecordNamespace("Ashby", "MyTomorrows")
	if err != nil || namespace != "board_record_id:ashby:mytomorrows" {
		t.Fatalf("board namespace: %q %v", namespace, err)
	}
	if _, err := BoardRecordNamespace("", "board"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty provider: %v", err)
	}
}

func openIdentityTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, ctx
}

func TestIdentityRetrievalFTSAndBtree(t *testing.T) {
	s, ctx := openIdentityTestStore(t)
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if err := SeedResearchCompany(ctx, db, "co-northwind", "Northwind"); err != nil {
			return err
		}
		seed := func(id, company, title, location string) error {
			_, err := db.ExecContext(ctx, `INSERT INTO opportunities
  (id,company_id,title,kind,stage,location_text,created_at,updated_at) VALUES (?,?,?,'employment','new',?,?,?)`,
				id, company, title, location, FixtureTime, FixtureTime)
			return err
		}
		if err := seed("opp-be", "co-northwind", "Backend Engineer", "Amsterdam"); err != nil {
			return err
		}
		if err := seed("opp-senior", "co-northwind", "Senior Backend Engineer", "Amsterdam"); err != nil {
			return err
		}
		return seed("opp-design", "co-northwind", "Designer", "Rotterdam")
	}); err != nil {
		t.Fatal(err)
	}
	var fts []IdentityCandidate
	if err := s.Read(ctx, func(r Reader) error {
		var err error
		fts, err = SearchOpportunityCandidatesFTS(ctx, r, "Backend Engineer", "", 20)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(fts) != 2 {
		t.Fatalf("fts backend hits: %+v", fts)
	}
	if err := s.Read(ctx, func(r Reader) error {
		loc, err := SearchOpportunityCandidatesFTS(ctx, r, "", "Rotterdam", 20)
		if err != nil || len(loc) != 1 || loc[0].ID != "opp-design" {
			t.Fatalf("fts location hits: %+v %v", loc, err)
		}
		none, err := SearchOpportunityCandidatesFTS(ctx, r, "", "", 20)
		if err != nil || len(none) != 0 {
			t.Fatalf("empty fts query matched: %+v %v", none, err)
		}
		quoted, err := SearchOpportunityCandidatesFTS(ctx, r, `Engineer "Backend OR 1=1 --`, "", 20)
		if err != nil {
			t.Fatalf("quoted fts: %v", err)
		}
		for _, c := range quoted {
			if c.Kind != "opportunity" || c.Revision < 1 {
				t.Fatalf("bad fts candidate: %+v", c)
			}
		}
		btree, err := FindOpportunitiesByTitle(ctx, r, "backend engineer", 10)
		if err != nil || len(btree) != 1 || btree[0].ID != "opp-be" {
			t.Fatalf("btree title: %+v %v", btree, err)
		}
		companies, err := FindCompaniesByName(ctx, r, "NORTHWIND", 10)
		if err != nil || len(companies) != 1 || companies[0].ID != "co-northwind" {
			t.Fatalf("company name: %+v %v", companies, err)
		}
		byID, err := GetIdentityCandidate(ctx, r, "opportunity", "opp-be")
		if err != nil || byID.Title != "Backend Engineer" || byID.Location != "Amsterdam" {
			t.Fatalf("by id: %+v %v", byID, err)
		}
		if _, err := GetIdentityCandidate(ctx, r, "opportunity", "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing id: %v", err)
		}
		if _, err := GetIdentityCandidate(ctx, r, "planet", "opp-be"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad kind: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityKeysByValueWithHistory(t *testing.T) {
	s, ctx := openIdentityTestStore(t)
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if err := SeedResearchCompany(ctx, db, "co-a", "A"); err != nil {
			return err
		}
		if err := SeedResearchOpportunity(ctx, db, "opp-old", "co-a", "Backend Engineer (Go)"); err != nil {
			return err
		}
		if err := SeedResearchOpportunity(ctx, db, "opp-new", "co-a", "Frontend Engineer (React)"); err != nil {
			return err
		}
		old, err := InsertEntityIdentityKey(ctx, db, FixtureActor(), EntityIdentityKeyInput{
			EntityKind: IdentitySubjectVacancy, Namespace: "issuer_req_id:acme",
			KeyValue: "eng-2041", Strength: IdentityKeyStrong, OpportunityID: "opp-old",
		})
		if err != nil {
			return err
		}
		if err := MarkEntityIdentityKeySuperseded(ctx, db, old.ID); err != nil {
			return err
		}
		_, err = InsertEntityIdentityKey(ctx, db, FixtureActor(), EntityIdentityKeyInput{
			EntityKind: IdentitySubjectVacancy, Namespace: "issuer_req_id:acme",
			KeyValue: "eng-2041", Strength: IdentityKeyAlias, OpportunityID: "opp-new",
			SupersedesID: old.ID,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(r Reader) error {
		rows, err := ListIdentityKeysByValue(ctx, r, "issuer_req_id:acme", "eng-2041")
		if err != nil || len(rows) != 2 {
			t.Fatalf("key history: %+v %v", rows, err)
		}
		if rows[0].Status != IdentityKeyCurrent || rows[1].Status != IdentityKeySuperseded {
			t.Fatalf("key order: %+v", rows)
		}
		across, err := ListReqIDKeys(ctx, r, " ENG-2041 ")
		if err != nil || len(across) != 2 {
			t.Fatalf("req lookup across issuers: %+v %v", across, err)
		}
		if _, err := FindCurrentStrongKey(ctx, r, "issuer_req_id:acme", "eng-2041"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("reused value still strongly unique: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResearchRoundActorAndSeededValidation(t *testing.T) {
	s, ctx := openIdentityTestStore(t)
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		return SeedResearchRound(ctx, db, "round-1", "attempt-1")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(r Reader) error {
		actor, err := ResearchRoundActor(ctx, r, "round-1")
		if err != nil || actor.Kind != FixtureActorKind || actor.ID != FixtureActorID {
			t.Fatalf("round actor: %+v %v", actor, err)
		}
		if _, err := ResearchRoundActor(ctx, r, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing round: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bad := DynamicAssessmentInput{RoundID: "round-1", JevAttemptID: "jev-1",
		Purpose: "p", QuestionsJSON: "[]", EvidenceRefsJSON: "[]", ProfileVersion: 1,
		RubricVersion: "r", CandidatesJSON: "[]",
		CandidateSetHash: strings.Repeat("a", 64), Status: DynamicAssessmentSucceeded, AnswersJSON: "[]"}
	if err := s.ResearchWrite(ctx, func(db ResearchDB) error {
		if _, err := InsertSeededDynamicAssessment(ctx, db, FixtureActor(), "",
			bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("empty seeded id: %v", err)
		}
		bad.ReuseKey = "short"
		if _, err := InsertSeededDynamicAssessment(ctx, db, FixtureActor(), "jda_x",
			bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("short reuse key: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
