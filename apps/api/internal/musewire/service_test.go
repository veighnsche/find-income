package musewire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/veighnsche/find-income-dashboard/api/internal/identity"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var fixtureActor = store.Actor{Kind: "administrator", ID: "owner"}

// gateTransport simulates the model session: it signals start, waits for the
// fixture to drive tool calls through the live MCP server, then emits the
// seeded save refs as validated saves.
type gateTransport struct {
	started  chan struct{}
	proceed  chan struct{}
	emitted  chan struct{}
	saves    []string
	after    func(context.Context)
	returned error
}

func (g *gateTransport) Run(ctx context.Context, _ musecode.SessionSpec, _ musecode.SessionInput, _ musecode.Cursor, sink musecode.EventSink) error {
	close(g.started)
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
		close(g.emitted)
	}
	if g.after != nil {
		g.after(ctx)
		return ctx.Err()
	}
	sink.Emit(musecode.Event{Kind: musecode.EventFinished})
	return g.returned
}

type scriptTransport struct {
	events []musecode.Event
	err    error
}

func (s scriptTransport) Run(ctx context.Context, _ musecode.SessionSpec, _ musecode.SessionInput, _ musecode.Cursor, sink musecode.EventSink) error {
	for _, event := range s.events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sink.Emit(event)
	}
	return s.err
}

type fakeExecutor struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeExecutor) Execute(context.Context, researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return researchcontract.ExecuteOutput{}, errors.New("fixture: no retrieval expected")
}

func (f *fakeExecutor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeCaptures struct {
	mu       sync.Mutex
	receipts map[string]researchcontract.ExecutionReceipt
	blobs    map[string][]byte
	broken   map[string]bool
	resolves int
	opens    int
}

func (f *fakeCaptures) breakOpen(captureID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.broken == nil {
		f.broken = map[string]bool{}
	}
	f.broken[captureID] = true
}

func (f *fakeCaptures) ResolveReceipt(_ context.Context, receiptID string) (researchcontract.ExecutionReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves++
	receipt, ok := f.receipts[receiptID]
	if !ok {
		return researchcontract.ExecutionReceipt{}, researchcontract.NewError(
			researchcontract.OutcomeNotFound, "receipt", "unknown receipt "+receiptID)
	}
	return receipt, nil
}

func (f *fakeCaptures) OpenCapture(_ context.Context, captureID string) (researchcontract.Capture, io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.broken[captureID] {
		return researchcontract.Capture{}, nil, researchcontract.NewError(
			researchcontract.OutcomeStale, "capture", "capture unreadable "+captureID)
	}
	blob, ok := f.blobs[captureID]
	if !ok {
		return researchcontract.Capture{}, nil, researchcontract.NewError(
			researchcontract.OutcomeNotFound, "capture", "unknown capture "+captureID)
	}
	return researchcontract.Capture{ID: captureID, Complete: true}, io.NopCloser(bytes.NewReader(blob)), nil
}

func (f *fakeCaptures) counts() (resolves, opens int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolves, f.opens
}

// fixtureProvider answers each asked Choice question from a per-call script.
// It proves the full assessor/exchange/persistence path with no live call.
type fixtureProvider struct {
	mu      sync.Mutex
	calls   int
	scripts []map[string]string
}

func (f *fixtureProvider) RequestedModel() string { return "jev-fixture-1" }

func (f *fixtureProvider) EncodedRequest(jev.Request) ([]byte, error) {
	return []byte(`{"model":"jev-fixture-1"}`), nil
}

func (f *fixtureProvider) EvaluateOnceCaptured(_ context.Context, req jev.Request) (jev.Result, jev.CapturedExchange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	script := map[string]string{}
	if f.calls < len(f.scripts) {
		script = f.scripts[f.calls]
	}
	f.calls++
	answers := make(map[string]jev.Answer, len(req.Questions))
	for key, question := range req.Questions {
		choice := script[key]
		if choice == "" {
			choice = jevassess.AbstainID
		}
		options := question.(jev.ChoiceQuestion).Criteria
		probabilities := make(map[string]float64, len(options))
		for option := range options {
			probabilities[option] = 0
		}
		probabilities[choice] = 1
		answers[key] = jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{
			Choice: choice, Probabilities: probabilities, Confidence: 0.9}}
	}
	body := []byte(`{"model":"jev-fixture-1"}`)
	return jev.Result{RequestedModel: "jev-fixture-1", ReturnedModel: "jev-fixture-1",
			Answers: answers, Usage: jev.Usage{InputTokens: 9, OutputTokens: 2}},
		jev.CapturedExchange{RequestBytes: body, ResponseBytes: body,
			HTTPStatus: 200, ReturnedModel: "jev-fixture-1"}, nil
}

