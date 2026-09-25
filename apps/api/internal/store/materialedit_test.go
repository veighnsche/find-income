package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// materialRevisionPackFor builds a pack mutation whose manifest material
// section carries the given origin, exact text (edit), and per-question
// entry texts (rewrite; nil entries carry question ids only).
func materialRevisionPackFor(t *testing.T, f materialFixture, origin, text string, entries map[string]string) ApplicationPackMutationInput {
	t.Helper()
	ctx := context.Background()
	workflow, err := f.store.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := f.store.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answers := make([]map[string]any, 0, len(f.check.Questions))
	for _, question := range f.check.Questions {
		entry := map[string]any{"questionId": question.ID}
		if entries != nil {
			entry["text"] = entries[question.ID]
		}
		answers = append(answers, entry)
	}
	material := map[string]any{"checkId": f.check.ID,
		"questionSetSha256": f.check.QuestionSetSHA256,
		"origin":            origin, "answers": answers}
	if text != "" {
		material["text"] = text
	}
	manifest := map[string]any{
		"role": map[string]any{"opportunityId": f.opportunity.ID,
			"opportunityRevision": workflow.OpportunityRev, "profileRevision": preferences.Version},
		"material": material,
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	typst := []byte(`= Application`)
	pdf := append([]byte("%PDF-1.4 fixture\n"), bytes.Repeat([]byte("0"), 200)...)
	return ApplicationPackMutationInput{OpportunityID: f.opportunity.ID,
		ExpectedOpportunityRevision: workflow.OpportunityRev, ExpectedProfileRevision: preferences.Version,
		ManifestJSON: manifestJSON, TypstSource: typst, PDF: pdf,
		ContentSHA256: applicationPackContentHash(manifestJSON, typst, pdf)}
}

func readPackManifest(t *testing.T, s *Store, packID string) map[string]any {
	t.Helper()
	var raw []byte
	err := s.db.QueryRow(`SELECT manifest_json FROM application_packs WHERE id=?`, packID).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func prepareMaterialV1(t *testing.T, f materialFixture, key string, answerOrdinals ...int) MaterialStatusView {
	t.Helper()
	ctx := context.Background()
	for _, ordinal := range answerOrdinals {
		saveFixtureAnswer(t, f, ordinal, "Fixture answer.")
	}
	view, created, err := f.store.PrepareOpportunityMaterials(ctx, ownerActor(), f.opportunity.ID,
		materialPrepareInput(t, f, key))
	if err != nil || !created || view.Current.Version != 1 {
		t.Fatalf("prepare v1: %+v %v", view, err)
	}
	return view
}

func materialRewriteTexts(f materialFixture, texts map[int]string) []MaterialRewriteText {
	out := make([]MaterialRewriteText, 0, len(f.check.Questions))
	for i, question := range f.check.Questions {
		text := texts[i]
		out = append(out, MaterialRewriteText{QuestionID: question.ID, Text: text, TextSHA256: materialTextSHA(text)})
	}
	return out
}

func TestMaterialEditRoundTripsExactly(t *testing.T) {
	f := setupMaterialAnswered(t, "edit-exact")
	ctx := context.Background()
	owner := ownerActor()
	first := prepareMaterialV1(t, f, "edit-exact-prep", 0, 1)
	manifestBefore := readPackManifest(t, f.store, first.Current.PackID)
	rawBefore, _ := json.Marshal(manifestBefore)

	// Byte-hostile text: unicode, trailing spaces, tabs, newlines, quotes.
	text := "Dear hiring team,\n\n\tI want this rôle — “Go platform work” — for its scale.  \nSnowman: ☃\n"
	input := MaterialEditInput{RequestKey: "edit-exact-key", ExpectedVersion: 1, Text: text,
		Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, text, nil)}
	view, created, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, input)
	if err != nil || !created {
		t.Fatalf("edit: %+v %v", view, err)
	}
	if view.Version != 2 || view.PackID == first.Current.PackID ||
		view.Provenance.Origin != MaterialOriginDirectEdit || view.Provenance.RewriteOf != nil {
		t.Fatalf("edit version: %+v", view)
	}
	if !view.Readiness.Ready {
		t.Fatalf("carried readiness lost: %+v", view.Readiness)
	}
	if len(view.Answers) != len(first.Current.Answers) {
		t.Fatalf("refs not carried: %+v", view.Answers)
	}
	for i, ref := range view.Answers {
		if ref != first.Current.Answers[i] {
			t.Fatalf("ref %d changed: %+v vs %+v", i, ref, first.Current.Answers[i])
		}
	}
	manifest := readPackManifest(t, f.store, view.PackID)
	material, _ := manifest["material"].(map[string]any)
	if material["text"] != text || material["origin"] != MaterialOriginDirectEdit {
		t.Fatalf("edited text not byte-exact: %#v", material["text"])
	}
	// Prior pack bytes preserved: v1 manifest untouched by the v2 commit.
	again := readPackManifest(t, f.store, first.Current.PackID)
	rawAgain, _ := json.Marshal(again)
	if string(rawAgain) != string(rawBefore) {
		t.Fatal("v1 pack manifest mutated by edit")
	}
	one, err := f.store.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil || one.PackID != first.Current.PackID {
		t.Fatalf("v1 row mutated: %+v %v", one, err)
	}
	// Current read follows the new version.
	current, err := f.store.CurrentOpportunityMaterials(ctx, f.opportunity.ID)
	if err != nil || current.Status != MaterialStatusPrepared || current.Current.Version != 2 {
		t.Fatalf("current after edit: %+v %v", current, err)
	}
}

