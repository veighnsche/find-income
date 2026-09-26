package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSaveOpportunityArtifactVersionsAndReplay(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-artifact-replay")
	basis := ArtifactBasis{FactIDs: []string{"fact-1"},
		AnswerRefs: []ArtifactAnswerRef{{QuestionID: "q1", AnswerVersion: 2}},
		CheckSpans: []CheckSourceSpan{{CaptureID: "cap-1", Start: 0, End: 4}}}
	first, created, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "art-1", Type: ArtifactCV, Content: "cv v1", Basis: basis})
	if err != nil || !created || first.Version != 1 {
		t.Fatalf("first save: %+v created=%v err=%v", first, created, err)
	}
	replay, created, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "art-1", Type: ArtifactCV, Content: "cv v1", Basis: basis})
	if err != nil || created || replay.ID != first.ID || replay.Version != 1 {
		t.Fatalf("replay: %+v created=%v err=%v", replay, created, err)
	}
	second, created, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "art-2", ExpectedVersion: 1, Type: ArtifactCV, Content: "cv v2"})
	if err != nil || !created || second.Version != 2 {
		t.Fatalf("second save: %+v created=%v err=%v", second, created, err)
	}
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "art-3", ExpectedVersion: 1, Type: ArtifactCV, Content: "fork"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale fence: %v", err)
	}
	current, err := s.GetOpportunityArtifact(ctx, opportunity.ID, ArtifactCV)
	if err != nil || current.Version != 2 || current.Content != "cv v2" {
		t.Fatalf("current: %+v err=%v", current, err)
	}
	if _, err := s.GetOpportunityArtifact(ctx, opportunity.ID, ArtifactCoverLetter); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent artifact: %v", err)
	}
	listed, err := s.ListOpportunityArtifacts(ctx, opportunity.ID)
	if err != nil || len(listed) != 1 || listed[0].Version != 2 {
		t.Fatalf("list: %+v err=%v", listed, err)
	}
}

func TestSaveOpportunityArtifactRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-artifact-invalid")
	valid := ArtifactSaveInput{RequestKey: "ok", Type: ArtifactEmailBody, Content: "hello"}
	cases := []ArtifactSaveInput{
		{RequestKey: "", Type: ArtifactEmailBody, Content: "hello"},
		{RequestKey: " bad", Type: ArtifactEmailBody, Content: "hello"},
		{RequestKey: "form", Type: ArtifactFormValues, Content: "derived"},
		{RequestKey: "unknown", Type: "portfolio", Content: "hello"},
		{RequestKey: "empty", Type: ArtifactEmailBody, Content: "  "},
		{RequestKey: "huge", Type: ArtifactEmailBody, Content: strings.Repeat("x", artifactMaxContent+1)},
		{RequestKey: "badbasis", Type: ArtifactEmailBody, Content: "hello", Basis: ArtifactBasis{FactIDs: []string{" "}}},
	}
	for _, input := range cases {
		if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("input %+v: %v", input, err)
		}
	}
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), "missing", valid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing role: %v", err)
	}
}

func TestSaveOpportunityArtifactRequiresSelection(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "unselected", Type: ArtifactCV, Content: "cv"}); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected write: %v", err)
	}
}

func TestSaveOpportunityArtifactChangedPayloadConflicts(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-artifact-r23")
	basis := ArtifactBasis{FactIDs: []string{"fact-1"},
		AnswerRefs: []ArtifactAnswerRef{{QuestionID: "q1", AnswerVersion: 1}}}
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "r23", Type: ArtifactCV, Content: "cv v1", Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "r23", Type: ArtifactCV, Content: "cv changed", Basis: basis}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed content replay: %v", err)
	}
	moved := ArtifactBasis{FactIDs: []string{"fact-1"},
		AnswerRefs: []ArtifactAnswerRef{{QuestionID: "q1", AnswerVersion: 2}}}
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "r23", ExpectedVersion: 1, Type: ArtifactCV, Content: "cv v1", Basis: moved}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed basis replay: %v", err)
	}
	replay, created, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "r23", ExpectedVersion: 0, Type: ArtifactCV, Content: "cv v1", Basis: basis})
	if err != nil || created || replay.Version != 1 {
		t.Fatalf("identical replay: %+v created=%v err=%v", replay, created, err)
	}
}

