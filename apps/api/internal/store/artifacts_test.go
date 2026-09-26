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
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
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
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
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