func TestMaterialEditConflictsAndReplay(t *testing.T) {
	f := setupMaterialAnswered(t, "edit-conflict")
	ctx := context.Background()
	owner := ownerActor()
	prepareMaterialV1(t, f, "edit-conflict-prep", 0, 1)
	text := "Edited material text."

	// Wrong base on existing materials is a conflict, not not-found.
	badBase := MaterialEditInput{RequestKey: "edit-conflict-bad", ExpectedVersion: 9, Text: text,
		Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, text, nil)}
	if _, _, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, badBase); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong base: %v", err)
	}
	first, created, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID,
		MaterialEditInput{RequestKey: "edit-conflict-key", ExpectedVersion: 1, Text: text,
			Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, text, nil)})
	if err != nil || !created || first.Version != 2 {
		t.Fatalf("first edit: %+v %v", first, err)
	}
	// Concurrent loser: same stale base, fresh key.
	stale := MaterialEditInput{RequestKey: "edit-conflict-key-2", ExpectedVersion: 1, Text: "Other text.",
		Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, "Other text.", nil)}
	if _, _, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base: %v", err)
	}
	// Same key with identical input replays without a new version.
	replay, created, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID,
		MaterialEditInput{RequestKey: "edit-conflict-key", ExpectedVersion: 1, Text: text})
	if err != nil || created || replay.Version != 2 || replay.PackID != first.PackID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	// Same key with changed text is an idempotency conflict.
	changed := MaterialEditInput{RequestKey: "edit-conflict-key", ExpectedVersion: 1, Text: "Changed.",
		Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, "Changed.", nil)}
	if _, _, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, changed); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("reused key: %v", err)
	}
}

