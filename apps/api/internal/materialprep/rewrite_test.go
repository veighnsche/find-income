package materialprep_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// stubTurns records Standard rewrite inputs and replays scripted model output.
// It proves rewrite runs through the shared Standard primitive: exactly one
// RunStandard call per rewrite attempt, no retries, no second turn.
type stubTurns struct {
	calls  int
	inputs []musecode.StandardInput
	fn     func(musecode.StandardInput) ([]string, error)
}

func (f *stubTurns) RunStandard(_ context.Context, input musecode.StandardInput) (materialprep.StandardResult, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	messages, err := f.fn(input)
	if err != nil {
		return materialprep.StandardResult{}, err
	}
	return materialprep.StandardResult{Messages: messages}, nil
}

func stubPrompt(input musecode.StandardInput) string { return input.Context["prompt"] }

// rewriteManifestMaterial mirrors the rewrite manifest section for test
// assertions: one exact ordinal text per pinned question.
type rewriteManifestMaterial struct {
	CheckID           string `json:"checkId"`
	QuestionSetSHA256 string `json:"questionSetSha256"`
	Origin            string `json:"origin"`
	Answers           []struct {
		QuestionID string `json:"questionId"`
		Text       string `json:"text"`
	} `json:"answers"`
}

type rewriteCommittedManifest struct {
	Role struct {
		OpportunityID string `json:"opportunityId"`
	} `json:"role"`
	Material rewriteManifestMaterial `json:"material"`
}

