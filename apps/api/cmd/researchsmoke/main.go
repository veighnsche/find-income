// Command researchsmoke is a TEST-ONLY server for the T27 browser/product
// acceptance smoke (scripts/ui-smoke/research-smoke.mjs). It is never part
// of production: it binds a controlled fixture board, a scripted zero-spend
// Jev provider, a disposable store and the real T23 research wiring
// (researchwire.Wire), then serves the real HTTP API plus the built web
// bundle on one ephemeral loopback port.
//
// Agent work (dispatch/assess/match/save) runs through the real supervisor,
// executor, memory, Jev, identity and save backends via /test/agent/*
// control endpoints; no live model drives turns here (live behavior is the
// T30 canary). Non-research rounds (pack preparation, correction intake)
// commission against the real store/HTTP with a disclosed no-op execution
// worker, so the smoke verifies intake durability without claiming runner
// completion. Research stop/resume always routes to the real supervisor;
// the test readiness explicitly rejects research_run to fail loudly on a
// routing regression.
//
// Configuration (environment): RESEARCHSMOKE_WEB_DIST (required, built
// web bundle dir), RESEARCHSMOKE_PASSWORD (optional admin password;
// random when empty). Prints one JSON line {"url","password"} on stdout
// and serves until SIGINT/SIGTERM arrives.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const (
	roleBody1 = "Northwind Traders seeks a Senior Backend Engineer in Berlin (REQ-NW-101). " +
		"Full-time, hybrid, EUR 6000-7500 monthly. Go services, Postgres, on-call rotation. " +
		"Apply with CV and availability."
	roleBody1Cross = "Senior Backend Engineer (m/f/d) -- Northwind Traders, Berlin, ref REQ-NW-101. " +
		"Full time hybrid position, EUR 6000-7500 per month. Go microservices and Postgres."
	roleBody2 = "Contoso Support seeks a Support Engineer in Amsterdam. Full-time remote, " +
		"EUR 4000-5000 monthly. Ticket queue, incident response, customer calls."
	emptyBody = "No roles match this search yet. Try different keywords."
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("researchsmoke: %v", err)
	}
}

// scriptedProvider is a zero-spend jevassess.Provider: verdicts are scripted
// per question id, pinning the binding machinery (capture refs, brief match,
// exchange log, sink), not provider evidence.
type scriptedProvider struct {
	mu       sync.Mutex
	model    string
	verdicts map[string]string
	calls    int
}

func (p *scriptedProvider) EncodedRequest(r jev.Request) ([]byte, error) {
	return json.Marshal(r.State)
}

func (p *scriptedProvider) RequestedModel() string { return p.model }

func (p *scriptedProvider) EvaluateOnceCaptured(_ context.Context, r jev.Request) (jev.Result, jev.CapturedExchange, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
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

// testReadiness accepts non-research round commissions for intake checks
// and loudly rejects research_run: research control must go through the
// supervisor, never the legacy worker path.
type testReadiness struct{}

func (testReadiness) CheckRound(_ context.Context, outcome string) error {
	if outcome == "research_run" {
		return fmt.Errorf("researchsmoke: research_run must route to the supervisor, not the test worker")
	}
	return nil
}

// testWorker records launches without model execution: intake durability
// (accepted, idempotent, reload-safe) is real; completion needs the runner.
type testWorker struct {
	mu       sync.Mutex
	launched []string
}

func (w *testWorker) LaunchRound(_ context.Context, r store.Round) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.launched = append(w.launched, r.ID+":"+r.Outcome)
	return nil
}

func (w *testWorker) CancelRound(_ string) {}

type harness struct {
	ctx      context.Context
	db       *store.Store
	stack    *researchwire.Stack
	owner    store.Actor
	agent    store.Actor
	board    string
	provider *scriptedProvider
	worker   *testWorker
}