func TestMaterialEditGuards(t *testing.T) {
	ctx := context.Background()
	owner := ownerActor()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, owner, fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	unselected := MaterialEditInput{RequestKey: "edit-guard", ExpectedVersion: 1, Text: "x"}
	if _, _, err := s.EditOpportunityMaterials(ctx, owner, opportunity.ID, unselected); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected edit: %v", err)
	}
	selected := selectFixtureOpportunity(t, s, company.ID, "edit-guard-select")
	if _, _, err := s.EditOpportunityMaterials(ctx, owner, selected.ID, unselected); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit without materials: %v", err)
	}

	f := setupMaterialAnswered(t, "edit-guard")
	prepareMaterialV1(t, f, "edit-guard-prep", 0, 1)
	good := func() MaterialEditInput {
		return MaterialEditInput{RequestKey: "edit-guard-key", ExpectedVersion: 1, Text: "Guard text.",
			Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, "Guard text.", nil)}
	}
	cases := map[string]func(*MaterialEditInput){
		"empty text": func(input *MaterialEditInput) { input.Text = "" },
		"oversize":   func(input *MaterialEditInput) { input.Text = strings.Repeat("x", materialMaxEditText+1) },
		"non-utf8":   func(input *MaterialEditInput) { input.Text = "bad\xff" },
		"bad actor":  func(input *MaterialEditInput) {},
		"empty key":  func(input *MaterialEditInput) { input.RequestKey = "" },
		"bad sha":    func(input *MaterialEditInput) { input.SourceShas = []string{"short"} },
	}
	for name, mutate := range cases {
		input := good()
		mutate(&input)
		actor := owner
		if name == "bad actor" {
			actor = Actor{Kind: "agent", ID: "someone"}
		}
		if _, _, err := f.store.EditOpportunityMaterials(ctx, actor, f.opportunity.ID, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Manifest must bind this check with direct_edit origin and the exact text.
	manifestCases := map[string]func(map[string]any){
		"wrong origin": func(manifest map[string]any) {
			manifest["material"].(map[string]any)["origin"] = MaterialOriginPrepared
		},
		"changed text": func(manifest map[string]any) {
			manifest["material"].(map[string]any)["text"] = "Something else."
		},
		"missing text": func(manifest map[string]any) {
			delete(manifest["material"].(map[string]any), "text")
		},
		"wrong count": func(manifest map[string]any) {
			manifest["material"].(map[string]any)["answers"] = []map[string]any{{"questionId": "only"}}
		},
	}
	for name, mutate := range manifestCases {
		pack := materialRevisionPackFor(t, f, MaterialOriginDirectEdit, "Guard text.", nil)
		var manifest map[string]any
		if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil {
			t.Fatal(err)
		}
		mutate(manifest)
		raw, _ := json.Marshal(manifest)
		pack.ManifestJSON = raw
		pack.ContentSHA256 = applicationPackContentHash(raw, pack.TypstSource, pack.PDF)
		input := good()
		input.Pack = pack
		if _, _, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// Stale base: a profile move after prepare blocks the edit.
	preferences, err := f.store.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.UpdatePreferences(ctx, preferences.Version, preferences, owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, good()); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale profile base accepted")
	}
}

func TestMaterialEditReviewingReturnsToPrepared(t *testing.T) {
	f := setupMaterialAnswered(t, "edit-review")
	ctx := context.Background()
	owner := ownerActor()
	prepareMaterialV1(t, f, "edit-review-prep", 0, 1)
	workflow, err := f.store.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil || workflow.Stage != RoleStagePrepared {
		t.Fatalf("fixture stage: %+v %v", workflow, err)
	}
	moved, err := f.store.AdvanceRoleWorkflow(ctx, f.opportunity.ID, workflow.Revision, RoleStageReviewing, "")
	if err != nil {
		t.Fatal(err)
	}
	text := "Edited while reviewing."
	view, created, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID,
		MaterialEditInput{RequestKey: "edit-review-key", ExpectedVersion: 1, Text: text,
			Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, text, nil)})
	if err != nil || !created || view.Version != 2 {
		t.Fatalf("edit while reviewing: %+v %v", view, err)
	}
	after, err := f.store.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil || after.Stage != RoleStagePrepared || after.Revision != moved.Revision+1 {
		t.Fatalf("review not reset: %+v %v", after, err)
	}
	// Sent roles never revise through this path.
	sent, err := f.store.AdvanceRoleWorkflow(ctx, f.opportunity.ID, after.Revision, RoleStageReviewing, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AdvanceRoleWorkflow(ctx, f.opportunity.ID, sent.Revision, RoleStageSent, ""); err != nil {
		t.Fatal(err)
	}
	late := MaterialEditInput{RequestKey: "edit-review-late", ExpectedVersion: 2, Text: "Too late.",
		Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, "Too late.", nil)}
	if _, _, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, late); !errors.Is(err, ErrConflict) {
		t.Fatalf("edit after send: %v", err)
	}
}

