package store

import (
	"context"
	"errors"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// RW-A3 chain verification: selected-only checking, sourced questions,
// evidence reuse, per-role blockers, honest none-fit matching, exact answer
// preservation and workflow identity across the check/answer service chain.

// Selection alone saves a choice and starts no work: no check row, no match,
// virtual selected stage.
func TestCheckChainSelectionWithoutWork(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "chain-quiet-select")

	status, err := s.CurrentJobCheck(ctx, opportunity.ID)
	if err != nil || status.Status != CheckOverallNotChecked || status.Check != nil {
		t.Fatalf("fresh selection: %+v %v", status, err)
	}
	workflow, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil || workflow.Stage != RoleStageSelected || workflow.Revision != 0 {
		t.Fatalf("fresh workflow: %+v %v", workflow, err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM job_checks WHERE opportunity_id=?`,
		opportunity.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("check rows after selection: %d %v", count, err)
	}
	if _, err := s.CurrentAnswerMatch(ctx, opportunity.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("match before check: %v", err)
	}
	workflows, err := s.ListRoleWorkflows(ctx)
	if err != nil || len(workflows) != 1 || workflows[0].OpportunityID != opportunity.ID ||
		workflows[0].Stage != RoleStageSelected {
		t.Fatalf("workflow list: %+v %v", workflows, err)
	}
}

// Every check/answer entry rejects unselected roles; dismissal revokes access.
func TestCheckChainUnselectedRejected(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, owner, fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	start := CheckStartInput{RequestKey: "chain-reject", ExpectedOpportunityRevision: opportunity.Revision}
	if _, _, err := s.StartJobCheck(ctx, owner, opportunity.ID, start); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected start: %v", err)
	}
	if _, err := s.SaveAnswerValue(ctx, owner, opportunity.ID, "q-missing",
		AnswerValueSaveInput{Text: "x"}); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected answer save: %v", err)
	}
	if _, err := s.CurrentAnswerMatch(ctx, opportunity.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected match read: %v", err)
	}
	if _, err := s.CurrentQuestionAnswers(ctx, opportunity.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected answer read: %v", err)
	}
	selected := selectFixtureOpportunity(t, s, company.ID, "chain-reject-select")
	if _, _, err := s.SetOwnerOpportunityDecision(ctx, owner, selected.ID, OwnerDecisionInput{
		RequestKey: "chain-reject-dismiss", ExpectedOpportunityRevision: selected.Revision,
		ExpectedDecisionRevision: 1, Decision: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartJobCheck(ctx, owner, selected.ID, start); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("dismissed start: %v", err)
	}
	if _, err := s.CurrentQuestionAnswers(ctx, selected.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("dismissed answer read: %v", err)
	}
}

// Every saved question cites a real capture span; unsourced questions fail and
// the check stays pending. Saved source, route and documents persist.
func TestCheckChainQuestionProvenance(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "chain-prov-select")
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "chain-prov-check", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "chain-prov-round",
		[]string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)

	bad := checkSaveFixture(opportunity.ID, started.ID, capture.ID)
	bad.Questions[0].SourceSpan.CaptureID = "capture-unknown"
	if _, _, err := applyCheckSave(t, s, round, capability, "chain-prov-bad",
		started.WorkflowRevision, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsourced question: %v", err)
	}
	if status, err := s.CurrentJobCheck(ctx, opportunity.ID); err != nil || status.Status != CheckOverallChecking {
		t.Fatalf("check after rejected save: %+v %v", status, err)
	}
	if _, _, err := applyCheckSave(t, s, round, capability, "chain-prov-good",
		started.WorkflowRevision, checkSaveFixture(opportunity.ID, started.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetJobCheck(ctx, opportunity.ID, started.ID)
	if err != nil || view.Status != CheckStatusChecked || len(view.Questions) != 2 {
		t.Fatalf("completed check: %+v %v", view, err)
	}
	if view.Questions[0].Text != "Why do you want this role?" || view.Questions[0].ID == view.Questions[1].ID {
		t.Fatalf("question identity/order: %+v", view.Questions)
	}
	for _, question := range view.Questions {
		if question.SourceSpan.CaptureID == "" || question.SourceExcerpt == "" || question.TextSHA256 == "" {
			t.Fatalf("unsourced saved question: %+v", question)
		}
		var found int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM source_captures WHERE id=?`,
			question.SourceSpan.CaptureID).Scan(&found); err != nil {
			t.Fatalf("span capture missing: %v", err)
		}
	}
	if len(view.RequestedDocuments) != 1 || view.RequestedDocuments[0].Label != "CV" ||
		view.RequestedDocuments[0].SourceSpan.CaptureID != capture.ID {
		t.Fatalf("saved documents: %+v", view.RequestedDocuments)
	}
	if view.Route.Judgment != CheckRouteJudgmentApplication || view.Route.Kind != CheckRouteDirect {
		t.Fatalf("saved route: %+v", view.Route)
	}
	if len(view.Vacancy.CaptureIDs) != 1 || view.Vacancy.CaptureIDs[0] != capture.ID {
		t.Fatalf("saved vacancy: %+v", view.Vacancy)
	}
}

