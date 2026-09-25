package publicresearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
)

// deniedKeys mirrors the musecode contract test: no Contributor output
// may carry these, at any depth.
var deniedKeys = []string{"owner", "owner_name", "email", "cv", "cv_text",
	"profile", "answer", "approved_answer", "jev", "jev_assessment",
	"jev_reason", "private", "secret", "token", "password",
	"session_history", "transcript"}

type fakeExecutor struct {
	mu     sync.Mutex
	calls  []researchcontract.ExecuteInput
	output researchcontract.ExecuteOutput
	err    error
}

func (f *fakeExecutor) Execute(_ context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.err != nil {
		return researchcontract.ExecuteOutput{}, f.err
	}
	return f.output, nil
}

func (f *fakeExecutor) last() researchcontract.ExecuteInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

type fakeBlob struct {
	desc researchcontract.Capture
	data []byte
}

type fakeCaptures struct {
	mu       sync.Mutex
	receipts map[string]researchcontract.ExecutionReceipt
	blobs    map[string]fakeBlob
}

func (f *fakeCaptures) ResolveReceipt(_ context.Context, receiptID string) (researchcontract.ExecutionReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.receipts[receiptID]
	if !ok {
		return researchcontract.ExecutionReceipt{}, researchcontract.NewError(
			researchcontract.OutcomeNotFound, "receipt", "receipt "+receiptID+" was never recorded")
	}
	return rec, nil
}

func (f *fakeCaptures) OpenCapture(_ context.Context, captureID string) (researchcontract.Capture, io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blob, ok := f.blobs[captureID]
	if !ok {
		return researchcontract.Capture{}, nil, researchcontract.NewError(
			researchcontract.OutcomeNotFound, "captureId", "unknown capture "+captureID)
	}
	return blob.desc, io.NopCloser(bytes.NewReader(blob.data)), nil
}

func testBounds() musecode.Bounds {
	return musecode.Bounds{
		MaxWallClock:  time.Hour,
		MaxModelSteps: 10,
		MaxToolCalls:  100,
		MaxBytesPerOp: 1 << 20,
		MaxBytesTotal: 10 << 20,
	}
}

func testFixtures() (*fakeExecutor, *fakeCaptures) {
	exec := &fakeExecutor{output: researchcontract.ExecuteOutput{
		Outcome:       researchcontract.OutcomeOK,
		ObservationID: "obs-fixture-1",
		CaptureID:     "cap-fixture-1",
		Receipt: researchcontract.ExecutionReceipt{
			ID: "rc-fixture-1", Status: researchcontract.ReceiptOK,
			CaptureID: "cap-fixture-1", FinalURL: "https://careers.fixture.invalid/jobs/1",
		},
		Usage: researchcontract.ExecuteUsage{Requests: 1, Bytes: 128},
	}}
	caps := &fakeCaptures{
		receipts: map[string]researchcontract.ExecutionReceipt{
			"rc-fixture-1": {
				ID: "rc-fixture-1", Status: researchcontract.ReceiptOK,
				CaptureID: "cap-fixture-1", FinalURL: "https://careers.fixture.invalid/jobs/1",
			},
		},
		blobs: map[string]fakeBlob{
			"cap-fixture-1": {
				desc: researchcontract.Capture{ID: "cap-fixture-1", ContentType: "text/html"},
				data: []byte("<html>fixture vacancy page</html>"),
			},
		},
	}
	return exec, caps
}