func TestMaterialRewriteCommitsVersion(t *testing.T) {
	f := setupMaterialAnswered(t, "rewrite-ok")
	ctx := context.Background()
	owner := ownerActor()
	// v1 held: second required question unset, no drafts.
	first := prepareMaterialV1(t, f, "rewrite-ok-prep", 0)
	if first.Status != MaterialStatusHeld {
		t.Fatalf("fixture status: %+v", first)
	}
	texts := materialRewriteTexts(f, map[int]string{
		0: "Rewritten motivation.",
		1: "Rewritten Go service history.",
		2: "Rewritten extra note.",
		3: "Rewritten hybrid answer.",
	})
	entries := map[string]string{}
	for _, text := range texts {
		entries[text.QuestionID] = text.Text
	}
	view, created, err := f.store.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID,
		MaterialRewriteInput{RequestKey: "rewrite-ok-key", ExpectedVersion: 1, Texts: texts,
			Pack: materialRevisionPackFor(t, f, MaterialOriginRewrite, "", entries)})
	if err != nil || !created {
		t.Fatalf("rewrite: %+v %v", view, err)
	}
	if view.Version != 2 || view.PackID == first.Current.PackID ||
		view.Provenance.Origin != MaterialOriginRewrite ||
		view.Provenance.RewriteOf == nil || *view.Provenance.RewriteOf != 1 {
		t.Fatalf("rewrite linkage: %+v", view.Provenance)
	}
	if !view.Readiness.Ready || len(view.Readiness.Held) != 0 {
		t.Fatalf("rewrite readiness: %+v", view.Readiness)
	}
	for i, ref := range view.Answers {
		if ref.QuestionID != f.check.Questions[i].ID || ref.AnswerVersion != 0 ||
			ref.TextSHA256 != texts[i].TextSHA256 {
			t.Fatalf("rewrite ref %d: %+v", i, ref)
		}
	}
	manifest := readPackManifest(t, f.store, view.PackID)
	material, _ := manifest["material"].(map[string]any)
	answers, _ := material["answers"].([]any)
	if len(answers) != len(f.check.Questions) {
		t.Fatalf("rewrite entries: %v", material["answers"])
	}
	for i, raw := range answers {
		entry, _ := raw.(map[string]any)
		if entry["questionId"] != f.check.Questions[i].ID || entry["text"] != texts[i].Text {
			t.Fatalf("rewrite entry %d not exact: %v", i, entry)
		}
	}
}

func TestMaterialRewriteValidationAndReplay(t *testing.T) {
	f := setupMaterialAnswered(t, "rewrite-guard")
	ctx := context.Background()
	owner := ownerActor()
	prepareMaterialV1(t, f, "rewrite-guard-prep", 0, 1)
	goodTexts := materialRewriteTexts(f, map[int]string{
		0: "One.", 1: "Two.", 2: "Three.", 3: "Four.",
	})
	good := func() MaterialRewriteInput {
		texts := append([]MaterialRewriteText{}, goodTexts...)
		entries := map[string]string{}
		for _, text := range texts {
			entries[text.QuestionID] = text.Text
		}
		return MaterialRewriteInput{RequestKey: "rewrite-guard-key", ExpectedVersion: 1,
			Texts: texts, Pack: materialRevisionPackFor(t, f, MaterialOriginRewrite, "", entries)}
	}
	shaCases := map[string]func(*MaterialRewriteInput){
		"missing question": func(input *MaterialRewriteInput) { input.Texts = input.Texts[:3] },
		"unknown question": func(input *MaterialRewriteInput) {
			input.Texts = append(append([]MaterialRewriteText{}, input.Texts...),
				MaterialRewriteText{QuestionID: "nope", Text: "x", TextSHA256: materialTextSHA("x")})
		},
		"sha mismatch": func(input *MaterialRewriteInput) { input.Texts[0].TextSHA256 = strings.Repeat("0", 64) },
		"duplicate": func(input *MaterialRewriteInput) {
			input.Texts = append(input.Texts, input.Texts[0])
		},
	}
	for name, mutate := range shaCases {
		input := good()
		mutate(&input)
		if _, _, err := f.store.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Manifest entry text must match the committed rewrite bytes.
	pack := good().Pack
	var manifest map[string]any
	if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	entries := manifest["material"].(map[string]any)["answers"].([]any)
	entries[0].(map[string]any)["text"] = "Tampered."
	raw, _ := json.Marshal(manifest)
	pack.ManifestJSON = raw
	pack.ContentSHA256 = applicationPackContentHash(raw, pack.TypstSource, pack.PDF)
	tampered := good()
	tampered.Pack = pack
	if _, _, err := f.store.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID, tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered manifest: %v", err)
	}
	// Empty rewrite for a required question holds honestly instead of failing.
	held := good()
	held.RequestKey = "rewrite-guard-held"
	held.Texts[1].Text, held.Texts[1].TextSHA256 = "", materialTextSHA("")
	heldEntries := map[string]string{}
	for _, text := range held.Texts {
		heldEntries[text.QuestionID] = text.Text
	}
	held.Pack = materialRevisionPackFor(t, f, MaterialOriginRewrite, "", heldEntries)
	heldView, created, err := f.store.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID, held)
	if err != nil || !created || heldView.Readiness.Ready {
		t.Fatalf("held rewrite: %+v %v", heldView, err)
	}
	want := f.check.Questions[1].ID
	if len(heldView.Readiness.MissingRequired) != 1 || heldView.Readiness.MissingRequired[0] != want ||
		len(heldView.Readiness.Held) != 1 || heldView.Readiness.Held[0] != want {
		t.Fatalf("held lists: %+v", heldView.Readiness)
	}
	// The held version is v2; the good rewrite now conflicts on the stale base.
	if _, _, err := f.store.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID, good()); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale rewrite base accepted")
	}
	// Same held key replays the held version without a new commit.
	again, created, err := f.store.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID, held)
	if err != nil || created || again.Version != 2 || again.PackID != heldView.PackID {
		t.Fatalf("held replay: %+v %v", again, err)
	}
}