func run() error {
	dist := os.Getenv("RESEARCHSMOKE_WEB_DIST")
	if dist == "" {
		return fmt.Errorf("RESEARCHSMOKE_WEB_DIST is required")
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		return fmt.Errorf("web bundle missing at %s: %w", dist, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := os.MkdirTemp("", "researchsmoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(ctx, dir)
	if err != nil {
		return err
	}
	defer db.Close()
	password := os.Getenv("RESEARCHSMOKE_PASSWORD")
	if password == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return err
		}
		password = hex.EncodeToString(raw[:])
	}
	authSvc := auth.NewService(db)
	if err := authSvc.SetupAdministrator(ctx, []byte(password)); err != nil {
		return err
	}
	board := startBoard()
	defer board.Close()
	provider := &scriptedProvider{model: "fixture-jev-1", verdicts: map[string]string{}}
	stack, err := researchwire.Wire(db, researchwire.Config{
		ArtifactRoot:   filepath.Join(dir, "research-artifacts"),
		ScratchRoot:    filepath.Join(dir, "scratch"),
		AgentID:        researchwire.DefaultAgentID,
		PermitLoopback: true, // test-only: controlled fixture board
		JevProvider:    provider,
	})
	if err != nil {
		return err
	}
	worker := &testWorker{}
	h := &harness{ctx: ctx, db: db, stack: stack,
		owner:    researchwire.OwnerActor(),
		agent:    store.Actor{Kind: "agent", ID: researchwire.DefaultAgentID},
		board:    board.URL,
		provider: provider,
		worker:   worker,
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	origin := "http://" + listener.Addr().String()
	api := httpapi.NewHandler(db, authSvc, httpapi.Options{
		AllowedOrigins:  []string{origin},
		Rounds:          &rounds.Service{Store: db, Readiness: testReadiness{}, Worker: worker},
		Research:        stack.Research,
		ResearchControl: stack.Supervisor,
	})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", api)
	mux.HandleFunc("/test/agent/research", h.handleResearch)
	mux.HandleFunc("/test/agent/zero-result", h.handleZeroResult)
	mux.HandleFunc("/test/agent/dispatch", h.handleDispatch)
	mux.HandleFunc("/test/state", h.handleState)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		serveDist(dist, w, r)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shut)
	}()
	raw, _ := json.Marshal(map[string]string{"url": origin, "password": password})
	if _, err := fmt.Println(string(raw)); err != nil {
		return err
	}
	log.Printf("researchsmoke: serving %s (board %s, data %s)", origin, board.URL, dir)
	if err := server.Serve(listener); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func startBoard() *boardServer {
	mux := http.NewServeMux()
	mux.HandleFunc("/roles/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body><h1>Senior Backend Engineer</h1><p>"+roleBody1+"</p></body></html>")
	})
	mux.HandleFunc("/roles/1-cross", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body><h1>Senior Backend Engineer (m/f/d)</h1><p>"+roleBody1Cross+"</p></body></html>")
	})
	mux.HandleFunc("/roles/empty", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body><p>"+emptyBody+"</p></body></html>")
	})
	mux.HandleFunc("/api/roles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		quoted, _ := json.Marshal(roleBody2)
		_, _ = io.WriteString(w, `{"roles":[{"title":"Support Engineer","employer":"Contoso Support","text":`+string(quoted)+`}]}`)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("researchsmoke board: %v", err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return &boardServer{Server: server, URL: "http://" + listener.Addr().String()}
}

type boardServer struct {
	*http.Server
	URL string
}

func (b *boardServer) Close() {
	shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.Shutdown(shut)
}

func serveDist(dist string, w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		file := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if strings.HasPrefix(file, dist+string(os.PathSeparator)) {
			if info, err := os.Stat(file); err == nil && !info.IsDir() {
				serveFile(file, w)
				return
			}
		}
	}
	serveFile(filepath.Join(dist, "index.html"), w)
}

