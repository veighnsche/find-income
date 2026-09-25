package materialprep_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// editManifestMaterial mirrors the direct-edit manifest section for test
// assertions: byte-exact text plus one bare ordinal answer per question.
type editManifestMaterial struct {
	CheckID           string `json:"checkId"`
	QuestionSetSHA256 string `json:"questionSetSha256"`
	Origin            string `json:"origin"`
	Text              string `json:"text"`
	Answers           []struct {
		QuestionID string `json:"questionId"`
	} `json:"answers"`
}

type editCommittedManifest struct {
	Role struct {
		OpportunityID       string `json:"opportunityId"`
		OpportunityRevision int64  `json:"opportunityRevision"`
		ProfileRevision     int64  `json:"profileRevision"`
	} `json:"role"`
	Material editManifestMaterial `json:"material"`
}

func prepareHeldV1(t *testing.T, f prepFixture, key string) {
	t.Helper()
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, created, err := f.svc.PrepareOpportunityMaterials(context.Background(), testOwner(),
		f.opportunity.ID, key, checkID, questionSet, workflowRev); err != nil || !created {
		t.Fatalf("prepare v1: created=%v err=%v", created, err)
	}
}

func readEditManifest(t *testing.T, f prepFixture, packID string) editCommittedManifest {
	t.Helper()
	pack, err := f.db.ApplicationPack(context.Background(), packID)
	if err != nil {
		t.Fatal(err)
	}
	var manifest editCommittedManifest
	if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestEditOpportunityMaterialsCommitsVersion(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "edit-commit")
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	target := f.check.Questions[1]
	draftLine := applicationpacks.Line{Text: "I shipped a billing service in Go last year.",
		Citations: []applicationpacks.Citation{{SourceID: careerEvidenceID, Excerpt: careerExcerptMain}}}
	f.draft.fn = func(materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) {
		return []materialprep.RequiredDraft{{QuestionID: target.ID, Lines: []applicationpacks.Line{draftLine}}}, nil
	}
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"edit-commit-prep", checkID, questionSet, workflowRev); err != nil || !created {
		t.Fatalf("prepare v1: created=%v err=%v", created, err)
	}
	base, err := f.db.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !base.Readiness.Ready {
		t.Fatalf("fixture v1 should be ready: %+v", base.Readiness)
	}
	// Byte-hostile and markup-hostile owner text: it must round-trip
	// byte-exact through the manifest and stay inert in the render path.
	text := "Edited cover — “quoted” & <bracketed> #typst *star* _under_ $dollar$ @at `code` |pipe| ~tilde~ ^caret^ é🎯 中文\nsecond line\n\nthird"
	draftCalls, relevanceCalls, renderCalls := f.draft.calls, f.relevance.calls, f.render.calls
	view, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-key-1", 1, text)
	if err != nil || !created {
		t.Fatalf("edit: created=%v err=%v", created, err)
	}
	if view.Version != 2 || view.Provenance.Origin != store.MaterialOriginDirectEdit || view.Provenance.RewriteOf != nil {
		t.Fatalf("edit version: %+v", view.Provenance)
	}
	if !reflect.DeepEqual(view.Answers, base.Answers) {
		t.Fatalf("edit must carry base resolution byte-identical: %+v vs %+v", view.Answers, base.Answers)
	}
	if !reflect.DeepEqual(view.Readiness, base.Readiness) {
		t.Fatalf("edit must carry base readiness: %+v vs %+v", view.Readiness, base.Readiness)
	}
	if f.draft.calls != draftCalls || f.relevance.calls != relevanceCalls {
		t.Fatalf("edit touched Codex/Jev collaborators: draft %d->%d relevance %d->%d",
			draftCalls, f.draft.calls, relevanceCalls, f.relevance.calls)
	}
	if f.render.calls != renderCalls+1 {
		t.Fatalf("edit renders once: %d->%d", renderCalls, f.render.calls)
	}
	manifest := readEditManifest(t, f, view.PackID)
	if manifest.Material.Origin != store.MaterialOriginDirectEdit || manifest.Material.Text != text {
		t.Fatalf("edit manifest material: %+v", manifest.Material)
	}
	if manifest.Material.CheckID != base.CheckID || manifest.Material.QuestionSetSHA256 != base.QuestionSetSHA256 {
		t.Fatalf("edit manifest pins: %+v", manifest.Material)
	}
	if len(manifest.Material.Answers) != len(f.check.Questions) {
		t.Fatalf("edit manifest answers: %d", len(manifest.Material.Answers))
	}
	for i, answer := range manifest.Material.Answers {
		if answer.QuestionID != f.check.Questions[i].ID {
			t.Fatalf("edit manifest answer %d: %q", i, answer.QuestionID)
		}
	}
	if manifest.Role.OpportunityID != f.opportunity.ID {
		t.Fatalf("edit manifest role: %+v", manifest.Role)
	}
	workflow, err := f.db.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Stage != store.RoleStagePrepared {
		t.Fatalf("edit stage: %q", workflow.Stage)
	}
	// The v1 pack bytes are immutable: the base version still reads back
	// with its original pack identity.
	if again, err := f.db.OpportunityMaterialVersion(ctx, f.opportunity.ID, 1); err != nil || again.PackID != base.PackID {
		t.Fatalf("base version changed: %+v %v", again, err)
	}
}

