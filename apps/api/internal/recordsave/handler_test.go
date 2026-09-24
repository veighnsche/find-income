package recordsave

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var (
	saveOwner = store.Actor{Kind: "administrator", ID: "owner"}
	saveAgent = store.Actor{Kind: "agent", ID: "agent-1"}
)

// fakeAuthority is a scriptable researchcontract.Authority double. The
// in-transaction T12 helpers always run for real underneath; this double
// only stands in for the handler's lock-free pre-check.
type fakeAuthority struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (f *fakeAuthority) Check(context.Context, researchcontract.CheckInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
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

type fakeCaptureBody struct {
	body         string
	snippet      bool
	completeness string
	originalURL  string
	finalURL     string
	provenance   researchcontract.ProvenanceKind
}

// fakeCaptures serves capture bytes from memory with real sha/excerpt
// semantics; unknown ids fail exactly like the production reader.
type fakeCaptures struct {
	mu     sync.Mutex
	bodies map[string]fakeCaptureBody
	calls  int
}

func (f *fakeCaptures) OpenCapture(_ context.Context, id string) (researchcontract.Capture, io.ReadCloser, error) {
	f.mu.Lock()
	body, ok := f.bodies[id]
	f.calls++
	f.mu.Unlock()
	if !ok {
		return researchcontract.Capture{}, nil, researchcontract.NewError(researchcontract.OutcomeNotFound,
			"captureId", "unknown capture "+id)
	}
	sum := sha256.Sum256([]byte(body.body))
	sha := hex.EncodeToString(sum[:])
	comp := body.completeness
	if comp == "" {
		comp = "complete"
	}
	prov := body.provenance
	if prov == "" {
		prov = researchcontract.ProvenanceFetchedResponse
	}
	desc := researchcontract.Capture{ID: sha, SHA256: sha, Bytes: int64(len(body.body)),
		IsSnippet: body.snippet, Completeness: comp, Provenance: prov, FinalURL: body.finalURL}
	return desc, io.NopCloser(strings.NewReader(body.body)), nil
}

func (f *fakeCaptures) ResolveReceipt(context.Context, string) (researchcontract.ExecutionReceipt, error) {
	return researchcontract.ExecutionReceipt{}, researchcontract.NewError(researchcontract.OutcomeNotFound,
		"receipt", "receipts are out of scope for saves")
}

func openSaveStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func saveScope() store.RoundScope {
	return store.RoundScope{
		Operations: []string{store.RoundCreateCompany, store.RoundCreateOpportunity},
		Resources:  []string{store.ResearchAuthorityResource},
		Delegates:  []string{saveAgent.ID},
	}
}

// commissionSaveRound starts and activates a running research round scoped
// for record writes through the real lifecycle.
func commissionSaveRound(t *testing.T, s *store.Store, limits store.RoundAllowance) store.Round {
	t.Helper()
	ctx := context.Background()
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, created, err := s.StartRound(ctx, saveOwner, store.StartRoundInput{
		RequestKey: "save-commission", Intent: "Save sourced roles", Outcome: "research_run",
		ProfileVersion: p.Version, Scope: saveScope(), Limits: limits,
		Deadline: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("start: %+v %v %v", r, created, err)
	}
	r, err = s.ActivateRound(ctx, saveOwner, r.ID)
	if err != nil || r.State != store.RoundRunning {
		t.Fatalf("activate: %+v %v", r, err)
	}
	return r
}

func generousLimits() store.RoundAllowance {
	return store.RoundAllowance{Requests: 60, Items: 60, Tools: 60, Turns: 8}
}

func saveHandler(s *store.Store, auth researchcontract.Authority, actor store.Actor, caps *fakeCaptures, briefs *fakeBriefs) *Handler {
	h, err := NewHandler(s, auth, actor, caps, briefs)
	if err != nil {
		panic(err)
	}
	return h
}

// seedCaptureRow inserts a source_captures row mirroring a fake body. The
// row id is the link-facing id; content addressing is by body sha.
func seedCaptureRow(t *testing.T, s *store.Store, id string, body fakeCaptureBody) {
	t.Helper()
	if body.completeness == "" {
		body.completeness = "complete"
	}
	prov := body.provenance
	if prov == "" {
		prov = researchcontract.ProvenanceFetchedResponse
	}
	sum := sha256.Sum256([]byte(body.body))
	snippet := 0
	if body.snippet {
		snippet = 1
	}
	err := s.ResearchWrite(context.Background(), func(db store.ResearchDB) error {
		_, err := db.ExecContext(context.Background(), `INSERT INTO source_captures
  (id,content_sha256,artifact_ref,byte_length,original_url,final_url,retrieved_at,
   provenance_kind,completeness,executor_identity_json,is_snippet,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,'{}',?,?)`,
			id, hex.EncodeToString(sum[:]), "fixture://"+id, len(body.body),
			nullIfEmpty(body.originalURL), nullIfEmpty(body.finalURL),
			"2026-09-24T00:00:00Z", string(prov), body.completeness, snippet, store.FixtureTime)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

type seedAssessmentOpts struct {
	id          string
	jevID       string
	roundID     string
	attemptID   string
	profile     int64
	rubric      string
	status      string
	evidenceRef string // capture row id bound by the assessment
	spanEnd     int64
}

// seedAssessment stores one dynamic assessment bound to one capture span,
// with its jev_attempt and round_attempt parents, and returns its id.
func seedAssessment(t *testing.T, s *store.Store, opts seedAssessmentOpts) string {
	t.Helper()
	ctx := context.Background()
	evidenceJSON := `[{"capture_id":"` + opts.evidenceRef + `","span_start":0,"span_end":` +
		strconv.FormatInt(opts.spanEnd, 10) + `}]`
	_, setHash, err := store.CanonicalCandidateSet(nil)
	if err != nil {
		t.Fatal(err)
	}
	var storedID string
	err = s.ResearchWrite(ctx, func(db store.ResearchDB) error {
		if _, err := db.ExecContext(ctx, `INSERT INTO round_attempts
  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
   requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at)
  VALUES (?,?,?,?,'research.fetch','research',1,'reserved',0,0,0,0,?,?)`,
			opts.attemptID, opts.roundID, "seed-"+opts.attemptID,
			store.FixtureSHA256("seed-"+opts.attemptID), store.FixtureTime, store.FixtureTime); err != nil {
			return err
		}
		if err := store.SeedResearchJevAttempt(ctx, db, opts.jevID, opts.roundID, opts.attemptID, 0); err != nil {
			return err
		}
		row, err := store.InsertDynamicAssessment(ctx, db, saveAgent, store.DynamicAssessmentInput{
			RoundID: opts.roundID, JevAttemptID: opts.jevID, Purpose: "role_fit",
			QuestionsJSON: "{}", EvidenceRefsJSON: evidenceJSON,
			ProfileVersion: opts.profile, RubricVersion: opts.rubric,
			CandidatesJSON: "[]", CandidateSetHash: setHash,
			ReuseKey: store.FixtureSHA256("reuse-" + opts.id),
			Status:   opts.status, AnswersJSON: "{}",
		})
		if err != nil {
			return err
		}
		storedID = row.ID
		return store.LinkAssessmentCapture(ctx, db, store.AssessmentCaptureLink{
			AssessmentID: row.ID, CaptureID: opts.evidenceRef, SpanStart: 0, SpanEnd: opts.spanEnd,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return storedID
}

func excerptSHA(body string, start, end int) string {
	sum := sha256.Sum256([]byte(body[start:end]))
	return hex.EncodeToString(sum[:])
}

func fullLink(id, body string) researchcontract.EvidenceLink {
	return researchcontract.EvidenceLink{CaptureID: id, SpanStart: 0,
		SpanEnd: int64(len(body)), ExcerptSHA256: excerptSHA(body, 0, len(body))}
}

func newDecision(cands ...researchcontract.CandidateIdentity) *researchcontract.IdentityDecision {
	return &researchcontract.IdentityDecision{Decision: "new", Candidates: cands}
}

func countRows(t *testing.T, s *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Read(context.Background(), func(r store.Reader) error {
		return r.QueryRowContext(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func contractErr(t *testing.T, err error) *researchcontract.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected a contract error, got nil")
	}
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *researchcontract.Error, got %T (%v)", err, err)
	}
	return cerr
}

func strptr(v string) *string { return &v }

func mustRealAuthority(t *testing.T, s *store.Store, actor store.Actor) *rounds.Authority {
	t.Helper()
	a, err := rounds.NewAuthority(s, actor)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSaveBatchCreatesCompanyAndRoles(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	bodyA := "Acme is hiring a backend engineer in Berlin."
	bodyB := "Acme is hiring a frontend engineer in Berlin."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{
		"cap-a": {body: bodyA, originalURL: "https://acme.example.com/jobs/1"},
		"cap-b": {body: bodyB, originalURL: "https://acme.example.com/jobs/2"},
	}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	seedCaptureRow(t, s, "cap-b", caps.bodies["cap-b"])
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})

	out, err := h.Save(ctx, researchcontract.SaveBatch{
		IdempotencyKey: "save-1", RunID: round.ID, Generation: 1,
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveCreateCompany,
				Fields:           map[string]string{"name": "Acme", "website": "https://acme.example.com"},
				EvidenceLinks:    []researchcontract.EvidenceLink{fullLink("cap-a", bodyA)},
				IdentityDecision: newDecision(),
			},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Backend Engineer",
					"kind": "employment", "sourceUrl": "https://acme.example.com/jobs/1",
					"originalText": bodyA, "locationText": "Berlin",
					"requisitionId": "REQ-1", "reqIssuer": "acme"},
				EvidenceLinks:    []researchcontract.EvidenceLink{fullLink("cap-a", bodyA)},
				IdentityDecision: newDecision(),
			},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Frontend Engineer",
					"sourceUrl": "https://acme.example.com/jobs/2", "originalText": bodyB,
					"locationText": "Berlin"},
				EvidenceLinks:    []researchcontract.EvidenceLink{fullLink("cap-b", bodyB)},
				IdentityDecision: newDecision(),
			},
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeOK || len(out.Saved) != 3 {
		t.Fatalf("outcome: %+v", out)
	}
	for _, saved := range out.Saved {
		if saved.RecordID == "" || saved.Revision != 1 || saved.AuditID == "" {
			t.Fatalf("bad saved record: %+v", saved)
		}
	}
	companyID, backendID, frontendID := out.Saved[0].RecordID, out.Saved[1].RecordID, out.Saved[2].RecordID

	backend, err := s.Opportunity(ctx, backendID)
	if err != nil {
		t.Fatal(err)
	}
	if backend.CompanyID != companyID || backend.Stage != store.UnassessedStage {
		t.Fatalf("backend linkage/label: %+v", backend)
	}
	frontend, err := s.Opportunity(ctx, frontendID)
	if err != nil {
		t.Fatal(err)
	}
	if frontend.Stage != store.UnassessedStage || frontend.CompanyID != companyID {
		t.Fatalf("frontend linkage/label: %+v", frontend)
	}
	if got := countRows(t, s, `SELECT count(*) FROM companies`); got != 1 {
		t.Fatalf("companies: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM opportunities`); got != 2 {
		t.Fatalf("opportunities: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM entity_identity_keys WHERE status='current'`); got != 4 {
		t.Fatalf("current keys: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM entity_identity_keys WHERE strength='strong' AND status='current'`); got != 4 {
		t.Fatalf("strong keys: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM record_sightings WHERE sighting_kind='first'`); got != 3 {
		t.Fatalf("first sightings: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM identity_decisions`); got != 3 {
		t.Fatalf("decisions: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM evidence_sources WHERE source_kind='vacancy_snapshot'`); got != 2 {
		t.Fatalf("snapshots: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM record_changes WHERE entity_kind IN ('company','opportunity')`); got != 3 {
		t.Fatalf("record changes: %d", got)
	}
	attempt, err := s.RoundAttemptForRequest(ctx, round.ID, "save-1")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != store.AttemptSucceeded || attempt.Operation != store.RecordsSaveOperation {
		t.Fatalf("attempt: %+v", attempt)
	}
	if got := countRows(t, s, `SELECT count(*) FROM round_results WHERE round_id=?`, round.ID); got != 1 {
		t.Fatalf("round results: %d", got)
	}
	links, err := s.RoundRecordChanges(ctx, round.ID)
	if err != nil || len(links) != 3 {
		t.Fatalf("record links: %v %v", links, err)
	}
	after, err := s.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Used != (store.RoundAllowance{Requests: 3, Items: 3, Tools: 3}) {
		t.Fatalf("charged: %+v", after.Used)
	}
}

func TestSaveConcurrentSameIdentityConverges(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring a backend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func(i int) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: "race-" + strconv.Itoa(i), RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": company.ID, "title": "Backend Engineer",
					"sourceUrl": "https://acme.example.com/jobs/1", "originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
			},
		}
	}
	const racers = 8
	var wg sync.WaitGroup
	outs := make([]researchcontract.SaveOutput, racers)
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = h.Save(ctx, makeBatch(i))
		}(i)
	}
	wg.Wait()
	var ok, ambiguous int
	for i := 0; i < racers; i++ {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		switch outs[i].Outcome {
		case researchcontract.OutcomeOK:
			ok++
		case researchcontract.OutcomeIdentityAmbiguous:
			ambiguous++
			if len(outs[i].Items) != 1 || outs[i].Items[0].Code != researchcontract.OutcomeIdentityAmbiguous {
				t.Fatalf("racer %d items: %+v", i, outs[i].Items)
			}
		default:
			t.Fatalf("racer %d outcome: %+v", i, outs[i])
		}
	}
	if ok != 1 || ambiguous != racers-1 {
		t.Fatalf("ok=%d ambiguous=%d", ok, ambiguous)
	}
	if got := countRows(t, s, `SELECT count(*) FROM opportunities`); got != 1 {
		t.Fatalf("opportunities: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM entity_identity_keys
  WHERE namespace='canonical_url' AND status='current'`); got != 1 {
		t.Fatalf("canonical keys: %d", got)
	}
}

func TestSaveConcurrentReplaySameKey(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring a backend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func() researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: "replay-race", RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": company.ID, "title": "Backend Engineer",
					"sourceUrl": "https://acme.example.com/jobs/1", "originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
			},
		}
	}
	const racers = 8
	var wg sync.WaitGroup
	outs := make([]researchcontract.SaveOutput, racers)
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = h.Save(ctx, makeBatch())
		}(i)
	}
	wg.Wait()
	var ok, reused int
	var want string
	for i := 0; i < racers; i++ {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if len(outs[i].Saved) != 1 {
			t.Fatalf("racer %d saved: %+v", i, outs[i])
		}
		if want == "" {
			want = outs[i].Saved[0].RecordID
		} else if outs[i].Saved[0].RecordID != want {
			t.Fatalf("racer %d diverged: %s vs %s", i, outs[i].Saved[0].RecordID, want)
		}
		switch outs[i].Outcome {
		case researchcontract.OutcomeOK:
			ok++
		case researchcontract.OutcomeReused:
			reused++
			if len(outs[i].ReusedIDs) != 1 || outs[i].ReusedIDs[0] != want {
				t.Fatalf("racer %d reused ids: %+v", i, outs[i])
			}
		default:
			t.Fatalf("racer %d outcome: %+v", i, outs[i])
		}
	}
	if ok != 1 || reused != racers-1 {
		t.Fatalf("ok=%d reused=%d", ok, reused)
	}
	if got := countRows(t, s, `SELECT count(*) FROM round_attempts WHERE round_id=?`, round.ID); got != 1 {
		t.Fatalf("attempts: %d", got)
	}
	after, err := s.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Used != (store.RoundAllowance{Requests: 1, Items: 1, Tools: 1}) {
		t.Fatalf("charged: %+v", after.Used)
	}
}

func TestSaveChangedCandidatesConflict(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring a backend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func(key string, rev int64) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: key, RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": company.ID, "title": "Backend Engineer",
					"sourceUrl": "https://acme.example.com/jobs/1", "originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "new",
					Candidates: []researchcontract.CandidateIdentity{
						{CandidateID: company.ID, Kind: "company", Revision: rev}}},
			}},
		}
	}
	// The employer record moves after the decision pinned revision 1.
	if _, _, err := s.PatchCompany(ctx, saveOwner, company.ID,
		store.CompanyPatch{ExpectedRevision: 1, Notes: strptr("owner note")}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Save(ctx, makeBatch("drift-1", 1))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeRevisionConflict || len(out.Items) != 1 {
		t.Fatalf("outcome: %+v", out)
	}
	if out.Items[0].CurrentRevision == nil || *out.Items[0].CurrentRevision != 2 {
		t.Fatalf("current revision: %+v", out.Items[0])
	}
	if got := countRows(t, s, `SELECT count(*) FROM opportunities`); got != 0 {
		t.Fatalf("opportunities: %d", got)
	}
	// Re-matching against the live revision retries cleanly under a new key.
	out, err = h.Save(ctx, makeBatch("drift-2", 2))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeOK || len(out.Saved) != 1 {
		t.Fatalf("retry outcome: %+v", out)
	}
}

func TestSaveBatchRollbackLeavesNothing(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})

	out, err := h.Save(ctx, researchcontract.SaveBatch{
		IdempotencyKey: "rollback-1", RunID: round.ID, Generation: 1,
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveCreateCompany,
				Fields:        map[string]string{"name": "Acme", "website": "https://acme.example.com"},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)},
			},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "no-such-company", "title": "Ghost",
					"originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)},
			},
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeInvalid || len(out.Items) != 1 || out.Items[0].Index != 1 {
		t.Fatalf("outcome: %+v", out)
	}
	for table, want := range map[string]int{
		"companies": 0, "opportunities": 0, "entity_identity_keys": 0,
		"record_sightings": 0, "identity_decisions": 0, "evidence_sources": 0,
		"round_attempts": 0, "round_results": 0, "round_record_changes": 0,
	} {
		if got := countRows(t, s, `SELECT count(*) FROM `+table); got != want {
			t.Fatalf("%s: %d", table, got)
		}
	}
	after, err := s.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Used != (store.RoundAllowance{}) {
		t.Fatalf("charged on rollback: %+v", after.Used)
	}
}

func TestSaveExactReplayAfterCredentialRotation(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring a backend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	auth := mustRealAuthority(t, s, saveAgent)
	h := saveHandler(s, auth, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	batch := researchcontract.SaveBatch{
		IdempotencyKey: "rotation-replay", RunID: round.ID, Generation: 1,
		Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": company.ID, "title": "Backend Engineer",
				"sourceUrl": "https://acme.example.com/jobs/1", "originalText": body},
			EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
		},
	}
	first, err := h.Save(ctx, batch)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first: %+v", first)
	}
	auth.RotateCredentials(round.ID)
	auth.RotateCredentials(round.ID)
	second, err := h.Save(ctx, batch)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("replay outcome: %+v", second)
	}
	if len(second.Saved) != 1 || second.Saved[0] != first.Saved[0] {
		t.Fatalf("replay ids: %+v vs %+v", second.Saved, first.Saved)
	}
	if len(second.ReusedIDs) != 1 || second.ReusedIDs[0] != first.Saved[0].RecordID {
		t.Fatalf("reused ids: %+v", second)
	}
	after, err := s.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Used != (store.RoundAllowance{Requests: 1, Items: 1, Tools: 1}) {
		t.Fatalf("charged: %+v", after.Used)
	}
}

func TestSaveChangedPayloadConflicts(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring a backend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func(title string) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: "payload-key", RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": company.ID, "title": title,
					"sourceUrl": "https://acme.example.com/jobs/1", "originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
			},
		}
	}
	if _, err := h.Save(ctx, makeBatch("Backend Engineer")); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err = h.Save(ctx, makeBatch("Senior Backend Engineer"))
	cerr := contractErr(t, err)
	if cerr.Code != researchcontract.OutcomeConflict {
		t.Fatalf("code: %+v", cerr)
	}
}

func TestSaveMultipleRolesPerBatch(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	bodyOld := "Acme seeks a backend engineer."
	bodyNew := "Acme seeks a senior backend engineer, remote."
	bodyBeta := "Beta seeks a designer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{
		"cap-old":  {body: bodyOld},
		"cap-new":  {body: bodyNew},
		"cap-beta": {body: bodyBeta},
	}}
	seedCaptureRow(t, s, "cap-old", caps.bodies["cap-old"])
	seedCaptureRow(t, s, "cap-new", caps.bodies["cap-new"])
	seedCaptureRow(t, s, "cap-beta", caps.bodies["cap-beta"])
	acme, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	backend, _, err := s.CreateOpportunity(ctx, saveOwner, store.OpportunityInput{
		CompanyID: acme.ID, Title: "Backend Engineer", Kind: "employment",
		Stage: "new", SourceURL: "https://acme.example.com/jobs/1", OriginalText: bodyOld,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})

	out, err := h.Save(ctx, researchcontract.SaveBatch{
		IdempotencyKey: "multi-1", RunID: round.ID, Generation: 1,
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveCreateCompany,
				Fields:        map[string]string{"name": "Beta", "website": "https://beta.example.com"},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-beta", bodyBeta)},
			},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Designer",
					"sourceUrl": "https://beta.example.com/jobs/9", "originalText": bodyBeta},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-beta", bodyBeta)},
			},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": acme.ID, "title": "Data Engineer",
					"sourceUrl": "https://acme.example.com/jobs/2", "originalText": bodyOld},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-old", bodyOld)},
			},
			{Op: researchcontract.SaveUpdateOpportunity, RecordID: backend.ID, ExpectedRevision: 1,
				Fields: map[string]string{"title": "Senior Backend Engineer",
					"originalText": bodyNew, "workPattern": "remote"},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-new", bodyNew)},
			},
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeOK || len(out.Saved) != 4 {
		t.Fatalf("outcome: %+v", out)
	}
	seen := map[string]bool{}
	for _, saved := range out.Saved {
		if seen[saved.RecordID] {
			t.Fatalf("duplicate id: %+v", out.Saved)
		}
		seen[saved.RecordID] = true
	}
	// The canonical opportunity id survives the update.
	updated, err := s.Opportunity(ctx, backend.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Title != "Senior Backend Engineer" || updated.WorkPattern != "remote" {
		t.Fatalf("updated: %+v", updated)
	}
	if out.Saved[3].RecordID != backend.ID || out.Saved[3].Revision != 2 {
		t.Fatalf("saved update: %+v", out.Saved[3])
	}
	page, err := s.ListOpportunities(ctx, store.OpportunityListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("opportunity views: %d", len(page.Items))
	}
}

func TestSaveUnassessedLabeling(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	bodyA := "Acme is hiring a backend engineer."
	bodyB := "Acme is hiring a frontend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{
		"cap-a": {body: bodyA}, "cap-b": {body: bodyB},
	}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	seedCaptureRow(t, s, "cap-b", caps.bodies["cap-b"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	asm := seedAssessment(t, s, seedAssessmentOpts{id: "asm1", jevID: "jev-1",
		roundID: round.ID, attemptID: "seed-attempt-1", profile: 1, rubric: "rubric-v1",
		status: "succeeded", evidenceRef: "cap-a", spanEnd: int64(len(bodyA))})
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})

	out, err := h.Save(ctx, researchcontract.SaveBatch{
		IdempotencyKey: "label-1", RunID: round.ID, Generation: 1,
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": company.ID, "title": "Backend Engineer",
					"sourceUrl": "https://acme.example.com/jobs/1", "originalText": bodyA},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", bodyA)},
				AssessmentIDs: []string{asm},
			},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": company.ID, "title": "Frontend Engineer",
					"sourceUrl": "https://acme.example.com/jobs/2", "originalText": bodyB},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-b", bodyB)},
			},
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeOK || len(out.Saved) != 2 {
		t.Fatalf("outcome: %+v", out)
	}
	assessed, err := s.Opportunity(ctx, out.Saved[0].RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.Stage != store.DiscoveredStage {
		t.Fatalf("assessed stage: %q", assessed.Stage)
	}
	unassessed, err := s.Opportunity(ctx, out.Saved[1].RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if unassessed.Stage != store.UnassessedStage {
		t.Fatalf("unassessed stage: %q", unassessed.Stage)
	}
}

func TestSaveAuthorityMatrix(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, limits store.RoundAllowance) (*store.Store, store.Round, *fakeCaptures, string) {
		t.Helper()
		s := openSaveStore(t)
		round := commissionSaveRound(t, s, limits)
		body := "Acme is hiring."
		caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
		seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
		return s, round, caps, body
	}
	companyBatch := func(round store.Round, companyID, body string) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: "auth-1", RunID: round.ID, Generation: round.Generation,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateCompany,
				Fields:        map[string]string{"name": "Acme"},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
			},
		}
	}

	t.Run("stale generation via real in-txn helper", func(t *testing.T) {
		s, round, caps, body := setup(t, generousLimits())
		// The pre-check double passes; the real T12 helper must still fence.
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		batch := companyBatch(round, "", body)
		batch.Generation = 999
		_, err := h.Save(ctx, batch)
		cerr := contractErr(t, err)
		if cerr.Code != researchcontract.OutcomeStale {
			t.Fatalf("code: %+v", cerr)
		}
	})

	t.Run("stopped round", func(t *testing.T) {
		s, round, caps, body := setup(t, generousLimits())
		stopped, _, err := s.StopRound(ctx, saveOwner, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		batch := companyBatch(stopped, "", body)
		_, err = h.Save(ctx, batch)
		cerr := contractErr(t, err)
		if cerr.Code != researchcontract.OutcomeStopped {
			t.Fatalf("code: %+v", cerr)
		}
	})

	t.Run("budget exhausted", func(t *testing.T) {
		s, round, caps, body := setup(t, store.RoundAllowance{Requests: 1, Items: 1, Tools: 1})
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		if _, err := h.Save(ctx, companyBatch(round, "", body)); err != nil {
			t.Fatalf("first: %v", err)
		}
		second := companyBatch(round, "", body)
		second.IdempotencyKey = "auth-2"
		_, err := h.Save(ctx, second)
		cerr := contractErr(t, err)
		if cerr.Code != researchcontract.OutcomeBudgetExhausted {
			t.Fatalf("code: %+v", cerr)
		}
	})

	t.Run("forbidden agent", func(t *testing.T) {
		s, round, caps, body := setup(t, generousLimits())
		outsider := store.Actor{Kind: "agent", ID: "agent-2"}
		h := saveHandler(s, &fakeAuthority{}, outsider, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		_, err := h.Save(ctx, companyBatch(round, "", body))
		cerr := contractErr(t, err)
		if cerr.Code != researchcontract.OutcomeForbidden {
			t.Fatalf("code: %+v", cerr)
		}
	})

	t.Run("unknown run", func(t *testing.T) {
		s, round, caps, body := setup(t, generousLimits())
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		batch := companyBatch(round, "", body)
		batch.RunID = "no-such-run"
		_, err := h.Save(ctx, batch)
		cerr := contractErr(t, err)
		if cerr.Code != researchcontract.OutcomeNotFound {
			t.Fatalf("code: %+v", cerr)
		}
	})

	t.Run("real authority pre-check fails before any write", func(t *testing.T) {
		s, round, caps, body := setup(t, generousLimits())
		h := saveHandler(s, mustRealAuthority(t, s, saveAgent), saveAgent, caps,
			&fakeBriefs{profile: 1, rubric: "rubric-v1"})
		batch := companyBatch(round, "", body)
		batch.Generation = 999
		_, err := h.Save(ctx, batch)
		cerr := contractErr(t, err)
		if cerr.Code != researchcontract.OutcomeStale {
			t.Fatalf("code: %+v", cerr)
		}
		if got := countRows(t, s, `SELECT count(*) FROM round_attempts`); got != 0 {
			t.Fatalf("attempts: %d", got)
		}
	})
}

func TestSaveSnippetBlocksCompleteClaim(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	snippet := "Acme backend engineer..."
	full := "Acme is hiring a backend engineer in Berlin. Apply now."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{
		"cap-snip": {body: snippet, snippet: true, provenance: researchcontract.ProvenanceSearchResult},
		"cap-full": {body: full},
	}}
	seedCaptureRow(t, s, "cap-snip", caps.bodies["cap-snip"])
	seedCaptureRow(t, s, "cap-full", caps.bodies["cap-full"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func(key, capID, body string, fields map[string]string) researchcontract.SaveBatch {
		fields["companyId"] = company.ID
		return researchcontract.SaveBatch{
			IdempotencyKey: key, RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
				Fields:        fields,
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink(capID, body)}},
			},
		}
	}
	out, err := h.Save(ctx, makeBatch("snip-1", "cap-snip", snippet, map[string]string{
		"title": "Backend Engineer", "sourceUrl": "https://acme.example.com/jobs/1",
		"originalText": snippet, "vacancyComplete": "true",
	}))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeCaptureIncomplete || len(out.Items) != 1 {
		t.Fatalf("outcome: %+v", out)
	}
	// The same snippet without a completeness claim saves as a partial find.
	out, err = h.Save(ctx, makeBatch("snip-2", "cap-snip", snippet, map[string]string{
		"title": "Backend Engineer", "sourceUrl": "https://acme.example.com/jobs/1",
		"originalText": snippet,
	}))
	if err != nil {
		t.Fatalf("partial: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("partial outcome: %+v", out)
	}
}

func TestSaveQuoteBinding(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring a backend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func(key string, link researchcontract.EvidenceLink) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: key, RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": company.ID, "title": "Backend Engineer",
					"originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{link}},
			},
		}
	}
	t.Run("unsupported quote", func(t *testing.T) {
		link := fullLink("cap-a", body)
		link.ExcerptSHA256 = store.FixtureSHA256("something-else")
		out, err := h.Save(ctx, makeBatch("quote-1", link))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeInvalid || len(out.Items) != 1 {
			t.Fatalf("outcome: %+v", out)
		}
	})
	t.Run("span out of range", func(t *testing.T) {
		link := researchcontract.EvidenceLink{CaptureID: "cap-a", SpanStart: 0,
			SpanEnd: int64(len(body) + 10), ExcerptSHA256: store.FixtureSHA256("x")}
		out, err := h.Save(ctx, makeBatch("quote-2", link))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeInvalid || len(out.Items) != 1 {
			t.Fatalf("outcome: %+v", out)
		}
	})
	t.Run("unknown capture", func(t *testing.T) {
		link := researchcontract.EvidenceLink{CaptureID: "cap-ghost", SpanStart: 0,
			SpanEnd: 4, ExcerptSHA256: store.FixtureSHA256("x")}
		out, err := h.Save(ctx, makeBatch("quote-3", link))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeInvalid || len(out.Items) != 1 {
			t.Fatalf("outcome: %+v", out)
		}
		if got := countRows(t, s, `SELECT count(*) FROM round_attempts`); got != 0 {
			t.Fatalf("attempts: %d", got)
		}
	})
}

func TestSaveAssessmentBinding(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*store.Store, store.Round, *fakeCaptures, string, string, string) {
		t.Helper()
		s := openSaveStore(t)
		round := commissionSaveRound(t, s, generousLimits())
		bodyA := "Acme backend posting."
		bodyB := "Acme frontend posting."
		caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{
			"cap-a": {body: bodyA}, "cap-b": {body: bodyB},
		}}
		seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
		seedCaptureRow(t, s, "cap-b", caps.bodies["cap-b"])
		company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
		if err != nil {
			t.Fatal(err)
		}
		return s, round, caps, bodyA, bodyB, company.ID
	}
	makeBatch := func(round store.Round, companyID, body, capID, asm string) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: "asm-bind", RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": companyID, "title": "Backend Engineer",
					"originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink(capID, body)},
				AssessmentIDs: []string{asm}},
			},
		}
	}

	t.Run("stale brief", func(t *testing.T) {
		s, round, caps, bodyA, _, companyID := setup(t)
		asm := seedAssessment(t, s, seedAssessmentOpts{id: "asm-old", jevID: "jev-old",
			roundID: round.ID, attemptID: "seed-old", profile: 1, rubric: "rubric-v1",
			status: "succeeded", evidenceRef: "cap-a", spanEnd: int64(len(bodyA))})
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 2, rubric: "rubric-v1"})
		out, err := h.Save(ctx, makeBatch(round, companyID, bodyA, "cap-a", asm))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeStale || len(out.Items) != 1 {
			t.Fatalf("outcome: %+v", out)
		}
	})

	t.Run("failed assessment unusable", func(t *testing.T) {
		s, round, caps, bodyA, _, companyID := setup(t)
		asm := seedAssessment(t, s, seedAssessmentOpts{id: "asm-failed", jevID: "jev-failed",
			roundID: round.ID, attemptID: "seed-failed", profile: 1, rubric: "rubric-v1",
			status: "failed", evidenceRef: "cap-a", spanEnd: int64(len(bodyA))})
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		out, err := h.Save(ctx, makeBatch(round, companyID, bodyA, "cap-a", asm))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeInvalid || len(out.Items) != 1 {
			t.Fatalf("outcome: %+v", out)
		}
	})

	t.Run("assessment cannot rebind to different evidence", func(t *testing.T) {
		s, round, caps, bodyA, bodyB, companyID := setup(t)
		asm := seedAssessment(t, s, seedAssessmentOpts{id: "asm-a", jevID: "jev-a",
			roundID: round.ID, attemptID: "seed-a", profile: 1, rubric: "rubric-v1",
			status: "succeeded", evidenceRef: "cap-a", spanEnd: int64(len(bodyA))})
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		out, err := h.Save(ctx, makeBatch(round, companyID, bodyB, "cap-b", asm))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeRevisionConflict || len(out.Items) != 1 {
			t.Fatalf("outcome: %+v", out)
		}
	})
}

func TestSaveReusedIdentifier(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	bodyA := "Acme is hiring."
	bodyB := "Acme Two is hiring."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{
		"cap-a": {body: bodyA}, "cap-b": {body: bodyB},
	}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	seedCaptureRow(t, s, "cap-b", caps.bodies["cap-b"])
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func(key, name, body, capID string, reuse bool) researchcontract.SaveBatch {
		fields := map[string]string{"name": name, "website": "https://acme.example.com"}
		if reuse {
			fields["reusedIdentifier"] = "true"
		}
		return researchcontract.SaveBatch{
			IdempotencyKey: key, RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateCompany,
				Fields:        fields,
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink(capID, body)}},
			},
		}
	}
	first, err := h.Save(ctx, makeBatch("reuse-1", "Acme", bodyA, "cap-a", false))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first: %+v", first)
	}
	// Same domain without the reuse signal is ambiguous, not a duplicate.
	out, err := h.Save(ctx, makeBatch("reuse-2", "Acme Two", bodyB, "cap-b", false))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeIdentityAmbiguous || len(out.Items) != 1 {
		t.Fatalf("collision outcome: %+v", out)
	}
	if out.Items[0].CurrentRevision == nil || *out.Items[0].CurrentRevision != 1 {
		t.Fatalf("established revision: %+v", out.Items[0])
	}
	// With the signal the identifier visibly moves to the new record.
	out, err = h.Save(ctx, makeBatch("reuse-3", "Acme Two", bodyB, "cap-b", true))
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeOK || len(out.Saved) != 1 {
		t.Fatalf("supersede outcome: %+v", out)
	}
	if got := countRows(t, s, `SELECT count(*) FROM entity_identity_keys
  WHERE namespace='employer_domain' AND status='superseded'`); got != 1 {
		t.Fatalf("superseded: %d", got)
	}
	var current, supersedes string
	if err := s.Read(ctx, func(r store.Reader) error {
		keys, err := store.ListIdentityKeysByValue(ctx, r, "employer_domain", "acme.example.com")
		if err != nil {
			return err
		}
		for _, k := range keys {
			if k.Status == "current" {
				current, supersedes = k.CompanyID, k.SupersedesID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if current != out.Saved[0].RecordID || supersedes == "" {
		t.Fatalf("current chain: %q supersedes %q", current, supersedes)
	}
	if got := countRows(t, s, `SELECT count(*) FROM record_sightings
  WHERE company_id=? AND sighting_kind='reused_identifier'`, out.Saved[0].RecordID); got != 1 {
		t.Fatalf("reused sightings: %d", got)
	}
}

func TestSaveSameDecisionUpdate(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring a backend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := s.CreateOpportunity(ctx, saveOwner, store.OpportunityInput{
		CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
		Stage: "new", OriginalText: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.CreateOpportunity(ctx, saveOwner, store.OpportunityInput{
		CompanyID: company.ID, Title: "Designer", Kind: "employment",
		Stage: "new", OriginalText: "design work",
	})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func(key string, cands ...researchcontract.CandidateIdentity) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: key, RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{
				Op: researchcontract.SaveUpdateOpportunity, RecordID: target.ID, ExpectedRevision: 1,
				Fields:           map[string]string{"locationText": "Berlin"},
				EvidenceLinks:    []researchcontract.EvidenceLink{fullLink("cap-a", body)},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "same", Candidates: cands},
			}},
		}
	}
	// A same-decision whose target left the candidate set is incoherent.
	out, err := h.Save(ctx, makeBatch("same-1",
		researchcontract.CandidateIdentity{CandidateID: other.ID, Kind: "opportunity", Revision: 1}))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeInvalid || len(out.Items) != 1 {
		t.Fatalf("outcome: %+v", out)
	}
	// The coherent same-decision updates its subject and records the call.
	out, err = h.Save(ctx, makeBatch("same-2",
		researchcontract.CandidateIdentity{CandidateID: target.ID, Kind: "opportunity", Revision: 1},
		researchcontract.CandidateIdentity{CandidateID: other.ID, Kind: "opportunity", Revision: 1}))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeOK || len(out.Saved) != 1 || out.Saved[0].Revision != 2 {
		t.Fatalf("outcome: %+v", out)
	}
	var subject, subjectRev string
	var rev int64
	if err := s.Read(ctx, func(r store.Reader) error {
		rows, err := store.ListIdentityDecisionsBySubject(ctx, r, "vacancy", "same", 10)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("decisions: %d", len(rows))
		}
		subject, rev = rows[0].SubjectOpportunityID, rows[0].SubjectRevision
		subjectRev = rows[0].Decision
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if subject != target.ID || rev != 1 || subjectRev != "same" {
		t.Fatalf("subject: %q rev %d decision %q", subject, rev, subjectRev)
	}
}

func TestSaveUnresolvedBlocked(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Someone is hiring."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})

	out, err := h.Save(ctx, researchcontract.SaveBatch{
		IdempotencyKey: "unresolved-1", RunID: round.ID, Generation: 1,
		Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": company.ID, "title": "Maybe",
				"originalText": body},
			EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)},
			IdentityDecision: &researchcontract.IdentityDecision{Decision: "unresolved",
				Candidates: []researchcontract.CandidateIdentity{
					{CandidateID: company.ID, Kind: "company", Revision: 1}}},
		}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeIdentityAmbiguous || len(out.Items) != 1 {
		t.Fatalf("outcome: %+v", out)
	}
	if got := countRows(t, s, `SELECT count(*) FROM opportunities`); got != 0 {
		t.Fatalf("opportunities: %d", got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM identity_decisions`); got != 0 {
		t.Fatalf("decisions: %d", got)
	}
}

func TestSaveUpdateRevisionConflict(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	body := "Acme is hiring a backend engineer."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := s.CreateOpportunity(ctx, saveOwner, store.OpportunityInput{
		CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
		Stage: "new", OriginalText: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	archived, _, err := s.CreateOpportunity(ctx, saveOwner, store.OpportunityInput{
		CompanyID: company.ID, Title: "Old role", Kind: "employment",
		Stage: "new", OriginalText: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PatchOpportunity(ctx, saveOwner, target.ID,
		store.OpportunityPatch{ExpectedRevision: 1, Notes: strptr("owner note")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ArchiveOpportunity(ctx, saveOwner, archived.ID, 1); err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
	makeBatch := func(key, recordID string, rev int64) researchcontract.SaveBatch {
		return researchcontract.SaveBatch{
			IdempotencyKey: key, RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{
				Op: researchcontract.SaveUpdateOpportunity, RecordID: recordID, ExpectedRevision: rev,
				Fields:        map[string]string{"locationText": "Berlin"},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)},
			}},
		}
	}
	out, err := h.Save(ctx, makeBatch("rev-1", target.ID, 1))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeRevisionConflict || len(out.Items) != 1 {
		t.Fatalf("outcome: %+v", out)
	}
	if out.Items[0].CurrentRevision == nil || *out.Items[0].CurrentRevision != 2 {
		t.Fatalf("current: %+v", out.Items[0])
	}
	out, err = h.Save(ctx, makeBatch("rev-2", archived.ID, 2))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.Outcome != researchcontract.OutcomeRevisionConflict || len(out.Items) != 1 {
		t.Fatalf("archived outcome: %+v", out)
	}
}

func TestSaveSightingKinds(t *testing.T) {
	ctx := context.Background()
	s := openSaveStore(t)
	round := commissionSaveRound(t, s, generousLimits())
	bodyA := "Acme backend posting, version one."
	bodyB := "Acme backend posting, version two."
	caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{
		"cap-a": {body: bodyA}, "cap-b": {body: bodyB},
	}}
	seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
	seedCaptureRow(t, s, "cap-b", caps.bodies["cap-b"])
	company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})

	first, err := h.Save(ctx, researchcontract.SaveBatch{
		IdempotencyKey: "sight-1", RunID: round.ID, Generation: 1,
		Items: []researchcontract.SaveItem{{Op: researchcontract.SaveCreateOpportunity,
			Fields: map[string]string{"companyId": company.ID, "title": "Backend Engineer",
				"sourceUrl": "https://acme.example.com/jobs/1", "originalText": bodyA},
			EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", bodyA)}},
		},
	})
	if err != nil || first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("create: %+v %v", first, err)
	}
	recordID := first.Saved[0].RecordID
	update := func(key, body, capID string, rev int64) (researchcontract.SaveOutput, error) {
		return h.Save(ctx, researchcontract.SaveBatch{
			IdempotencyKey: key, RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{{
				Op: researchcontract.SaveUpdateOpportunity, RecordID: recordID, ExpectedRevision: rev,
				Fields:        map[string]string{"originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink(capID, body)},
			}},
		})
	}
	if out, err := update("sight-2", bodyB, "cap-b", 1); err != nil || out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("changed: %+v %v", out, err)
	}
	if out, err := update("sight-3", bodyA, "cap-a", 2); err != nil || out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("unchanged: %+v %v", out, err)
	}
	var kinds []string
	if err := s.Read(ctx, func(r store.Reader) error {
		sightings, err := store.ListRecordSightingsByOpportunity(ctx, r, recordID)
		if err != nil {
			return err
		}
		for _, sighting := range sightings {
			kinds = append(kinds, sighting.SightingKind)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 3 || kinds[0] != "first" || kinds[1] != "changed" || kinds[2] != "unchanged" {
		t.Fatalf("sighting kinds: %v", kinds)
	}
}

func TestSaveEdgeValidation(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*store.Store, store.Round, *fakeCaptures, string, string) {
		t.Helper()
		s := openSaveStore(t)
		round := commissionSaveRound(t, s, generousLimits())
		body := "Acme is hiring."
		caps := &fakeCaptures{bodies: map[string]fakeCaptureBody{"cap-a": {body: body}}}
		seedCaptureRow(t, s, "cap-a", caps.bodies["cap-a"])
		company, _, err := s.CreateCompany(ctx, saveOwner, store.CompanyInput{Name: "Acme"})
		if err != nil {
			t.Fatal(err)
		}
		return s, round, caps, body, company.ID
	}

	t.Run("empty batch", func(t *testing.T) {
		s, round, caps, _, _ := setup(t)
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		_, err := h.Save(ctx, researchcontract.SaveBatch{IdempotencyKey: "k", RunID: round.ID, Generation: 1})
		cerr := contractErr(t, err)
		if cerr.Code != researchcontract.OutcomeInvalid {
			t.Fatalf("code: %+v", cerr)
		}
	})

	t.Run("item problems are outputs not errors", func(t *testing.T) {
		s, round, caps, body, companyID := setup(t)
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		out, err := h.Save(ctx, researchcontract.SaveBatch{
			IdempotencyKey: "edge-1", RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{
				{Op: researchcontract.SaveOp("explode"), RecordID: "x",
					EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
				{Op: researchcontract.SaveCreateOpportunity,
					Fields: map[string]string{"companyId": companyID, "title": "T",
						"originalText": body, "bogus": "1"},
					EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
				{Op: researchcontract.SaveUpdateCompany, ExpectedRevision: 1,
					Fields:        map[string]string{"name": "Acme"},
					EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
			},
		})
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeInvalid || len(out.Items) != 3 {
			t.Fatalf("outcome: %+v", out)
		}
		for i, item := range out.Items {
			if item.Index != i || item.Code != researchcontract.OutcomeInvalid {
				t.Fatalf("item %d: %+v", i, item)
			}
		}
	})

	t.Run("intra-batch key collision", func(t *testing.T) {
		s, round, caps, body, companyID := setup(t)
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		item := func(title string) researchcontract.SaveItem {
			return researchcontract.SaveItem{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": companyID, "title": title,
					"sourceUrl": "https://acme.example.com/jobs/1", "originalText": body},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}}
		}
		out, err := h.Save(ctx, researchcontract.SaveBatch{
			IdempotencyKey: "edge-2", RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{item("One"), item("Two")},
		})
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeIdentityAmbiguous || len(out.Items) != 1 || out.Items[0].Index != 1 {
			t.Fatalf("outcome: %+v", out)
		}
	})

	t.Run("intra-batch double update", func(t *testing.T) {
		s, round, caps, body, companyID := setup(t)
		target, _, err := s.CreateOpportunity(ctx, saveOwner, store.OpportunityInput{
			CompanyID: companyID, Title: "Role", Kind: "employment", Stage: "new", OriginalText: body,
		})
		if err != nil {
			t.Fatal(err)
		}
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		item := func() researchcontract.SaveItem {
			return researchcontract.SaveItem{Op: researchcontract.SaveUpdateOpportunity,
				RecordID: target.ID, ExpectedRevision: 1,
				Fields:        map[string]string{"locationText": "Berlin"},
				EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}}
		}
		out, err := h.Save(ctx, researchcontract.SaveBatch{
			IdempotencyKey: "edge-3", RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{item(), item()},
		})
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeRevisionConflict || len(out.Items) != 1 || out.Items[0].Index != 1 {
			t.Fatalf("outcome: %+v", out)
		}
	})

	t.Run("forward batch ref", func(t *testing.T) {
		s, round, caps, body, _ := setup(t)
		h := saveHandler(s, &fakeAuthority{}, saveAgent, caps, &fakeBriefs{profile: 1, rubric: "rubric-v1"})
		out, err := h.Save(ctx, researchcontract.SaveBatch{
			IdempotencyKey: "edge-4", RunID: round.ID, Generation: 1,
			Items: []researchcontract.SaveItem{
				{Op: researchcontract.SaveCreateOpportunity,
					Fields: map[string]string{"companyId": "batch:1", "title": "T",
						"originalText": body},
					EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
				{Op: researchcontract.SaveCreateCompany,
					Fields:        map[string]string{"name": "Late"},
					EvidenceLinks: []researchcontract.EvidenceLink{fullLink("cap-a", body)}},
			},
		})
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if out.Outcome != researchcontract.OutcomeInvalid || len(out.Items) != 1 || out.Items[0].Index != 0 {
			t.Fatalf("outcome: %+v", out)
		}
	})
}
