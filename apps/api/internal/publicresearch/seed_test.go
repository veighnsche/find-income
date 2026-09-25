package publicresearch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// seedVacancy builds a valid seed entry bound to the shared fixture receipt.
func seedVacancy(pageURL, title string) SeedVacancy {
	return SeedVacancy{
		PageURL: pageURL, EmployerName: "Seed Ltd", Title: title,
		LocationText: "Rotterdam", PostedText: "today", ReceiptRef: "rc-fixture-1",
	}
}

// seedFailure runs one seed batch and unpacks the expected *SeedError.
func seedFailure(t *testing.T, s *Server, batch []SeedVacancy) *SeedError {
	t.Helper()
	_, err := s.SeedVacancies(batch)
	if err == nil {
		t.Fatalf("seed %+v succeeded, want failure", batch)
	}
	var serr *SeedError
	if !errors.As(err, &serr) {
		t.Fatalf("seed error = %T %v, want *SeedError", err, err)
	}
	return serr
}

func savedVacancyCount(s *Server) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.vacancies)
}

func TestSeedVacanciesEmptyInput(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	for _, batch := range [][]SeedVacancy{nil, {}} {
		got, err := s.SeedVacancies(batch)
		if err != nil {
			t.Fatalf("empty seed: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("empty seed = %+v, want empty non-nil", got)
		}
	}
	if calls, accounted := s.Usage(); calls != 0 || accounted != 0 {
		t.Errorf("empty seed consumed calls=%d bytes=%d, want zero", calls, accounted)
	}
	if n := savedVacancyCount(s); n != 0 {
		t.Errorf("empty seed saved %d vacancies", n)
	}
}

func TestSeedVacanciesSuccess(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	batch := []SeedVacancy{
		seedVacancy("https://careers.seed.invalid/jobs/1", "Harbor Pilot"),
		seedVacancy("https://careers.seed.invalid/jobs/2", "Night Mate"),
	}
	got, err := s.SeedVacancies(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].VacancyRef != "vac-0001" || got[1].VacancyRef != "vac-0002" {
		t.Fatalf("seed refs = %+v, want vac-0001/vac-0002 in order", got)
	}
	for i, vac := range got {
		want := batch[i]
		if vac.PageURL != want.PageURL || vac.EmployerName != want.EmployerName ||
			vac.Title != want.Title || vac.LocationText != want.LocationText ||
			vac.PostedText != want.PostedText || vac.ReceiptRef != want.ReceiptRef {
			t.Errorf("seed %d = %+v, want projection of %+v", i, vac, want)
		}
		if vac.SourceHost != "careers.seed.invalid" {
			t.Errorf("seed %d host = %q, want derived from page URL", i, vac.SourceHost)
		}
		if vac.CapturedAt == "" {
			t.Errorf("seed %d has no capture stamp", i)
		}
	}
	if calls, accounted := s.Usage(); calls != 2 || accounted <= 0 {
		t.Errorf("seed usage = calls=%d bytes=%d, want 2 calls and charged bytes", calls, accounted)
	}
	if n := savedVacancyCount(s); n != 2 {
		t.Errorf("saved = %d vacancies, want 2", n)
	}
}

func TestSeedValidationParityWithTool(t *testing.T) {
	cases := []struct {
		name      string
		batch     []SeedVacancy
		wantIndex int
		wantField string
	}{
		{"bad url shape", []SeedVacancy{seedVacancy("not-a-url", "Pilot")}, 0, "page_url"},
		{"missing scheme host", []SeedVacancy{seedVacancy("ftp://x.invalid/j", "Pilot")}, 0, "page_url"},
		{"empty title", []SeedVacancy{func() SeedVacancy { v := seedVacancy("https://careers.seed.invalid/j", "  "); return v }()}, 0, "title"},
		{"empty receipt", []SeedVacancy{func() SeedVacancy {
			v := seedVacancy("https://careers.seed.invalid/j", "Pilot")
			v.ReceiptRef = "  "
			return v
		}()}, 0, "receipt"},
		{"untrusted receipt", []SeedVacancy{func() SeedVacancy {
			v := seedVacancy("https://careers.seed.invalid/j", "Pilot")
			v.ReceiptRef = "rc-forged"
			return v
		}()}, 0, "receipt"},
		{"second entry bad", []SeedVacancy{
			seedVacancy("https://careers.seed.invalid/jobs/1", "Pilot"),
			seedVacancy("://bad", "Mate"),
		}, 1, "page_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec, caps := testFixtures()
			s := testServer(t, testBounds(), exec, caps)
			serr := seedFailure(t, s, tc.batch)
			if serr.Index != tc.wantIndex || serr.Field != tc.wantField {
				t.Errorf("seed error = %+v, want index=%d field=%q", serr, tc.wantIndex, tc.wantField)
			}
			if n := savedVacancyCount(s); n != 0 {
				t.Errorf("failed seed left %d partial saves", n)
			}
			if calls, _ := s.Usage(); calls != 0 {
				t.Errorf("failed seed consumed %d calls, want zero allowance", calls)
			}
		})
	}
}