// One discovery capture is reusable evidence across roles; unknown evidence
// sources are rejected rather than silently accepted.
func TestCheckChainEvidenceReuse(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	first := selectFixtureOpportunity(t, s, company.ID, "chain-reuse-first")
	second := selectFixtureOpportunity(t, s, company.ID, "chain-reuse-second")
	firstStarted, _, err := s.StartJobCheck(ctx, ownerActor(), first.ID,
		CheckStartInput{RequestKey: "chain-reuse-check-1", ExpectedOpportunityRevision: first.Revision})
	if err != nil {
		t.Fatal(err)
	}
	secondStarted, _, err := s.StartJobCheck(ctx, ownerActor(), second.ID,
		CheckStartInput{RequestKey: "chain-reuse-check-2", ExpectedOpportunityRevision: second.Revision})
	if err != nil {
		t.Fatal(err)
	}
	shared := insertCheckCapture(t, s, "https://harbour.example/jobs/shared")
	round, capability := startCheckRound(t, s, "chain-reuse-round",
		[]string{RoundCodexTurn, RoundCheckSave}, first.ID, second.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, "chain-reuse-save-1",
		firstStarted.WorkflowRevision, checkSaveFixture(first.ID, firstStarted.ID, shared.ID)); err != nil {
		t.Fatal(err)
	}
	secondSave := checkSaveFixture(second.ID, secondStarted.ID, shared.ID)
	secondSave.Vacancy.EvidenceSourceIDs = []string{"evidence-unknown"}
	if _, _, err := applyCheckSave(t, s, round, capability, "chain-reuse-bad",
		secondStarted.WorkflowRevision, secondSave); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown evidence source: %v", err)
	}
	secondSave.Vacancy.EvidenceSourceIDs = nil
	if _, _, err := applyCheckSave(t, s, round, capability, "chain-reuse-save-2",
		secondStarted.WorkflowRevision, secondSave); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		opportunityID, checkID string
	}{
		{first.ID, firstStarted.ID}, {second.ID, secondStarted.ID},
	} {
		view, err := s.GetJobCheck(ctx, item.opportunityID, item.checkID)
		if err != nil || view.Vacancy.CaptureIDs[0] != shared.ID {
			t.Fatalf("shared evidence on %s: %+v %v", item.opportunityID, view, err)
		}
		for _, question := range view.Questions {
			if question.SourceSpan.CaptureID != shared.ID {
				t.Fatalf("shared span on %s: %+v", item.opportunityID, question)
			}
		}
	}
}

