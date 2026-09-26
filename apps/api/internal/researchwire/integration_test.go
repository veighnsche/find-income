package researchwire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// gateTransport holds discovery sessions until the test releases the gate,
// so commissioned runs stay active deterministically and no test ever
// spawns the live CLI. started closes when a session enters the transport.
// saves lists refs the session emits as validated saves after release;
// after, when set, runs after emission instead of finishing (stop tests
// hold the session open with it). release is idempotent so tests and the
// harness cleanup can both call it.
type gateTransport struct {
	started  chan struct{}
	proceed  chan struct{}
	emitted  chan struct{}
	saves       []string
	after       func(context.Context)
	releaseOnce sync.Once
}

func (g *gateTransport) Release() {
	g.releaseOnce.Do(func() { close(g.proceed) })
}

func (g *gateTransport) Run(ctx context.Context, _ musecode.SessionSpec, _ musecode.SessionInput, _ musecode.Cursor, sink musecode.EventSink) error {
	select {
	case <-g.started:
	default:
		close(g.started)
	}
	select {
	case <-g.proceed:
	case <-ctx.Done():
		return ctx.Err()
	}
	sink.Emit(musecode.Event{Kind: musecode.EventModelStep})
	for _, ref := range g.saves {
		sink.Emit(musecode.Event{Kind: musecode.EventToolCall, Tool: "public_save_vacancy"})
		sink.Emit(musecode.Event{Kind: musecode.EventSaved, SaveRef: ref})
	}
	if g.emitted != nil {
		select {
		case <-g.emitted:
		default:
			close(g.emitted)
		}
	}
	if g.after != nil {
		g.after(ctx)
		return ctx.Err()
	}
	sink.Emit(musecode.Event{Kind: musecode.EventFinished})
	return nil
}

func fixtureFacts() musecode.Facts {
	return musecode.Facts{CLIPath: "/fixture/muse", CLIReportVersion: "1.4.0",
		EffectiveModel: "fixture-model", SubscriptionLaneProved: true,
		SessionProtocolProved: true, WorkspaceIsolatedProved: true}
}

// scriptedProvider is a zero-spend jevassess.Provider: verdicts are scripted
// per question id, pinning the binding machinery (capture refs, brief match,
// exchange log, sink), not provider evidence.
type scriptedProvider struct {
	model    string
	verdicts map[string]string
	calls    int
}

func (p *scriptedProvider) EncodedRequest(r jev.Request) ([]byte, error) {
	return json.Marshal(r.State)
}

func (p *scriptedProvider) RequestedModel() string { return p.model }

func (p *scriptedProvider) EvaluateOnceCaptured(_ context.Context, r jev.Request) (jev.Result, jev.CapturedExchange, error) {
	p.calls++
	encoded, _ := json.Marshal(r.State)
	out := jev.Result{
		RequestedModel: p.model, ReturnedModel: p.model,
		Answers:     map[string]jev.Answer{},
		Usage:       jev.Usage{InputTokens: 10, OutputTokens: 5},
		RawResponse: json.RawMessage(`{"model":"fixture"}`),
	}
	for id := range r.Questions {
		choice, ok := p.verdicts[id]
		if !ok {
			choice = "abstain"
		}
		out.Answers[id] = jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{
			Choice: choice, Probabilities: map[string]float64{choice: 1}, Confidence: 0.9}}
	}
	return out, jev.CapturedExchange{RequestBytes: encoded, ResponseBytes: []byte("{}"),
		HTTPStatus: 200, ReturnedModel: p.model}, nil
}

var _ jevassess.Provider = (*scriptedProvider)(nil)

// fixture bodies: role 1 + its cross-post share REQ-NW-101; role 2 is distinct.
const (
	roleBody1 = "Northwind Traders seeks a Senior Backend Engineer in Berlin (REQ-NW-101). " +
		"Full-time, hybrid, EUR 6000-7500 monthly. Go services, Postgres, on-call rotation. " +
		"Apply with CV and availability."
	roleBody1Cross = "Senior Backend Engineer (m/f/d) -- Northwind Traders, Berlin, ref REQ-NW-101. " +
		"Full time hybrid position, EUR 6000-7500 per month. Go microservices and Postgres."
	roleBody2 = "Contoso Support seeks a Support Engineer in Amsterdam. Full-time remote, " +
		"EUR 4000-5000 monthly. Ticket queue, incident response, customer calls."
)

