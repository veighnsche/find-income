// T21 lane D focused judgment/identity/save evaluation (lane D, T21).
//
// Drives the FROZEN T04 acceptance corpus (implementation-notes/
// autonomous-recruitment/T04-corpus/corpus.json, frozen 2026-09-24;
// human-readable expected-distinctions.md) end-to-end through the D stack:
// identity.Matcher with scripted Assessor doubles, jevassess.Handler with a
// fixture provider, identity.StoreSink, and recordsave.Handler over a real
// SQLite store on t.TempDir. No live Jev spend, no browser launches.
//
// Every verdict below is a DETERMINISTIC fixture outcome: doubles return the
// corpus-scripted verdicts so the test pins the binding machinery (capture+
// span citations, identity rechecks, brief binding, key convergence). These
// tests prove the machinery preserves distinctions; they are not provider
// evidence and make no calibrated-probability claim. Live-Jev behavior is
// T26/T30 scope. Cases owned by other lanes (U01-U03 execution, F01/F02 and
// Q02 dispatch mechanics) are marked N/A with the D-adjacent aspect covered
// instead; see implementation-notes/autonomous-recruitment/T21-eval/note.md.
package recordsave

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/identity"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ---------------------------------------------------------------------------
// Corpus snapshot + minimal real-anchored bodies.
// ---------------------------------------------------------------------------

// loadCorpusBodies reads the verbatim read-only T04 synthetic snapshot kept
// in the identity package testdata (copied 2026-09-24; never edited).
func loadCorpusBodies(t *testing.T) map[string]string {
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

// Minimal test-local bodies for real-anchored items (C01/D01/A01/M01/B01).
// Doubles decide verdicts, so live source text adds nothing and stays out of
// the repo (same rule as the identity matcher tests); real-byte hashes are
// verified by shell in the T21 evidence note instead. Each body keeps the
// DISTINGUISHING substance the corpus expects (board ids, seniority scope,
// frontend span, hours span, pay silence).
const (
	evalC01APIBody  = `Ashby API record 863ede5e-b578-431e-b036-a5ba69e501c5: myTomorrows Backend Engineer. developing and maintaining the platform infrastructure. Amsterdam.`
	evalC01PageBody = `myTomorrows Backend Engineer posting page. developing and maintaining the platform infrastructure. Amsterdam. Apply via jobs.ashbyhq.com/myTomorrows/863ede5e-b578-431e-b036-a5ba69e501c5.`

	evalMyTBackendBody = `myTomorrows Backend Engineer posting (evaluation fixture). As a Backend Engineer, you will be responsible for developing and maintaining the platform infrastructure. You will work closely with our Frontend Engineers, DevOps Engineers, and other stakeholders. Our office is in Amsterdam. Board record 863ede5e-b578-431e-b036-a5ba69e501c5.`
	evalMyTSeniorBody  = `myTomorrows Senior Backend Engineer posting (evaluation fixture). Lead the data platform team building large-scale ingestion services. Requires 6+ years backend experience and prior tech-lead scope. Our office is in Amsterdam. Board record 52f27ead-683f-4047-9185-f14c75324893.`

	evalCrispBody = `Crisp developer posting (evaluation fixture, minimal substance of the real capture). You will: Work with product owners to implement new app features. Code across our stack - frontend and backend. Ship often and take responsibility. A minimum of B2 level Dutch and a full-time availability of 36-40 hours per week are required. What we offer: A competitive salary and equity/stock. Our process starts with a technical assignment.`

	evalVacBody  = `Source A, vacancy: You own backend APIs. This position has no frontend implementation duties.`
	evalClarBody = `Source B, employer clarification about this same role, same date: The engineer must also maintain our React UI one day each week.`
)

// ---------------------------------------------------------------------------
// Doubles: scripted Matcher assessor + fixture Jev provider.
// ---------------------------------------------------------------------------

// evalMatchAssessor answers scripted identity verdicts per comparison
// question id (default abstain). Unknown captures and out-of-range spans
// fail loudly, like the real handler's binding checks.
type evalMatchAssessor struct {
	bodies   map[string]string
	verdicts map[string]string // questionID -> same|distinct|abstain
	calls    int
	last     researchcontract.AssessInput
}

func (f *evalMatchAssessor) Assess(_ context.Context, in researchcontract.AssessInput) (researchcontract.Assessment, error) {
	f.calls++
	f.last = in
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
		switch f.verdicts[q.ID] {
		case identity.AnswerSame:
			results = append(results, researchcontract.AssessAnswer{QuestionID: q.ID, AnswerID: identity.AnswerSame})
		case identity.AnswerDistinct:
			results = append(results, researchcontract.AssessAnswer{QuestionID: q.ID, AnswerID: identity.AnswerDistinct})
		default:
			results = append(results, researchcontract.AssessAnswer{QuestionID: q.ID, Abstained: true})
		}
	}
	return researchcontract.Assessment{ID: "asm_eval", Results: results, ReuseKey: "eval"}, nil
}

// evalJevCap is one fixture capture for the Jev pipeline doubles.
type evalJevCap struct {
	body     string
	complete bool
}

// evalJevCaptures serves fixture bytes with the real handler's binding
// semantics (descriptor id must equal the cited id; Complete gates use).
type evalJevCaptures struct {
	items map[string]evalJevCap
	opens []string
}

func (f *evalJevCaptures) ResolveReceipt(context.Context, string) (researchcontract.ExecutionReceipt, error) {
	return researchcontract.ExecutionReceipt{}, researchcontract.NewError(researchcontract.OutcomeNotFound, "", "unused")
}

func (f *evalJevCaptures) OpenCapture(_ context.Context, id string) (researchcontract.Capture, io.ReadCloser, error) {
	f.opens = append(f.opens, id)
	item, ok := f.items[id]
	if !ok {
		return researchcontract.Capture{}, nil, researchcontract.NewError(researchcontract.OutcomeNotFound,
			"", "no such capture")
	}
	return researchcontract.Capture{ID: id, Complete: item.complete, Bytes: int64(len(item.body))},
		io.NopCloser(strings.NewReader(item.body)), nil
}

// evalProvider is the fixture Jev provider: scripted result or error, exact
// exchange bytes retained. No network, no spend.
type evalProvider struct {
	model    string
	calls    int
	last     jev.Request
	encoded  []byte
	result   jev.Result
	exchange jev.CapturedExchange
	err      error
}

func (f *evalProvider) RequestedModel() string { return f.model }

func (f *evalProvider) EncodedRequest(jev.Request) ([]byte, error) {
	return append([]byte(nil), f.encoded...), nil
}

func (f *evalProvider) EvaluateOnceCaptured(_ context.Context, r jev.Request) (jev.Result, jev.CapturedExchange, error) {
	f.calls++
	f.last = r
	return f.result, f.exchange, f.err
}

type evalExchanges struct {
	begins       []store.JevAttemptStart
	finishes     []store.JevAttemptFinish
	reclassified []string
	actor        store.Actor
}

// MarkRoundDispatched, Round and FinishRoundAttempt implement the
// handler's Dispatcher/Finisher hooks (T23 jevfix/jevfinish): the exchange
// log double also settles the reserved round attempt, mirroring the
// jevassess package's own fakeExchanges.
func (f *evalExchanges) MarkRoundDispatched(_ context.Context, runID, attemptID string) (store.RoundAttempt, error) {
	return store.RoundAttempt{ID: attemptID, RoundID: runID, State: store.AttemptDispatched}, nil
}

func (f *evalExchanges) Round(_ context.Context, runID string) (store.Round, error) {
	return store.Round{ID: runID, Actor: f.actor}, nil
}

func (f *evalExchanges) FinishRoundAttempt(_ context.Context, _ store.Actor, runID, attemptID string, success bool, _ json.RawMessage, _ string) (store.RoundAttempt, error) {
	state := store.AttemptFailed
	if success {
		state = store.AttemptSucceeded
	}
	return store.RoundAttempt{ID: attemptID, RoundID: runID, State: state}, nil
}

func (f *evalExchanges) BeginJevAttempt(_ context.Context, in store.JevAttemptStart) (store.JevAttempt, error) {
	f.begins = append(f.begins, in)
	return store.JevAttempt{ID: "jev-eval", Purpose: in.Purpose, Status: "dispatched"}, nil
}

func (f *evalExchanges) FinishJevAttempt(_ context.Context, in store.JevAttemptFinish) (store.JevAttempt, error) {
	f.finishes = append(f.finishes, in)
	return store.JevAttempt{ID: in.ID, Status: in.Status}, nil
}

func (f *evalExchanges) ReclassifyJevAttempt(_ context.Context, id, status, _ string) error {
	f.reclassified = append(f.reclassified, id+":"+status)
	return nil
}

type evalSink struct {
	records []jevassess.DynamicAssessmentRecord
}

func (f *evalSink) SaveDynamicAssessment(_ context.Context, r jevassess.DynamicAssessmentRecord) error {
	f.records = append(f.records, r)
	return nil
}

func evalChoiceResult(answers map[string]string, confidence float64) jev.Result {
	out := jev.Result{RequestedModel: "jev-1.13.0", ReturnedModel: "jev-1.13.0",
		Answers: map[string]jev.Answer{}, RawResponse: json.RawMessage(`{"model":"jev-1.13.0"}`)}
	for id, choice := range answers {
		out.Answers[id] = jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{
			Choice: choice, Probabilities: map[string]float64{choice: 1}, Confidence: confidence}}
	}
	return out
}

func evalAbstainResult(ids []string, confidence float64) jev.Result {
	answers := map[string]string{}
	for _, id := range ids {
		answers[id] = jevassess.AbstainID
	}
	return evalChoiceResult(answers, confidence)
}

// evalAuthAuthority is a researchcontract.Authority double that records
// reservations so the eval can assert one-reservation-per-attempt.
type evalAuthAuthority struct {
	checkErr error
	reserves []string
}

func (f *evalAuthAuthority) Check(context.Context, researchcontract.CheckInput) error {
	return f.checkErr
}

