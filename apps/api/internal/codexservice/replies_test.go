package codexservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/replydraft"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func replyRoundFixture(t *testing.T) (*Service, *store.Store, store.Round, string, store.ReplyProcessing, store.Opportunity) {
	t.Helper()
	ctx := context.Background()
	s, db := testService(t, testConfig())
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Fixture Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment", SourceURL: "https://example.test/reply", OriginalText: "Build Go services.", Stage: "new"})
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := db.ConnectCorrespondenceAccount(ctx, owner, store.CorrespondenceAccountInput{Provider: "fake", ExternalAccountID: "owner@example.test", DisplayName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = db.SyncCorrespondenceThreads(ctx, owner, account.ID, []store.CorrespondenceThreadSnapshot{{ProviderThreadID: "thread-1", Subject: "Interview", LastMessageAt: "2026-09-24T10:00:00Z",
		Messages: []store.CorrespondenceMessageSnapshot{{ProviderMessageID: "m-1", Sender: "recruiter@example.test", Recipients: []string{"owner@example.test"}, SentAt: "2026-09-24T10:00:00Z", Body: "We invite you to interview on Tuesday."}}}})
	if err != nil {
		t.Fatal(err)
	}
	threads, err := db.OwnerCorrespondenceThreads(ctx, owner.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads: %+v %v", threads, err)
	}
	processing, _, err := db.CommissionReplyProcessing(ctx, owner, store.ReplyProcessingCommissionInput{RequestKey: "replies-1", ThreadID: threads[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	resource := "thread:" + threads[0].ID
	r, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "replies-round", Intent: "Process one thread", Outcome: "process_replies", ProfileVersion: processing.ProfileVersion, Deadline: time.Now().Add(time.Hour), Scope: store.RoundScope{InputRefs: []string{"replies:" + processing.ID}, Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundJevRequest, store.RoundReplyUpdateSave, store.RoundReplyDraftSave}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: 5, Items: 3, Tools: 6, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = db.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindReplyRound(ctx, owner, processing.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "replies:" + processing.ID, Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, db, r, capability, processing, opportunity
}

func TestReplyUpdateToolLinksExactOpportunity(t *testing.T) {
	ctx := context.Background()
	s, db, r, capability, processing, opportunity := replyRoundFixture(t)
	args := replyUpdateArgs{RoundID: r.ID, Capability: capability, RequestKey: "update", ProcessingID: processing.ID, OpportunityID: opportunity.ID}
	first, err := s.replyUpdateTool(ctx, args)
	if err != nil || first["created"] != true {
		t.Fatalf("update: %+v %v", first, err)
	}
	thread, err := db.OwnerCorrespondenceThread(ctx, "owner", processing.ThreadID)
	if err != nil || thread.OpportunityID != opportunity.ID {
		t.Fatalf("thread link: %+v %v", thread, err)
	}
	second, err := s.replyUpdateTool(ctx, args)
	if err != nil || second["created"] != false {
		t.Fatalf("replay: %+v %v", second, err)
	}
	other, _, err := db.CreateOpportunity(ctx, store.Actor{Kind: "administrator", ID: "owner"}, store.OpportunityInput{CompanyID: opportunity.CompanyID, Title: "Other", Kind: "employment", SourceURL: "https://example.test/other", OriginalText: "Other role.", Stage: "new"})
	if err != nil {
		t.Fatal(err)
	}
	clash := args
	clash.RequestKey = "update-clash"
	clash.OpportunityID = other.ID
	if _, err := s.replyUpdateTool(ctx, clash); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("relink accepted: %v", err)
	}
}

func TestReplyDraftToolSavesOnlyCitedDraft(t *testing.T) {
	ctx := context.Background()
	s, db, r, capability, processing, _ := replyRoundFixture(t)
	messages, err := db.CorrespondenceThreadMessages(ctx, "owner", processing.ThreadID)
	if err != nil || len(messages) != 1 {
		t.Fatal(err)
	}
	args := replyDraftArgs{RoundID: r.ID, Capability: capability, RequestKey: "draft", ProcessingID: processing.ID,
		Body: "Thank you for the Tuesday invitation.", Citations: []replydraft.DraftCitation{{MessageID: messages[0].ID, Excerpt: "interview on Tuesday"}}, Unknowns: []string{"Format unknown."}}
	first, err := s.replyDraftTool(ctx, args)
	if err != nil || first["created"] != true || first["draftId"] == "" {
		t.Fatalf("draft: %+v %v", first, err)
	}
	saved, err := db.ReplyDraftForProcessing(ctx, processing.ID)
	if err != nil || saved.RoundID != r.ID {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	second, err := s.replyDraftTool(ctx, args)
	if err != nil || second["created"] != false {
		t.Fatalf("replay: %+v %v", second, err)
	}
	invented := args
	invented.RequestKey = "draft-invented"
	invented.Citations = []replydraft.DraftCitation{{MessageID: messages[0].ID, Excerpt: "we offer you the role"}}
	if _, err := s.replyDraftTool(ctx, invented); !errors.Is(err, replydraft.ErrInvalid) {
		t.Fatalf("invented excerpt accepted: %v", err)
	}
}

func TestReplyRoundContextRequiresThreadScope(t *testing.T) {
	ctx := context.Background()
	s, _, r, capability, _, _ := replyRoundFixture(t)
	read, err := s.roundContextTool(ctx, roundContextArgs{RoundID: r.ID, Capability: capability, RequestKey: "ctx"})
	if err != nil || read == nil {
		t.Fatalf("context: %+v %v", read, err)
	}
}