func serveFile(file string, w http.ResponseWriter) {
	raw, err := os.ReadFile(file)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	mime := "application/octet-stream"
	switch filepath.Ext(file) {
	case ".js":
		mime = "text/javascript"
	case ".css":
		mime = "text/css"
	case ".html":
		mime = "text/html"
	case ".svg":
		mime = "image/svg+xml"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func writeTestJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func failTest(w http.ResponseWriter, err error) {
	writeTestJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func excerptSHA(body string, start, end int) string {
	sum := sha256.Sum256([]byte(body[start:end]))
	return hex.EncodeToString(sum[:])
}

func spanOf(body, needle string) (int64, int64, error) {
	at := strings.Index(body, needle)
	if at < 0 {
		return 0, 0, fmt.Errorf("needle %q missing", needle)
	}
	return int64(at), int64(at + len(needle)), nil
}

func (h *harness) captureBytes(capID string) (string, error) {
	_, rc, err := h.stack.Captures.OpenCapture(h.ctx, capID)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (h *harness) generation(runID string) (int64, int64, string, error) {
	round, err := h.db.Round(h.ctx, runID)
	if err != nil {
		return 0, 0, "", err
	}
	profile, rubric, err := h.briefOf(runID)
	if err != nil {
		return 0, 0, "", err
	}
	return round.Generation, profile, rubric, nil
}

// briefOf reads the binding brief versions from the run checkpoint.
func (h *harness) briefOf(runID string) (int64, string, error) {
	checkpoint, err := h.stack.Supervisor.Checkpoint(h.ctx, runID)
	if err != nil {
		return 0, "", err
	}
	return checkpoint.ProfileVersion, checkpoint.RubricVersion, nil
}

func (h *harness) dispatchFetch(runID string, gen int64, key, url string) (researchcontract.Outcome, researchcontract.ExecutionReceipt, string, error) {
	out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: runID, Generation: gen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: url,
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: key,
	})
	if err != nil {
		return "", researchcontract.ExecutionReceipt{}, "", err
	}
	return out.Outcome, out.Receipt, out.ObservationID, nil
}

// handleResearch performs the scripted results scenario through the real
// backends: two fetches (role + cross-post), one assessment, match, two
// converging saves, one read-only API POST save, one 404 negative and one
// exact-repeat reuse. The browser commissioned the run; this stands in for
// the agent's tool calls only.
func (h *harness) handleResearch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.RunID == "" {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "runId required"})
		return
	}
	out, err := h.scriptResults(in.RunID)
	if err != nil {
		failTest(w, err)
		return
	}
	writeTestJSON(w, http.StatusOK, out)
}