func testServer(t *testing.T, bounds musecode.Bounds, exec *fakeExecutor, caps *fakeCaptures) *Server {
	t.Helper()
	s, err := NewServer(Deps{
		Executor: exec, Captures: caps, Bounds: bounds,
		RunID: "run-fixture", Generation: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testSession(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	httpServer := httptest.NewServer(s.Handler())
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL, HTTPClient: httpServer.Client(),
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
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

func assertNoDeniedKeys(t *testing.T, tool string, payload map[string]any) {
	t.Helper()
	var walk func(value any, path string)
	walk = func(value any, path string) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				for _, bad := range deniedKeys {
					if key == bad {
						t.Errorf("%s: output exposes denied key %q at %s", tool, bad, path)
					}
				}
				walk(child, path+"."+key)
			}
		case []any:
			for i, child := range v {
				walk(child, path+"["+string(rune('0'+i))+"]")
			}
		}
	}
	walk(payload, "$")
}

func dtoKeys(t *testing.T, tool string, payload map[string]any, field string) []string {
	t.Helper()
	nested, ok := payload[field].(map[string]any)
	if !ok {
		t.Fatalf("%s: %q is not an object: %+v", tool, field, payload)
	}
	keys := make([]string, 0, len(nested))
	for key := range nested {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestServedInventoryEqualsFrozenAllowlist(t *testing.T) {
	exec, caps := testFixtures()
	session := testSession(t, testServer(t, testBounds(), exec, caps))
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, tool := range result.Tools {
		served[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("served tool %q has no description", tool.Name)
		}
	}
	frozen := musecode.ContributorToolNames()
	if len(served) != len(frozen) {
		t.Fatalf("served %d tools, frozen allowlist has %d", len(served), len(frozen))
	}
	for _, name := range frozen {
		if !served[name] {
			t.Errorf("frozen tool %q is not served", name)
		}
		if !musecode.ContributorToolAllowed(name) {
			t.Errorf("served tool %q fails the frozen allowlist", name)
		}
	}
	for name := range served {
		if !musecode.ContributorToolAllowed(name) {
			t.Errorf("served tool %q is outside the frozen allowlist", name)
		}
	}
	for _, name := range ToolNames() {
		if !served[name] {
			t.Errorf("package tool %q is not served", name)
		}
	}
	// Inherited host tools and private capabilities must never appear.
	for _, name := range []string{"context_read", "research_memory", "evidence_capture",
		"jev_assess", "opportunity_match", "records_save", "research_execute",
		"round_context", "round_mutation", "exec", "shell", "read_memory"} {
		if served[name] {
			t.Errorf("private/host tool %q is served", name)
		}
	}
}

func TestPublicSearchNovelSourcePassthrough(t *testing.T) {
	exec, caps := testFixtures()
	session := testSession(t, testServer(t, testBounds(), exec, caps))
	payload := call(t, session, "public_search", map[string]any{
		"query":   "harbor pilot rotterdam night shift",
		"backend": "novel-search-backend-7",
		"params": []any{
			map[string]any{"name": "hl", "value": "en"},
			map[string]any{"name": "num", "value": "10"},
		},
		"limit":  "25",
		"locale": "en-GB",
		"criteria": map[string]any{
			"role_keywords":  []any{"harbor pilot"},
			"region_text":    "rotterdam",
			"skill_keywords": []any{"radar", "vhf"},
		},
	})
	if payload["outcome"] != "ok" {
		t.Fatalf("outcome: %+v", payload)
	}
	got := exec.last()
	if got.Kind != researchcontract.ExecuteSearch || got.Request.Operation != researchcontract.OperationSearch {
		t.Errorf("kind/operation = %s/%s, want search/search", got.Kind, got.Request.Operation)
	}
	if got.Request.Backend != "novel-search-backend-7" {
		t.Errorf("backend = %q, want the novel backend untouched", got.Request.Backend)
	}
	if got.Request.URLOrQuery != "harbor pilot rotterdam night shift" {
		t.Errorf("query = %q, want the novel query untouched", got.Request.URLOrQuery)
	}
	wantParams := []researchcontract.Param{{Name: "hl", Value: "en"}, {Name: "num", Value: "10"}}
	if !reflect.DeepEqual(got.Request.Params, wantParams) {
		t.Errorf("params = %+v, want %+v", got.Request.Params, wantParams)
	}
	if got.Request.Pagination.Limit != "25" || got.Request.SessionFields["locale"] != "en-GB" {
		t.Errorf("pagination/locale = %+v %+v", got.Request.Pagination, got.Request.SessionFields)
	}
	if got.RunID != "run-fixture" || got.Generation != 3 {
		t.Errorf("run scope = %s/%d", got.RunID, got.Generation)
	}
	if got.Bounds.MaxBytes <= 0 || got.Bounds.MaxRequests <= 0 || got.Bounds.DeadlineMs <= 0 {
		t.Errorf("execution bounds not derived: %+v", got.Bounds)
	}
	if payload["receipt_id"] != "rc-fixture-1" || payload["capture_id"] != "cap-fixture-1" {
		t.Errorf("receipt/capture not passed through: %+v", payload)
	}
	if payload["excerpt"] != "<html>fixture vacancy page</html>" {
		t.Errorf("excerpt = %v", payload["excerpt"])
	}
	raw, _ := json.Marshal(payload["criteria"])
	var criteria musecode.PublicCriteria
	if err := json.Unmarshal(raw, &criteria); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(criteria.RoleKeywords, []string{"harbor pilot"}) ||
		criteria.RegionText != "rotterdam" ||
		!reflect.DeepEqual(criteria.SkillKeywords, []string{"radar", "vhf"}) {
		t.Errorf("criteria projection = %+v", criteria)
	}
	assertNoDeniedKeys(t, "public_search", payload)
}

func TestPublicSourcesAreUnrestricted(t *testing.T) {
	exec, caps := testFixtures()
	session := testSession(t, testServer(t, testBounds(), exec, caps))
	// Two unrelated novel backends and queries must both dispatch: there
	// is no fixed source menu to reject them.
	for _, query := range []struct{ backend, text string }{
		{"aaa-novel-backend", "cobol night operator groningen"},
		{"zzz-other-backend", "lighthouse keeper terschelling"},
	} {
		payload := call(t, session, "public_search", map[string]any{
			"query": query.text, "backend": query.backend,
		})
		if payload["outcome"] != "ok" {
			t.Fatalf("%s: %+v", query.backend, payload)
		}
	}
	if len(exec.calls) != 2 || exec.calls[0].Request.Backend == exec.calls[1].Request.Backend {
		t.Errorf("novel backends were not both dispatched: %+v", exec.calls)
	}
}

func TestPublicAPICapturePath(t *testing.T) {
	exec, caps := testFixtures()
	session := testSession(t, testServer(t, testBounds(), exec, caps))
	payload := call(t, session, "public_fetch", map[string]any{
		"url":          "https://api.novel-example.invalid/v1/jobs",
		"kind":         "api",
		"method":       "POST",
		"body":         `{"q":"pilot"}`,
		"content_type": "application/json",
		"params":       []any{map[string]any{"name": "page", "value": "2"}},
	})
	if payload["outcome"] != "ok" {
		t.Fatalf("outcome: %+v", payload)
	}
	got := exec.last()
	if got.Kind != researchcontract.ExecuteAPI || got.Request.Operation != researchcontract.OperationAPI {
		t.Fatalf("kind/operation = %s/%s, want api/api", got.Kind, got.Request.Operation)
	}
	if got.Request.Method != http.MethodPost || got.Request.Body != `{"q":"pilot"}` {
		t.Errorf("method/body = %s %s", got.Request.Method, got.Request.Body)
	}
	sum := sha256.Sum256([]byte(`{"q":"pilot"}`))
	if got.Request.BodySHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("body_sha256 = %q", got.Request.BodySHA256)
	}
	foundContentType := false
	for _, p := range got.Request.Params {
		if p.Name == "Content-Type" && p.Value == "application/json" {
			foundContentType = true
		}
	}
	if !foundContentType {
		t.Errorf("Content-Type param missing: %+v", got.Request.Params)
	}
	// The search tool also serves public API capture over GET.
	payload = call(t, session, "public_search", map[string]any{
		"query": "https://api.novel-example.invalid/v1/search?q=pilot",
		"kind":  "api",
	})
	if payload["outcome"] != "ok" {
		t.Fatalf("api search outcome: %+v", payload)
	}
	if got := exec.last(); got.Kind != researchcontract.ExecuteAPI {
		t.Errorf("api search kind = %s", got.Kind)
	}
	assertNoDeniedKeys(t, "public_fetch", payload)
}

func TestCareerPageCapturePaths(t *testing.T) {
	exec, caps := testFixtures()
	session := testSession(t, testServer(t, testBounds(), exec, caps))
	payload := call(t, session, "public_fetch", map[string]any{
		"url": "https://careers.novel-example.invalid/jobs/12345",
	})
	if payload["outcome"] != "ok" {
		t.Fatalf("outcome: %+v", payload)
	}
	got := exec.last()
	if got.Kind != researchcontract.ExecuteFetch || got.Request.Operation != researchcontract.OperationFetch {
		t.Errorf("kind/operation = %s/%s, want fetch/fetch", got.Kind, got.Request.Operation)
	}
	if got.Request.URLOrQuery != "https://careers.novel-example.invalid/jobs/12345" {
		t.Errorf("url = %q", got.Request.URLOrQuery)
	}
	if got.Request.Backend != researchexecute.BackendHTTP {
		t.Errorf("default fetch backend = %q", got.Request.Backend)
	}
	if payload["final_url"] != "https://careers.fixture.invalid/jobs/1" {
		t.Errorf("final_url = %v", payload["final_url"])
	}
	assertNoDeniedKeys(t, "public_fetch", payload)

	payload = call(t, session, "public_fetch", map[string]any{
		"url":      "https://careers.novel-example.invalid/jobs/67890",
		"kind":     "browse",
		"viewport": "1280x800",
	})
	if payload["outcome"] != "ok" {
		t.Fatalf("browse outcome: %+v", payload)
	}
	if got := exec.last(); got.Kind != researchcontract.ExecuteBrowse ||
		got.Request.Backend != researchexecute.BackendBrowser {
		t.Errorf("browse backend = %s %s", got.Kind, got.Request.Backend)
	}
}

func TestSaveVacancyRequiresTrustedReceipt(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	session := testSession(t, s)
	unknown := call(t, session, "public_save_vacancy", map[string]any{
		"page_url": "https://careers.novel-example.invalid/jobs/9",
		"title":    "Harbor Pilot",
		"receipt":  "rc-forged",
	})
	if unknown["outcome"] != string(researchcontract.OutcomeNotFound) {
		t.Fatalf("forged receipt outcome: %+v", unknown)
	}
	saved := call(t, session, "public_save_vacancy", map[string]any{
		"page_url":        "https://careers.novel-example.invalid/jobs/9",
		"employer_name":   "Novel Port",
		"title":           "Harbor Pilot",
		"location_text":   "Rotterdam",
		"posted_text":     "2 days ago",
		"receipt":         "rc-fixture-1",
		"idempotency_key": "save-1",
	})
	if saved["outcome"] != "ok" {
		t.Fatalf("outcome: %+v", saved)
	}
	raw, _ := json.Marshal(saved["vacancy"])
	var vac musecode.PublicVacancy
	if err := json.Unmarshal(raw, &vac); err != nil {
		t.Fatal(err)
	}
	if vac.VacancyRef == "" || vac.ReceiptRef != "rc-fixture-1" || vac.SourceHost != "careers.novel-example.invalid" {
		t.Errorf("vacancy projection = %+v", vac)
	}
	if vac.Title != "Harbor Pilot" || vac.EmployerName != "Novel Port" || vac.CapturedAt == "" {
		t.Errorf("vacancy fields = %+v", vac)
	}
	wantKeys := []string{"captured_at", "employer_name", "location_text", "page_url",
		"posted_text", "receipt_ref", "source_host", "title", "vacancy_ref"}
	if keys := dtoKeys(t, "public_save_vacancy", saved, "vacancy"); !reflect.DeepEqual(keys, wantKeys) {
		t.Errorf("vacancy keys = %v, want frozen %v", keys, wantKeys)
	}
	assertNoDeniedKeys(t, "public_save_vacancy", saved)

	replay := call(t, session, "public_save_vacancy", map[string]any{
		"page_url":        "https://careers.novel-example.invalid/jobs/9",
		"title":           "Harbor Pilot",
		"receipt":         "rc-fixture-1",
		"idempotency_key": "save-1",
	})
	if replay["outcome"] != string(researchcontract.OutcomeReused) {
		t.Fatalf("replay outcome: %+v", replay)
	}
	raw, _ = json.Marshal(replay["vacancy"])
	var again musecode.PublicVacancy
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if again.VacancyRef != vac.VacancyRef {
		t.Errorf("replay ref = %q, want %q", again.VacancyRef, vac.VacancyRef)
	}
	listed := call(t, session, "public_list_saved", nil)
	if vacancies, ok := listed["vacancies"].([]any); !ok || len(vacancies) != 1 {
		t.Errorf("replay duplicated the save: %+v", listed)
	}
}

func TestSaveQuestionLinksSavedVacancy(t *testing.T) {
	exec, caps := testFixtures()
	session := testSession(t, testServer(t, testBounds(), exec, caps))
	missing := call(t, session, "public_save_question", map[string]any{
		"vacancy_ref": "vac-9999",
		"prompt_text": "Why this role?",
		"source_url":  "https://careers.novel-example.invalid/apply",
	})
	if missing["outcome"] != string(researchcontract.OutcomeNotFound) {
		t.Fatalf("unknown vacancy outcome: %+v", missing)
	}
	saved := call(t, session, "public_save_vacancy", map[string]any{
		"page_url": "https://careers.novel-example.invalid/jobs/9",
		"title":    "Harbor Pilot",
		"receipt":  "rc-fixture-1",
	})
	raw, _ := json.Marshal(saved["vacancy"])
	var vac musecode.PublicVacancy
	if err := json.Unmarshal(raw, &vac); err != nil {
		t.Fatal(err)
	}
	empty := call(t, session, "public_save_question", map[string]any{
		"vacancy_ref": vac.VacancyRef,
		"prompt_text": "   ",
		"source_url":  "https://careers.novel-example.invalid/apply",
	})
	if empty["outcome"] != string(researchcontract.OutcomeInvalid) {
		t.Fatalf("empty prompt outcome: %+v", empty)
	}
	stored := call(t, session, "public_save_question", map[string]any{
		"vacancy_ref": vac.VacancyRef,
		"prompt_text": "Describe your pilotage experience.",
		"required":    true,
		"source_url":  "https://careers.novel-example.invalid/apply",
	})
	if stored["outcome"] != "ok" {
		t.Fatalf("outcome: %+v", stored)
	}
	raw, _ = json.Marshal(stored["question"])
	var q musecode.PublicQuestion
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if q.QuestionRef == "" || q.VacancyRef != vac.VacancyRef || !q.Required {
		t.Errorf("question projection = %+v", q)
	}
	wantKeys := []string{"prompt_text", "question_ref", "required", "source_url", "vacancy_ref"}
	if keys := dtoKeys(t, "public_save_question", stored, "question"); !reflect.DeepEqual(keys, wantKeys) {
		t.Errorf("question keys = %v, want frozen %v", keys, wantKeys)
	}
	assertNoDeniedKeys(t, "public_save_question", stored)

	listed := call(t, session, "public_list_saved", nil)
	if listed["outcome"] != "ok" {
		t.Fatalf("list outcome: %+v", listed)
	}
	if vacancies, ok := listed["vacancies"].([]any); !ok || len(vacancies) != 1 {
		t.Errorf("vacancies = %+v", listed["vacancies"])
	}
	if questions, ok := listed["questions"].([]any); !ok || len(questions) != 1 {
		t.Errorf("questions = %+v", listed["questions"])
	}
	assertNoDeniedKeys(t, "public_list_saved", listed)
}

func TestListSavedPages(t *testing.T) {
	exec, caps := testFixtures()
	session := testSession(t, testServer(t, testBounds(), exec, caps))
	for _, title := range []string{"Pilot", "Mate"} {
		payload := call(t, session, "public_save_vacancy", map[string]any{
			"page_url": "https://careers.novel-example.invalid/jobs/" + strings.ToLower(title),
			"title":    title,
			"receipt":  "rc-fixture-1",
		})
		if payload["outcome"] != "ok" {
			t.Fatal(payload)
		}
	}
	first := call(t, session, "public_list_saved", map[string]any{"limit": 1})
	vacancies, ok := first["vacancies"].([]any)
	if !ok || len(vacancies) != 1 {
		t.Fatalf("first page: %+v", first)
	}
	cursor, ok := first["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("first page cursor: %+v", first)
	}
	second := call(t, session, "public_list_saved", map[string]any{"limit": 1, "cursor": cursor})
	if vacancies, ok := second["vacancies"].([]any); !ok || len(vacancies) != 1 {
		t.Fatalf("second page: %+v", second)
	}
	if _, more := second["next_cursor"]; more {
		t.Fatalf("unexpected third page: %+v", second)
	}
	bad := call(t, session, "public_list_saved", map[string]any{"cursor": "nope"})
	if bad["outcome"] != string(researchcontract.OutcomeInvalid) {
		t.Fatalf("bad cursor outcome: %+v", bad)
	}
}

func TestInvalidInputConsumesZeroAllowance(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	session := testSession(t, s)
	secret := call(t, session, "public_search", map[string]any{
		"query":  "pilot jobs",
		"params": []any{map[string]any{"name": "api_key", "value": "shh"}},
	})
	if secret["outcome"] != string(researchcontract.OutcomeInvalid) {
		t.Fatalf("secret param outcome: %+v", secret)
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"public_search", map[string]any{"query": "  "}},
		{"public_search", map[string]any{"query": "x", "kind": "exec"}},
		{"public_fetch", map[string]any{"url": ""}},
		{"public_fetch", map[string]any{"url": "https://x.invalid", "method": "DELETE"}},
		{"public_fetch", map[string]any{"url": "https://x.invalid", "kind": "browse", "viewport": "wide"}},
		{"public_save_vacancy", map[string]any{"page_url": "not-a-url", "title": "t", "receipt": "r"}},
		{"public_save_question", map[string]any{"vacancy_ref": "vac-0001", "prompt_text": "", "source_url": "https://x.invalid"}},
	} {
		if got := call(t, session, tc.tool, tc.args); got["outcome"] != string(researchcontract.OutcomeInvalid) {
			t.Fatalf("%s %+v outcome: %+v", tc.tool, tc.args, got)
		}
	}
	calls, _ := s.Usage()
	if calls != 0 {
		t.Errorf("invalid calls consumed %d requests, want zero allowance", calls)
	}
	if len(exec.calls) != 0 {
		t.Errorf("invalid input dispatched %d executions", len(exec.calls))
	}
}

func TestPrivateInputFieldsAreDroppedByConstruction(t *testing.T) {
	// Unknown JSON keys, including every private-denied name, have no
	// field to land on and vanish on decode.
	raw := `{"query":"pilot","owner":"mallory","email":"m@x.invalid","cv_text":"secret",
		"jev_assessment":"high","private":true,"session_history":["a"],"transcript":"t"}`
	var args searchArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatal(err)
	}
	if args.Query != "pilot" {
		t.Fatalf("args = %+v", args)
	}
	encoded, _ := json.Marshal(args)
	for _, bad := range deniedKeys {
		if strings.Contains(string(encoded), `"`+bad+`"`) {
			t.Errorf("decoded search args retain %q: %s", bad, encoded)
		}
	}
}

