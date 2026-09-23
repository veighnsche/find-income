package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOwnerInstructionRevisionReplayAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	profile, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input := OwnerInstructionInput{RequestKey: "profile-context", TargetKind: "profile", TargetID: "current",
		ExpectedRevision: profile.Version, Text: "I can work 32 hours, need at least €5,000 monthly, and prefer remote."}
	if _, _, err := s.AddOwnerInstruction(ctx, Actor{Kind: "agent", ID: "codex"}, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("agent authored owner context: %v", err)
	}
	stale := input
	stale.ExpectedRevision++
	if _, _, err := s.AddOwnerInstruction(ctx, owner, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale instruction: %v", err)
	}
	value, created, err := s.AddOwnerInstruction(ctx, owner, input)
	if err != nil || !created {
		t.Fatalf("create: %+v %v", value, err)
	}
	replay, created, err := s.AddOwnerInstruction(ctx, owner, input)
	if err != nil || created || replay.ID != value.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	changed := input
	changed.Text = "Different instruction"
	if _, _, err := s.AddOwnerInstruction(ctx, owner, changed); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	if err := s.RevokeOwnerInstruction(ctx, owner, value.ID); err != nil {
		t.Fatal(err)
	}
	list, err := s.OwnerInstructions(ctx, "")
	if err != nil || len(list) != 0 {
		t.Fatalf("revoked instruction visible: %+v %v", list, err)
	}
}