func TestListArtifactVersionsKeepsHistory(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-artifact-history")
	for i, content := range []string{"cv v1", "cv v2"} {
		if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
			RequestKey: "hist-" + string(rune('a'+i)), ExpectedVersion: int64(i),
			Type: ArtifactCV, Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	views, err := s.ListArtifactVersions(ctx, opportunity.ID, ArtifactCV)
	if err != nil || len(views) != 2 || views[0].Version != 1 || views[1].Version != 2 ||
		views[0].Content != "cv v1" || views[1].Content != "cv v2" {
		t.Fatalf("versions: %+v err=%v", views, err)
	}
	empty, err := s.ListArtifactVersions(ctx, opportunity.ID, ArtifactCoverLetter)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty: %+v err=%v", empty, err)
	}
}

func TestListSavedJobsIndexesPartialWork(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	full := selectFixtureOpportunity(t, s, company.ID, "select-index-full")
	saveReadyCheck(t, s, full, "index-full", nil)
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), full.ID, ArtifactSaveInput{
		RequestKey: "index-cv", Type: ArtifactCV, Content: "cv v1"}); err != nil {
		t.Fatal(err)
	}
	partial := selectFixtureOpportunity(t, s, company.ID, "select-index-partial")
	saveReadyCheck(t, s, partial, "index-partial", nil)
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), partial.ID, ArtifactSaveInput{
		RequestKey: "index-subject", Type: ArtifactEmailSubject, Content: "subject v1"}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListSavedJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SavedJobEntry{}
	for _, entry := range entries {
		byID[entry.OpportunityID] = entry
	}
	got, ok := byID[full.ID]
	if !ok || got.CheckStatus != CheckOverallChecked || got.CompanyName == "" || got.Title == "" {
		t.Fatalf("full: %+v", got)
	}
	seen := map[string]SavedJobItem{}
	for _, item := range got.Items {
		seen[item.Type] = item
	}
	if seen[ArtifactCV].Version != 1 || seen[ArtifactCV].State != ArtifactStateReady {
		t.Fatalf("full cv: %+v", seen[ArtifactCV])
	}
	if seen[ArtifactEmailBody].State != ArtifactStateHeld || seen[ArtifactEmailBody].Version != 0 {
		t.Fatalf("full body: %+v", seen[ArtifactEmailBody])
	}
	held, ok := byID[partial.ID]
	if !ok {
		t.Fatalf("partial job missing: %+v", entries)
	}
	if len(entries) != 2 || entries[0].OpportunityID != partial.ID {
		t.Fatalf("recency order: %+v", entries)
	}
	_ = held
}

func TestHandoffProjectionCarriesRouteAndUploads(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-handoff")
	saveReadyCheck(t, s, opportunity, "handoff", func(save *CheckSaveInput) {
		save.Questions = append(save.Questions, CheckQuestionInput{
			Text: "Upload your curriculum vitae.", Required: CheckRequired, Kind: CheckQuestionAttachment,
			SourceSpan:    CheckSourceSpan{CaptureID: save.Vacancy.CaptureIDs[0], Start: 400, End: 429},
			SourceExcerpt: "Upload your curriculum vitae."})
	})
	if _, _, err := s.SaveOpportunityArtifact(ctx, ownerActor(), opportunity.ID, ArtifactSaveInput{
		RequestKey: "handoff-cv", Type: ArtifactCV, Content: "cv v1"}); err != nil {
		t.Fatal(err)
	}
	view, err := s.HandoffProjection(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.CheckStatus != CheckOverallChecked || view.WorkflowStage == "" ||
		view.RouteDestination != "jobs@example.invalid" || view.CompanyName == "" {
		t.Fatalf("handoff: %+v", view)
	}
	var cv *HandoffItem
	for i := range view.Items {
		if view.Items[i].Type == ArtifactCV {
			cv = &view.Items[i]
		}
	}
	if cv == nil || cv.State != ArtifactStateReady || cv.Content != "cv v1" || cv.ContentSHA256 == "" {
		t.Fatalf("cv item: %+v", view.Items)
	}
	if len(view.Uploads) != 1 || view.Uploads[0].ArtifactType != ArtifactCV ||
		view.Uploads[0].State != ArtifactStateReady || view.Uploads[0].Version != 1 {
		t.Fatalf("uploads: %+v", view.Uploads)
	}
	if _, err := s.HandoffProjection(ctx, "missing"); err == nil {
		t.Fatal("missing role projected")
	}
}
