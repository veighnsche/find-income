package materialprep_test

import (
	"context"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/agency"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func clarifyStore(f prepFixture) *agency.StoreClarifications {
	return &agency.StoreClarifications{DB: f.db}
}

func openCVClarification(t *testing.T, f prepFixture, key string) agency.Clarification {
	t.Helper()
	item, _, err := agency.OpenClarification(context.Background(), clarifyStore(f), testOwner(), f.opportunity.ID,
		agency.ClarificationOpenInput{RequestKey: key, CheckID: f.check.ID,
			Requirement: agency.ClarificationRequirement{Statement: "Send your CV to jobs@example.invalid",
				CaptureID: f.check.RequestedDocuments[0].SourceSpan.CaptureID,
				SpanStart: f.check.RequestedDocuments[0].SourceSpan.Start,
				SpanEnd:   f.check.RequestedDocuments[0].SourceSpan.End},
			Prompt:       "What should the CV say about reach-truck certification?",
			AffectedWork: []agency.ClarificationWorkRef{{Kind: materialprep.ClarifyWorkArtifact, ID: store.ArtifactCV}}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestPrepareSkipsOpenClarificationTargets(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "clarify-skip")
	f.svc.Clarifications = clarifyStore(f)
	question := openCVClarification(t, f, "clarify-skip-1")
	stub := &stubArtifactDrafter{fn: func(req materialprep.ArtifactDraftRequest) (materialprep.DraftOutcome, error) {
		return materialprep.DraftOutcome{}, nil
	}}
	f.svc.Artifacts = stub
	checkID, questionSet, workflowRev := prepPins(t, f)
	set, created, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"skip-1", checkID, questionSet, workflowRev)
	if err != nil || created {
		t.Fatalf("draft: created=%v err=%v", created, err)
	}
	if stub.calls != 1 {
		t.Fatalf("turns: %d", stub.calls)
	}
	for _, target := range stub.requests[0].Targets {
		if target.Type == store.ArtifactCV {
			t.Fatalf("open-clarification target reached the turn: %+v", stub.requests[0].Targets)
		}
	}
	if entry := artifactEntry(set, store.ArtifactCV); entry.State != store.ArtifactStateHeld {
		t.Fatalf("cv: %+v", entry)
	}
	events, _, err := f.db.ListPrepareActivity(ctx, f.opportunity.ID, "", 25)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == store.PrepareArtifactHeld &&
			strings.Contains(string(event.Payload), question.ID) &&
			strings.Contains(string(event.Payload), "awaiting owner clarification") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no clarification hold journaled: %+v", events)
	}
}

func TestPrepareResumesOnlyDependentItems(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "clarify-resume")
	f.svc.Clarifications = clarifyStore(f)
	question := openCVClarification(t, f, "clarify-resume-1")
	if _, err := agency.AnswerClarification(ctx, clarifyStore(f), testOwner(), question.ID,
		agency.ClarificationAnswer{RequestKey: "clarify-resume-a1", Text: "Reach-truck certificate since 2021."}); err != nil {
		t.Fatal(err)
	}
	stub := &stubArtifactDrafter{fn: func(req materialprep.ArtifactDraftRequest) (materialprep.DraftOutcome, error) {
		if len(req.Clarifications) != 1 || req.Clarifications[0].Text != "Reach-truck certificate since 2021." {
			t.Fatalf("clarified context: %+v", req.Clarifications)
		}
		return materialprep.DraftOutcome{Drafts: []materialprep.ArtifactDraft{{Type: "cv",
			Content: "Go engineer, six years.", FactIDs: []string{careerEvidenceID}}}}, nil
	}}
	f.svc.Artifacts = stub
	checkID, questionSet, workflowRev := prepPins(t, f)
	set, created, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"resume-1", checkID, questionSet, workflowRev)
	if err != nil || !created {
		t.Fatalf("draft: created=%v err=%v", created, err)
	}
	if entry := artifactEntry(set, store.ArtifactCV); entry.State != store.ArtifactStateReady {
		t.Fatalf("cv: %+v", entry)
	}
	if entry := artifactEntry(set, store.ArtifactEmailSubject); entry.State != store.ArtifactStateHeld {
		t.Fatalf("unrelated held item must stay held, not resume: %+v", entry)
	}
	events, _, err := f.db.ListPrepareActivity(ctx, f.opportunity.ID, "", 25)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == store.PrepareArtifactDone && strings.Contains(string(event.Payload), question.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no resume journaled: %+v", events)
	}
}