func TestEvidenceCorrectionRequiresSelectedOwnerEvidence(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-1"}
	company := createFixtureCompany(t, s)
	opening, _, err := s.CreateOpportunity(ctx, owner, fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	source := addStatementFixture(t, s, opening.ID, "Backend work is assigned to this role.")
	prior := addEvidenceFixture(t, s, source, "Backend work", EvidenceInput{Criterion: "role_criterion", CriterionID: "backend-platform", Presence: "mention_only", ObservedValue: "Mentions backend work"})
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "evidence-correction", Intent: "Correct one evidence claim",
		Outcome: "discover", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope:  RoundScope{Resources: []string{"company:" + company.ID}, Operations: []string{RoundCodexTurn, RoundCorrectEvidence}, Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	capability := mutationCapability(t, s, r, agent, "company:"+company.ID)
	v := currentInputVersions(t, s, opening.ID)
	input := RoundEvidenceCorrectionInput{RequestKey: "correct", OwnerInstructionID: "missing", PriorEvidenceID: prior.ID,
		Capability: capability, Evidence: EvidenceInput{OpportunityID: opening.ID, SourceID: source.ID,
			Criterion: "role_criterion", CriterionID: "backend-platform", Presence: "explicit_presence",
			ObservedValue: "Assigned backend work", ExpectedEvidenceVersion: v.EvidenceVersion,
			ExpectedPreferencesVersion: v.PreferencesVersion, SpanStart: strings.Index(source.OriginalText, "Backend work"),
			SpanEnd: strings.Index(source.OriginalText, "Backend work") + len("Backend work")}}
	if _, _, err := s.CorrectRoundEvidence(ctx, agent, r.ID, input); !errors.Is(err, ErrFenced) {
		t.Fatalf("evidence inferred from vacancy: %v", err)
	}
	instruction, _, err := s.AddOwnerInstruction(ctx, owner, OwnerInstructionInput{RequestKey: "evidence-owner", TargetKind: "evidence",
		TargetID: prior.ID, ExpectedRevision: 1, RoundID: r.ID, Text: "This role explicitly assigns backend work; correct that finding."})
	if err != nil {
		t.Fatal(err)
	}
	input.OwnerInstructionID = instruction.ID
	if err := s.RevokeOwnerInstruction(ctx, owner, instruction.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CorrectRoundEvidence(ctx, agent, r.ID, input); !errors.Is(err, ErrFenced) {
		t.Fatalf("revoked instruction wrote evidence: %v", err)
	}
	instruction, _, err = s.AddOwnerInstruction(ctx, owner, OwnerInstructionInput{RequestKey: "evidence-owner-again", TargetKind: "evidence",
		TargetID: prior.ID, ExpectedRevision: 1, RoundID: r.ID, Text: "The assignment is explicit in the employer statement; correct the finding."})
	if err != nil {
		t.Fatal(err)
	}
	input.OwnerInstructionID = instruction.ID
	result, created, err := s.CorrectRoundEvidence(ctx, agent, r.ID, input)
	if err != nil || !created || result.EntityKind != "evidence" {
		t.Fatalf("evidence correction: %+v %v", result, err)
	}
	claim, err := s.Evidence(ctx, result.EntityID)
	if err != nil || claim.SupersedesID != prior.ID || claim.Finding != "explicit_match" {
		t.Fatalf("corrected evidence: %+v %v", claim, err)
	}
	history, err := s.RoundHistory(ctx, r.ID)
	if err != nil || len(history) != 1 || history[0].AuditID != result.AuditID {
		t.Fatalf("evidence history: %+v %v", history, err)
	}
}

func TestProfileCorrectionNeedsExplicitCurrentOwnerInstruction(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-1"}
	profile, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "correction-round", Intent: "Apply owner profile correction",
		Outcome: "discover", ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{InputRefs: []string{"profile:current"}, Resources: []string{"profile:current"},
			Operations: []string{RoundCodexTurn, RoundCorrectPreferences}, Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Requests: 1, Items: 1, Tools: 2, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	capability := mutationCapability(t, s, r, agent, "profile:current")
	next := profile
	next.TargetHoursHundredths = 3200
	next.MinMonthlyBaseCents = 500000
	next.AllowRemote = true
	mutation := RoundMutationInput{RequestKey: "profile-update", Operation: RoundCorrectPreferences,
		ResourceID: "profile:current", ExpectedRevision: profile.Version, OwnerInstructionID: "not-an-instruction",
		Preferences: &next, Capability: capability}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, mutation); !errors.Is(err, ErrFenced) {
		t.Fatalf("profile inferred without owner: %v", err)
	}
	instruction, _, err := s.AddOwnerInstruction(ctx, owner, OwnerInstructionInput{RequestKey: "owner-update", TargetKind: "profile",
		TargetID: "current", ExpectedRevision: profile.Version, RoundID: r.ID,
		Text: "I can work 32 hours, need at least €5,000 monthly, and prefer remote."})
	if err != nil {
		t.Fatal(err)
	}
	mutation.OwnerInstructionID = instruction.ID
	wrong := mutation
	wrong.RequestKey = "wrong-version"
	wrong.ExpectedRevision++
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale profile update: %v", err)
	}
	result, created, err := s.ApplyRoundMutation(ctx, agent, r.ID, mutation)
	if err != nil || !created || result.EntityKind != "preferences" || result.Revision != profile.Version+1 {
		t.Fatalf("profile correction: %+v %v", result, err)
	}
	current, err := s.CurrentPreferences(ctx)
	if err != nil || current.TargetHoursHundredths != 3200 || current.MinMonthlyBaseCents != 500000 || !current.AllowRemote {
		t.Fatalf("multi-field correction: %+v %v", current, err)
	}
	replay, created, err := s.ApplyRoundMutation(ctx, agent, r.ID, mutation)
	if err != nil || created || replay != result {
		t.Fatalf("exact replay wrote again: %+v %v", replay, err)
	}
	newWrite := mutation
	newWrite.RequestKey = "another-profile-update"
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, newWrite); !errors.Is(err, ErrConflict) {
		t.Fatalf("old profile authorized a new write: %v", err)
	}
	changes, err := s.RoundHistory(ctx, r.ID)
	if err != nil || len(changes) != 1 || changes[0].AuditID != result.AuditID {
		t.Fatalf("correction audit link: %+v %v", changes, err)
	}
}

func TestOwnerDecisionDirectWriteAndTypedRoundCard(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-1"}
	company, _, err := s.CreateCompany(ctx, owner, CompanyInput{Name: "Example Employer"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "card-round", Intent: "Save role",
		Outcome: "discover", ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Hour),
		Scope:  RoundScope{Resources: []string{"company:" + company.ID}, Operations: []string{RoundCodexTurn, RoundCreateOpportunity, RoundCorrectOpportunity}, Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Requests: 2, Items: 2, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	capability := mutationCapability(t, s, r, agent, "company:"+company.ID)
	opening := fixtureOpportunity(company.ID)
	created, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, RoundMutationInput{RequestKey: "role", Operation: RoundCreateOpportunity,
		ResourceID: "company:" + company.ID, ExpectedRevision: company.Revision, Opportunity: &opening, Capability: capability})
	if err != nil {
		t.Fatal(err)
	}
	cards, err := s.RoundCards(ctx, r.ID)
	if err != nil || len(cards) != 1 || cards[0].SourceAuditID != created.AuditID || cards[0].Title != opening.Title || cards[0].SourceURL != opening.SourceURL {
		t.Fatalf("typed card: %+v %v", cards, err)
	}
	input := OwnerDecisionInput{RequestKey: "selected", ExpectedOpportunityRevision: 1, ExpectedDecisionRevision: 0, Decision: "selected"}
	decision, saved, err := s.SetOwnerOpportunityDecision(ctx, owner, created.EntityID, input)
	if err != nil || !saved || decision.Revision != 1 {
		t.Fatalf("direct decision: %+v %v", decision, err)
	}
	replayed, saved, err := s.SetOwnerOpportunityDecision(ctx, owner, created.EntityID, input)
	if err != nil || saved || replayed.ID != decision.ID {
		t.Fatalf("decision replay: %+v %v", replayed, err)
	}
	stale := input
	stale.RequestKey = "stale"
	stale.Decision = "dismissed"
	if _, _, err := s.SetOwnerOpportunityDecision(ctx, owner, created.EntityID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale direct decision: %v", err)
	}
	cards, err = s.RoundCards(ctx, r.ID)
	if err != nil || cards[0].Decision != "selected" || cards[0].DecisionRevision != 1 {
		t.Fatalf("decision card: %+v %v", cards, err)
	}
	instruction, _, err := s.AddOwnerInstruction(ctx, owner, OwnerInstructionInput{RequestKey: "correct-title", TargetKind: "opportunity",
		TargetID: created.EntityID, ExpectedRevision: 1, RoundID: r.ID, Text: "The correct title is Senior Backend Engineer."})
	if err != nil {
		t.Fatal(err)
	}
	title := "Senior Backend Engineer"
	correction, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, RoundMutationInput{RequestKey: "title-fix",
		Operation: RoundCorrectOpportunity, ResourceID: "opportunity:" + created.EntityID,
		ExpectedRevision: 1, OwnerInstructionID: instruction.ID,
		OpportunityPatch: &OpportunityPatch{ExpectedRevision: 1, Title: &title}, Capability: capability})
	if err != nil {
		t.Fatal(err)
	}
	cards, err = s.RoundCards(ctx, r.ID)
	if err != nil || len(cards) != 1 || cards[0].Title != title || cards[0].SourceAuditID != created.AuditID || !cards[0].SourceStale {
		t.Fatalf("duplicate or stale card after correction: %+v %v", cards, err)
	}
	history, err := s.RoundHistory(ctx, r.ID)
	if err != nil || len(history) != 2 || history[1].AuditID != correction.AuditID {
		t.Fatalf("typed edit history: %+v %v", history, err)
	}
}
