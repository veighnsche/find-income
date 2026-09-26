package researchservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var (
	testOwner = store.Actor{Kind: "administrator", ID: "owner"}
	testAgent = store.Actor{Kind: "agent", ID: "codex-runner"}
)

type fakeExecutor struct {
	mu  sync.Mutex
	run func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error)
}

func (f *fakeExecutor) Execute(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return researchcontract.ExecuteOutput{Outcome: researchcontract.OutcomeOK,
			ObservationID: "obs-" + in.IdempotencyKey, CaptureID: "cap-" + in.IdempotencyKey,
			Receipt: researchcontract.ExecutionReceipt{ID: "rcpt-" + in.IdempotencyKey,
				Operation: "fetch", Status: "ok", BytesIn: 100, BytesOut: 800},
			Usage: researchcontract.ExecuteUsage{Requests: 1, Bytes: 900}}, nil
	}
	return run(ctx, in)
}

type fakeTurns struct {
	mu  sync.Mutex
	run func(ctx context.Context, agent store.Actor, in rounds.RunnerTurnInput) (rounds.RunnerTurnOutput, error)
}

func (f *fakeTurns) RunTurn(ctx context.Context, agent store.Actor, in rounds.RunnerTurnInput) (rounds.RunnerTurnOutput, error) {
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	if run == nil {
		thread := in.ThreadID
		if thread == "" {
			thread = "thread-" + in.RequestKey
		}
		return rounds.RunnerTurnOutput{ThreadID: thread, TurnID: "turn-" + in.RequestKey,
			Status: "completed", NextWork: []string{"follow-" + in.RequestKey},
			Evidence: json.RawMessage(`{"status":"completed"}`)}, nil
	}
	return run(ctx, agent, in)
}

type fakeConversation struct {
	mu  sync.Mutex
	run func(ctx context.Context, roundID, attemptID, text string) (string, error)
}

func (f *fakeConversation) SteerAttempt(ctx context.Context, roundID, attemptID, text string) (string, error) {
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return "turn-live", nil
	}
	return run(ctx, roundID, attemptID, text)
}

type fakeCaptures struct {
	mu     sync.Mutex
	open   func(captureID string) (researchcontract.Capture, error)
	closed *bool
}

func (f *fakeCaptures) ResolveReceipt(context.Context, string) (researchcontract.ExecutionReceipt, error) {
	return researchcontract.ExecutionReceipt{}, researchcontract.NewError(researchcontract.OutcomeNotFound,
		"receipt", "no receipts in this fake")
}

func (f *fakeCaptures) OpenCapture(_ context.Context, captureID string) (researchcontract.Capture, io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.open == nil {
		return researchcontract.Capture{}, nil, researchcontract.NewError(researchcontract.OutcomeNotFound,
			"captureId", "unknown capture "+captureID)
	}
	capture, err := f.open(captureID)
	if err != nil {
		return researchcontract.Capture{}, nil, err
	}
	closed := f.closed
	return capture, &closeTracker{closed: closed}, nil
}

type closeTracker struct {
	closed *bool
}

func (c *closeTracker) Read(p []byte) (int, error) { return 0, io.EOF }

func (c *closeTracker) Close() error {
	if c.closed != nil {
		*c.closed = true
	}
	return nil
}

// gateTransport holds the background session until the test releases it,
// so commissioned runs stay active deterministically for the whole test.
// Cleanup releases the gate; the session then finishes on its own.
type gateTransport struct {
	proceed chan struct{}
}