func (f *evalAuthAuthority) Reserve(_ context.Context, _ string, _ string, key string, _ string) (researchcontract.Reservation, error) {
	f.reserves = append(f.reserves, key)
	return researchcontract.Reservation{ID: "res-" + key, AttemptID: "round-attempt-" + key}, nil
}

func (f *evalAuthAuthority) Release(context.Context, string) error { return nil }

func (f *evalAuthAuthority) Usage(context.Context, string) (researchcontract.UsageLedger, error) {
	return researchcontract.UsageLedger{}, nil
}

func (f *evalAuthAuthority) ReconciliationFor(context.Context, string) (researchcontract.Reconciliation, error) {
	return researchcontract.Reconciliation{}, nil
}

// evalAssessHandler wires a real jevassess.Handler over fixture doubles.
func evalAssessHandler(caps map[string]evalJevCap) (*jevassess.Handler, *evalAuthAuthority, *evalProvider, *evalExchanges, *evalSink) {
	auth := &evalAuthAuthority{}
	provider := &evalProvider{model: "jev-1.13.0", encoded: []byte(`{"model":"jev-1.13.0"}`),
		exchange: jev.CapturedExchange{RequestBytes: []byte(`{"model":"jev-1.13.0"}`),
			ResponseBytes: []byte(`{"model":"jev-1.13.0"}`), HTTPStatus: 200, ReturnedModel: "jev-1.13.0"}}
	exchanges, sink := &evalExchanges{actor: store.Actor{Kind: "administrator", ID: "owner"}}, &evalSink{}
	h := &jevassess.Handler{Authority: auth, Captures: &evalJevCaptures{items: caps},
		Briefs:   &fakeBriefs{profile: 1, rubric: "rubric-v1"},
		Provider: provider, Exchanges: exchanges, Sink: sink}
	return h, auth, provider, exchanges, sink
}

func evalContractCode(t *testing.T, err error) researchcontract.Outcome {
	t.Helper()
	var contractErr *researchcontract.Error
	if !errors.As(err, &contractErr) {
		t.Fatalf("expected typed contract error, got %T %v", err, err)
	}
	return contractErr.Code
}

// ---------------------------------------------------------------------------
// Eval environment: real store + commissioned round + saver + span helpers.
// ---------------------------------------------------------------------------

type evalEnv struct {
	store  *store.Store
	round  store.Round
	caps   *fakeCaptures
	briefs *fakeBriefs
	saver  *Handler
	bodies map[string]string
}

func newEvalEnv(t *testing.T) *evalEnv {
	t.Helper()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{}}
	briefs := &fakeBriefs{profile: 1, rubric: "rubric-v1"}
	return &evalEnv{store: s, round: round, caps: caps, briefs: briefs,
		saver:  saveHandler(s, &fakeAuthority{}, saveAgent, caps, briefs),
		bodies: map[string]string{}}
}

// seedCapture registers one immutable capture in both the memory reader and
// the store row, so handler verification and in-transaction rebinding agree.
func (e *evalEnv) seedCapture(t *testing.T, id, url, body string, mutate func(*fakeCaptureBody)) {
	t.Helper()
	fb := fakeCaptureBody{body: body, originalURL: url, finalURL: url}
	if mutate != nil {
		mutate(&fb)
	}
	e.caps.bodies[id] = fb
	e.bodies[id] = body
	seedCaptureRow(t, e.store, id, e.caps.bodies[id])
}

func evalSpan(t *testing.T, body, substr string) (int64, int64) {
	t.Helper()
	i := strings.Index(body, substr)
	if i < 0 {
		t.Fatalf("span %q not found in capture body", substr)
	}
	return int64(i), int64(i + len(substr))
}

// link builds an evidence link over the exact span of substr in the capture.
func (e *evalEnv) link(t *testing.T, capID, substr string) researchcontract.EvidenceLink {
	t.Helper()
	body := e.bodies[capID]
	start, end := evalSpan(t, body, substr)
	return researchcontract.EvidenceLink{CaptureID: capID, SpanStart: start, SpanEnd: end,
		ExcerptSHA256: excerptSHA(body, int(start), int(end))}
}

func (e *evalEnv) fullLink(capID string) researchcontract.EvidenceLink {
	return fullLink(capID, e.bodies[capID])
}

// ref builds a cited evidence ref over the exact span of substr.
func (e *evalEnv) ref(t *testing.T, capID, substr string) researchcontract.EvidenceRef {
	t.Helper()
	body := e.bodies[capID]
	start, end := evalSpan(t, body, substr)
	return researchcontract.EvidenceRef{CaptureID: capID, SpanStart: start, SpanEnd: end}
}

func (e *evalEnv) fullRef(capID string) researchcontract.EvidenceRef {
	return researchcontract.EvidenceRef{CaptureID: capID, SpanStart: 0, SpanEnd: int64(len(e.bodies[capID]))}
}

