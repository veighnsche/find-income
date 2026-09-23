package jev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func interviewFocusFixture() InterviewFocusInput {
	invitation := "The interview will discuss Go services and how the team collaborates."
	invitationSHA := sha256.Sum256([]byte(invitation))
	return InterviewFocusInput{InterviewID: "interview-one", Role: "Platform Engineer", Employer: "Fixture Employer", MaxReportedTokens: 100,
		Context: []InterviewFocusContext{{ID: "invitation", Kind: "invitation", Revision: "owner-paste-1",
			Body: invitation, SHA256: hex.EncodeToString(invitationSHA[:])}},
		Evidence: []InterviewFocusEvidence{
			{ID: "role", SourceID: "role-one", SourceRevision: "2", SourceKind: "saved_role", SourceSHA256: strings.Repeat("a", 64), Excerpt: "Build Go services and Linux tools."},
			{ID: "project", SourceID: "career-one", SourceRevision: "approved", SourceKind: "personal_project", SourceSHA256: strings.Repeat("b", 64), Excerpt: "The personal project uses a Go service."},
		},
		Candidates: []InterviewFocusCandidate{
			{ID: "service-design", Description: "Discuss the cited personal Go service design as a project example, with its limits.", EvidenceIDs: []string{"role", "project"}},
			{ID: "team-process", Description: "Ask how this team reviews and operates its services.", EvidenceIDs: []string{"role"}},
		},
	}
}

func TestInterviewFocusSelectsGroundedOptionOrAbstains(t *testing.T) {
	input := interviewFocusFixture()
	fake := screenFake(func(request Request) (Result, error) {
		question := request.Questions[interviewFocusQuestionID].(ChoiceQuestion)
		if len(question.Criteria) != 3 || question.Criteria[interviewFocusUnresolved] == "" {
			t.Fatal("interview focus options or abstention omitted")
		}
		state := request.State.(InterviewFocusInput)
		if len(state.Context) != 1 || state.Context[0].Body != input.Context[0].Body {
			t.Fatal("complete interview context omitted from Jev state")
		}
		return screenResult(request, map[string]string{interviewFocusQuestionID: "service-design"}), nil
	})
	selected, err := SelectInterviewFocus(context.Background(), fake, input)
	if err != nil || selected.Disposition != InterviewFocusSelected || selected.SelectedID != "service-design" || selected.InputSHA256 == "" || len(selected.RequestSnapshot) == 0 {
		t.Fatalf("selection=%+v err=%v", selected, err)
	}
	abstain := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{interviewFocusQuestionID: interviewFocusUnresolved}), nil
	})
	unresolved, err := SelectInterviewFocus(context.Background(), abstain, input)
	if err != nil || unresolved.Disposition != InterviewFocusUnresolved || unresolved.SelectedID != "" {
		t.Fatalf("abstention=%+v err=%v", unresolved, err)
	}
}

func TestInterviewFocusRejectsUnsupportedChoiceAndUngroundedOption(t *testing.T) {
	input := interviewFocusFixture()
	bad := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{interviewFocusQuestionID: "invented-focus"}), nil
	})
	if _, err := SelectInterviewFocus(context.Background(), bad, input); !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("invented choice accepted: %v", err)
	}
	input.Candidates[0].EvidenceIDs = []string{"absent"}
	if _, err := SelectInterviewFocus(context.Background(), bad, input); !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("ungrounded candidate accepted: %v", err)
	}
}
