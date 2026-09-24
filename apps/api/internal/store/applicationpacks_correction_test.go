package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestPackCorrectionCommitsExactOwnerInstructionAndPreservesPriorVersion(t *testing.T) {
	ctx := context.Background()
	db := openJobTestStore(t)
	owner := ownerActor()
	agent := Actor{Kind: "agent", ID: "codex-runner"}
	company := createFixtureCompany(t, db)
	opportunity, _, err := db.CreateOpportunity(ctx, owner, fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, OwnerDecisionInput{RequestKey: "select-for-pack", ExpectedOpportunityRevision: opportunity.Revision, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	base := packMutationFixture(opportunity)
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	priorID, priorVersion, err := createApplicationPackTx(ctx, tx, base)
	if err != nil || priorVersion != 1 {
		t.Fatalf("prior pack: %s %d %v", priorID, priorVersion, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	prior, err := db.ApplicationPack(ctx, priorID)
	if err != nil {
		t.Fatal(err)
	}
	// A newer immutable version may exist while the owner selects version one.
	tx, err = db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	newerID, newerVersion, err := createApplicationPackTx(ctx, tx, base)
	if err != nil || newerVersion != 2 {
		t.Fatalf("newer pack: %s %d %v", newerID, newerVersion, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	newer, err := db.ApplicationPack(ctx, newerID)
	if err != nil {
		t.Fatal(err)
	}
	instruction, _, err := db.AddOwnerInstruction(ctx, owner, OwnerInstructionInput{RequestKey: "correct-pack", TargetKind: "application_pack", TargetID: priorID, ExpectedRevision: priorVersion, Text: "Correct the focus line to match the approved evidence."})
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := db.StartRound(ctx, owner, StartRoundInput{RequestKey: "pack-correction-round", Intent: "Correct the selected pack", Outcome: "process_input", ProfileVersion: 1,
		Scope:  RoundScope{InputRefs: []string{"instruction:" + instruction.ID}, Resources: []string{"opportunity:" + opportunity.ID, "campaign:active"}, Operations: []string{RoundCodexTurn, RoundPrepareApplicationPack}, Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Requests: 2, Items: 1, Tools: 2, Turns: 1}, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	capability := mutationCapability(t, db, round, agent, "opportunity:"+opportunity.ID)
	correction := base
	correction.PriorPackID = prior.ID
	var manifest map[string]any
	if err := json.Unmarshal(correction.ManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["correction"] = map[string]any{"priorPackId": prior.ID, "priorVersion": prior.Version, "priorContentSha256": prior.ContentSHA256,
		"ownerInstructionId": instruction.ID, "ownerInstructionRequestKey": instruction.RequestKey,
		"ownerInstructionExpectedRevision": instruction.ExpectedRevision, "ownerInstructionText": instruction.Text}
	correction.ManifestJSON, _ = json.Marshal(manifest)
	correction.ContentSHA256 = applicationPackContentHash(correction.ManifestJSON, correction.TypstSource, correction.PDF)
	mutation := RoundMutationInput{RequestKey: "correct-one", Operation: RoundPrepareApplicationPack, ResourceID: "opportunity:" + opportunity.ID,
		ExpectedRevision: opportunity.Revision, OwnerInstructionID: instruction.ID, ApplicationPack: &correction, Capability: capability}
	result, created, err := db.ApplyRoundMutation(ctx, agent, round.ID, mutation)
	if err != nil || !created || result.Revision != 3 {
		t.Fatalf("correction: %+v created=%v err=%v", result, created, err)
	}
	replay, created, err := db.ApplyRoundMutation(ctx, agent, round.ID, mutation)
	if err != nil || created || replay != result {
		t.Fatalf("exact replay: %+v created=%v err=%v", replay, created, err)
	}
	mutation.RequestKey = "stale-prior"
	if _, _, err := db.ApplyRoundMutation(ctx, agent, round.ID, mutation); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale prior pack reused: %v", err)
	}
	storedPrior, err := db.ApplicationPack(ctx, prior.ID)
	if err != nil || !bytes.Equal(storedPrior.PDF, prior.PDF) || !bytes.Equal(storedPrior.ManifestJSON, prior.ManifestJSON) {
		t.Fatalf("prior immutable bytes changed: %v", err)
	}
	storedNewer, err := db.ApplicationPack(ctx, newer.ID)
	if err != nil || !bytes.Equal(storedNewer.PDF, newer.PDF) || !bytes.Equal(storedNewer.ManifestJSON, newer.ManifestJSON) {
		t.Fatalf("newer immutable bytes changed: %v", err)
	}
	var linked int
	if err := db.db.QueryRowContext(ctx, `SELECT count(*) FROM owner_instruction_applications WHERE audit_id=? AND instruction_id=?`, result.AuditID, instruction.ID).Scan(&linked); err != nil || linked != 1 {
		t.Fatalf("instruction audit link: %d %v", linked, err)
	}
}