func readRewriteManifest(t *testing.T, f prepFixture, packID string) rewriteCommittedManifest {
	t.Helper()
	pack, err := f.db.ApplicationPack(context.Background(), packID)
	if err != nil {
		t.Fatal(err)
	}
	var manifest rewriteCommittedManifest
	if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// rewritePayloadJSON builds one strict rewrite response covering every
// fixture question in shuffled order: the service must accept any order and
// commit ordinal.
func rewritePayloadJSON(t *testing.T, f prepFixture, texts map[string]string) string {
	t.Helper()
	type entry struct {
		QuestionID string `json:"questionId"`
		Text       string `json:"text"`
	}
	ids := []string{f.check.Questions[2].ID, f.check.Questions[0].ID, f.check.Questions[3].ID, f.check.Questions[1].ID}
	payload := struct {
		Texts []entry `json:"texts"`
	}{}
	for _, id := range ids {
		payload.Texts = append(payload.Texts, entry{QuestionID: id, Text: texts[id]})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// setupRewrite prepares a held v1 (Q0 answered, Q1 held) and wires the
// rewrite turn stub through the shared Standard shape: Draft becomes a
// StandardDrafter carrying the stub runner. The default stub covers every
// pinned question with Q1 (required) left blank, exercising the honest hold.
func setupRewrite(t *testing.T, key string) (prepFixture, *stubTurns) {
	t.Helper()
	f := setupPrep(t, key)
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	prepareHeldV1(t, f, key+"-prep")
	texts := map[string]string{
		f.check.Questions[0].ID: "Rewritten motivation for the Go platform role.",
		f.check.Questions[1].ID: "",
		f.check.Questions[2].ID: "Rewritten extra note.",
		f.check.Questions[3].ID: "",
	}
	payload := rewritePayloadJSON(t, f, texts)
	turns := &stubTurns{fn: func(musecode.StandardInput) ([]string, error) { return []string{payload}, nil }}
	f.svc.Draft = &materialprep.StandardDrafter{Runner: turns}
	return f, turns
}

func TestRewriteOpportunityMaterialsCommitsVersion(t *testing.T) {
	ctx := context.Background()
	f, turns := setupRewrite(t, "rewrite-commit")
	// Nil Relevance would panic if touched: rewrite runs no Jev assessment.
	f.svc.Relevance = nil
	draftCalls, renderCalls := f.draft.calls, f.render.calls
	instruction := "Tighten every answer to two sentences."
	view, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-key-1", 1, instruction)
	if err != nil || !created {
		t.Fatalf("rewrite: created=%v err=%v", created, err)
	}
	if view.Version != 2 || view.Provenance.Origin != store.MaterialOriginRewrite {
		t.Fatalf("rewrite version: %+v", view.Provenance)
	}
	if view.Provenance.RewriteOf == nil || *view.Provenance.RewriteOf != 1 {
		t.Fatalf("rewrite linkage: %+v", view.Provenance)
	}
	held := f.check.Questions[1].ID
	if view.Readiness.Ready || len(view.Readiness.MissingRequired) != 1 || view.Readiness.MissingRequired[0] != held ||
		len(view.Readiness.Held) != 1 || view.Readiness.Held[0] != held {
		t.Fatalf("rewrite readiness: %+v", view.Readiness)
	}
	if turns.calls != 1 {
		t.Fatalf("rewrite turns: %d", turns.calls)
	}
	if f.draft.calls != draftCalls {
		t.Fatalf("rewrite called DraftRequiredAnswers: %d->%d", draftCalls, f.draft.calls)
	}
	if f.render.calls != renderCalls+1 {
		t.Fatalf("rewrite renders once: %d->%d", renderCalls, f.render.calls)
	}
	stdInput := turns.inputs[0]
	if stdInput.Purpose != materialprep.StandardRewritePurpose || stdInput.BundleRef == "" {
		t.Fatalf("rewrite Standard input purpose/bundle: %+v", stdInput)
	}
	if len(stdInput.Targets) != len(f.check.Questions) {
		t.Fatalf("rewrite Standard targets: %v", stdInput.Targets)
	}
	prompt := stubPrompt(stdInput)
	if !strings.Contains(prompt, instruction) {
		t.Fatalf("rewrite prompt lacks instruction")
	}
	for _, question := range f.check.Questions {
		if !strings.Contains(prompt, question.ID) || !strings.Contains(prompt, question.Text) {
			t.Fatalf("rewrite prompt lacks question %q", question.ID)
		}
	}
	if strings.Contains(prompt, "jobs@example.invalid") {
		t.Fatalf("rewrite prompt carries employer contact handles")
	}
	manifest := readRewriteManifest(t, f, view.PackID)
	if manifest.Material.Origin != store.MaterialOriginRewrite {
		t.Fatalf("rewrite manifest origin: %+v", manifest.Material)
	}
	if len(manifest.Material.Answers) != len(f.check.Questions) {
		t.Fatalf("rewrite manifest answers: %d", len(manifest.Material.Answers))
	}
	for i, answer := range manifest.Material.Answers {
		want := f.check.Questions[i].ID
		if answer.QuestionID != want {
			t.Fatalf("rewrite manifest answer %d: %q want %q", i, answer.QuestionID, want)
		}
	}
	if manifest.Material.Answers[0].Text != "Rewritten motivation for the Go platform role." ||
		manifest.Material.Answers[1].Text != "" || manifest.Material.Answers[2].Text != "Rewritten extra note." {
		t.Fatalf("rewrite manifest texts: %+v", manifest.Material.Answers)
	}
	workflow, err := f.db.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Stage != store.RoleStagePrepared {
		t.Fatalf("rewrite stage: %q", workflow.Stage)
	}
}

func TestRewriteSingleTurnFencing(t *testing.T) {
	ctx := context.Background()
	f, turns := setupRewrite(t, "rewrite-fence")
	ids := []string{f.check.Questions[0].ID, f.check.Questions[1].ID, f.check.Questions[2].ID, f.check.Questions[3].ID}
	full := map[string]string{ids[0]: "a", ids[1]: "b", ids[2]: "c", ids[3]: "d"}
	good := rewritePayloadJSON(t, f, full)
	missing := `{"texts":[{"questionId":"` + ids[0] + `","text":"a"},{"questionId":"` + ids[1] + `","text":"b"},{"questionId":"` + ids[2] + `","text":"c"}]}`
	cases := []struct {
		name     string
		messages []string
	}{
		{"missing question", []string{missing}},
		{"extra question", []string{`{"texts":[{"questionId":"` + ids[0] + `","text":"a"},{"questionId":"` + ids[1] + `","text":"b"},{"questionId":"` + ids[2] + `","text":"c"},{"questionId":"` + ids[3] + `","text":"d"},{"questionId":"q-unknown","text":"e"}]}`}},
		{"duplicate question", []string{`{"texts":[{"questionId":"` + ids[0] + `","text":"a"},{"questionId":"` + ids[0] + `","text":"b"},{"questionId":"` + ids[2] + `","text":"c"},{"questionId":"` + ids[3] + `","text":"d"}]}`}},
		{"empty question id", []string{`{"texts":[{"questionId":"","text":"a"},{"questionId":"` + ids[1] + `","text":"b"},{"questionId":"` + ids[2] + `","text":"c"},{"questionId":"` + ids[3] + `","text":"d"}]}`}},
		{"overlong text", []string{`{"texts":[{"questionId":"` + ids[0] + `","text":"` + strings.Repeat("x", 20001) + `"},{"questionId":"` + ids[1] + `","text":"b"},{"questionId":"` + ids[2] + `","text":"c"},{"questionId":"` + ids[3] + `","text":"d"}]}`}},
		{"nul text", []string{`{"texts":[{"questionId":"` + ids[0] + `","text":"a` + "\\u0000" + `b"},{"questionId":"` + ids[1] + `","text":"b"},{"questionId":"` + ids[2] + `","text":"c"},{"questionId":"` + ids[3] + `","text":"d"}]}`}},
		{"malformed", []string{`{nope}`}},
		{"trailing content", []string{good + " trailing"}},
		{"unknown field", []string{`{"texts":[],"extra":1}`}},
		{"empty messages", nil},
		{"blank message", []string{"  "}},
		{"unclosed fence", []string{"```json\n" + good}},
	}
	for i, tc := range cases {
		turns.fn = func(musecode.StandardInput) ([]string, error) { return tc.messages, nil }
		turnCalls, renderCalls := turns.calls, f.render.calls
		_, _, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-fence-key", 1, "instruction")
		if err == nil {
			t.Fatalf("%s: rewrite accepted bad output", tc.name)
		}
		if errors.Is(err, store.ErrInvalid) || errors.Is(err, materialprep.ErrUnavailable) {
			t.Fatalf("%s: model failure misclassified: %v", tc.name, err)
		}
		if turns.calls != turnCalls+1 {
			t.Fatalf("%s: turns %d->%d, want exactly one", tc.name, turnCalls, turns.calls)
		}
		if f.render.calls != renderCalls {
			t.Fatalf("%s: rendered after bad output", tc.name)
		}
		_ = i
	}
	// No version was committed by any fenced attempt.
	if _, err := f.db.OpportunityMaterialVersion(ctx, f.opportunity.ID, 2); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fenced attempts committed: %v", err)
	}
}

func TestRewriteAcceptsFencedJSON(t *testing.T) {
	ctx := context.Background()
	f, turns := setupRewrite(t, "rewrite-fenced")
	ids := []string{f.check.Questions[0].ID, f.check.Questions[1].ID, f.check.Questions[2].ID, f.check.Questions[3].ID}
	payload := rewritePayloadJSON(t, f, map[string]string{ids[0]: "a", ids[1]: "b", ids[2]: "c", ids[3]: "d"})
	turns.fn = func(musecode.StandardInput) ([]string, error) { return []string{"```json\n" + payload + "\n```"}, nil }
	if _, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-fenced-1", 1, "instruction"); err != nil || !created {
		t.Fatalf("fenced rewrite: created=%v err=%v", created, err)
	}
}

func TestRewriteRequiresSharedStandardRunner(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "rewrite-norunner")
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	prepareHeldV1(t, f, "rewrite-norunner-prep")
	renderCalls := f.render.calls
	// Foreign Drafter implementations cannot supply rewrite turns.
	if _, _, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-norunner-1", 1, "instruction"); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("foreign drafter: %v", err)
	}
	f.svc.Draft = nil
	if _, _, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-norunner-2", 1, "instruction"); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("nil drafter: %v", err)
	}
	f.svc.Draft = &materialprep.StandardDrafter{}
	if _, _, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-norunner-3", 1, "instruction"); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("nil runner: %v", err)
	}
	if f.render.calls != renderCalls {
		t.Fatalf("unavailable rewrite rendered")
	}
}

