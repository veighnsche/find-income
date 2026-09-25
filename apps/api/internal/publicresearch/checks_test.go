package publicresearch

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// countingCaptures wraps the shared fake with call counters so checks can
// prove reuse (zero executor calls) and untouched roles (zero opens).
type countingCaptures struct {
	*fakeCaptures
	mu           sync.Mutex
	resolveCalls []string
	openCalls    []string
}

func (c *countingCaptures) ResolveReceipt(ctx context.Context, receiptID string) (researchcontract.ExecutionReceipt, error) {
	c.mu.Lock()
	c.resolveCalls = append(c.resolveCalls, receiptID)
	c.mu.Unlock()
	return c.fakeCaptures.ResolveReceipt(ctx, receiptID)
}

func (c *countingCaptures) OpenCapture(ctx context.Context, captureID string) (researchcontract.Capture, io.ReadCloser, error) {
	c.mu.Lock()
	c.openCalls = append(c.openCalls, captureID)
	c.mu.Unlock()
	return c.fakeCaptures.OpenCapture(ctx, captureID)
}

func (c *countingCaptures) counts() (resolves, opens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.resolveCalls), len(c.openCalls)
}

// scriptExecutor answers per-URL: selected-role tests need one URL to
// succeed while another fails, which the fixed-output fake cannot do.
type scriptExecutor struct {
	mu      sync.Mutex
	calls   []researchcontract.ExecuteInput
	outputs map[string]researchcontract.ExecuteOutput
	errs    map[string]error
}

func (f *scriptExecutor) Execute(_ context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if err, ok := f.errs[in.Request.URLOrQuery]; ok {
		return researchcontract.ExecuteOutput{}, err
	}
	if out, ok := f.outputs[in.Request.URLOrQuery]; ok {
		return out, nil
	}
	return researchcontract.ExecuteOutput{}, researchcontract.NewError(
		researchcontract.OutcomeNotFound, "url", "no fixture for "+in.Request.URLOrQuery)
}

func (f *scriptExecutor) urls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	urls := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		urls = append(urls, call.Request.URLOrQuery)
	}
	return urls
}

