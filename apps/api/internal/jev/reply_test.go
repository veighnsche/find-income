package jev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func replyIntentFixture() ReplyIntentInput {
	inbound := "We would like to invite you for an interview next Tuesday morning."
	inboundSHA := sha256.Sum256([]byte(inbound))
	return ReplyIntentInput{ThreadID: "thread-one", MaxReportedTokens: 100,
		Context: []ReplyIntentContext{{ID: "m-1", Kind: "inbound", Revision: "provider-m-1",
			Body: inbound, SHA256: hex.EncodeToString(inboundSHA[:])}},
		Evidence: []ReplyIntentEvidence{
			{ID: "invite", SourceID: "m-1", SourceRevision: "provider-m-1", SourceKind: "inbound_message", SourceSHA256: strings.Repeat("c", 64), Excerpt: "invite you for an interview next Tuesday"},
		},
		Candidates: []ReplyIntentCandidate{
			{ID: "interview_invitation", Description: "The sender invites the owner to an interview.", EvidenceIDs: []string{"invite"}},
			{ID: "not_actionable", Description: "The message needs no owner action.", EvidenceIDs: []string{"invite"}},
		},
	}
}

func replyWireBytes(t *testing.T, input ReplyIntentInput, choice string) []byte {
	t.Helper()
	canonical, err := canonicalReplyIntent(input)
	if err != nil {
		t.Fatal(err)
	}
	question := replyIntentRequest(canonical).Questions[replyIntentQuestionID].(ChoiceQuestion)
	probabilities := map[string]float64{}
	for option := range question.Criteria {
		probabilities[option] = 0
	}
	probabilities[choice] = 1
	answer, err := json.Marshal(map[string]any{"type": "choice", "choice": choice, "probabilities": probabilities, "confidence": 0.8})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": map[string]json.RawMessage{replyIntentQuestionID: answer}, "usage": map[string]any{"input_tokens": 5, "output_tokens": 2}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReplyIntentSelectsGroundedOptionOrAbstains(t *testing.T) {
	input := replyIntentFixture()
	fake := screenFake(func(request Request) (Result, error) {
		question := request.Questions[replyIntentQuestionID].(ChoiceQuestion)
		if len(question.Criteria) != 3 || question.Criteria[replyIntentUnresolved] == "" {
			t.Fatal("reply intent options or abstention omitted")
		}
		state := request.State.(ReplyIntentInput)
		if len(state.Context) != 1 || state.Context[0].Body != input.Context[0].Body {
			t.Fatal("complete thread context omitted from Jev state")
		}
		return screenResult(request, map[string]string{replyIntentQuestionID: "interview_invitation"}), nil
	})
	selected, err := SelectReplyIntent(context.Background(), fake, input)
	if err != nil || selected.Disposition != ReplyIntentSelected || selected.SelectedID != "interview_invitation" || selected.InputSHA256 == "" || len(selected.RequestSnapshot) == 0 {
		t.Fatalf("selection=%+v err=%v", selected, err)
	}
	abstain := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{replyIntentQuestionID: replyIntentUnresolved}), nil
	})
	unresolved, err := SelectReplyIntent(context.Background(), abstain, input)
	if err != nil || unresolved.Disposition != ReplyIntentUnresolved || unresolved.SelectedID != "" {
		t.Fatalf("abstention=%+v err=%v", unresolved, err)
	}
}

func TestReplyIntentRejectsInventedChoiceUngroundedAndOffTaxonomyOption(t *testing.T) {
	input := replyIntentFixture()
	bad := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{replyIntentQuestionID: "invented-intent"}), nil
	})
	if _, err := SelectReplyIntent(context.Background(), bad, input); !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("invented choice accepted: %v", err)
	}
	grounded := input
	grounded.Candidates[0].EvidenceIDs = []string{"absent"}
	if _, err := SelectReplyIntent(context.Background(), bad, grounded); !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("ungrounded candidate accepted: %v", err)
	}
	offTaxonomy := input
	offTaxonomy.Candidates[0].ID = "plausible_but_unlisted"
	if _, err := SelectReplyIntent(context.Background(), bad, offTaxonomy); !errors.Is(err, &Error{Kind: ErrInvalidRequest}) {
		t.Fatalf("off-taxonomy candidate accepted: %v", err)
	}
}

func TestReplyIntentRecoversOnlyExactCapture(t *testing.T) {
	input := replyIntentFixture()
	fake := screenFake(func(request Request) (Result, error) {
		return screenResult(request, map[string]string{replyIntentQuestionID: "interview_invitation"}), nil
	})
	selected, err := SelectReplyIntent(context.Background(), fake, input)
	if err != nil {
		t.Fatal(err)
	}
	raw := replyWireBytes(t, input, "interview_invitation")
	recovered, err := RecoverCapturedReplyIntent(input, selected.RequestSnapshot, raw, "jev-1.13.0")
	if err != nil || recovered.Disposition != ReplyIntentSelected || recovered.SelectedID != "interview_invitation" ||
		recovered.ProviderResult.Usage.InputTokens != 5 || recovered.ProviderResult.Usage.OutputTokens != 2 {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	tampered := append([]byte(nil), selected.RequestSnapshot...)
	tampered[len(tampered)-2] ^= 0xff
	if _, err := RecoverCapturedReplyIntent(input, tampered, raw, "jev-1.13.0"); !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("tampered logical request accepted: %v", err)
	}
	wrongChoice := replyWireBytes(t, input, "rejection")
	if _, err := RecoverCapturedReplyIntent(input, selected.RequestSnapshot, wrongChoice, "jev-1.13.0"); !errors.Is(err, &Error{Kind: ErrInvalidResponse}) {
		t.Fatalf("off-candidate response accepted: %v", err)
	}
}