func TestSeedReceiptWithoutCapture(t *testing.T) {
	exec, caps := testFixtures()
	caps.receipts["rc-hollow"] = researchcontract.ExecutionReceipt{
		ID: "rc-hollow", Status: researchcontract.ReceiptOK,
	}
	s := testServer(t, testBounds(), exec, caps)
	bad := seedVacancy("https://careers.seed.invalid/jobs/1", "Pilot")
	bad.ReceiptRef = "rc-hollow"
	serr := seedFailure(t, s, []SeedVacancy{bad})
	if serr.Index != 0 || serr.Field != "receipt" {
		t.Errorf("hollow receipt error = %+v, want index=0 field=receipt", serr)
	}
	if n := savedVacancyCount(s); n != 0 {
		t.Errorf("hollow receipt left %d partial saves", n)
	}
}

func TestSeedBoundBreach(t *testing.T) {
	t.Run("tool calls", func(t *testing.T) {
		exec, caps := testFixtures()
		bounds := testBounds()
		bounds.MaxToolCalls = 2
		s := testServer(t, bounds, exec, caps)
		serr := seedFailure(t, s, []SeedVacancy{
			seedVacancy("https://careers.seed.invalid/jobs/1", "One"),
			seedVacancy("https://careers.seed.invalid/jobs/2", "Two"),
			seedVacancy("https://careers.seed.invalid/jobs/3", "Three"),
		})
		if serr.Index != 2 || serr.Field != "tool_calls" {
			t.Errorf("tool-call breach = %+v, want index=2 field=tool_calls", serr)
		}
		if n := savedVacancyCount(s); n != 0 {
			t.Errorf("breached seed left %d partial saves", n)
		}
		if calls, _ := s.Usage(); calls != 0 {
			t.Errorf("breached seed consumed %d calls", calls)
		}
	})
	t.Run("exhausted server", func(t *testing.T) {
		exec, caps := testFixtures()
		bounds := testBounds()
		bounds.MaxToolCalls = 1
		s := testServer(t, bounds, exec, caps)
		if _, err := s.SeedVacancies([]SeedVacancy{seedVacancy("https://careers.seed.invalid/jobs/1", "One")}); err != nil {
			t.Fatal(err)
		}
		serr := seedFailure(t, s, []SeedVacancy{seedVacancy("https://careers.seed.invalid/jobs/2", "Two")})
		if serr.Index != 0 || serr.Field != "tool_calls" {
			t.Errorf("exhausted seed error = %+v, want index=0 field=tool_calls", serr)
		}
		if n := savedVacancyCount(s); n != 1 {
			t.Errorf("saved = %d vacancies, want only the first batch", n)
		}
	})
	t.Run("bytes per op", func(t *testing.T) {
		exec, caps := testFixtures()
		bounds := testBounds()
		bounds.MaxBytesPerOp = 100
		bounds.MaxBytesTotal = 200
		s := testServer(t, bounds, exec, caps)
		serr := seedFailure(t, s, []SeedVacancy{seedVacancy("https://careers.seed.invalid/jobs/1", "Pilot")})
		if serr.Index != 0 || serr.Field != "bytes_per_op" {
			t.Errorf("per-op breach = %+v, want index=0 field=bytes_per_op", serr)
		}
		if n := savedVacancyCount(s); n != 0 {
			t.Errorf("breached seed left %d partial saves", n)
		}
	})
	t.Run("bytes total", func(t *testing.T) {
		exec, caps := testFixtures()
		bounds := testBounds()
		bounds.MaxBytesPerOp = 1 << 20
		bounds.MaxBytesTotal = 1 << 20
		s := testServer(t, bounds, exec, caps)
		batch := make([]SeedVacancy, 5)
		for i := range batch {
			batch[i] = seedVacancy("https://careers.seed.invalid/jobs/big", strings.Repeat("p", 300000))
		}
		serr := seedFailure(t, s, batch)
		if serr.Field != "bytes_total" || serr.Index != 3 {
			t.Errorf("total breach = %+v, want index=3 field=bytes_total", serr)
		}
		if n := savedVacancyCount(s); n != 0 {
			t.Errorf("breached seed left %d partial saves", n)
		}
	})
}