// matchOne runs the real identity matcher with scripted verdicts.
func (e *evalEnv) matchOne(t *testing.T, verdicts map[string]string, attrs researchcontract.MatchAttributes, refs ...researchcontract.EvidenceRef) (researchcontract.MatchOutput, *evalMatchAssessor) {
	t.Helper()
	assessor := &evalMatchAssessor{bodies: e.bodies, verdicts: verdicts}
	mh := &identity.Handler{Authority: &fakeAuthority{}, Store: e.store, Briefs: e.briefs, Assessor: assessor}
	out, err := mh.Match(context.Background(), researchcontract.MatchInput{
		Attributes: attrs, EvidenceRefs: refs, RunID: e.round.ID, Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, assessor
}

func (e *evalEnv) save(t *testing.T, key string, items ...researchcontract.SaveItem) researchcontract.SaveOutput {
	t.Helper()
	out, err := e.saver.Save(context.Background(), researchcontract.SaveBatch{
		Items: items, IdempotencyKey: key, RunID: e.round.ID, Generation: 1,
	})
	if err != nil {
		t.Fatalf("save %s: %v", key, err)
	}
	return out
}

func evalExactIDs(out researchcontract.MatchOutput) map[string]string {
	got := map[string]string{}
	for _, m := range out.ExactMatches {
		got[m.RecordID] = m.Kind
	}
	return got
}

func evalOppRow(t *testing.T, s *store.Store, id string) (title, location, text, stage string, revision int64) {
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

func evalSnapshots(t *testing.T, s *store.Store, oppID string) []string {
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

func evalDecisionRefs(t *testing.T, s *store.Store, oppID string) (decision, refs string) {
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

func evalSightings(t *testing.T, s *store.Store, oppID string) []store.RecordSighting {
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

func evalKeys(t *testing.T, s *store.Store, oppID string) []store.EntityIdentityKey {
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

// ---------------------------------------------------------------------------
// Cross-posts: C01 (two retrieval paths, one vacancy).
// DISTINCTION: ONE vacancy identity, TWO sightings, same record ID on reuse.
// ---------------------------------------------------------------------------

func TestCorpusEval_C01_CrossPostSightingMerge(t *testing.T) {
	e := newEvalEnv(t)
	apiURL := "https://api.ashbyhq.com/posting-api/job-board/myTomorrows?includeCompensation=true"
	pageURL := "https://jobs.ashbyhq.com/myTomorrows/863ede5e-b578-431e-b036-a5ba69e501c5"
	e.seedCapture(t, "eval/c01-api", apiURL, evalC01APIBody, nil)
	e.seedCapture(t, "eval/c01-page", pageURL, evalC01PageBody, nil)
	const sharedSpan = "developing and maintaining the platform infrastructure"

	first := e.save(t, "c01-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "myTomorrows"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "eval/c01-api", "myTomorrows")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": apiURL, "originalText": evalC01APIBody,
				"locationText": "Amsterdam", "vacancyComplete": "true",
				"boardProvider": "ashby", "board": "myTomorrows",
				"boardRecordId": "863ede5e-b578-431e-b036-a5ba69e501c5"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "eval/c01-api", sharedSpan)},
			IdentityDecision: newDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK || len(first.Saved) != 2 {
		t.Fatalf("first sighting save: %+v", first)
	}
	oppID, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	out, assessor := e.matchOne(t,
		map[string]string{
			"cmp-opportunity-" + oppID: identity.AnswerSame,
			"cmp-company-" + coID:      identity.AnswerSame,
		},
		researchcontract.MatchAttributes{Employer: "myTomorrows", Title: "Backend Engineer",
			URL: pageURL, Location: "Amsterdam"},
		e.ref(t, "eval/c01-page", sharedSpan))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("match outcome: %+v", out)
	}
	if exact := evalExactIDs(out); exact[oppID] != "opportunity" {
		t.Fatalf("second retrieval path must merge: %+v", out.ExactMatches)
	}
	if assessor.calls != 1 {
		t.Fatalf("merge needs one semantic comparison, calls=%d", assessor.calls)
	}

	reuse := e.save(t, "c01-reuse", researchcontract.SaveItem{
		Op: "update_opportunity", RecordID: oppID, ExpectedRevision: 1,
		Fields: map[string]string{"originalText": evalC01PageBody, "sourceUrl": pageURL,
			"boardProvider": "ashby", "board": "myTomorrows",
			"boardRecordId": "863ede5e-b578-431e-b036-a5ba69e501c5"},
		EvidenceLinks: []researchcontract.EvidenceLink{e.link(t, "eval/c01-page", sharedSpan)},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 1},
			{CandidateID: coID, Kind: "company", Revision: 1},
		}},
	})
	if reuse.Outcome != researchcontract.OutcomeOK || len(reuse.Saved) != 1 || reuse.Saved[0].RecordID != oppID {
		t.Fatalf("reuse must return the same record ID: %+v", reuse)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 1 {
		t.Fatalf("ONE vacancy identity required, got %d opportunities", n)
	}
	sightings := evalSightings(t, e.store, oppID)
	if len(sightings) != 2 {
		t.Fatalf("TWO sightings required, got %d", len(sightings))
	}
	seen := map[string]bool{}
	for _, s := range sightings {
		seen[s.CaptureID] = true
	}
	if !seen["eval/c01-api"] || !seen["eval/c01-page"] {
		t.Fatalf("each sighting binds its own capture: %+v", sightings)
	}
	decision, refs := evalDecisionRefs(t, e.store, oppID)
	if decision != "same" || !strings.Contains(refs, "eval/c01-page") {
		t.Fatalf("identity decision must cite the distinguishing evidence: %s %s", decision, refs)
	}
}

// ---------------------------------------------------------------------------
// Cross-posts: C02 (true cross-post, different title wording, one vacancy).
// DISTINCTION: title difference is a sighting variant, not a new role.
// ---------------------------------------------------------------------------

func TestCorpusEval_C02_CrossPostDifferentTitles(t *testing.T) {
	e := newEvalEnv(t)
	bodies := loadCorpusBodies(t)
	careersURL := "https://example-careers-northwind.test/jobs/NW-117"
	aggURL := "https://example-aggregator.test/listings/88412-northwind-backend"
	e.seedCapture(t, "syn/NW-117-careers", careersURL, bodies["syn/NW-117-careers"], nil)
	e.seedCapture(t, "syn/NW-117-aggregator", aggURL, bodies["syn/NW-117-aggregator"], nil)

	first := e.save(t, "c02-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Northwind Technologies"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/NW-117-careers", "Northwind Technologies")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": careersURL, "originalText": bodies["syn/NW-117-careers"],
				"locationText": "Amsterdam", "vacancyComplete": "true",
				"requisitionId": "NW-117", "reqIssuer": "northwind"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/NW-117-careers", "NW-117")},
			IdentityDecision: newDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first save: %+v", first)
	}
	oppID, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	out, _ := e.matchOne(t,
		map[string]string{
			"cmp-opportunity-" + oppID: identity.AnswerSame,
			"cmp-company-" + coID:      identity.AnswerSame,
		},
		researchcontract.MatchAttributes{Employer: "Northwind Technologies",
			Title: "Sr. Backend Engineer (Python)", URL: aggURL,
			RequisitionID: "NW-117", Location: "Amsterdam"},
		e.ref(t, "syn/NW-117-aggregator", "req NW-117"))
	if exact := evalExactIDs(out); exact[oppID] != "opportunity" {
		t.Fatalf("cross-post must merge despite title wording: %+v", out.ExactMatches)
	}

	reuse := e.save(t, "c02-reuse", researchcontract.SaveItem{
		Op: "update_opportunity", RecordID: oppID, ExpectedRevision: 1,
		Fields: map[string]string{"originalText": bodies["syn/NW-117-aggregator"],
			"sourceUrl": aggURL, "requisitionId": "NW-117", "reqIssuer": "northwind"},
		EvidenceLinks: []researchcontract.EvidenceLink{e.link(t, "syn/NW-117-aggregator", "Sr. Backend Engineer (Python)")},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 1},
			{CandidateID: coID, Kind: "company", Revision: 1},
		}},
	})
	if reuse.Outcome != researchcontract.OutcomeOK || reuse.Saved[0].RecordID != oppID {
		t.Fatalf("second sighting must reuse the record ID: %+v", reuse)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 1 {
		t.Fatalf("ONE vacancy identity required, got %d", n)
	}
	sightings := evalSightings(t, e.store, oppID)
	if len(sightings) != 2 || sightings[0].CaptureID == sightings[1].CaptureID {
		t.Fatalf("TWO sightings with distinct captures required: %+v", sightings)
	}
	snaps := evalSnapshots(t, e.store, oppID)
	if len(snaps) != 2 || !strings.Contains(snaps[1], "Sr. Backend Engineer (Python)") {
		t.Fatalf("title wording difference must survive as a sighting variant: %+v", snaps)
	}
	decision, refs := evalDecisionRefs(t, e.store, oppID)
	if decision != "same" || !strings.Contains(refs, "syn/NW-117-aggregator") {
		t.Fatalf("decision must cite distinguishing evidence: %s %s", decision, refs)
	}
}

// ---------------------------------------------------------------------------
// Distinct same-title: D01 (real hard negative, same employer).
// DISTINCTION: overlapping titles must NOT merge; distinct board record IDs,
// seniority scope and URLs distinguish; saving both yields two record IDs.
// ---------------------------------------------------------------------------

func TestCorpusEval_D01_DistinctSameTitleHardNegative(t *testing.T) {
	e := newEvalEnv(t)
	beURL := "https://jobs.ashbyhq.com/myTomorrows/863ede5e-b578-431e-b036-a5ba69e501c5"
	srURL := "https://jobs.ashbyhq.com/myTomorrows/52f27ead-683f-4047-9185-f14c75324893"
	e.seedCapture(t, "eval/d01-be", beURL, evalMyTBackendBody, nil)
	e.seedCapture(t, "eval/d01-senior", srURL, evalMyTSeniorBody, nil)

	first := e.save(t, "d01-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "myTomorrows"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "eval/d01-be", "myTomorrows")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": beURL, "originalText": evalMyTBackendBody,
				"locationText": "Amsterdam", "vacancyComplete": "true",
				"boardProvider": "ashby", "board": "myTomorrows",
				"boardRecordId": "863ede5e-b578-431e-b036-a5ba69e501c5"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "eval/d01-be", "developing and maintaining the platform infrastructure")},
			IdentityDecision: newDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first save: %+v", first)
	}
	oppBE, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	out, assessor := e.matchOne(t,
		map[string]string{
			"cmp-opportunity-" + oppBE: identity.AnswerDistinct,
			"cmp-company-" + coID:      identity.AnswerSame,
		},
		researchcontract.MatchAttributes{Employer: "myTomorrows", Title: "Senior Backend Engineer",
			URL: srURL, Location: "Amsterdam"},
		e.ref(t, "eval/d01-senior", "prior tech-lead scope"))
	if assessor.calls != 1 {
		t.Fatalf("hard negative needs a semantic comparison, calls=%d", assessor.calls)
	}
	if exact := evalExactIDs(out); exact[oppBE] != "" {
		t.Fatalf("title-word overlap must NOT merge distinct roles: %+v", out.ExactMatches)
	}
	for _, p := range out.PossibleMatches {
		if p.RecordID == oppBE {
			t.Fatalf("confirmed-distinct role must not surface as possible: %+v", out.PossibleMatches)
		}
	}
	if exact := evalExactIDs(out); exact[coID] != "company" {
		t.Fatalf("same employer must still match: %+v", out.ExactMatches)
	}

	second := e.save(t, "d01-second", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Senior Backend Engineer",
			"sourceUrl": srURL, "originalText": evalMyTSeniorBody,
			"locationText": "Amsterdam", "vacancyComplete": "true",
			"boardProvider": "ashby", "board": "myTomorrows",
			"boardRecordId": "52f27ead-683f-4047-9185-f14c75324893"},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "eval/d01-senior", "prior tech-lead scope")},
		IdentityDecision: newDecision(),
	})
	if second.Outcome != researchcontract.OutcomeOK || second.Saved[0].RecordID == oppBE {
		t.Fatalf("distinct role needs its own record ID: %+v", second)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 2 {
		t.Fatalf("TWO vacancy identities required, got %d", n)
	}
	// Save-time board distinction: reusing the first board record id for a
	// new create converges instead of duplicating (false-merge prevention).
	dup := e.save(t, "d01-dup", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Backend Engineer",
			"sourceUrl": beURL, "originalText": evalMyTBackendBody,
			"boardProvider": "ashby", "board": "myTomorrows",
			"boardRecordId": "863ede5e-b578-431e-b036-a5ba69e501c5"},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("eval/d01-be")},
		IdentityDecision: newDecision(),
	})
	if dup.Outcome != researchcontract.OutcomeIdentityAmbiguous {
		t.Fatalf("duplicate board record id must converge: %+v", dup)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 2 {
		t.Fatalf("convergence must leave exactly two records, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Distinct same-title: D02 (synthetic hard negative, different employers).
// DISTINCTION: identical titles alone establish nothing.
// ---------------------------------------------------------------------------

func TestCorpusEval_D02_DistinctIdenticalTitles(t *testing.T) {
	e := newEvalEnv(t)
	bodies := loadCorpusBodies(t)
	acmeURL := "https://example-careers-acme.test/jobs/ACME-BE-9"
	brewURL := "https://example-careers-brouwerij.test/vacatures/VBB-14"
	e.seedCapture(t, "syn/acme-backend", acmeURL, bodies["syn/acme-backend"], nil)
	e.seedCapture(t, "syn/brewer-backend", brewURL, bodies["syn/brewer-backend"], nil)

	first := e.save(t, "d02-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Acme Robotics"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/acme-backend", "Acme Robotics")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": acmeURL, "originalText": bodies["syn/acme-backend"],
				"locationText": "Eindhoven", "vacancyComplete": "true",
				"requisitionId": "ACME-BE-9", "reqIssuer": "acme"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/acme-backend", "Go, Kubernetes")},
			IdentityDecision: newDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first save: %+v", first)
	}
	oppAcme := first.Saved[1].RecordID

	out, _ := e.matchOne(t,
		map[string]string{"cmp-opportunity-" + oppAcme: identity.AnswerDistinct},
		researchcontract.MatchAttributes{Employer: "De Voorbeeld Brouwerij",
			Title: "Backend Engineer", URL: brewURL, Location: "Rotterdam"},
		e.ref(t, "syn/brewer-backend", "PHP, Laravel"))
	if exact := evalExactIDs(out); exact[oppAcme] != "" {
		t.Fatalf("identical titles must not merge different employers: %+v", out.ExactMatches)
	}

	second := e.save(t, "d02-second",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "De Voorbeeld Brouwerij"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/brewer-backend", "De Voorbeeld Brouwerij")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": brewURL, "originalText": bodies["syn/brewer-backend"],
				"locationText": "Rotterdam", "vacancyComplete": "true",
				"requisitionId": "VBB-14", "reqIssuer": "brouwerij"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/brewer-backend", "PHP, Laravel")},
			IdentityDecision: newDecision()},
	)
	if second.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("second save: %+v", second)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 2 {
		t.Fatalf("TWO vacancy identities required, got %d", n)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM companies`); n != 2 {
		t.Fatalf("two employers required, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Changed postings: R01 (same URL/req, materially updated claims).
// DISTINCTION: SAME identity, versioned claims (v1 preserved), suitability
// reassessed, v1 assessment never silently rebound to v2.
// ---------------------------------------------------------------------------

func TestCorpusEval_R01_ChangedPostingVersions(t *testing.T) {
	e := newEvalEnv(t)
	bodies := loadCorpusBodies(t)
	postURL := "https://example-careers-northwind.test/jobs/NW-204"
	e.seedCapture(t, "syn/NW-204-v1", postURL, bodies["syn/NW-204-v1"], nil)
	e.seedCapture(t, "syn/NW-204-v2", postURL, bodies["syn/NW-204-v2"], nil)

	first := e.save(t, "r01-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Northwind Technologies"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/NW-204-v1", "Northwind")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": postURL, "originalText": bodies["syn/NW-204-v1"],
				"locationText": "Amsterdam, hybrid", "vacancyComplete": "true",
				"requisitionId": "NW-204", "reqIssuer": "northwind"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/NW-204-v1", "Amsterdam, hybrid")},
			IdentityDecision: newDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("v1 save: %+v", first)
	}
	oppID, coID := first.Saved[1].RecordID, first.Saved[0].RecordID
	asmV1 := seedAssessment(t, e.store, seedAssessmentOpts{id: "asm-r01-v1", jevID: "jev-r01-v1",
		roundID: e.round.ID, attemptID: "seed-r01-v1", profile: 1, rubric: "rubric-v1",
		status: "succeeded", evidenceRef: "syn/NW-204-v1", spanEnd: int64(len(bodies["syn/NW-204-v1"]))})

	out, _ := e.matchOne(t,
		map[string]string{
			"cmp-opportunity-" + oppID: identity.AnswerSame,
			"cmp-company-" + coID:      identity.AnswerSame,
		},
		researchcontract.MatchAttributes{Employer: "Northwind Technologies",
			Title: "Backend Engineer", URL: postURL, RequisitionID: "NW-204",
			Location: "Remote within EU"},
		e.ref(t, "syn/NW-204-v2", "Remote within EU"))
	if exact := evalExactIDs(out); exact[oppID] != "opportunity" {
		t.Fatalf("changed posting must stay the same identity: %+v", out.ExactMatches)
	}

	v2 := e.save(t, "r01-v2", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 1,
		Fields: map[string]string{"originalText": bodies["syn/NW-204-v2"],
			"locationText": "Remote within EU", "sourceUrl": postURL,
			"requisitionId": "NW-204", "reqIssuer": "northwind",
			"compCurrency": "EUR", "compMinCents": "7000000", "compMaxCents": "8500000",
			"compPeriod": "year", "compBasis": "base"},
		EvidenceLinks: []researchcontract.EvidenceLink{e.link(t, "syn/NW-204-v2", "EUR 70,000-85,000 per year")},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 1},
			{CandidateID: coID, Kind: "company", Revision: 1},
		}},
	})
	if v2.Outcome != researchcontract.OutcomeOK || v2.Saved[0].RecordID != oppID {
		t.Fatalf("v2 must update the same record: %+v", v2)
	}
	_, location, _, _, revision := evalOppRow(t, e.store, oppID)
	if location != "Remote within EU" || revision != 2 {
		t.Fatalf("row must show v2 claims at revision 2: %q rev=%d", location, revision)
	}
	snaps := evalSnapshots(t, e.store, oppID)
	if len(snaps) != 2 || !strings.Contains(snaps[0], "Amsterdam, hybrid") ||
		!strings.Contains(snaps[1], "Remote within EU") {
		t.Fatalf("v1 claims must be preserved alongside v2: %+v", snaps)
	}
	sightings := evalSightings(t, e.store, oppID)
	if len(sightings) != 2 {
		t.Fatalf("a new sighting must be appended: %+v", sightings)
	}
	var currency, period string
	var minCents, maxCents int64
	if err := e.store.Read(context.Background(), func(r store.Reader) error {
		return r.QueryRowContext(context.Background(),
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
	rebind := e.save(t, "r01-rebind", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 2,
		Fields:        map[string]string{"notes": "rebind probe"},
		EvidenceLinks: []researchcontract.EvidenceLink{e.fullLink("syn/NW-204-v2")},
		AssessmentIDs: []string{asmV1},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 2},
		}},
	})
	if rebind.Outcome != researchcontract.OutcomeRevisionConflict {
		t.Fatalf("v1 assessment must not rebind to v2: %+v", rebind)
	}
	// Reassessment against the new claims binds and saves.
	asmV2 := seedAssessment(t, e.store, seedAssessmentOpts{id: "asm-r01-v2", jevID: "jev-r01-v2",
		roundID: e.round.ID, attemptID: "seed-r01-v2", profile: 1, rubric: "rubric-v1",
		status: "succeeded", evidenceRef: "syn/NW-204-v2", spanEnd: int64(len(bodies["syn/NW-204-v2"]))})
	reassessed := e.save(t, "r01-reassessed", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 2,
		Fields:        map[string]string{"notes": "reassessed against v2"},
		EvidenceLinks: []researchcontract.EvidenceLink{e.fullLink("syn/NW-204-v2")},
		AssessmentIDs: []string{asmV2},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 2},
		}},
	})
	if reassessed.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("reassessed v2 save: %+v", reassessed)
	}
}

// ---------------------------------------------------------------------------
// Reused identifiers: R02 (req ENG-2041 backend, then frontend).
// DISTINCTION: NEW identity; the req id is qualified by reuse history, never
// a unique key by itself; no overwrite or merge of the old record.
// ---------------------------------------------------------------------------

func TestCorpusEval_R02_ReusedReqIDNewIdentity(t *testing.T) {
	e := newEvalEnv(t)
	bodies := loadCorpusBodies(t)
	reqURL := "https://example-careers-initech.test/jobs/ENG-2041"
	e.seedCapture(t, "syn/ENG-2041-backend", reqURL, bodies["syn/ENG-2041-backend"], nil)
	e.seedCapture(t, "syn/ENG-2041-frontend", reqURL, bodies["syn/ENG-2041-frontend"], nil)

	first := e.save(t, "r02-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Initech"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/ENG-2041-backend", "ENG-2041")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer (Go)",
				"sourceUrl": reqURL, "originalText": bodies["syn/ENG-2041-backend"],
				"vacancyComplete": "true", "requisitionId": "ENG-2041", "reqIssuer": "initech"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/ENG-2041-backend", "Backend Engineer (Go)")},
			IdentityDecision: newDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("old save: %+v", first)
	}
	oppGo, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	out, _ := e.matchOne(t,
		map[string]string{
			"cmp-opportunity-" + oppGo: identity.AnswerDistinct,
			"cmp-company-" + coID:      identity.AnswerSame,
		},
		researchcontract.MatchAttributes{Employer: "Initech", Title: "Frontend Engineer (React)",
			URL: reqURL, RequisitionID: "ENG-2041"},
		e.ref(t, "syn/ENG-2041-frontend", "Frontend Engineer (React)"))
	if exact := evalExactIDs(out); exact[oppGo] != "" {
		t.Fatalf("reused req id must not merge materially different content: %+v", out.ExactMatches)
	}

	// Without the explicit reuse signal the shared strong key converges:
	// no silent overwrite is possible.
	guard := e.save(t, "r02-guard", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Frontend Engineer (React)",
			"sourceUrl": reqURL, "originalText": bodies["syn/ENG-2041-frontend"],
			"requisitionId": "ENG-2041", "reqIssuer": "initech"},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/ENG-2041-frontend")},
		IdentityDecision: newDecision(),
	})
	if guard.Outcome != researchcontract.OutcomeIdentityAmbiguous {
		t.Fatalf("unflagged req reuse must converge: %+v", guard)
	}

	reuse := e.save(t, "r02-reuse", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Frontend Engineer (React)",
			"sourceUrl": reqURL, "originalText": bodies["syn/ENG-2041-frontend"],
			"requisitionId": "ENG-2041", "reqIssuer": "initech", "reusedIdentifier": "true",
			"notes": "ENG-2041 now advertises a React frontend role; Go backend opening superseded by reuse"},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/ENG-2041-frontend", "Materially different opening reusing req ID ENG-2041")},
		IdentityDecision: newDecision(),
	})
	if reuse.Outcome != researchcontract.OutcomeOK || reuse.Saved[0].RecordID == oppGo {
		t.Fatalf("reused req needs a NEW record: %+v", reuse)
	}
	oppReact := reuse.Saved[0].RecordID
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 2 {
		t.Fatalf("two identities required, got %d", n)
	}
	title, _, _, _, _ := evalOppRow(t, e.store, oppGo)
	if title != "Backend Engineer (Go)" {
		t.Fatalf("old record must keep its historical content: %q", title)
	}
	oldKeys, newKeys := evalKeys(t, e.store, oppGo), evalKeys(t, e.store, oppReact)
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
	sightings := evalSightings(t, e.store, oppReact)
	if len(sightings) != 1 || sightings[0].SightingKind != store.SightingReusedIdentifier {
		t.Fatalf("reuse sighting must be marked: %+v", sightings)
	}
	if kept := evalSightings(t, e.store, oppGo); len(kept) != 1 {
		t.Fatalf("historical sighting must be retained: %+v", kept)
	}
}

// ---------------------------------------------------------------------------
// Reused identifiers: R03 (one slug, backend then data role).
// DISTINCTION: URL equality alone never proves sameness; the historical
// sighting (with its hash) is retained as prior content of that URL.
// ---------------------------------------------------------------------------

func TestCorpusEval_R03_ReusedURLSlugNewIdentity(t *testing.T) {
	e := newEvalEnv(t)
	bodies := loadCorpusBodies(t)
	slug := "https://example-careers-initech.test/openings/senior-engineer"
	e.seedCapture(t, "syn/slug-backend", slug, bodies["syn/slug-backend"], nil)
	e.seedCapture(t, "syn/slug-data", slug, bodies["syn/slug-data"], nil)

	first := e.save(t, "r03-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Initech"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/slug-backend", "BE-102")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": slug, "originalText": bodies["syn/slug-backend"],
				"vacancyComplete": "true", "requisitionId": "BE-102", "reqIssuer": "initech"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/slug-backend", "Req BE-102")},
			IdentityDecision: newDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("old save: %+v", first)
	}
	oppBE, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	out, _ := e.matchOne(t,
		map[string]string{
			"cmp-opportunity-" + oppBE: identity.AnswerDistinct,
			"cmp-company-" + coID:      identity.AnswerSame,
		},
		researchcontract.MatchAttributes{Employer: "Initech", Title: "Senior Data Engineer",
			URL: slug, RequisitionID: "DA-330"},
		e.ref(t, "syn/slug-data", "Req DA-330"))
	if exact := evalExactIDs(out); exact[oppBE] != "" {
		t.Fatalf("URL equality must not merge a new opening: %+v", out.ExactMatches)
	}

	guard := e.save(t, "r03-guard", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Senior Data Engineer",
			"sourceUrl": slug, "originalText": bodies["syn/slug-data"],
			"requisitionId": "DA-330", "reqIssuer": "initech"},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/slug-data")},
		IdentityDecision: newDecision(),
	})
	if guard.Outcome != researchcontract.OutcomeIdentityAmbiguous {
		t.Fatalf("unflagged URL reuse must converge: %+v", guard)
	}

	reuse := e.save(t, "r03-reuse", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Senior Data Engineer",
			"sourceUrl": slug, "originalText": bodies["syn/slug-data"],
			"requisitionId": "DA-330", "reqIssuer": "initech", "reusedIdentifier": "true"},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/slug-data", "Same URL slug, different opening")},
		IdentityDecision: newDecision(),
	})
	if reuse.Outcome != researchcontract.OutcomeOK || reuse.Saved[0].RecordID == oppBE {
		t.Fatalf("reused slug needs a NEW record: %+v", reuse)
	}
	oldKeys := evalKeys(t, e.store, oppBE)
	var oldURL *store.EntityIdentityKey
	for i := range oldKeys {
		if oldKeys[i].Namespace == store.IdentityNamespaceCanonicalURL {
			oldURL = &oldKeys[i]
		}
	}
	if oldURL == nil || oldURL.Status != store.IdentityKeySuperseded {
		t.Fatalf("old URL key must be retained as superseded history: %+v", oldKeys)
	}
	prior := evalSightings(t, e.store, oppBE)
	if len(prior) != 1 || len(prior[0].ContentSHA256) != 64 {
		t.Fatalf("historical sighting with hash must be retained: %+v", prior)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 2 {
		t.Fatalf("two identities required, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Conflicting evidence: A03 (same req, Amsterdam vs Rotterdam).
// DISTINCTION: ONE identity with a visible location conflict; both sources
// stay cited; location suitability stays UNKNOWN (unassessed), never
// presented as established fact.
// ---------------------------------------------------------------------------

func TestCorpusEval_A03_LocationConflictSameIdentity(t *testing.T) {
	e := newEvalEnv(t)
	bodies := loadCorpusBodies(t)
	empURL := "https://example-careers-northwind.test/jobs/LOC-55"
	aggURL := "https://example-aggregator.test/listings/99120-northwind"
	e.seedCapture(t, "syn/LOC-55-ams", empURL, bodies["syn/LOC-55-ams"], nil)
	e.seedCapture(t, "syn/LOC-55-rtm", aggURL, bodies["syn/LOC-55-rtm"], nil)

	first := e.save(t, "a03-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Northwind"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/LOC-55-ams", "LOC-55")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": empURL, "originalText": bodies["syn/LOC-55-ams"],
				"locationText": "Amsterdam", "vacancyComplete": "true",
				"requisitionId": "LOC-55", "reqIssuer": "northwind"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "syn/LOC-55-ams", "Location: Amsterdam")},
			IdentityDecision: newDecision()},
	)
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first save: %+v", first)
	}
	oppID, coID := first.Saved[1].RecordID, first.Saved[0].RecordID

	out, _ := e.matchOne(t,
		map[string]string{
			"cmp-opportunity-" + oppID: identity.AnswerSame,
			"cmp-company-" + coID:      identity.AnswerSame,
		},
		researchcontract.MatchAttributes{Employer: "Northwind", Title: "Backend Engineer",
			URL: aggURL, RequisitionID: "LOC-55", Location: "Rotterdam"},
		e.ref(t, "syn/LOC-55-rtm", "Location: Rotterdam"))
	if exact := evalExactIDs(out); exact[oppID] != "opportunity" {
		t.Fatalf("location conflict must not split the identity: %+v", out.ExactMatches)
	}

	second := e.save(t, "a03-second", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: 1,
		Fields: map[string]string{"locationText": "Rotterdam",
			"originalText": bodies["syn/LOC-55-rtm"], "sourceUrl": aggURL,
			"requisitionId": "LOC-55", "reqIssuer": "northwind",
			"notes": "location conflicts with the employer page (Amsterdam); unresolved"},
		EvidenceLinks: []researchcontract.EvidenceLink{e.link(t, "syn/LOC-55-rtm", "Location: Rotterdam")},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: oppID, Kind: "opportunity", Revision: 1},
		}},
	})
	if second.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("conflict sighting save: %+v", second)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 1 {
		t.Fatalf("ONE identity required, got %d", n)
	}
	snaps := evalSnapshots(t, e.store, oppID)
	if len(snaps) != 2 || !strings.Contains(snaps[0], "Amsterdam") || !strings.Contains(snaps[1], "Rotterdam") {
		t.Fatalf("both locations must stay visible with their sources: %+v", snaps)
	}
	sightings := evalSightings(t, e.store, oppID)
	if len(sightings) != 2 {
		t.Fatalf("both sightings must be retained: %+v", sightings)
	}
	_, _, _, stage, _ := evalOppRow(t, e.store, oppID)
	if stage != store.UnassessedStage {
		t.Fatalf("location suitability must stay UNKNOWN (unassessed), stage=%q", stage)
	}
}

// ---------------------------------------------------------------------------
// Missing facts: M01 (no numeric pay, no weekly hours).
// DISTINCTION: pay/hours stay UNKNOWN (no invented amounts); the role saves
// as a FACTUAL, UNASSESSED record and is never presented as qualified.
// ---------------------------------------------------------------------------

func TestCorpusEval_M01_MissingFactsUnassessed(t *testing.T) {
	e := newEvalEnv(t)
	pageURL := "https://jobs.ashbyhq.com/myTomorrows/863ede5e-b578-431e-b036-a5ba69e501c5"
	e.seedCapture(t, "eval/m01", pageURL, evalMyTBackendBody, nil)

	out := e.save(t, "m01-first",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "myTomorrows"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "eval/m01", "myTomorrows")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
				"sourceUrl": pageURL, "originalText": evalMyTBackendBody,
				"locationText": "Amsterdam", "vacancyComplete": "true",
				"boardProvider": "ashby", "board": "myTomorrows",
				"boardRecordId": "863ede5e-b578-431e-b036-a5ba69e501c5"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.link(t, "eval/m01", "developing and maintaining the platform infrastructure")},
			IdentityDecision: newDecision()},
	)
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("factual unassessed save: %+v", out)
	}
	oppID := out.Saved[1].RecordID
	_, _, _, stage, _ := evalOppRow(t, e.store, oppID)
	if stage != store.UnassessedStage {
		t.Fatalf("missing-facts role must be labeled unassessed, stage=%q", stage)
	}
	// Unknown pay persists as an explicit unknown row (currency unknown, no
	// amounts) — never an invented figure.
	if n := countRows(t, e.store, `SELECT count(*) FROM compensation WHERE opportunity_id=? AND
  (currency != 'unknown' OR min_amount_cents IS NOT NULL OR max_amount_cents IS NOT NULL)`, oppID); n != 0 {
		t.Fatalf("unknown pay must persist no invented amounts, rows=%d", n)
	}
	snaps := evalSnapshots(t, e.store, oppID)
	if len(snaps) != 1 || !strings.Contains(snaps[0], "developing and maintaining") {
		t.Fatalf("factual snapshot must cite the capture: %+v", snaps)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM jev_assessments_dynamic`); n != 0 {
		t.Fatalf("no assessment may qualify silence: %d rows", n)
	}
}

// ---------------------------------------------------------------------------
// Missing facts: M02 (search snippet only).
// DISTINCTION: the snippet is DISCOVERY evidence only; a complete-vacancy
// claim from it fails capture_incomplete; no qualified opportunity arises.
// ---------------------------------------------------------------------------

func TestCorpusEval_M02_SnippetBlocked(t *testing.T) {
	e := newEvalEnv(t)
	bodies := loadCorpusBodies(t)
	e.seedCapture(t, "syn/snippet-go-ams", "search results page (no posting URL visited)",
		bodies["syn/snippet-go-ams"], func(fb *fakeCaptureBody) { fb.snippet = true })

	blocked := e.save(t, "m02-blocked",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "ExampleCorp"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/snippet-go-ams")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Senior Go Engineer",
				"originalText": bodies["syn/snippet-go-ams"],
				"locationText": "Amsterdam", "vacancyComplete": "true"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/snippet-go-ams")},
			IdentityDecision: newDecision()},
	)
	if blocked.Outcome != researchcontract.OutcomeCaptureIncomplete {
		t.Fatalf("snippet complete-vacancy claim must fail capture_incomplete: %+v", blocked)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 0 {
		t.Fatalf("blocked batch must leave no records, got %d", n)
	}

	partial := e.save(t, "m02-partial",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "ExampleCorp"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/snippet-go-ams")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "Senior Go Engineer (snippet)",
				"originalText": bodies["syn/snippet-go-ams"], "locationText": "Amsterdam"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/snippet-go-ams")},
			IdentityDecision: newDecision()},
	)
	if partial.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("snippet partial find must save without the complete claim: %+v", partial)
	}
	_, _, _, stage, _ := evalOppRow(t, e.store, partial.Saved[1].RecordID)
	if stage != store.UnassessedStage {
		t.Fatalf("snippet find must stay unassessed, stage=%q", stage)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM jev_assessments_dynamic`); n != 0 {
		t.Fatalf("no qualified opportunity from a snippet alone: %d assessments", n)
	}
}

// ---------------------------------------------------------------------------
// Brief change: B01 (Crisp capture reassessed under brief v2, no refetch).
// DISTINCTION: one capture with two assessments across brief versions; v1 is
// superseded, never rebound; v1 evidence under the v2 brief fails stale.
// ---------------------------------------------------------------------------

func TestCorpusEval_B01_BriefChangeReassessment(t *testing.T) {
	ctx := context.Background()
	e := newEvalEnv(t)
	crispURL := "https://example.test/crisp"
	e.seedCapture(t, "eval/crisp", crispURL, evalCrispBody, nil)
	frontStart, frontEnd := evalSpan(t, evalCrispBody, "Code across our stack - frontend and backend.")
	hoursStart, hoursEnd := evalSpan(t, evalCrispBody, "36-40 hours per week")

	if err := e.store.ResearchWrite(ctx, func(db store.ResearchDB) error {
		for _, att := range []string{"att-b01-v1", "att-b01-v2"} {
			if _, err := db.ExecContext(ctx, `INSERT INTO round_attempts
  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
   requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at)
  VALUES (?,?,?,?,'research.fetch','research',1,'reserved',0,0,0,0,?,?)`,
				att, e.round.ID, "seed-"+att,
				store.FixtureSHA256("seed-"+att), store.FixtureTime, store.FixtureTime); err != nil {
				return err
			}
		}
		if err := store.SeedResearchJevAttempt(ctx, db, "jev-b01-v1", e.round.ID, "att-b01-v1", 0); err != nil {
			return err
		}
		return store.SeedResearchJevAttempt(ctx, db, "jev-b01-v2", e.round.ID, "att-b01-v2", 0)
	}); err != nil {
		t.Fatal(err)
	}

	evidenceJSON := `[{"capture_id":"eval/crisp","span_start":` + itoa64(frontStart) + `,"span_end":` + itoa64(frontEnd) +
		`},{"capture_id":"eval/crisp","span_start":` + itoa64(hoursStart) + `,"span_end":` + itoa64(hoursEnd) + `}]`
	sink := &identity.StoreSink{Store: e.store}
	v1 := jevassess.DynamicAssessmentRecord{ID: "jda_b01_v1", RunID: e.round.ID,
		JevAttemptID: "jev-b01-v1", Purpose: "role_fit",
		QuestionsJSON:    []byte(`[{"id":"q-frontend","text":"Frontend duties?","alternatives":[{"id":"present","label":"Present"}],"abstainAllowed":true}]`),
		EvidenceRefsJSON: []byte(evidenceJSON),
		ProfileVersion:   1, RubricVersion: "rubric-v1",
		CandidatesJSON: []byte(`[]`), CandidateSetHash: store.FixtureSHA256("candidates-b01-v1"),
		RequestedModel: "jev-1.13.0", ReuseKey: store.FixtureSHA256("reuse-b01-v1"),
		Status:      store.DynamicAssessmentSucceeded,
		AnswersJSON: []byte(`[{"questionId":"q-frontend","answerId":"present"}]`)}
	if err := sink.SaveDynamicAssessment(ctx, v1); err != nil {
		t.Fatal(err)
	}
	// Reassessment under brief v2: same capture and spans, new answers, no
	// refetch. The outcome changes on the stated ground (frontend duties
	// now hard-excluded under v2).
	v2 := v1
	v2.ID, v2.JevAttemptID, v2.ProfileVersion, v2.RubricVersion = "jda_b01_v2", "jev-b01-v2", 2, "rubric-v2"
	v2.ReuseKey, v2.CandidateSetHash = store.FixtureSHA256("reuse-b01-v2"), store.FixtureSHA256("candidates-b01-v2")
	v2.SupersedesID = "jda_b01_v1"
	v2.AnswersJSON = []byte(`[{"questionId":"q-frontend","answerId":"present-excluded-by-v2"}]`)
	if err := sink.SaveDynamicAssessment(ctx, v2); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Read(ctx, func(r store.Reader) error {
		gotV2, err := store.GetDynamicAssessment(ctx, r, "jda_b01_v2")
		if err != nil || gotV2.SupersedesID != "jda_b01_v1" {
			t.Fatalf("v2 must chain to v1: %+v %v", gotV2, err)
		}
		if gotV2.ProfileVersion != 2 || gotV2.ReuseKey == v1.ReuseKey {
			t.Fatalf("v2 must bind the new brief with a new key: %+v", gotV2)
		}
		gotV1, err := store.GetDynamicAssessment(ctx, r, "jda_b01_v1")
		if err != nil || gotV1.SupersedesID != "" ||
			!strings.Contains(gotV1.AnswersJSON, `"answerId":"present"`) {
			t.Fatalf("v1 must stay immutable: %+v %v", gotV1, err)
		}
		links, err := store.ListAssessmentsByCapture(ctx, r, "eval/crisp")
		if err != nil {
			return err
		}
		byAsm := map[string]int{}
		for _, l := range links {
			byAsm[l.AssessmentID]++
		}
		if byAsm["jda_b01_v1"] != 2 || byAsm["jda_b01_v2"] != 2 {
			t.Fatalf("one capture with two assessments required: %+v", links)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The v1 assessment judged brief v1: citing it under the current v2
	// brief fails stale; the v2 assessment saves.
	e.briefs.profile, e.briefs.rubric = 2, "rubric-v2"
	co := e.save(t, "b01-co", researchcontract.SaveItem{
		Op:               researchcontract.SaveCreateCompany,
		Fields:           map[string]string{"name": "Crisp"},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("eval/crisp")},
		IdentityDecision: newDecision(),
	})
	coID := co.Saved[0].RecordID
	stale := e.save(t, "b01-stale", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Developer",
			"sourceUrl": crispURL, "originalText": evalCrispBody},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("eval/crisp")},
		AssessmentIDs:    []string{"jda_b01_v1"},
		IdentityDecision: newDecision(),
	})
	if stale.Outcome != researchcontract.OutcomeStale {
		t.Fatalf("v1 assessment under brief v2 must fail stale: %+v", stale)
	}
	fresh := e.save(t, "b01-fresh", researchcontract.SaveItem{
		Op: researchcontract.SaveCreateOpportunity,
		Fields: map[string]string{"companyId": coID, "title": "Developer",
			"sourceUrl": crispURL, "originalText": evalCrispBody},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("eval/crisp")},
		AssessmentIDs:    []string{"jda_b01_v2"},
		IdentityDecision: newDecision(),
	})
	if fresh.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("v2 assessment under brief v2 must save: %+v", fresh)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ---------------------------------------------------------------------------
// Save races and replay (plan §11; Q02 at the D level).
// DISTINCTION: concurrent same-identity saves converge to ONE record;
// identical replay returns the same IDs without a second charge; changed
// payload conflicts; changed candidates conflict; failed batches leave
// nothing behind. (Dispatch-level exact-request reuse is lane C scope.)
// ---------------------------------------------------------------------------

func TestCorpusEval_SaveRaceAndReplay(t *testing.T) {
	e := newEvalEnv(t)
	bodies := loadCorpusBodies(t)
	aggURL := "https://example-aggregator.test/listings/88412-northwind-backend"
	e.seedCapture(t, "syn/NW-117-aggregator", aggURL, bodies["syn/NW-117-aggregator"], nil)
	co := e.save(t, "race-co", researchcontract.SaveItem{
		Op:               researchcontract.SaveCreateCompany,
		Fields:           map[string]string{"name": "Northwind Technologies"},
		EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/NW-117-aggregator")},
		IdentityDecision: newDecision(),
	})
	coID := co.Saved[0].RecordID
	racer := func(key string) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: key, RunID: e.round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{
				Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": coID, "title": "Sr. Backend Engineer (Python)",
					"sourceUrl": aggURL, "originalText": bodies["syn/NW-117-aggregator"],
					"requisitionId": "NW-117", "reqIssuer": "northwind"},
				EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/NW-117-aggregator")},
				IdentityDecision: newDecision(),
			}},
		}
	}

	const racers = 8
	outs := make([]researchcontract.SaveOutput, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := e.saver.Save(context.Background(), racer("race-nw117-"+string(rune('a'+i))))
			if err != nil {
				t.Errorf("racer %d: %v", i, err)
				return
			}
			outs[i] = out
		}(i)
	}
	wg.Wait()
	var winner, winnerKey string
	ambiguous := 0
	for i, out := range outs {
		switch out.Outcome {
		case researchcontract.OutcomeOK:
			if winner != "" {
				t.Fatalf("two racers committed: %s and %+v (racer %d)", winner, out, i)
			}
			winner = out.Saved[0].RecordID
			winnerKey = "race-nw117-" + string(rune('a'+i))
		case researchcontract.OutcomeIdentityAmbiguous:
			ambiguous++
		default:
			t.Fatalf("racer %d: unexpected outcome %+v", i, out)
		}
	}
	if winner == "" || ambiguous != racers-1 {
		t.Fatalf("one winner + %d ambiguous required: %+v", racers-1, outs)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 1 {
		t.Fatalf("duplicate racers must converge to ONE record, got %d", n)
	}

	// Identical replay of the winner's key returns the same IDs without a
	// second write (Q02 at the save level); a changed payload conflicts.
	replayed, err := e.saver.Save(context.Background(), racer(winnerKey))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.Outcome != researchcontract.OutcomeReused || len(replayed.Saved) != 1 ||
		replayed.Saved[0].RecordID != winner || len(replayed.ReusedIDs) != 1 ||
		replayed.ReusedIDs[0] != winner {
		t.Fatalf("replay must return the same IDs: %+v", replayed)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM opportunities`); n != 1 {
		t.Fatalf("replay must not duplicate, got %d", n)
	}
	mutated := racer(winnerKey)
	mutated.Items[0].Fields["title"] = "Something Else Entirely"
	if _, err := e.saver.Save(context.Background(), mutated); evalContractCode(t, err) != researchcontract.OutcomeConflict {
		t.Fatalf("changed payload must conflict, got %v", err)
	}

	// Changed match candidates cause a conflict, never a silent write.
	_, _, _, _, rev := evalOppRow(t, e.store, winner)
	bump := e.save(t, "race-bump", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: winner, ExpectedRevision: rev,
		Fields:        map[string]string{"notes": "bump"},
		EvidenceLinks: []researchcontract.EvidenceLink{e.fullLink("syn/NW-117-aggregator")},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: winner, Kind: "opportunity", Revision: rev},
		}},
	})
	if bump.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("bump: %+v", bump)
	}
	stalePin := e.save(t, "race-stale-pin", researchcontract.SaveItem{
		Op: researchcontract.SaveUpdateOpportunity, RecordID: winner, ExpectedRevision: rev + 1,
		Fields:        map[string]string{"notes": "stale pin probe"},
		EvidenceLinks: []researchcontract.EvidenceLink{e.fullLink("syn/NW-117-aggregator")},
		IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: []researchcontract.CandidateIdentity{
			{CandidateID: winner, Kind: "opportunity", Revision: rev}, // drifted
		}},
	})
	if stalePin.Outcome != researchcontract.OutcomeRevisionConflict || stalePin.Items[0].CurrentRevision == nil {
		t.Fatalf("drifted candidate must conflict with the live revision: %+v", stalePin)
	}

	// Failed batches leave no partial business writes.
	rollback := e.save(t, "race-rollback",
		researchcontract.SaveItem{Op: researchcontract.SaveCreateCompany,
			Fields:           map[string]string{"name": "Rollback Probe"},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/NW-117-aggregator")},
			IdentityDecision: newDecision()},
		researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": "batch:0", "title": "",
				"originalText": bodies["syn/NW-117-aggregator"]},
			EvidenceLinks:    []researchcontract.EvidenceLink{e.fullLink("syn/NW-117-aggregator")},
			IdentityDecision: newDecision()},
	)
	if rollback.Outcome == researchcontract.OutcomeOK {
		t.Fatalf("bad batch must not commit: %+v", rollback)
	}
	if n := countRows(t, e.store, `SELECT count(*) FROM companies WHERE name='Rollback Probe'`); n != 0 {
		t.Fatalf("failed batch left a partial company: %d", n)
	}

	// Identical match comparisons replay a stable idempotency key (no
	// second dispatch for the same effective judgment).
	attrs := researchcontract.MatchAttributes{Employer: "Northwind Technologies",
		Title: "Sr. Backend Engineer (Python)", URL: aggURL, RequisitionID: "NW-117"}
	_, firstAssessor := e.matchOne(t, map[string]string{
		"cmp-opportunity-" + winner: identity.AnswerSame,
		"cmp-company-" + coID:       identity.AnswerSame,
	}, attrs, e.fullRef("syn/NW-117-aggregator"))
	_, secondAssessor := e.matchOne(t, map[string]string{
		"cmp-opportunity-" + winner: identity.AnswerSame,
		"cmp-company-" + coID:       identity.AnswerSame,
	}, attrs, e.fullRef("syn/NW-117-aggregator"))
	if firstAssessor.last.IdempotencyKey == "" ||
		firstAssessor.last.IdempotencyKey != secondAssessor.last.IdempotencyKey {
		t.Fatalf("comparison key must be stable: %q vs %q",
			firstAssessor.last.IdempotencyKey, secondAssessor.last.IdempotencyKey)
	}
}