func TestEditProbeReplaysWithoutRender(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "edit-replay")
	prepareHeldV1(t, f, "edit-replay-prep")
	view, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-replay-1", 1, "First exact text.")
	if err != nil || !created || view.Version != 2 {
		t.Fatalf("first edit: %+v %v", view, err)
	}
	renderCalls := f.render.calls
	replayed, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-replay-1", 1, "First exact text.")
	if err != nil || created {
		t.Fatalf("replay: created=%v err=%v", created, err)
	}
	if replayed.Version != 2 || replayed.PackID != view.PackID {
		t.Fatalf("replay version: %+v", replayed)
	}
	if f.render.calls != renderCalls {
		t.Fatalf("replay rendered: %d->%d", renderCalls, f.render.calls)
	}
	if _, _, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-replay-1", 1, "Changed text."); !errors.Is(err, store.ErrRoundIdempotencyConflict) {
		t.Fatalf("changed input same key: %v", err)
	}
	third, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-replay-2", 2, "Second exact text.")
	if err != nil || !created || third.Version != 3 {
		t.Fatalf("second edit: %+v %v", third, err)
	}
	// Retry after success with a stale base still replays: idempotency is
	// checked before fences.
	stale, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-replay-1", 1, "First exact text.")
	if err != nil || created || stale.Version != 2 || stale.PackID != view.PackID {
		t.Fatalf("stale-base replay: %+v %v", stale, err)
	}
}

func TestEditValidation(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "edit-invalid")
	owner := testOwner()
	longKey := strings.Repeat("k", 201)
	longText := strings.Repeat("x", 100001)
	cases := []struct {
		name          string
		actor         store.Actor
		opportunityID string
		key           string
		version       int64
		text          string
	}{
		{"foreign actor", store.Actor{Kind: "agent", ID: "codex"}, f.opportunity.ID, "k", 1, "text"},
		{"empty actor id", store.Actor{Kind: "administrator", ID: ""}, f.opportunity.ID, "k", 1, "text"},
		{"empty opportunity", owner, "", "k", 1, "text"},
		{"empty key", owner, f.opportunity.ID, "", 1, "text"},
		{"padded key", owner, f.opportunity.ID, " k", 1, "text"},
		{"long key", owner, f.opportunity.ID, longKey, 1, "text"},
		{"zero version", owner, f.opportunity.ID, "k", 0, "text"},
		{"empty text", owner, f.opportunity.ID, "k", 1, ""},
		{"long text", owner, f.opportunity.ID, "k", 1, longText},
		{"invalid utf8", owner, f.opportunity.ID, "k", 1, "bad\xfftext"},
		{"nul text", owner, f.opportunity.ID, "k", 1, "bad\x00text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := f.svc.EditOpportunityMaterials(ctx, tc.actor, tc.opportunityID, tc.key, tc.version, tc.text); !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("edit validation: %v", err)
			}
		})
	}
	// A nil Store, Career, or Render reports unavailable. Draft and
	// Relevance stay nil-able: edit never touches them (covered by
	// TestEditIgnoresDraftAndRelevance).
	unavailable := []*materialprep.Service{
		nil,
		{},
		{Store: f.db},
		{Store: f.db, Career: f.career.load},
	}
	for i, svc := range unavailable {
		if _, _, err := svc.EditOpportunityMaterials(ctx, owner, f.opportunity.ID, "k", 1, "text"); !errors.Is(err, materialprep.ErrUnavailable) {
			t.Fatalf("unavailable %d: %v", i, err)
		}
	}
}