func TestRewriteValidation(t *testing.T) {
	ctx := context.Background()
	f, _ := setupRewrite(t, "rewrite-invalid")
	owner := testOwner()
	longInstruction := strings.Repeat("é", 2001)
	cases := []struct {
		name          string
		actor         store.Actor
		opportunityID string
		key           string
		version       int64
		instruction   string
	}{
		{"foreign actor", store.Actor{Kind: "agent", ID: "codex"}, f.opportunity.ID, "k", 1, "do it"},
		{"empty key", owner, f.opportunity.ID, "", 1, "do it"},
		{"zero version", owner, f.opportunity.ID, "k", 0, "do it"},
		{"invalid utf8", owner, f.opportunity.ID, "k", 1, "bad\xfftext"},
		{"long instruction", owner, f.opportunity.ID, "k", 1, longInstruction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := f.svc.RewriteOpportunityMaterials(ctx, tc.actor, tc.opportunityID, tc.key, tc.version, tc.instruction); !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("rewrite validation: %v", err)
			}
		})
	}
	if _, _, err := (&materialprep.Service{}).RewriteOpportunityMaterials(ctx, owner, f.opportunity.ID, "k", 1, "do it"); !errors.Is(err, materialprep.ErrUnavailable) {
		t.Fatalf("unwired service: %v", err)
	}
}

