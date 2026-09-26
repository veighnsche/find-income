package store

import (
	"context"
	"errors"
	"testing"
)

func clarificationOpenFixture() ClarificationOpenInput {
	return ClarificationOpenInput{RequestKey: "clar-req-1", CheckID: "check-1",
		Requirement: ClarificationRequirement{Statement: "must hold a forklift certificate",
			CaptureID: "cap-1", SpanStart: 10, SpanEnd: 46},
		Prompt:       "Do you hold a forklift certificate? If so, which one and since when?",
		AffectedWork: []ClarificationWorkRef{{Kind: "artifact", ID: "art-cv"}, {Kind: "artifact", ID: "art-letter"}}}
}

func TestOwnerClarificationOpenAnswerResolveOnce(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-clar")
	view := saveReadyCheck(t, s, opportunity, "clar", nil)

	input := clarificationOpenFixture()
	input.CheckID = view.ID
	opened, created, err := s.OpenOwnerClarification(ctx, ownerActor(), opportunity.ID, input)
	if err != nil || !created {
		t.Fatalf("open=%+v err=%v, want a created clarification", opened, err)
	}
	if opened.Origin != "owner_clarification" || opened.Status != ClarificationOpen ||
		len(opened.AffectedWork) != 2 || opened.CreatedAt == "" {
		t.Fatalf("opened=%+v, want an open owner clarification", opened)
	}

	replayed, created, err := s.OpenOwnerClarification(ctx, ownerActor(), opportunity.ID, input)
	if err != nil || created || replayed.ID != opened.ID {
		t.Fatalf("replay=%+v created=%v err=%v, want idempotent open", replayed, created, err)
	}

	changed := input
	changed.Prompt = "A different prompt for the same key."
	if _, _, err := s.OpenOwnerClarification(ctx, ownerActor(), opportunity.ID, changed); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed-input replay: %v, want idempotency conflict", err)
	}

	answered, err := s.AnswerOwnerClarification(ctx, ownerActor(), opened.ID,
		ClarificationAnswer{RequestKey: "clar-ans-1", Text: "RTITB counterbalance since 2019."})
	if err != nil || answered.Status != ClarificationAnswered ||
		answered.Answer != "RTITB counterbalance since 2019." ||
		answered.AnsweredBy.ID != "owner" || answered.AnsweredAt == "" {
		t.Fatalf("answered=%+v err=%v, want the exact saved answer", answered, err)
	}

	same, err := s.AnswerOwnerClarification(ctx, ownerActor(), opened.ID,
		ClarificationAnswer{RequestKey: "clar-ans-2", Text: "RTITB counterbalance since 2019."})
	if err != nil || same.ID != opened.ID || same.Answer != answered.Answer {
		t.Fatalf("same-text answer=%+v err=%v, want idempotent replay", same, err)
	}
	if _, err := s.AnswerOwnerClarification(ctx, ownerActor(), opened.ID,
		ClarificationAnswer{RequestKey: "clar-ans-3", Text: "A different answer."}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second answer: %v, want conflict instead of forked truth", err)
	}

	got, err := s.GetOwnerClarification(ctx, opened.ID)
	if err != nil || got.ID != opened.ID || got.Answer != answered.Answer {
		t.Fatalf("get=%+v err=%v, want the answered clarification", got, err)
	}
	items, err := s.ListOwnerClarifications(ctx, opportunity.ID)
	if err != nil || len(items) != 1 || items[0].ID != opened.ID {
		t.Fatalf("list=%+v err=%v, want the one job clarification", items, err)
	}
	other, err := s.ListOwnerClarifications(ctx, "opp-other")
	if err != nil || len(other) != 0 {
		t.Fatalf("other-job list=%+v err=%v, want empty job-scoped list", other, err)
	}
}

func TestOwnerClarificationGuards(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	unselected, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	input := clarificationOpenFixture()
	if _, _, err := s.OpenOwnerClarification(ctx, ownerActor(), unselected.ID, input); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected open: %v, want role-not-selected", err)
	}
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-clar-guard")
	input.CheckID = "missing-check"
	if _, _, err := s.OpenOwnerClarification(ctx, ownerActor(), opportunity.ID, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing check: %v, want not found", err)
	}
	if _, err := s.AnswerOwnerClarification(ctx, ownerActor(), "missing",
		ClarificationAnswer{RequestKey: "k", Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing answer: %v, want not found", err)
	}
	if _, err := s.GetOwnerClarification(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing get: %v, want not found", err)
	}
}