func TestEditReviewingReturnsToPreparedAndSentRejected(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "edit-review")
	prepareHeldV1(t, f, "edit-review-prep")
	workflow, err := f.db.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.AdvanceRoleWorkflow(ctx, f.opportunity.ID, workflow.Revision, store.RoleStageReviewing, ""); err != nil {
		t.Fatal(err)
	}
	if _, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-review-1", 1, "Reviewed text."); err != nil || !created {
		t.Fatalf("edit from reviewing: created=%v err=%v", created, err)
	}
	if workflow, err = f.db.RoleWorkflow(ctx, f.opportunity.ID); err != nil || workflow.Stage != store.RoleStagePrepared {
		t.Fatalf("edit stage: %+v %v", workflow, err)
	}
	if workflow, err = f.db.AdvanceRoleWorkflow(ctx, f.opportunity.ID, workflow.Revision, store.RoleStageReviewing, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.AdvanceRoleWorkflow(ctx, f.opportunity.ID, workflow.Revision, store.RoleStageSent, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-review-2", 2, "Sent text."); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("edit from sent: %v", err)
	}
}

func TestEditRenderPathDeterministic(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "edit-deterministic")
	prepareHeldV1(t, f, "edit-deterministic-prep")
	// Typst-significant markup plus multibyte text past the focus bound.
	text := "Fix #1: ship *fast* — costs $5 & “quoted” é🎯 " + strings.Repeat("pad ", 400)
	if _, _, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-det-1", 1, text); err != nil {
		t.Fatal(err)
	}
	first := f.render.inputs[len(f.render.inputs)-1]
	focus := first.Draft.Focus
	if !utf8.ValidString(focus.Text) || len(focus.Text) > 1200 || len(focus.Text) < 1197 {
		t.Fatalf("focus truncation: %d bytes", len(focus.Text))
	}
	if !strings.HasPrefix(text, focus.Text) {
		t.Fatalf("focus is not an exact prefix: %q", focus.Text)
	}
	if len(focus.Citations) != 1 || focus.Citations[0].SourceID != "owner-edit" || focus.Citations[0].Excerpt != focus.Text {
		t.Fatalf("focus citation: %+v", focus.Citations)
	}
	found := false
	for _, source := range first.Sources {
		if source.ID != "owner-edit" {
			continue
		}
		found = true
		if source.Body != text || !source.Approved {
			t.Fatalf("edit source: approved=%v bytes=%d", source.Approved, len(source.Body))
		}
	}
	if !found {
		t.Fatalf("edit source missing: %+v", first.Sources)
	}
	if _, _, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-det-2", 2, text); err != nil {
		t.Fatal(err)
	}
	second := f.render.inputs[len(f.render.inputs)-1]
	if second.Draft.Focus.Text != focus.Text || len(second.Sources) != len(first.Sources) {
		t.Fatalf("edit render not deterministic")
	}
	for i := range first.Sources {
		if second.Sources[i] != first.Sources[i] {
			t.Fatalf("edit sources not deterministic at %d", i)
		}
	}
}

func TestEditBlankTextFallsBackToRoleFocus(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "edit-blank")
	prepareHeldV1(t, f, "edit-blank-prep")
	text := "   \n  "
	view, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-blank-1", 1, text)
	if err != nil || !created {
		t.Fatalf("blank edit: created=%v err=%v", created, err)
	}
	input := f.render.inputs[len(f.render.inputs)-1]
	for _, source := range input.Sources {
		if source.ID == "owner-edit" {
			t.Fatalf("blank edit must omit the edit source")
		}
	}
	if len(input.Draft.Focus.Citations) != 1 || input.Draft.Focus.Citations[0].SourceID != "role-description" {
		t.Fatalf("blank edit focus: %+v", input.Draft.Focus)
	}
	manifest := readEditManifest(t, f, view.PackID)
	if manifest.Material.Text != text {
		t.Fatalf("blank edit stored text: %q", manifest.Material.Text)
	}
}

func TestEditIgnoresDraftAndRelevance(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "edit-nollm")
	prepareHeldV1(t, f, "edit-nollm-prep")
	// Nil collaborators would panic if touched: edit must succeed with
	// only Store, Career, and Render wired.
	f.svc.Draft = nil
	f.svc.Relevance = nil
	if _, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "edit-nollm-1", 1, "Exact text, no model."); err != nil || !created {
		t.Fatalf("edit without draft/relevance: created=%v err=%v", created, err)
	}
}
