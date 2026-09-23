package organisation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jobs"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type evaluatorFunc func(context.Context, jev.Request) (jev.Result, error)

func (f evaluatorFunc) Evaluate(ctx context.Context, request jev.Request) (jev.Result, error) {
	return f(ctx, request)
}

func selectedEvaluator(choice string) evaluatorFunc {
	return func(_ context.Context, request jev.Request) (jev.Result, error) {
		var questionID string
		var criteria map[string]string
		for id, value := range request.Questions {
			questionID = id
			criteria = value.(jev.ChoiceQuestion).Criteria
		}
		probabilities := map[string]float64{}
		for option := range criteria {
			probabilities[option] = 0
		}
		probabilities[choice] = 0.8
		for option := range probabilities {
			if option != choice {
				probabilities[option] = 0.2
				break
			}
		}
		return jev.Result{RequestedModel: "jev-1.13.0", ReturnedModel: "jev-1.13.0",
			Usage: jev.Usage{InputTokens: 30, OutputTokens: 5}, Answers: map[string]jev.Answer{
				questionID: {Type: "choice", Choice: &jev.ChoiceAnswer{Choice: choice, Probabilities: probabilities, Confidence: 0.8}}}}, nil
	}
}

func setupOrganisedIntake(t *testing.T) (*store.Store, store.IngestionRequest, store.Opportunity) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	_, err = s.UpdateOrganisationCategories(ctx, 1, []store.OrganisationCategory{
		{ID: "backend", Description: "Backend and platform service openings."},
		{ID: "other", Description: "Openings in other work directions."}}, owner)
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := s.SubmitIngestion(ctx, owner, store.IngestionInput{Origin: "owner", OriginalText: "Build Go backend services at 32 hours.", IdempotencyKey: "organise-1"})
	if err != nil {
		t.Fatal(err)
	}
	claim, found, err := s.ClaimNextJob(ctx, "test-ingestion", []string{store.IngestionJobKind}, time.Minute, time.Now())
	if err != nil || !found {
		t.Fatalf("ingestion claim: %v %v", found, err)
	}
	opportunity, _, err := s.SaveIngestionOpportunity(ctx, claim, store.IngestionRecordInput{
		NewCompany: &store.CompanyInput{Name: "Example Systems"}, Opportunity: store.OpportunityInput{
			Title: "Backend Engineer", Kind: "employment", Compensation: store.AdvertisedCompensation{
				Currency: "unknown", Period: "unknown", Basis: "unknown"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteIngestionProcessing(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.CompleteJob(ctx, claim, store.JobResult{Ref: opportunity.ID}, time.Now()); err != nil || !applied {
		t.Fatalf("ingestion complete: %v %v", applied, err)
	}
	return s, item, opportunity
}

func TestAutomaticOrganisationKeepsFactualStateSeparate(t *testing.T) {
	ctx := context.Background()
	s, intake, opportunity := setupOrganisedIntake(t)
	read, err := s.Ingestion(ctx, intake.ID)
	if err != nil || read.OrganisationJobID == "" || read.Status != "completed" {
		t.Fatalf("organisation not queued: %+v %v", read, err)
	}
	worker := jobs.Worker{Queue: s, ID: "test-organisation", Handlers: map[string]jobs.Handler{
		store.OrganisationJobKind: Handler(s, selectedEvaluator("backend"))}, PollInterval: time.Millisecond, LeaseDuration: time.Minute}
	if processed, err := worker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process: %v %v", processed, err)
	}
	view, err := s.Organisation(ctx, opportunity.ID)
	if err != nil || view.Status != "selected" || view.Current == nil || view.Current.CategoryID != "backend" ||
		view.Current.CategoryVersion != 2 || len(view.Current.SourceRefsJSON) == 0 {
		t.Fatalf("selection: %+v %v", view, err)
	}
	summaries, err := s.OrganisationSummaries(ctx, []string{opportunity.ID})
	if err != nil || summaries[opportunity.ID].Status != view.Status || summaries[opportunity.ID].CategoryID != "backend" {
		t.Fatalf("bulk selection: %+v %v", summaries, err)
	}
	stored, err := s.Opportunity(ctx, opportunity.ID)
	if err != nil || stored.Stage != "discovered" {
		t.Fatalf("organisation changed stage: %+v %v", stored, err)
	}
	changed := "Different role duties after source correction."
	_, _, err = s.PatchOpportunity(ctx, store.Actor{Kind: "administrator", ID: "owner"}, opportunity.ID,
		store.OpportunityPatch{ExpectedRevision: 1, OriginalText: &changed})
	if err != nil {
		t.Fatal(err)
	}
	view, err = s.Organisation(ctx, opportunity.ID)
	if err != nil || view.Current != nil || view.LatestHistorical == nil || view.Status != "outdated" {
		t.Fatalf("stale category remained current: %+v %v", view, err)
	}
	summaries, err = s.OrganisationSummaries(ctx, []string{opportunity.ID})
	if err != nil || summaries[opportunity.ID].Status != view.Status || summaries[opportunity.ID].CategoryID != "" {
		t.Fatalf("bulk stale status: %+v %v", summaries, err)
	}
}

func TestUncertainAndChangedCategoriesRemainVisible(t *testing.T) {
	ctx := context.Background()
	s, _, opportunity := setupOrganisedIntake(t)
	worker := jobs.Worker{Queue: s, ID: "test-organisation", Handlers: map[string]jobs.Handler{
		store.OrganisationJobKind: Handler(s, selectedEvaluator("__uncertain__"))}, PollInterval: time.Millisecond, LeaseDuration: time.Minute}
	if processed, err := worker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process: %v %v", processed, err)
	}
	view, err := s.Organisation(ctx, opportunity.ID)
	if err != nil || view.Status != "uncertain" || view.Current == nil || view.Current.CategoryID != "" {
		t.Fatalf("uncertainty forced into category: %+v %v", view, err)
	}
	summaries, err := s.OrganisationSummaries(ctx, []string{opportunity.ID})
	if err != nil || summaries[opportunity.ID].Status != view.Status || summaries[opportunity.ID].CategoryID != "" {
		t.Fatalf("bulk uncertainty: %+v %v", summaries, err)
	}
	_, err = s.UpdateOrganisationCategories(ctx, 2, []store.OrganisationCategory{{ID: "platform", Description: "Platform engineering roles."}},
		store.Actor{Kind: "administrator", ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	view, err = s.Organisation(ctx, opportunity.ID)
	if err != nil || view.Current != nil || view.LatestHistorical == nil || view.Status != "pending" {
		t.Fatalf("category edit reused old assessment: %+v %v", view, err)
	}
}

func TestCategoryChangeDuringEvaluationDiscardsStaleAnswer(t *testing.T) {
	ctx := context.Background()
	s, intake, opportunity := setupOrganisedIntake(t)
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	evaluator := evaluatorFunc(func(ctx context.Context, request jev.Request) (jev.Result, error) {
		_, err := s.UpdateOrganisationCategories(ctx, 2,
			[]store.OrganisationCategory{{ID: "platform", Description: "Platform engineering roles."}}, owner)
		if err != nil {
			return jev.Result{}, err
		}
		return selectedEvaluator("backend").Evaluate(ctx, request)
	})
	worker := jobs.Worker{Queue: s, ID: "test-organisation", Handlers: map[string]jobs.Handler{
		store.OrganisationJobKind: Handler(s, evaluator)}, PollInterval: time.Millisecond, LeaseDuration: time.Minute}
	if processed, err := worker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process: %v %v", processed, err)
	}
	view, err := s.Organisation(ctx, opportunity.ID)
	if err != nil || view.Current != nil || view.Status != "pending" || view.JobID == "" {
		t.Fatalf("stale answer became current: %+v %v", view, err)
	}
	read, err := s.Ingestion(ctx, intake.ID)
	if err != nil || read.OrganisationJobID != view.JobID {
		t.Fatalf("replacement job not linked: %+v %v", read, err)
	}
}

func TestSourceChangeDuringEvaluationDiscardsStaleAnswer(t *testing.T) {
	ctx := context.Background()
	s, _, opportunity := setupOrganisedIntake(t)
	evaluator := evaluatorFunc(func(ctx context.Context, request jev.Request) (jev.Result, error) {
		changed := "The saved vacancy now describes a different role."
		_, _, err := s.PatchOpportunity(ctx, store.Actor{Kind: "administrator", ID: "owner"}, opportunity.ID,
			store.OpportunityPatch{ExpectedRevision: 1, OriginalText: &changed})
		if err != nil {
			return jev.Result{}, err
		}
		return selectedEvaluator("backend").Evaluate(ctx, request)
	})
	worker := jobs.Worker{Queue: s, ID: "test-organisation", Handlers: map[string]jobs.Handler{
		store.OrganisationJobKind: Handler(s, evaluator)}, PollInterval: time.Millisecond, LeaseDuration: time.Minute}
	if processed, err := worker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process: %v %v", processed, err)
	}
	view, err := s.Organisation(ctx, opportunity.ID)
	if err != nil || view.Current != nil || view.Status != "outdated" {
		t.Fatalf("changed source received stale answer: %+v %v", view, err)
	}
}

func TestLongSourceUsesBoundedExcerptsAcrossWholeText(t *testing.T) {
	text := "Beginning: backend role. " + strings.Repeat("é", 25000) + " Ending: platform role."
	facts, err := sourcedFacts(store.OrganisationSnapshot{OriginalText: text, SourceID: "source-1", SourceRevision: "revision-1"})
	if err != nil || len(facts) != maxFacts {
		t.Fatalf("bounded source facts: count=%d err=%v", len(facts), err)
	}
	if !strings.Contains(facts[0].Excerpt, "Beginning:") || !strings.Contains(facts[len(facts)-1].Excerpt, "Ending:") {
		t.Fatalf("source ends missing from excerpts: first=%q last=%q", facts[0].Excerpt, facts[len(facts)-1].Excerpt)
	}
	for _, fact := range facts {
		if len(fact.Excerpt) > maxFactBytes || !strings.Contains(text, fact.Excerpt) {
			t.Fatalf("invalid source excerpt: %+v", fact)
		}
	}
}
