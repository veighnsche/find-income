package publicresearch

import (
	"context"
	"errors"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
)

func directFixture(t *testing.T) (*Server, *fakeExecutor, *fakeCaptures) {
	t.Helper()
	exec, caps := testFixtures()
	server, err := NewServer(Deps{Executor: exec, Captures: caps,
		Bounds: testBounds(), RunID: "round-direct", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	return server, exec, caps
}

func TestFetchURLDeterministicGET(t *testing.T) {
	ctx := context.Background()
	server, exec, _ := directFixture(t)
	got, err := server.FetchURL(ctx, "https://careers.fixture.invalid/jobs/1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt.ID != "rc-fixture-1" || got.CaptureID != "cap-fixture-1" ||
		got.FinalURL != "https://careers.fixture.invalid/jobs/1" {
		t.Fatalf("fetch: %+v", got)
	}
	in := exec.last()
	if in.Kind != researchcontract.ExecuteFetch || in.Request.Operation != researchcontract.OperationFetch ||
		in.Request.Backend != researchexecute.BackendHTTP || in.Request.Method != "GET" ||
		in.RunID != "round-direct" || in.Generation != 1 {
		t.Fatalf("execute input: %+v", in)
	}
	if in.IdempotencyKey == "" {
		t.Fatal("fetch carries no idempotency key")
	}
	again, err := server.FetchURL(ctx, "https://careers.fixture.invalid/jobs/1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Receipt.ID != got.Receipt.ID || exec.last().IdempotencyKey != in.IdempotencyKey {
		t.Fatalf("repeat fetch is not stable: %+v %+v", again, exec.last())
	}
}

func TestFetchURLRejectsBeforeSpend(t *testing.T) {
	ctx := context.Background()
	server, exec, _ := directFixture(t)
	for _, url := range []string{"", "notaurl", "ftp://files.example.invalid/x", "https://"} {
		if _, err := server.FetchURL(ctx, url); err == nil {
			t.Fatalf("fetch %q accepted", url)
		}
	}
	if len(exec.calls) != 0 {
		t.Fatalf("executor calls = %d, want zero spend on invalid input", len(exec.calls))
	}
	exec.err = researchcontract.NewError(researchcontract.OutcomeNotFound, "url_or_query", "gone")
	if _, err := server.FetchURL(ctx, "https://careers.fixture.invalid/gone"); err == nil {
		t.Fatal("backend error accepted")
	}
}

func TestFetchURLNeedsCapturedOutcome(t *testing.T) {
	ctx := context.Background()
	server, exec, _ := directFixture(t)
	exec.output = researchcontract.ExecuteOutput{Outcome: researchcontract.OutcomeInvalid,
		Receipt: researchcontract.ExecutionReceipt{ID: "rc-bad", Status: researchcontract.ReceiptFailed}}
	if _, err := server.FetchURL(ctx, "https://careers.fixture.invalid/jobs/1"); err == nil {
		t.Fatal("non-OK outcome accepted")
	}
}

func TestSaveVacancyBindsReceiptAndDedupesURL(t *testing.T) {
	ctx := context.Background()
	server, _, _ := directFixture(t)
	vac, reused, err := server.SaveVacancy(ctx, SaveVacancyInput{
		PageURL: "https://careers.fixture.invalid/jobs/1", EmployerName: "Fixture BV",
		Title: "Support Engineer", LocationText: "Amsterdam", Receipt: "rc-fixture-1",
	})
	if err != nil || reused || vac.VacancyRef != "vac-0001" || vac.ReceiptRef != "rc-fixture-1" {
		t.Fatalf("save: %+v reused=%v err=%v", vac, reused, err)
	}
	again, reused, err := server.SaveVacancy(ctx, SaveVacancyInput{
		PageURL: "https://careers.fixture.invalid/jobs/1", EmployerName: "Renamed BV",
		Title: "Other title", Receipt: "rc-fixture-1",
	})
	if err != nil || !reused || again.VacancyRef != "vac-0001" || again.EmployerName != "Fixture BV" {
		t.Fatalf("re-save: %+v reused=%v err=%v, want the original ref", again, reused, err)
	}
	other, reused, err := server.SaveVacancy(ctx, SaveVacancyInput{
		PageURL: "https://careers.fixture.invalid/jobs/2", EmployerName: "Fixture BV",
		Title: "Helpdesk Lead", Receipt: "rc-fixture-1",
	})
	if err != nil || reused || other.VacancyRef != "vac-0002" {
		t.Fatalf("second URL: %+v reused=%v err=%v", other, reused, err)
	}
	if found, ok := server.VacancyByURL("https://careers.fixture.invalid/jobs/2"); !ok || found.VacancyRef != "vac-0002" {
		t.Fatalf("lookup: %+v %v", found, ok)
	}
	if _, ok := server.VacancyByURL("https://careers.fixture.invalid/jobs/9"); ok {
		t.Fatal("unknown URL found")
	}
}

func TestSaveVacancyRejectsUngrounded(t *testing.T) {
	ctx := context.Background()
	server, _, _ := directFixture(t)
	cases := []struct {
		name  string
		input SaveVacancyInput
		field string
	}{
		{"no employer", SaveVacancyInput{PageURL: "https://careers.fixture.invalid/jobs/1",
			Title: "T", Receipt: "rc-fixture-1"}, "employer_name"},
		{"no URL", SaveVacancyInput{EmployerName: "E", Title: "T", Receipt: "rc-fixture-1"}, "page_url"},
		{"bad URL", SaveVacancyInput{PageURL: "notaurl", EmployerName: "E", Title: "T",
			Receipt: "rc-fixture-1"}, "page_url"},
		{"no title", SaveVacancyInput{PageURL: "https://careers.fixture.invalid/jobs/1",
			EmployerName: "E", Receipt: "rc-fixture-1"}, "title"},
		{"no receipt", SaveVacancyInput{PageURL: "https://careers.fixture.invalid/jobs/1",
			EmployerName: "E", Title: "T"}, "receipt"},
		{"unknown receipt", SaveVacancyInput{PageURL: "https://careers.fixture.invalid/jobs/1",
			EmployerName: "E", Title: "T", Receipt: "rc-absent"}, "receipt"},
	}
	for _, tc := range cases {
		_, _, err := server.SaveVacancy(ctx, tc.input)
		var saveErr *SaveError
		if !errors.As(err, &saveErr) || saveErr.Field != tc.field {
			t.Errorf("%s: err = %v, want SaveError field %q", tc.name, err, tc.field)
		}
	}
	if len(server.SavedVacancyRefs()) != 0 {
		t.Fatalf("rejected saves persisted: %v", server.SavedVacancyRefs())
	}
}

func TestSaveQuestionNeedsSavedVacancy(t *testing.T) {
	ctx := context.Background()
	server, _, _ := directFixture(t)
	vac, _, err := server.SaveVacancy(ctx, SaveVacancyInput{
		PageURL: "https://careers.fixture.invalid/jobs/1", EmployerName: "Fixture BV",
		Title: "Support Engineer", Receipt: "rc-fixture-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	question, err := server.SaveQuestion(ctx, SaveQuestionInput{
		VacancyRef: vac.VacancyRef, PromptText: "Why this role?", Required: true,
		SourceURL: "https://careers.fixture.invalid/jobs/1",
	})
	if err != nil || question.QuestionRef != "q-0001" || !question.Required {
		t.Fatalf("save: %+v err=%v", question, err)
	}
	if _, err := server.SaveQuestion(ctx, SaveQuestionInput{VacancyRef: "vac-absent",
		PromptText: "Why?", SourceURL: "https://careers.fixture.invalid/jobs/1"}); err == nil {
		t.Fatal("unknown vacancy accepted")
	}
	if _, err := server.SaveQuestion(ctx, SaveQuestionInput{VacancyRef: vac.VacancyRef,
		SourceURL: "https://careers.fixture.invalid/jobs/1"}); err == nil {
		t.Fatal("empty prompt accepted")
	}
}
