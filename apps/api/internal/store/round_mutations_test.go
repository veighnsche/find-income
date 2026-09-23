package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mutationCapability(t *testing.T, s *Store, r Round, agent Actor, resource string) string {
	t.Helper()
	ctx := context.Background()
	turn, _, err := s.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{
		RequestKey: "bound-turn", Operation: RoundCodexTurn, ResourceID: resource, Cost: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := s.IssueRoundToolCapability(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return capability
}

func TestRoundMutationBindsActorCostRevisionAuditAndReplay(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-1"}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "mutate", Intent: "Save a sourced employer",
		Outcome: "discover", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{Operations: []string{RoundCreateCompany, RoundCodexTurn}, Resources: []string{"campaign:active"},
			Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	capability := mutationCapability(t, s, r, agent, "campaign:active")
	input := RoundMutationInput{RequestKey: "company-a", Operation: RoundCreateCompany,
		ResourceID: "campaign:active", ExpectedRevision: p.Version,
		Company: &CompanyInput{Name: "Synthetic Employer", Website: "https://example.test"}, Capability: capability}
	if _, _, err := s.ApplyRoundMutation(ctx, Actor{Kind: "agent", ID: "other"}, r.ID, input); !errors.Is(err, ErrFenced) {
		t.Fatalf("undelegated agent wrote record: %v", err)
	}
	stale := input
	stale.ExpectedRevision++
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision wrote record: %v", err)
	}
	invalid := input
	invalid.Company = &CompanyInput{Name: ""}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid record charged allowance: %v", err)
	}
	read, err := s.Round(ctx, r.ID)
	if err != nil || read.Used != (RoundAllowance{Tools: 1, Turns: 1}) {
		t.Fatalf("rejected mutation spent allowance: %+v %v", read.Used, err)
	}
	result, created, err := s.ApplyRoundMutation(ctx, agent, r.ID, input)
	if err != nil || !created || result.EntityKind != "company" || result.Revision != 1 {
		t.Fatalf("authorized mutation: %+v %v %v", result, created, err)
	}
	change, err := s.RecordChange(ctx, result.AuditID)
	if err != nil || change.EntityID != result.EntityID || change.Actor != agent {
		t.Fatalf("record audit: %+v %v", change, err)
	}
	links, err := s.RoundRecordChanges(ctx, r.ID)
	if err != nil || len(links) != 1 || links[0] != result.AuditID {
		t.Fatalf("mutation and round audit were not linked: %v %v", links, err)
	}
	replay, created, err := s.ApplyRoundMutation(ctx, agent, r.ID, input)
	if err != nil || created || replay != result {
		t.Fatalf("exact replay duplicated mutation: %+v %v %v", replay, created, err)
	}
	other := input
	other.RequestKey = "company-b"
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, other); !errors.Is(err, ErrAllowance) {
		t.Fatalf("new mutation bypassed cost: %v", err)
	}
	read, err = s.Round(ctx, r.ID)
	if err != nil || read.Used != (RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1}) {
		t.Fatalf("wrong final charge: %+v %v", read.Used, err)
	}
	if _, _, err := s.StopRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, other); !errors.Is(err, ErrFenced) {
		t.Fatalf("stopped round allowed a new mutation: %v", err)
	}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, input); !errors.Is(err, ErrFenced) {
		t.Fatalf("old turn replay after Stop passed fence: %v", err)
	}
}

func TestRoundOpportunityMutationChecksCompanyRevision(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-1"}
	company, _, err := s.CreateCompany(ctx, owner, CompanyInput{Name: "Synthetic Source"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "opening", Intent: "Save an exact opening",
		Outcome: "discover", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{Operations: []string{RoundCreateOpportunity, RoundCodexTurn}, Resources: []string{"company:" + company.ID},
			Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if r, err = s.ActivateRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	capability := mutationCapability(t, s, r, agent, "company:"+company.ID)
	opening := fixtureOpportunity(company.ID)
	input := RoundMutationInput{RequestKey: "save", Operation: RoundCreateOpportunity,
		ResourceID: "company:" + company.ID, ExpectedRevision: company.Revision + 1, Opportunity: &opening, Capability: capability}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong company revision accepted: %v", err)
	}
	input.ExpectedRevision = company.Revision
	result, created, err := s.ApplyRoundMutation(ctx, agent, r.ID, input)
	if err != nil || !created || result.EntityKind != "opportunity" {
		t.Fatalf("opportunity save: %+v %v %v", result, created, err)
	}
	view, err := s.Opportunity(ctx, result.EntityID)
	if err != nil || view.OriginalText != opening.OriginalText || view.CompanyID != company.ID {
		t.Fatalf("opportunity source lost: %+v %v", view, err)
	}
}

func TestRoundCreatedCompanyScopesItsOwnOpportunity(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-1"}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "new-company-role", Intent: "Save sourced role",
		Outcome: "discover", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{Resources: []string{"campaign:active"},
			Operations: []string{RoundCodexTurn, RoundCreateCompany, RoundCreateOpportunity}, Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Requests: 2, Items: 2, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	capability := mutationCapability(t, s, r, agent, "campaign:active")
	created, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, RoundMutationInput{RequestKey: "company",
		Operation: RoundCreateCompany, ResourceID: "campaign:active", ExpectedRevision: p.Version,
		Company: &CompanyInput{Name: "New Employer"}, Capability: capability})
	if err != nil {
		t.Fatal(err)
	}
	outside, _, err := s.CreateCompany(ctx, owner, CompanyInput{Name: "Outside Employer"})
	if err != nil {
		t.Fatal(err)
	}
	other := fixtureOpportunity(outside.ID)
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, RoundMutationInput{RequestKey: "outside",
		Operation: RoundCreateOpportunity, ResourceID: "company:" + outside.ID, ExpectedRevision: outside.Revision,
		Opportunity: &other, Capability: capability}); !errors.Is(err, ErrFenced) {
		t.Fatalf("unscoped outside company accepted: %v", err)
	}
	opening := fixtureOpportunity(created.EntityID)
	result, wasCreated, err := s.ApplyRoundMutation(ctx, agent, r.ID, RoundMutationInput{RequestKey: "opening",
		Operation: RoundCreateOpportunity, ResourceID: "company:" + created.EntityID, ExpectedRevision: 1,
		Opportunity: &opening, Capability: capability})
	if err != nil || !wasCreated || result.EntityKind != "opportunity" {
		t.Fatalf("round child resource: %+v %v %v", result, wasCreated, err)
	}
}