func TestSeedAtomicityAcrossBatch(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	bad := seedVacancy("https://careers.seed.invalid/jobs/2", "Mate")
	bad.ReceiptRef = "rc-forged"
	serr := seedFailure(t, s, []SeedVacancy{
		seedVacancy("https://careers.seed.invalid/jobs/1", "Pilot"),
		bad,
		seedVacancy("https://careers.seed.invalid/jobs/3", "Keeper"),
	})
	if serr.Index != 1 {
		t.Errorf("atomicity error index = %d, want 1", serr.Index)
	}
	if n := savedVacancyCount(s); n != 0 {
		t.Errorf("failed batch left %d partial saves, want zero", n)
	}
}

func TestSeedVacanciesConcurrent(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.SeedVacancies([]SeedVacancy{seedVacancy("https://careers.seed.invalid/jobs/c", "Concurrent")})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.vacancies) != 8 || len(s.vacOrder) != 8 {
		t.Fatalf("concurrent seeds saved %d vacancies, want 8", len(s.vacancies))
	}
	seen := map[string]bool{}
	for _, ref := range s.vacOrder {
		if seen[ref] {
			t.Errorf("duplicate ref %q under concurrency", ref)
		}
		seen[ref] = true
	}
}

// assertSpanCorrectness proves every recorded span satisfies Start >= 0,
// End > Start, and span bytes equal to the linked saved prompt.
func assertSpanCorrectness(t *testing.T, role RoleCheck, data []byte) {
	t.Helper()
	if len(role.Sources) != len(role.Questions) {
		t.Fatalf("sources=%d questions=%d, want one span per saved question", len(role.Sources), len(role.Questions))
	}
	prompts := map[string]string{}
	for _, q := range role.Questions {
		prompts[q.QuestionRef] = q.PromptText
	}
	for _, src := range role.Sources {
		prompt, ok := prompts[src.QuestionRef]
		if !ok {
			t.Errorf("span %+v links no saved question", src)
			continue
		}
		if src.CaptureID == "" {
			t.Errorf("span %+v has no capture", src)
		}
		if src.Start < 0 || src.End <= src.Start || src.End > len(data) {
			t.Errorf("span %+v out of bounds for %d bytes", src, len(data))
			continue
		}
		if string(data[src.Start:src.End]) != prompt {
			t.Errorf("span %+v quotes %q, want prompt %q", src, string(data[src.Start:src.End]), prompt)
		}
	}
}

func assertQuestionSourcesJSON(t *testing.T, role RoleCheck) {
	t.Helper()
	raw, err := json.Marshal(role)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	spans, ok := payload["question_sources"].([]any)
	if !ok {
		t.Fatalf("role JSON lacks question_sources: %s", raw)
	}
	if len(spans) != len(role.Questions) {
		t.Errorf("question_sources=%d questions=%d in %s", len(spans), len(role.Questions), raw)
	}
}