func TestRewriteReplayAfterSuccess(t *testing.T) {
	ctx := context.Background()
	f, turns := setupRewrite(t, "rewrite-replay")
	ids := []string{f.check.Questions[0].ID, f.check.Questions[1].ID, f.check.Questions[2].ID, f.check.Questions[3].ID}
	planA := rewritePayloadJSON(t, f, map[string]string{ids[0]: "Plan A motivation.", ids[1]: "Plan A story.", ids[2]: "Plan A note.", ids[3]: ""})
	planB := rewritePayloadJSON(t, f, map[string]string{ids[0]: "Plan B motivation.", ids[1]: "Plan A story.", ids[2]: "Plan A note.", ids[3]: ""})
	turns.fn = func(input musecode.StandardInput) ([]string, error) {
		if strings.Contains(stubPrompt(input), "PLAN-B") {
			return []string{planB}, nil
		}
		return []string{planA}, nil
	}
	view, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-replay-1", 1, "Write PLAN-A.")
	if err != nil || !created || view.Version != 2 || !view.Readiness.Ready {
		t.Fatalf("first rewrite: %+v %v", view, err)
	}
	renderCalls := f.render.calls
	replayed, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-replay-1", 1, "Write PLAN-A.")
	if err != nil || created {
		t.Fatalf("replay: created=%v err=%v", created, err)
	}
	if replayed.Version != 2 || replayed.PackID != view.PackID {
		t.Fatalf("replay version: %+v", replayed)
	}
	// The turn runs before the probe (the replay digest needs the resolved
	// texts), so a retry costs one turn but never a second render.
	if turns.calls != 2 {
		t.Fatalf("replay turns: %d", turns.calls)
	}
	if f.render.calls != renderCalls {
		t.Fatalf("replay rendered: %d->%d", renderCalls, f.render.calls)
	}
	if _, _, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-replay-1", 1, "Write PLAN-B."); !errors.Is(err, store.ErrRoundIdempotencyConflict) {
		t.Fatalf("changed input same key: %v", err)
	}
}

