package codexservice

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchmemory"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type fakeResearchAuthority struct {
	usage researchcontract.UsageLedger
}

func (f *fakeResearchAuthority) Check(context.Context, researchcontract.CheckInput) error {
	return nil
}
func (f *fakeResearchAuthority) Reserve(_ context.Context, runID, op, key, hash string) (researchcontract.Reservation, error) {
	return researchcontract.Reservation{ID: "res-" + key, AttemptID: "att-" + key,
		Operation: op, RunID: runID, IdempotencyKey: key, PayloadHash: hash}, nil
}
func (f *fakeResearchAuthority) Release(context.Context, string) error { return nil }
func (f *fakeResearchAuthority) Usage(context.Context, string) (researchcontract.UsageLedger, error) {
	return f.usage, nil
}
func (f *fakeResearchAuthority) ReconciliationFor(context.Context, string) (researchcontract.Reconciliation, error) {
	return researchcontract.Reconciliation{}, researchcontract.NewError(researchcontract.OutcomeNotFound,
		"attempt", "no reconciliation recorded")
}

type fakeAssessor struct {
	seen researchcontract.AssessInput
	out  researchcontract.Assessment
	err  error
}

func (f *fakeAssessor) Assess(_ context.Context, in researchcontract.AssessInput) (researchcontract.Assessment, error) {
	f.seen = in
	return f.out, f.err
}

type fakeMatcher struct {
	seen researchcontract.MatchInput
	out  researchcontract.MatchOutput
	err  error
}

func (f *fakeMatcher) Match(_ context.Context, in researchcontract.MatchInput) (researchcontract.MatchOutput, error) {
	f.seen = in
	return f.out, f.err
}

type fakeSaver struct {
	seen researchcontract.SaveBatch
	out  researchcontract.SaveOutput
	err  error
}

func (f *fakeSaver) Save(_ context.Context, batch researchcontract.SaveBatch) (researchcontract.SaveOutput, error) {
	f.seen = batch
	return f.out, f.err
}

type researchHarness struct {
	svc        *Service
	db         *store.Store
	runID      string
	agent      store.Actor
	attemptID  string
	capability string
	generation int64
	assessor   *fakeAssessor
	matcher    *fakeMatcher
	saver      *fakeSaver
	dispatch   ResearchDispatch
	dispatched *rounds.DispatchInput
}

