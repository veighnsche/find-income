package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/replydraft"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func replyIntentServer(t *testing.T, choice string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		probabilities := map[string]float64{}
		for id := range wire.Questions["reply_intent"].Criteria {
			probabilities[id] = 0
		}
		if _, ok := probabilities[choice]; !ok {
			t.Errorf("choice %q not offered", choice)
			return
		}
		probabilities[choice] = 1
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"reply_intent": map[string]any{"type": "choice", "choice": choice, "confidence": 0.9, "probabilities": probabilities}}, "usage": map[string]any{"input_tokens": 6, "output_tokens": 2}})
	}))
}

func replyFixture(t *testing.T) (*store.Store, store.Actor, store.CorrespondenceThread, store.Opportunity, store.Preferences) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "reply-owner"}
	company, _, err := s.CreateCompany(ctx, owner, store.CompanyInput{Name: "Reply Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := s.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment", SourceURL: "https://example.test/reply", OriginalText: "Build Go services.", Stage: "new", WorkPattern: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := s.ConnectCorrespondenceAccount(ctx, owner, store.CorrespondenceAccountInput{Provider: "fake", ExternalAccountID: "owner@example.test", DisplayName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SyncCorrespondenceThreads(ctx, owner, account.ID, []store.CorrespondenceThreadSnapshot{{ProviderThreadID: "thread-1", Subject: "Interview", LastMessageAt: "2026-09-24T10:00:00Z",
		Messages: []store.CorrespondenceMessageSnapshot{{ProviderMessageID: "m-1", Sender: "recruiter@example.test", Recipients: []string{"owner@example.test"}, SentAt: "2026-09-24T10:00:00Z", Body: "We invite you to interview Tuesday."}}}})
	if err != nil {
		t.Fatal(err)
	}
	threads, err := s.OwnerCorrespondenceThreads(ctx, owner.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads: %+v %v", threads, err)
	}
	return s, owner, threads[0], opportunity, profile
}

func replyIntentInput(thread store.CorrespondenceThread, body, sha string) jev.ReplyIntentInput {
	return jev.ReplyIntentInput{ThreadID: thread.ID, MaxReportedTokens: 100,
		Context: []jev.ReplyIntentContext{{ID: "m-1", Kind: "inbound", Revision: "provider-m-1", Body: body, SHA256: sha}},
		Evidence: []jev.ReplyIntentEvidence{
			{ID: "invite", SourceID: "m-1", SourceRevision: "provider-m-1", SourceKind: "inbound_message", SourceSHA256: sha, Excerpt: "invite you to interview"},
		},
		Candidates: []jev.ReplyIntentCandidate{
			{ID: "interview_invitation", Description: "The sender invites the owner to an interview.", EvidenceIDs: []string{"invite"}},
			{ID: "not_actionable", Description: "The message needs no owner action.", EvidenceIDs: []string{"invite"}},
		},
	}
}

func TestReplyCommissionBindIntentUpdateAndDraft(t *testing.T) {
	ctx := context.Background()
	s, owner, thread, opportunity, profile := replyFixture(t)
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	processing, created, err := s.CommissionReplyProcessing(ctx, owner, store.ReplyProcessingCommissionInput{RequestKey: "replies-1", ThreadID: thread.ID})
	if err != nil || !created {
		t.Fatalf("commission: %v %v", created, err)
	}
	replay, created, err := s.CommissionReplyProcessing(ctx, owner, store.ReplyProcessingCommissionInput{RequestKey: "replies-1", ThreadID: thread.ID})
	if err != nil || created || replay.ID != processing.ID {
		t.Fatalf("replay: %+v %v %v", replay, created, err)
	}
	if _, _, err := s.CommissionReplyProcessing(ctx, owner, store.ReplyProcessingCommissionInput{RequestKey: "replies-1", ThreadID: "other"}); !errors.Is(err, store.ErrRoundIdempotencyConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	resource := "thread:" + thread.ID
	round, _, err := s.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "replies-1", Intent: "Process one thread.", Outcome: "process_replies", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{InputRefs: []string{"replies:" + processing.ID}, Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundJevRequest, store.RoundReplyUpdateSave, store.RoundReplyDraftSave}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 5, Items: 3, Tools: 6, Turns: 1}, Deadline: time.Now().Add(time.Hour).UTC().Round(0)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = s.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindReplyRound(ctx, owner, processing.ID, round.ID); err != nil {
		t.Fatal(err)
	}
	messages, err := s.CorrespondenceThreadMessages(ctx, owner.ID, thread.ID)
	if err != nil || len(messages) != 1 {
		t.Fatal(err)
	}
	server := replyIntentServer(t, "interview_invitation")
	defer server.Close()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-key")
	cfg := jev.DefaultConfig()
	cfg.Enabled, cfg.Endpoint, cfg.Timeout, cfg.MaxAttempts = true, server.URL+"/v1/systemone", time.Second, 1
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := jevservice.Service{Store: s, Client: client}
	decided, err := service.RunReplyIntent(ctx, jevservice.Binding{Actor: agent, RoundID: round.ID, ResourceID: resource, RequestKeyPrefix: "reply-intent:" + processing.ID, ProfileVersion: profile.Version, MaxReportedTokens: 100}, replyIntentInput(thread, messages[0].Body, messages[0].BodySHA256))
	if err != nil || decided.Disposition != jev.ReplyIntentSelected || decided.SelectedID != "interview_invitation" {
		t.Fatalf("intent=%+v err=%v", decided, err)
	}
	ids, err := s.JevAttemptIDsForRequestPrefix(ctx, round.ID, "reply-intent:"+processing.ID)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%+v err=%v", ids, err)
	}
	processing, err = s.ApplyReplyIntent(ctx, agent, round.ID, round.Generation, processing.ID, ids[0], "interview_invitation")
	if err != nil || processing.Intent != "interview_invitation" || processing.ProcessedAt == "" {
		t.Fatalf("apply=%+v err=%v", processing, err)
	}
	if _, err := s.ApplyReplyIntent(ctx, agent, round.ID, round.Generation, processing.ID, ids[0], "rejection"); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("second intent: %v", err)
	}
	turn, _, err := s.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "replies-turn", Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := s.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, created, err = s.ApplyRoundMutation(ctx, agent, round.ID, store.RoundMutationInput{RequestKey: "replies-1/update", Operation: store.RoundReplyUpdateSave, ResourceID: resource, ExpectedRevision: opportunity.Revision,
		ReplyUpdate: &store.ReplyUpdateMutation{ProcessingID: processing.ID, ThreadID: thread.ID, OpportunityID: opportunity.ID}, Capability: capability})
	if err != nil || !created {
		t.Fatalf("update: %v %v", created, err)
	}
	thread, err = s.OwnerCorrespondenceThread(ctx, owner.ID, thread.ID)
	if err != nil || thread.OpportunityID != opportunity.ID {
		t.Fatalf("thread link: %+v %v", thread, err)
	}
	draft, err := replydraft.ValidateDraft(replydraft.DraftInput{ProcessingID: processing.ID, ThreadID: thread.ID, Body: "Thank you for the Tuesday invitation.", Citations: []replydraft.DraftCitation{{MessageID: messages[0].ID, Excerpt: "interview Tuesday"}}, Unknowns: []string{"Interview format unknown."}}, map[string]string{messages[0].ID: messages[0].Body})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(draft)
	_, created, err = s.ApplyRoundMutation(ctx, agent, round.ID, store.RoundMutationInput{RequestKey: "replies-1/draft", Operation: store.RoundReplyDraftSave, ResourceID: resource, ExpectedRevision: 1,
		ReplyDraft: &store.ReplyDraftMutation{ProcessingID: processing.ID, ThreadID: thread.ID, DraftJSON: encoded}, Capability: capability})
	if err != nil || !created {
		t.Fatalf("draft: %v %v", created, err)
	}
	saved, err := s.ReplyDraftForProcessing(ctx, processing.ID)
	if err != nil || saved.Revision != 1 || saved.RoundID != round.ID {
		t.Fatalf("saved draft: %+v %v", saved, err)
	}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, round.ID, store.RoundMutationInput{RequestKey: "replies-1/draft-again", Operation: store.RoundReplyDraftSave, ResourceID: resource, ExpectedRevision: 1,
		ReplyDraft: &store.ReplyDraftMutation{ProcessingID: processing.ID, ThreadID: thread.ID, DraftJSON: encoded}, Capability: capability}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second draft: %v", err)
	}
}

func TestRecoverCapturedReplyIntentRejectsBadState(t *testing.T) {
	ctx := context.Background()
	s, owner, thread, _, profile := replyFixture(t)
	if _, err := s.RecoverCapturedReplyIntentAttempt(ctx, "", "a", "j", 1); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty round: %v", err)
	}
	if _, err := s.RecoverCapturedReplyIntentAttempt(ctx, "missing", "a", "j", 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing round: %v", err)
	}
	round, _, err := s.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "guard", Intent: "Guard.", Outcome: "process_input", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{InputRefs: []string{"campaign:test"}, Resources: []string{"campaign:active"}, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: 1}, Deadline: time.Now().Add(time.Hour).UTC().Round(0)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = s.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = thread
	if _, err := s.RecoverCapturedReplyIntentAttempt(ctx, round.ID, "a", "j", round.Generation); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("wrong outcome: %v", err)
	}
}