// A blocked role never blocks another role: independent verdicts, independent
// recheck, and staleness stays per-role.
func TestCheckChainBlockedRolesIndependent(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	blocked := selectFixtureOpportunity(t, s, company.ID, "chain-block-a")
	healthy := selectFixtureOpportunity(t, s, company.ID, "chain-block-b")
	blockedStarted, _, err := s.StartJobCheck(ctx, ownerActor(), blocked.ID,
		CheckStartInput{RequestKey: "chain-block-check-a", ExpectedOpportunityRevision: blocked.Revision})
	if err != nil {
		t.Fatal(err)
	}
	healthyStarted, _, err := s.StartJobCheck(ctx, ownerActor(), healthy.ID,
		CheckStartInput{RequestKey: "chain-block-check-b", ExpectedOpportunityRevision: healthy.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "chain-block-round",
		[]string{RoundCodexTurn, RoundCheckSave}, blocked.ID, healthy.ID)
	blockedSave := checkSaveFixture(blocked.ID, blockedStarted.ID, capture.ID)
	blockedSave.Blocked = &CheckBlockedInput{Code: CheckBlockedSourceUnavailable,
		Detail: "The vacancy page no longer loads; only the cached snippet was read."}
	if _, _, err := applyCheckSave(t, s, round, capability, "chain-block-save-a",
		blockedStarted.WorkflowRevision, blockedSave); err != nil {
		t.Fatal(err)
	}
	if _, _, err := applyCheckSave(t, s, round, capability, "chain-block-save-b",
		healthyStarted.WorkflowRevision, checkSaveFixture(healthy.ID, healthyStarted.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	if status, err := s.CurrentJobCheck(ctx, blocked.ID); err != nil || status.Status != CheckOverallBlocked ||
		status.Check.BlockedReason.Code != CheckBlockedSourceUnavailable {
		t.Fatalf("blocked role: %+v %v", status, err)
	}
	if workflow, err := s.RoleWorkflow(ctx, blocked.ID); err != nil || workflow.Stage != RoleStageBlocked {
		t.Fatalf("blocked stage: %+v %v", workflow, err)
	}
	if status, err := s.CurrentJobCheck(ctx, healthy.ID); err != nil || status.Status != CheckOverallChecked {
		t.Fatalf("healthy role: %+v %v", status, err)
	}
	workflow, err := s.RoleWorkflow(ctx, blocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	recheck, created, err := s.StartJobCheck(ctx, ownerActor(), blocked.ID, CheckStartInput{
		RequestKey: "chain-block-recheck", ExpectedOpportunityRevision: blocked.Revision,
		ExpectedWorkflowRevision: workflow.Revision})
	if err != nil || !created || recheck.Status != CheckStatusChecking {
		t.Fatalf("blocked recheck: %+v %v", recheck, err)
	}
	if status, err := s.CurrentJobCheck(ctx, healthy.ID); err != nil || status.Status != CheckOverallChecked {
		t.Fatalf("healthy role after recheck: %+v %v", status, err)
	}
	notes := "Owner note after the healthy check completed"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), healthy.ID,
		OpportunityPatch{ExpectedRevision: healthy.Revision, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if status, err := s.CurrentJobCheck(ctx, healthy.ID); err != nil || status.Status != CheckOverallOutdated {
		t.Fatalf("stale healthy role: %+v %v", status, err)
	}
	if status, err := s.CurrentJobCheck(ctx, blocked.ID); err != nil || status.Status != CheckOverallChecking {
		t.Fatalf("rechecking role after peer edit: %+v %v", status, err)
	}
}

// With no fitting approved answer the chain stays honest: deterministic
// none_fits with no Jev attempt, an unmatched run, and owner-written saves.
func TestCheckChainMatchNoneFitsHonest(t *testing.T) {
	f := setupAnswerMatch(t, "chain-none")
	ctx := context.Background()
	catalog := answerMatchCatalog(t, f.store)
	input := answerMatchBatch(f.check, catalog, f.check.Questions, nil)
	result, err := jev.MatchAnswers(ctx, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range result.Selections {
		if !selection.NoneFits {
			t.Fatalf("deterministic selection must be none_fits: %+v", selection)
		}
	}
	view, created, err := f.store.SaveAnswerMatch(ctx, ownerActor(), f.round.ID, "",
		"chain-none-key", input, result)
	if err != nil || !created || view.Status != AnswerMatchStatusUnmatched {
		t.Fatalf("none-fit save: %+v %v", view, err)
	}
	current, err := f.store.CurrentAnswerMatch(ctx, f.opportunity.ID)
	if err != nil || current.Status != AnswerMatchStatusUnmatched || len(current.Matches) != 2 {
		t.Fatalf("none-fit read: %+v %v", current, err)
	}
	for _, match := range current.Matches {
		if !match.Choice.NoneFits || !match.Deterministic || match.JevAttemptID != "" || match.Confidence != 0 {
			t.Fatalf("none-fit match row: %+v", match)
		}
	}
	saved, err := f.store.SaveAnswerValue(ctx, ownerActor(), f.opportunity.ID,
		f.check.Questions[0].ID, AnswerValueSaveInput{Text: "Owner's own words."})
	if err != nil || saved.Version != 1 || saved.Provenance.Origin != AnswerValueOriginOwnerWritten {
		t.Fatalf("unsuggested save: %+v %v", saved, err)
	}
}

// Exact text and revisions survive the chain: byte-identical saves, guarded
// versions, explicit blanks, and stale suggestions resolve as owner-written.
func TestCheckChainAnswerPreservation(t *testing.T) {
	f := setupAnswerMatch(t, "chain-preserve")
	ctx := context.Background()
	owner := ownerActor()
	catalog := answerMatchCatalog(t, f.store)
	remote := answerMatchOffered(f.answers[0])
	questions := f.check.Questions
	remoteText := f.answers[0].Versions[len(f.answers[0].Versions)-1].Text
	input := answerMatchBatch(f.check, catalog, questions[:1], map[string][]jev.AnswerMatchCandidate{
		questions[0].ID: {remote},
	})
	view, _ := runAnswerMatchBatch(t, f, "chain-pres-key", "chain-pres-attempt", input,
		map[string]string{questions[0].ID: remote.AnswerID})
	if view.Status != AnswerMatchStatusPartial {
		t.Fatalf("single-question run: %+v", view)
	}
	suggested, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, questions[0].ID,
		AnswerValueSaveInput{Text: remoteText})
	if err != nil || suggested.Version != 1 || suggested.Text != remoteText ||
		suggested.Provenance.Origin != AnswerValueOriginJevSuggestion ||
		suggested.Provenance.MatchChoice.AnswerID != remote.AnswerID {
		t.Fatalf("suggested save: %+v %v", suggested, err)
	}
	edited, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, questions[0].ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: remoteText + " Tailored."})
	if err != nil || edited.Version != 2 || edited.Provenance.Origin != AnswerValueOriginOwnerEdited {
		t.Fatalf("edited save: %+v %v", edited, err)
	}
	if _, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, questions[0].ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	padded, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, questions[1].ID,
		AnswerValueSaveInput{Text: "  spaced  "})
	if err != nil || padded.State != AnswerValueStateAnswered || padded.Text != "  spaced  " ||
		padded.Provenance.Origin != AnswerValueOriginOwnerWritten {
		t.Fatalf("whitespace save: %+v %v", padded, err)
	}
	blanked, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, questions[1].ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: padded.Version, Text: ""})
	if err != nil || blanked.State != AnswerValueStateBlank || blanked.Version != padded.Version+1 ||
		blanked.Provenance.Origin != AnswerValueOriginCarriedBlank {
		t.Fatalf("blank save: %+v %v", blanked, err)
	}
	if _, _, err := f.store.ApproveSavedAnswerVersion(ctx, owner, f.answers[0].ID,
		SavedAnswerVersionCreateInput{RequestKey: "chain-pres-approve",
			ExpectedVersion: f.answers[0].CurrentVersion, Text: "I work remotely from a new city."}); err != nil {
		t.Fatal(err)
	}
	afterCatalog, err := f.store.SaveAnswerValue(ctx, owner, f.opportunity.ID, questions[0].ID,
		AnswerValueSaveInput{ExpectedAnswerVersion: edited.Version, Text: "Fresh words."})
	if err != nil || afterCatalog.Provenance.Origin != AnswerValueOriginOwnerWritten ||
		afterCatalog.Provenance.MatchChoice != nil {
		t.Fatalf("stale-catalog save: %+v %v", afterCatalog, err)
	}
	list, err := f.store.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil || list.CheckID != f.check.ID || list.QuestionSetSHA256 != f.check.QuestionSetSHA256 ||
		len(list.Values) != 2 {
		t.Fatalf("answer list: %+v %v", list, err)
	}
}