func (g *gateTransport) Run(ctx context.Context, _ musecode.SessionSpec, _ musecode.SessionInput, _ musecode.Cursor, sink musecode.EventSink) error {
	select {
	case <-g.proceed:
	case <-ctx.Done():
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

type adapterHarness struct {
	db     *store.Store
	sup    *rounds.Supervisor
	exec   *fakeExecutor
	turns  *fakeTurns
	conv   *fakeConversation
	caps   *fakeCaptures
	svc    *Service
	closed bool
}

func newAdapterHarness(t *testing.T) *adapterHarness {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	auth, err := rounds.NewAuthority(db, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.NewRunEventJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	h := &adapterHarness{db: db, exec: &fakeExecutor{}, turns: &fakeTurns{},
		conv: &fakeConversation{}, caps: &fakeCaptures{}}
	h.caps.closed = &h.closed
	sup, err := rounds.NewSupervisor(rounds.SupervisorDeps{DB: db, Authority: auth,
		Executor: h.exec, Journal: journal, Turns: h.turns, Conversation: h.conv,
		Config: rounds.SupervisorConfig{MaxConcurrent: 2}})
	if err != nil {
		t.Fatal(err)
	}
	h.sup = sup
	t.Cleanup(sup.Close)
	gate := &gateTransport{proceed: make(chan struct{})}
	t.Cleanup(func() { close(gate.proceed) })
	museSvc, err := musewire.NewService(musewire.Deps{
		Facts: fixtureFacts(), Bounds: musecode.DefaultBounds(), Transport: gate,
		Cursors: musewire.StoreCursors{DB: db}, DB: db, Actor: testOwner,
		Executor: h.exec, Captures: h.caps, Assessor: &jevassess.Handler{},
		Workspaces: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var counter int
	svc, err := New(Deps{DB: db, Supervisor: sup, Journal: journal, Captures: h.caps,
		AgentID: testAgent.ID, Muse: museSvc, NewKey: func(prefix string) string {
			counter++
			return prefix + "-auto"
		}})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

func (h *adapterHarness) commission(t *testing.T, brief, key string, allow *generated.ResearchAllowance) httpapi.CommissionResearchOutput {
	t.Helper()
	out, err := h.svc.CommissionResearch(context.Background(), httpapi.CommissionResearchInput{
		Actor: testOwner, BriefText: brief, Allowance: allow, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func contractCode(t *testing.T, err error) researchcontract.Outcome {
	t.Helper()
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("not a contract error: %v", err)
	}
	return cerr.Code
}

func TestCommissionResolvesBriefAndRecordsSource(t *testing.T) {
	h := newAdapterHarness(t)
	out := h.commission(t, "Find backend roles near Amsterdam.", "run-brief", nil)
	if !out.Created || out.View.RunId == "" {
		t.Fatalf("commission: %+v", out)
	}
	if out.View.State != generated.ResearchRunViewStateRunning {
		t.Fatalf("run state: %s", out.View.State)
	}
	if !strings.HasPrefix(out.View.BriefVersion.RubricVersion, "criteria-v") ||
		out.View.BriefVersion.ProfileVersion < 1 {
		t.Fatalf("brief versions: %+v", out.View.BriefVersion)
	}
	if out.View.Allowance.MaxActions != 60 || out.View.Allowance.MaxJev != 12 ||
		out.View.Allowance.MaxTurns != 8 || out.View.Allowance.MaxConcurrent != 2 {
		t.Fatalf("canary allowance: %+v", out.View.Allowance)
	}
	if out.View.Usage.Enforced.MaxActions != 60 || out.View.Usage.Unknown {
		t.Fatalf("usage: %+v", out.View.Usage)
	}
	// The commission record journals the rubric source for later audits.
	events, _, err := h.svc.journal.List(context.Background(), out.View.RunId, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.ID != "run."+out.View.RunId+".commissioned" {
			continue
		}
		var rec struct {
			RubricVersion string `json:"rubricVersion"`
			RubricSource  string `json:"rubricSource"`
		}
		if err := json.Unmarshal(e.Payload, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.RubricVersion != out.View.BriefVersion.RubricVersion ||
			!strings.HasPrefix(rec.RubricSource, "preferences_versions:current:v") {
			t.Fatalf("commission record: %+v", rec)
		}
		return
	}
	t.Fatal("commission record not journaled")
}

func TestCommissionReplayAndAllowance(t *testing.T) {
	h := newAdapterHarness(t)
	first := h.commission(t, "Replayed run.", "run-replay", &generated.ResearchAllowance{
		TimeMs: 60000, MaxActions: 5, MaxJev: 1, MaxTurns: 2, MaxConcurrent: 1})
	if !first.Created || first.View.Allowance.MaxActions != 5 || first.View.Allowance.MaxConcurrent != 1 {
		t.Fatalf("custom allowance: %+v", first.View.Allowance)
	}
	replay, err := h.svc.CommissionResearch(context.Background(), httpapi.CommissionResearchInput{
		Actor: testOwner, BriefText: "Replayed run.", IdempotencyKey: "run-replay",
		Allowance: &generated.ResearchAllowance{TimeMs: 60000, MaxActions: 5,
			MaxJev: 1, MaxTurns: 2, MaxConcurrent: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Created || replay.View.RunId != first.View.RunId {
		t.Fatalf("replay: %+v", replay)
	}
}

func TestRunViewAggregatesDurableState(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Aggregated run.", "run-view", nil).View.RunId
	company, _, err := h.db.CreateCompany(ctx, testOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	cp, err := h.sup.Checkpoint(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	cp.SavedRecordIDs = []string{company.ID, "record-dangling"}
	cp.UnresolvedAttempts = []researchcontract.UnresolvedAttempt{{AttemptID: "att-x", Reason: "reconcile me"}}
	cp.NextWork = []string{"verify salary band"}
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SaveRunCheckpoint(ctx, db, run, cp)
	}); err != nil {
		t.Fatal(err)
	}
	for _, note := range []store.ResearchNoteInput{
		{RoundID: run, BriefProfileVersion: cp.ProfileVersion, BriefRubricVersion: cp.RubricVersion,
			Intent: "Backend roles", OutstandingJSON: `["salary"]`},
		{RoundID: run, BriefProfileVersion: cp.ProfileVersion, BriefRubricVersion: cp.RubricVersion,
			Intent: "Cross-post check", Conclusion: "same opening"},
	} {
		if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
			_, err := store.InsertResearchNote(ctx, db, testAgent, note)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := h.svc.ResearchRun(ctx, testOwner, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.SavedIds) != 2 || view.UnresolvedCount != 1 {
		t.Fatalf("saves/unresolved: %+v", view)
	}
	if len(view.Investigations) != 2 ||
		view.Investigations[0].Status != generated.ResearchInvestigationStatusActive ||
		view.Investigations[1].Status != generated.ResearchInvestigationStatusConcluded {
		t.Fatalf("investigations: %+v", view.Investigations)
	}
	if _, err := h.svc.ResearchRun(ctx, testOwner, "run-missing"); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("missing run: %v", err)
	}
}

func TestActivitySummariesAreRedacted(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	brief := "SECRET brief text that must never surface"
	run := h.commission(t, brief, "run-activity", nil).View.RunId
	// The service refuses steering on muse runs; journal one steering
	// record directly so the redaction assertions still cover a body.
	now := time.Now()
	steerPayload, err := json.Marshal(map[string]any{"revision": 1,
		"body": "SECRET steering body that must never surface"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.journal.Append(ctx, researchcontract.Event{
		ID: "steer-test." + run + ".1", RunID: run, Kind: "run.steer_received",
		Outcome: researchcontract.OutcomeOK, Payload: steerPayload,
		ObservedAt: now, RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	page, err := h.svc.ResearchActivity(ctx, testOwner, run, "", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) < 2 {
		t.Fatalf("activity events: %+v", page.Events)
	}
	joined, _ := json.Marshal(page)
	if strings.Contains(string(joined), "SECRET") {
		t.Fatalf("activity leaks source text: %s", joined)
	}
	kinds := map[string]string{}
	for _, e := range page.Events {
		kinds[e.Kind] = e.Summary
		if e.Cursor == nil || *e.Cursor == "" || e.Summary == "" {
			t.Fatalf("event shape: %+v", e)
		}
	}
	if !strings.Contains(kinds["run.commissioned"], museAgentID) {
		t.Fatalf("commissioned summary: %q", kinds["run.commissioned"])
	}
	if !strings.Contains(kinds["run.steer_received"], "revision 1") {
		t.Fatalf("steer summary: %q", kinds["run.steer_received"])
	}
	// Cursor paging walks the same journal without overlap.
	first, err := h.svc.ResearchActivity(ctx, testOwner, run, "", 1)
	if err != nil || len(first.Events) != 1 || first.NextCursor == nil {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := h.svc.ResearchActivity(ctx, testOwner, run, *first.NextCursor, 100)
	if err != nil || len(second.Events) == 0 || second.Events[0].EventId == first.Events[0].EventId {
		t.Fatalf("second page: %+v %v", second, err)
	}
	if _, err := h.svc.ResearchActivity(ctx, testOwner, run, "cursor-bogus", 10); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("bad cursor: %v", err)
	}
	if _, err := h.svc.ResearchActivity(ctx, testOwner, "run-missing", "", 10); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("missing run: %v", err)
	}
}

func TestSteerMuseRunsConflict(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Unsteerable run.", "run-steer", nil).View.RunId
	_, err := h.svc.SteerResearch(ctx, httpapi.SteerResearchInput{
		Actor: testOwner, RunID: run, Body: "Prefer platform teams.", IdempotencyKey: "steer-q"})
	if contractCode(t, err) != researchcontract.OutcomeConflict {
		t.Fatalf("muse steer: %v", err)
	}
	if _, err := h.svc.SteerResearch(ctx, httpapi.SteerResearchInput{
		Actor: testAgent, RunID: run, Body: "No.", IdempotencyKey: "steer-forbidden"}); contractCode(t, err) != researchcontract.OutcomeForbidden {
		t.Fatalf("non-owner steer: %v", err)
	}
}

func TestCommissionWithoutMuseReportsNotReady(t *testing.T) {
	h := newAdapterHarness(t)
	h.svc.muse = nil
	_, err := h.svc.CommissionResearch(context.Background(), httpapi.CommissionResearchInput{
		Actor: testOwner, BriefText: "No commissioner.", IdempotencyKey: "run-nomuse"})
	if !errors.Is(err, rounds.ErrNotReady) {
		t.Fatalf("commission without muse: %v", err)
	}
}

func TestDiscoveryCriteriaMapsBrief(t *testing.T) {
	brief := codexservice.OwnerBrief{ProfileVersion: 3, RubricVersion: "criteria-v3-abc", Source: "s",
		Facts: []codexservice.BriefFact{
			{Key: "preferredLocation", Value: "Amsterdam"},
			{Key: "allowRemote", Value: "true"},
			{Key: "allowHybrid", Value: "false"},
			{Key: "minMonthlyBase", Value: "500000 EUR"},
			{Key: "timezone", Value: "Europe/Amsterdam"},
		},
		Preferences: []codexservice.BriefFact{
			{Key: "c1", Value: "must: senior support"},
			{Key: "c2", Value: "want: hybrid friendly"},
		}}
	criteria := discoveryCriteria(brief, "night shifts excluded")
	if criteria.RegionText != "Amsterdam" {
		t.Fatalf("region: %q", criteria.RegionText)
	}
	joined := strings.Join(criteria.RoleKeywords, "\n")
	for _, want := range []string{"must: senior support", "want: hybrid friendly",
		"night shifts excluded", "remote work acceptable"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("keywords %q miss %q", joined, want)
		}
	}
	for _, leak := range []string{"500000", "Europe/Amsterdam", "hybrid work acceptable"} {
		if strings.Contains(joined, leak) || strings.Contains(criteria.RegionText, leak) {
			t.Fatalf("criteria leak %q: %+v", leak, criteria)
		}
	}
	long := strings.Repeat("word ", 100)
	capped := discoveryCriteria(codexservice.OwnerBrief{}, long)
	if len(capped.RoleKeywords) == 0 || len(capped.RoleKeywords) > 20 {
		t.Fatalf("chunked keywords: %d", len(capped.RoleKeywords))
	}
	for _, keyword := range capped.RoleKeywords {
		if len(keyword) > 200 {
			t.Fatalf("keyword over 200 chars: %q", keyword)
		}
	}
}

func TestCaptureView(t *testing.T) {
	h := newAdapterHarness(t)
	at := time.Now().UTC().Truncate(time.Second)
	h.caps.open = func(captureID string) (researchcontract.Capture, error) {
		if captureID != "cap-1" {
			return researchcontract.Capture{}, researchcontract.NewError(researchcontract.OutcomeNotFound,
				"captureId", "unknown capture "+captureID)
		}
		return researchcontract.Capture{ID: "cap-1", Status: "200 OK",
			FinalURL: "https://example.invalid/jobs", ContentType: "text/html",
			Request: researchcontract.RequestDescriptor{Operation: "fetch",
				Backend: "test", URLOrQuery: "https://example.invalid/jobs"},
			ObservedAt: at, SHA256: strings.Repeat("a", 64), Bytes: 2048, Complete: true}, nil
	}
	view, err := h.svc.ResearchCapture(context.Background(), testOwner, "cap-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.CaptureId != "cap-1" || view.ContentHash != strings.Repeat("a", 64) ||
		view.Extent.Bytes != 2048 || !view.Extent.Complete || view.MediaType != "text/html" ||
		view.ObservedUrl != "https://example.invalid/jobs" || view.FinalUrl == nil {
		t.Fatalf("capture view: %+v", view)
	}
	if !h.closed {
		t.Fatal("capture reader not closed")
	}
	if _, err := h.svc.ResearchCapture(context.Background(), testOwner, "cap-missing"); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("missing capture: %v", err)
	}
}

func TestIdentityView(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Identity run.", "run-identity", nil).View.RunId
	company, _, err := h.db.CreateCompany(ctx, testOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	round, err := h.db.Round(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	jevCost, _ := store.RoundOperationCost(store.RoundJevRequest)
	jevRes, _, err := h.db.ReserveRoundAttempt(ctx, testOwner, run, store.RoundAttemptInput{
		RequestKey: "jev-res", Operation: store.RoundJevRequest,
		ResourceID: store.ResearchAuthorityResource, Cost: jevCost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.MarkRoundDispatched(ctx, run, jevRes.ID); err != nil {
		t.Fatal(err)
	}
	jevAttempt, err := h.db.BeginJevAttempt(ctx, store.JevAttemptStart{RoundID: run,
		RoundAttemptID: jevRes.ID, Purpose: "identity_match",
		InputSHA256: strings.Repeat("c", 64), SourceRefsJSON: []byte(`[]`),
		CandidateSetJSON: []byte(`[]`), ProfileVersion: round.ProfileVersion,
		RubricVersion: "criteria-test", RequestedModel: "jev-test",
		LogicalRequestJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	cp, err := h.sup.Checkpoint(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		capture, err := store.InsertSourceCapture(ctx, db, store.SourceCaptureInput{
			ContentSHA256: strings.Repeat("b", 64), ArtifactRef: "artifact-test",
			ByteLength: 128, MediaType: "text/html", OriginalURL: "https://example.invalid/",
			RetrievedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Provenance:  researchcontract.ProvenanceFetchedResponse, Completeness: "complete",
			Executor: researchcontract.ExecutorIdentity{Backend: "test"},
		})
		if err != nil {
			return err
		}
		if _, err := store.InsertSeededDynamicAssessment(ctx, db, testAgent, "jev-1",
			store.DynamicAssessmentInput{RoundID: run, JevAttemptID: jevAttempt.ID,
				Purpose: "identity_match", QuestionsJSON: `[]`, EvidenceRefsJSON: `[]`,
				ProfileVersion: cp.ProfileVersion, RubricVersion: cp.RubricVersion,
				CandidatesJSON: `[]`, CandidateSetHash: strings.Repeat("d", 64),
				RequestedModel: "jev-test", ReuseKey: strings.Repeat("e", 64),
				Status: "succeeded", AnswersJSON: `[]`}); err != nil {
			return err
		}
		_, err = store.InsertIdentityDecision(ctx, db, testAgent, store.IdentityDecisionInput{
			RoundID: run, SubjectKind: "employer", Decision: "same",
			DecisionBasis:      "Jev same-verdict over cited evidence",
			Candidates:         []researchcontract.CandidateIdentity{{CandidateID: company.ID, Kind: "company", Revision: 1}},
			SubjectCompanyID:   company.ID,
			SubjectRevision:    1,
			JevAssessmentID:    "jev-1",
			DistinguishingRefs: "[]",
		})
		if err != nil {
			return err
		}
		_, err = store.InsertRecordSighting(ctx, db, testAgent, store.RecordSightingInput{
			CompanyID: company.ID, CaptureID: capture.ID, ObservedURL: "https://example.invalid/",
			ContentSHA256: strings.Repeat("b", 64), SightingKind: store.SightingFirst,
			ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view, err := h.svc.ResearchIdentity(ctx, testOwner, "employer", company.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Decision != generated.ResearchIdentityViewDecisionSame ||
		view.Basis != "Jev same-verdict over cited evidence" || len(view.Candidates) != 1 ||
		view.Candidates[0].RecordId != company.ID || view.AssessmentId == nil {
		t.Fatalf("identity view: %+v", view)
	}
	if view.Sightings == nil || len(*view.Sightings) != 1 {
		t.Fatalf("sightings: %+v", view.Sightings)
	}
	// A subject without a recorded decision reports unresolved, honestly.
	bare, _, err := h.db.CreateCompany(ctx, testOwner, store.CompanyInput{Name: "Bare"})
	if err != nil {
		t.Fatal(err)
	}
	undecided, err := h.svc.ResearchIdentity(ctx, testOwner, "employer", bare.ID)
	if err != nil || undecided.Decision != generated.ResearchIdentityViewDecisionUnresolved ||
		undecided.Basis == "" {
		t.Fatalf("undecided: %+v %v", undecided, err)
	}
	if _, err := h.svc.ResearchIdentity(ctx, testOwner, "employer", "company-missing"); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("missing subject: %v", err)
	}
	if _, err := h.svc.ResearchIdentity(ctx, testOwner, "planet", company.ID); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("bad kind: %v", err)
	}
}

func TestReportAggregatesActualOutcomes(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Reported run.", "run-report", nil).View.RunId
	company, _, err := h.db.CreateCompany(ctx, testOwner, store.CompanyInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	opp, _, err := h.db.CreateOpportunity(ctx, testOwner, store.OpportunityInput{CompanyID: company.ID,
		Title: "Backend Engineer", Kind: "employment", SourceURL: "https://example.invalid/job",
		OriginalText: "Build a service.", Stage: "new", WorkPattern: "remote", LocationText: "Brussels"})
	if err != nil {
		t.Fatal(err)
	}
	request := researchcontract.RequestDescriptor{Operation: "fetch",
		Backend: "test", URLOrQuery: "https://example.invalid/jobs"}
	if _, err := h.sup.Dispatch(ctx, rounds.DispatchInput{RunID: run, Kind: researchcontract.ExecuteFetch,
		Request: request, Bounds: researchcontract.Bounds{MaxBytes: 4096, MaxRequests: 2},
		IdempotencyKey: "exec-report"}); err != nil {
		t.Fatal(err)
	}
	cp, err := h.sup.Checkpoint(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	cp.SavedRecordIDs = []string{opp.ID, "record-dangling"}
	cp.UnresolvedAttempts = []researchcontract.UnresolvedAttempt{{AttemptID: "att-x", Reason: "rate_limited"}}
	cp.NextWork = []string{"retry after window"}
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SaveRunCheckpoint(ctx, db, run, cp)
	}); err != nil {
		t.Fatal(err)
	}
	report, err := h.svc.ResearchReport(ctx, testOwner, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Outcomes) != 2 || !strings.Contains(report.Outcomes[0], "Backend Engineer") ||
		report.Outcomes[1] != "record record-dangling" {
		t.Fatalf("outcomes: %+v", report.Outcomes)
	}
	if len(report.Searched) != 1 || !strings.Contains(report.Searched[0], "research.fetch") {
		t.Fatalf("searched: %+v", report.Searched)
	}
	if len(report.Uncertainty) != 1 || !strings.Contains(report.Uncertainty[0], "att-x") {
		t.Fatalf("uncertainty: %+v", report.Uncertainty)
	}
	if report.Budget.Observed.Observed.Bytes != 900 || report.Budget.Unknown {
		t.Fatalf("budget: %+v", report.Budget)
	}
	if report.NextWork == nil || len(*report.NextWork) != 1 {
		t.Fatalf("next work: %+v", report.NextWork)
	}
	if _, err := h.svc.ResearchReport(ctx, testOwner, "run-missing"); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("missing run: %v", err)
	}
}
