package store

import (
	"context"
	"errors"
	"testing"
)

func selectFixtureOpportunity(t *testing.T, s *Store, companyID, key string) Opportunity {
	t.Helper()
	ctx := context.Background()
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(companyID))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SetOwnerOpportunityDecision(ctx, ownerActor(), opportunity.ID,
		OwnerDecisionInput{RequestKey: key, ExpectedOpportunityRevision: opportunity.Revision,
			ExpectedDecisionRevision: 0, Decision: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	return opportunity
}

func TestRoleWorkflowSelectionDoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-key-1")
	value, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Stage != RoleStageSelected || value.Revision != 0 || value.OpportunityID != opportunity.ID {
		t.Fatalf("selection advanced the role: %+v", value)
	}
	if value.DecisionAt == "" || value.UpdatedAt != value.DecisionAt {
		t.Fatalf("virtual state lacks decision time: %+v", value)
	}
}

func TestRoleWorkflowRequiresSelection(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RoleWorkflow(ctx, opportunity.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected read: %v", err)
	}
	if _, err := s.AdvanceRoleWorkflow(ctx, opportunity.ID, 0, RoleStageChecking, ""); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected advance: %v", err)
	}
	if _, err := s.RoleWorkflow(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing read: %v", err)
	}
}

func TestRoleWorkflowTransitionsGuardsAndReload(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	company, _, err := first.CreateCompany(ctx, ownerActor(), CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := first.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = first.SetOwnerOpportunityDecision(ctx, ownerActor(), opportunity.ID,
		OwnerDecisionInput{RequestKey: "select-key-2", ExpectedOpportunityRevision: opportunity.Revision,
			ExpectedDecisionRevision: 0, Decision: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.AdvanceRoleWorkflow(ctx, opportunity.ID, 0, RoleStageChecked, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("skipped stage: %v", err)
	}
	moved, err := first.AdvanceRoleWorkflow(ctx, opportunity.ID, 0, RoleStageChecking, "")
	if err != nil || moved.Revision != 1 || moved.Stage != RoleStageChecking {
		t.Fatalf("advance: %+v %v", moved, err)
	}
	if _, err := first.AdvanceRoleWorkflow(ctx, opportunity.ID, 0, RoleStageChecked, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err := first.AdvanceRoleWorkflow(ctx, opportunity.ID, 1, RoleStageBlocked, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blocked without reason: %v", err)
	}
	blocked, err := first.AdvanceRoleWorkflow(ctx, opportunity.ID, 1, RoleStageBlocked, "route ambiguous")
	if err != nil || blocked.BlockedReason != "route ambiguous" {
		t.Fatalf("block: %+v %v", blocked, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	reloaded, err := second.RoleWorkflow(ctx, opportunity.ID)
	if err != nil || reloaded.Stage != RoleStageBlocked || reloaded.Revision != 2 {
		t.Fatalf("reload: %+v %v", reloaded, err)
	}
	retried, err := second.AdvanceRoleWorkflow(ctx, opportunity.ID, 2, RoleStageChecking, "")
	if err != nil || retried.BlockedReason != "" || retried.Revision != 3 {
		t.Fatalf("retry clears block: %+v %v", retried, err)
	}
}

func TestRoleWorkflowBlockedRoleDoesNotBlockAnother(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	first := selectFixtureOpportunity(t, s, company.ID, "select-key-3")
	second := selectFixtureOpportunity(t, s, company.ID, "select-key-4")
	if _, err := s.AdvanceRoleWorkflow(ctx, first.ID, 0, RoleStageChecking, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceRoleWorkflow(ctx, first.ID, 1, RoleStageBlocked, "employer unreachable"); err != nil {
		t.Fatal(err)
	}
	moved, err := s.AdvanceRoleWorkflow(ctx, second.ID, 0, RoleStageChecking, "")
	if err != nil || moved.Stage != RoleStageChecking {
		t.Fatalf("independent role held back: %+v %v", moved, err)
	}
	items, err := s.ListRoleWorkflows(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("list: %+v %v", items, err)
	}
	if items[0].Stage != RoleStageBlocked || items[1].Stage != RoleStageChecking {
		t.Fatalf("per-role stages wrong: %+v", items)
	}
}