// ---------------------------------------------------------------------------
// Ambiguous suitability: A01 (reviewed Crisp judgments, fixture provider).
// DISTINCTION: ambiguous stays ambiguous (never forced present/absent),
// unknown stays unknown, hours/pay judgments carry source units verbatim.
// NOTE: the fixture provider returns the reviewed outcomes deterministically;
// this pins the binding machinery, not provider evidence (T26/T30 scope).
// ---------------------------------------------------------------------------

func TestCorpusEval_A01_AmbiguousSuitabilityBinding(t *testing.T) {
	ctx := context.Background()
	h, auth, provider, exchanges, sink := evalAssessHandler(map[string]evalJevCap{
		"eval/crisp": {body: evalCrispBody, complete: true},
	})
	span := func(substr string) researchcontract.EvidenceRef {
		start, end := evalSpan(t, evalCrispBody, substr)
		return researchcontract.EvidenceRef{CaptureID: "eval/crisp", SpanStart: start, SpanEnd: end}
	}
	frontRef := span("Code across our stack - frontend and backend.")
	hoursRef := span("36-40 hours per week")
	payRef := span("A competitive salary and equity/stock.")
	in := researchcontract.AssessInput{
		Purpose: "role_fit",
		Questions: []researchcontract.AssessQuestion{
			{ID: "q-backend-scope", Text: "Is the primary assigned work backend/platform?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "present", Label: "Present: primary backend assignment",
						EvidenceRefs: []researchcontract.EvidenceRef{span("implement new app features")}},
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
		ProfileVersion: 1, RubricVersion: "rubric-v1",
		SourceRefs:     []researchcontract.EvidenceRef{{CaptureID: "eval/crisp", SpanStart: 0, SpanEnd: int64(len(evalCrispBody))}},
		IdempotencyKey: "a01-eval", RunID: "run-1", Generation: 1,
	}
	// Reviewed outcomes, returned deterministically by the fixture.
	provider.result = evalChoiceResult(map[string]string{
		"q-backend-scope": "ambiguous", "q-frontend": "present",
		"q-pay": "unknown", "q-hours32": "not_satisfied",
	}, 0.9)
	got, err := h.Assess(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
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
	sent, ok := provider.last.Questions["q-hours32"].(jev.ChoiceQuestion)
	if !ok || !strings.Contains(sent.Criteria["not_satisfied"], "36-40 hours per week") {
		t.Fatalf("units/context must reach the judge verbatim: %+v", provider.last.Questions)
	}
	if len(exchanges.begins) != 1 || len(exchanges.finishes) != 1 ||
		exchanges.finishes[0].Status != "succeeded" || len(auth.reserves) != 1 {
		t.Fatalf("one reservation, one logged attempt required")
	}
	if len(sink.records) != 1 || sink.records[0].ReuseKey != got.ReuseKey ||
		len(got.ReuseKey) != 64 || sink.records[0].Status != "succeeded" {
		t.Fatalf("binding row must persist with the reuse key: %+v", sink.records)
	}
	if !strings.Contains(string(sink.records[0].EvidenceRefsJSON), "eval/crisp") {
		t.Fatalf("assessment must bind exact evidence: %s", sink.records[0].EvidenceRefsJSON)
	}
}

// ---------------------------------------------------------------------------
// Conflicting evidence: A02 (vacancy vs same-date employer clarification).
// DISTINCTION: frontend-duty status CONFLICTING with BOTH source spans
// preserved and cited; dependent suitability UNKNOWN (abstained).
// NOTE: persisting a conflicting flag on evidence claims is a T26 need (the
// saver writes the snapshots to cite; no claim flag exists yet).
// ---------------------------------------------------------------------------

func TestCorpusEval_A02_ConflictingEvidenceBinding(t *testing.T) {
	ctx := context.Background()
	h, _, provider, _, sink := evalAssessHandler(map[string]evalJevCap{
		"eval/vac":  {body: evalVacBody, complete: true},
		"eval/clar": {body: evalClarBody, complete: true},
	})
	vacStart, vacEnd := evalSpan(t, evalVacBody, "no frontend implementation duties")
	clarStart, clarEnd := evalSpan(t, evalClarBody, "maintain our React UI one day each week")
	vacRef := researchcontract.EvidenceRef{CaptureID: "eval/vac", SpanStart: vacStart, SpanEnd: vacEnd}
	clarRef := researchcontract.EvidenceRef{CaptureID: "eval/clar", SpanStart: clarStart, SpanEnd: clarEnd}
	// One exact span cannot support two competing alternatives of one
	// question, so the conflict verdict cites the full captures while the
	// single-sided verdicts cite their sentences. Both source IDs stay
	// bound to the conflicting status, per the reviewed expectation.
	vacFull := researchcontract.EvidenceRef{CaptureID: "eval/vac", SpanStart: 0, SpanEnd: int64(len(evalVacBody))}
	clarFull := researchcontract.EvidenceRef{CaptureID: "eval/clar", SpanStart: 0, SpanEnd: int64(len(evalClarBody))}
	in := researchcontract.AssessInput{
		Purpose: "role_fit",
		Questions: []researchcontract.AssessQuestion{
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
		ProfileVersion: 1, RubricVersion: "rubric-v1",
		SourceRefs: []researchcontract.EvidenceRef{
			{CaptureID: "eval/vac", SpanStart: 0, SpanEnd: int64(len(evalVacBody))},
			{CaptureID: "eval/clar", SpanStart: 0, SpanEnd: int64(len(evalClarBody))},
		},
		IdempotencyKey: "a02-eval", RunID: "run-1", Generation: 1,
	}
	provider.result = evalChoiceResult(map[string]string{
		"q-frontend": "conflicting", "q-suitability": jevassess.AbstainID,
	}, 0.85)
	got, err := h.Assess(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
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
	if len(conflict.SourceBindings) != 2 || !seen["eval/vac"] || !seen["eval/clar"] {
		t.Fatalf("both incompatible sources must be cited: %+v", conflict.SourceBindings)
	}
	suit := byQ["q-suitability"]
	if !suit.Abstained || suit.AnswerID != "" || len(suit.SourceBindings) != 0 {
		t.Fatalf("dependent suitability must stay UNKNOWN: %+v", suit)
	}
	if len(sink.records) != 1 || sink.records[0].Status != "partial_abstain" {
		t.Fatalf("abstention must persist as partial_abstain: %+v", sink.records)
	}
}

// ---------------------------------------------------------------------------
// Query overlap: Q01 (differently phrased overlapping investigation).
// DISTINCTION: Jev recommends REUSE with the prior evidence links and
// retained uncertainty (fixture). Prior-coverage retrieval itself is lane C.
// ---------------------------------------------------------------------------

func TestCorpusEval_Q01_SemanticReuseRecommendation(t *testing.T) {
	ctx := context.Background()
	priorBody := "Prior investigation: myTomorrows Ashby board, backend roles, captured 2026-09-23. Three backend postings reviewed with signed-off evidence."
	requestBody := "Fresh query: Backend Engineer Amsterdam HQ site:jobs.ashbyhq.com (differently phrased, overlapping intent)."
	h, _, provider, _, _ := evalAssessHandler(map[string]evalJevCap{
		"eval/prior":   {body: priorBody, complete: true},
		"eval/request": {body: requestBody, complete: true},
	})
	priorStart, priorEnd := evalSpan(t, priorBody, "myTomorrows Ashby board, backend roles")
	reqStart, reqEnd := evalSpan(t, requestBody, "Backend Engineer Amsterdam HQ")
	in := researchcontract.AssessInput{
		Purpose: "coverage_reuse",
		Questions: []researchcontract.AssessQuestion{
			{ID: "q-reuse", Text: "Does the prior coverage answer the fresh query?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "reuse", Label: "Reuse: same employer board and role family already covered",
						EvidenceRefs: []researchcontract.EvidenceRef{
							{CaptureID: "eval/prior", SpanStart: priorStart, SpanEnd: priorEnd}}},
					{ID: "new_coverage", Label: "New coverage: prior work lacks what the query needs",
						EvidenceRefs: []researchcontract.EvidenceRef{
							{CaptureID: "eval/request", SpanStart: reqStart, SpanEnd: reqEnd}}},
				}},
		},
		ProfileVersion: 1, RubricVersion: "rubric-v1",
		SourceRefs: []researchcontract.EvidenceRef{
			{CaptureID: "eval/prior", SpanStart: 0, SpanEnd: int64(len(priorBody))},
			{CaptureID: "eval/request", SpanStart: 0, SpanEnd: int64(len(requestBody))},
		},
		IdempotencyKey: "q01-eval", RunID: "run-1", Generation: 1,
	}
	provider.result = evalChoiceResult(map[string]string{"q-reuse": "reuse"}, 0.75)
	got, err := h.Assess(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Results[0].AnswerID != "reuse" || len(got.Results[0].SourceBindings) != 1 ||
		got.Results[0].SourceBindings[0].CaptureID != "eval/prior" {
		t.Fatalf("reuse must cite the prior evidence links: %+v", got.Results[0])
	}
	if got.Results[0].Uncertainty != "confidence 0.7500" {
		t.Fatalf("reuse keeps retained uncertainty: %+v", got.Results[0])
	}
	// The reuse key is input-derived: the same effective judgment replays
	// its identity under a fresh idempotency key.
	second := in
	second.IdempotencyKey = "q01-eval-retry"
	provider.result = evalChoiceResult(map[string]string{"q-reuse": "reuse"}, 0.75)
	again, err := h.Assess(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if again.ReuseKey != got.ReuseKey {
		t.Fatalf("reuse key must be stable: %q vs %q", got.ReuseKey, again.ReuseKey)
	}
}

// ---------------------------------------------------------------------------
// Jev outage and invalid results (plan §11; F01/F02 at the D level).
// DISTINCTION: explicit failure, no fabricated assessment; retries carry new
// reservations; missing/incomplete evidence fails before dispatch. Source
// fetch mechanics (404/403/429/empty) are lane C scope.
// ---------------------------------------------------------------------------

func TestCorpusEval_OutageAndBindingFailures(t *testing.T) {
	ctx := context.Background()
	newInput := func() researchcontract.AssessInput {
		return researchcontract.AssessInput{
			Purpose: "role_fit",
			Questions: []researchcontract.AssessQuestion{
				{ID: "q-fit", Text: "Fit?", AbstainAllowed: true,
					Alternatives: []researchcontract.AssessAlternative{
						{ID: "yes", Label: "Yes",
							EvidenceRefs: []researchcontract.EvidenceRef{
								{CaptureID: "eval/crisp", SpanStart: 0, SpanEnd: 10}}},
					}},
			},
			ProfileVersion: 1, RubricVersion: "rubric-v1",
			SourceRefs:     []researchcontract.EvidenceRef{{CaptureID: "eval/crisp", SpanStart: 0, SpanEnd: 10}},
			IdempotencyKey: "outage-eval", RunID: "run-1", Generation: 1,
		}
	}

	t.Run("timeout is uncertain without retry", func(t *testing.T) {
		h, auth, provider, exchanges, sink := evalAssessHandler(map[string]evalJevCap{
			"eval/crisp": {body: evalCrispBody, complete: true},
		})
		provider.err = &jev.Error{Kind: jev.ErrTimeout, Attempts: 1}
		if _, err := h.Assess(ctx, newInput()); evalContractCode(t, err) != researchcontract.OutcomeUncertain {
			t.Fatalf("outage must fail outcome_uncertain, got %v", err)
		}
		if provider.calls != 1 || len(auth.reserves) != 1 {
			t.Fatalf("exactly one call per reservation, calls=%d reserves=%d", provider.calls, len(auth.reserves))
		}
		if len(exchanges.finishes) != 1 || exchanges.finishes[0].Status != "uncertain" {
			t.Fatalf("outage must log uncertain: %+v", exchanges.finishes)
		}
		if len(sink.records) != 0 {
			t.Fatalf("no fabricated assessment may persist: %+v", sink.records)
		}
		provider.err = nil
		provider.result = evalChoiceResult(map[string]string{"q-fit": "yes"}, 0.9)
		retry := newInput()
		retry.IdempotencyKey = "outage-eval-retry"
		got, err := h.Assess(ctx, retry)
		if err != nil || got.Results[0].AnswerID != "yes" {
			t.Fatalf("explicit retry with a new reservation must succeed: %+v %v", got, err)
		}
		if len(auth.reserves) != 2 || provider.calls != 2 {
			t.Fatalf("retry is a separately reserved attempt")
		}
	})

	t.Run("malformed response keeps exact bytes", func(t *testing.T) {
		h, _, provider, exchanges, sink := evalAssessHandler(map[string]evalJevCap{
			"eval/crisp": {body: evalCrispBody, complete: true},
		})
		provider.err = &jev.Error{Kind: jev.ErrInvalidResponse, Attempts: 1}
		provider.exchange = jev.CapturedExchange{RequestBytes: provider.encoded,
			ResponseBytes: []byte(`{broken`), HTTPStatus: 200}
		if _, err := h.Assess(ctx, newInput()); evalContractCode(t, err) != researchcontract.OutcomeInvalid {
			t.Fatalf("malformed response must fail invalid, got %v", err)
		}
		if len(exchanges.finishes) != 1 || exchanges.finishes[0].Status != "invalid_response" ||
			string(exchanges.finishes[0].RawResponseBytes) != "{broken" {
			t.Fatalf("exact malformed bytes must be retained: %+v", exchanges.finishes)
		}
		if len(sink.records) != 0 {
			t.Fatal("no assessment row on provider failure")
		}
	})

	t.Run("unknown answer id is rejected and reclassified", func(t *testing.T) {
		h, _, provider, exchanges, sink := evalAssessHandler(map[string]evalJevCap{
			"eval/crisp": {body: evalCrispBody, complete: true},
		})
		provider.result = evalChoiceResult(map[string]string{"q-fit": "invented"}, 0.9)
		if _, err := h.Assess(ctx, newInput()); evalContractCode(t, err) != researchcontract.OutcomeInvalid {
			t.Fatalf("unknown answer id must fail invalid, got %v", err)
		}
		if len(exchanges.reclassified) != 1 || len(sink.records) != 0 {
			t.Fatalf("rejection must reclassify without persisting: %+v %+v", exchanges.reclassified, sink.records)
		}
	})

	t.Run("missing and incomplete evidence fail before dispatch", func(t *testing.T) {
		h, _, provider, exchanges, _ := evalAssessHandler(map[string]evalJevCap{
			"eval/crisp": {body: evalCrispBody, complete: true},
		})
		missing := newInput()
		missing.SourceRefs = []researchcontract.EvidenceRef{{CaptureID: "cap-gone", SpanStart: 0, SpanEnd: 4}}
		if _, err := h.Assess(ctx, missing); evalContractCode(t, err) != researchcontract.OutcomeNotFound {
			t.Fatalf("missing capture must fail not_found, got %v", err)
		}
		h2, _, provider2, _, _ := evalAssessHandler(map[string]evalJevCap{
			"eval/crisp": {body: "partial", complete: false},
		})
		if _, err := h2.Assess(ctx, newInput()); evalContractCode(t, err) != researchcontract.OutcomeCaptureIncomplete {
			t.Fatalf("incomplete capture must fail capture_incomplete, got %v", err)
		}
		if provider.calls+provider2.calls != 0 || len(exchanges.begins) != 0 {
			t.Fatal("unbound evidence must fail before reservation and dispatch")
		}
	})

	t.Run("stale brief fails before dispatch", func(t *testing.T) {
		h, _, provider, _, _ := evalAssessHandler(map[string]evalJevCap{
			"eval/crisp": {body: evalCrispBody, complete: true},
		})
		stale := newInput()
		stale.ProfileVersion = 2
		if _, err := h.Assess(ctx, stale); evalContractCode(t, err) != researchcontract.OutcomeStale {
			t.Fatalf("brief mismatch must fail stale, got %v", err)
		}
		if provider.calls != 0 {
			t.Fatal("stale brief must fail before dispatch")
		}
	})

	t.Run("one span cannot support two competing alternatives", func(t *testing.T) {
		h, _, provider, _, _ := evalAssessHandler(map[string]evalJevCap{
			"eval/crisp": {body: evalCrispBody, complete: true},
		})
		in := newInput()
		in.Questions[0].Alternatives = append(in.Questions[0].Alternatives,
			researchcontract.AssessAlternative{ID: "no", Label: "No",
				EvidenceRefs: []researchcontract.EvidenceRef{
					{CaptureID: "eval/crisp", SpanStart: 0, SpanEnd: 10}}})
		if _, err := h.Assess(ctx, in); evalContractCode(t, err) != researchcontract.OutcomeConflict {
			t.Fatalf("shared span must fail conflict, got %v", err)
		}
		if provider.calls != 0 {
			t.Fatal("conflicting evidence must fail before dispatch")
		}
	})
}
