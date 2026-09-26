package musewire

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func checkAdapterFixture(t *testing.T) *CheckAdapter {
	t.Helper()
	page := "<html><body><h1>Support Engineer</h1><p>Weekend availability is required for this rota.</p>" +
		"<p>Send your CV and motivation to jobs@example.invalid.</p>" +
		"<p>Why do you want this support role?</p></body></html>"
	captures := &fakeCaptures{
		receipts: map[string]researchcontract.ExecutionReceipt{
			"rc-check": {ID: "rc-check", Status: researchcontract.ReceiptOK, CaptureID: "cap-check"},
		},
		blobs: map[string][]byte{"cap-check": []byte(page)},
	}
	server, err := publicresearch.NewServer(publicresearch.Deps{
		Executor: &fakeExecutor{}, Captures: captures,
		Bounds: musecode.Bounds{MaxWallClock: 60000000000, MaxModelSteps: 10, MaxToolCalls: 100,
			MaxBytesPerOp: 1 << 20, MaxBytesTotal: 10 << 20},
		RunID: "round-check", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := server.SeedVacancies([]publicresearch.SeedVacancy{{
		PageURL: "https://jobs.example.invalid/1", EmployerName: "Example BV",
		Title: "Support Engineer", ReceiptRef: "rc-check",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &CheckAdapter{Captures: captures, Saved: server,
		Fetch: func(_ context.Context, sourceURL string) (string, error) {
			if sourceURL == "https://jobs.example.invalid/1" {
				return "cap-check", nil
			}
			return "", errors.New("fixture: unknown source " + sourceURL)
		},
		VacancyRef: seeded[0].VacancyRef}
}

// Verified claims persist with exact capture spans; invented text,
// unfetchable sources and unknown kinds drop as named gaps. Verified
// questions save under the checked vacancy via the deterministic saver.
func TestCheckAdapterVerifiesVerbatim(t *testing.T) {
	ctx := context.Background()
	adapter := checkAdapterFixture(t)
	text := `{"requirements":[` +
		`{"text":"Weekend availability is required for this rota.","source":"https://jobs.example.invalid/1"},` +
		`{"text":"Five years of Go are mandatory.","source":"https://jobs.example.invalid/1"}],` +
		`"route":{"kind":"direct","destination":"jobs@example.invalid","source":"https://jobs.example.invalid/1"},` +
		`"documents":[{"label":"CV","required":true,"text":"Send your CV and motivation","source":"https://jobs.example.invalid/1"},` +
		`{"label":"","required":false,"text":"Send your CV and motivation","source":"https://jobs.example.invalid/1"}],` +
		`"questions":[{"prompt_text":"Why do you want this support role?","required":false,"source":"https://jobs.example.invalid/1"},` +
		`{"prompt_text":"What is your quest?","required":true,"source":"https://jobs.example.invalid/1"}],` +
		`"gaps":[]}`
	adapted, err := adapter.Adapt(ctx, "2026-09-26T10:00:00Z", text)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapted.Requirements) != 1 || adapted.Requirements[0].Statement != "Weekend availability is required for this rota." {
		t.Fatalf("requirements: %+v gaps: %v", adapted.Requirements, adapted.Gaps)
	}
	span := adapted.Requirements[0].SourceSpan
	if span.CaptureID != "cap-check" || span.Start < 0 || span.End <= span.Start {
		t.Fatalf("requirement span: %+v", span)
	}
	if adapted.Route.Judgment != store.CheckRouteJudgmentApplication || adapted.Route.DestinationText != "jobs@example.invalid" {
		t.Fatalf("route: %+v gaps: %v", adapted.Route, adapted.Gaps)
	}
	if len(adapted.Documents) != 1 || adapted.Documents[0].Label != "CV" || !adapted.Documents[0].Required {
		t.Fatalf("documents: %+v gaps: %v", adapted.Documents, adapted.Gaps)
	}
	if len(adapted.Questions) != 1 || adapted.Questions[0].Text != "Why do you want this support role?" {
		t.Fatalf("questions: %+v gaps: %v", adapted.Questions, adapted.Gaps)
	}
	if adapted.Questions[0].Required != store.CheckOptional {
		t.Fatalf("question requiredness: %+v", adapted.Questions[0])
	}
	if len(adapted.CitedQuestions) != 1 {
		t.Fatalf("cited questions: %v", adapted.CitedQuestions)
	}
	if _, ok := adapter.Saved.Question(adapted.CitedQuestions[0]); !ok {
		t.Fatalf("verified question %q was not saved", adapted.CitedQuestions[0])
	}
	joined := strings.Join(adapted.Gaps, "\n")
	for _, want := range []string{"requirement 2 dropped", "document 2 dropped", "question 2 dropped"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gaps miss %q: %v", want, adapted.Gaps)
		}
	}
}

func TestCheckAdapterDropsUnfetchableSources(t *testing.T) {
	ctx := context.Background()
	adapter := checkAdapterFixture(t)
	text := `{"requirements":[{"text":"Weekend availability is required for this rota.","source":"https://jobs.example.invalid/gone"}],` +
		`"route":{"kind":"unsupported","destination":"","source":""},` +
		`"documents":[],"questions":[{"prompt_text":"Why?","required":false,"source":"https://jobs.example.invalid/gone"}],` +
		`"gaps":["listing page timed out"]}`
	adapted, err := adapter.Adapt(ctx, "2026-09-26T10:00:00Z", text)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapted.Requirements) != 0 || len(adapted.Questions) != 0 {
		t.Fatalf("unfetchable claims persisted: %+v", adapted)
	}
	if adapted.Route.Judgment != store.CheckRouteJudgmentUnresolved || adapted.Route.Kind != store.CheckRouteUnsupported {
		t.Fatalf("unsupported route: %+v", adapted.Route)
	}
	joined := strings.Join(adapted.Gaps, "\n")
	for _, want := range []string{"requirement 1 dropped", "question 1 dropped", "turn reported: listing page timed out"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gaps miss %q: %v", want, adapted.Gaps)
		}
	}
}

func TestCheckAdapterRejectsMalformed(t *testing.T) {
	ctx := context.Background()
	adapter := checkAdapterFixture(t)
	if _, err := adapter.Adapt(ctx, "2026-09-26T10:00:00Z", "not json"); err == nil {
		t.Fatal("malformed text adapted")
	}
	empty, err := adapter.Adapt(ctx, "2026-09-26T10:00:00Z", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Gaps) != 1 || len(empty.Requirements) != 0 || len(empty.Questions) != 0 {
		t.Fatalf("empty adaptation: %+v", empty)
	}
	badRoute, err := adapter.Adapt(ctx, "2026-09-26T10:00:00Z",
		`{"requirements":[],"route":{"kind":"teleport","destination":"x","source":"https://jobs.example.invalid/1"},"documents":[],"questions":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(badRoute.Gaps) != 1 || !strings.Contains(badRoute.Gaps[0], "unknown kind") {
		t.Fatalf("bad route gaps: %v", badRoute.Gaps)
	}
	unresolved, err := adapter.Adapt(ctx, "2026-09-26T10:00:00Z",
		`{"requirements":[],"route":{"kind":"direct","destination":"","source":""},"documents":[],"questions":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if unresolved.Route.Judgment != store.CheckRouteJudgmentUnresolved || unresolved.Route.Kind != "" {
		t.Fatalf("unresolved route: %+v", unresolved.Route)
	}
	guards := &CheckAdapter{}
	if _, err := guards.Adapt(ctx, "2026-09-26T10:00:00Z", "{}"); err == nil {
		t.Fatal("adapter without fetch/vacancy adapted")
	}
}