func testResearchHarness(t *testing.T) *researchHarness {
	t.Helper()
	ctx := context.Background()
	svc, db := testService(t, testConfig())
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	artifacts, err := researchmemory.OpenArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auth, err := rounds.NewAuthority(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.NewRunEventJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	sup, err := rounds.NewSupervisor(rounds.SupervisorDeps{DB: db, Authority: auth, Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	commissioned, err := sup.Commission(ctx, rounds.CommissionInput{Actor: owner,
		BriefText: "Find backend roles.", AgentID: agent.ID,
		RubricVersion: "criteria-test-v1", RubricSource: "test", IdempotencyKey: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	cost, ok := store.RoundOperationCost(store.RoundCodexTurn)
	if !ok {
		t.Fatal("codex turn cost missing")
	}
	attempt, _, err := db.ReserveRoundAttempt(ctx, agent, commissioned.RunID, store.RoundAttemptInput{
		RequestKey: "turn-cap", Operation: store.RoundCodexTurn,
		ResourceID: store.ResearchAuthorityResource, Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, commissioned.RunID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, commissioned.RunID, attempt.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := &researchHarness{svc: svc, db: db, runID: commissioned.RunID, agent: agent,
		attemptID: attempt.ID, capability: capability, generation: commissioned.Generation,
		assessor: &fakeAssessor{}, matcher: &fakeMatcher{}, saver: &fakeSaver{}}
	h.dispatch = func(_ context.Context, in rounds.DispatchInput) (rounds.DispatchOutput, error) {
		h.dispatched = &in
		return rounds.DispatchOutput{AttemptID: "dispatch-1", Outcome: researchcontract.OutcomeOK,
			ObservationID: "obs-1", CaptureID: "cap-1",
			Receipt: researchcontract.ExecutionReceipt{ID: "rc-1", Status: "ok"},
			Usage:   researchcontract.ExecuteUsage{Requests: 2, Bytes: 512, Redirects: 1}}, nil
	}
	fakeAuth := &fakeResearchAuthority{}
	svc.SetResearchTools(&ResearchToolchain{
		NewMemory: func(actor store.Actor) (*researchmemory.Memory, error) {
			return researchmemory.NewMemory(db, actor, artifacts, fakeAuth, nil)
		},
		Captures:  mustCaptures(t, db, artifacts),
		Authority: fakeAuth,
		Assessor:  h.assessor,
		Matcher:   h.matcher,
		NewSaver: func(store.Actor) (researchcontract.RecordSaver, error) {
			return h.saver, nil
		},
		Dispatch: h.dispatch,
		NoteSaved: func(context.Context, string, string, []string) error {
			return nil
		},
		OwnerBrief: OwnerBriefFunc(func(context.Context) (OwnerBrief, error) {
			return OwnerBrief{ProfileVersion: 3, RubricVersion: "criteria-v3-test",
				Source:      "test-brief-store",
				Facts:       []BriefFact{{Key: "preferredLocation", Value: "Amsterdam"}},
				Preferences: []BriefFact{{Key: "backend-platform", Value: "require: Backend"}}}, nil
		}),
		Records: StoreRecordReader{DB: db},
	})
	return h
}

func mustCaptures(t *testing.T, db *store.Store, artifacts *researchmemory.ArtifactStore) *researchmemory.Captures {
	t.Helper()
	caps, err := researchmemory.NewCaptures(db, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	return caps
}

func (h *researchHarness) call(t *testing.T, tool string, args map[string]any) map[string]any {
	t.Helper()
	session, _ := sdkSession(t, h.svc)
	args["capability"] = h.capability
	args["runId"] = h.runID
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("%s: %+v %v", tool, result, err)
	}
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s: unstructured %+v", tool, result.StructuredContent)
	}
	return payload
}

func TestResearchToolManifest(t *testing.T) {
	h := testResearchHarness(t)
	session, _ := sdkSession(t, h.svc)
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 12 {
		t.Fatalf("tool count: %d", len(result.Tools))
	}
	seen := map[string]bool{}
	for _, tool := range result.Tools {
		seen[tool.Name] = true
	}
	for _, name := range ResearchToolNames() {
		if !seen[name] {
			t.Fatalf("research tool %q not exposed", name)
		}
	}
	for _, tool := range result.Tools {
		if !seen[tool.Name] || len(tool.Name) == 0 {
			t.Fatal("empty tool name")
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok || schema == nil {
			t.Fatalf("%s: missing input schema", tool.Name)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: schema has no properties", tool.Name)
		}
		isResearch := false
		for _, name := range ResearchToolNames() {
			if tool.Name == name {
				isResearch = true
			}
		}
		if !isResearch {
			continue
		}
		if tool.Description == "" {
			t.Fatalf("%s: missing description", tool.Name)
		}
		if _, ok := props["runId"]; !ok {
			t.Fatalf("%s: schema lacks runId", tool.Name)
		}
		if _, ok := props["capability"]; !ok {
			t.Fatalf("%s: schema lacks capability", tool.Name)
		}
	}
}

func TestResearchToolsAbsentUntilWired(t *testing.T) {
	svc, _ := testService(t, testConfig())
	session, _ := sdkSession(t, svc)
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 5 {
		t.Fatalf("unwired tool count: %d", len(result.Tools))
	}
	if err := svc.CheckRound(context.Background(), "research_run"); err != ErrUnavailable {
		t.Fatalf("unwired research run: %v", err)
	}
}

func TestResearchMemoryClaimReleaseRoundTrip(t *testing.T) {
	h := testResearchHarness(t)
	descriptor := map[string]any{"operation": "fetch", "backend": "test",
		"url_or_query": "https://example.invalid/jobs"}
	claimed := h.call(t, "research_memory", map[string]any{"op": "claim",
		"generation": h.generation, "idempotencyKey": "claim-1", "requestDescriptor": descriptor})
	if claimed["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("claim: %+v", claimed)
	}
	lease, ok := claimed["lease"].(map[string]any)
	if !ok || lease["owner"] != h.attemptID {
		t.Fatalf("lease owner defaults to the live attempt: %+v", claimed)
	}
	released := h.call(t, "research_memory", map[string]any{"op": "release",
		"owner": h.attemptID, "leaseId": lease["leaseId"], "generation": h.generation})
	if released["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("release: %+v", released)
	}
}

func TestResearchMemoryStaleGeneration(t *testing.T) {
	h := testResearchHarness(t)
	out := h.call(t, "research_memory", map[string]any{"op": "checkpoint",
		"generation": h.generation + 100})
	if out["outcome"] != string(researchcontract.OutcomeStale) || out["generation"] == nil {
		t.Fatalf("stale generation: %+v", out)
	}
}

func TestResearchToolRejectsBadCapability(t *testing.T) {
	h := testResearchHarness(t)
	session, _ := sdkSession(t, h.svc)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "research_memory",
		Arguments: map[string]any{"capability": strings.Repeat("x", 64), "runId": h.runID, "op": "checkpoint"},
	})
	if err == nil && !result.IsError {
		t.Fatalf("bad capability accepted: %+v", result)
	}
}

func TestContextReadComposesOwnerBrief(t *testing.T) {
	h := testResearchHarness(t)
	out := h.call(t, "context_read", map[string]any{"brief": true, "profile": true})
	if out["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("context read: %+v", out)
	}
	current, ok := out["currentBrief"].(map[string]any)
	if !ok || current["rubricVersion"] != "criteria-v3-test" || current["source"] != "test-brief-store" {
		t.Fatalf("current brief composition: %+v", out)
	}
	if _, ok := out["runLimits"]; !ok {
		t.Fatalf("run limits missing: %+v", out)
	}
	// The run binding pair stays untouched by current-brief composition.
	binding, ok := out["briefVersion"].(map[string]any)
	if !ok || binding["rubricVersion"] != "criteria-test-v1" {
		t.Fatalf("run binding pair: %+v", out)
	}
}

func TestContextReadRecordSubject(t *testing.T) {
	h := testResearchHarness(t)
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := h.db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	out := h.call(t, "context_read", map[string]any{
		"subject": map[string]any{"kind": "company", "id": company.ID}})
	if out["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("company subject: %+v", out)
	}
	subject, ok := out["subject"].(map[string]any)
	if !ok || subject["name"] != "Acme" || subject["revision"] == nil {
		t.Fatalf("company summary: %+v", out)
	}
	missing := h.call(t, "context_read", map[string]any{
		"subject": map[string]any{"kind": "opportunity", "id": "opp-missing"}})
	if missing["outcome"] != string(researchcontract.OutcomeNotFound) {
		t.Fatalf("missing subject: %+v", missing)
	}
}

func TestEvidenceCaptureUnknownReceipt(t *testing.T) {
	h := testResearchHarness(t)
	out := h.call(t, "evidence_capture", map[string]any{"receipt": "receipt-missing"})
	if out["outcome"] != string(researchcontract.OutcomeNotFound) {
		t.Fatalf("unknown receipt: %+v", out)
	}
}

func TestJevAssessMapsContract(t *testing.T) {
	h := testResearchHarness(t)
	h.assessor.out = researchcontract.Assessment{ID: "assess-1",
		Results: []researchcontract.AssessAnswer{{QuestionID: "q1", AnswerID: "a1"}},
		Model:   "jev-test", ModelVersion: "v1", ReuseKey: "reuse-1"}
	out := h.call(t, "jev_assess", map[string]any{
		"generation": h.generation, "purpose": "screening",
		"questions": []any{map[string]any{"id": "q1", "text": "Remote?",
			"alternatives":   []any{map[string]any{"id": "a1", "label": "Yes"}},
			"abstainAllowed": true}},
		"profileVersion": 3, "rubricVersion": "criteria-test-v1",
		"sourceRefs":     []any{map[string]any{"captureId": "cap-1", "spanStart": 0, "spanEnd": 4}},
		"idempotencyKey": "jev-1"})
	if out["outcome"] != string(researchcontract.OutcomeOK) || out["assessmentId"] != "assess-1" ||
		out["reuseKey"] != "reuse-1" {
		t.Fatalf("assess: %+v", out)
	}
	if h.assessor.seen.RunID != h.runID || h.assessor.seen.Generation != h.generation ||
		h.assessor.seen.IdempotencyKey != "jev-1" {
		t.Fatalf("assessor input: %+v", h.assessor.seen)
	}
}

func TestOpportunityMatchAdvice(t *testing.T) {
	h := testResearchHarness(t)
	h.matcher.out = researchcontract.MatchOutput{Outcome: researchcontract.OutcomeOK,
		PossibleMatches: []researchcontract.PossibleMatch{{RecordID: "opp-1"}}}
	out := h.call(t, "opportunity_match", map[string]any{
		"attributes":   map[string]any{"employer": "Acme", "title": "Engineer"},
		"evidenceRefs": []any{map[string]any{"captureId": "cap-1", "spanStart": 0, "spanEnd": 4}}})
	if out["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("match: %+v", out)
	}
	if h.matcher.seen.RunID != h.runID || h.matcher.seen.Generation != h.generation {
		t.Fatalf("matcher input: %+v", h.matcher.seen)
	}
}

func TestRecordsSaveBatch(t *testing.T) {
	h := testResearchHarness(t)
	h.saver.out = researchcontract.SaveOutput{Outcome: researchcontract.OutcomeOK,
		Saved: []researchcontract.SavedRecord{{RecordID: "opp-1", Revision: 1, AuditID: "audit-1"}}}
	out := h.call(t, "records_save", map[string]any{
		"batch": []any{map[string]any{"op": "create_opportunity",
			"fields": map[string]any{"title": "Engineer"}, "evidenceLinks": []any{}}},
		"idempotencyKey": "save-1"})
	if out["outcome"] != string(researchcontract.OutcomeOK) {
		t.Fatalf("save: %+v", out)
	}
	if h.saver.seen.RunID != h.runID || h.saver.seen.Generation != h.generation ||
		h.saver.seen.IdempotencyKey != "save-1" || len(h.saver.seen.Items) != 1 {
		t.Fatalf("saver input: %+v", h.saver.seen)
	}
}

func TestResearchExecuteDispatch(t *testing.T) {
	h := testResearchHarness(t)
	out := h.call(t, "research_execute", map[string]any{"kind": "fetch",
		"request": map[string]any{"operation": "fetch", "backend": "test",
			"url_or_query": "https://example.invalid/jobs"},
		"bounds":         map[string]any{"maxBytes": 1024, "maxRequests": 2, "deadlineMs": 5000},
		"idempotencyKey": "exec-1"})
	if out["outcome"] != string(researchcontract.OutcomeOK) || out["observationId"] != "obs-1" ||
		out["captureId"] != "cap-1" || out["attemptId"] != "dispatch-1" {
		t.Fatalf("execute: %+v", out)
	}
	if h.dispatched == nil || h.dispatched.RunID != h.runID || h.dispatched.Generation != h.generation ||
		h.dispatched.Kind != researchcontract.ExecuteFetch {
		t.Fatalf("dispatch input: %+v", h.dispatched)
	}
	fullBounds := map[string]any{"maxBytes": 1024, "maxRequests": 2, "deadlineMs": 5000}
	fullRequest := func() map[string]any {
		return map[string]any{"operation": "fetch", "backend": "test", "url_or_query": "https://example.invalid/"}
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{{"bad kind", map[string]any{"kind": "crawl", "request": fullRequest(),
		"bounds": fullBounds, "idempotencyKey": "exec-bad"}},
		{"secret param", func() map[string]any {
			request := fullRequest()
			request["params"] = []any{map[string]any{"name": "api_key", "value": "x"}}
			return map[string]any{"kind": "fetch", "request": request,
				"bounds": fullBounds, "idempotencyKey": "exec-secret"}
		}()},
		{"bad bounds", map[string]any{"kind": "fetch", "request": fullRequest(),
			"bounds":         map[string]any{"maxBytes": -1, "maxRequests": 2, "deadlineMs": 5000},
			"idempotencyKey": "exec-bounds"}}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.call(t, "research_execute", tc.args); got["outcome"] != string(researchcontract.OutcomeInvalid) {
				t.Fatalf("%s: %+v", tc.name, got)
			}
		})
	}
	// A missing idempotency key never reaches the handler: the inferred
	// schema requires it, so the protocol rejects the call.
	session, _ := sdkSession(t, h.svc)
	missing, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "research_execute",
		Arguments: map[string]any{"capability": h.capability, "runId": h.runID,
			"kind": "fetch", "request": fullRequest(), "bounds": fullBounds}})
	if err != nil {
		t.Fatal(err)
	}
	if !missing.IsError {
		t.Fatalf("missing key accepted: %+v", missing)
	}
	// Direct callers still get the in-band contract outcome.
	direct, err := h.svc.researchExecuteTool(context.Background(), researchExecuteToolArgs{
		Capability: h.capability, RunID: h.runID,
		Kind:    researchcontract.ExecuteFetch,
		Request: researchcontract.RequestDescriptor{Operation: "fetch", Backend: "test", URLOrQuery: "https://example.invalid/"},
		Bounds:  researchcontract.Bounds{MaxBytes: 1, MaxRequests: 1, DeadlineMs: 1},
	})
	if err != nil || direct["outcome"] != string(researchcontract.OutcomeInvalid) {
		t.Fatalf("direct missing key: %+v %v", direct, err)
	}
}

func TestResearchReadinessGates(t *testing.T) {
	h := testResearchHarness(t)
	f := installRuntime(t, h.svc)
	ctx := boundedContext(t)
	if err := h.svc.CheckRound(ctx, "research_run"); err != nil {
		t.Fatalf("wired research run unavailable: %v", err)
	}
	if f.count("thread/start") != 0 || f.count("turn/start") != 0 {
		t.Fatal("readiness dispatched a model turn")
	}
	bare, _ := testService(t, testConfig())
	installRuntime(t, bare)
	if err := bare.CheckRound(ctx, "research_run"); err != ErrUnavailable {
		t.Fatalf("unwired research run with live runtime: %v", err)
	}
	offline, _ := testService(t, testConfig())
	offline.SetResearchTools(h.svc.researchTools())
	if err := offline.CheckRound(ctx, "research_run"); err != ErrUnavailable {
		t.Fatalf("wired research run without runtime: %v", err)
	}
	partial, _ := testService(t, testConfig())
	installRuntime(t, partial)
	broken := *h.svc.researchTools()
	broken.Dispatch = nil
	partial.SetResearchTools(&broken)
	if err := partial.CheckRound(ctx, "research_run"); err != ErrUnavailable {
		t.Fatalf("partial toolchain stayed available: %v", err)
	}
}

func TestOwnerBriefResolution(t *testing.T) {
	h := testResearchHarness(t)
	ctx := context.Background()
	first, err := CurrentOwnerBrief(ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.RubricVersion, "criteria-v") || first.Source == "" || len(first.Facts) == 0 {
		t.Fatalf("brief resolution: %+v", first)
	}
	second, err := CurrentOwnerBrief(ctx, h.db)
	if err != nil || second.RubricVersion != first.RubricVersion || second.Source != first.Source {
		t.Fatalf("brief stability: %+v %+v", first, second)
	}
	profile, rubric, err := RunBriefs{DB: h.db}.CurrentBrief(ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if profile != first.ProfileVersion || rubric != "criteria-test-v1" {
		t.Fatalf("run binding pair: %d %s", profile, rubric)
	}
	_, _, err = RunBriefs{DB: h.db}.CurrentBrief(ctx, "run-missing")
	if err == nil {
		t.Fatal("missing run brief resolved")
	}
}
