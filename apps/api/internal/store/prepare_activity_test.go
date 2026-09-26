package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPrepareActivityJournalAndPage(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-prep-activity")
	if _, err := s.RecordPrepareActivity(ctx, ownerActor(), opportunity.ID, PrepareActivityInput{
		Kind: "prepare.unknown"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("kind: %v", err)
	}
	if _, err := s.RecordPrepareActivity(ctx, ownerActor(), opportunity.ID, PrepareActivityInput{
		Kind: PrepareTurnStarted, Payload: json.RawMessage(`{broken}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("payload: %v", err)
	}
	if _, err := s.RecordPrepareActivity(ctx, ownerActor(), opportunity.ID, PrepareActivityInput{
		Kind: PrepareTurnStarted, CheckID: "missing"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("check: %v", err)
	}
	unselected, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordPrepareActivity(ctx, ownerActor(), unselected.ID, PrepareActivityInput{
		Kind: PrepareTurnStarted}); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected write: %v", err)
	}
	first, err := s.RecordPrepareActivity(ctx, ownerActor(), opportunity.ID, PrepareActivityInput{
		Kind: PrepareTurnStarted, Outcome: "started",
		Payload: json.RawMessage(`{"targets":["cv"]}`)})
	if err != nil || first == "" {
		t.Fatal(err)
	}
	second, err := s.RecordPrepareActivity(ctx, ownerActor(), opportunity.ID, PrepareActivityInput{
		Kind: PrepareCompleted, Outcome: "ok",
		Payload: json.RawMessage(`{"drafted":["cv"],"held":[]}`)})
	if err != nil || second == "" || second == first {
		t.Fatal(err)
	}
	page, next, err := s.ListPrepareActivity(ctx, opportunity.ID, "", 1)
	if err != nil || len(page) != 1 || page[0].Kind != PrepareTurnStarted || next == "" {
		t.Fatalf("head: %+v next=%q err=%v", page, next, err)
	}
	if string(page[0].Payload) != `{"targets":["cv"]}` || page[0].RunID != opportunity.ID {
		t.Fatalf("event: %+v", page[0])
	}
	tail, next, err := s.ListPrepareActivity(ctx, opportunity.ID, next, 10)
	if err != nil || len(tail) != 1 || tail[0].Kind != PrepareCompleted || next != "" {
		t.Fatalf("tail: %+v next=%q err=%v", tail, next, err)
	}
	if _, _, err := s.ListPrepareActivity(ctx, opportunity.ID, "missing", 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cursor: %v", err)
	}
	if _, _, err := s.ListPrepareActivity(ctx, unselected.ID, "", 10); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected read: %v", err)
	}
}