func (h *harness) scriptResults(runID string) (map[string]any, error) {
	gen, profile, rubric, err := h.generation(runID)
	if err != nil {
		return nil, err
	}
	saver, err := h.stack.NewSaverFor(h.agent)
	if err != nil {
		return nil, err
	}
	outcome, receipt1, _, err := h.dispatchFetch(runID, gen, "smoke-fetch-1", h.board+"/roles/1")
	if err != nil || outcome != researchcontract.OutcomeOK {
		return nil, fmt.Errorf("fetch role 1: outcome=%s err=%v", outcome, err)
	}
	outcome, receipt2, _, err := h.dispatchFetch(runID, gen, "smoke-fetch-1-cross", h.board+"/roles/1-cross")
	if err != nil || outcome != researchcontract.OutcomeOK {
		return nil, fmt.Errorf("fetch cross-post: outcome=%s err=%v", outcome, err)
	}
	cap1, cap2 := receipt1.CaptureID, receipt2.CaptureID
	body1, err := h.captureBytes(cap1)
	if err != nil {
		return nil, err
	}
	body2, err := h.captureBytes(cap2)
	if err != nil {
		return nil, err
	}
	s1s, s1e, err := spanOf(body1, "REQ-NW-101")
	if err != nil {
		return nil, err
	}
	s2s, s2e, err := spanOf(body2, "REQ-NW-101")
	if err != nil {
		return nil, err
	}
	t1s, t1e, err := spanOf(body1, "Senior Backend Engineer")
	if err != nil {
		return nil, err
	}
	t2s, t2e, err := spanOf(body2, "Senior Backend Engineer (m/f/d)")
	if err != nil {
		return nil, err
	}
	sameRefs := []researchcontract.EvidenceRef{
		{CaptureID: cap1, SpanStart: s1s, SpanEnd: s1e},
		{CaptureID: cap2, SpanStart: s2s, SpanEnd: s2e},
	}
	distinctRefs := []researchcontract.EvidenceRef{
		{CaptureID: cap1, SpanStart: t1s, SpanEnd: t1e},
		{CaptureID: cap2, SpanStart: t2s, SpanEnd: t2e},
	}
	h.provider.mu.Lock()
	h.provider.verdicts["smoke-q-cross"] = "same"
	h.provider.mu.Unlock()
	assessed, err := h.stack.Assessor.Assess(h.ctx, researchcontract.AssessInput{
		Purpose: "identity_match",
		Questions: []researchcontract.AssessQuestion{{
			ID: "smoke-q-cross", Text: "Do these captures describe one opening?",
			Alternatives: []researchcontract.AssessAlternative{
				{ID: "same", Label: "one opening", EvidenceRefs: sameRefs},
				{ID: "distinct", Label: "two openings", EvidenceRefs: distinctRefs},
			},
			AbstainAllowed: true,
		}},
		ProfileVersion: profile, RubricVersion: rubric,
		SourceRefs:     append(append([]researchcontract.EvidenceRef{}, sameRefs...), distinctRefs...),
		IdempotencyKey: "smoke-assess-cross", RunID: runID, Generation: gen,
	})
	if err != nil {
		return nil, fmt.Errorf("assess cross: %w", err)
	}
	allRefs := append(append([]researchcontract.EvidenceRef{}, sameRefs...), distinctRefs...)
	match, err := h.stack.Matcher.Match(h.ctx, researchcontract.MatchInput{
		Attributes: researchcontract.MatchAttributes{
			Employer: "Northwind Traders", Title: "Senior Backend Engineer",
			URL: h.board + "/roles/1", RequisitionID: "REQ-NW-101", Location: "Berlin",
		},
		EvidenceRefs: allRefs, RunID: runID, Generation: gen,
	})
	if err != nil || match.Outcome != researchcontract.OutcomeOK {
		return nil, fmt.Errorf("first match: %+v %v", match, err)
	}
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
		for _, ref := range allRefs {
			out = append(out, link(ref))
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
					"kind": "employment", "sourceUrl": h.board + "/roles/1", "originalText": body1,
					"locationText": "Berlin", "requisitionId": "REQ-NW-101", "reqIssuer": "northwind",
					"vacancyComplete": "true"},
				EvidenceLinks:    links(),
				AssessmentIDs:    []string{assessed.ID},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "new"}},
		},
		IdempotencyKey: "smoke-save-1", RunID: runID, Generation: gen,
	})
	if err != nil {
		return nil, fmt.Errorf("save role 1: %w", err)
	}
	if saved1.Outcome != researchcontract.OutcomeOK || len(saved1.Saved) != 2 {
		return nil, fmt.Errorf("save role 1: %+v", saved1)
	}
	opp1, rev1, company1 := saved1.Saved[1].RecordID, saved1.Saved[1].Revision, saved1.Saved[0].RecordID
	if err := h.stack.Supervisor.NoteSavedRecords(h.ctx, runID, "smoke-save-1", []string{company1, opp1}); err != nil {
		return nil, fmt.Errorf("note save 1: %w", err)
	}
	h.provider.mu.Lock()
	h.provider.verdicts["cmp-opportunity-"+opp1] = "same"
	h.provider.verdicts["cmp-company-"+company1] = "distinct"
	h.provider.mu.Unlock()
	match2, err := h.stack.Matcher.Match(h.ctx, researchcontract.MatchInput{
		Attributes: researchcontract.MatchAttributes{
			Employer: "Northwind Traders", Title: "Senior Backend Engineer (m/f/d)",
			URL: h.board + "/roles/1-cross", RequisitionID: "REQ-NW-101", Location: "Berlin",
		},
		EvidenceRefs: allRefs, RunID: runID, Generation: gen,
	})
	if err != nil {
		return nil, fmt.Errorf("cross-post match: %w", err)
	}
	if len(match2.ExactMatches) != 1 || match2.ExactMatches[0].RecordID != opp1 {
		return nil, fmt.Errorf("cross-post match: %+v", match2)
	}
	savedCross, err := saver.Save(h.ctx, researchcontract.SaveBatch{
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveUpdateOpportunity, RecordID: opp1, ExpectedRevision: rev1,
				Fields: map[string]string{"title": "Senior Backend Engineer",
					"kind": "employment", "sourceUrl": h.board + "/roles/1-cross", "originalText": body2,
					"locationText": "Berlin", "requisitionId": "REQ-NW-101", "reqIssuer": "northwind",
					"vacancyComplete": "true"},
				EvidenceLinks: links(),
				AssessmentIDs: []string{assessed.ID},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "same",
					Candidates: []researchcontract.CandidateIdentity{{CandidateID: opp1, Kind: "opportunity", Revision: rev1}}}},
		},
		IdempotencyKey: "smoke-save-1-cross", RunID: runID, Generation: gen,
	})
	if err != nil {
		return nil, fmt.Errorf("cross-post save: %w", err)
	}
	if savedCross.Outcome != researchcontract.OutcomeOK || savedCross.Saved[0].RecordID != opp1 {
		return nil, fmt.Errorf("cross-post save: %+v", savedCross)
	}
	if err := h.stack.Supervisor.NoteSavedRecords(h.ctx, runID, "smoke-save-1-cross", []string{opp1}); err != nil {
		return nil, fmt.Errorf("note cross-post save: %w", err)
	}
	apiOut, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: runID, Generation: gen, Kind: researchcontract.ExecuteAPI,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationAPI, Backend: "generic-http",
			Method: "POST", URLOrQuery: h.board + "/api/roles",
			Params: []researchcontract.Param{{Name: "Content-Type", Value: "application/json"}},
			Body:   `{"q":"support"}`,
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 15000},
		IdempotencyKey: "smoke-api-roles",
	})
	if err != nil || apiOut.Outcome != researchcontract.OutcomeOK {
		return nil, fmt.Errorf("api dispatch: %+v %v", apiOut, err)
	}
	cap3 := apiOut.Receipt.CaptureID
	body3, err := h.captureBytes(cap3)
	if err != nil {
		return nil, err
	}
	s3s, s3e, err := spanOf(body3, "Support Engineer")
	if err != nil {
		return nil, err
	}
	a3s, a3e, err := spanOf(body3, "Amsterdam")
	if err != nil {
		return nil, err
	}
	ref3a := researchcontract.EvidenceRef{CaptureID: cap3, SpanStart: s3s, SpanEnd: s3e}
	ref3b := researchcontract.EvidenceRef{CaptureID: cap3, SpanStart: a3s, SpanEnd: a3e}
	// Role 2 is a factual save before fit is known (plan §6): no
	// assessment backing, so the store labels it unassessed.
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
					"kind": "employment", "sourceUrl": h.board + "/api/roles", "originalText": body3,
					"locationText": "Amsterdam", "vacancyComplete": "true"},
				EvidenceLinks:    []researchcontract.EvidenceLink{link3(ref3a), link3(ref3b)},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "new"}},
		},
		IdempotencyKey: "smoke-save-2", RunID: runID, Generation: gen,
	})
	if err != nil {
		return nil, fmt.Errorf("save role 2: %w", err)
	}
	if saved2.Outcome != researchcontract.OutcomeOK || saved2.Saved[1].RecordID == opp1 {
		return nil, fmt.Errorf("save role 2: %+v", saved2)
	}
	opp2, company2 := saved2.Saved[1].RecordID, saved2.Saved[0].RecordID
	if err := h.stack.Supervisor.NoteSavedRecords(h.ctx, runID, "smoke-save-2", []string{company2, opp2}); err != nil {
		return nil, fmt.Errorf("note save 2: %w", err)
	}
	negOutcome, negReceipt, _, err := h.dispatchFetch(runID, gen, "smoke-fetch-missing", h.board+"/roles/missing")
	if err != nil {
		return nil, fmt.Errorf("negative fetch: %w", err)
	}
	if negReceipt.Status == researchcontract.ReceiptOK {
		return nil, fmt.Errorf("missing page receipt unexpectedly ok: %+v", negReceipt)
	}
	reuseOutcome, _, _, err := h.dispatchFetch(runID, gen, "smoke-fetch-1-again", h.board+"/roles/1")
	if err != nil || reuseOutcome != researchcontract.OutcomeReused {
		return nil, fmt.Errorf("exact repeat: outcome=%s err=%v", reuseOutcome, err)
	}
	var sighted int
	if err := h.db.Read(h.ctx, func(r store.Reader) error {
		rows, err := store.ListRecordSightingsByOpportunity(h.ctx, r, opp1)
		if err != nil {
			return err
		}
		sighted = len(rows)
		return nil
	}); err != nil {
		return nil, err
	}
	return map[string]any{
		"opportunity1": opp1, "company1": company1,
		"opportunity2": opp2, "company2": company2,
		"capture1": cap1, "capture2": cap2, "capture3": cap3,
		"assessment1": assessed.ID,
		"sightings1":  sighted, "negativeOutcome": string(negOutcome),
		"negativeStatus": string(negReceipt.Status), "reused": true,
	}, nil
}

