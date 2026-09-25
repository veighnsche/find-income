package store

import (
	"context"
	"errors"
	"testing"
)

func TestSaveJobCheckBodyCompletesPendingCheck(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-body")
	start := CheckStartInput{RequestKey: "check-body", ExpectedOpportunityRevision: opportunity.Revision, ExpectedWorkflowRevision: 0}
	pending, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, start)
	if err != nil || !created {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	view, err := s.SaveJobCheckBody(ctx, ownerActor(), checkSaveFixture(opportunity.ID, pending.ID, capture.ID))
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != CheckStatusChecked || len(view.Questions) != 2 || view.QuestionSetSHA256 == "" || view.QuestionSetVersion != 1 {
		t.Fatalf("view = %+v, want checked with 2 sourced questions", view)
	}
	if view.Questions[0].TextSHA256 == "" || view.Questions[0].SourceSpan.CaptureID != capture.ID {
		t.Fatalf("question = %+v, want sourced span", view.Questions[0])
	}
	reloaded, err := s.GetJobCheck(ctx, opportunity.ID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != CheckStatusChecked || len(reloaded.Questions) != 2 {
		t.Fatalf("reloaded = %+v, want persisted questions", reloaded)
	}
	if _, err := s.SaveJobCheckBody(ctx, ownerActor(), checkSaveFixture(opportunity.ID, pending.ID, capture.ID)); !errors.Is(err, ErrConflict) {
		t.Fatalf("second save err = %v, want conflict", err)
	}
}

func TestSaveJobCheckBodyGuards(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-guards")
	start := CheckStartInput{RequestKey: "check-guards", ExpectedOpportunityRevision: opportunity.Revision, ExpectedWorkflowRevision: 0}
	pending, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, start)
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/2")
	save := checkSaveFixture(opportunity.ID, pending.ID, capture.ID)
	if _, err := s.SaveJobCheckBody(ctx, Actor{Kind: "agent", ID: "x"}, save); !errors.Is(err, ErrFenced) {
		t.Fatalf("agent actor err = %v, want fenced", err)
	}
	incomplete := save
	incomplete.Questions = nil
	if _, err := s.SaveJobCheckBody(ctx, ownerActor(), incomplete); !errors.Is(err, ErrInvalid) {
		t.Fatalf("questionless save err = %v, want invalid", err)
	}
	blocked := save
	blocked.Questions = nil
	blocked.Blocked = &CheckBlockedInput{Code: CheckBlockedQuestionsUnresolved, Detail: "capture held no questions"}
	view, err := s.SaveJobCheckBody(ctx, ownerActor(), blocked)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != CheckStatusBlocked || view.BlockedReason == nil || view.BlockedReason.Code != CheckBlockedQuestionsUnresolved {
		t.Fatalf("view = %+v, want blocked with code", view)
	}
}
