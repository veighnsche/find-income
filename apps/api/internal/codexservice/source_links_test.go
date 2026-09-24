package codexservice

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func scopedSourceCall(t *testing.T, s *Service, db *store.Store) (store.Round, store.RoundAttempt, store.Actor, SourceLinksArgs) {
	return scopedSourceCallWithDeadline(t, s, db, time.Now().Add(time.Hour))
}
func scopedSourceCallWithDeadline(t *testing.T, s *Service, db *store.Store, deadline time.Time) (store.Round, store.RoundAttempt, store.Actor, SourceLinksArgs) {
	t.Helper()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Shopify", Website: "https://www.shopify.com/"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "company:" + company.ID
	r, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "source-round", Intent: "Read saved employer source", Outcome: "process_input", ProfileVersion: p.Version, Deadline: deadline, Scope: store.RoundScope{Resources: []string{resource}, Operations: []string{store.RoundCodexTurn, store.RoundSearchSource}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: 2, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = db.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	cap, err := s.BindRoundToolSession(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r, turn, owner, SourceLinksArgs{RoundID: r.ID, Capability: cap, RequestKey: "links", ResourceID: resource}
}
func TestRoundSourceLinksChargesOnceAndReplaysSavedEvidence(t *testing.T) {
	s, db := testService(t, testConfig())
	r, _, _, args := scopedSourceCall(t, s, db)
	ctx := boundedContext(t)
	var calls atomic.Int32
	s.linkFetch = func(_ context.Context, u string, _ SourceLinkPage) (SourceLinksSnapshot, error) {
		calls.Add(1)
		if u != "https://www.shopify.com/" {
			t.Fatalf("unexpected server-selected URL %q", u)
		}
		return SourceLinksSnapshot{SourceURL: u, Status: "ok", ContentSHA256: "synthetic-hash", Links: []SourceLink{{URL: "https://www.shopify.com/careers", Text: "Careers"}}}, nil
	}
	first, err := s.RoundSourceLinks(ctx, args)
	if err != nil || first.AttemptID == "" || first.CompanyID == "" || first.CompanyRevision < 1 {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := s.RoundSourceLinks(ctx, args)
	if err != nil || second.AttemptID != first.AttemptID || calls.Load() != 1 {
		t.Fatalf("replay %+v %v calls=%d", second, err, calls.Load())
	}
	args.Offset = 64
	args.ExpectedContentSHA256 = strings.Repeat("0", 64)
	if _, err := s.RoundSourceLinks(ctx, args); !errors.Is(err, store.ErrRoundIdempotencyConflict) || calls.Load() != 1 {
		t.Fatalf("changed page reused key: %v", err)
	}
	args.Offset = 0
	args.ExpectedContentSHA256 = ""
	current, err := db.Round(ctx, r.ID)
	if err != nil || current.Used.Requests != 1 || current.Used.Tools != 2 || current.Used.Turns != 1 {
		t.Fatalf("allowance %+v %v", current.Used, err)
	}
	args.ResourceID = "company:other"
	if _, err := s.RoundSourceLinks(ctx, args); err == nil || calls.Load() != 1 {
		t.Fatalf("scope escape fetched: %v", err)
	}
}
func TestRoundSourceLinksStopCancelsReadAndFencesResult(t *testing.T) {
	s, db := testService(t, testConfig())
	r, turn, owner, args := scopedSourceCall(t, s, db)
	ctx := boundedContext(t)
	started := make(chan struct{})
	s.linkFetch = func(ctx context.Context, _ string, _ SourceLinkPage) (SourceLinksSnapshot, error) {
		close(started)
		<-ctx.Done()
		return SourceLinksSnapshot{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := s.RoundSourceLinks(ctx, args); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("source fetch did not start")
	}
	controller := &rounds.Service{Store: db, Canceller: s}
	paused, err := controller.Stop(ctx, owner, r.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("stop %+v %v", paused, err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrFenced) {
			t.Fatalf("late source result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source fetch not cancelled")
	}
	if _, err := db.VerifyRoundToolCapability(ctx, args.Capability, r.ID); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("old token survived Stop: %v", err)
	}
	observation, err := s.ObserveDispatch(ctx, turn.ID)
	if err == nil && observation.State == store.AttemptObservedSuccess {
		t.Fatal("unseen Codex turn declared successful")
	}
}
func TestRoundSourceLinksHonorsSavedRoundDeadline(t *testing.T) {
	s, db := testService(t, testConfig())
	r, _, _, args := scopedSourceCallWithDeadline(t, s, db, time.Now().Add(350*time.Millisecond))
	started := make(chan struct{})
	var attemptID string
	s.linkFetch = func(ctx context.Context, _ string, _ SourceLinkPage) (SourceLinksSnapshot, error) {
		attempt, _, e := db.ReserveRoundAttempt(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, store.RoundAttemptInput{RequestKey: args.RequestKey, Operation: store.RoundSearchSource, ResourceID: args.ResourceID, Cost: store.RoundAllowance{Requests: 1, Tools: 1}, BoundCapability: args.Capability})
		if e == nil {
			attemptID = attempt.ID
		}
		close(started)
		<-ctx.Done()
		return SourceLinksSnapshot{}, ctx.Err()
	}
	begin := time.Now()
	_, err := s.RoundSourceLinks(context.Background(), args)
	if err == nil || time.Since(begin) > 2*time.Second {
		t.Fatalf("round deadline not enforced: elapsed=%s err=%v", time.Since(begin), err)
	}
	select {
	case <-started:
	default:
		t.Fatal("source fetch did not start")
	}
	current, e := db.Round(context.Background(), r.ID)
	if e != nil || current.Used.Requests != 1 || current.State != store.RoundFailed || current.StopReason != "deadline_reached" {
		t.Fatalf("unaccounted deadline: %+v %v", current, e)
	}
	attempts, e := db.UncertainRoundAttempts(context.Background(), r.ID)
	if !errors.Is(e, store.ErrFenced) || len(attempts) != 0 {
		t.Fatalf("expired round unexpectedly resumable: %v %+v", e, attempts)
	}
	if attemptID == "" {
		t.Fatal("deadline attempt ID was not retained")
	}
	attempt, e := db.RoundAttempt(context.Background(), attemptID)
	if e != nil || attempt.State != store.AttemptUncertain || len(attempt.LateResult) == 0 {
		t.Fatalf("lost deadline evidence: %+v %v", attempt, e)
	}
}
func TestSourceLinksMCPCallConsumesRoundAllowance(t *testing.T) {
	s, db := testService(t, testConfig())
	r, _, _, args := scopedSourceCall(t, s, db)
	s.linkFetch = func(_ context.Context, u string, _ SourceLinkPage) (SourceLinksSnapshot, error) {
		return SourceLinksSnapshot{SourceURL: u, Status: "ok", ContentSHA256: strings.Repeat("a", 64), Links: []SourceLink{{URL: "https://www.shopify.com/careers", Text: "Careers"}}}, nil
	}
	session, _ := sdkSession(t, s)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "source_links", Arguments: map[string]any{"roundId": args.RoundID, "capability": args.Capability, "requestKey": args.RequestKey, "resourceId": args.ResourceID}})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("MCP tool: result=%+v err=%v", result, err)
	}
	current, err := db.Round(context.Background(), r.ID)
	if err != nil || current.Used.Requests != 1 || current.Used.Tools != 2 {
		t.Fatalf("MCP tool did not charge: %+v %v", current.Used, err)
	}
}
