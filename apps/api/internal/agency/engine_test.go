package agency

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/collector"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type inertRuntime struct{ turns int }

func (r *inertRuntime) CheckRound(context.Context, string) error { return nil }
func (r *inertRuntime) ExecuteRoundTurn(context.Context, store.Actor, string, codexservice.RoundTurnInput) (store.RoundAttempt, error) {
	r.turns++
	return store.RoundAttempt{}, nil
}

type firstDecision struct{}

func (firstDecision) RunDecision(_ context.Context, _ jevservice.Binding, input jev.DecisionInput) (jev.DecisionResult, error) {
	return jev.DecisionResult{Disposition: jev.DecisionSelected, SelectedID: input.Candidates[0].ID}, nil
}
func (firstDecision) RunScreening(context.Context, jevservice.Binding, jev.ScreeningInput) (jev.ScreeningResult, error) {
	panic("no source to screen")
}
func (firstDecision) RunOrganisation(context.Context, jevservice.Binding, jev.OrganisationInput) (jev.OrganisationResult, error) {
	panic("no source to organise")
}

type emptyCollector struct{ called chan struct{} }

func (c *emptyCollector) AcquireLever(ctx context.Context, _ collector.Request) (collector.Batch, error) {
	select {
	case c.called <- struct{}{}:
	default:
	}
	return collector.Batch{Postings: []collector.StagedPosting{}, PagesFetched: 1, ItemsExamined: 0}, nil
}

func TestOwnerStartStagesOneBoundedPageAndReturnsPartialReport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	boards, err := db.ListCollectorBoards(ctx)
	if err != nil || len(boards) == 0 {
		t.Fatalf("boards: %v", err)
	}
	runtime := &inertRuntime{}
	collect := &emptyCollector{called: make(chan struct{}, 1)}
	engine := &Engine{Store: db, Runtime: runtime, Decisions: firstDecision{}, Collector: collect, Context: ctx}
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	svc := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	r, created, err := svc.Start(ctx, owner, store.StartRoundInput{RequestKey: "start", Intent: "Find sourced openings", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active", "board:" + boards[0].ID}, Operations: []string{store.RoundJevRequest, store.RoundCollectorPage}},
		Limits: store.RoundAllowance{Requests: 4, Items: 4, Tools: 4, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil || !created {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-collect.called:
	case <-time.After(time.Second):
		t.Fatal("commissioned page not collected")
	}
	var finished store.Round
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		finished, err = db.Round(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished.State == store.RoundCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if finished.State != store.RoundCompleted || finished.DeliverableStatus != "partial" || runtime.turns != 0 {
		t.Fatalf("round: %+v turns=%d", finished, runtime.turns)
	}
	var report struct {
		Code               string `json:"code"`
		CollectorAttemptID string `json:"collectorAttemptId"`
	}
	if json.Unmarshal(finished.Report, &report) != nil || report.Code != "no_new_source" || report.CollectorAttemptID == "" {
		t.Fatalf("report: %s", finished.Report)
	}
	if _, err := db.RoundCollectorBatch(ctx, report.CollectorAttemptID); err != nil {
		t.Fatal(err)
	}
}