func TestMaterialRevisionsPreservePriorVersions(t *testing.T) {
	f := setupMaterialAnswered(t, "revision-chain")
	ctx := context.Background()
	owner := ownerActor()
	first := prepareMaterialV1(t, f, "revision-chain-prep", 0, 1)
	editText := "Owner-polished full text."
	second, _, err := f.store.EditOpportunityMaterials(ctx, owner, f.opportunity.ID,
		MaterialEditInput{RequestKey: "revision-chain-edit", ExpectedVersion: 1, Text: editText,
			Pack: materialRevisionPackFor(t, f, MaterialOriginDirectEdit, editText, nil)})
	if err != nil || second.Version != 2 {
		t.Fatalf("chain edit: %+v %v", second, err)
	}
	texts := materialRewriteTexts(f, map[int]string{
		0: "R0.", 1: "R1.", 2: "R2.", 3: "R3.",
	})
	entries := map[string]string{}
	for _, text := range texts {
		entries[text.QuestionID] = text.Text
	}
	third, _, err := f.store.RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID,
		MaterialRewriteInput{RequestKey: "revision-chain-rewrite", ExpectedVersion: 2, Texts: texts,
			Pack: materialRevisionPackFor(t, f, MaterialOriginRewrite, "", entries)})
	if err != nil || third.Version != 3 || *third.Provenance.RewriteOf != 2 {
		t.Fatalf("chain rewrite: %+v %v", third, err)
	}
	// Every prior version still reads back exactly.
	one, err := f.store.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil || one.PackID != first.Current.PackID || one.Provenance.Origin != MaterialOriginPrepared {
		t.Fatalf("v1 changed: %+v %v", one, err)
	}
	two, err := f.store.OpportunityMaterialVersion(ctx, f.opportunity.ID, 2)
	if err != nil || two.PackID != second.PackID || two.Provenance.Origin != MaterialOriginDirectEdit {
		t.Fatalf("v2 changed: %+v %v", two, err)
	}
	if len(two.Answers) != len(one.Answers) {
		t.Fatalf("v2 refs diverged from v1")
	}
	for i := range one.Answers {
		if two.Answers[i] != one.Answers[i] {
			t.Fatalf("v2 ref %d diverged", i)
		}
	}
	// Three distinct packs: no version can reuse another's review authorization.
	seen := map[string]bool{one.PackID: true, two.PackID: true, third.PackID: true}
	if len(seen) != 3 {
		t.Fatalf("pack ids reused: %v %v %v", one.PackID, two.PackID, third.PackID)
	}
	current, err := f.store.CurrentOpportunityMaterials(ctx, f.opportunity.ID)
	if err != nil || current.Current.Version != 3 {
		t.Fatalf("current: %+v %v", current, err)
	}
}