func fixtureBoard(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/roles/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body><h1>Senior Backend Engineer</h1><p>"+roleBody1+"</p></body></html>")
	})
	mux.HandleFunc("/roles/1-cross", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body><h1>Senior Backend Engineer (m/f/d)</h1><p>"+roleBody1Cross+"</p></body></html>")
	})
	mux.HandleFunc("/api/roles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"roles":[{"title":"Support Engineer","employer":"Contoso Support","text":`+strconvQuote(roleBody2)+`}]}`)
	})
	return httptest.NewServer(mux)
}

func strconvQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func excerptSHA(body string, start, end int) string {
	sum := sha256.Sum256([]byte(body[start:end]))
	return hex.EncodeToString(sum[:])
}

type harness struct {
	ctx      context.Context
	db       *store.Store
	stack    *Stack
	owner    store.Actor
	agent    store.Actor
	board    *httptest.Server
	provider *scriptedProvider
	gate     *gateTransport
	runID    string
	gen      int64
	profile  int64
	rubric   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	provider := &scriptedProvider{model: "fixture-jev-1", verdicts: map[string]string{}}
	gate := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{})}
	t.Cleanup(func() { gate.Release() })
	facts := fixtureFacts()
	cfg := Config{
		ArtifactRoot:   filepath.Join(dir, "research-artifacts"),
		ScratchRoot:    filepath.Join(dir, "scratch"),
		AgentID:        DefaultAgentID,
		PermitLoopback: true, // test-only: controlled fixture servers
		JevProvider:    provider,
		MuseTransport:  gate,
		MuseFacts:      &facts,
	}
	if shell := isolatedShellPath(); shell != "" {
		cfg.ChromePath = shell // lazy-verified; construction launches nothing
	}
	stack, err := Wire(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{ctx: ctx, db: db, stack: stack,
		owner:    OwnerActor(),
		agent:    store.Actor{Kind: "agent", ID: DefaultAgentID},
		board:    fixtureBoard(t),
		provider: provider,
		gate:     gate,
	}
}

