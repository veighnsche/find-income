// Package researchrestore proves current-format backup/restore on composed
// data (lane C, T25). New tests only: it drives the T23 integrated stack
// (researchwire.Wire) through the real recovery.py CLI and asserts the
// restored database, artifacts, manifest and idle behavior. No production
// file is changed; every directory used is a fresh private test directory.
package researchrestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchmemory"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// scriptedProvider is a zero-spend jevassess.Provider: verdicts are scripted
// per question id, pinning the binding machinery, not provider evidence.
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
		raw, _ := json.Marshal(roleBody2)
		_, _ = io.WriteString(w, `{"roles":[{"title":"Support Engineer","employer":"Contoso Support","text":`+string(raw)+`}]}`)
	})
	return httptest.NewServer(mux)
}

// repoPaths locates the dashboard checkout and the pinned career assets from
// this test file's position, independent of the caller's workdir.
func repoPaths(t *testing.T) (recoveryPY, assetsSrc string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dashboard := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".."))
	recoveryPY = filepath.Join(dashboard, "ops", "i26", "recovery.py")
	assetsSrc = filepath.Dir(dashboard) // pinned assets live at the workspace root
	if _, err := os.Stat(recoveryPY); err != nil {
		t.Fatalf("recovery.py missing: %v", err)
	}
	return recoveryPY, assetsSrc
}

var pinnedAssets = []string{
	"cv-vince-liem.typ",
	"cv-vince-liem.md",
	"github-evidence-review.md",
	"portfolio-case-studies.md",
}

func mkdirPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writePrivate(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRecovery(t *testing.T, recoveryPY string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", append([]string{recoveryPY}, args...)...)
	combined, err := cmd.CombinedOutput()
	t.Logf("recovery.py %s\n%s", strings.Join(args, " "), combined)
	if err != nil {
		t.Fatalf("recovery.py %s: %v", args[0], err)
	}
	return strings.TrimSpace(string(combined))
}

func queryInt(t *testing.T, db *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	ctx := context.Background()
	if err := db.Read(ctx, func(r store.Reader) error {
		return r.QueryRowContext(ctx, q, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

func queryString(t *testing.T, db *store.Store, q string, args ...any) string {
	t.Helper()
	var s string
	ctx := context.Background()
	if err := db.Read(ctx, func(r store.Reader) error {
		return r.QueryRowContext(ctx, q, args...).Scan(&s)
	}); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return s
}

func queryStrings(t *testing.T, db *store.Store, q string, args ...any) []string {
	t.Helper()
	var out []string
	ctx := context.Background()
	if err := db.Read(ctx, func(r store.Reader) error {
		rows, err := r.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return out
}

func queryPairs(t *testing.T, db *store.Store, q string, args ...any) map[string]string {
	t.Helper()
	out := map[string]string{}
	ctx := context.Background()
	if err := db.Read(ctx, func(r store.Reader) error {
		rows, err := r.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			out[k] = v
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return out
}

type composed struct {
	ctx      context.Context
	db       *store.Store
	stack    *Stack
	owner    store.Actor
	agent    store.Actor
	board    *httptest.Server
	provider *scriptedProvider
	runID    string
	gen      int64
	profile  int64
	rubric   string
}

type Stack = researchwire.Stack

func wireComposed(t *testing.T, dataDir, artifactRoot, scratchRoot string) *composed {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	db, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	provider := &scriptedProvider{model: "fixture-jev-1", verdicts: map[string]string{}}
	stack, err := researchwire.Wire(db, researchwire.Config{
		ArtifactRoot:   artifactRoot,
		ScratchRoot:    scratchRoot,
		AgentID:        researchwire.DefaultAgentID,
		PermitLoopback: true, // test-only: controlled fixture board
		JevProvider:    provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &composed{ctx: ctx, db: db, stack: stack,
		owner:    researchwire.OwnerActor(),
		agent:    store.Actor{Kind: "agent", ID: researchwire.DefaultAgentID},
		board:    fixtureBoard(t),
		provider: provider,
	}
}

// commission opens a codex-scoped run directly through the run
// supervisor. Recovery tests pin restore mechanics over composed state,
// not discovery commissioning, and need the codex scope plus a
// steerable run.
func (h *composed) commission(t *testing.T) {
	t.Helper()
	ownerBrief, err := codexservice.CurrentOwnerBrief(h.ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.stack.Supervisor.Commission(h.ctx, rounds.CommissionInput{
		Actor: h.owner, BriefText: "Find backend roles in Berlin.", AgentID: h.agent.ID,
		ProfileVersion: ownerBrief.ProfileVersion, RubricVersion: ownerBrief.RubricVersion,
		RubricSource: ownerBrief.Source, IdempotencyKey: "t25-run-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || out.RunID == "" {
		t.Fatalf("commission: %+v", out)
	}
	h.runID = out.RunID
	h.profile = out.ProfileVersion
	h.rubric = out.RubricVersion
	h.refreshGen(t)
}

func (h *composed) refreshGen(t *testing.T) {
	t.Helper()
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	h.gen = round.Generation
}

func (h *composed) fetch(t *testing.T, key, url string) (obsID, contentSHA string) {
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
	if out.Receipt.CaptureID == "" || out.ObservationID == "" {
		t.Fatalf("dispatch %s missing ids: %+v", key, out)
	}
	return out.ObservationID, out.Receipt.CaptureID
}

func captureBytes(t *testing.T, h *composed, contentSHA string) string {
	t.Helper()
	_, rc, err := h.stack.Captures.OpenCapture(h.ctx, contentSHA)
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

func (h *composed) assess(t *testing.T, key, questionID, verdict string, sameRefs, distinctRefs []researchcontract.EvidenceRef) string {
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

func excerptSHA(body string, start, end int) string {
	sum := sha256.Sum256([]byte(body[start:end]))
	return hex.EncodeToString(sum[:])
}

// records bundles the saved-record ids the restore assertions re-check.
type records struct {
	opp1, company1 string
	opp2           string
	rev1           int64
	cap1, cap2     string            // cross-post content SHAs pinning opp1
	bodies         map[string]string // contentSHA -> bytes
}

// saveComposedRecords runs the T23 record flow: two fetches converging on one
// record with two sightings, plus a distinct second record via API POST.
func (h *composed) saveComposedRecords(t *testing.T) records {
	t.Helper()
	saver, err := h.stack.NewSaverFor(h.agent)
	if err != nil {
		t.Fatal(err)
	}
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

	h.provider.verdicts["cmp-opportunity-"+opp1] = "same"
	h.provider.verdicts["cmp-company-"+company1] = "distinct"

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
	h.assertSightings(t, opp1, cap1, cap2)

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
	cap3 := apiOut.Receipt.CaptureID
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
	return records{opp1: opp1, rev1: rev1, company1: company1, opp2: saved2.Saved[1].RecordID,
		cap1: cap1, cap2: cap2, bodies: map[string]string{cap1: body1, cap2: body2, cap3: body3}}
}

func (h *composed) assertSightings(t *testing.T, oppID, cap1, cap2 string) {
	t.Helper()
	sighted := map[string]bool{}
	if err := h.db.Read(h.ctx, func(r store.Reader) error {
		rows, err := store.ListRecordSightingsByOpportunity(h.ctx, r, oppID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			sighted[row.ContentSHA256] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(sighted) != 2 || !sighted[cap1] || !sighted[cap2] {
		t.Fatalf("cross-post sightings: %v", sighted)
	}
}

// pivotAndSteer records a distinguishable negative, an exact-repeat reuse,
// one steering message and a checkpoint read on the composed run.
func (h *composed) pivotAndSteer(t *testing.T) researchcontract.Checkpoint {
	t.Helper()
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
	steerMsg, err := h.stack.Research.SteerResearch(h.ctx, httpapi.SteerResearchInput{
		Actor: h.owner, RunID: h.runID, Body: "Prefer remote-friendly roles.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if steerMsg.MessageId == "" {
		t.Fatalf("steer: %+v", steerMsg)
	}
	events, _, err := h.stack.Journal.List(h.ctx, h.runID, "", 500)
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
	return checkpoint
}

// liveState arranges the exact pre-backup activity the scrubber must
// invalidate or retain: one dispatched round attempt with an issued tool
// capability, one live claimed research request, and one settled uncertain
// request with its receipt. All rows flow through production code.
type liveState struct {
	attemptID  string
	capability string
	liveFP     string
	uncFP      string
	uncObs     string
}

func (h *composed) arrangeLiveState(t *testing.T) liveState {
	t.Helper()
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if round.State != store.RoundRunning {
		t.Fatalf("run not active before backup: %q", round.State)
	}
	cost, ok := store.RoundOperationCost(store.RoundCodexTurn)
	if !ok {
		t.Fatal("codex turn cost missing")
	}
	attempt, _, err := h.db.ReserveRoundAttempt(h.ctx, h.agent, h.runID, store.RoundAttemptInput{
		RequestKey: "t25-live-turn", Operation: store.RoundCodexTurn,
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

	liveReq := researchcontract.RequestDescriptor{
		Operation: researchcontract.OperationFetch, Backend: "generic-http",
		Method: "GET", URLOrQuery: h.board.URL + "/roles/live-claim-probe",
	}
	liveFP := mustFingerprint(t, liveReq)
	liveClaim, err := h.stack.Memory.Claim(h.ctx, h.runID, attempt.ID, h.gen, liveReq, "t25-live-claim")
	if err != nil {
		t.Fatal(err)
	}
	if liveClaim.Outcome != researchcontract.OutcomeOK || liveClaim.Lease == nil {
		t.Fatalf("live claim: %+v %v", liveClaim, err)
	}

	uncReq := researchcontract.RequestDescriptor{
		Operation: researchcontract.OperationFetch, Backend: "generic-http",
		Method: "GET", URLOrQuery: h.board.URL + "/roles/uncertain-probe",
	}
	uncFP := mustFingerprint(t, uncReq)
	uncClaim, err := h.stack.Memory.Claim(h.ctx, h.runID, attempt.ID, h.gen, uncReq, "t25-uncertain-claim")
	if err != nil {
		t.Fatal(err)
	}
	if uncClaim.Outcome != researchcontract.OutcomeOK || uncClaim.Lease == nil {
		t.Fatalf("uncertain claim: %+v %v", uncClaim, err)
	}
	now := time.Now()
	obsOut, err := h.stack.Memory.Observe(h.ctx, researchmemory.ObserveInput{
		LeaseID: uncClaim.Lease.LeaseID, Owner: attempt.ID, Generation: h.gen,
		RoundID: h.runID, RoundAttemptID: attempt.ID,
		ActualURLOrQuery: uncReq.URLOrQuery,
		StartedAt:        now.Add(-time.Second), FinishedAt: now,
		Outcome:    store.ObservationUncertain,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt: researchcontract.ExecutionReceipt{
			ID: "t25-uncertain-rcpt-1", Operation: researchcontract.OperationFetch,
			Status: researchcontract.ReceiptUncertain, Attempts: 1,
			Executor:  researchcontract.ExecutorIdentity{Backend: "generic-http", Version: "t25-probe"},
			StartedAt: now.Add(-time.Second), EndedAt: now,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if obsOut.Late || obsOut.ObservationID == "" {
		t.Fatalf("uncertain observe: %+v %v", obsOut, err)
	}
	lookup, err := h.stack.Memory.Lookup(h.ctx, h.runID, uncReq)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Outcome != researchcontract.OutcomeUncertain {
		t.Fatalf("uncertain lookup: %+v", lookup)
	}
	if n := queryInt(t, h.db, "SELECT COUNT(*) FROM research_requests WHERE state='claimed'"); n != 1 {
		t.Fatalf("live claims before backup: %d", n)
	}
	return liveState{attemptID: attempt.ID, capability: capability, liveFP: liveFP, uncFP: uncFP, uncObs: obsOut.ObservationID}
}

func mustFingerprint(t *testing.T, req researchcontract.RequestDescriptor) string {
	t.Helper()
	fp, err := req.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// snapshot captures the pre-backup database shape the restore assertions
// compare against.
type snapshot struct {
	captures, observations, events, checkpoints int
	capabilities                                int
	requests                                    map[string]string // id -> state
	requestFPs                                  map[string]string // id -> fingerprint
	attempts                                    map[string]string // id -> state
	roundState                                  string
	roundGen                                    int64
	checkpointGen                               int64
	checkpointActive                            string
	checkpointEvidence                          string
	receipts                                    []string
	captureRows                                 map[string]captureRow // id -> row
	opportunities, companies, assessments       int
}

type captureRow struct {
	sha, ref string
	size     int
}

func takeSnapshot(t *testing.T, db *store.Store) snapshot {
	t.Helper()
	snap := snapshot{
		captures:     queryInt(t, db, "SELECT COUNT(*) FROM source_captures"),
		observations: queryInt(t, db, "SELECT COUNT(*) FROM research_observations"),
		events:       queryInt(t, db, "SELECT COUNT(*) FROM run_events"),
		checkpoints:  queryInt(t, db, "SELECT COUNT(*) FROM run_checkpoints"),
		capabilities: queryInt(t, db, "SELECT COUNT(*) FROM round_tool_capabilities"),
		requests:     queryPairs(t, db, "SELECT id,state FROM research_requests"),
		requestFPs:   queryPairs(t, db, "SELECT id,fingerprint FROM research_requests"),
		attempts:     queryPairs(t, db, "SELECT id,state FROM round_attempts"),
		receipts: queryStrings(t, db,
			"SELECT receipt_ref FROM research_observations WHERE receipt_ref IS NOT NULL ORDER BY id"),
		opportunities: queryInt(t, db, "SELECT COUNT(*) FROM opportunities"),
		companies:     queryInt(t, db, "SELECT COUNT(*) FROM companies"),
		assessments:   queryInt(t, db, "SELECT COUNT(*) FROM jev_assessments_dynamic"),
	}
	ctx := context.Background()
	if err := db.Read(ctx, func(r store.Reader) error {
		rows, err := r.QueryContext(ctx, "SELECT id,content_sha256,artifact_ref,byte_length FROM source_captures")
		if err != nil {
			return err
		}
		defer rows.Close()
		snap.captureRows = map[string]captureRow{}
		for rows.Next() {
			var id string
			var row captureRow
			if err := rows.Scan(&id, &row.sha, &row.ref, &row.size); err != nil {
				return err
			}
			snap.captureRows[id] = row
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return snap
}

type manifestCapture struct {
	SHA256 string `json:"sha256"`
	Ref    string `json:"ref"`
	Size   int    `json:"size"`
}

type manifestFile struct {
	Format             string                     `json:"format"`
	SchemaSHA256       string                     `json:"schemaSha256"`
	PackCount          int                        `json:"packCount"`
	CaptureCount       int                        `json:"captureCount"`
	ReceiptCount       int                        `json:"receiptCount"`
	RunEventCount      int                        `json:"runEventCount"`
	RunCheckpointCount int                        `json:"runCheckpointCount"`
	Captures           map[string]manifestCapture `json:"captures"`
	Receipts           []string                   `json:"receipts"`
	Files              map[string]string          `json:"files"`
}

var pinShape = regexp.MustCompile(`\A[0-9a-f]{64}\z`)

// TestRestoreComposedStackRoundTrip backs up the records, captures, receipts,
// journal and checkpoint produced by the T23 integrated stack through the
// real recovery.py CLI, restores into fresh private directories, and proves:
// manifest/foreign-key/provenance integrity, live-claim and capability
// invalidation, uncertain-work retention, and that no execution starts.
func TestRestoreComposedStackRoundTrip(t *testing.T) {
	recoveryPY, assetsSrc := repoPaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{}
	for _, name := range []string{"data", "artifacts", "scratch", "assets", "secrets", "backups",
		"restore-data", "restore-assets", "restore-artifacts", "scratch2"} {
		path := filepath.Join(root, name)
		mkdirPrivate(t, path)
		dirs[name] = path
	}
	for _, name := range pinnedAssets {
		body, err := os.ReadFile(filepath.Join(assetsSrc, name))
		if err != nil {
			t.Fatalf("pinned asset %s: %v", name, err)
		}
		writePrivate(t, filepath.Join(dirs["assets"], name), body)
	}
	writePrivate(t, filepath.Join(dirs["secrets"], "api.env"),
		[]byte("TYPESAFE_API_KEY=\"t25-provider-key\"\nJOBSEEK_CODEX_BRIDGE_TOKEN=\"t25-bridge\"\n"))
	writePrivate(t, filepath.Join(dirs["secrets"], "ssh_key"), []byte("t25-ssh-private-key-body-0123456789"))
	writePrivate(t, filepath.Join(dirs["secrets"], "tls_key"), []byte("t25-tls-private-key-body-0123456789"))

	// Composed data through the real T23 stack: commission, fetch x2 with
	// cross-post convergence, API POST second record, negative + reuse,
	// steer, journal, checkpoint, then the live-claim/capability/uncertain
	// arrangement the scrubber must handle.
	h := wireComposed(t, dirs["data"], dirs["artifacts"], dirs["scratch"])
	h.commission(t)
	rec := h.saveComposedRecords(t)
	checkpoint := h.pivotAndSteer(t)
	live := h.arrangeLiveState(t)
	round, err := h.db.Round(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	snap := takeSnapshot(t, h.db)
	snap.roundState, snap.roundGen = string(round.State), round.Generation
	snap.checkpointGen = checkpoint.Generation
	snap.checkpointEvidence = queryString(t, h.db,
		"SELECT evidence_ids_json FROM run_checkpoints WHERE round_id=?", h.runID)
	snap.checkpointActive = queryString(t, h.db,
		"SELECT active_claims_json FROM run_checkpoints WHERE round_id=?", h.runID)
	t.Logf("pre-backup: captures=%d observations=%d requests=%d events=%d checkpoints=%d "+
		"capabilities=%d opportunities=%d assessments=%d round=%s/gen=%d",
		snap.captures, snap.observations, len(snap.requests), snap.events, snap.checkpoints,
		snap.capabilities, snap.opportunities, snap.assessments, snap.roundState, snap.roundGen)
	if snap.captures == 0 || snap.observations == 0 || snap.opportunities != 2 || snap.assessments == 0 {
		t.Fatalf("composed data incomplete: %+v", snap)
	}
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}

	// Backup + verify through the real CLI, exactly as operations runs it.
	archive := filepath.Join(dirs["backups"], "t25-current")
	pin := runRecovery(t, recoveryPY, "backup",
		"--data-dir", dirs["data"],
		"--assets-root", dirs["assets"],
		"--artifact-root", dirs["artifacts"],
		"--out", archive,
		"--api-env", filepath.Join(dirs["secrets"], "api.env"),
		"--ssh-key", filepath.Join(dirs["secrets"], "ssh_key"),
		"--tls-key", filepath.Join(dirs["secrets"], "tls_key"),
	)
	if !pinShape.MatchString(pin) {
		t.Fatalf("backup pin shape: %q", pin)
	}
	t.Logf("backup pin: %s", pin)
	entries := map[string]bool{}
	for _, e := range mustReadDir(t, archive) {
		entries[e] = true
	}
	for _, want := range []string{"jobseek.sqlite", "assets", "manifest.json", "captures", "executor-identity.json"} {
		if !entries[want] {
			t.Fatalf("archive missing %q (%v)", want, entries)
		}
	}
	if got := runRecovery(t, recoveryPY, "verify", "--archive", archive, "--manifest-sha256", pin); got != "verified" {
		t.Fatalf("verify: %q", got)
	}

	manifest := readManifest(t, archive)
	if manifest.Format != "jobseek-current-backup-v2" {
		t.Fatalf("manifest format: %q", manifest.Format)
	}
	if manifest.CaptureCount != snap.captures || len(manifest.Captures) != snap.captures {
		t.Fatalf("manifest captures %d vs snapshot %d", manifest.CaptureCount, snap.captures)
	}
	for id, row := range snap.captureRows {
		entry, ok := manifest.Captures[id]
		if !ok || entry.SHA256 != row.sha || entry.Ref != row.ref || entry.Size != row.size {
			t.Fatalf("manifest capture %s: %+v vs %+v", id, entry, row)
		}
	}
	if manifest.ReceiptCount != len(snap.receipts) {
		t.Fatalf("manifest receipts %d vs snapshot %d", manifest.ReceiptCount, len(snap.receipts))
	}
	wantReceipts := map[string]bool{}
	for _, r := range snap.receipts {
		wantReceipts[r] = true
	}
	for _, r := range manifest.Receipts {
		if !wantReceipts[r] {
			t.Fatalf("manifest receipt %q not in snapshot", r)
		}
		delete(wantReceipts, r)
	}
	if len(wantReceipts) != 0 {
		t.Fatalf("snapshot receipts missing from manifest: %v", wantReceipts)
	}
	if manifest.RunEventCount != snap.events || manifest.RunCheckpointCount != snap.checkpoints {
		t.Fatalf("manifest journal %d/%d vs snapshot %d/%d",
			manifest.RunEventCount, manifest.RunCheckpointCount, snap.events, snap.checkpoints)
	}
	assertExecutorRecord(t, archive)

	// Restore into fresh private directories; the API is stopped (never
	// started here) and the targets are empty.
	restored := runRecovery(t, recoveryPY, "restore",
		"--archive", archive, "--manifest-sha256", pin,
		"--data-dir", dirs["restore-data"],
		"--assets-root", dirs["restore-assets"],
		"--artifact-root", dirs["restore-artifacts"],
	)
	if !strings.HasPrefix(restored, "restored;") {
		t.Fatalf("restore: %q", restored)
	}
	for _, extra := range []string{"jobseek.sqlite-wal", "jobseek.sqlite-shm"} {
		if _, err := os.Stat(filepath.Join(dirs["restore-data"], extra)); !os.IsNotExist(err) {
			t.Fatalf("restore left %s", extra)
		}
	}
	for _, name := range pinnedAssets {
		want, err := os.ReadFile(filepath.Join(assetsSrc, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dirs["restore-assets"], name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("restored asset %s mismatch: %v", name, err)
		}
	}

	assertRestoredRows(t, dirs["restore-data"], snap, h.runID, live)
	assertRestoredBehavior(t, dirs["restore-data"], dirs["restore-artifacts"], dirs["scratch2"],
		h.runID, h.gen, rec, snap)
}

func mustReadDir(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readManifest(t *testing.T, archive string) manifestFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(archive, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest manifestFile
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func assertExecutorRecord(t *testing.T, archive string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(archive, "executor-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Executors []struct {
			Backend string `json:"backend"`
		} `json:"executors"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range record.Executors {
		seen[e.Backend] = true
	}
	if !seen["generic-http"] {
		t.Fatalf("executor record missing generic-http: %s", raw)
	}
	t.Logf("executor record covers: %s", raw)
}

// assertRestoredRows opens the restored database and compares every
// scrub-sensitive row against the pre-backup snapshot: evidence identical,
// live claims and capabilities gone, uncertain work retained, run fenced.
func assertRestoredRows(t *testing.T, dataDir string, snap snapshot, runID string, live liveState) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if got := queryString(t, db, "PRAGMA integrity_check"); got != "ok" {
		t.Fatalf("restored integrity_check: %q", got)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Fatalf("restored foreign_key_check rows: %d", n)
	}

	// Immutable evidence is byte-identical in row counts and linkage.
	if n := queryInt(t, db, "SELECT COUNT(*) FROM source_captures"); n != snap.captures {
		t.Fatalf("restored captures %d vs %d", n, snap.captures)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM research_observations"); n != snap.observations {
		t.Fatalf("restored observations %d vs %d", n, snap.observations)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM run_events"); n != snap.events {
		t.Fatalf("restored events %d vs %d", n, snap.events)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM run_checkpoints"); n != snap.checkpoints {
		t.Fatalf("restored checkpoints %d vs %d", n, snap.checkpoints)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM opportunities"); n != snap.opportunities {
		t.Fatalf("restored opportunities %d vs %d", n, snap.opportunities)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM companies"); n != snap.companies {
		t.Fatalf("restored companies %d vs %d", n, snap.companies)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM jev_assessments_dynamic"); n != snap.assessments {
		t.Fatalf("restored assessments %d vs %d", n, snap.assessments)
	}
	restoredReceipts := queryStrings(t, db,
		"SELECT receipt_ref FROM research_observations WHERE receipt_ref IS NOT NULL ORDER BY id")
	if fmt.Sprint(restoredReceipts) != fmt.Sprint(snap.receipts) {
		t.Fatalf("restored receipts %v vs %v", restoredReceipts, snap.receipts)
	}

	// The live claim is reconcilable uncertain with its lease cleared; the
	// settled uncertain request and every other request keep their state.
	if n := queryInt(t, db, "SELECT COUNT(*) FROM research_requests WHERE state='claimed'"); n != 0 {
		t.Fatalf("restored live claims: %d", n)
	}
	liveID := ""
	for id, fp := range snap.requestFPs {
		if fp == live.liveFP {
			liveID = id
		}
	}
	if liveID == "" {
		t.Fatal("live-claim request missing from snapshot")
	}
	got := queryPairs(t, db, "SELECT 'state',state FROM research_requests WHERE id=? "+
		"UNION ALL SELECT 'owner',COALESCE(lease_owner,'') FROM research_requests WHERE id=? "+
		"UNION ALL SELECT 'gen',COALESCE(lease_generation,'') FROM research_requests WHERE id=? "+
		"UNION ALL SELECT 'until',COALESCE(lease_until,'') FROM research_requests WHERE id=?",
		liveID, liveID, liveID, liveID)
	if got["state"] != store.ResearchStateUncertain || got["owner"] != "" || got["gen"] != "" || got["until"] != "" {
		t.Fatalf("live claim after restore: %+v", got)
	}
	uncID := ""
	for id, fp := range snap.requestFPs {
		if fp == live.uncFP {
			uncID = id
		}
	}
	if uncID == "" {
		t.Fatal("uncertain request missing from snapshot")
	}
	if state := queryString(t, db, "SELECT state FROM research_requests WHERE id=?", uncID); state != store.ResearchStateUncertain {
		t.Fatalf("uncertain request after restore: %q", state)
	}
	if outcome := queryString(t, db, "SELECT outcome FROM research_observations WHERE id=?", live.uncObs); outcome != store.ObservationUncertain {
		t.Fatalf("uncertain observation after restore: %q", outcome)
	}
	if ref := queryString(t, db, "SELECT receipt_ref FROM research_observations WHERE id=?", live.uncObs); ref != "t25-uncertain-rcpt-1" {
		t.Fatalf("uncertain observation receipt after restore: %q", ref)
	}
	for id, state := range snap.requests {
		if id == liveID {
			continue
		}
		if got := queryString(t, db, "SELECT state FROM research_requests WHERE id=?", id); got != state {
			t.Fatalf("request %s: %q vs %q", id, got, state)
		}
	}

	// Checkpoint keeps evidence but no active claims, and its generation is
	// now stale against the bumped round generation: resumes fence.
	if active := queryString(t, db, "SELECT active_claims_json FROM run_checkpoints WHERE round_id=?", runID); active != "[]" {
		t.Fatalf("restored checkpoint active claims: %q", active)
	}
	if evidence := queryString(t, db, "SELECT evidence_ids_json FROM run_checkpoints WHERE round_id=?", runID); evidence != snap.checkpointEvidence {
		t.Fatalf("restored checkpoint evidence: %q vs %q", evidence, snap.checkpointEvidence)
	}
	restoredGen := queryInt(t, db, "SELECT generation FROM run_checkpoints WHERE round_id=?", runID)
	if int64(restoredGen) != snap.checkpointGen {
		t.Fatalf("restored checkpoint generation %d vs %d", restoredGen, snap.checkpointGen)
	}

	// The active run is failed with a bumped generation; the dispatched
	// attempt is uncertain; capabilities are gone.
	round, err := db.Round(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if round.State != store.RoundFailed {
		t.Fatalf("restored run state: %q", round.State)
	}
	if round.Generation != snap.roundGen+1 {
		t.Fatalf("restored run generation %d vs %d+1", round.Generation, snap.roundGen)
	}
	if round.StopReason != "restored_inactive" || !round.ReconciliationRequired {
		t.Fatalf("restored run fence: %+v", round)
	}
	if state := queryString(t, db, "SELECT state FROM round_attempts WHERE id=?", live.attemptID); state != "uncertain" {
		t.Fatalf("dispatched attempt after restore: %q", state)
	}
	for id, state := range snap.attempts {
		if id == live.attemptID {
			continue
		}
		if got := queryString(t, db, "SELECT state FROM round_attempts WHERE id=?", id); got != state {
			t.Fatalf("attempt %s: %q vs %q", id, got, state)
		}
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM round_tool_capabilities"); n != 0 {
		t.Fatalf("restored capabilities: %d", n)
	}
	if _, err := db.VerifyRoundToolCapability(ctx, live.capability, runID); err == nil {
		t.Fatal("pre-backup capability still verifies after restore")
	}

	// Nothing runnable remains anywhere.
	if n := queryInt(t, db, "SELECT COUNT(*) FROM jobs WHERE state IN ('queued','running')"); n != 0 {
		t.Fatalf("restored runnable jobs: %d", n)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused')"); n != 0 {
		t.Fatalf("restored runnable rounds: %d", n)
	}
	t.Logf("restored rows: run=%s/gen=%d claims=0 capabilities=0 uncertain-requests=%d",
		round.State, round.Generation,
		queryInt(t, db, "SELECT COUNT(*) FROM research_requests WHERE state='uncertain'"))
}

// assertRestoredBehavior rewires the real stack over the restored database
// and artifacts and proves provenance reads, idle stability, and that no
// execution can start: stale-generation dispatch and resume are rejected.
func assertRestoredBehavior(t *testing.T, dataDir, artifactRoot, scratchRoot, runID string, staleGen int64, rec records, snap snapshot) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	provider := &scriptedProvider{model: "fixture-jev-1", verdicts: map[string]string{}}
	stack, err := researchwire.Wire(db, researchwire.Config{
		ArtifactRoot: artifactRoot, ScratchRoot: scratchRoot,
		AgentID: researchwire.DefaultAgentID, JevProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Provenance: every captured byte string reopens with identical bytes,
	// and the cross-post sightings still pin the same record.
	for sha, want := range rec.bodies {
		_, rc, err := stack.Captures.OpenCapture(ctx, sha)
		if err != nil {
			t.Fatalf("OpenCapture %s: %v", sha[:12], err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want {
			t.Fatalf("capture %s bytes changed across restore", sha[:12])
		}
	}
	sighted := map[string]bool{}
	if err := db.Read(ctx, func(r store.Reader) error {
		rows, err := store.ListRecordSightingsByOpportunity(ctx, r, rec.opp1)
		if err != nil {
			return err
		}
		for _, row := range rows {
			sighted[row.ContentSHA256] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(sighted) != 2 || !sighted[rec.cap1] || !sighted[rec.cap2] {
		t.Fatalf("restored sightings: %v", sighted)
	}

	// Idle stability: opening, wiring and reading start nothing.
	before := takeSnapshot(t, db)
	claims, err := stack.Memory.LiveClaims(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("live claims on restored stack: %+v", claims)
	}
	expired, err := stack.ExpireLeases(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if expired != 0 {
		t.Fatalf("expiry sweep settled %d leases on a restored database", expired)
	}
	events, _, err := stack.Journal.List(ctx, runID, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != snap.events {
		t.Fatalf("journal after reopen %d vs %d", len(events), snap.events)
	}
	checkpoint, err := stack.Supervisor.Checkpoint(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.EvidenceIDs) == 0 {
		t.Fatalf("restored checkpoint lost evidence: %+v", checkpoint)
	}
	after := takeSnapshot(t, db)
	if fmt.Sprint(before.requests) != fmt.Sprint(after.requests) ||
		fmt.Sprint(before.attempts) != fmt.Sprint(after.attempts) ||
		before.events != after.events || before.observations != after.observations {
		t.Fatal("reopening the restored stack mutated research state")
	}

	// No execution starts: the stale generation is fenced and the failed
	// run refuses resume.
	if _, err := stack.Supervisor.Dispatch(ctx, rounds.DispatchInput{
		RunID: runID, Generation: staleGen, Kind: researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationFetch, Backend: "generic-http",
			Method: "GET", URLOrQuery: "https://example.invalid/after-restore",
		},
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 8, DeadlineMs: 5000},
		IdempotencyKey: "t25-after-restore",
	}); err == nil {
		t.Fatal("stale-generation dispatch succeeded on the restored run")
	} else {
		t.Logf("stale dispatch rejected: %v", err)
	}
	if _, err := stack.Supervisor.Resume(ctx, researchwire.OwnerActor(), runID); err == nil {
		t.Fatal("resume succeeded on the restored failed run")
	} else {
		t.Logf("resume rejected: %v", err)
	}
	final := takeSnapshot(t, db)
	if fmt.Sprint(before.requests) != fmt.Sprint(final.requests) ||
		fmt.Sprint(before.attempts) != fmt.Sprint(final.attempts) ||
		before.events != final.events || before.observations != final.observations {
		t.Fatal("rejected dispatch/resume mutated restored state")
	}
}
