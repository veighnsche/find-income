package store

import (
	"context"
	"errors"
	"testing"
)

func TestCommitRoleAnswersStages(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-commit")
	saveReadyCheck(t, s, opportunity, "commit", nil)
	workflow, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil || workflow.Stage != RoleStageChecked {
		t.Fatalf("checked stage: %+v err=%v", workflow, err)
	}
	check, err := s.CurrentJobCheck(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	var requiredID string
	for _, question := range check.Check.Questions {
		if question.Required == CheckRequired {
			requiredID = question.ID
		}
	}
	if _, _, err := s.CommitRoleAnswers(ctx, ownerActor(), opportunity.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("empty commit: %v", err)
	}
	if _, err := s.SaveAnswerValue(ctx, ownerActor(), opportunity.ID, requiredID,
		AnswerValueSaveInput{Text: "because"}); err != nil {
		t.Fatal(err)
	}
	workflow, err = s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil || workflow.Stage != RoleStageAnswering {
		t.Fatalf("answering stage: %+v err=%v", workflow, err)
	}
	committed, missing, err := s.CommitRoleAnswers(ctx, ownerActor(), opportunity.ID)
	if err != nil || len(missing) != 0 || committed.Stage != RoleStageAnswered {
		t.Fatalf("commit: %+v missing=%v err=%v", committed, missing, err)
	}
	replay, missing, err := s.CommitRoleAnswers(ctx, ownerActor(), opportunity.ID)
	if err != nil || len(missing) != 0 || replay.Revision != committed.Revision {
		t.Fatalf("replay: %+v missing=%v err=%v", replay, missing, err)
	}
	value, err := s.CurrentQuestionAnswers(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	for _, answer := range value.Values {
		if answer.QuestionID == requiredID {
			version = answer.Version
		}
	}
	if _, err := s.SaveAnswerValue(ctx, ownerActor(), opportunity.ID, requiredID,
		AnswerValueSaveInput{ExpectedAnswerVersion: version, Text: ""}); err != nil {
		t.Fatal(err)
	}
	if _, missing, err := s.CommitRoleAnswers(ctx, ownerActor(), opportunity.ID); !errors.Is(err, ErrConflict) ||
		len(missing) != 1 || missing[0] != requiredID {
		t.Fatalf("blanked commit: missing=%v err=%v", missing, err)
	}
}

func TestCommitRoleAnswersGuards(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	unchecked := selectFixtureOpportunity(t, s, company.ID, "select-commit-guard")
	if _, _, err := s.CommitRoleAnswers(ctx, ownerActor(), unchecked.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unchecked: %v", err)
	}
	unselectedInput := fixtureOpportunity(company.ID)
	unselectedInput.SourceURL = "https://harbour.example/jobs/commit-unselected"
	unselected, _, err := s.CreateOpportunity(ctx, ownerActor(), unselectedInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CommitRoleAnswers(ctx, ownerActor(), unselected.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected: %v", err)
	}
	optional := selectFixtureOpportunity(t, s, company.ID, "select-commit-optional")
	saveReadyCheck(t, s, optional, "commit-optional", func(save *CheckSaveInput) {
		for i := range save.Questions {
			save.Questions[i].Required = CheckOptional
		}
	})
	committed, missing, err := s.CommitRoleAnswers(ctx, ownerActor(), optional.ID)
	if err != nil || len(missing) != 0 || committed.Stage != RoleStageAnswered {
		t.Fatalf("optional commit: %+v missing=%v err=%v", committed, missing, err)
	}
}
