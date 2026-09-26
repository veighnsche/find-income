// T26 lane D integrated identity/evidence acceptance (lane D, T26).
//
// Re-runs ONLY the integration-sensitive T04 corpus cases against the real
// T23 wired stack (researchwire.Wire): real executor capture over a
// controlled fixture board, real store, real Jev binding (jevassess.Handler
// with real authority/captures/briefs/exchange-log/sink), real identity
// matcher, and the real records_save path. The Jev provider is a zero-spend
// scripted fixture: verdicts are corpus-scripted so the tests pin the
// binding machinery (capture+span citations, brief binding, identity
// rechecks, key convergence), not provider evidence.
//
// Corpus mapping (T04 corpus.json frozen 2026-09-24 is authoritative;
// expected-distinctions.md is the human record):
//
//	C02 cross-post w/ title wording  -> TestT26_CrossSourceIdentity
//	D01/D02 distinct same-title      -> TestT26_SeparateVacancies
//	A03 location conflict            -> TestT26_SourceConflictSameIdentity
//	A02 conflicting evidence         -> TestT26_ConflictingEvidenceBinding
//	R01 changed posting + refresh    -> TestT26_LegitimateRefresh
//	R02/R03 reused identifiers       -> TestT26_HistoricalCapturesOnReuse
//	B01 brief change, no refetch     -> TestT26_BriefChangeReassessment
//	batch independence               -> TestT26_BatchIndependentRecords
//	A01/M01/M02 suitability backing  -> TestT26_SourceBackedSuitability
//	Q02 exact repeat + refresh mech  -> TestT26_ExactRepeatReuseAndRefresh
//
// Deliberately NOT re-run here (not integration-sensitive at the D level;
// T21 pins the D aspect, owning lanes cover the mechanics): C01 (T23
// already proves sighting-merge over the same stack; C02 adds the wording
// variant), Q01 (fixture recommendation; retrieval is lane C), F01/F02
// fetch mechanics (lane C; the justified-refresh memory mechanism is
// probed lightly in TestT26_ExactRepeatReuseAndRefresh), U01/U02/U03
// (lanes B/C; T23 proves the browse kind), outage/binding failures (T21).
//
// Retrieval vs claimed identity signals: the fixture board is a retrieval
// path only. Saves claim the corpus posting URLs (.test) as sourceUrl, so
// canonical-URL/req/board identity keys keep their corpus meaning while
// capture bytes stay hermetic. Real-anchored items (D01/M01/A01/B01) use
// minimal test-local bodies preserving the distinguishing substance (same
// rule as T14/T21: scripted verdicts decide, so live source text adds
// nothing); real-byte provenance is re-verified by shell in the T26 note.
//
// Known open limitation under test: the wired assessor's Supersedes
// predecessor resolver is nil (T23). Fresh assessments are unaffected;
// reassessment chains stay unlinked. TestT26_BriefChangeReassessment
// verifies around it and records exactly which B01 expectation it touches.
package recordsave_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/identity"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchmemory"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ---------------------------------------------------------------------------
// Fixture Jev provider (zero spend) + controlled fixture board.
// ---------------------------------------------------------------------------

// t26Provider answers scripted verdicts per question id (default abstain).
// It records every call's question ids so tests can prove a semantic
// comparison actually ran (non-vacuous distinct verdicts).
type t26Provider struct {
	mu       sync.Mutex
	model    string
	verdicts map[string]string
	calls    int
	lastIDs  []string
	lastReq  jev.Request
}

func (p *t26Provider) RequestedModel() string { return p.model }

func (p *t26Provider) EncodedRequest(r jev.Request) ([]byte, error) {
	return json.Marshal(r.State)
}

func (p *t26Provider) EvaluateOnceCaptured(_ context.Context, r jev.Request) (jev.Result, jev.CapturedExchange, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	encoded, _ := json.Marshal(r.State)
	out := jev.Result{
		RequestedModel: p.model, ReturnedModel: p.model,
		Answers:     map[string]jev.Answer{},
		Usage:       jev.Usage{InputTokens: 10, OutputTokens: 5},
		RawResponse: json.RawMessage(`{"model":"t26-fixture"}`),
	}
	var ids []string
	for id := range r.Questions {
		ids = append(ids, id)
		choice, ok := p.verdicts[id]
		if !ok {
			choice = jevassess.AbstainID
		}
		out.Answers[id] = jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{
			Choice: choice, Probabilities: map[string]float64{choice: 1}, Confidence: 0.9}}
	}
	p.lastIDs = ids
	p.lastReq = r
	return out, jev.CapturedExchange{RequestBytes: encoded, ResponseBytes: []byte(`{}`),
		HTTPStatus: 200, ReturnedModel: p.model}, nil
}

func (p *t26Provider) setVerdict(id, verdict string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verdicts[id] = verdict
}

func (p *t26Provider) nCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// saw reports whether the most recent Jev call carried the question id.
func (p *t26Provider) saw(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, got := range p.lastIDs {
		if got == id {
			return true
		}
	}
	return false
}

var _ jevassess.Provider = (*t26Provider)(nil)

// t26Board is a mutable controlled posting board. Bodies are served as
// text/plain so captured bytes equal the served corpus bytes.
type t26Board struct {
	mu     sync.Mutex
	bodies map[string]string
	srv    *httptest.Server
}

func newT26Board(t *testing.T, bodies map[string]string) *t26Board {
	t.Helper()
	b := &t26Board{bodies: map[string]string{}}
	for k, v := range bodies {
		b.bodies[k] = v
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		body, ok := b.bodies[r.URL.Path]
		b.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, body)
	})
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

func (b *t26Board) url(path string) string { return b.srv.URL + path }

// ---------------------------------------------------------------------------
// Corpus bodies: verbatim synthetic snapshot + minimal real-anchored bodies.
// ---------------------------------------------------------------------------