func TestPrepareOpensClarificationForMissingFact(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "clarify-open")
	f.svc.Clarifications = clarifyStore(f)
	stub := &stubArtifactDrafter{fn: func(materialprep.ArtifactDraftRequest) (materialprep.DraftOutcome, error) {
		return materialprep.DraftOutcome{Held: []materialprep.ArtifactHold{{Type: "cv",
			Reason:      "unsupported claim: the cited evidence does not support the drafted text",
			MissingFact: "I hold a valid reach-truck certificate"}}}, nil
	}}
	f.svc.Artifacts = stub
	checkID, questionSet, workflowRev := prepPins(t, f)
	set, created, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"open-1", checkID, questionSet, workflowRev)
	if err != nil || created {
		t.Fatalf("draft: created=%v err=%v", created, err)
	}
	if entry := artifactEntry(set, store.ArtifactCV); entry.State != store.ArtifactStateHeld {
		t.Fatalf("cv: %+v", entry)
	}
	items, err := clarifyStore(f).ListClarifications(ctx, f.opportunity.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("clarifications: %+v err=%v", items, err)
	}
	question := items[0]
	if question.Status != agency.ClarificationOpen || len(question.AffectedWork) != 1 ||
		question.AffectedWork[0].Kind != materialprep.ClarifyWorkArtifact || question.AffectedWork[0].ID != store.ArtifactCV {
		t.Fatalf("question: %+v", question)
	}
	if !strings.Contains(question.Prompt, "reach-truck") || question.Requirement.CaptureID == "" {
		t.Fatalf("unsourced prompt: %+v", question)
	}
	events, _, err := f.db.ListPrepareActivity(ctx, f.opportunity.ID, "", 25)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == store.PrepareArtifactHeld && strings.Contains(string(event.Payload), question.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("hold journal misses the clarification id: %+v", events)
	}
	// A second Prepare skips the cv without spending another turn on
	// it, while other held types still draft.
	stub.fn = func(req materialprep.ArtifactDraftRequest) (materialprep.DraftOutcome, error) {
		for _, target := range req.Targets {
			if target.Type == store.ArtifactCV {
				t.Fatalf("held cv reached the turn: %+v", req.Targets)
			}
		}
		return materialprep.DraftOutcome{Drafts: []materialprep.ArtifactDraft{
			{Type: "email_subject", Content: "Application: Backend Engineer"}}}, nil
	}
	set, created, err = f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"open-2", checkID, questionSet, workflowRev)
	if err != nil || !created {
		t.Fatalf("second draft: created=%v err=%v", created, err)
	}
	if entry := artifactEntry(set, store.ArtifactEmailSubject); entry.State != store.ArtifactStateReady {
		t.Fatalf("subject: %+v", entry)
	}
	if entry := artifactEntry(set, store.ArtifactCV); entry.State != store.ArtifactStateHeld {
		t.Fatalf("cv: %+v", entry)
	}
}

func TestPrepareConflictsOnMidTurnAnswerChange(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "clarify-fence")
	questionID := f.check.Questions[0].ID
	if _, err := f.db.SaveAnswerValue(ctx, testOwner(), f.opportunity.ID, questionID,
		store.AnswerValueSaveInput{Text: "I like Go."}); err != nil {
		t.Fatal(err)
	}
	f.svc.Artifacts = &stubArtifactDrafter{fn: func(materialprep.ArtifactDraftRequest) (materialprep.DraftOutcome, error) {
		if _, err := f.db.SaveAnswerValue(ctx, testOwner(), f.opportunity.ID, questionID,
			store.AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: "I love Go."}); err != nil {
			t.Fatal(err)
		}
		return materialprep.DraftOutcome{Drafts: []materialprep.ArtifactDraft{{Type: "cv",
			Content: "Go engineer.", AnswerIDs: []string{questionID}}}}, nil
	}}
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"fence-1", checkID, questionSet, workflowRev); err == nil {
		t.Fatal("mid-turn answer change committed")
	}
	if _, err := f.db.GetOpportunityArtifact(ctx, f.opportunity.ID, store.ArtifactCV); err == nil {
		t.Fatal("stale draft committed")
	}
}

func TestPrepareCarriesVacancyContextAndPins(t *testing.T) {
	ctx := context.Background()
	f := setupPrep(t, "clarify-context")
	stub := &stubArtifactDrafter{fn: func(materialprep.ArtifactDraftRequest) (materialprep.DraftOutcome, error) {
		return materialprep.DraftOutcome{Drafts: []materialprep.ArtifactDraft{{Type: "cv",
			Content: "Go engineer.", FactIDs: []string{careerEvidenceID}}}}, nil
	}}
	f.svc.Artifacts = stub
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, _, err := f.svc.DraftOpportunityArtifacts(ctx, testOwner(), f.opportunity.ID,
		"context-1", checkID, questionSet, workflowRev); err != nil {
		t.Fatal(err)
	}
	req := stub.requests[0]
	if req.Description != "Build Go services." || len(req.Requirements) != 1 || len(req.Questions) != 4 {
		t.Fatalf("vacancy context: description=%q requirements=%d questions=%d",
			req.Description, len(req.Requirements), len(req.Questions))
	}
	view, err := f.db.GetOpportunityArtifact(ctx, f.opportunity.ID, store.ArtifactCV)
	if err != nil {
		t.Fatal(err)
	}
	if view.Basis.CheckID != checkID || view.Basis.QuestionSetSHA256 != questionSet ||
		view.Basis.OpportunityRevision != f.opportunity.Revision {
		t.Fatalf("basis pins: %+v", view.Basis)
	}
	if len(view.Basis.FactSHA256) != 1 || view.Basis.FactSHA256[careerEvidenceID] == "" {
		t.Fatalf("fact digests: %+v", view.Basis)
	}
}