func TestRequestLimitEnforced(t *testing.T) {
	exec, caps := testFixtures()
	bounds := testBounds()
	bounds.MaxToolCalls = 2
	s := testServer(t, bounds, exec, caps)
	session := testSession(t, s)
	for i := 0; i < 2; i++ {
		if payload := call(t, session, "public_search", map[string]any{"query": "pilot"}); payload["outcome"] != "ok" {
			t.Fatalf("call %d: %+v", i, payload)
		}
	}
	limited := call(t, session, "public_list_saved", nil)
	if limited["outcome"] != string(researchcontract.OutcomeBudgetExhausted) || limited["field"] != "tool_calls" {
		t.Fatalf("limit outcome: %+v", limited)
	}
	if calls, _ := s.Usage(); calls != 2 {
		t.Errorf("usage calls = %d, want 2", calls)
	}
}

func TestByteLimitsEnforced(t *testing.T) {
	exec, caps := testFixtures()
	exec.output.Usage.Bytes = 8 << 20
	bounds := testBounds()
	bounds.MaxBytesPerOp = 1 << 20
	session := testSession(t, testServer(t, bounds, exec, caps))
	limited := call(t, session, "public_search", map[string]any{"query": "pilot"})
	if limited["outcome"] != string(researchcontract.OutcomeBudgetExhausted) || limited["field"] != "bytes_per_op" {
		t.Fatalf("per-op limit outcome: %+v", limited)
	}

	// Measure one save on a probe server, then bound the total between
	// two and three saves so exhaustion is deterministic.
	probeExec, probeCaps := testFixtures()
	probe := testServer(t, testBounds(), probeExec, probeCaps)
	probeSession := testSession(t, probe)
	probeSave := call(t, probeSession, "public_save_vacancy", map[string]any{
		"page_url": "https://careers.novel-example.invalid/jobs/t",
		"title":    "Harbor Pilot",
		"receipt":  "rc-fixture-1",
	})
	if probeSave["outcome"] != "ok" {
		t.Fatalf("probe save: %+v", probeSave)
	}
	_, oneSave := probe.Usage()
	if oneSave <= 0 {
		t.Fatalf("probe accounted %d bytes", oneSave)
	}

	exec2, caps2 := testFixtures()
	bounds2 := testBounds()
	bounds2.MaxBytesPerOp = oneSave + 100
	bounds2.MaxBytesTotal = 2*oneSave + 50
	s2 := testServer(t, bounds2, exec2, caps2)
	session2 := testSession(t, s2)
	exhausted := false
	for i := 0; i < 5; i++ {
		payload := call(t, session2, "public_save_vacancy", map[string]any{
			"page_url": "https://careers.novel-example.invalid/jobs/t",
			"title":    "Harbor Pilot",
			"receipt":  "rc-fixture-1",
		})
		if payload["outcome"] == string(researchcontract.OutcomeBudgetExhausted) {
			if payload["field"] != "bytes_total" {
				t.Fatalf("total limit field: %+v", payload)
			}
			exhausted = true
			break
		}
		if payload["outcome"] != "ok" {
			t.Fatalf("save %d: %+v", i, payload)
		}
	}
	if !exhausted {
		t.Fatal("total byte bound never exhausted")
	}
	if _, total := s2.Usage(); total > bounds2.MaxBytesTotal {
		t.Errorf("accounted bytes %d exceed total %d", total, bounds2.MaxBytesTotal)
	}
}

