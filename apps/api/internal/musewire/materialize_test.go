package musewire

import (
	"context"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

func TestParseDiscoveryOutput(t *testing.T) {
	text := `{"vacancies":[{"page_url":"https://jobs.example.invalid/1","employer_name":"Example BV",` +
		`"title":"Support Engineer","location_text":"Amsterdam","posted_text":"2d"}],` +
		`"sources_searched":["https://jobs.example.invalid"],"gaps":["pay unstated"]}`
	output, err := parseDiscoveryOutput(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Vacancies) != 1 || output.Vacancies[0].EmployerName != "Example BV" ||
		len(output.SourcesSearched) != 1 || len(output.Gaps) != 1 {
		t.Fatalf("output: %+v", output)
	}
	fenced, err := parseDiscoveryOutput("```json\n" + text + "\n```")
	if err != nil || len(fenced.Vacancies) != 1 {
		t.Fatalf("fenced: %+v %v", fenced, err)
	}
	if _, err := parseDiscoveryOutput("  "); err == nil {
		t.Fatal("empty text parsed")
	}
	if _, err := parseDiscoveryOutput("not json"); err == nil {
		t.Fatal("malformed text parsed")
	}
	big := `{"vacancies":[` + strings.TrimSuffix(strings.Repeat(
		`{"page_url":"https://jobs.example.invalid/x","employer_name":"E","title":"T"},`, maxMaterializedVacancies+1), ",") +
		`],"sources_searched":[],"gaps":[]}`
	if _, err := parseDiscoveryOutput(big); err == nil {
		t.Fatal("over-gate answer parsed")
	}
}

func materializeFixture(t *testing.T) (*publicresearch.Server, *fakeExecutor) {
	t.Helper()
	captures := &fakeCaptures{receipts: map[string]researchcontract.ExecutionReceipt{}, blobs: map[string][]byte{}}
	executor := &fakeExecutor{captures: captures}
	server, err := publicresearch.NewServer(publicresearch.Deps{
		Executor: executor, Captures: captures, Bounds: musecode.DefaultBounds(),
		RunID: "round-mat", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, executor
}

func TestMaterializeDiscoverySavesVerified(t *testing.T) {
	ctx := context.Background()
	server, executor := materializeFixture(t)
	executor.scriptFetch("https://jobs.example.invalid/1", "rc-mat-1", "cap-mat-1", []byte("<html>one</html>"))
	executor.scriptFetch("https://careers.example.invalid/2", "rc-mat-2", "cap-mat-2", []byte("<html>two</html>"))
	texts := []string{
		`{"vacancies":[` +
			`{"page_url":"https://jobs.example.invalid/1","employer_name":"Example BV","title":"Support Engineer"},` +
			`{"page_url":"https://careers.example.invalid/2","employer_name":"Careers Inc","title":"Helpdesk Lead"}],` +
			`"sources_searched":[],"gaps":[]}`,
		// A resumed turn re-reports the first sighting: it converges
		// on the same ref without re-fetching.
		`{"vacancies":[` +
			`{"page_url":"https://jobs.example.invalid/1","employer_name":"Example BV","title":"Support Engineer"}],` +
			`"sources_searched":[],"gaps":["second pass"]}`,
	}
	refs, gaps := materializeDiscovery(ctx, server, texts)
	if len(refs) != 3 || refs[0] != "vac-0001" || refs[1] != "vac-0002" || refs[2] != "vac-0001" {
		t.Fatalf("refs = %v gaps = %v, want [vac-0001 vac-0002 vac-0001]", refs, gaps)
	}
	if len(gaps) != 1 || !strings.Contains(gaps[0], "second pass") {
		t.Fatalf("gaps = %v, want the turn-reported gap", gaps)
	}
	if executor.count() != 2 {
		t.Fatalf("executor calls = %d, want 2 (re-report reuses the save)", executor.count())
	}
	if vac, ok := server.Vacancy("vac-0001"); !ok || vac.ReceiptRef != "rc-mat-1" {
		t.Fatalf("vac-0001 = %+v %v, want the trusted receipt binding", vac, ok)
	}
}

func TestMaterializeDiscoveryGaps(t *testing.T) {
	ctx := context.Background()
	server, executor := materializeFixture(t)
	executor.scriptFetch("https://jobs.example.invalid/1", "rc-mat-1", "cap-mat-1", []byte("<html>one</html>"))
	refs, gaps := materializeDiscovery(ctx, server, []string{
		"not json",
		`{"vacancies":[{"page_url":"https://jobs.example.invalid/gone","employer_name":"E","title":"T"}],` +
			`"sources_searched":[],"gaps":[]}`,
		`{"vacancies":[{"page_url":"https://jobs.example.invalid/1","employer_name":"","title":"T"}],` +
			`"sources_searched":[],"gaps":[]}`,
		`{"vacancies":[{"page_url":"","employer_name":"E","title":"T"}],"sources_searched":[],"gaps":[]}`,
	})
	if len(refs) != 0 {
		t.Fatalf("refs = %v, want none saved", refs)
	}
	joined := strings.Join(gaps, "\n")
	for _, want := range []string{"findings 1 dropped", "vacancy 1 dropped", "employer_name", "page_url is required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gaps miss %q: %v", want, gaps)
		}
	}
	refs, gaps = materializeDiscovery(ctx, server, nil)
	if len(refs) != 0 || len(gaps) != 1 || !strings.Contains(gaps[0], "no structured findings") {
		t.Fatalf("empty texts: refs=%v gaps=%v", refs, gaps)
	}
}

func TestMergeSavedRefs(t *testing.T) {
	got := mergeSavedRefs([]string{"vac-0001"}, []string{"vac-0002", "vac-0001", "", "vac-0003"})
	if len(got) != 3 || got[0] != "vac-0001" || got[1] != "vac-0002" || got[2] != "vac-0003" {
		t.Fatalf("merged = %v", got)
	}
}

// A live-shaped turn (structured text, no harness saves) commissions
// through fetch-and-verify to a classified finding with the saved ref.
func TestCommissionDiscoveryMaterializesStructuredText(t *testing.T) {
	pageURL := "https://jobs.example.invalid/1"
	text := `{"vacancies":[{"page_url":"` + pageURL + `","employer_name":"Example BV",` +
		`"title":"Senior support engineer","location_text":"Amsterdam"}],` +
		`"sources_searched":["` + pageURL + `"],"gaps":[]}`
	transport := scriptTransport{events: []musecode.Event{
		{Kind: musecode.EventModelStep},
		{Kind: musecode.EventToolCall, Tool: "web_search"},
		{Kind: musecode.EventToolResult, Tool: "web_search", BytesOut: 128},
		{Kind: musecode.EventModelStep},
		{Kind: musecode.EventModelText, Text: text, BytesOut: int64(len(text))},
		{Kind: musecode.EventFinished},
	}}
	fix := newConnectedFixture(t, transport, []map[string]string{
		{"reason-positive": "hybrid-ok", "reason-negative": "abstain", "reason-missing": "abstain"},
	})
	captureID := fix.captures.receipts["rc-1"].CaptureID
	fix.executor.scriptFetch(pageURL, "rc-mat-live", captureID, fix.captures.blobs[captureID])
	result, err := fix.service.CommissionDiscovery(context.Background(), "run-mat-1",
		musecode.PublicCriteria{RoleKeywords: []string{"support"}}, fix.profile, fix.rubric)
	if err != nil {
		t.Fatal(err)
	}
	if result.Terminal.Outcome != musecode.OutcomeCompleted {
		t.Fatalf("terminal = %+v errors = %v", result.Terminal, result.ClassifyErrors)
	}
	if len(result.Terminal.SavedRefs) != 1 || result.Terminal.SavedRefs[0] != "vac-0001" {
		t.Fatalf("saved refs = %v", result.Terminal.SavedRefs)
	}
	if len(result.Findings) != 1 || result.Findings[0].VacancyRef != "vac-0001" {
		t.Fatalf("findings = %+v errors = %v", result.Findings, result.ClassifyErrors)
	}
	if len(result.ClassifyErrors) != 0 {
		t.Fatalf("errors = %v", result.ClassifyErrors)
	}
	if fix.executor.count() != 1 {
		t.Fatalf("executor calls = %d, want the one deterministic fetch", fix.executor.count())
	}
}
