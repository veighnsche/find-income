package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRelationshipRoundKeepsIntroductionUnqualifiedAndSharesOpportunity(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-relationships"}
	company, _, err := s.CreateCompany(ctx, owner, CompanyInput{Name: "Example Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opening, _, err := s.CreateOpportunity(ctx, owner, fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	otherCompany, _, err := s.CreateCompany(ctx, owner, CompanyInput{Name: "Unrelated Employer"})
	if err != nil {
		t.Fatal(err)
	}
	otherInput := fixtureOpportunity(otherCompany.ID)
	otherInput.SourceURL = "https://harbour.example/jobs/other"
	other, _, err := s.CreateOpportunity(ctx, owner, otherInput)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "relationships", Intent: "Record attributed introductions and routes", Outcome: "process_input", ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
		Scope:  RoundScope{Resources: []string{"campaign:active", "opportunity:" + opening.ID}, Operations: []string{RoundCodexTurn, RoundRelationshipCounterpartyCreate, RoundRelationshipEventCreate, RoundRelationshipRouteCreate}, Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Requests: 4, Items: 4, Tools: 5, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	round, err = s.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	capability := mutationCapability(t, s, round, agent, "campaign:active")
	now := time.Now().UTC().Format(time.RFC3339)
	mutate := func(key, operation, resource string, expected int64, payload RelationshipMutationInput) RoundMutationResult {
		t.Helper()
		result, created, err := s.ApplyRoundMutation(ctx, agent, round.ID, RoundMutationInput{RequestKey: key, Operation: operation, ResourceID: resource, ExpectedRevision: expected, Relationship: &payload, Capability: capability})
		if err != nil || !created {
			t.Fatalf("%s: %+v created=%v err=%v", key, result, created, err)
		}
		return result
	}
	contact := mutate("contact", RoundRelationshipCounterpartyCreate, "campaign:active", profile.Version, RelationshipMutationInput{Counterparty: &RelationshipCounterpartyInput{DisplayName: "Alex Example", Kind: "referrer", SourceKind: "owner_statement", SourceExcerpt: "Alex offered to introduce me.", ObservedAt: now}})
	intro := mutate("intro", RoundRelationshipEventCreate, "campaign:active", profile.Version, RelationshipMutationInput{Event: &RelationshipEventInput{CounterpartyID: contact.EntityID, Kind: "introduction", Summary: "An introduction was offered; no opening was identified.", SourceKind: "owner_statement", SourceExcerpt: "Alex offered to introduce me; no role was discussed.", ObservedAt: now}})
	events, err := s.ListRelationshipEvents(ctx)
	if err != nil || len(events) != 1 || events[0].ID != intro.EntityID || events[0].OpportunityID != "" {
		t.Fatalf("pre-vacancy event acquired a role: %+v %v", events, err)
	}
	if _, err := s.ListOpportunityRoutes(ctx, opening.ID); err != nil {
		t.Fatal(err)
	}
	outside := RoundMutationInput{RequestKey: "outside", Operation: RoundRelationshipRouteCreate, ResourceID: "opportunity:" + other.ID, ExpectedRevision: other.Revision, Relationship: &RelationshipMutationInput{Route: &OpportunityRouteInput{OpportunityID: other.ID, Kind: "direct", SourceKind: "posting", SourceExcerpt: "Apply on the employer website.", ObservedAt: now}}, Capability: capability}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, round.ID, outside); !errors.Is(err, ErrFenced) {
		t.Fatalf("unrelated route accepted: %v", err)
	}
	direct := mutate("direct", RoundRelationshipRouteCreate, "opportunity:"+opening.ID, opening.Revision, RelationshipMutationInput{Route: &OpportunityRouteInput{OpportunityID: opening.ID, Kind: "direct", DestinationText: "Employer portal", SourceKind: "posting", SourceExcerpt: "Apply through this portal.", ObservedAt: now}})
	mutate("referral", RoundRelationshipRouteCreate, "opportunity:"+opening.ID, opening.Revision, RelationshipMutationInput{Route: &OpportunityRouteInput{OpportunityID: opening.ID, EventID: intro.EntityID, CounterpartyID: contact.EntityID, Kind: "referral", DestinationText: "Unknown pending introduction", SourceKind: "owner_statement", SourceExcerpt: "Alex offered to introduce me; no referral has been sent.", ObservedAt: now}})
	routes, err := s.ListOpportunityRoutes(ctx, opening.ID)
	if err != nil || len(routes) != 2 {
		t.Fatalf("two routes to one role: %+v %v", routes, err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM opportunities WHERE id=?`, opening.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicated opportunity: %d %v", count, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM opportunities WHERE id=?`, other.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("altered unrelated opportunity: %d %v", count, err)
	}
	if _, _, err := s.StopRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseStoppedRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRound(ctx, owner, round.ID, RoundCompleted, "recorded_relationships", "partial", json.RawMessage(`{"recorded":true}`)); err != nil {
		t.Fatal(err)
	}
	instruction, _, err := s.AddOwnerInstruction(ctx, owner, OwnerInstructionInput{RequestKey: "correct-route", TargetKind: "relationship", TargetID: direct.EntityID, ExpectedRevision: direct.Revision, Text: "Correct the portal destination from the posting text."})
	if err != nil {
		t.Fatal(err)
	}
	correctionRound, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "relationship-correction", Intent: "Correct the sourced route", Outcome: "process_input", ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour), Scope: RoundScope{Resources: []string{"relationship:" + direct.EntityID}, InputRefs: []string{"instruction:" + instruction.ID}, Operations: []string{RoundCodexTurn, RoundRelationshipCorrect}, Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 2, Items: 2, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	correctionRound, err = s.ActivateRound(ctx, owner, correctionRound.ID)
	if err != nil {
		t.Fatal(err)
	}
	correctionCapability := mutationCapability(t, s, correctionRound, agent, "relationship:"+direct.EntityID)
	corrected := OpportunityRouteInput{ID: direct.EntityID, OpportunityID: opening.ID, Kind: "direct", DestinationText: "Employer candidate portal", SourceKind: "posting", SourceExcerpt: "Submit your application on the employer candidate portal.", ObservedAt: now}
	correction := RoundMutationInput{RequestKey: "correct", Operation: RoundRelationshipCorrect, ResourceID: "relationship:" + direct.EntityID, ExpectedRevision: direct.Revision, OwnerInstructionID: instruction.ID, Relationship: &RelationshipMutationInput{Route: &corrected}, Capability: correctionCapability}
	result, created, err := s.ApplyRoundMutation(ctx, agent, correctionRound.ID, correction)
	if err != nil || !created || result.Revision != 2 {
		t.Fatalf("sourced correction: %+v created=%v err=%v", result, created, err)
	}
	stale := correction
	stale.RequestKey = "stale"
	if _, _, err := s.ApplyRoundMutation(ctx, agent, correctionRound.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale correction accepted: %v", err)
	}
	// Fixing a mistaken association must be possible, but only with authority
	// for both the previous and corrected opportunity and a fresh instruction.
	reassignInstruction, _, err := s.AddOwnerInstruction(ctx, owner, OwnerInstructionInput{RequestKey: "reassign-route", TargetKind: "relationship", TargetID: direct.EntityID, ExpectedRevision: 2, Text: "This direct portal route belongs to Unrelated Employer's opening, not the first opening."})
	if err != nil {
		t.Fatal(err)
	}
	reassigned := corrected
	reassigned.OpportunityID = other.ID
	reassigned.SourceExcerpt = "This portal route belongs to the second employer's opening."
	reassign := RoundMutationInput{RequestKey: "reassign-without-scope", Operation: RoundRelationshipCorrect, ResourceID: "relationship:" + direct.EntityID, ExpectedRevision: 2, OwnerInstructionID: reassignInstruction.ID, Relationship: &RelationshipMutationInput{Route: &reassigned}, Capability: correctionCapability}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, correctionRound.ID, reassign); !errors.Is(err, ErrFenced) {
		t.Fatalf("reassignment without old/new scope accepted: %v", err)
	}
	if _, _, err := s.StopRound(ctx, owner, correctionRound.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseStoppedRound(ctx, owner, correctionRound.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRound(ctx, owner, correctionRound.ID, RoundCompleted, "corrected", "partial", json.RawMessage(`{"corrected":true}`)); err != nil {
		t.Fatal(err)
	}
	reassignRound, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "reassign-authorized", Intent: "Correct a source-backed mistaken association", Outcome: "process_input", ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour), Scope: RoundScope{Resources: []string{"relationship:" + direct.EntityID, "opportunity:" + opening.ID, "opportunity:" + other.ID}, InputRefs: []string{"instruction:" + reassignInstruction.ID}, Operations: []string{RoundCodexTurn, RoundRelationshipCorrect}, Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 2, Items: 2, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	reassignRound, err = s.ActivateRound(ctx, owner, reassignRound.ID)
	if err != nil {
		t.Fatal(err)
	}
	reassign.Capability = mutationCapability(t, s, reassignRound, agent, "relationship:"+direct.EntityID)
	reassign.RequestKey = "reassign"
	if _, created, err := s.ApplyRoundMutation(ctx, agent, reassignRound.ID, reassign); err != nil || !created {
		t.Fatalf("authorized source correction failed: created=%v err=%v", created, err)
	}
	oldRoutes, err := s.ListOpportunityRoutes(ctx, opening.ID)
	if err != nil || len(oldRoutes) != 1 {
		t.Fatalf("old association remains current: %+v %v", oldRoutes, err)
	}
	newRoutes, err := s.ListOpportunityRoutes(ctx, other.ID)
	if err != nil || len(newRoutes) != 1 || newRoutes[0].ID != direct.EntityID || newRoutes[0].Revision != 3 {
		t.Fatalf("corrected association missing: %+v %v", newRoutes, err)
	}

}

func TestRelationshipTransactionPersistsSourcedPreVacancyEvent(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	observed := time.Now().UTC().Format(time.RFC3339)
	id, kind, revision, err := writeRelationshipTx(ctx, tx, RoundRelationshipEventCreate, 1, RelationshipMutationInput{Event: &RelationshipEventInput{Kind: "introduction", Summary: "No vacancy was discussed.", SourceKind: "owner_statement", SourceExcerpt: "An introduction was offered. No role was discussed.", ObservedAt: observed}})
	if err != nil || kind != "relationship_event" || revision != 1 {
		t.Fatalf("write sourced event: %s %s %d %v", id, kind, revision, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListRelationshipEvents(ctx)
	if err != nil || len(events) != 1 || events[0].ID != id || events[0].OpportunityID != "" || events[0].SourceExcerpt != "An introduction was offered. No role was discussed." {
		t.Fatalf("read sourced event: %+v %v", events, err)
	}
}