// handleZeroResult performs a no-yield investigation: a 404 and an empty
// 200 page. Nothing is saved; the run report must stay honestly empty.
func (h *harness) handleZeroResult(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.RunID == "" {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "runId required"})
		return
	}
	round, err := h.db.Round(h.ctx, in.RunID)
	if err != nil {
		failTest(w, err)
		return
	}
	missingOutcome, missingReceipt, missingObs, err := h.dispatchFetch(in.RunID, round.Generation, "smoke-zero-missing", h.board+"/roles/missing")
	if err != nil {
		failTest(w, err)
		return
	}
	emptyOutcome, emptyReceipt, emptyObs, err := h.dispatchFetch(in.RunID, round.Generation, "smoke-zero-empty", h.board+"/roles/empty")
	if err != nil {
		failTest(w, err)
		return
	}
	writeTestJSON(w, http.StatusOK, map[string]any{
		"missing": map[string]any{"outcome": string(missingOutcome), "status": string(missingReceipt.Status), "observation": missingObs},
		"empty":   map[string]any{"outcome": string(emptyOutcome), "status": string(emptyReceipt.Status), "observation": emptyObs},
		"saved":   []string{},
	})
}

// handleDispatch runs one ad-hoc fetch under the given generation so the
// smoke can prove the stop fence (stale generation fails) and resume
// (fresh generation dispatches) through the real supervisor.
func (h *harness) handleDispatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunID      string `json:"runId"`
		Generation int64  `json:"generation"`
		Key        string `json:"key"`
		Path       string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.RunID == "" || in.Key == "" || in.Path == "" {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "runId, generation, key, path required"})
		return
	}
	outcome, receipt, obs, err := h.dispatchFetch(in.RunID, in.Generation, in.Key, h.board+in.Path)
	if err != nil {
		writeTestJSON(w, http.StatusOK, map[string]any{"outcome": "error", "error": err.Error()})
		return
	}
	writeTestJSON(w, http.StatusOK, map[string]any{
		"outcome": string(outcome), "status": string(receipt.Status), "observation": obs,
	})
}

