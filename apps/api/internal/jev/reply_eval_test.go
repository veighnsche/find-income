package jev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

type replyEvalCase struct {
	name     string
	sender   string
	body     string
	expected string
}

// replyEvalCases are fixed supplied-evidence cases for the reply_intent
// class. The scripted evaluator below proves harness mechanics only; provider
// quality is established by a live run, queued as an end blocker.
var replyEvalCases = []replyEvalCase{
	{name: "invitation", sender: "recruiter@example.test", body: "We would like to invite you to interview for the backend role next Tuesday.", expected: "interview_invitation"},
	{name: "scheduling", sender: "recruiter@example.test", body: "Tuesday no longer works; are you available Thursday afternoon instead?", expected: "scheduling_exchange"},
	{name: "info-request", sender: "hiring@example.test", body: "Could you share two references and your earliest start date?", expected: "information_request"},
	{name: "offer", sender: "hiring@example.test", body: "We offer 5200 gross monthly for 32 hours; the details are attached.", expected: "offer_terms"},
	{name: "rejection", sender: "recruiter@example.test", body: "We decided to proceed with another candidate for this opening.", expected: "rejection"},
	{name: "referral", sender: "peer@example.test", body: "Let me introduce you to our hiring manager about the platform opening.", expected: "referral_introduction"},
	{name: "nudge", sender: "recruiter@example.test", body: "Just checking whether you saw my earlier message about the role.", expected: "follow_up_nudge"},
	{name: "newsletter", sender: "news@example.test", body: "This week's top backend roles across the region.", expected: "not_actionable"},
}

func replyEvalInput(c replyEvalCase) ReplyIntentInput {
	sum := sha256.Sum256([]byte(c.body))
	sha := hex.EncodeToString(sum[:])
	input := ReplyIntentInput{ThreadID: "eval-" + c.name, MaxReportedTokens: 100,
		Context:  []ReplyIntentContext{{ID: "m-1", Kind: "inbound", Revision: "eval-m-1", Body: c.body, SHA256: sha}},
		Evidence: []ReplyIntentEvidence{{ID: "excerpt", SourceID: "m-1", SourceRevision: "eval-m-1", SourceKind: "inbound_message", SourceSHA256: sha, Excerpt: c.body}},
	}
	for _, id := range ReplyIntentTaxonomy {
		input.Candidates = append(input.Candidates, ReplyIntentCandidate{ID: id, Description: "Supplied " + id + " option.", EvidenceIDs: []string{"excerpt"}})
	}
	return input
}

func TestReplyIntentEvalHarnessSelectsExpectedCases(t *testing.T) {
	script := map[string]string{}
	for _, c := range replyEvalCases {
		script["eval-"+c.name] = c.expected
	}
	fake := screenFake(func(request Request) (Result, error) {
		state := request.State.(ReplyIntentInput)
		want, ok := script[state.ThreadID]
		if !ok {
			t.Fatalf("unexpected eval thread %q", state.ThreadID)
		}
		if len(state.Candidates) != len(ReplyIntentTaxonomy) {
			t.Fatalf("taxonomy drift: %d candidates", len(state.Candidates))
		}
		return screenResult(request, map[string]string{replyIntentQuestionID: want}), nil
	})
	for _, c := range replyEvalCases {
		result, err := SelectReplyIntent(context.Background(), fake, replyEvalInput(c))
		if err != nil || result.Disposition != ReplyIntentSelected || result.SelectedID != c.expected {
			t.Fatalf("case %s: %+v %v", c.name, result, err)
		}
	}
}