func (h *harness) commission(t *testing.T) {
	t.Helper()
	out, err := h.stack.Research.CommissionResearch(h.ctx, httpapi.CommissionResearchInput{
		Actor: h.owner, BriefText: "Find backend roles in Berlin.", IdempotencyKey: "t23-run-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || out.View.RunId == "" {
		t.Fatalf("commission: %+v", out)
	}
	h.runID = out.View.RunId
	h.profile = int64(out.View.BriefVersion.ProfileVersion)
	h.rubric = out.View.BriefVersion.RubricVersion
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	h.gen = round.Generation
}

// commissionSupervisor commissions a codex-scoped run directly through the
// run supervisor for toolchain tests (saver, bridge, browse, stop/resume
// routing): those pin the record/dispatch/control machinery, not discovery
// commissioning, and need the codex turn/record scope plus delegation.
func (h *harness) commissionSupervisor(t *testing.T) {
	t.Helper()
	brief, err := codexservice.CurrentOwnerBrief(h.ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.stack.Supervisor.Commission(h.ctx, rounds.CommissionInput{
		Actor: h.owner, BriefText: "Find backend roles in Berlin.", AgentID: h.agent.ID,
		ProfileVersion: brief.ProfileVersion, RubricVersion: brief.RubricVersion,
		RubricSource: brief.Source, IdempotencyKey: "t23-run-1"})
	if err != nil {
		t.Fatal(err)
	}
	h.runID = out.RunID
	h.gen = out.Generation
	h.profile = out.ProfileVersion
	h.rubric = out.RubricVersion
}

func (h *harness) fetch(t *testing.T, key, url string) (obsID, capID string) {
	t.Helper()
	out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: h.gen, Kind: researchcontract.ExecuteFetch,
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
	if out.CaptureID == "" || out.ObservationID == "" {
		t.Fatalf("dispatch %s missing ids: %+v", key, out)
	}
	// Citations pin the receipt's canonical content id, not the retrieval row.
	if out.Receipt.CaptureID == "" {
		t.Fatalf("dispatch %s receipt missing canonical capture: %+v", key, out.Receipt)
	}
	return out.ObservationID, out.Receipt.CaptureID
}

func captureBytes(t *testing.T, h *harness, capID string) string {
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

func (h *harness) assess(t *testing.T, key, questionID, verdict string, sameRefs, distinctRefs []researchcontract.EvidenceRef) string {
	t.Helper()
	h.provider.verdicts[questionID] = verdict
	refs := append(append([]researchcontract.EvidenceRef{}, sameRefs...), distinctRefs...)
	out, err := h.stack.Assessor.Assess(h.ctx, researchcontract.AssessInput{
		Purpose: "identity_match",
		Questions: []researchcontract.AssessQuestion{{
			ID: questionID, Text: "Do these captures describe one opening?",
			Alternatives: []researchcontract.AssessAlternative{
				{ID: "same", Label: "one opening", EvidenceRefs: sameRefs},
				{ID: "distinct", Label: "two openings", EvidenceRefs: distinctRefs},
			},
			AbstainAllowed: true,
		}},
		ProfileVersion: h.profile, RubricVersion: h.rubric,
		SourceRefs: refs, IdempotencyKey: key, RunID: h.runID, Generation: h.gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Results[0].AnswerID != verdict {
		t.Fatalf("assess %s: %+v", key, out.Results)
	}
	return out.ID
}

func spanOf(t *testing.T, body, needle string) (int64, int64) {
	t.Helper()
	at := strings.Index(body, needle)
	if at < 0 {
		t.Fatalf("needle %q missing", needle)
	}
	return int64(at), int64(at + len(needle))
}

func TestIntegratedControlledSourceRun(t *testing.T) {
	h := newHarness(t)
	h.commissionSupervisor(t)
	saver, err := h.stack.NewSaverFor(h.agent)
	if err != nil {
		t.Fatal(err)
	}

	// Two role pages through the real executor: capture-before-claims.
	_, cap1 := h.fetch(t, "fetch-role-1", h.board.URL+"/roles/1")
	_, cap2 := h.fetch(t, "fetch-role-1-cross", h.board.URL+"/roles/1-cross")
	body1 := captureBytes(t, h, cap1)
	body2 := captureBytes(t, h, cap2)
	if !strings.Contains(body1, "REQ-NW-101") || !strings.Contains(body2, "REQ-NW-101") {
		t.Fatal("captured bytes do not carry the controlled postings")
	}
	s1s, s1e := spanOf(t, body1, "REQ-NW-101")
	s2s, s2e := spanOf(t, body2, "REQ-NW-101")
	t1s, t1e := spanOf(t, body1, "Senior Backend Engineer")
	t2s, t2e := spanOf(t, body2, "Senior Backend Engineer (m/f/d)")
	sameRefs := []researchcontract.EvidenceRef{
		{CaptureID: cap1, SpanStart: s1s, SpanEnd: s1e},
		{CaptureID: cap2, SpanStart: s2s, SpanEnd: s2e},
	}
	distinctRefs := []researchcontract.EvidenceRef{
		{CaptureID: cap1, SpanStart: t1s, SpanEnd: t1e},
		{CaptureID: cap2, SpanStart: t2s, SpanEnd: t2e},
	}
	allRefs := append(append([]researchcontract.EvidenceRef{}, sameRefs...), distinctRefs...)

	// Identity round: the cross-post pair is one opening (scripted verdict;
	// the binding machinery — spans, brief, exchange log, sink — is real).
	assessID := h.assess(t, "assess-cross-1", "q-cross-1", "same", sameRefs, distinctRefs)
	match, err := h.stack.Matcher.Match(h.ctx, researchcontract.MatchInput{
		Attributes: researchcontract.MatchAttributes{
			Employer: "Northwind Traders", Title: "Senior Backend Engineer",
			URL: h.board.URL + "/roles/1", RequisitionID: "REQ-NW-101", Location: "Berlin",
		},
		EvidenceRefs: allRefs, RunID: h.runID, Generation: h.gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first match: %+v", match)
	}

	// Save role 1 citing every assessment span.
	link := func(ref researchcontract.EvidenceRef) researchcontract.EvidenceLink {
		body := body1
		if ref.CaptureID == cap2 {
			body = body2
		}
		return researchcontract.EvidenceLink{CaptureID: ref.CaptureID, SpanStart: ref.SpanStart, SpanEnd: ref.SpanEnd,
			ExcerptSHA256: excerptSHA(body, int(ref.SpanStart), int(ref.SpanEnd))}
	}
	links := func() []researchcontract.EvidenceLink {
		out := make([]researchcontract.EvidenceLink, 0, len(allRefs))
		for _, r := range allRefs {
			out = append(out, link(r))
		}
		return out
	}
	saved1, err := saver.Save(h.ctx, researchcontract.SaveBatch{
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveCreateCompany,
				Fields:        map[string]string{"name": "Northwind Traders"},
				EvidenceLinks: []researchcontract.EvidenceLink{link(sameRefs[0])}},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Senior Backend Engineer",
					"kind": "employment", "sourceUrl": h.board.URL + "/roles/1", "originalText": body1,
					"locationText": "Berlin", "requisitionId": "REQ-NW-101", "reqIssuer": "northwind",
					"vacancyComplete": "true"},
				EvidenceLinks:    links(),
				AssessmentIDs:    []string{assessID},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "new"}},
		},
		IdempotencyKey: "save-role-1", RunID: h.runID, Generation: h.gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved1.Outcome != researchcontract.OutcomeOK || len(saved1.Saved) != 2 {
		t.Fatalf("save role 1: %+v", saved1)
	}
	opp1 := saved1.Saved[1].RecordID
	rev1 := saved1.Saved[1].Revision
	company1 := saved1.Saved[0].RecordID

	// Script the matcher's per-candidate compare verdicts: the posting is the
	// saved opportunity, not the saved company.
	h.provider.verdicts["cmp-opportunity-"+opp1] = "same"
	h.provider.verdicts["cmp-company-"+company1] = "distinct"

	// Cross-post reuse: the second capture converges on the same record with
	// a second sighting — no duplicate role.
	match2, err := h.stack.Matcher.Match(h.ctx, researchcontract.MatchInput{
		Attributes: researchcontract.MatchAttributes{
			Employer: "Northwind Traders", Title: "Senior Backend Engineer (m/f/d)",
			URL: h.board.URL + "/roles/1-cross", RequisitionID: "REQ-NW-101", Location: "Berlin",
		},
		EvidenceRefs: allRefs, RunID: h.runID, Generation: h.gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(match2.ExactMatches) != 1 || match2.ExactMatches[0].RecordID != opp1 {
		t.Fatalf("cross-post match: %+v", match2)
	}
	savedCross, err := saver.Save(h.ctx, researchcontract.SaveBatch{
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveUpdateOpportunity, RecordID: opp1, ExpectedRevision: rev1,
				Fields: map[string]string{"title": "Senior Backend Engineer",
					"kind": "employment", "sourceUrl": h.board.URL + "/roles/1-cross", "originalText": body2,
					"locationText": "Berlin", "requisitionId": "REQ-NW-101", "reqIssuer": "northwind",
					"vacancyComplete": "true"},
				EvidenceLinks: links(),
				AssessmentIDs: []string{assessID},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "same",
					Candidates: []researchcontract.CandidateIdentity{{CandidateID: opp1, Kind: "opportunity", Revision: rev1}}}},
		},
		IdempotencyKey: "save-role-1-cross", RunID: h.runID, Generation: h.gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if savedCross.Outcome != researchcontract.OutcomeOK || savedCross.Saved[0].RecordID != opp1 {
		t.Fatalf("cross-post save: %+v", savedCross)
	}
	var sighted map[string]bool
	if err := h.db.Read(h.ctx, func(r store.Reader) error {
		rows, err := store.ListRecordSightingsByOpportunity(h.ctx, r, opp1)
		if err != nil {
			return err
		}
		sighted = map[string]bool{}
		for _, row := range rows {
			sighted[row.ContentSHA256] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// One sighting row per cited span; both captures pin the same record.
	if len(sighted) != 2 || !sighted[cap1] || !sighted[cap2] {
		t.Fatalf("cross-post sightings: %v", sighted)
	}

	// Distinct second role via read-only API POST: multiple records per run.
	apiOut, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: h.gen, Kind: researchcontract.ExecuteAPI,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationAPI, Backend: "generic-http",
			Method: "POST", URLOrQuery: h.board.URL + "/api/roles",
			Params: []researchcontract.Param{{Name: "Content-Type", Value: "application/json"}},
			Body:   `{"q":"support"}`,
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "api-roles-1",
	})
	if err != nil || apiOut.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("api dispatch: %+v %v", apiOut, err)
	}
	cap3 := apiOut.Receipt.CaptureID // canonical content id for citations
	if cap3 == "" {
		t.Fatalf("api receipt missing canonical capture: %+v", apiOut.Receipt)
	}
	body3 := captureBytes(t, h, cap3)
	s3s, s3e := spanOf(t, body3, "Support Engineer")
	a3s, a3e := spanOf(t, body3, "Amsterdam")
	ref3a := researchcontract.EvidenceRef{CaptureID: cap3, SpanStart: s3s, SpanEnd: s3e}
	ref3b := researchcontract.EvidenceRef{CaptureID: cap3, SpanStart: a3s, SpanEnd: a3e}
	assess2 := h.assess(t, "assess-distinct-1", "q-distinct-1", "distinct", []researchcontract.EvidenceRef{ref3a}, []researchcontract.EvidenceRef{ref3b})
	link3 := func(ref researchcontract.EvidenceRef) researchcontract.EvidenceLink {
		return researchcontract.EvidenceLink{CaptureID: ref.CaptureID, SpanStart: ref.SpanStart, SpanEnd: ref.SpanEnd,
			ExcerptSHA256: excerptSHA(body3, int(ref.SpanStart), int(ref.SpanEnd))}
	}
	saved2, err := saver.Save(h.ctx, researchcontract.SaveBatch{
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveCreateCompany,
				Fields:        map[string]string{"name": "Contoso Support"},
				EvidenceLinks: []researchcontract.EvidenceLink{link3(ref3a)}},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Support Engineer",
					"kind": "employment", "sourceUrl": h.board.URL + "/api/roles", "originalText": body3,
					"locationText": "Amsterdam", "vacancyComplete": "true"},
				EvidenceLinks:    []researchcontract.EvidenceLink{link3(ref3a), link3(ref3b)},
				AssessmentIDs:    []string{assess2},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "new"}},
		},
		IdempotencyKey: "save-role-2", RunID: h.runID, Generation: h.gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved2.Outcome != researchcontract.OutcomeOK || saved2.Saved[1].RecordID == opp1 {
		t.Fatalf("save role 2: %+v", saved2)
	}

	// Pivot machinery: a failed request persists as a distinguishable
	// negative, then a different request succeeds. (Scripted here; the
	// model-driven choice is the T30 canary.)
	negOut, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: h.gen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: h.board.URL + "/roles/missing",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "fetch-missing-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if negOut.Receipt.Status == researchcontract.ReceiptOK {
		t.Fatalf("missing page receipt: %+v", negOut.Receipt)
	}
	retryOut, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: h.gen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: h.board.URL + "/roles/1",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "fetch-role-1-again",
	})
	if err != nil || retryOut.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("exact repeat: %+v %v", retryOut, err)
	}

	// Steering, journal, checkpoint: durable activity behind the run.
	steerMsg, err := h.stack.Research.SteerResearch(h.ctx, httpapi.SteerResearchInput{
		Actor: h.owner, RunID: h.runID, Body: "Prefer remote-friendly roles.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if steerMsg.MessageId == "" {
		t.Fatalf("steer: %+v", steerMsg)
	}
	events, _, err := h.stack.Journal.List(h.ctx, h.runID, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, e := range events {
		kinds[e.Kind] = true
	}
	for _, want := range []string{"claim", "capture", "observation"} {
		if !kinds[want] {
			t.Fatalf("journal missing %q (kinds %v)", want, kinds)
		}
	}
	checkpoint, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.EvidenceIDs) == 0 {
		t.Fatalf("checkpoint without evidence: %+v", checkpoint)
	}

	// Stop fences; resume continues on remaining allowance.
	if _, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "operator check"); err != nil {
		t.Fatal(err)
	}
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	stoppedGen := round.Generation
	if stoppedGen == h.gen {
		t.Fatal("stop did not rotate the control generation")
	}
	if _, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID); err != nil {
		t.Fatal(err)
	}
	round, err = h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	h.gen = round.Generation
	postOut, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: h.gen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: h.board.URL + "/roles/1-cross",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "fetch-after-resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if postOut.Outcome != researchcontract.OutcomeOK && postOut.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("post-resume dispatch: %+v", postOut)
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestBridgeServesRealToolchain(t *testing.T) {
	h := newHarness(t)
	h.commissionSupervisor(t)
	token := strings.Repeat("t", 64)
	svc, err := codexservice.New(h.ctx, h.db, codexservice.Config{
		Host: "isolated.test", User: "runner", IdentityFile: "/key", KnownHostsFile: "/known",
		Launcher: "/runner/launch", IsolationVerified: true, BridgeToken: token,
		Model: "test-model", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetResearchTools(h.stack.Toolchain)
	server := httptest.NewServer(svc.MCPHandler())
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "t23", Version: "1"}, nil)
	session, err := client.Connect(h.ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL, HTTPClient: &http.Client{Transport: bearerTransport{token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	listed, err := session.ListTools(h.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 11 {
		t.Fatalf("bridge tool count: %d", len(listed.Tools))
	}
	seen := map[string]bool{}
	for _, tool := range listed.Tools {
		seen[tool.Name] = true
	}
	for _, name := range codexservice.ResearchToolNames() {
		if !seen[name] {
			t.Fatalf("research tool %q not exposed", name)
		}
	}

	// One real tool call over the wire with a real issued capability.
	cost, ok := store.RoundOperationCost(store.RoundCodexTurn)
	if !ok {
		t.Fatal("codex turn cost missing")
	}
	attempt, _, err := h.db.ReserveRoundAttempt(h.ctx, h.agent, h.runID, store.RoundAttemptInput{
		RequestKey: "t23-bridge-turn", Operation: store.RoundCodexTurn,
		ResourceID: store.ResearchAuthorityResource, Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.MarkRoundDispatched(h.ctx, h.runID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := h.db.IssueRoundToolCapability(h.ctx, h.runID, attempt.ID, h.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(h.ctx, &mcp.CallToolParams{
		Name: "context_read",
		Arguments: map[string]any{
			"capability": capability, "runId": h.runID, "generation": h.gen, "brief": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("context_read over bridge: %+v", result.Content)
	}
}

func TestHTTPResearchAPIOnRealStack(t *testing.T) {
	h := newHarness(t)
	h.commission(t)
	_, capID := h.fetch(t, "fetch-http-1", h.board.URL+"/roles/1")

	authSvc := auth.NewService(h.db)
	const password = "t23-http-password"
	if err := authSvc.SetupAdministrator(h.ctx, []byte(password)); err != nil {
		t.Fatal(err)
	}
	const origin = "http://127.0.0.1:9"
	handler := httpapi.NewHandler(h.db, authSvc, httpapi.Options{
		AllowedOrigins: []string{origin},
		Rounds:         &rounds.Service{Store: h.db},
		Research:       h.stack.Research,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := server.Client()
	do := func(method, path, body string, cookie *http.Cookie, csrf string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, server.URL+"/api/v1"+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "application/json")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Origin", origin)
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(resp.Cookies()) != 1 {
		t.Fatalf("login cookies: %d", len(resp.Cookies()))
	}
	cookie := resp.Cookies()[0]
	status, raw := do(http.MethodGet, "/auth/session", "", cookie, "")
	if status != http.StatusOK {
		t.Fatalf("session: %d %s", status, raw)
	}
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	get := func(path string) []byte {
		t.Helper()
		status, raw := do(http.MethodGet, path, "", cookie, session.CSRFToken)
		if status != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, status, raw)
		}
		return raw
	}
	runRaw := get("/research/runs/" + h.runID)
	if !strings.Contains(string(runRaw), h.runID) {
		t.Fatalf("run view: %s", runRaw)
	}
	activityRaw := get("/research/runs/" + h.runID + "/activity?limit=50")
	if !strings.Contains(string(activityRaw), "capture") {
		t.Fatalf("activity: %s", activityRaw)
	}
	captureRaw := get("/research/captures/" + capID)
	if !strings.Contains(string(captureRaw), capID) {
		t.Fatalf("capture view: %s", captureRaw)
	}
	reportRaw := get("/research/runs/" + h.runID + "/report")
	if !strings.Contains(string(reportRaw), h.runID) {
		t.Fatalf("report: %s", reportRaw)
	}
	// Muse discovery runs are not steerable: the owner commissions a new
	// run instead, and the API says so with a conflict.
	status, steerRaw := do(http.MethodPost, "/research/runs/"+h.runID+"/steer", `{"body":"http steer check"}`, cookie, session.CSRFToken)
	if status != http.StatusConflict {
		t.Fatalf("steer: %d %s, want 409", status, steerRaw)
	}
}

func TestContinuationAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{model: "fixture-jev-1", verdicts: map[string]string{}}
	gate := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{})}
	t.Cleanup(func() { gate.Release() })
	facts := fixtureFacts()
	stack, err := Wire(db, Config{
		ArtifactRoot: filepath.Join(dir, "research-artifacts"), AgentID: DefaultAgentID,
		PermitLoopback: true, JevProvider: provider,
		MuseTransport: gate, MuseFacts: &facts,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := OwnerActor()
	commissioned, err := stack.Research.CommissionResearch(ctx, httpapi.CommissionResearchInput{
		Actor: owner, BriefText: "restart check", IdempotencyKey: "t23-restart-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	runID := commissioned.View.RunId
	round, err := db.Round(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	board := fixtureBoard(t)
	dispatched, err := stack.Supervisor.Dispatch(ctx, rounds.DispatchInput{
		RunID: runID, Generation: round.Generation, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: board.URL + "/roles/1",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "restart-fetch-1",
	})
	if err != nil || dispatched.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("dispatch: %+v %v", dispatched, err)
	}
	wantCapture := dispatched.CaptureID
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen on the same data dir with a fresh stack: durable state stands.
	db2, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	gate2 := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{})}
	t.Cleanup(func() { gate2.Release() })
	stack2, err := Wire(db2, Config{
		ArtifactRoot: filepath.Join(dir, "research-artifacts"), AgentID: DefaultAgentID,
		PermitLoopback: true, JevProvider: provider,
		MuseTransport: gate2, MuseFacts: &facts,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stack2.Supervisor.Checkpoint(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range checkpoint.EvidenceIDs {
		if id == wantCapture {
			found = true
		}
	}
	if !found {
		t.Fatalf("checkpoint lost evidence: %+v", checkpoint)
	}
	events, _, err := stack2.Journal.List(ctx, runID, "", 100)
	if err != nil || len(events) == 0 {
		t.Fatalf("journal after reopen: %d %v", len(events), err)
	}
	round2, err := db2.Round(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if round2.State != store.RoundPaused {
		t.Fatalf("restarted run state: %q", round2.State)
	}
	// Crash recovery pauses the run; resume continues on remaining allowance.
	resumed, err := stack2.Supervisor.Resume(ctx, owner, runID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Generation == 0 {
		t.Fatalf("resume: %+v", resumed)
	}
	round2, err = db2.Round(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := stack2.Supervisor.Dispatch(ctx, rounds.DispatchInput{
		RunID: runID, Generation: round2.Generation, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: board.URL + "/roles/1",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "restart-fetch-2",
	})
	if err != nil || again.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("post-restart reuse: %+v %v", again, err)
	}
	_, rc, err := stack2.Captures.OpenCapture(ctx, wantCapture)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
}

func isolatedShellPath() string {
	if shell := os.Getenv("T23_CHROME_PATH"); shell != "" {
		if _, err := os.Stat(shell); err == nil {
			return shell
		}
		return ""
	}
	def := "/Users/vince/.cache/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-mac-arm64/chrome-headless-shell"
	if _, err := os.Stat(def); err != nil {
		return ""
	}
	return def
}

func TestBrowseKindThroughIntegratedStack(t *testing.T) {
	if isolatedShellPath() == "" {
		t.Skip("isolated headless shell unavailable; T16 covers the browser kind")
	}
	h := newHarness(t)
	h.commissionSupervisor(t)
	out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: h.gen, Kind: researchcontract.ExecuteBrowse,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationBrowser, Backend: "chromium-headless-shell",
			Method: "GET", URLOrQuery: h.board.URL + "/roles/1",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 30000},
		IdempotencyKey: "browse-integrated-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != researchcontract.OutcomeOK || out.CaptureID == "" {
		t.Fatalf("browse dispatch: %+v", out)
	}
	if body := captureBytes(t, h, out.CaptureID); !strings.Contains(body, "Senior Backend Engineer") {
		t.Fatalf("rendered DOM missing marker: %q", body)
	}
}
