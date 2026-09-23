package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func packMutationFixture(opportunity Opportunity) ApplicationPackMutationInput {
	manifest, _ := json.Marshal(map[string]any{"role": map[string]any{"opportunityId": opportunity.ID, "opportunityRevision": opportunity.Revision, "profileRevision": 1}, "sources": []any{map[string]any{"id": "fixture", "sha256": "source-digest", "body": "snapshot"}}})
	input := ApplicationPackMutationInput{OpportunityID: opportunity.ID, ExpectedOpportunityRevision: opportunity.Revision, ExpectedProfileRevision: 1,
		ManifestJSON: manifest, TypstSource: []byte("= Vince Liem\n"), PDF: append([]byte("%PDF-1.7\n"), make([]byte, 110)...)}
	input.ContentSHA256 = applicationPackContentHash(input.ManifestJSON, input.TypstSource, input.PDF)
	return input
}

func TestApplicationPackImmutableVersionsAndRevisionFence(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	input := packMutationFixture(opportunity)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, version, err := createApplicationPackTx(ctx, tx, input)
	if err != nil || version != 1 {
		t.Fatalf("create: %s %d %v", id, version, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stored, err := s.ApplicationPack(ctx, id)
	if err != nil || string(stored.ManifestJSON) != string(input.ManifestJSON) {
		t.Fatalf("read snapshot: %+v %v", stored, err)
	}
	input.ManifestJSON = append([]byte(nil), input.ManifestJSON...)
	input.ManifestJSON[0] = ' '
	if string(stored.ManifestJSON) == string(input.ManifestJSON) {
		t.Fatal("snapshot aliased caller memory")
	}
	input = packMutationFixture(opportunity)
	tx, _ = s.db.BeginTx(ctx, nil)
	_, version, err = createApplicationPackTx(ctx, tx, input)
	if err != nil || version != 2 {
		t.Fatalf("second version: %d %v", version, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	input.ExpectedProfileRevision = 2
	tx, _ = s.db.BeginTx(ctx, nil)
	_, _, err = createApplicationPackTx(ctx, tx, input)
	_ = tx.Rollback()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("manifest/profile mismatch accepted: %v", err)
	}
	input = packMutationFixture(opportunity)
	next, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next.PreferredLocation = "Rotterdam"
	if _, _, err := s.UpdatePreferences(ctx, 1, next, ownerActor()); err != nil {
		t.Fatal(err)
	}
	tx, _ = s.db.BeginTx(ctx, nil)
	_, _, err = createApplicationPackTx(ctx, tx, input)
	_ = tx.Rollback()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale profile accepted: %v", err)
	}
	updatedText := "Revised Go platform opening."
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{ExpectedRevision: 1, OriginalText: &updatedText}); err != nil {
		t.Fatal(err)
	}
	input.ExpectedProfileRevision = 2
	var manifest map[string]any
	if err := json.Unmarshal(input.ManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["role"].(map[string]any)["profileRevision"] = 2
	input.ManifestJSON, _ = json.Marshal(manifest)
	input.ContentSHA256 = applicationPackContentHash(input.ManifestJSON, input.TypstSource, input.PDF)
	tx, _ = s.db.BeginTx(ctx, nil)
	_, _, err = createApplicationPackTx(ctx, tx, input)
	_ = tx.Rollback()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale opportunity accepted: %v", err)
	}
	storedAgain, err := s.ApplicationPack(ctx, id)
	if err != nil || string(storedAgain.ManifestJSON) != string(stored.ManifestJSON) {
		t.Fatalf("prior pack changed: %v", err)
	}
}