// t26Snapshot reads the verbatim read-only T04 synthetic snapshot kept in
// the identity package testdata (copied 2026-09-24; never edited).
func t26Snapshot(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "identity", "testdata", "synthetic-captures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Fixtures []struct {
			CaptureID string `json:"capture_id"`
			Body      string `json:"body"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	for _, f := range parsed.Fixtures {
		bodies[f.CaptureID] = f.Body
	}
	for _, want := range []string{
		"syn/NW-117-careers", "syn/NW-117-aggregator",
		"syn/acme-backend", "syn/brewer-backend",
		"syn/NW-204-v1", "syn/NW-204-v2",
		"syn/ENG-2041-backend", "syn/ENG-2041-frontend",
		"syn/slug-backend", "syn/slug-data",
		"syn/LOC-55-ams", "syn/LOC-55-rtm",
		"syn/snippet-go-ams",
	} {
		if _, ok := bodies[want]; !ok {
			t.Fatalf("corpus snapshot missing %s", want)
		}
	}
	return bodies
}

// Minimal test-local bodies for real-anchored items (D01/M01/A01/B01/A02).
// Scripted verdicts decide, so live source text adds nothing (same rule as
// T14/T21); each body keeps the DISTINGUISHING substance the corpus
// expects. Real-byte hashes are re-verified by shell in the T26 note.
const (
	t26MyTBackend = `myTomorrows Backend Engineer posting (T26 integration fixture). As a Backend Engineer, you will be responsible for developing and maintaining the platform infrastructure. You will work closely with our Frontend Engineers, DevOps Engineers, and other stakeholders. Our office is in Amsterdam. Board record 863ede5e-b578-431e-b036-a5ba69e501c5.`
	t26MyTSenior  = `myTomorrows Senior Backend Engineer posting (T26 integration fixture). Lead the data platform team building large-scale ingestion services. Requires 6+ years backend experience and prior tech-lead scope. Our office is in Amsterdam. Board record 52f27ead-683f-4047-9185-f14c75324893.`

	t26Crisp = `Crisp developer posting (T26 integration fixture, minimal substance of the real capture). You will: Work with product owners to implement new app features. Code across our stack - frontend and backend. Ship often and take responsibility. A minimum of B2 level Dutch and a full-time availability of 36-40 hours per week are required. What we offer: A competitive salary and equity/stock. Our process starts with a technical assignment.`

	t26VacBody  = `Source A, vacancy: You own backend APIs. This position has no frontend implementation duties.`
	t26ClarBody = `Source B, employer clarification about this same role, same date: The engineer must also maintain our React UI one day each week.`
)

// t26BoardBodies maps fixture-board paths to served bodies.
func t26BoardBodies(t *testing.T) map[string]string {
	t.Helper()
	snap := t26Snapshot(t)
	return map[string]string{
		"/c02/careers":    snap["syn/NW-117-careers"],
		"/c02/aggregator": snap["syn/NW-117-aggregator"],
		"/d02/acme":       snap["syn/acme-backend"],
		"/d02/brewer":     snap["syn/brewer-backend"],
		"/r01/v1":         snap["syn/NW-204-v1"],
		"/r01/v2":         snap["syn/NW-204-v2"],
		"/r02/backend":    snap["syn/ENG-2041-backend"],
		"/r02/frontend":   snap["syn/ENG-2041-frontend"],
		"/r03/backend":    snap["syn/slug-backend"],
		"/r03/data":       snap["syn/slug-data"],
		"/a03/ams":        snap["syn/LOC-55-ams"],
		"/a03/rtm":        snap["syn/LOC-55-rtm"],
		"/m02/snippet":    snap["syn/snippet-go-ams"],
		"/d01/backend":    t26MyTBackend,
		"/d01/senior":     t26MyTSenior,
		"/m01/myt":        t26MyTBackend,
		"/a01/crisp":      t26Crisp,
		"/b01/crisp":      t26Crisp,
		"/a02/vac":        t26VacBody,
		"/a02/clar":       t26ClarBody,
		"/batch/alpha":    snap["syn/acme-backend"],
		"/batch/beta":     snap["syn/brewer-backend"],
		"/q02/item":       snap["syn/LOC-55-ams"],
	}
}

// ---------------------------------------------------------------------------
// Harness: one real wired stack per test.
// ---------------------------------------------------------------------------

type t26Run struct {
	id      string
	gen     int64
	profile int64
	rubric  string
}

type t26Harness struct {
	ctx      context.Context
	db       *store.Store
	stack    *researchwire.Stack
	owner    store.Actor
	agent    store.Actor
	board    *t26Board
	provider *t26Provider
	saver    researchcontract.RecordSaver
}

func newT26Harness(t *testing.T) *t26Harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	provider := &t26Provider{model: "t26-fixture-jev-1", verdicts: map[string]string{}}
	stack, err := researchwire.Wire(db, researchwire.Config{
		ArtifactRoot:   filepath.Join(dir, "research-artifacts"),
		ScratchRoot:    filepath.Join(dir, "scratch"),
		AgentID:        researchwire.DefaultAgentID,
		PermitLoopback: true, // test-only: controlled fixture board
		JevProvider:    provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := store.Actor{Kind: "agent", ID: researchwire.DefaultAgentID}
	saver, err := stack.NewSaverFor(agent)
	if err != nil {
		t.Fatal(err)
	}
	return &t26Harness{ctx: ctx, db: db, stack: stack,
		owner: researchwire.OwnerActor(), agent: agent,
		board: newT26Board(t, t26BoardBodies(t)), provider: provider, saver: saver}
}

// commission opens a codex-scoped run directly through the run
// supervisor. T26 pins the saver/identity machinery, not discovery
// commissioning, and the scoped saver needs the codex turn/record scope
// plus agent delegation that discovery runs deliberately lack.
func (h *t26Harness) commission(t *testing.T, key, brief string) t26Run {
	t.Helper()
	ownerBrief, err := codexservice.CurrentOwnerBrief(h.ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.stack.Supervisor.Commission(h.ctx, rounds.CommissionInput{
		Actor: h.owner, BriefText: brief, AgentID: h.agent.ID,
		ProfileVersion: ownerBrief.ProfileVersion, RubricVersion: ownerBrief.RubricVersion,
		RubricSource: ownerBrief.Source,
		Allowance: &rounds.AllowanceInput{
			TimeMs: 600000, MaxActions: 200, MaxJev: 100, MaxTurns: 50, MaxConcurrent: 2},
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || out.RunID == "" {
		t.Fatalf("commission: %+v", out)
	}
	return t26Run{id: out.RunID, gen: out.Generation,
		profile: out.ProfileVersion, rubric: out.RubricVersion}
}

// fetch dispatches one real fetch through the supervisor and returns the
// observation id and the receipt's canonical content id (citations pin the
// receipt id, per the T23 binding interpretation).
func (h *t26Harness) fetch(t *testing.T, run t26Run, key, url string) (obsID, capID string) {
	t.Helper()
	out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: run.id, Generation: run.gen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: url,
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("dispatch %s: %s %+v", key, out.Outcome, out.Receipt)
	}
	if out.Receipt.CaptureID == "" || out.ObservationID == "" {
		t.Fatalf("dispatch %s missing ids: %+v", key, out)
	}
	return out.ObservationID, out.Receipt.CaptureID
}

func (h *t26Harness) captureBytes(t *testing.T, capID string) string {
	t.Helper()
	_, rc, err := h.stack.Captures.OpenCapture(h.ctx, capID)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func t26Span(t *testing.T, body, needle string) (int64, int64) {
	t.Helper()
	at := strings.Index(body, needle)
	if at < 0 {
		t.Fatalf("needle %q missing from capture bytes", needle)
	}
	return int64(at), int64(at + len(needle))
}

func t26ExcerptSHA(body string, start, end int) string {
	sum := sha256.Sum256([]byte(body[start:end]))
	return hex.EncodeToString(sum[:])
}

func t26Link(t *testing.T, body, capID, needle string) researchcontract.EvidenceLink {
	t.Helper()
	start, end := t26Span(t, body, needle)
	return researchcontract.EvidenceLink{CaptureID: capID, SpanStart: start, SpanEnd: end,
		ExcerptSHA256: t26ExcerptSHA(body, int(start), int(end))}
}

func t26FullLink(body, capID string) researchcontract.EvidenceLink {
	return researchcontract.EvidenceLink{CaptureID: capID, SpanStart: 0, SpanEnd: int64(len(body)),
		ExcerptSHA256: t26ExcerptSHA(body, 0, len(body))}
}

func t26Ref(t *testing.T, body, capID, needle string) researchcontract.EvidenceRef {
	t.Helper()
	start, end := t26Span(t, body, needle)
	return researchcontract.EvidenceRef{CaptureID: capID, SpanStart: start, SpanEnd: end}
}

func t26FullRef(body, capID string) researchcontract.EvidenceRef {
	return researchcontract.EvidenceRef{CaptureID: capID, SpanStart: 0, SpanEnd: int64(len(body))}
}

func t26NewDecision() *researchcontract.IdentityDecision {
	return &researchcontract.IdentityDecision{Decision: "new"}
}

// assess runs one direct assessment through the REAL wired Jev binding:
// real authority check, real brief binding, real capture spans, one
// scripted provider call, real exchange log, real dynamic-row sink.
func (h *t26Harness) assess(t *testing.T, run t26Run, key, purpose string,
	questions []researchcontract.AssessQuestion, refs []researchcontract.EvidenceRef) researchcontract.Assessment {
	t.Helper()
	got, err := h.stack.Assessor.Assess(h.ctx, researchcontract.AssessInput{
		Purpose: purpose, Questions: questions,
		ProfileVersion: run.profile, RubricVersion: run.rubric,
		SourceRefs: refs, IdempotencyKey: key, RunID: run.id, Generation: run.gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// match runs one identity lookup through the REAL wired matcher: broad
// retrieval over the real store plus one real Jev comparison round.
func (h *t26Harness) match(t *testing.T, run t26Run,
	attrs researchcontract.MatchAttributes, refs ...researchcontract.EvidenceRef) researchcontract.MatchOutput {
	t.Helper()
	out, err := h.stack.Matcher.Match(h.ctx, researchcontract.MatchInput{
		Attributes: attrs, EvidenceRefs: refs, RunID: run.id, Generation: run.gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *t26Harness) save(t *testing.T, run t26Run, key string, items ...researchcontract.SaveItem) researchcontract.SaveOutput {
	t.Helper()
	out, err := h.saver.Save(h.ctx, researchcontract.SaveBatch{
		Items: items, IdempotencyKey: key, RunID: run.id, Generation: run.gen,
	})
	if err != nil {
		t.Fatalf("save %s: %v", key, err)
	}
	return out
}

func t26ExactIDs(out researchcontract.MatchOutput) map[string]string {
	got := map[string]string{}
	for _, m := range out.ExactMatches {
		got[m.RecordID] = m.Kind
	}
	return got
}

// ---------------------------------------------------------------------------
// Store readers (real persisted state assertions).
// ---------------------------------------------------------------------------

func t26Count(t *testing.T, s *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Read(context.Background(), func(r store.Reader) error {
		return r.QueryRowContext(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func t26OppRow(t *testing.T, s *store.Store, id string) (title, location, text, stage string, revision int64) {
	t.Helper()
	if err := s.Read(context.Background(), func(r store.Reader) error {
		return r.QueryRowContext(context.Background(),
			`SELECT title,location_text,original_text,stage,revision FROM opportunities WHERE id=?`, id,
		).Scan(&title, &location, &text, &stage, &revision)
	}); err != nil {
		t.Fatal(err)
	}
	return title, location, text, stage, revision
}

func t26Snapshots(t *testing.T, s *store.Store, oppID string) []string {
	t.Helper()
	var out []string
	if err := s.Read(context.Background(), func(r store.Reader) error {
		rows, err := r.QueryContext(context.Background(),
			`SELECT original_text FROM evidence_sources WHERE opportunity_id=? AND source_kind='vacancy_snapshot' ORDER BY rowid`, oppID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				return err
			}
			out = append(out, text)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func t26Sightings(t *testing.T, s *store.Store, oppID string) []store.RecordSighting {
	t.Helper()
	var out []store.RecordSighting
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		out, err = store.ListRecordSightingsByOpportunity(context.Background(), r, oppID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func t26Keys(t *testing.T, s *store.Store, oppID string) []store.EntityIdentityKey {
	t.Helper()
	var out []store.EntityIdentityKey
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		out, err = store.ListEntityIdentityKeysByOpportunity(context.Background(), r, oppID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func t26DecisionRefs(t *testing.T, s *store.Store, oppID string) (decision, refs string) {
	t.Helper()
	if err := s.Read(context.Background(), func(r store.Reader) error {
		return r.QueryRowContext(context.Background(),
			`SELECT decision,distinguishing_refs_json FROM identity_decisions WHERE subject_opportunity_id=? ORDER BY rowid DESC LIMIT 1`, oppID,
		).Scan(&decision, &refs)
	}); err != nil {
		t.Fatal(err)
	}
	return decision, refs
}

func t26DynamicAsm(t *testing.T, s *store.Store, id string) store.DynamicAssessment {
	t.Helper()
	var got store.DynamicAssessment
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		got, err = store.GetDynamicAssessment(context.Background(), r, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func t26AssessLinks(t *testing.T, s *store.Store, capID string) []store.AssessmentCaptureLink {
	t.Helper()
	var out []store.AssessmentCaptureLink
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		out, err = store.ListAssessmentsByCapture(context.Background(), r, capID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// ---------------------------------------------------------------------------
// Cross-source identity: C02 (true cross-post, different title wording).
// T04 expected: ONE identity; title wording is a sighting variant; 2nd
// save reuses the record ID; distinguishing evidence cited (same employer
// domain, same req NW-117, same Amsterdam location, overlapping text).
// ---------------------------------------------------------------------------

func TestT26_CrossSourceIdentity(t *testing.T) {
	h := newT26Harness(t)
	run := h.commission(t, "t26-c02-run", "T26 C02 cross-post.")
	careersURL := "https://example-careers-northwind.test/jobs/NW-117"
	aggURL := "https://example-aggregator.test/listings/88412-northwind-backend"

	// Two real captures over distinct retrieval paths.
	_, cap1 := h.fetch(t, run, "t26-c02-fetch-careers", h.board.url("/c02/careers"))
	_, cap2 := h.fetch(t, run, "t26-c02-fetch-agg", h.board.url("/c02/aggregator"))
	body1, body2 := h.captureBytes(t, cap1), h.captureBytes(t, cap2)
	if !strings.Contains(body1, "Backend Engineer") || !strings.Contains(body1, "NW-117") ||
		!strings.Contains(body2, "Sr. Backend Engineer (Python)") || !strings.Contains(body2, "req NW-117") {
		t.Fatal("captured bytes do not carry the controlled cross-post")
	}

	// No records yet: retrieval is empty and no Jev call is spent.
	before := h.match(t, run,
		researchcontract.MatchAttributes{Employer: "Northwind Technologies",
			Title: "Backend Engineer", URL: careersURL, RequisitionID: "NW-117", Location: "Amsterdam"},
		t26Ref(t, body1, cap1, "NW-117"))
	if before.Outcome != researchcontract.OutcomeOK || len(before.ExactMatches) != 0 {
		t.Fatalf("first match: %+v", before)
	}
	if h.provider.nCalls() != 0 {
		t.Fatalf("empty retrieval must not spend a Jev call, calls=%d", h.provider.nCalls())
	}

	first := h.save(t, run, "t26-c02-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Northwind Technologies"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, body1, cap1, "Northwind Technologies")},
			IdentityDecision: t26NewDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": careersURL, "originalText": body1,
				"locationText": "Amsterdam", "vacancyComplete": "true",
				"requisitionId": "NW-117", "reqIssuer": "northwind"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, body1, cap1, "NW-117")},
			IdentityDecision: t26NewDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first save: %+v", first)
	}
	oppID, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	// The aggregator wording resolves to the SAME vacancy through a real
	// Jev comparison over the real captures (keys retrieve, Jev decides).
	h.provider.setVerdict("cmp-opportunity-"+oppID, identity.AnswerSame)
	h.provider.setVerdict("cmp-company-"+coID, identity.AnswerSame)
	out := h.match(t, run,
		researchcontract.MatchAttributes{Employer: "Northwind Technologies",
			Title: "Sr. Backend Engineer (Python)", URL: aggURL,
			RequisitionID: "NW-117", Location: "Amsterdam"},
		t26Ref(t, body2, cap2, "req NW-117"))
	if !h.provider.saw("cmp-opportunity-" + oppID) {
		t.Fatal("cross-post merge needs a real semantic comparison of the saved record")
	}
	if exact := t26ExactIDs(out); exact[oppID] != "opportunity" {
		t.Fatalf("cross-post must merge despite title wording: %+v", out.ExactMatches)
	}

	reuse := h.save(t, run, "t26-c02-reuse", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 1,
		Fields: map[string]string{"originalText": body2,
			"sourceUrl": aggURL, "requisitionId": "NW-117", "reqIssuer": "northwind"},
		EvidenceLinks: []researchcontract.EvidenceLink{t26Link(t, body2, cap2, "Sr. Backend Engineer (Python)")},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 1},
			{CandidateID: coID, Kind: "company", Revision: 1},
		}},
	})
	if reuse.Outcome != researchcontract.OutcomeOK || reuse.Saved[0].RecordID != oppID {
		t.Fatalf("second sighting must reuse the record ID: %+v", reuse)
	}
	if n := t26Count(t, h.db, `SELECT count(*) FROM opportunities`); n != 1 {
		t.Fatalf("ONE vacancy identity required, got %d", n)
	}
	sightings := t26Sightings(t, h.db, oppID)
	if len(sightings) != 2 {
		t.Fatalf("TWO sightings required: %+v", sightings)
	}
	seen := map[string]bool{}
	for _, s := range sightings {
		seen[s.ContentSHA256] = true
	}
	if len(seen) != 2 || !seen[cap1] || !seen[cap2] {
		t.Fatalf("each sighting binds its own immutable capture: %+v", sightings)
	}
	snaps := t26Snapshots(t, h.db, oppID)
	if len(snaps) != 2 || !strings.Contains(snaps[1], "Sr. Backend Engineer (Python)") {
		t.Fatalf("title wording difference must survive as a sighting variant: %+v", snaps)
	}
	decision, refs := t26DecisionRefs(t, h.db, oppID)
	if decision != "same" {
		t.Fatalf("identity decision must be same, got %s", decision)
	}
	// Decision refs cite the capture ROW id; it must resolve to the
	// aggregator's immutable bytes (the distinguishing evidence).
	var cited []researchcontract.EvidenceRef
	if err := json.Unmarshal([]byte(refs), &cited); err != nil || len(cited) == 0 {
		t.Fatalf("decision refs must parse: %s %v", refs, err)
	}
	var content string
	if err := h.db.Read(h.ctx, func(r store.Reader) error {
		return r.QueryRowContext(h.ctx,
			`SELECT content_sha256 FROM source_captures WHERE id=?`, cited[0].CaptureID,
		).Scan(&content)
	}); err != nil {
		t.Fatal(err)
	}
	if content != cap2 {
		t.Fatalf("decision must cite the aggregator capture, resolves to %s", content)
	}
}

// ---------------------------------------------------------------------------
// Separate vacancies: D01 (same employer, overlapping titles) + D02
// (different employers, identical titles). T04 expected: TWO identities in
// each pair; title overlap alone never merges; saving both yields two IDs.
// ---------------------------------------------------------------------------

func TestT26_SeparateVacancies(t *testing.T) {
	t.Run("D01", func(t *testing.T) {
		h := newT26Harness(t)
		run := h.commission(t, "t26-d01-run", "T26 D01 hard negative.")
		beURL := "https://jobs.ashbyhq.com/myTomorrows/863ede5e-b578-431e-b036-a5ba69e501c5"
		srURL := "https://jobs.ashbyhq.com/myTomorrows/52f27ead-683f-4047-9185-f14c75324893"

		_, capBE := h.fetch(t, run, "t26-d01-fetch-be", h.board.url("/d01/backend"))
		_, capSR := h.fetch(t, run, "t26-d01-fetch-sr", h.board.url("/d01/senior"))
		bodyBE, bodySR := h.captureBytes(t, capBE), h.captureBytes(t, capSR)
		if !strings.Contains(bodyBE, "863ede5e-b578-431e-b036-a5ba69e501c5") ||
			!strings.Contains(bodySR, "52f27ead-683f-4047-9185-f14c75324893") {
			t.Fatal("captured bytes do not carry the distinguishing board records")
		}

		first := h.save(t, run, "t26-d01-first",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "myTomorrows"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyBE, capBE, "myTomorrows")},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
					"sourceUrl": beURL, "originalText": bodyBE,
					"locationText": "Amsterdam", "vacancyComplete": "true",
					"boardProvider": "ashby", "board": "myTomorrows",
					"boardRecordId": "863ede5e-b578-431e-b036-a5ba69e501c5"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyBE, capBE, "developing and maintaining the platform infrastructure")},
				IdentityDecision: t26NewDecision()},
		)
		if first.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("first save: %+v", first)
		}
		oppBE, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

		// The senior role retrieves the backend record (title/employer
		// overlap; no board-record pin exists at retrieval — T14 known
		// gap) and the real Jev comparison rules it distinct.
		h.provider.setVerdict("cmp-opportunity-"+oppBE, identity.AnswerDistinct)
		h.provider.setVerdict("cmp-company-"+coID, identity.AnswerSame)
		out := h.match(t, run,
			researchcontract.MatchAttributes{Employer: "myTomorrows",
				Title: "Senior Backend Engineer", URL: srURL, Location: "Amsterdam"},
			t26Ref(t, bodySR, capSR, "prior tech-lead scope"))
		if !h.provider.saw("cmp-opportunity-" + oppBE) {
			t.Fatal("hard negative needs a real semantic comparison of the overlapping record")
		}
		if exact := t26ExactIDs(out); exact[oppBE] != "" {
			t.Fatalf("title-word overlap must NOT merge distinct roles: %+v", out.ExactMatches)
		}
		for _, p := range out.PossibleMatches {
			if p.RecordID == oppBE {
				t.Fatalf("confirmed-distinct role must not surface as possible: %+v", out.PossibleMatches)
			}
		}
		if exact := t26ExactIDs(out); exact[coID] != "company" {
			t.Fatalf("same employer must still match: %+v", out.ExactMatches)
		}

		second := h.save(t, run, "t26-d01-second", researchcontract.SaveItem{
			Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": coID, "title": "Senior Backend Engineer",
				"sourceUrl": srURL, "originalText": bodySR,
				"locationText": "Amsterdam", "vacancyComplete": "true",
				"boardProvider": "ashby", "board": "myTomorrows",
				"boardRecordId": "52f27ead-683f-4047-9185-f14c75324893"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodySR, capSR, "prior tech-lead scope")},
			IdentityDecision: t26NewDecision(),
		})
		if second.Outcome != researchcontract.OutcomeOK || second.Saved[0].RecordID == oppBE {
			t.Fatalf("distinct role needs its own record ID: %+v", second)
		}
		if n := t26Count(t, h.db, `SELECT count(*) FROM opportunities`); n != 2 {
			t.Fatalf("TWO vacancy identities required, got %d", n)
		}
		// Save-time board distinction: reusing the first board record id
		// for a new create converges instead of duplicating.
		dup := h.save(t, run, "t26-d01-dup", researchcontract.SaveItem{
			Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": coID, "title": "Backend Engineer",
				"sourceUrl": beURL, "originalText": bodyBE,
				"boardProvider": "ashby", "board": "myTomorrows",
				"boardRecordId": "863ede5e-b578-431e-b036-a5ba69e501c5"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(bodyBE, capBE)},
			IdentityDecision: t26NewDecision(),
		})
		if dup.Outcome != researchcontract.OutcomeIdentityAmbiguous {
			t.Fatalf("duplicate board record id must converge: %+v", dup)
		}
		if n := t26Count(t, h.db, `SELECT count(*) FROM opportunities`); n != 2 {
			t.Fatalf("convergence must leave exactly two records, got %d", n)
		}
	})

	t.Run("D02", func(t *testing.T) {
		h := newT26Harness(t)
		run := h.commission(t, "t26-d02-run", "T26 D02 hard negative.")
		acmeURL := "https://example-careers-acme.test/jobs/ACME-BE-9"
		brewURL := "https://example-careers-brouwerij.test/vacatures/VBB-14"

		_, capAcme := h.fetch(t, run, "t26-d02-fetch-acme", h.board.url("/d02/acme"))
		_, capBrew := h.fetch(t, run, "t26-d02-fetch-brew", h.board.url("/d02/brewer"))
		bodyAcme, bodyBrew := h.captureBytes(t, capAcme), h.captureBytes(t, capBrew)

		first := h.save(t, run, "t26-d02-first",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "Acme Robotics"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyAcme, capAcme, "Acme Robotics")},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
					"sourceUrl": acmeURL, "originalText": bodyAcme,
					"locationText": "Eindhoven", "vacancyComplete": "true",
					"requisitionId": "ACME-BE-9", "reqIssuer": "acme"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyAcme, capAcme, "Go, Kubernetes")},
				IdentityDecision: t26NewDecision()},
		)
		if first.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("first save: %+v", first)
		}
		oppAcme := first.Saved[1].RecordID

		h.provider.setVerdict("cmp-opportunity-"+oppAcme, identity.AnswerDistinct)
		out := h.match(t, run,
			researchcontract.MatchAttributes{Employer: "De Voorbeeld Brouwerij",
				Title: "Backend Engineer", URL: brewURL, Location: "Rotterdam"},
			t26Ref(t, bodyBrew, capBrew, "PHP, Laravel"))
		if !h.provider.saw("cmp-opportunity-" + oppAcme) {
			t.Fatal("identical-title pair needs a real semantic comparison")
		}
		if exact := t26ExactIDs(out); exact[oppAcme] != "" {
			t.Fatalf("identical titles must not merge different employers: %+v", out.ExactMatches)
		}

		second := h.save(t, run, "t26-d02-second",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "De Voorbeeld Brouwerij"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyBrew, capBrew, "De Voorbeeld Brouwerij")},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
					"sourceUrl": brewURL, "originalText": bodyBrew,
					"locationText": "Rotterdam", "vacancyComplete": "true",
					"requisitionId": "VBB-14", "reqIssuer": "brouwerij"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyBrew, capBrew, "PHP, Laravel")},
				IdentityDecision: t26NewDecision()},
		)
		if second.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("second save: %+v", second)
		}
		if n := t26Count(t, h.db, `SELECT count(*) FROM opportunities`); n != 2 {
			t.Fatalf("TWO vacancy identities required, got %d", n)
		}
		if n := t26Count(t, h.db, `SELECT count(*) FROM companies`); n != 2 {
			t.Fatalf("two employers required, got %d", n)
		}
	})
}

// ---------------------------------------------------------------------------
// Source conflict, same identity: A03 (Amsterdam vs Rotterdam, one req).
// T04 expected: ONE identity with a flagged location conflict; both
// locations stay visible with sources; location suitability UNKNOWN.
// ---------------------------------------------------------------------------

func TestT26_SourceConflictSameIdentity(t *testing.T) {
	h := newT26Harness(t)
	run := h.commission(t, "t26-a03-run", "T26 A03 location conflict.")
	empURL := "https://example-careers-northwind.test/jobs/LOC-55"
	aggURL := "https://example-aggregator.test/listings/99120-northwind"

	_, capAms := h.fetch(t, run, "t26-a03-fetch-ams", h.board.url("/a03/ams"))
	_, capRtm := h.fetch(t, run, "t26-a03-fetch-rtm", h.board.url("/a03/rtm"))
	bodyAms, bodyRtm := h.captureBytes(t, capAms), h.captureBytes(t, capRtm)

	first := h.save(t, run, "t26-a03-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Northwind"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyAms, capAms, "LOC-55")},
			IdentityDecision: t26NewDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": empURL, "originalText": bodyAms,
				"locationText": "Amsterdam", "vacancyComplete": "true",
				"requisitionId": "LOC-55", "reqIssuer": "northwind"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyAms, capAms, "Location: Amsterdam")},
			IdentityDecision: t26NewDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first save: %+v", first)
	}
	oppID, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	h.provider.setVerdict("cmp-opportunity-"+oppID, identity.AnswerSame)
	h.provider.setVerdict("cmp-company-"+coID, identity.AnswerSame)
	out := h.match(t, run,
		researchcontract.MatchAttributes{Employer: "Northwind", Title: "Backend Engineer",
			URL: aggURL, RequisitionID: "LOC-55", Location: "Rotterdam"},
		t26Ref(t, bodyRtm, capRtm, "Location: Rotterdam"))
	if !h.provider.saw("cmp-opportunity-" + oppID) {
		t.Fatal("location conflict still needs a real semantic comparison")
	}
	if exact := t26ExactIDs(out); exact[oppID] != "opportunity" {
		t.Fatalf("location conflict must not split the identity: %+v", out.ExactMatches)
	}

	second := h.save(t, run, "t26-a03-second", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 1,
		Fields: map[string]string{"locationText": "Rotterdam",
			"originalText": bodyRtm, "sourceUrl": aggURL,
			"requisitionId": "LOC-55", "reqIssuer": "northwind",
			"notes": "location conflicts with the employer page (Amsterdam); unresolved"},
		EvidenceLinks: []researchcontract.EvidenceLink{t26Link(t, bodyRtm, capRtm, "Location: Rotterdam")},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 1},
		}},
	})
	if second.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("conflict sighting save: %+v", second)
	}
	if n := t26Count(t, h.db, `SELECT count(*) FROM opportunities`); n != 1 {
		t.Fatalf("ONE identity required, got %d", n)
	}
	snaps := t26Snapshots(t, h.db, oppID)
	if len(snaps) != 2 || !strings.Contains(snaps[0], "Amsterdam") || !strings.Contains(snaps[1], "Rotterdam") {
		t.Fatalf("both locations must stay visible with their sources: %+v", snaps)
	}
	if sightings := t26Sightings(t, h.db, oppID); len(sightings) != 2 {
		t.Fatalf("both sightings must be retained: %+v", sightings)
	}
	if _, _, _, stage, _ := t26OppRow(t, h.db, oppID); stage != store.UnassessedStage {
		t.Fatalf("location suitability must stay UNKNOWN (unassessed), stage=%q", stage)
	}
	// Carried T21 need (b), confirmed present on the wired stack: the
	// conflict is visible via snapshots+sightings+notes, but no dedicated
	// conflicting claim flag row exists. Not a T26 failure.
}

// ---------------------------------------------------------------------------
// Conflicting evidence: A02 (vacancy vs same-date employer clarification).
// T04 expected: frontend-duty status CONFLICTING with BOTH sources cited;
// dependent suitability UNKNOWN (abstained).
// ---------------------------------------------------------------------------

func TestT26_ConflictingEvidenceBinding(t *testing.T) {
	h := newT26Harness(t)
	run := h.commission(t, "t26-a02-run", "T26 A02 conflict.")
	vacURL := "https://example.test/a02/vacancy"
	clarURL := "https://example.test/a02/clarification"

	_, capVac := h.fetch(t, run, "t26-a02-fetch-vac", h.board.url("/a02/vac"))
	_, capClar := h.fetch(t, run, "t26-a02-fetch-clar", h.board.url("/a02/clar"))
	bodyVac, bodyClar := h.captureBytes(t, capVac), h.captureBytes(t, capClar)

	// One exact span cannot support two competing alternatives of one
	// question, so the conflict verdict cites the full captures while the
	// single-sided verdicts cite their sentences (T09 documented rule).
	vacRef := t26Ref(t, bodyVac, capVac, "no frontend implementation duties")
	clarRef := t26Ref(t, bodyClar, capClar, "maintain our React UI one day each week")
	vacFull, clarFull := t26FullRef(bodyVac, capVac), t26FullRef(bodyClar, capClar)
	h.provider.setVerdict("q-frontend", "conflicting")
	h.provider.setVerdict("q-suitability", jevassess.AbstainID)
	got := h.assess(t, run, "t26-a02-assess", "role_fit",
		[]researchcontract.AssessQuestion{
			{ID: "q-frontend", Text: "Are frontend implementation duties assigned?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "present", Label: "Present per the employer clarification",
						EvidenceRefs: []researchcontract.EvidenceRef{clarRef}},
					{ID: "absent", Label: "Absent per the vacancy snapshot",
						EvidenceRefs: []researchcontract.EvidenceRef{vacRef}},
					{ID: "conflicting", Label: "Conflicting: incompatible same-role claims",
						EvidenceRefs: []researchcontract.EvidenceRef{vacFull, clarFull}},
				}},
			{ID: "q-suitability", Text: "Is the role suitable on frontend duties?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "qualified", Label: "Qualified"},
					{ID: "excluded", Label: "Excluded"},
				}},
		},
		[]researchcontract.EvidenceRef{vacFull, clarFull})
	byQ := map[string]researchcontract.AssessAnswer{}
	for _, a := range got.Results {
		byQ[a.QuestionID] = a
	}
	conflict := byQ["q-frontend"]
	if conflict.AnswerID != "conflicting" || conflict.Abstained {
		t.Fatalf("conflict verdict required: %+v", conflict)
	}
	seen := map[string]bool{}
	for _, b := range conflict.SourceBindings {
		seen[b.CaptureID] = true
	}
	if len(conflict.SourceBindings) != 2 || !seen[capVac] || !seen[capClar] {
		t.Fatalf("both incompatible sources must be cited: %+v", conflict.SourceBindings)
	}
	if suit := byQ["q-suitability"]; !suit.Abstained || suit.AnswerID != "" || len(suit.SourceBindings) != 0 {
		t.Fatalf("dependent suitability must stay UNKNOWN: %+v", suit)
	}
	row := t26DynamicAsm(t, h.db, got.ID)
	if row.Status != "partial_abstain" || len(row.ReuseKey) != 64 {
		t.Fatalf("abstention must persist as partial_abstain with reuse key: %+v", row)
	}
	if !strings.Contains(row.EvidenceRefsJSON, capVac) || !strings.Contains(row.EvidenceRefsJSON, capClar) {
		t.Fatalf("assessment row must bind both captures: %s", row.EvidenceRefsJSON)
	}
	for _, cap := range []string{capVac, capClar} {
		links := t26AssessLinks(t, h.db, cap)
		if len(links) == 0 || links[0].AssessmentID != got.ID {
			t.Fatalf("capture %s must link the assessment: %+v", cap, links)
		}
	}

	// Both sources persist as snapshots on ONE record; the multi-capture
	// assessment binds the save, so the update cites both captures.
	saved := h.save(t, run, "t26-a02-save",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Example Employer"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(bodyVac, capVac)},
			IdentityDecision: t26NewDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": vacURL, "originalText": bodyVac, "vacancyComplete": "true"},
			EvidenceLinks: []researchcontract.EvidenceLink{
				t26FullLink(bodyVac, capVac), t26FullLink(bodyClar, capClar)},
			AssessmentIDs:    []string{got.ID},
			IdentityDecision: t26NewDecision()},
	)
	if saved.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("conflict save: %+v", saved)
	}
	oppID := saved.Saved[1].RecordID
	clarified := h.save(t, run, "t26-a02-clarify", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 1,
		Fields: map[string]string{"originalText": bodyClar, "sourceUrl": clarURL,
			"notes": "employer clarification conflicts with the vacancy snapshot; unresolved"},
		EvidenceLinks: []researchcontract.EvidenceLink{
			t26FullLink(bodyVac, capVac), t26FullLink(bodyClar, capClar)},
		AssessmentIDs: []string{got.ID},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 1},
		}},
	})
	if clarified.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("clarification save: %+v", clarified)
	}
	snaps := t26Snapshots(t, h.db, oppID)
	if len(snaps) != 2 || !strings.Contains(snaps[0], "no frontend implementation duties") ||
		!strings.Contains(snaps[1], "maintain our React UI") {
		t.Fatalf("both incompatible claims must be preserved with sources: %+v", snaps)
	}
}

// ---------------------------------------------------------------------------
// Legitimate refresh: R01 (same URL/req, materially updated claims).
// T04 expected: SAME identity; new sighting appended; claims versioned
// (v1 preserved); suitability reassessed; v1 assessment never rebound.
// The two versions arrive over distinct retrieval paths; both saves claim
// the same canonical posting URL, preserving the R01 identity semantics.
// ---------------------------------------------------------------------------

func TestT26_LegitimateRefresh(t *testing.T) {
	h := newT26Harness(t)
	run := h.commission(t, "t26-r01-run", "T26 R01 refresh.")
	postURL := "https://example-careers-northwind.test/jobs/NW-204"

	_, capV1 := h.fetch(t, run, "t26-r01-fetch-v1", h.board.url("/r01/v1"))
	_, capV2 := h.fetch(t, run, "t26-r01-fetch-v2", h.board.url("/r01/v2"))
	bodyV1, bodyV2 := h.captureBytes(t, capV1), h.captureBytes(t, capV2)
	if !strings.Contains(bodyV1, "Amsterdam, hybrid") || !strings.Contains(bodyV2, "Remote within EU") {
		t.Fatal("captured bytes do not carry the v1/v2 claims")
	}

	h.provider.setVerdict("q-r01-loc-v1", "present")
	asmV1 := h.assess(t, run, "t26-r01-assess-v1", "role_fit",
		[]researchcontract.AssessQuestion{
			{ID: "q-r01-loc-v1", Text: "Is the role Amsterdam hybrid?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "present", Label: "Present: Amsterdam, hybrid",
						EvidenceRefs: []researchcontract.EvidenceRef{t26Ref(t, bodyV1, capV1, "Amsterdam, hybrid")}},
					{ID: "absent", Label: "Absent"},
				}},
		},
		[]researchcontract.EvidenceRef{t26FullRef(bodyV1, capV1)})
	if asmV1.Results[0].AnswerID != "present" {
		t.Fatalf("v1 assessment: %+v", asmV1.Results)
	}

	first := h.save(t, run, "t26-r01-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Northwind Technologies"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyV1, capV1, "Northwind")},
			IdentityDecision: t26NewDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": postURL, "originalText": bodyV1,
				"locationText": "Amsterdam, hybrid", "vacancyComplete": "true",
				"requisitionId": "NW-204", "reqIssuer": "northwind"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyV1, capV1, "Amsterdam, hybrid")},
			AssessmentIDs:    []string{asmV1.ID},
			IdentityDecision: t26NewDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("v1 save: %+v", first)
	}
	oppID, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	h.provider.setVerdict("cmp-opportunity-"+oppID, identity.AnswerSame)
	h.provider.setVerdict("cmp-company-"+coID, identity.AnswerSame)
	out := h.match(t, run,
		researchcontract.MatchAttributes{Employer: "Northwind Technologies",
			Title: "Backend Engineer", URL: postURL, RequisitionID: "NW-204", Location: "Remote within EU"},
		t26Ref(t, bodyV2, capV2, "Remote within EU"))
	if !h.provider.saw("cmp-opportunity-" + oppID) {
		t.Fatal("changed posting needs a real semantic comparison")
	}
	if exact := t26ExactIDs(out); exact[oppID] != "opportunity" {
		t.Fatalf("changed posting must stay the same identity: %+v", out.ExactMatches)
	}

	v2 := h.save(t, run, "t26-r01-v2", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 1,
		Fields: map[string]string{"originalText": bodyV2,
			"locationText": "Remote within EU", "sourceUrl": postURL,
			"requisitionId": "NW-204", "reqIssuer": "northwind",
			"compCurrency": "EUR", "compMinCents": "7000000", "compMaxCents": "8500000",
			"compPeriod": "year", "compBasis": "base"},
		EvidenceLinks: []researchcontract.EvidenceLink{t26Link(t, bodyV2, capV2, "EUR 70,000-85,000 per year")},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 1},
			{CandidateID: coID, Kind: "company", Revision: 1},
		}},
	})
	if v2.Outcome != researchcontract.OutcomeOK || v2.Saved[0].RecordID != oppID {
		t.Fatalf("v2 must update the same record: %+v", v2)
	}
	if _, location, _, _, revision := t26OppRow(t, h.db, oppID); location != "Remote within EU" || revision != 2 {
		t.Fatalf("row must show v2 claims at revision 2: %q rev=%d", location, revision)
	}
	snaps := t26Snapshots(t, h.db, oppID)
	if len(snaps) != 2 || !strings.Contains(snaps[0], "Amsterdam, hybrid") ||
		!strings.Contains(snaps[1], "Remote within EU") {
		t.Fatalf("v1 claims must be preserved alongside v2: %+v", snaps)
	}
	if sightings := t26Sightings(t, h.db, oppID); len(sightings) != 2 {
		t.Fatalf("a new sighting must be appended: %+v", sightings)
	}
	var currency, period string
	var minCents, maxCents int64
	if err := h.db.Read(h.ctx, func(r store.Reader) error {
		return r.QueryRowContext(h.ctx,
			`SELECT currency,min_amount_cents,max_amount_cents,period FROM compensation WHERE opportunity_id=?`, oppID,
		).Scan(&currency, &minCents, &maxCents, &period)
	}); err != nil {
		t.Fatal(err)
	}
	if currency != "EUR" || minCents != 7000000 || maxCents != 8500000 || period != "year" {
		t.Fatalf("v2 pay must persist with units: %s %d-%d/%s", currency, minCents, maxCents, period)
	}

	// The v1 assessment binds v1 evidence: citing it for v2 claims is a
	// rebind attempt and must fail, never silently transfer.
	rebind := h.save(t, run, "t26-r01-rebind", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 2,
		Fields:        map[string]string{"notes": "rebind probe"},
		EvidenceLinks: []researchcontract.EvidenceLink{t26FullLink(bodyV2, capV2)},
		AssessmentIDs: []string{asmV1.ID},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 2},
		}},
	})
	if rebind.Outcome != researchcontract.OutcomeRevisionConflict {
		t.Fatalf("v1 assessment must not rebind to v2: %+v", rebind)
	}
	// Reassessment against the new claims binds and saves.
	h.provider.setVerdict("q-r01-loc-v2", "present")
	asmV2 := h.assess(t, run, "t26-r01-assess-v2", "role_fit",
		[]researchcontract.AssessQuestion{
			{ID: "q-r01-loc-v2", Text: "Is the role remote within EU?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "present", Label: "Present: Remote within EU",
						EvidenceRefs: []researchcontract.EvidenceRef{t26Ref(t, bodyV2, capV2, "Remote within EU")}},
					{ID: "absent", Label: "Absent"},
				}},
		},
		[]researchcontract.EvidenceRef{t26FullRef(bodyV2, capV2)})
	reassessed := h.save(t, run, "t26-r01-reassessed", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 2,
		Fields:        map[string]string{"notes": "reassessed against v2"},
		EvidenceLinks: []researchcontract.EvidenceLink{t26FullLink(bodyV2, capV2)},
		AssessmentIDs: []string{asmV2.ID},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 2},
		}},
	})
	if reassessed.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("reassessed v2 save: %+v", reassessed)
	}
}

// ---------------------------------------------------------------------------
// Historical captures on reuse: R02 (req ENG-2041 backend, then frontend)
// + R03 (one slug, backend then data role). T04 expected: NEW identity in
// each case; the reused identifier becomes qualified history (never a
// unique key); the old record and its sighting stay intact.
// ---------------------------------------------------------------------------

func TestT26_HistoricalCapturesOnReuse(t *testing.T) {
	t.Run("R02", func(t *testing.T) {
		h := newT26Harness(t)
		run := h.commission(t, "t26-r02-run", "T26 R02 req reuse.")
		reqURL := "https://example-careers-initech.test/jobs/ENG-2041"

		_, capGo := h.fetch(t, run, "t26-r02-fetch-go", h.board.url("/r02/backend"))
		_, capReact := h.fetch(t, run, "t26-r02-fetch-react", h.board.url("/r02/frontend"))
		bodyGo, bodyReact := h.captureBytes(t, capGo), h.captureBytes(t, capReact)

		first := h.save(t, run, "t26-r02-first",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "Initech"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyGo, capGo, "ENG-2041")},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer (Go)",
					"sourceUrl": reqURL, "originalText": bodyGo,
					"vacancyComplete": "true", "requisitionId": "ENG-2041", "reqIssuer": "initech"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyGo, capGo, "Backend Engineer (Go)")},
				IdentityDecision: t26NewDecision()},
		)
		if first.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("old save: %+v", first)
		}
		oppGo, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

		h.provider.setVerdict("cmp-opportunity-"+oppGo, identity.AnswerDistinct)
		h.provider.setVerdict("cmp-company-"+coID, identity.AnswerSame)
		out := h.match(t, run,
			researchcontract.MatchAttributes{Employer: "Initech",
				Title: "Frontend Engineer (React)", URL: reqURL, RequisitionID: "ENG-2041"},
			t26Ref(t, bodyReact, capReact, "Frontend Engineer (React)"))
		if !h.provider.saw("cmp-opportunity-" + oppGo) {
			t.Fatal("reused req id needs a real semantic comparison")
		}
		if exact := t26ExactIDs(out); exact[oppGo] != "" {
			t.Fatalf("reused req id must not merge materially different content: %+v", out.ExactMatches)
		}

		// Without the explicit reuse signal the shared strong key
		// converges: no silent overwrite is possible.
		guard := h.save(t, run, "t26-r02-guard", researchcontract.SaveItem{
			Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": coID, "title": "Frontend Engineer (React)",
				"sourceUrl": reqURL, "originalText": bodyReact,
				"requisitionId": "ENG-2041", "reqIssuer": "initech"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(bodyReact, capReact)},
			IdentityDecision: t26NewDecision(),
		})
		if guard.Outcome != researchcontract.OutcomeIdentityAmbiguous {
			t.Fatalf("unflagged req reuse must converge: %+v", guard)
		}

		reuse := h.save(t, run, "t26-r02-reuse", researchcontract.SaveItem{
			Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": coID, "title": "Frontend Engineer (React)",
				"sourceUrl": reqURL, "originalText": bodyReact,
				"requisitionId": "ENG-2041", "reqIssuer": "initech", "reusedIdentifier": "true",
				"notes": "ENG-2041 now advertises a React frontend role; Go backend opening superseded by reuse"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyReact, capReact, "Materially different opening reusing req ID ENG-2041")},
			IdentityDecision: t26NewDecision(),
		})
		if reuse.Outcome != researchcontract.OutcomeOK || reuse.Saved[0].RecordID == oppGo {
			t.Fatalf("reused req needs a NEW record: %+v", reuse)
		}
		oppReact := reuse.Saved[0].RecordID
		if n := t26Count(t, h.db, `SELECT count(*) FROM opportunities`); n != 2 {
			t.Fatalf("two identities required, got %d", n)
		}
		if title, _, _, _, _ := t26OppRow(t, h.db, oppGo); title != "Backend Engineer (Go)" {
			t.Fatalf("old record must keep its historical content: %q", title)
		}
		oldKeys, newKeys := t26Keys(t, h.db, oppGo), t26Keys(t, h.db, oppReact)
		var oldReq, newReq *store.EntityIdentityKey
		for i := range oldKeys {
			if strings.HasPrefix(oldKeys[i].Namespace, "issuer_req_id:") {
				oldReq = &oldKeys[i]
			}
		}
		for i := range newKeys {
			if strings.HasPrefix(newKeys[i].Namespace, "issuer_req_id:") {
				newReq = &newKeys[i]
			}
		}
		if oldReq == nil || oldReq.Status != store.IdentityKeySuperseded {
			t.Fatalf("old req key must be superseded history: %+v", oldKeys)
		}
		if newReq == nil || newReq.Status != store.IdentityKeyCurrent || newReq.SupersedesID != oldReq.ID {
			t.Fatalf("new req key must chain to the old row: %+v", newKeys)
		}
		if sightings := t26Sightings(t, h.db, oppReact); len(sightings) != 1 ||
			sightings[0].SightingKind != store.SightingReusedIdentifier {
			t.Fatalf("reuse sighting must be marked: %+v", sightings)
		}
		if kept := t26Sightings(t, h.db, oppGo); len(kept) != 1 {
			t.Fatalf("historical sighting must be retained: %+v", kept)
		}
	})

	t.Run("R03", func(t *testing.T) {
		h := newT26Harness(t)
		run := h.commission(t, "t26-r03-run", "T26 R03 slug reuse.")
		slug := "https://example-careers-initech.test/openings/senior-engineer"

		_, capBE := h.fetch(t, run, "t26-r03-fetch-be", h.board.url("/r03/backend"))
		_, capData := h.fetch(t, run, "t26-r03-fetch-data", h.board.url("/r03/data"))
		bodyBE, bodyData := h.captureBytes(t, capBE), h.captureBytes(t, capData)

		first := h.save(t, run, "t26-r03-first",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "Initech"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyBE, capBE, "BE-102")},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
					"sourceUrl": slug, "originalText": bodyBE,
					"vacancyComplete": "true", "requisitionId": "BE-102", "reqIssuer": "initech"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyBE, capBE, "Req BE-102")},
				IdentityDecision: t26NewDecision()},
		)
		if first.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("old save: %+v", first)
		}
		oppBE, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

		h.provider.setVerdict("cmp-opportunity-"+oppBE, identity.AnswerDistinct)
		h.provider.setVerdict("cmp-company-"+coID, identity.AnswerSame)
		out := h.match(t, run,
			researchcontract.MatchAttributes{Employer: "Initech", Title: "Senior Data Engineer",
				URL: slug, RequisitionID: "DA-330"},
			t26Ref(t, bodyData, capData, "Req DA-330"))
		if !h.provider.saw("cmp-opportunity-" + oppBE) {
			t.Fatal("reused slug needs a real semantic comparison")
		}
		if exact := t26ExactIDs(out); exact[oppBE] != "" {
			t.Fatalf("URL equality must not merge a new opening: %+v", out.ExactMatches)
		}

		guard := h.save(t, run, "t26-r03-guard", researchcontract.SaveItem{
			Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": coID, "title": "Senior Data Engineer",
				"sourceUrl": slug, "originalText": bodyData,
				"requisitionId": "DA-330", "reqIssuer": "initech"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(bodyData, capData)},
			IdentityDecision: t26NewDecision(),
		})
		if guard.Outcome != researchcontract.OutcomeIdentityAmbiguous {
			t.Fatalf("unflagged URL reuse must converge: %+v", guard)
		}

		reuse := h.save(t, run, "t26-r03-reuse", researchcontract.SaveItem{
			Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": coID, "title": "Senior Data Engineer",
				"sourceUrl": slug, "originalText": bodyData,
				"requisitionId": "DA-330", "reqIssuer": "initech", "reusedIdentifier": "true"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyData, capData, "Same URL slug, different opening")},
			IdentityDecision: t26NewDecision(),
		})
		if reuse.Outcome != researchcontract.OutcomeOK || reuse.Saved[0].RecordID == oppBE {
			t.Fatalf("reused slug needs a NEW record: %+v", reuse)
		}
		oldKeys := t26Keys(t, h.db, oppBE)
		var oldURL *store.EntityIdentityKey
		for i := range oldKeys {
			if oldKeys[i].Namespace == store.IdentityNamespaceCanonicalURL {
				oldURL = &oldKeys[i]
			}
		}
		if oldURL == nil || oldURL.Status != store.IdentityKeySuperseded {
			t.Fatalf("old URL key must be retained as superseded history: %+v", oldKeys)
		}
		if prior := t26Sightings(t, h.db, oppBE); len(prior) != 1 || len(prior[0].ContentSHA256) != 64 {
			t.Fatalf("historical sighting with hash must be retained: %+v", prior)
		}
		if n := t26Count(t, h.db, `SELECT count(*) FROM opportunities`); n != 2 {
			t.Fatalf("two identities required, got %d", n)
		}
	})
}

// ---------------------------------------------------------------------------
// Brief change without refetch: B01. T04 expected: NO refetch; a NEW
// assessment binds brief v2 while v1 stays immutable; v1 evidence under
// the v2 brief fails stale; one capture carries two assessments.
// The known T23 limitation applies here: the wired assessor's Supersedes
// resolver is nil, so the v2 row cannot chain to v1. The test verifies
// around it and pins the exact gap.
// ---------------------------------------------------------------------------

func TestT26_BriefChangeReassessment(t *testing.T) {
	h := newT26Harness(t)
	if h.stack.Assessor.Supersedes != nil {
		t.Fatal("precondition for the limitation probe: wired Supersedes resolver must be nil")
	}
	crispURL := "https://example.test/b01/crisp"
	run1 := h.commission(t, "t26-b01-run1", "T26 B01 brief v1.")

	_, capCrisp := h.fetch(t, run1, "t26-b01-fetch", h.board.url("/b01/crisp"))
	body := h.captureBytes(t, capCrisp)
	frontRef := t26Ref(t, body, capCrisp, "Code across our stack - frontend and backend.")
	hoursRef := t26Ref(t, body, capCrisp, "36-40 hours per week")

	h.provider.setVerdict("q-b01-frontend-v1", "present")
	h.provider.setVerdict("q-b01-hours-v1", "h36_40")
	asmV1 := h.assess(t, run1, "t26-b01-assess-v1", "role_fit",
		[]researchcontract.AssessQuestion{
			{ID: "q-b01-frontend-v1", Text: "Are frontend implementation duties assigned?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "present", Label: "Present: explicit frontend code across the stack",
						EvidenceRefs: []researchcontract.EvidenceRef{frontRef}},
					{ID: "absent", Label: "Absent: no frontend implementation duties"},
				}},
			{ID: "q-b01-hours-v1", Text: "What weekly hours are required?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "h36_40", Label: "36-40 hours per week",
						EvidenceRefs: []researchcontract.EvidenceRef{hoursRef}},
					{ID: "other", Label: "Other"},
				}},
		},
		[]researchcontract.EvidenceRef{t26FullRef(body, capCrisp)})
	byV1 := map[string]researchcontract.AssessAnswer{}
	for _, a := range asmV1.Results {
		byV1[a.QuestionID] = a
	}
	if byV1["q-b01-frontend-v1"].AnswerID != "present" || byV1["q-b01-hours-v1"].AnswerID != "h36_40" {
		t.Fatalf("v1 assessment: %+v", asmV1.Results)
	}
	rowV1 := t26DynamicAsm(t, h.db, asmV1.ID)
	if rowV1.ProfileVersion != run1.profile || rowV1.SupersedesID != "" {
		t.Fatalf("v1 row must bind brief v1 with no predecessor: %+v", rowV1)
	}

	// The owner edits the brief: frontend duties are now avoided and 32h
	// preferred. A new run commissions against brief v2.
	current, err := h.db.CurrentPreferences(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.RoleCriteria = append(append([]store.RoleCriterion{}, current.RoleCriteria...),
		store.RoleCriterion{ID: "no-frontend", Label: "No frontend duties",
			Description: "32h preferred; frontend implementation duties excluded", Kind: "responsibility", Mode: "avoid"})
	updated, _, err := h.db.UpdatePreferences(h.ctx, current.Version, next, h.owner)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != current.Version+1 {
		t.Fatalf("brief edit must bump the profile version: %+v", updated)
	}
	// One active round at a time: run1's work is done (its assessment
	// persists), so it stops and completes — freeing the slot for run2
	// under the edited brief. (No product completion path commissions
	// research runs terminally yet; the generic lifecycle API stands in.)
	if _, err := h.stack.Supervisor.Stop(h.ctx, h.owner, run1.id, "T26 B01 v1 work done"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.FinishRound(h.ctx, h.owner, run1.id, store.RoundCompleted,
		"t26-b01-v1-done", "assessment-persisted", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	run2 := h.commission(t, "t26-b01-run2", "T26 B01 brief v2.")
	if run2.profile != updated.Version || run2.rubric == run1.rubric {
		t.Fatalf("run2 must bind brief v2, run1=%+v run2=%+v", run1, run2)
	}

	// Reassessment cites the SAME capture: no refetch occurs.
	h.provider.setVerdict("q-b01-frontend-v2", "present")
	h.provider.setVerdict("q-b01-fit-v2", "excluded_by_v2")
	asmV2 := h.assess(t, run2, "t26-b01-assess-v2", "role_fit",
		[]researchcontract.AssessQuestion{
			{ID: "q-b01-frontend-v2", Text: "Are frontend implementation duties assigned?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "present", Label: "Present: explicit frontend code across the stack",
						EvidenceRefs: []researchcontract.EvidenceRef{frontRef}},
					{ID: "absent", Label: "Absent: no frontend implementation duties"},
				}},
			{ID: "q-b01-fit-v2", Text: "Is the role suitable under brief v2?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "qualified", Label: "Qualified"},
					{ID: "excluded_by_v2", Label: "Excluded: brief v2 avoids frontend duties and prefers 32h",
						EvidenceRefs: []researchcontract.EvidenceRef{frontRef, hoursRef}},
				}},
		},
		[]researchcontract.EvidenceRef{t26FullRef(body, capCrisp)})
	byV2 := map[string]researchcontract.AssessAnswer{}
	for _, a := range asmV2.Results {
		byV2[a.QuestionID] = a
	}
	if byV2["q-b01-frontend-v2"].AnswerID != "present" || byV2["q-b01-fit-v2"].AnswerID != "excluded_by_v2" {
		t.Fatalf("v2 assessment must change the outcome on the stated ground: %+v", asmV2.Results)
	}
	if n := t26Count(t, h.db, `SELECT count(*) FROM source_captures`); n != 1 {
		t.Fatalf("reassessment must reuse the one capture without refetching, captures=%d", n)
	}
	rowV2 := t26DynamicAsm(t, h.db, asmV2.ID)
	if rowV2.ProfileVersion != run2.profile || rowV2.RubricVersion != run2.rubric {
		t.Fatalf("v2 row must bind brief v2: %+v", rowV2)
	}
	if rowV2.ReuseKey == rowV1.ReuseKey {
		t.Fatal("v2 needs its own reuse key under the new brief")
	}
	// The nil-resolver limitation, pinned: the v2 row cannot chain to v1.
	if rowV2.SupersedesID != "" {
		t.Fatalf("with a nil resolver the v2 row must stay unlinked, got %q", rowV2.SupersedesID)
	}
	// v1 stays immutable.
	againV1 := t26DynamicAsm(t, h.db, asmV1.ID)
	if againV1.AnswersJSON != rowV1.AnswersJSON || againV1.SupersedesID != "" {
		t.Fatalf("v1 must stay immutable: %+v", againV1)
	}
	// One capture carries the assessments across brief versions.
	links := t26AssessLinks(t, h.db, capCrisp)
	byAsm := map[string]int{}
	for _, l := range links {
		byAsm[l.AssessmentID]++
	}
	if byAsm[asmV1.ID] == 0 || byAsm[asmV2.ID] == 0 {
		t.Fatalf("one capture with two assessments required: %+v", links)
	}

	// The v1 assessment judged brief v1: citing it under run2 fails
	// stale; the v2 assessment saves.
	co := h.save(t, run2, "t26-b01-co", researchcontract.SaveItem{
		Op:               researchcontract.SaveCreateCompany,
		Fields:           map[string]string{"name": "Crisp"},
		EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(body, capCrisp)},
		IdentityDecision: t26NewDecision(),
	})
	coID := co.Saved[0].RecordID
	stale := h.save(t, run2, "t26-b01-stale", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Developer",
			"sourceUrl": crispURL, "originalText": body},
		EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(body, capCrisp)},
		AssessmentIDs:    []string{asmV1.ID},
		IdentityDecision: t26NewDecision(),
	})
	if stale.Outcome != researchcontract.OutcomeStale {
		t.Fatalf("v1 assessment under brief v2 must fail stale: %+v", stale)
	}
	fresh := h.save(t, run2, "t26-b01-fresh", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Developer",
			"sourceUrl": crispURL, "originalText": body},
		EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(body, capCrisp)},
		AssessmentIDs:    []string{asmV2.ID},
		IdentityDecision: t26NewDecision(),
	})
	if fresh.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("v2 assessment under brief v2 must save: %+v", fresh)
	}
}

// ---------------------------------------------------------------------------
// Independent useful records in one batch: two companies plus two
// opportunities commit atomically with distinct identities, each with its
// own evidence; a bad batch leaves nothing behind.
// ---------------------------------------------------------------------------

func TestT26_BatchIndependentRecords(t *testing.T) {
	h := newT26Harness(t)
	run := h.commission(t, "t26-batch-run", "T26 batch independence.")
	alphaURL := "https://example-careers-acme.test/jobs/ACME-BE-9"
	betaURL := "https://example-careers-brouwerij.test/vacatures/VBB-14"

	_, capAlpha := h.fetch(t, run, "t26-batch-fetch-alpha", h.board.url("/batch/alpha"))
	_, capBeta := h.fetch(t, run, "t26-batch-fetch-beta", h.board.url("/batch/beta"))
	bodyAlpha, bodyBeta := h.captureBytes(t, capAlpha), h.captureBytes(t, capBeta)

	// The alpha record carries a suitability assessment; beta stays a
	// factual unassessed find. Both are independently useful.
	h.provider.setVerdict("q-batch-alpha", "suitable")
	asmAlpha := h.assess(t, run, "t26-batch-assess", "role_fit",
		[]researchcontract.AssessQuestion{
			{ID: "q-batch-alpha", Text: "Is the Acme role suitable on stack?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "suitable", Label: "Suitable: Go, Kubernetes",
						EvidenceRefs: []researchcontract.EvidenceRef{t26Ref(t, bodyAlpha, capAlpha, "Go, Kubernetes")}},
					{ID: "unsuitable", Label: "Unsuitable"},
				}},
		},
		[]researchcontract.EvidenceRef{t26FullRef(bodyAlpha, capAlpha)})

	batch := h.save(t, run, "t26-batch-save",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Acme Robotics"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyAlpha, capAlpha, "Acme Robotics")},
			IdentityDecision: t26NewDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": alphaURL, "originalText": bodyAlpha,
				"locationText": "Eindhoven", "vacancyComplete": "true",
				"requisitionId": "ACME-BE-9", "reqIssuer": "acme"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyAlpha, capAlpha, "Go, Kubernetes")},
			AssessmentIDs:    []string{asmAlpha.ID},
			IdentityDecision: t26NewDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "De Voorbeeld Brouwerij"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyBeta, capBeta, "De Voorbeeld Brouwerij")},
			IdentityDecision: t26NewDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:2", "title": "Backend Engineer",
				"sourceUrl": betaURL, "originalText": bodyBeta,
				"locationText": "Rotterdam", "vacancyComplete": "true",
				"requisitionId": "VBB-14", "reqIssuer": "brouwerij"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, bodyBeta, capBeta, "PHP, Laravel")},
			IdentityDecision: t26NewDecision()},
	)
	if batch.Outcome != researchcontract.OutcomeOK || len(batch.Saved) != 4 {
		t.Fatalf("batch save: %+v", batch)
	}
	oppAlpha, oppBeta := batch.Saved[1].RecordID, batch.Saved[3].RecordID
	if oppAlpha == oppBeta {
		t.Fatalf("batch records need distinct identities: %+v", batch.Saved)
	}
	if _, _, _, stageAlpha, _ := t26OppRow(t, h.db, oppAlpha); stageAlpha != store.DiscoveredStage {
		t.Fatalf("assessed record must be discoverable, stage=%q", stageAlpha)
	}
	if _, _, _, stageBeta, _ := t26OppRow(t, h.db, oppBeta); stageBeta != store.UnassessedStage {
		t.Fatalf("unassessed record must stay unassessed, stage=%q", stageBeta)
	}
	if sightings := t26Sightings(t, h.db, oppAlpha); len(sightings) != 1 {
		t.Fatalf("alpha sighting: %+v", sightings)
	}
	if sightings := t26Sightings(t, h.db, oppBeta); len(sightings) != 1 {
		t.Fatalf("beta sighting: %+v", sightings)
	}

	// A bad batch rolls back entirely: no partial company survives.
	rollback := h.save(t, run, "t26-batch-rollback",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Rollback Probe"},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(bodyAlpha, capAlpha)},
			IdentityDecision: t26NewDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "",
				"originalText": bodyAlpha},
			EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(bodyAlpha, capAlpha)},
			IdentityDecision: t26NewDecision()},
	)
	if rollback.Outcome == researchcontract.OutcomeOK {
		t.Fatalf("bad batch must not commit: %+v", rollback)
	}
	if n := t26Count(t, h.db, `SELECT count(*) FROM companies WHERE name='Rollback Probe'`); n != 0 {
		t.Fatalf("failed batch left a partial company: %d", n)
	}
}

// ---------------------------------------------------------------------------
// Source-backed suitability: A01 (ambiguous/conflicting-free judgments
// with units verbatim), M01 (missing facts stay unknown, unassessed save),
// M02 (snippet is discovery-only; complete claims fail capture_incomplete).
// ---------------------------------------------------------------------------

func TestT26_SourceBackedSuitability(t *testing.T) {
	t.Run("A01", func(t *testing.T) {
		h := newT26Harness(t)
		run := h.commission(t, "t26-a01-run", "T26 A01 suitability.")
		crispURL := "https://example.test/a01/crisp"

		_, capCrisp := h.fetch(t, run, "t26-a01-fetch", h.board.url("/a01/crisp"))
		body := h.captureBytes(t, capCrisp)
		frontRef := t26Ref(t, body, capCrisp, "Code across our stack - frontend and backend.")
		hoursRef := t26Ref(t, body, capCrisp, "36-40 hours per week")
		payRef := t26Ref(t, body, capCrisp, "A competitive salary and equity/stock.")

		// Reviewed outcomes (T04), returned deterministically by the
		// fixture through the REAL handler binding.
		h.provider.setVerdict("q-backend-scope", "ambiguous")
		h.provider.setVerdict("q-frontend", "present")
		h.provider.setVerdict("q-pay", "unknown")
		h.provider.setVerdict("q-hours32", "not_satisfied")
		got := h.assess(t, run, "t26-a01-assess", "role_fit",
			[]researchcontract.AssessQuestion{
				{ID: "q-backend-scope", Text: "Is the primary assigned work backend/platform?", AbstainAllowed: true,
					Alternatives: []researchcontract.AssessAlternative{
						{ID: "present", Label: "Present: primary backend assignment",
							EvidenceRefs: []researchcontract.EvidenceRef{t26Ref(t, body, capCrisp, "implement new app features")}},
						{ID: "ambiguous", Label: "Ambiguous: mixed frontend/backend signals",
							EvidenceRefs: []researchcontract.EvidenceRef{frontRef}},
					}},
				{ID: "q-frontend", Text: "Are frontend implementation duties assigned?", AbstainAllowed: true,
					Alternatives: []researchcontract.AssessAlternative{
						{ID: "present", Label: "Present: explicit frontend code across the stack",
							EvidenceRefs: []researchcontract.EvidenceRef{frontRef}},
						{ID: "absent", Label: "Absent: no frontend implementation duties"},
					}},
				{ID: "q-pay", Text: "Is numeric pay stated?", AbstainAllowed: true,
					Alternatives: []researchcontract.AssessAlternative{
						{ID: "stated", Label: "Stated: numeric amount with currency and period"},
						{ID: "unknown", Label: "Unknown: competitive salary names no amount",
							EvidenceRefs: []researchcontract.EvidenceRef{payRef}},
					}},
				{ID: "q-hours32", Text: "Does a 32 h/week preference fit?", AbstainAllowed: true,
					Alternatives: []researchcontract.AssessAlternative{
						{ID: "satisfied", Label: "Satisfied"},
						{ID: "not_satisfied", Label: "Not satisfied: requires 36-40 hours per week, outside the 32 h preference",
							EvidenceRefs: []researchcontract.EvidenceRef{hoursRef}},
					}},
			},
			[]researchcontract.EvidenceRef{t26FullRef(body, capCrisp)})
		byQ := map[string]researchcontract.AssessAnswer{}
		for _, a := range got.Results {
			byQ[a.QuestionID] = a
		}
		if byQ["q-backend-scope"].AnswerID != "ambiguous" || byQ["q-backend-scope"].Abstained {
			t.Fatalf("ambiguous scope must survive, not forced: %+v", byQ["q-backend-scope"])
		}
		if len(byQ["q-backend-scope"].SourceBindings) != 1 ||
			byQ["q-backend-scope"].SourceBindings[0] != frontRef {
			t.Fatalf("answer must bind exactly the chosen refs: %+v", byQ["q-backend-scope"])
		}
		if byQ["q-frontend"].AnswerID != "present" || byQ["q-pay"].AnswerID != "unknown" ||
			byQ["q-hours32"].AnswerID != "not_satisfied" {
			t.Fatalf("reviewed outcomes not preserved: %+v", got.Results)
		}
		if byQ["q-pay"].Uncertainty != "confidence 0.9000" {
			t.Fatalf("uncertainty must render losslessly: %+v", byQ["q-pay"])
		}
		h.provider.mu.Lock()
		sent, ok := h.provider.lastReq.Questions["q-hours32"].(jev.ChoiceQuestion)
		h.provider.mu.Unlock()
		if !ok || !strings.Contains(sent.Criteria["not_satisfied"], "36-40 hours per week") {
			t.Fatalf("units/context must reach the judge verbatim")
		}
		row := t26DynamicAsm(t, h.db, got.ID)
		if row.Status != "succeeded" || row.ReuseKey != got.ReuseKey || len(got.ReuseKey) != 64 {
			t.Fatalf("binding row must persist with the reuse key: %+v", row)
		}
		if !strings.Contains(row.EvidenceRefsJSON, capCrisp) {
			t.Fatalf("assessment must bind exact evidence: %s", row.EvidenceRefsJSON)
		}

		// Suitability-backed save: the assessed record is discoverable.
		saved := h.save(t, run, "t26-a01-save",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "Crisp"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(body, capCrisp)},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Developer",
					"sourceUrl": crispURL, "originalText": body,
					"locationText": "Amsterdam", "vacancyComplete": "true"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(body, capCrisp)},
				AssessmentIDs:    []string{got.ID},
				IdentityDecision: t26NewDecision()},
		)
		if saved.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("suitability-backed save: %+v", saved)
		}
		if _, _, _, stage, _ := t26OppRow(t, h.db, saved.Saved[1].RecordID); stage != store.DiscoveredStage {
			t.Fatalf("assessed record must be discoverable, stage=%q", stage)
		}
	})

	t.Run("M01", func(t *testing.T) {
		h := newT26Harness(t)
		run := h.commission(t, "t26-m01-run", "T26 M01 missing facts.")
		pageURL := "https://jobs.ashbyhq.com/myTomorrows/863ede5e-b578-431e-b036-a5ba69e501c5"

		_, capM := h.fetch(t, run, "t26-m01-fetch", h.board.url("/m01/myt"))
		body := h.captureBytes(t, capM)

		out := h.save(t, run, "t26-m01-first",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "myTomorrows"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, body, capM, "myTomorrows")},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
					"sourceUrl": pageURL, "originalText": body,
					"locationText": "Amsterdam", "vacancyComplete": "true",
					"boardProvider": "ashby", "board": "myTomorrows",
					"boardRecordId": "863ede5e-b578-431e-b036-a5ba69e501c5"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26Link(t, body, capM, "developing and maintaining the platform infrastructure")},
				IdentityDecision: t26NewDecision()},
		)
		if out.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("factual unassessed save: %+v", out)
		}
		oppID := out.Saved[1].RecordID
		if _, _, _, stage, _ := t26OppRow(t, h.db, oppID); stage != store.UnassessedStage {
			t.Fatalf("missing-facts role must be labeled unassessed, stage=%q", stage)
		}
		// Unknown pay persists as an explicit unknown row — never an
		// invented figure.
		if n := t26Count(t, h.db, `SELECT count(*) FROM compensation WHERE opportunity_id=? AND
  (currency != 'unknown' OR min_amount_cents IS NOT NULL OR max_amount_cents IS NOT NULL)`, oppID); n != 0 {
			t.Fatalf("unknown pay must persist no invented amounts, rows=%d", n)
		}
		if n := t26Count(t, h.db, `SELECT count(*) FROM jev_assessments_dynamic`); n != 0 {
			t.Fatalf("no assessment may qualify silence: %d rows", n)
		}
	})

	t.Run("M02", func(t *testing.T) {
		h := newT26Harness(t)
		run := h.commission(t, "t26-m02-run", "T26 M02 snippet.")
		snippet := t26Snapshot(t)["syn/snippet-go-ams"]

		// A search-result snippet observed through the REAL memory path
		// (claim + observe): search provenance defaults to snippet rows.
		desc := researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationSearch, Backend: "fixture-search",
			URLOrQuery: "Senior Go Engineer Amsterdam",
		}
		claim, err := h.stack.Memory.Claim(h.ctx, run.id, "t26-m02-owner", run.gen, desc, "t26-m02-claim")
		if err != nil {
			t.Fatal(err)
		}
		if claim.Outcome != researchcontract.OutcomeOK || claim.Lease == nil {
			t.Fatalf("snippet claim: %+v", claim)
		}
		now := time.Now()
		obs, err := h.stack.Memory.Observe(h.ctx, researchmemory.ObserveInput{
			LeaseID: claim.Lease.LeaseID, Owner: "t26-m02-owner", Generation: run.gen,
			RoundID: run.id, ActualURLOrQuery: desc.URLOrQuery,
			StartedAt: now, FinishedAt: now,
			Outcome:    store.ObservationSuccess,
			Provenance: researchcontract.ProvenanceSearchResult,
			Receipt: researchcontract.ExecutionReceipt{ID: "t26-m02-receipt-1", Status: researchcontract.ReceiptOK,
				Executor: researchcontract.ExecutorIdentity{Backend: "fixture-search", Version: "t26"}},
			Capture: &researchmemory.CaptureInput{Bytes: []byte(snippet),
				MediaType: "text/plain", Completeness: store.CaptureComplete},
		})
		if err != nil {
			t.Fatal(err)
		}
		// The observation carries the retrieval row id; citations pin
		// the canonical content sha over the snippet bytes.
		sum := sha256.Sum256([]byte(snippet))
		capSnippet := hex.EncodeToString(sum[:])
		var rowContent string
		if err := h.db.Read(h.ctx, func(r store.Reader) error {
			return r.QueryRowContext(h.ctx,
				`SELECT content_sha256 FROM source_captures WHERE id=?`, obs.CaptureID,
			).Scan(&rowContent)
		}); err != nil {
			t.Fatal(err)
		}
		if rowContent != capSnippet {
			t.Fatalf("snippet row must hold the snippet bytes: %+v", obs)
		}

		blocked := h.save(t, run, "t26-m02-blocked",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "ExampleCorp"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(snippet, capSnippet)},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Senior Go Engineer",
					"originalText": snippet,
					"locationText": "Amsterdam", "vacancyComplete": "true"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(snippet, capSnippet)},
				IdentityDecision: t26NewDecision()},
		)
		if blocked.Outcome != researchcontract.OutcomeCaptureIncomplete {
			t.Fatalf("snippet complete-vacancy claim must fail capture_incomplete: %+v", blocked)
		}
		if n := t26Count(t, h.db, `SELECT count(*) FROM opportunities`); n != 0 {
			t.Fatalf("blocked batch must leave no records, got %d", n)
		}

		partial := h.save(t, run, "t26-m02-partial",
			researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "ExampleCorp"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(snippet, capSnippet)},
				IdentityDecision: t26NewDecision()},
			researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Senior Go Engineer (snippet)",
					"originalText": snippet, "locationText": "Amsterdam"},
				EvidenceLinks:    []researchcontract.EvidenceLink{t26FullLink(snippet, capSnippet)},
				IdentityDecision: t26NewDecision()},
		)
		if partial.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("snippet partial find must save without the complete claim: %+v", partial)
		}
		if _, _, _, stage, _ := t26OppRow(t, h.db, partial.Saved[1].RecordID); stage != store.UnassessedStage {
			t.Fatalf("snippet find must stay unassessed, stage=%q", stage)
		}
		if n := t26Count(t, h.db, `SELECT count(*) FROM jev_assessments_dynamic`); n != 0 {
			t.Fatalf("no qualified opportunity from a snippet alone: %d assessments", n)
		}
	})
}

// ---------------------------------------------------------------------------
// Exact repeat reuse (Q02 at the dispatch level) + the justified-refresh
// memory mechanism. T04 Q02 expected: the repeat REUSES the prior result;
// no second dispatch/charge; original capture reference carried.
// The refresh probe shows a justified refresh re-opens a fresh request
// with its reason recorded and history linked (lane C mechanics, used by
// legitimate-refresh flows like R01).
// ---------------------------------------------------------------------------

func TestT26_ExactRepeatReuseAndRefresh(t *testing.T) {
	h := newT26Harness(t)
	run := h.commission(t, "t26-q02-run", "T26 Q02 reuse.")
	url := h.board.url("/q02/item")

	obs1, cap1 := h.fetch(t, run, "t26-q02-first", url)
	again, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: run.id, Generation: run.gen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: url,
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "t26-q02-repeat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("exact repeat must reuse: %+v", again)
	}
	// The original observation id and the canonical content id travel
	// with the reuse (the dispatch-level CaptureID is the retrieval
	// handle; citations pin the receipt's canonical id per T23).
	if again.ObservationID != obs1 || again.Receipt.CaptureID != cap1 {
		t.Fatalf("reuse must carry the original references: %+v", again)
	}

	// Justified refresh re-opens the fresh request with history linked.
	desc := researchcontract.RequestDescriptor{
		Operation: researchcontract.OperationFetch, Backend: "generic-http",
		Method: "GET", URLOrQuery: url,
	}
	refreshed, err := h.stack.Memory.Refresh(h.ctx, run.id, "t26-refresh-owner", run.gen, desc, store.RefreshReasonStale)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Outcome != researchcontract.OutcomeOK || refreshed.Lease == nil {
		t.Fatalf("justified refresh must grant a lease: %+v", refreshed)
	}
	if refreshed.RefreshFrom != obs1 {
		t.Fatalf("refresh must link its prior observation: %+v", refreshed)
	}
	events, _, err := h.stack.Journal.List(h.ctx, run.id, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == researchcontract.EventRefresh && strings.Contains(string(e.Payload), store.RefreshReasonStale) {
			found = true
		}
	}
	if !found {
		t.Fatalf("journal must record the refresh reason")
	}
	if _, err := h.stack.Memory.Refresh(h.ctx, run.id, "t26-refresh-owner", run.gen, desc, "curiosity"); err == nil {
		t.Fatal("unjustified refresh succeeded")
	} else {
		var contractErr *researchcontract.Error
		if !errors.As(err, &contractErr) || contractErr.Code != researchcontract.OutcomeInvalid {
			t.Fatalf("unjustified refresh must fail invalid, got %v", err)
		}
	}
}