func TestRewriteReviewingReturnsToPrepared(t *testing.T) {
	ctx := context.Background()
	f, _ := setupRewrite(t, "rewrite-review")
	workflow, err := f.db.RoleWorkflow(ctx, f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.AdvanceRoleWorkflow(ctx, f.opportunity.ID, workflow.Revision, store.RoleStageReviewing, ""); err != nil {
		t.Fatal(err)
	}
	if _, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-review-1", 1, "instruction"); err != nil || !created {
		t.Fatalf("rewrite from reviewing: created=%v err=%v", created, err)
	}
	if workflow, err = f.db.RoleWorkflow(ctx, f.opportunity.ID); err != nil || workflow.Stage != store.RoleStagePrepared {
		t.Fatalf("rewrite stage: %+v %v", workflow, err)
	}
}

func TestRewriteOverDirectEditBase(t *testing.T) {
	ctx := context.Background()
	f, turns := setupRewrite(t, "rewrite-editbase")
	if _, created, err := f.svc.EditOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-editbase-edit", 1, "COMBINED-EDIT-MARKER owner text."); err != nil || !created {
		t.Fatalf("base edit: created=%v err=%v", created, err)
	}
	if turns.calls != 0 {
		t.Fatalf("edit ran the rewrite runner: %d", turns.calls)
	}
	view, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-editbase-1", 2, "instruction")
	if err != nil || !created || view.Version != 3 {
		t.Fatalf("rewrite over edit: %+v %v", view, err)
	}
	if view.Provenance.RewriteOf == nil || *view.Provenance.RewriteOf != 2 {
		t.Fatalf("rewrite linkage: %+v", view.Provenance)
	}
	if !strings.Contains(stubPrompt(turns.inputs[0]), "COMBINED-EDIT-MARKER") {
		t.Fatalf("rewrite prompt lacks combined edit context")
	}
	if !strings.Contains(stubPrompt(turns.inputs[0]), "I want this role for its Go platform work.") {
		t.Fatalf("rewrite prompt lacks saved answer context")
	}
}

func TestRewriteEmptyInstructionAllowed(t *testing.T) {
	ctx := context.Background()
	f, turns := setupRewrite(t, "rewrite-noinstr")
	if _, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-noinstr-1", 1, ""); err != nil || !created {
		t.Fatalf("empty instruction rewrite: created=%v err=%v", created, err)
	}
	if !strings.Contains(stubPrompt(turns.inputs[0]), "(none") {
		t.Fatalf("empty instruction prompt: %q", stubPrompt(turns.inputs[0])[:200])
	}
}

func TestRewriteAllBlankHeld(t *testing.T) {
	ctx := context.Background()
	f, turns := setupRewrite(t, "rewrite-blank")
	ids := []string{f.check.Questions[0].ID, f.check.Questions[1].ID, f.check.Questions[2].ID, f.check.Questions[3].ID}
	turns.fn = func(musecode.StandardInput) ([]string, error) {
		return []string{rewritePayloadJSON(t, f, map[string]string{ids[0]: "", ids[1]: "", ids[2]: "", ids[3]: ""})}, nil
	}
	view, created, err := f.svc.RewriteOpportunityMaterials(ctx, testOwner(), f.opportunity.ID, "rewrite-blank-1", 1, "Blank everything.")
	if err != nil || !created {
		t.Fatalf("blank rewrite: created=%v err=%v", created, err)
	}
	if view.Readiness.Ready || len(view.Readiness.MissingRequired) != 2 || len(view.Readiness.Held) != 2 {
		t.Fatalf("blank rewrite readiness: %+v", view.Readiness)
	}
}