func TestWallClockLimitEnforced(t *testing.T) {
	exec, caps := testFixtures()
	now := time.Now()
	current := now
	s, err := NewServer(Deps{
		Executor: exec, Captures: caps,
		Bounds: func() musecode.Bounds { b := testBounds(); b.MaxWallClock = time.Minute; return b }(),
		RunID:  "run-fixture", Generation: 1, Now: func() time.Time { return current },
	})
	if err != nil {
		t.Fatal(err)
	}
	session := testSession(t, s)
	if payload := call(t, session, "public_list_saved", nil); payload["outcome"] != "ok" {
		t.Fatalf("first call: %+v", payload)
	}
	current = now.Add(2 * time.Minute)
	expired := call(t, session, "public_list_saved", nil)
	if expired["outcome"] != string(researchcontract.OutcomeBudgetExhausted) || expired["field"] != "wall_clock" {
		t.Fatalf("wall-clock outcome: %+v", expired)
	}
}

func TestExcerptTruncationIsExplicit(t *testing.T) {
	exec, caps := testFixtures()
	caps.blobs["cap-fixture-1"] = fakeBlob{
		desc: researchcontract.Capture{ID: "cap-fixture-1", ContentType: "text/html"},
		data: bytes.Repeat([]byte("x"), 3000),
	}
	bounds := testBounds()
	bounds.MaxBytesPerOp = 1024
	session := testSession(t, testServer(t, bounds, exec, caps))
	payload := call(t, session, "public_fetch", map[string]any{"url": "https://careers.novel-example.invalid/big"})
	if payload["outcome"] != "ok" {
		t.Fatalf("outcome: %+v", payload)
	}
	if payload["excerpt_truncated"] != true {
		t.Fatalf("truncation not marked: %+v", payload)
	}
	if excerpt, ok := payload["excerpt"].(string); !ok || len(excerpt) != 1024 {
		t.Fatalf("excerpt length = %d", len(excerpt))
	}
}

func TestNewServerRejectsBadDeps(t *testing.T) {
	exec, caps := testFixtures()
	badBounds := testBounds()
	badBounds.MaxBytesPerOp = badBounds.MaxBytesTotal + 1
	for name, deps := range map[string]Deps{
		"nil executor":    {Captures: caps, Bounds: testBounds(), RunID: "r", Generation: 1},
		"nil captures":    {Executor: exec, Bounds: testBounds(), RunID: "r", Generation: 1},
		"bad bounds":      {Executor: exec, Captures: caps, Bounds: badBounds, RunID: "r", Generation: 1},
		"empty run":       {Executor: exec, Captures: caps, Bounds: testBounds(), Generation: 1},
		"zero generation": {Executor: exec, Captures: caps, Bounds: testBounds(), RunID: "r"},
	} {
		if _, err := NewServer(deps); err == nil {
			t.Errorf("%s admitted", name)
		}
	}
}