func (f *fixtureProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fixtureBriefs struct {
	profile int64
	rubric  string
}

func (f fixtureBriefs) CurrentBrief(context.Context, string) (int64, string, error) {
	return f.profile, f.rubric, nil
}

func fixtureFacts() musecode.Facts {
	return musecode.Facts{CLIPath: "/fixture/muse", CLIReportVersion: "1.4.0",
		EffectiveModel: "fixture-model", SubscriptionLaneProved: true,
		SessionProtocolProved: true, WorkspaceIsolatedProved: true}
}

func openFixtureDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func authorFixtureCatalog(t *testing.T, db *store.Store, profile int64) store.ReasonCatalog {
	t.Helper()
	catalog, err := db.AuthorReasonCatalog(context.Background(), fixtureActor, store.ReasonCatalogInput{
		ProfileVersion: profile,
		Rubric:         "Match senior support roles; hybrid or remote; exclude on-call heavy rotations.",
		Positive: []store.ReasonChoice{
			{ID: "hybrid-ok", Label: "Hybrid friendly", Detail: "Listing offers hybrid or remote work in the owner's timezone."},
			{ID: "support-senior", Label: "Senior support scope", Detail: "Role centers on senior customer-support engineering."},
		},
		Negative: []store.ReasonChoice{
			{ID: "oncall-heavy", Label: "Heavy on-call", Detail: "Listing requires frequent night or weekend on-call rotations."},
		},
		MissingInformation: []store.ReasonChoice{
			{ID: "pay-unknown", Label: "Pay unstated", Detail: "Listing states no base pay, so the minimum cannot be checked."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func insertFixtureCapture(t *testing.T, db *store.Store, url string, body []byte) store.SourceCapture {
	t.Helper()
	sum := sha256.Sum256(body)
	status := int64(200)
	capture, err := func() (store.SourceCapture, error) {
		var out store.SourceCapture
		err := db.ResearchWrite(context.Background(), func(rdb store.ResearchDB) error {
			var err error
			out, err = store.InsertSourceCapture(context.Background(), rdb, store.SourceCaptureInput{
				ContentSHA256: hex.EncodeToString(sum[:]), ArtifactRef: "fixture-" + url,
				ByteLength: int64(len(body)), MediaType: "text/html", HTTPStatus: &status,
				OriginalURL: url, FinalURL: url, Provenance: researchcontract.ProvenanceFetchedResponse,
				Completeness: store.CaptureComplete, Executor: researchcontract.ExecutorIdentity{Backend: "fixture"},
			})
			return err
		})
		return out, err
	}()
	if err != nil {
		t.Fatal(err)
	}
	return capture
}

func buildFixtureAssessor(t *testing.T, db *store.Store, captures *fakeCaptures, provider *fixtureProvider, profile int64, rubric string) *jevassess.Handler {
	t.Helper()
	authority, err := rounds.NewAuthority(db, fixtureActor)
	if err != nil {
		t.Fatal(err)
	}
	return &jevassess.Handler{
		Authority: authority, Captures: captures,
		Briefs:   fixtureBriefs{profile: profile, rubric: rubric},
		Provider: provider, Exchanges: db, Sink: &identity.StoreSink{Store: db},
	}
}

func mcpSession(t *testing.T, server *publicresearch.Server) *mcp.ClientSession {
	t.Helper()
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL, HTTPClient: httpServer.Client(),
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
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

func saveFixtureVacancy(t *testing.T, session *mcp.ClientSession, pageURL, employer, title, receipt string) string {
	t.Helper()
	payload := callTool(t, session, "public_save_vacancy", map[string]any{
		"page_url": pageURL, "employer_name": employer, "title": title,
		"location_text": "Amsterdam", "receipt": receipt,
	})
	if payload["outcome"] != "ok" {
		t.Fatalf("save vacancy: %+v", payload)
	}
	vacancy, ok := payload["vacancy"].(map[string]any)
	if !ok {
		t.Fatalf("save vacancy: no vacancy in %+v", payload)
	}
	ref, _ := vacancy["vacancy_ref"].(string)
	if ref == "" {
		t.Fatalf("save vacancy: no ref in %+v", payload)
	}
	return ref
}

var deniedReportKeys = []string{"owner", "owner_name", "email", "cv", "cv_text", "profile",
	"answer", "approved_answer", "jev_assessment", "private", "secret", "token", "password",
	"session_history", "transcript"}

func assertNoDeniedKeys(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				for _, bad := range deniedReportKeys {
					if key == bad {
						t.Errorf("report exposes denied key %q at %s", bad, path)
					}
				}
				walk(child, path+"."+key)
			}
		case []any:
			for _, child := range typed {
				walk(child, path+"[]")
			}
		}
	}
	walk(decoded, "$")
}

type connectedFixture struct {
	db       *store.Store
	service  *Service
	executor *fakeExecutor
	captures *fakeCaptures
	provider *fixtureProvider
	profile  int64
	rubric   string
}

func newConnectedFixture(t *testing.T, transport musecode.Transport, scripts []map[string]string) *connectedFixture {
	t.Helper()
	ctx := context.Background()
	db := openFixtureDB(t)
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog := authorFixtureCatalog(t, db, prefs.Version)
	captures := &fakeCaptures{receipts: map[string]researchcontract.ExecutionReceipt{}, blobs: map[string][]byte{}}
	for i, page := range []string{"https://jobs.example.invalid/1", "https://careers.example.invalid/2"} {
		body := []byte("<html><body>\nSenior support engineer, hybrid Amsterdam.\nWhy do you want this support role?\nAre you available for night shifts (required)?\nBase pay unstated.\n</body></html>")
		capture := insertFixtureCapture(t, db, page, body)
		receiptID := "rc-1"
		if i == 1 {
			receiptID = "rc-2"
		}
		captures.receipts[receiptID] = researchcontract.ExecutionReceipt{
			ID: receiptID, Status: researchcontract.ReceiptOK, CaptureID: capture.ID}
		captures.blobs[capture.ID] = body
	}
	executor := &fakeExecutor{}
	provider := &fixtureProvider{scripts: scripts}
	assessor := buildFixtureAssessor(t, db, captures, provider, prefs.Version, catalog.RubricVersion)
	service, err := NewService(Deps{
		Facts: fixtureFacts(), Bounds: musecode.DefaultBounds(), Transport: transport,
		Cursors: StoreCursors{DB: db}, DB: db, Actor: fixtureActor,
		Executor: executor, Captures: captures, Assessor: assessor,
		Workspaces: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &connectedFixture{db: db, service: service, executor: executor,
		captures: captures, provider: provider, profile: prefs.Version, rubric: catalog.RubricVersion}
}

// The connected discovery slice: a scripted Contributor session saves sourced
// vacancies through the live MCP tools, Jev classifies them through the real
// assessor against the saved catalog, and findings persist with
// group/reason/version bindings.
func TestConnectedDiscoverySavesSourcedClassifiedVacancies(t *testing.T) {
	ctx := context.Background()
	transport := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{})}
	fix := newConnectedFixture(t, transport, []map[string]string{
		{"reason-positive": "hybrid-ok", "reason-negative": "abstain", "reason-missing": "abstain"},
		{"reason-positive": "abstain", "reason-negative": "oncall-heavy", "reason-missing": "pay-unknown"},
	})
	type commissionOutcome struct {
		result CommissionResult
		err    error
	}
	done := make(chan commissionOutcome, 1)
	go func() {
		result, err := fix.service.CommissionDiscovery(ctx, "run-1",
			musecode.PublicCriteria{RoleKeywords: []string{"support"}, RegionText: "Amsterdam"}, fix.profile, fix.rubric)
		done <- commissionOutcome{result, err}
	}()
	<-transport.started
	server, ok := fix.service.ServerForRun("run-1")
	if !ok {
		t.Fatal("live tool server unavailable during the session")
	}
	session := mcpSession(t, server)
	ref1 := saveFixtureVacancy(t, session, "https://jobs.example.invalid/1", "Example BV", "Senior support engineer", "rc-1")
	ref2 := saveFixtureVacancy(t, session, "https://careers.example.invalid/2", "Careers Inc", "Support engineer nights", "rc-2")
	transport.saves = []string{ref1, ref2}
	close(transport.proceed)
	outcome := <-done
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	result := outcome.result
	if result.Terminal.Outcome != musecode.OutcomeCompleted {
		t.Fatalf("terminal = %+v, want completed", result.Terminal)
	}
	if len(result.Findings) != 2 || len(result.ClassifyErrors) != 0 {
		t.Fatalf("findings=%d errors=%v, want 2 findings and no errors", len(result.Findings), result.ClassifyErrors)
	}
	first, second := result.Findings[0], result.Findings[1]
	if first.Group != store.FindingGroupRecommended || len(first.Reasons) != 1 || first.Reasons[0].ReasonID != "hybrid-ok" {
		t.Fatalf("first finding = %+v, want recommended hybrid-ok", first)
	}
	if first.Reasons[0].Label != "Hybrid friendly" || first.ProfileVersion != fix.profile || first.RubricVersion != fix.rubric || first.CatalogVersion == "" {
		t.Fatalf("first finding versions/labels = %+v", first)
	}
	if len(first.EvidenceLinks) != 1 || first.EvidenceLinks[0].SpanEnd <= 0 || len(first.EvidenceLinks[0].ExcerptSHA256) != 64 {
		t.Fatalf("first evidence = %+v, want one hashed span", first.EvidenceLinks)
	}
	if second.Group != store.FindingGroupNotRecommended || len(second.Reasons) != 2 {
		t.Fatalf("second finding = %+v, want not-recommended with 2 reasons", second)
	}
	if fix.provider.count() != 2 {
		t.Fatalf("jev calls = %d, want 2", fix.provider.count())
	}
	if fix.executor.count() != 0 {
		t.Fatalf("executor calls = %d, want 0 hidden retrieval", fix.executor.count())
	}
	if got := fix.service.Commissions(); got != 1 {
		t.Fatalf("commissions = %d, want 1", got)
	}

	report, err := fix.service.Report(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != musecode.OutcomeCompleted || len(report.SavedRefs) != 2 || len(report.Gaps) != 0 {
		t.Fatalf("report = %+v, want completed without gaps", report)
	}
	if len(report.Searched) != 2 || report.NextAction != "Review recommended roles" {
		t.Fatalf("report coverage = %+v, want 2 hosts + review action", report)
	}
	checkpoints, err := fix.service.Checkpoints(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 || checkpoints[0].SavedCount != 2 {
		t.Fatalf("checkpoints = %+v, want one count-2 checkpoint", checkpoints)
	}
	assertNoDeniedKeys(t, report)
	assertNoDeniedKeys(t, checkpoints)

	// Reload honesty: re-reads serve durable rows with zero new provider,
	// retrieval or capture calls.
	resolves, opens := fix.captures.counts()
	reportAgain, err := fix.service.Report(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fix.service.Checkpoints(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}
	if reportAgain.NextAction != report.NextAction || len(reportAgain.SavedRefs) != 2 {
		t.Fatalf("reloaded report = %+v, want identical", reportAgain)
	}
	if resolvesAfter, opensAfter := fix.captures.counts(); resolvesAfter != resolves || opensAfter != opens {
		t.Fatal("reload issued new capture calls")
	}
	if fix.provider.count() != 2 || fix.executor.count() != 0 {
		t.Fatal("reload issued new provider or retrieval calls")
	}

	// Replay honesty: the same key returns the stored result without
	// re-conducting or re-charging.
	replayed, err := fix.service.CommissionDiscovery(ctx, "run-1",
		musecode.PublicCriteria{RoleKeywords: []string{"support"}, RegionText: "Amsterdam"}, fix.profile, fix.rubric)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Terminal.Outcome != musecode.OutcomeCompleted || len(replayed.Findings) != 2 {
		t.Fatalf("replay = %+v, want stored completed result", replayed.Terminal)
	}
	if got := fix.service.Commissions(); got != 1 {
		t.Fatalf("commissions after replay = %d, want 1", got)
	}
	if fix.provider.count() != 2 {
		t.Fatalf("jev calls after replay = %d, want 2", fix.provider.count())
	}
	if _, err := fix.service.CommissionDiscovery(ctx, "run-1",
		musecode.PublicCriteria{}, fix.profile, "criteria-v9-deadbeefcafe"); !errors.Is(err, store.ErrRoundIdempotencyConflict) {
		t.Fatalf("different-brief replay err = %v, want idempotency conflict", err)
	}
}

func TestStopKeepsPartialSavesHonest(t *testing.T) {
	ctx := context.Background()
	transport := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{}), emitted: make(chan struct{})}
	transport.after = func(runCtx context.Context) {
		<-runCtx.Done()
	}
	fix := newConnectedFixture(t, transport, []map[string]string{
		{"reason-positive": "hybrid-ok", "reason-negative": "abstain", "reason-missing": "abstain"},
	})
	done := make(chan error, 1)
	var result CommissionResult
	go func() {
		var err error
		result, err = fix.service.CommissionDiscovery(ctx, "run-stop",
			musecode.PublicCriteria{RoleKeywords: []string{"support"}}, fix.profile, fix.rubric)
		done <- err
	}()
	<-transport.started
	server, ok := fix.service.ServerForRun("run-stop")
	if !ok {
		t.Fatal("live tool server unavailable during the session")
	}
	ref := saveFixtureVacancy(t, mcpSession(t, server), "https://jobs.example.invalid/1", "Example BV", "Senior support engineer", "rc-1")
	transport.saves = []string{ref}
	close(transport.proceed)
	<-transport.emitted
	if !fix.service.Stop("run-stop", "owner stop") {
		t.Fatal("stop reported unknown run")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result.Terminal.Outcome != musecode.OutcomeStopped {
		t.Fatalf("terminal = %+v, want stopped", result.Terminal)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want the one partial save classified", len(result.Findings))
	}
	report, err := fix.service.Report(ctx, "run-stop")
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != musecode.OutcomeStopped || len(report.Gaps) == 0 {
		t.Fatalf("report = %+v, want stopped with gaps", report)
	}
	if report.NextAction != "Review partial checkpoints, then re-commission" {
		t.Fatalf("next action = %q", report.NextAction)
	}
}

func TestNoResultRunReportsCoverageHonestly(t *testing.T) {
	ctx := context.Background()
	transport := scriptTransport{events: []musecode.Event{
		{Kind: musecode.EventModelStep},
		{Kind: musecode.EventFinished},
	}}
	fix := newConnectedFixture(t, transport, nil)
	result, err := fix.service.CommissionDiscovery(ctx, "run-empty",
		musecode.PublicCriteria{RoleKeywords: []string{"support"}}, fix.profile, fix.rubric)
	if err != nil {
		t.Fatal(err)
	}
	if result.Terminal.Outcome != musecode.OutcomeCompleted || len(result.Findings) != 0 {
		t.Fatalf("terminal = %+v findings=%d, want completed empty", result.Terminal, len(result.Findings))
	}
	if fix.provider.count() != 0 {
		t.Fatalf("jev calls = %d, want 0 without saves", fix.provider.count())
	}
	report, err := fix.service.Report(ctx, "run-empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.SavedRefs) != 0 || len(report.Gaps) != 1 || len(report.Searched) != 0 {
		t.Fatalf("report = %+v, want empty saves with one gap", report)
	}
	if report.NextAction != "Adjust the search brief or run again" {
		t.Fatalf("next action = %q", report.NextAction)
	}
	checkpoints, err := fix.service.Checkpoints(ctx, "run-empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 || checkpoints[0].SavedCount != 0 {
		t.Fatalf("checkpoints = %+v, want one empty checkpoint", checkpoints)
	}
}

func TestMapAnswersTruthTable(t *testing.T) {
	catalog := store.ReasonCatalog{
		Positive:           []store.ReasonChoice{{ID: "p", Label: "P", Detail: "Pd"}},
		Negative:           []store.ReasonChoice{{ID: "n", Label: "N", Detail: "Nd"}},
		MissingInformation: []store.ReasonChoice{{ID: "m", Label: "M", Detail: "Md"}},
	}
	_, index, err := frameQuestions(catalog)
	if err != nil {
		t.Fatal(err)
	}
	answer := func(question, choice string) researchcontract.AssessAnswer {
		if choice == "" {
			return researchcontract.AssessAnswer{QuestionID: question, Abstained: true}
		}
		return researchcontract.AssessAnswer{QuestionID: question, AnswerID: choice}
	}
	cases := []struct {
		name    string
		results []researchcontract.AssessAnswer
		group   string
		reasons int
	}{
		{"positive only", []researchcontract.AssessAnswer{answer("reason-positive", "p"), answer("reason-negative", ""), answer("reason-missing", "")}, store.FindingGroupRecommended, 1},
		{"mixed", []researchcontract.AssessAnswer{answer("reason-positive", "p"), answer("reason-negative", "n"), answer("reason-missing", "")}, store.FindingGroupProbablyNotRecommended, 2},
		{"negative only", []researchcontract.AssessAnswer{answer("reason-positive", ""), answer("reason-negative", "n"), answer("reason-missing", "")}, store.FindingGroupNotRecommended, 1},
		{"missing only", []researchcontract.AssessAnswer{answer("reason-positive", ""), answer("reason-negative", ""), answer("reason-missing", "m")}, store.FindingGroupCouldBeRecommended, 1},
		{"total abstention", []researchcontract.AssessAnswer{answer("reason-positive", ""), answer("reason-negative", ""), answer("reason-missing", "")}, store.FindingGroupUnknown, 0},
		{"unknown answer id", []researchcontract.AssessAnswer{answer("reason-positive", "forged"), answer("reason-negative", ""), answer("reason-missing", "")}, store.FindingGroupUnknown, 0},
	}
	for _, tc := range cases {
		group, basis, reasons := mapAnswers(tc.results, index)
		if group != tc.group || len(reasons) != tc.reasons {
			t.Errorf("%s: group=%q reasons=%d, want %q %d", tc.name, group, len(reasons), tc.group, tc.reasons)
		}
		if group == store.FindingGroupUnknown && basis == "" {
			t.Errorf("%s: unknown group without basis", tc.name)
		}
	}
}

func TestServiceFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := openFixtureDB(t)
	deps := Deps{Facts: fixtureFacts(), Bounds: musecode.DefaultBounds(),
		Transport: musecode.UnavailableTransport{}, Cursors: StoreCursors{DB: db}, DB: db,
		Actor: fixtureActor, Executor: &fakeExecutor{}, Captures: &fakeCaptures{},
		Assessor: &jevassess.Handler{}, Workspaces: t.TempDir()}
	if _, err := NewService(deps); err != nil {
		t.Fatalf("valid deps rejected: %v", err)
	}
	badBounds := deps
	badBounds.Bounds = musecode.Bounds{}
	if _, err := NewService(badBounds); err == nil {
		t.Error("zero bounds admitted")
	}
	badWorkspaces := deps
	badWorkspaces.Workspaces = "relative"
	if _, err := NewService(badWorkspaces); err == nil {
		t.Error("relative workspace root admitted")
	}
	service, err := NewService(deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, runRef := range []string{"", "../escape", "a/b", strings.Repeat("r", 101)} {
		if _, err := service.CommissionDiscovery(ctx, runRef, musecode.PublicCriteria{}, 1, "rubric"); err == nil {
			t.Errorf("run ref %q admitted", runRef)
		}
	}
	unready := deps
	unready.Facts = musecode.Facts{}
	closed, err := NewService(unready)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := closed.CommissionDiscovery(ctx, "run-1", musecode.PublicCriteria{}, 1, "rubric"); err == nil {
		t.Error("commission admitted without readiness")
	}
	if got := closed.Commissions(); got != 0 {
		t.Errorf("commissions = %d, want 0", got)
	}
	if _, err := service.Report(ctx, "absent"); !errors.Is(err, ErrUnknownRun) {
		t.Errorf("absent report err = %v, want ErrUnknownRun", err)
	}
	if _, err := service.Checkpoints(ctx, "absent"); !errors.Is(err, ErrUnknownRun) {
		t.Errorf("absent checkpoints err = %v, want ErrUnknownRun", err)
	}
	if service.Stop("absent", "nope") {
		t.Error("stop of unknown run reported found")
	}
	if _, ok := service.ServerForRun("absent"); ok {
		t.Error("server for unknown run reported found")
	}
	status := service.Readiness(musecode.Tier("ultra"))
	if status.Available {
		t.Errorf("unknown tier status = %+v, want unavailable", status)
	}
}