// handleState reports backend truth for cross-checking UI counts: live
// generation/state, journal volume, commission count, checkpoint saves and
// the usage ledger. The smoke asserts the UI projections equal this state.
func (h *harness) handleState(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("runId")
	if runID == "" {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "runId required"})
		return
	}
	round, err := h.db.Round(h.ctx, runID)
	if err != nil {
		failTest(w, err)
		return
	}
	events, _, err := h.stack.Journal.List(h.ctx, runID, "", 1000)
	if err != nil {
		failTest(w, err)
		return
	}
	kinds := map[string]int{}
	commissioned := 0
	for _, e := range events {
		kinds[e.Kind]++
		if e.Kind == "run.commissioned" {
			commissioned++
		}
	}
	checkpoint, err := h.stack.Supervisor.Checkpoint(h.ctx, runID)
	if err != nil {
		failTest(w, err)
		return
	}
	ledger, err := h.stack.Supervisor.Usage(h.ctx, runID)
	if err != nil {
		failTest(w, err)
		return
	}
	writeTestJSON(w, http.StatusOK, map[string]any{
		"state": round.State, "generation": round.Generation,
		"journalEvents": len(events), "journalKinds": kinds, "commissioned": commissioned,
		"savedIds": checkpoint.SavedRecordIDs, "unresolved": len(checkpoint.UnresolvedAttempts),
		"observedActions": ledger.Observed.Items, "reservedActions": ledger.Reserved.Items,
		"unknown": ledger.Unknown,
	})
}