func checkService(t *testing.T, exec researchcontract.Executor, caps researchcontract.CaptureReader, saved *Server) *CheckService {
	t.Helper()
	c, err := NewCheckService(CheckDeps{
		Executor: exec, Captures: caps, Saved: saved,
		Bounds: testBounds(), RunID: "run-check", Generation: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// saveFixtureVacancy stores one vacancy bound to a trusted receipt,
// returning its ref. It goes through the real E03 save path.
func saveFixtureVacancy(t *testing.T, s *Server, pageURL, receipt string) string {
	t.Helper()
	out, err := s.saveVacancyTool(context.Background(), saveVacancyArgs{
		PageURL: pageURL, EmployerName: "Fixture Ltd", Title: "Fixture role", Receipt: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcomeOf(out) != researchcontract.OutcomeOK {
		t.Fatalf("save vacancy: %+v", out)
	}
	vac, ok := out["vacancy"].(musecode.PublicVacancy)
	if !ok {
		t.Fatalf("save vacancy: no vacancy in %+v", out)
	}
	return vac.VacancyRef
}

func reportJSON(t *testing.T, report CheckReport) map[string]any {
	t.Helper()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestNewCheckServiceRejectsBadDeps(t *testing.T) {
	exec, caps := testFixtures()
	saved := testServer(t, testBounds(), exec, caps)
	good := CheckDeps{Executor: exec, Captures: caps, Saved: saved,
		Bounds: testBounds(), RunID: "run-check", Generation: 3}
	if _, err := NewCheckService(good); err != nil {
		t.Fatalf("good deps rejected: %v", err)
	}
	bad := []struct {
		name   string
		mutate func(*CheckDeps)
	}{
		{"nil executor", func(d *CheckDeps) { d.Executor = nil }},
		{"nil captures", func(d *CheckDeps) { d.Captures = nil }},
		{"nil saved", func(d *CheckDeps) { d.Saved = nil }},
		{"zero bounds", func(d *CheckDeps) { d.Bounds = testBounds(); d.Bounds.MaxToolCalls = 0 }},
		{"empty run", func(d *CheckDeps) { d.RunID = "" }},
		{"zero generation", func(d *CheckDeps) { d.Generation = 0 }},
	}
	for _, tc := range bad {
		deps := good
		tc.mutate(&deps)
		if _, err := NewCheckService(deps); err == nil {
			t.Errorf("%s admitted", tc.name)
		}
	}
}

func TestSelectionAloneTriggersZeroDeeperCalls(t *testing.T) {
	exec, caps := testFixtures()
	counted := &countingCaptures{fakeCaptures: caps}
	saved := testServer(t, testBounds(), exec, caps)
	svc := checkService(t, exec, counted, saved)

	sel := NewSelection("vac-0001", "vac-0002")
	if len(sel.VacancyRefs) != 2 {
		t.Fatalf("selection = %+v", sel)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("building a selection executed %d calls", len(exec.calls))
	}
	if resolves, opens := counted.counts(); resolves+opens != 0 {
		t.Fatalf("building a selection resolved %d receipts and opened %d captures", resolves, opens)
	}

	report, err := svc.Check(context.Background(), NewSelection())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Roles) != 0 {
		t.Fatalf("empty check roles = %+v", report.Roles)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("empty check executed %d calls", len(exec.calls))
	}
	if resolves, opens := counted.counts(); resolves+opens != 0 {
		t.Fatalf("empty check resolved %d receipts and opened %d captures", resolves, opens)
	}
	if report.CategoryCatalogVersion != QuestionCategoryCatalogVersion || len(report.Categories) == 0 {
		t.Fatalf("report catalog = %q %+v", report.CategoryCatalogVersion, report.Categories)
	}
	assertNoDeniedKeys(t, "check-report", reportJSON(t, report))
}

func TestCheckReusesSavedCaptureWithZeroFetch(t *testing.T) {
	exec, caps := testFixtures()
	caps.blobs["cap-fixture-1"] = fakeBlob{
		desc: researchcontract.Capture{ID: "cap-fixture-1", ContentType: "text/html", Complete: true},
		data: []byte("Fixture role at Fixture Ltd\nAre you eligible to work in this location?\nWhat is your notice period (required)?\nWe offer hybrid work.\n"),
	}
	counted := &countingCaptures{fakeCaptures: caps}
	saved := testServer(t, testBounds(), exec, caps)
	ref := saveFixtureVacancy(t, saved, "https://careers.fixture.invalid/jobs/1", "rc-fixture-1")

	svc := checkService(t, exec, counted, saved)
	report, err := svc.Check(context.Background(), NewSelection(ref))
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("reused capture still executed %d fetches", len(exec.calls))
	}
	if len(report.Roles) != 1 || report.Roles[0].Outcome != RoleChecked {
		t.Fatalf("report = %+v", report.Roles)
	}
	role := report.Roles[0]
	if !role.ReusedCapture {
		t.Error("reused evidence not reported as reused")
	}
	if len(role.Questions) != 2 {
		t.Fatalf("questions = %+v, want the two verbatim interrogatives", role.Questions)
	}
	for _, q := range role.Questions {
		if q.VacancyRef != ref {
			t.Errorf("question %+v links vacancy %q, want %q", q, q.VacancyRef, ref)
		}
		if !strings.Contains(string(caps.blobs["cap-fixture-1"].data), q.PromptText) {
			t.Errorf("question %q is not verbatim in its capture", q.PromptText)
		}
		if q.SourceURL != "https://careers.fixture.invalid/jobs/1" {
			t.Errorf("question source = %q, want the real capture route", q.SourceURL)
		}
	}
	if role.Questions[1].Required != true || role.Questions[0].Required != false {
		t.Errorf("required flags = %+v, want mechanical marker rule", role.Questions)
	}
	assertNoDeniedKeys(t, "check-report", reportJSON(t, report))
}

func TestCheckLeavesUnselectedRolesUntouched(t *testing.T) {
	exec, caps := testFixtures()
	caps.receipts["rc-second"] = researchcontract.ExecutionReceipt{
		ID: "rc-second", Status: researchcontract.ReceiptOK,
		CaptureID: "cap-second", FinalURL: "https://careers.fixture.invalid/jobs/2",
	}
	caps.blobs["cap-second"] = fakeBlob{
		desc: researchcontract.Capture{ID: "cap-second", Complete: true},
		data: []byte("Second role\nDo you hold a relevant permit?\n"),
	}
	counted := &countingCaptures{fakeCaptures: caps}
	saved := testServer(t, testBounds(), exec, caps)
	first := saveFixtureVacancy(t, saved, "https://careers.fixture.invalid/jobs/1", "rc-fixture-1")
	second := saveFixtureVacancy(t, saved, "https://careers.fixture.invalid/jobs/2", "rc-second")

	svc := checkService(t, exec, counted, saved)
	report, err := svc.Check(context.Background(), NewSelection(first))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Roles) != 1 || report.Roles[0].VacancyRef != first {
		t.Fatalf("report = %+v, want only the selected role", report.Roles)
	}
	for _, opened := range counted.openCalls {
		if opened == "cap-second" {
			t.Errorf("unselected role capture %q was opened", opened)
		}
	}
	saved.mu.Lock()
	defer saved.mu.Unlock()
	for ref, q := range saved.questions {
		if q.VacancyRef == second {
			t.Errorf("question %q saved for unselected vacancy %q", ref, second)
		}
	}
}

func TestBlockedRoleDoesNotStopOthers(t *testing.T) {
	caps := &fakeCaptures{
		receipts: map[string]researchcontract.ExecutionReceipt{},
		blobs: map[string]fakeBlob{
			"cap-good": {
				desc: researchcontract.Capture{ID: "cap-good", Complete: true},
				data: []byte("Good role\nWhen can you start?\n"),
			},
		},
	}
	exec := &scriptExecutor{
		outputs: map[string]researchcontract.ExecuteOutput{
			"https://careers.fixture.invalid/good": {
				Outcome:   researchcontract.OutcomeOK,
				CaptureID: "cap-good",
				Receipt: researchcontract.ExecutionReceipt{
					ID: "rc-good", Status: researchcontract.ReceiptOK,
					CaptureID: "cap-good", FinalURL: "https://careers.fixture.invalid/good",
				},
			},
		},
		errs: map[string]error{
			"https://careers.fixture.invalid/bad": researchcontract.NewError(
				researchcontract.OutcomeNotFound, "url", "route gone"),
		},
	}
	// Saved vacancies carry dangling receipts so both roles take the
	// fetch path; production saves always bind a receipt, and a
	// receipt that no longer resolves must not stop the other role.
	seedCaps := &fakeCaptures{
		receipts: map[string]researchcontract.ExecutionReceipt{
			"rc-seed-good": {ID: "rc-seed-good", Status: researchcontract.ReceiptOK, CaptureID: "cap-seed"},
			"rc-seed-bad":  {ID: "rc-seed-bad", Status: researchcontract.ReceiptOK, CaptureID: "cap-seed"},
		},
		blobs: map[string]fakeBlob{},
	}
	seedExec, _ := testFixtures()
	saved := testServer(t, testBounds(), seedExec, seedCaps)
	good := saveFixtureVacancy(t, saved, "https://careers.fixture.invalid/good", "rc-seed-good")
	bad := saveFixtureVacancy(t, saved, "https://careers.fixture.invalid/bad", "rc-seed-bad")

	svc := checkService(t, exec, caps, saved)
	report, err := svc.Check(context.Background(), NewSelection(good, bad, "vac-absent"))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Roles) != 3 {
		t.Fatalf("roles = %+v, want all three selected refs reported", report.Roles)
	}
	byRef := map[string]RoleCheck{}
	for _, role := range report.Roles {
		byRef[role.VacancyRef] = role
	}
	if byRef[good].Outcome != RoleChecked || len(byRef[good].Questions) != 1 {
		t.Errorf("good role = %+v, want checked with one verbatim question", byRef[good])
	}
	if byRef[bad].Outcome != RoleBlocked || byRef[bad].BlockReason == "" {
		t.Errorf("bad role = %+v, want blocked with a reason", byRef[bad])
	}
	if byRef["vac-absent"].Outcome != RoleBlocked || byRef["vac-absent"].BlockReason != "unknown_vacancy" {
		t.Errorf("absent role = %+v, want blocked unknown_vacancy", byRef["vac-absent"])
	}
	if len(byRef[bad].Questions) != 0 {
		t.Errorf("blocked role saved %+v, want zero questions", byRef[bad].Questions)
	}
	urls := exec.urls()
	if len(urls) != 2 || urls[0] != "https://careers.fixture.invalid/good" || urls[1] != "https://careers.fixture.invalid/bad" {
		t.Errorf("fetch urls = %v, want selection order", urls)
	}
	for _, call := range exec.calls {
		if call.RunID != "run-check" || call.Generation != 3 {
			t.Errorf("fetch scope = %+v, want the check run scope", call)
		}
		if call.Kind != researchcontract.ExecuteFetch || call.Request.Operation != researchcontract.OperationFetch {
			t.Errorf("fetch kind = %+v, want public fetch", call)
		}
	}
	assertNoDeniedKeys(t, "check-report", reportJSON(t, report))
}

func TestSavedQuestionsAlwaysLinkRealCaptures(t *testing.T) {
	exec, caps := testFixtures()
	caps.blobs["cap-fixture-1"] = fakeBlob{
		desc: researchcontract.Capture{ID: "cap-fixture-1", Complete: true},
		data: []byte("No interrogatives here, only statements.\n"),
	}
	saved := testServer(t, testBounds(), exec, caps)
	ref := saveFixtureVacancy(t, saved, "https://careers.fixture.invalid/jobs/1", "rc-fixture-1")

	svc := checkService(t, exec, caps, saved)
	report, err := svc.Check(context.Background(), NewSelection(ref))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Roles) != 1 || report.Roles[0].Outcome != RoleChecked {
		t.Fatalf("roles = %+v, want checked with zero questions", report.Roles)
	}
	if len(report.Roles[0].Questions) != 0 {
		t.Fatalf("questions = %+v, want none invented from statements", report.Roles[0].Questions)
	}
	if len(report.Roles[0].CaptureIDs) != 1 || report.Roles[0].CaptureIDs[0] != "cap-fixture-1" {
		t.Fatalf("captures = %+v, want the real reused capture", report.Roles[0].CaptureIDs)
	}
	saved.mu.Lock()
	defer saved.mu.Unlock()
	if len(saved.questions) != 0 {
		t.Fatalf("saved questions = %+v, want none", saved.questions)
	}
}

func TestQuestionCatalogIsFixed(t *testing.T) {
	first, second := QuestionCategories(), QuestionCategories()
	if len(first) != 6 {
		t.Fatalf("catalog = %+v, want six generic categories", first)
	}
	seen := map[string]bool{}
	for i, cat := range first {
		if cat.ID == "" || cat.Description == "" {
			t.Errorf("category %d = %+v, want id and description", i, cat)
		}
		if cat != second[i] {
			t.Errorf("catalog unstable at %d: %+v vs %+v", i, cat, second[i])
		}
		seen[cat.ID] = true
	}
	if !seen["other"] {
		t.Error("catalog lacks a catch-all other category")
	}
}
