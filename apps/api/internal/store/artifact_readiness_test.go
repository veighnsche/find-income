package store

import (
	"context"
	"encoding/json"
	"testing"
)

func saveReadyCheck(t *testing.T, s *Store, opportunity Opportunity, key string, mutate func(*CheckSaveInput)) CheckView {
	t.Helper()
	ctx := context.Background()
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-" + key, ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/"+key)
	round, capability := startCheckRound(t, s, "round-"+key, []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)
	save := checkSaveFixture(opportunity.ID, started.ID, capture.ContentSHA256)
	if mutate != nil {
		mutate(&save)
	}
	if _, _, err := applyCheckSave(t, s, round, capability, "save-"+key, started.WorkflowRevision, save); err != nil {
		t.Fatalf("save %s: %v", key, err)
	}
	if _, _, err := s.StopRound(ctx, ownerActor(), round.ID); err != nil {
		t.Fatalf("stop %s: %v", key, err)
	}
	if _, err := s.PauseStoppedRound(ctx, ownerActor(), round.ID); err != nil {
		t.Fatalf("pause %s: %v", key, err)
	}
	if _, err := s.FinishRound(ctx, ownerActor(), round.ID, RoundCompleted, "check_saved", "partial", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("finish %s: %v", key, err)
	}
	view, err := s.GetJobCheck(ctx, opportunity.ID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func readinessEntry(set ArtifactReadinessSet, artifactType string) ArtifactReadinessEntry {
	for _, entry := range set.Entries {
		if entry.Type == artifactType {
			return entry
		}
	}
	return ArtifactReadinessEntry{}
}

func TestArtifactReadinessEmailRoute(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-ready")
	view := saveReadyCheck(t, s, opportunity, "ready", nil)
	if view.Status != CheckStatusChecked {
		t.Fatalf("check status: %s", view.Status)
	}
	set, err := s.ArtifactReadiness(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if set.CheckStatus != CheckOverallChecked || set.CheckID != view.ID || len(set.Entries) != 5 {
		t.Fatalf("set: %+v", set)
	}
	cv := readinessEntry(set, ArtifactCV)
	if !cv.Required || cv.State != ArtifactStateHeld || cv.Basis == "" || cv.Current != nil {
		t.Fatalf("cv: %+v", cv)
	}
	subject := readinessEntry(set, ArtifactEmailSubject)
	body := readinessEntry(set, ArtifactEmailBody)
	if !subject.Required || subject.State != ArtifactStateHeld || subject.Basis == "" {
		t.Fatalf("subject: %+v", subject)
	}
	if !body.Required || body.State != ArtifactStateHeld || body.Basis == "" {
		t.Fatalf("body: %+v", body)
	}
	letter := readinessEntry(set, ArtifactCoverLetter)
	if letter.Required || letter.State != ArtifactStateNotRequired || letter.Current != nil {
		t.Fatalf("letter: %+v", letter)
	}
	forms := readinessEntry(set, ArtifactFormValues)
	if !forms.Required || forms.State != ArtifactStateHeld || len(forms.FormValues) != 2 {
		t.Fatalf("forms: %+v", forms)
	}
	// Storing the CV and answering the required question flips both to ready.
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "art-ready", Type: ArtifactCV, Content: "cv text"}); err != nil {
		t.Fatal(err)
	}
	for _, question := range view.Questions {
		if question.Required != CheckRequired {
			continue
		}
		if _, err := s.SaveAnswerValue(ctx, ownerActor(), opportunity.ID, question.ID,
			AnswerValueSaveInput{Text: "because"}); err != nil {
			t.Fatal(err)
		}
	}
	set, err = s.ArtifactReadiness(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	cv = readinessEntry(set, ArtifactCV)
	if !cv.Required || cv.State != ArtifactStateReady || cv.Current == nil || cv.Current.Content != "cv text" {
		t.Fatalf("cv ready: %+v", cv)
	}
	forms = readinessEntry(set, ArtifactFormValues)
	if !forms.Required || forms.State != ArtifactStateReady {
		t.Fatalf("forms ready: %+v", forms)
	}
}

func TestArtifactReadinessKeywordSources(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	newRole := func(key string, mutate func(*CheckSaveInput)) Opportunity {
		opportunity := selectFixtureOpportunity(t, s, company.ID, "select-"+key)
		saveReadyCheck(t, s, opportunity, key, mutate)
		return opportunity
	}
	letter := newRole("letter", func(save *CheckSaveInput) {
		save.RequestedDocuments = []RequestedDocumentInput{{Label: "motivatiebrief", Required: true,
			SourceExcerpt: "Stuur je motivatiebrief.", SourceSpan: save.RequestedDocuments[0].SourceSpan}}
		save.Requirements = []CheckRequirementInput{{Statement: "Show motivation for the rota.",
			SourceExcerpt: "Show motivation for the rota.", SourceSpan: save.Requirements[0].SourceSpan}}
		save.Route.DestinationText = "https://harbour.example/apply"
	})
	set, err := s.ArtifactReadiness(ctx, letter.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry := readinessEntry(set, ArtifactCoverLetter)
	if !entry.Required || entry.State != ArtifactStateHeld || entry.Basis == "" {
		t.Fatalf("dutch letter: %+v", entry)
	}
	if entry := readinessEntry(set, ArtifactEmailSubject); entry.Required || entry.State != ArtifactStateNotRequired {
		t.Fatalf("portal subject: %+v", entry)
	}
	if entry := readinessEntry(set, ArtifactFormValues); !entry.Required || entry.State != ArtifactStateHeld {
		t.Fatalf("letter forms: %+v", entry)
	}
	// Bare "motivation" prose must not conjure a letter on its own.
	prose := newRole("prose", func(save *CheckSaveInput) {
		save.RequestedDocuments = []RequestedDocumentInput{}
		save.Requirements = []CheckRequirementInput{{Statement: "Show motivation for the rota.",
			SourceExcerpt: "Show motivation for the rota.", SourceSpan: save.Requirements[0].SourceSpan}}
		save.Route.DestinationText = "https://harbour.example/apply"
	})
	set, err = s.ArtifactReadiness(ctx, prose.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry := readinessEntry(set, ArtifactCoverLetter); entry.Required || entry.State != ArtifactStateNotRequired {
		t.Fatalf("bare prose letter: %+v", entry)
	}
	if entry := readinessEntry(set, ArtifactCV); entry.Required || entry.State != ArtifactStateNotRequired {
		t.Fatalf("bare prose cv: %+v", entry)
	}
}

func TestEvaluateApplicationRouteQuestionlessAndAttachment(t *testing.T) {
	check := &CheckView{ID: "check-1",
		Route: CheckRouteView{Kind: CheckRouteDirect, Judgment: CheckRouteJudgmentApplication,
			DestinationText: "https://harbour.example/apply"}}
	entries := evaluateApplicationRoute(check, map[string]ArtifactView{}, map[string]QuestionAnswerValue{})
	set := ArtifactReadinessSet{Entries: entries}
	forms := readinessEntry(set, ArtifactFormValues)
	if forms.Required || forms.State != ArtifactStateNotRequired || len(forms.FormValues) != 0 {
		t.Fatalf("questionless forms: %+v", forms)
	}
	check.Questions = []CheckQuestionView{{ID: "q1", Text: "Upload your curriculum vitae.",
		Required: CheckRequired, Kind: CheckQuestionAttachment}}
	entries = evaluateApplicationRoute(check,
		map[string]ArtifactView{ArtifactCoverLetter: {ID: "stale", Type: ArtifactCoverLetter, Version: 1}},
		map[string]QuestionAnswerValue{})
	set = ArtifactReadinessSet{Entries: entries}
	cv := readinessEntry(set, ArtifactCV)
	if !cv.Required || cv.State != ArtifactStateHeld || cv.Basis == "" {
		t.Fatalf("attachment cv: %+v", cv)
	}
	letter := readinessEntry(set, ArtifactCoverLetter)
	if letter.Required || letter.State != ArtifactStateReady || letter.Current == nil {
		t.Fatalf("kept letter: %+v", letter)
	}
}

func TestArtifactReadinessUnverifiedRoutes(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	unchecked := selectFixtureOpportunity(t, s, company.ID, "select-unchecked")
	set, err := s.ArtifactReadiness(ctx, unchecked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if set.CheckStatus != CheckOverallNotChecked {
		t.Fatalf("status: %s", set.CheckStatus)
	}
	for _, entry := range set.Entries {
		if entry.Required || entry.State != ArtifactStateUnresolved {
			t.Fatalf("unchecked %s: %+v", entry.Type, entry)
		}
	}
	unresolved := selectFixtureOpportunity(t, s, company.ID, "select-unresolved")
	saveReadyCheck(t, s, unresolved, "unresolved", func(save *CheckSaveInput) {
		save.Route = CheckRouteInput{Judgment: CheckRouteJudgmentUnresolved,
			SourceExcerpt: "No application address found.", ObservedAt: "2026-09-24T11:30:00Z"}
	})
	set, err = s.ArtifactReadiness(ctx, unresolved.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range set.Entries {
		if entry.Required || entry.State != ArtifactStateUnresolved {
			t.Fatalf("unresolved %s: %+v", entry.Type, entry)
		}
	}
	other := selectFixtureOpportunity(t, s, company.ID, "select-other")
	saveReadyCheck(t, s, other, "other", func(save *CheckSaveInput) {
		save.Route = CheckRouteInput{Judgment: CheckRouteJudgmentOther,
			SourceExcerpt: "A press contact address.", ObservedAt: "2026-09-24T11:30:00Z"}
	})
	set, err = s.ArtifactReadiness(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range set.Entries {
		if entry.Required || entry.State != ArtifactStateNotRequired {
			t.Fatalf("other %s: %+v", entry.Type, entry)
		}
	}
}
