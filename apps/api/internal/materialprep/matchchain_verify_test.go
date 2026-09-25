package materialprep_test

import (
	"context"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// RW-A3 chain verification where answers meet preparation: exact answer text
// and versions carried into materials, check/question-set/workflow pins
// fencing every prepare, and later answer edits producing new immutable
// versions without rewriting history.

// Answered, blank and unset values flow into the committed material with byte
// identity, and no draft turn runs when no required question is unset.
func TestMatchChainPreparePreservesAnswerText(t *testing.T) {
	f := setupPrep(t, "chain-text")
	ctx := context.Background()
	exact := "I want this role for its Go platform work.  Double  spaces kept."
	first := savePrepAnswer(t, f, 0, exact)
	second := savePrepAnswer(t, f, 1, "I shipped a billing service in Go.")
	blanked := savePrepAnswer(t, f, 2, "")
	checkID, questionSet, workflowRev := prepPins(t, f)
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"chain-text-key", checkID, questionSet, workflowRev)
	if err != nil || !created || view.Status != store.MaterialStatusPrepared {
		t.Fatalf("prepare: %+v %v", view, err)
	}
	if f.draft.calls != 0 {
		t.Fatalf("draft turn ran with zero required+unset: %d", f.draft.calls)
	}
	_, manifest := readCommittedManifest(t, f, view.Current.PackID)
	if manifest.Role.OpportunityID != f.opportunity.ID {
		t.Fatalf("manifest role: %+v", manifest.Role)
	}
	if manifest.Material.CheckID != checkID || manifest.Material.QuestionSetSHA256 != questionSet {
		t.Fatalf("manifest pins: %+v", manifest.Material)
	}
	if len(manifest.Material.Answers) != 4 {
		t.Fatalf("manifest answers: %+v", manifest.Material.Answers)
	}
	got := manifest.Material.Answers[0]
	if got.QuestionID != f.check.Questions[0].ID || got.State != "answered" ||
		got.Text != exact || got.AnswerVersion != first.Version || got.TextSHA256 != first.TextSHA256 {
		t.Fatalf("carried answer: %+v", got)
	}
	if manifest.Material.Answers[1].TextSHA256 != second.TextSHA256 {
		t.Fatalf("second answer: %+v", manifest.Material.Answers[1])
	}
	blank := manifest.Material.Answers[2]
	if blank.State != "blank" || blank.AnswerVersion != blanked.Version || blank.Text != "" {
		t.Fatalf("carried blank: %+v", blank)
	}
	unset := manifest.Material.Answers[3]
	if unset.QuestionID != f.check.Questions[3].ID || unset.State != "blank" ||
		unset.AnswerVersion != 0 || unset.Text != "" {
		t.Fatalf("carried unset: %+v", unset)
	}
}

// Each prepare pin is fenced independently and fences run before any draft
// spend; valid pins still commit afterwards.
func TestMatchChainPreparePinFences(t *testing.T) {
	f := setupPrep(t, "chain-pins")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	savePrepAnswer(t, f, 1, "I shipped a billing service in Go.")
	checkID, questionSet, workflowRev := prepPins(t, f)
	wrongSet := strings.Repeat("0", 64)
	if wrongSet == questionSet {
		wrongSet = strings.Repeat("1", 64)
	}
	for _, item := range []struct {
		name       string
		check, set string
		workflow   int64
	}{
		{"question set", checkID, wrongSet, workflowRev},
		{"check id", "check-unknown", questionSet, workflowRev},
		{"workflow revision", checkID, questionSet, workflowRev + 9},
	} {
		if _, _, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
			"chain-pins-"+item.name, item.check, item.set, item.workflow); err == nil {
			t.Fatalf("wrong %s accepted", item.name)
		}
	}
	if f.draft.calls != 0 || f.render.calls != 0 {
		t.Fatalf("spend before fences: draft=%d render=%d", f.draft.calls, f.render.calls)
	}
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"chain-pins-good", checkID, questionSet, workflowRev)
	if err != nil || !created || view.Status != store.MaterialStatusPrepared {
		t.Fatalf("prepare after rejections: %+v %v", view, err)
	}
}

// A later exact answer edit commits a new material version carrying the new
// text while the earlier version's manifest stays byte-identical.
func TestMatchChainPrepareAfterAnswerEdit(t *testing.T) {
	f := setupPrep(t, "chain-edit")
	ctx := context.Background()
	owner := testOwner()
	first := savePrepAnswer(t, f, 0, "First draft of my motivation.")
	savePrepAnswer(t, f, 1, "I shipped a billing service in Go.")
	checkID, questionSet, workflowRev := prepPins(t, f)
	before, created, err := f.svc.PrepareOpportunityMaterials(ctx, owner, f.opportunity.ID,
		"chain-edit-v1", checkID, questionSet, workflowRev)
	if err != nil || !created {
		t.Fatalf("first prepare: %+v %v", before, err)
	}
	beforeRaw, _ := readCommittedManifest(t, f, before.Current.PackID)
	edited, err := f.db.SaveAnswerValue(ctx, owner, f.opportunity.ID,
		f.check.Questions[0].ID, store.AnswerValueSaveInput{
			ExpectedAnswerVersion: first.Version, Text: "Second draft, sharper and exact."})
	if err != nil || edited.Version != first.Version+1 {
		t.Fatalf("answer edit: %+v %v", edited, err)
	}
	// The first prepare advanced the workflow; re-pin before version two.
	checkID, questionSet, workflowRev = prepPins(t, f)
	after, created, err := f.svc.PrepareOpportunityMaterials(ctx, owner, f.opportunity.ID,
		"chain-edit-v2", checkID, questionSet, workflowRev)
	if err != nil || !created || after.Current.Version != before.Current.Version+1 {
		t.Fatalf("second prepare: %+v %v", after, err)
	}
	_, afterManifest := readCommittedManifest(t, f, after.Current.PackID)
	if afterManifest.Material.Answers[0].Text != "Second draft, sharper and exact." ||
		afterManifest.Material.Answers[0].AnswerVersion != edited.Version {
		t.Fatalf("edited carry: %+v", afterManifest.Material.Answers[0])
	}
	again, _ := readCommittedManifest(t, f, before.Current.PackID)
	if string(again) != string(beforeRaw) {
		t.Fatal("first version manifest changed after the edit")
	}
}
