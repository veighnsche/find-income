package publicresearch

import (
	"context"
	"reflect"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

func TestAccessorsEmptyServer(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	if _, ok := s.Vacancy(""); ok {
		t.Error("Vacancy(\"\") = true, want false")
	}
	if _, ok := s.Vacancy("vac-0001"); ok {
		t.Error("Vacancy on empty server = true, want false")
	}
	if _, ok := s.Question(""); ok {
		t.Error("Question(\"\") = true, want false")
	}
	if _, ok := s.Question("q-0001"); ok {
		t.Error("Question on empty server = true, want false")
	}
	if got := s.SavedVacancyRefs(); len(got) != 0 {
		t.Errorf("SavedVacancyRefs = %v, want empty", got)
	}
	if got := s.SavedQuestionRefs(); len(got) != 0 {
		t.Errorf("SavedQuestionRefs = %v, want empty", got)
	}
}

func TestAccessorsResolveSavedContent(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	ctx := context.Background()

	vacOut, err := s.saveVacancyTool(ctx, saveVacancyArgs{
		PageURL: "https://careers.novel-example.invalid/jobs/9", EmployerName: "Novel Port",
		Title: "Harbor Pilot", LocationText: "Rotterdam", Receipt: "rc-fixture-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if vacOut["outcome"] != "ok" {
		t.Fatalf("save vacancy outcome: %+v", vacOut)
	}
	savedVac, ok := vacOut["vacancy"].(musecode.PublicVacancy)
	if !ok {
		t.Fatalf("saved vacancy has wrong type: %+v", vacOut["vacancy"])
	}
	gotVac, ok := s.Vacancy(savedVac.VacancyRef)
	if !ok {
		t.Fatalf("Vacancy(%q) missed a saved ref", savedVac.VacancyRef)
	}
	if !reflect.DeepEqual(gotVac, savedVac) {
		t.Errorf("Vacancy content = %+v, want %+v", gotVac, savedVac)
	}

	qOut, err := s.saveQuestionTool(ctx, saveQuestionArgs{
		VacancyRef: savedVac.VacancyRef, PromptText: "Why this role?",
		SourceURL: "https://careers.novel-example.invalid/apply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if qOut["outcome"] != "ok" {
		t.Fatalf("save question outcome: %+v", qOut)
	}
	savedQ, ok := qOut["question"].(musecode.PublicQuestion)
	if !ok {
		t.Fatalf("saved question has wrong type: %+v", qOut["question"])
	}
	gotQ, ok := s.Question(savedQ.QuestionRef)
	if !ok {
		t.Fatalf("Question(%q) missed a saved ref", savedQ.QuestionRef)
	}
	if !reflect.DeepEqual(gotQ, savedQ) {
		t.Errorf("Question content = %+v, want %+v", gotQ, savedQ)
	}

	if refs := s.SavedVacancyRefs(); !reflect.DeepEqual(refs, []string{savedVac.VacancyRef}) {
		t.Errorf("SavedVacancyRefs = %v", refs)
	}
	if refs := s.SavedQuestionRefs(); !reflect.DeepEqual(refs, []string{savedQ.QuestionRef}) {
		t.Errorf("SavedQuestionRefs = %v", refs)
	}
}

func TestAccessorsMissesAndOrder(t *testing.T) {
	exec, caps := testFixtures()
	s := testServer(t, testBounds(), exec, caps)
	ctx := context.Background()

	refs := make([]string, 0, 3)
	for _, page := range []string{"jobs/1", "jobs/2", "jobs/3"} {
		out, err := s.saveVacancyTool(ctx, saveVacancyArgs{
			PageURL: "https://careers.novel-example.invalid/" + page,
			Title:   "Role " + page, Receipt: "rc-fixture-1",
			EmployerName: "Novel Port",
		})
		if err != nil || out["outcome"] != "ok" {
			t.Fatalf("save %s: %+v %v", page, out, err)
		}
		refs = append(refs, out["vacancy"].(musecode.PublicVacancy).VacancyRef)
	}
	qRefs := make([]string, 0, 2)
	for _, prompt := range []string{"Why this role?", "When can you start?"} {
		out, err := s.saveQuestionTool(ctx, saveQuestionArgs{
			VacancyRef: refs[0], PromptText: prompt,
			SourceURL: "https://careers.novel-example.invalid/apply",
		})
		if err != nil || out["outcome"] != "ok" {
			t.Fatalf("save question: %+v %v", out, err)
		}
		qRefs = append(qRefs, out["question"].(musecode.PublicQuestion).QuestionRef)
	}

	if got := s.SavedVacancyRefs(); !reflect.DeepEqual(got, refs) {
		t.Errorf("SavedVacancyRefs = %v, want %v", got, refs)
	}
	if again := s.SavedVacancyRefs(); !reflect.DeepEqual(again, refs) {
		t.Errorf("SavedVacancyRefs unstable: %v then %v", refs, again)
	}
	if got := s.SavedQuestionRefs(); !reflect.DeepEqual(got, qRefs) {
		t.Errorf("SavedQuestionRefs = %v, want %v", got, qRefs)
	}

	// Returned slices are copies: mutating them must not affect the store.
	mut := s.SavedVacancyRefs()
	mut[0] = "vac-corrupted"
	if got := s.SavedVacancyRefs(); !reflect.DeepEqual(got, refs) {
		t.Errorf("store mutated through returned slice: %v", got)
	}

	for _, ref := range []string{"", "vac-9999", "q-9999", "nope"} {
		if _, ok := s.Vacancy(ref); ok {
			t.Errorf("Vacancy(%q) = true, want false", ref)
		}
		if _, ok := s.Question(ref); ok {
			t.Errorf("Question(%q) = true, want false", ref)
		}
	}
	// Cross-type refs must miss.
	if _, ok := s.Vacancy(qRefs[0]); ok {
		t.Errorf("Vacancy(%q) matched a question ref", qRefs[0])
	}
	if _, ok := s.Question(refs[0]); ok {
		t.Errorf("Question(%q) matched a vacancy ref", refs[0])
	}
}