// Seeded vacancies must flow through the reuse path with a span per question.
func TestCheckRecordsSpansOnReusePath(t *testing.T) {
	exec, caps := testFixtures()
	data := []byte("Seeded role at Seed Ltd\nAre you eligible to work in this location?\nWhat is your notice period (required)?\nWe offer hybrid work.\n")
	caps.blobs["cap-fixture-1"] = fakeBlob{
		desc: researchcontract.Capture{ID: "cap-fixture-1", ContentType: "text/html", Complete: true},
		data: data,
	}
	saved := testServer(t, testBounds(), exec, caps)
	seeded, err := saved.SeedVacancies([]SeedVacancy{seedVacancy("https://careers.seed.invalid/jobs/1", "Seeded role")})
	if err != nil {
		t.Fatal(err)
	}
	svc := checkService(t, exec, caps, saved)
	report, err := svc.Check(context.Background(), NewSelection(seeded[0].VacancyRef))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Roles) != 1 || report.Roles[0].Outcome != RoleChecked {
		t.Fatalf("report = %+v", report.Roles)
	}
	role := report.Roles[0]
	if !role.ReusedCapture || len(role.Questions) != 2 {
		t.Fatalf("role = %+v, want reused capture with two questions", role)
	}
	assertSpanCorrectness(t, role, data)
	for _, src := range role.Sources {
		if src.CaptureID != "cap-fixture-1" {
			t.Errorf("span %+v pins %q, want the reused capture", src, src.CaptureID)
		}
	}
	assertQuestionSourcesJSON(t, role)
}

// Seeded vacancies must flow through the fetch path with a span per question.
func TestCheckRecordsSpansOnFetchPath(t *testing.T) {
	data := []byte("Fetched role\nWhen can you start?\n")
	fetchCaps := &fakeCaptures{
		receipts: map[string]researchcontract.ExecutionReceipt{},
		blobs: map[string]fakeBlob{
			"cap-fetched": {
				desc: researchcontract.Capture{ID: "cap-fetched", Complete: true},
				data: data,
			},
		},
	}
	exec := &scriptExecutor{
		outputs: map[string]researchcontract.ExecuteOutput{
			"https://careers.seed.invalid/fetched": {
				Outcome:   researchcontract.OutcomeOK,
				CaptureID: "cap-fetched",
				Receipt: researchcontract.ExecutionReceipt{
					ID: "rc-fetched", Status: researchcontract.ReceiptOK,
					CaptureID: "cap-fetched", FinalURL: "https://careers.seed.invalid/fetched",
				},
			},
		},
	}
	// Seed against a receipt the check-time reader cannot resolve, so the
	// role takes the fetch path like TestBlockedRoleDoesNotStopOthers.
	seedCaps := &fakeCaptures{
		receipts: map[string]researchcontract.ExecutionReceipt{
			"rc-seed-fetch": {ID: "rc-seed-fetch", Status: researchcontract.ReceiptOK, CaptureID: "cap-seed"},
		},
		blobs: map[string]fakeBlob{},
	}
	seedExec, _ := testFixtures()
	saved := testServer(t, testBounds(), seedExec, seedCaps)
	seed := seedVacancy("https://careers.seed.invalid/fetched", "Fetched role")
	seed.ReceiptRef = "rc-seed-fetch"
	seeded, err := saved.SeedVacancies([]SeedVacancy{seed})
	if err != nil {
		t.Fatal(err)
	}
	svc := checkService(t, exec, fetchCaps, saved)
	report, err := svc.Check(context.Background(), NewSelection(seeded[0].VacancyRef))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Roles) != 1 || report.Roles[0].Outcome != RoleChecked {
		t.Fatalf("report = %+v", report.Roles)
	}
	role := report.Roles[0]
	if role.ReusedCapture {
		t.Error("fetch-path role reported reused capture")
	}
	if len(role.Questions) != 1 {
		t.Fatalf("questions = %+v, want the one verbatim interrogative", role.Questions)
	}
	assertSpanCorrectness(t, role, data)
	if role.Sources[0].CaptureID != "cap-fetched" {
		t.Errorf("span %+v pins %q, want the fetched capture", role.Sources[0], role.Sources[0].CaptureID)
	}
	assertQuestionSourcesJSON(t, role)
}