// One stable opportunity identity flows through selection, check, match and
// answers; revisions advance monotonically and stale pins cannot proceed.
func TestCheckChainWorkflowIdentity(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "chain-ident-select")
	if _, _, err := s.StartJobCheck(ctx, owner, opportunity.ID, CheckStartInput{
		RequestKey: "chain-ident-stale", ExpectedOpportunityRevision: opportunity.Revision,
		ExpectedWorkflowRevision: 5}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale workflow pin: %v", err)
	}
	started, created, err := s.StartJobCheck(ctx, owner, opportunity.ID,
		CheckStartInput{RequestKey: "chain-ident-check", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil || !created || started.OpportunityID != opportunity.ID || started.WorkflowRevision != 1 {
		t.Fatalf("check identity: %+v %v", started, err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "chain-ident-round",
		[]string{RoundCodexTurn, RoundCheckSave, RoundJevRequest}, opportunity.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, "chain-ident-save",
		started.WorkflowRevision, checkSaveFixture(opportunity.ID, started.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	workflow, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil || workflow.OpportunityID != opportunity.ID || workflow.Stage != RoleStageChecked ||
		workflow.OpportunityRev != opportunity.Revision {
		t.Fatalf("checked workflow: %+v %v", workflow, err)
	}
	converged, created, err := s.StartJobCheck(ctx, owner, opportunity.ID,
		CheckStartInput{RequestKey: "chain-ident-again", ExpectedOpportunityRevision: opportunity.Revision,
			ExpectedWorkflowRevision: workflow.Revision})
	if err != nil || created || converged.ID != started.ID {
		t.Fatalf("checked converge: %+v %v %v", converged, created, err)
	}
	catalog, err := s.AnswerCatalogDigest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.CurrentJobCheck(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := answerMatchBatch(*status.Check, catalog, status.Check.Questions, nil)
	result, err := jev.MatchAnswers(ctx, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	match, _, err := s.SaveAnswerMatch(ctx, owner, round.ID, "", "chain-ident-match", input, result)
	if err != nil || match.CheckID != started.ID || match.QuestionSetSHA256 != status.Check.QuestionSetSHA256 {
		t.Fatalf("match identity: %+v %v", match, err)
	}
	saved, err := s.SaveAnswerValue(ctx, owner, opportunity.ID, status.Check.Questions[0].ID,
		AnswerValueSaveInput{Text: "Identity-carried answer."})
	if err != nil || saved.QuestionTextSHA256 != status.Check.Questions[0].TextSHA256 {
		t.Fatalf("answer identity: %+v %v", saved, err)
	}
	answers, err := s.CurrentQuestionAnswers(ctx, opportunity.ID)
	if err != nil || answers.CheckID != started.ID {
		t.Fatalf("answer list identity: %+v %v", answers, err)
	}
}
