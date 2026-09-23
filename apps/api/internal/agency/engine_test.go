package agency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/collector"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type collectorTransport func(*http.Request) (*http.Response, error)

func (f collectorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type savingRuntime struct {
	db             *store.Store
	turns          atomic.Int32
	beforeDispatch func(context.Context, string)
	afterSave      func(context.Context, string)
}

func (r *savingRuntime) CheckRound(context.Context, string) error { return nil }
func (r *savingRuntime) ExecuteRoundTurn(ctx context.Context, agent store.Actor, roundID string, input codexservice.RoundTurnInput) (store.RoundAttempt, error) {
	r.turns.Add(1)
	if r.beforeDispatch != nil {
		r.beforeDispatch(ctx, roundID)
		return store.RoundAttempt{}, context.Canceled
	}
	turn, _, err := r.db.ReserveRoundAttempt(ctx, agent, roundID, store.RoundAttemptInput{RequestKey: input.RequestKey, Operation: store.RoundCodexTurn, ResourceID: input.ResourceID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		return turn, err
	}
	if turn, err = r.db.MarkRoundDispatched(ctx, roundID, turn.ID); err != nil {
		return turn, err
	}
	if err := r.db.BindRoundThread(ctx, roundID, turn.ID, turn.Generation, "fixture-thread"); err != nil {
		return turn, err
	}
	if err := r.db.BindRoundTurn(ctx, roundID, turn.ID, turn.Generation, "fixture-thread", "fixture-turn"); err != nil {
		return turn, err
	}
	if err := r.db.ObserveRoundTurn(ctx, roundID, turn.ID, turn.Generation, "fixture-thread", "fixture-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		return turn, err
	}
	capability, err := r.db.IssueRoundToolCapability(ctx, roundID, turn.ID, agent.ID)
	if err != nil {
		return turn, err
	}
	var evidence struct {
		SourceOpeningID    string `json:"sourceOpeningId"`
		ExistingCompanyID  string `json:"existingCompanyId"`
		ExpectedRevision   int64  `json:"expectedRevision"`
		BoardCompanyName   string `json:"boardCompanyName"`
		OfficialCareersURL string `json:"officialCareersUrl"`
	}
	if err := json.Unmarshal([]byte(input.Evidence), &evidence); err != nil {
		return turn, err
	}
	if evidence.ExistingCompanyID == "" {
		profile, profileErr := r.db.CurrentPreferences(ctx)
		if profileErr != nil {
			return turn, profileErr
		}
		company, _, createErr := r.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "company:" + evidence.SourceOpeningID,
			Operation: store.RoundCreateCompany, ResourceID: "campaign:active", ExpectedRevision: profile.Version, Capability: capability,
			Company: &store.CompanyInput{Name: evidence.BoardCompanyName, Website: evidence.OfficialCareersURL}})
		if createErr != nil {
			return turn, createErr
		}
		evidence.ExistingCompanyID, evidence.ExpectedRevision = company.EntityID, company.Revision
	}
	_, _, err = r.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "save:" + evidence.SourceOpeningID, Operation: store.RoundSaveSourceOpportunity,
		ResourceID: "source-opening:" + evidence.SourceOpeningID, ExpectedRevision: evidence.ExpectedRevision, Capability: capability,
		SourceOpportunity: &store.SourceOpportunityMutationInput{SourceOpeningID: evidence.SourceOpeningID, ExpectedRevision: evidence.ExpectedRevision, CompanyID: evidence.ExistingCompanyID,
			Opportunity: store.OpportunityInput{CompanyID: evidence.ExistingCompanyID, Title: "Engineer", Kind: "employment", Stage: "new", WorkPattern: "remote", LocationText: "Brussels"}}})
	if err != nil {
		return turn, err
	}
	ended, err := r.db.FinishRoundAttempt(ctx, agent, roundID, turn.ID, true, json.RawMessage(`{"saved":true}`), "")
	if err == nil && r.afterSave != nil {
		r.afterSave(ctx, roundID)
	}
	return ended, err
}

type stopAfterSaveDecision struct{}

func (stopAfterSaveDecision) RunDecision(ctx context.Context, binding jevservice.Binding, input jev.DecisionInput) (jev.DecisionResult, error) {
	return firstDecision{}.RunDecision(ctx, binding, input)
}
func (stopAfterSaveDecision) RunScreening(context.Context, jevservice.Binding, jev.ScreeningInput) (jev.ScreeningResult, error) {
	return jev.ScreeningResult{}, errors.New("screening intentionally unavailable in collector fixture")
}
func (stopAfterSaveDecision) RunOrganisation(context.Context, jevservice.Binding, jev.OrganisationInput) (jev.OrganisationResult, error) {
	panic("screening must stop this fixture")
}

type countedDecisions struct {
	choices      atomic.Int32
	screened     atomic.Int32
	lastInput    jev.ScreeningInput
	beforeSource func(context.Context, string)
}

func (d *countedDecisions) RunDecision(ctx context.Context, binding jevservice.Binding, input jev.DecisionInput) (jev.DecisionResult, error) {
	d.choices.Add(1)
	if d.beforeSource != nil && len(input.Capabilities) == 1 && input.Capabilities[0].ID == "source_extract" {
		d.beforeSource(ctx, binding.RoundID)
		return jev.DecisionResult{}, context.Canceled
	}
	return firstDecision{}.RunDecision(ctx, binding, input)
}
func (d *countedDecisions) RunScreening(_ context.Context, _ jevservice.Binding, input jev.ScreeningInput) (jev.ScreeningResult, error) {
	d.screened.Add(1)
	d.lastInput = input
	return jev.ScreeningResult{}, errors.New("assessment fixture stops after screening input")
}
func (d *countedDecisions) RunOrganisation(context.Context, jevservice.Binding, jev.OrganisationInput) (jev.OrganisationResult, error) {
	panic("screening must stop this fixture")
}

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

func TestLaterCommissionDrainsExactSavedCollectorPosting(t *testing.T) {
	ctx := context.Background()
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
	board := boards[0]
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: board.DisplayName, Website: board.OfficialCareersURL})
	if err != nil {
		t.Fatal(err)
	}
	postingHost := "jobs.lever.co"
	if board.Region == "eu" {
		postingHost = "jobs.eu.lever.co"
	}
	var raw []string
	for i := 1; i <= 5; i++ {
		raw = append(raw, fmt.Sprintf(`{ "id": "role-%d", "text": "Engineer %d", "descriptionPlain": "Build Go services %d", "hostedUrl": "https://%s/%s/role-%d" }`, i, i, i, postingHost, board.Site, i))
	}
	page := []byte("[" + strings.Join(raw, ",") + "]")
	var calls atomic.Int32
	client := &http.Client{Transport: collectorTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.Query().Get("skip") != "0" {
			t.Errorf("unexpected page offset: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(page)), Request: req}, nil
	})}
	engine := &Engine{Store: db, Runtime: &savingRuntime{db: db}, Decisions: stopAfterSaveDecision{}, Collector: &collector.Collector{HTTPClient: client}, Context: ctx}
	svc := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	start := func(key string) (store.Round, store.RoundAllowance, string, bool) {
		t.Helper()
		r, _, err := svc.Start(ctx, owner, store.StartRoundInput{RequestKey: key, Intent: "Find sourced openings", Outcome: "discover", ProfileVersion: profile.Version,
			Scope:  store.RoundScope{Resources: []string{"campaign:active", "board:" + board.ID, "company:" + company.ID}, Operations: []string{store.RoundJevRequest, store.RoundCollectorPage, store.RoundCodexTurn, store.RoundSaveSourceOpportunity}, Delegates: []string{"codex-runner"}},
			Limits: store.RoundAllowance{Requests: 3, Items: 5, Tools: 5, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			r, err = db.Round(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.State == store.RoundCompleted {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if r.State != store.RoundCompleted {
			t.Fatalf("commission did not finish: %+v", r)
		}
		var report struct {
			CollectorAttemptID string `json:"collectorAttemptId"`
			CollectorHasMore   bool   `json:"collectorHasMore"`
		}
		if err := json.Unmarshal(r.Report, &report); err != nil || report.CollectorAttemptID == "" {
			t.Fatalf("report: %s %v", r.Report, err)
		}
		return r, r.Used, report.CollectorAttemptID, report.CollectorHasMore
	}
	var previousRoundID string
	for i := range raw {
		r, used, attemptID, hasMore := start(fmt.Sprintf("commission-%d", i))
		if r.ID == previousRoundID || calls.Load() != 1 {
			t.Fatalf("commission %d charge/calls: round=%s used=%+v calls=%d", i, r.ID, used, calls.Load())
		}
		previousRoundID = r.ID
		if i == 0 && (used.Items != 5 || used.Requests != 2) || i > 0 && i < 4 && (used.Items != 1 || used.Requests != 1) || i == 4 && (used.Items != 5 || used.Requests != 1) {
			t.Fatalf("commission %d allowance: %+v report=%s", i, used, r.Report)
		}
		payload, err := db.RoundCollectorBatch(ctx, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		var batch collector.Batch
		if json.Unmarshal(payload, &batch) != nil || len(batch.Postings) == 0 || hasMore != (i < len(raw)-1) {
			t.Fatalf("commission %d exact source: %+v hasMore=%v", i, batch, hasMore)
		}
		if i == 0 {
			if len(batch.Postings) != 4 || batch.Next == nil || len(batch.Next.Pending) != 1 || !bytes.Equal(batch.Next.Pending[0].Raw, []byte(raw[4])) {
				t.Fatalf("first bounded batch: %+v", batch)
			}
		} else if i < 4 {
			if len(batch.Postings) != 4 || batch.Next == nil || len(batch.Next.Pending) != 1 {
				t.Fatalf("backlog should reuse first batch: %+v", batch)
			}
		} else if len(batch.Postings) != 1 || batch.Next != nil || !bytes.Equal(batch.Postings[0].OriginalText, []byte(raw[4])) {
			t.Fatalf("later exact posting: %+v", batch)
		}
	}
	if _, _, err := db.LatestRoundCollectorContinuation(ctx, owner, board.ID); err != store.ErrNotFound {
		t.Fatalf("completed board exposed older continuation: %v", err)
	}
}

func TestResumeAfterCollectorCursorImportBeforeAcquisition(t *testing.T) {
	ctx := context.Background()
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
	board := boards[0]
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: board.DisplayName, Website: board.OfficialCareersURL})
	if err != nil {
		t.Fatal(err)
	}
	host := "jobs.lever.co"
	if board.Region == "eu" {
		host = "jobs.eu.lever.co"
	}
	raw := []byte(fmt.Sprintf(`{"id":"retained-one","text":"Engineer","descriptionPlain":"Build services.","hostedUrl":"https://%s/%s/retained-one"}`, host, board.Site))
	prior, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "prior-pending", Intent: "Collect source", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"board:" + board.ID}, Operations: []string{store.RoundCollectorPage}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 1, Items: 4, Tools: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	prior, err = db.ActivateRound(ctx, owner, prior.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := db.ReserveCollectorAcquisition(ctx, agent, prior.ID, store.RoundCollectorAcquisitionInput{RequestKey: "prior-page", BoardID: board.ID, MaxPages: 1, MaxItems: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, prior.ID, page.ID); err != nil {
		t.Fatal(err)
	}
	prior, err = db.Round(ctx, prior.ID)
	if err != nil {
		t.Fatal(err)
	}
	batch, _ := json.Marshal(collector.Batch{Next: &collector.Cursor{BoardID: board.ID, Pending: []collector.PendingPosting{{Raw: raw, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}, EndOfBoard: true}, PagesFetched: 1})
	if _, err := db.SaveRoundCollectorBatch(ctx, agent, prior.ID, page.ID, prior.Revision, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRound(ctx, owner, prior.ID, store.RoundCompleted, "batch_staged", "partial", json.RawMessage(`{"code":"batch_staged"}`)); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	client := &http.Client{Transport: collectorTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("saved pending item must not refetch")
	})}
	engine := &Engine{Store: db, Runtime: &savingRuntime{db: db}, Decisions: stopAfterSaveDecision{}, Collector: &collector.Collector{HTTPClient: client}, Context: ctx}
	svc := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	current, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "resume-import", Intent: "Review saved pending source", Outcome: "discover", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{"campaign:active", "board:" + board.ID, "company:" + company.ID}, Operations: []string{store.RoundCollectorPage, store.RoundCodexTurn, store.RoundSaveSourceOpportunity, store.RoundJevRequest}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 2, Items: 5, Tools: 4, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	current, err = db.ActivateRound(ctx, owner, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := db.ImportRoundCollectorCursor(ctx, owner, current.ID, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ImportRoundCollectorCursor(ctx, owner, current.ID, page.ID); err != nil {
		t.Fatalf("same cursor import should replay: %v", err)
	}
	var saved agencyCursor
	if json.Unmarshal(imported.Cursor, &saved) != nil || saved.CollectorBatchAttemptID != page.ID || saved.ImportedCollectorAttemptID != page.ID || saved.Selected != nil {
		t.Fatalf("import boundary: %s", imported.Cursor)
	}
	if _, err := svc.Stop(ctx, owner, current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resume(ctx, owner, current.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, err = db.Round(ctx, current.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == store.RoundCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if current.State != store.RoundCompleted || calls.Load() != 0 || current.Used.Requests != 1 || current.Used.Items != 5 {
		t.Fatalf("resumed saved cursor: state=%s step=%s cursor=%s calls=%d used=%+v report=%s", current.State, current.Step, current.Cursor, calls.Load(), current.Used, current.Report)
	}
	var final agencyCursor
	if json.Unmarshal(current.Cursor, &final) != nil || final.Selected == nil || final.Selected.OutcomeRoundID != current.ID {
		t.Fatalf("resumed pending source selection: %s", current.Cursor)
	}
	staged, err := db.RoundCollectorBatch(ctx, final.Selected.BatchAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var drained collector.Batch
	if json.Unmarshal(staged, &drained) != nil || len(drained.Postings) != 1 || !bytes.Equal(drained.Postings[0].OriginalText, raw) {
		t.Fatalf("saved exact source was not drained: %+v", drained)
	}
}

func TestResumeKeepsExactSourcePhase(t *testing.T) {
	for _, scenario := range []struct {
		name, stopAt string
		retained     bool
	}{{"fresh_after_save", "after_save", false}, {"retained_after_save", "after_save", true}, {"pin_before_dispatch", "before_dispatch", false}, {"batch_before_selection", "before_source", false}} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
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
			board := boards[0]
			owner := store.Actor{Kind: "administrator", ID: "owner"}
			company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: board.DisplayName, Website: board.OfficialCareersURL})
			if err != nil {
				t.Fatal(err)
			}
			host := "jobs.lever.co"
			if board.Region == "eu" {
				host = "jobs.eu.lever.co"
			}
			raw := []string{
				fmt.Sprintf(`{"id":"role-one","text":"Engineer One","descriptionPlain":"Build services one.","hostedUrl":"https://%s/%s/role-one"}`, host, board.Site),
				fmt.Sprintf(`{"id":"role-two","text":"Engineer Two","descriptionPlain":"Build services two.","hostedUrl":"https://%s/%s/role-two"}`, host, board.Site),
			}
			var providerCalls atomic.Int32
			client := &http.Client{Transport: collectorTransport(func(req *http.Request) (*http.Response, error) {
				providerCalls.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("[" + strings.Join(raw, ",") + "]")), Request: req}, nil
			})}
			runtime := &savingRuntime{db: db}
			decisions := &countedDecisions{}
			engine := &Engine{Store: db, Runtime: runtime, Decisions: decisions, Collector: &collector.Collector{HTTPClient: client}, Context: ctx}
			svc := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
			start := func(key string) store.Round {
				t.Helper()
				r, _, err := svc.Start(ctx, owner, store.StartRoundInput{RequestKey: key, Intent: "Inspect this board", Outcome: "discover", ProfileVersion: profile.Version,
					Scope:  store.RoundScope{Resources: []string{"campaign:active", "board:" + board.ID, "company:" + company.ID}, Operations: []string{store.RoundCollectorPage, store.RoundCodexTurn, store.RoundSaveSourceOpportunity, store.RoundJevRequest}, Delegates: []string{"codex-runner"}},
					Limits: store.RoundAllowance{Requests: 3, Items: 5, Tools: 5, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			waitState := func(id string, wanted store.RoundState) store.Round {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					r, readErr := db.Round(ctx, id)
					if readErr != nil {
						t.Fatal(readErr)
					}
					if r.State == wanted {
						return r
					}
					time.Sleep(5 * time.Millisecond)
				}
				r, _ := db.Round(ctx, id)
				t.Fatalf("round did not reach %s: %+v", wanted, r)
				return store.Round{}
			}
			if scenario.retained {
				first := start("seed-retained-batch")
				waitState(first.ID, store.RoundCompleted)
			}
			stopped := make(chan error, 1)
			stop := func(callCtx context.Context, id string) {
				_, stopErr := svc.Stop(context.WithoutCancel(callCtx), owner, id)
				stopped <- stopErr
			}
			switch scenario.stopAt {
			case "after_save":
				runtime.afterSave = stop
			case "before_dispatch":
				runtime.beforeDispatch = stop
			case "before_source":
				decisions.beforeSource = stop
			}
			current := start(scenario.name)
			select {
			case stopErr := <-stopped:
				if stopErr != nil {
					t.Fatal(stopErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("source save did not reach Stop boundary")
			}
			paused := waitState(current.ID, store.RoundPaused)
			var cursor agencyCursor
			if json.Unmarshal(paused.Cursor, &cursor) != nil {
				t.Fatalf("paused cursor: %s", paused.Cursor)
			}
			if scenario.stopAt == "before_source" {
				if cursor.Selected != nil || cursor.CollectorBatchAttemptID == "" {
					t.Fatalf("batch should be staged without a selected source: %s", paused.Cursor)
				}
			} else {
				if cursor.Selected == nil || cursor.Selected.SourceOpeningID == "" || cursor.Selected.IngestionID == "" {
					t.Fatalf("selected source was not pinned: %s", paused.Cursor)
				}
				if scenario.retained && cursor.Selected.OutcomeRoundID == current.ID || !scenario.retained && cursor.Selected.OutcomeRoundID != current.ID {
					t.Fatalf("selected batch origin: %+v", cursor.Selected)
				}
			}
			oldExtractionKey := ""
			if cursor.Selected != nil {
				oldExtractionKey = cursor.Selected.ExtractionRequestKey
			}
			beforeChoices, beforeTurns, beforeScreened := decisions.choices.Load(), runtime.turns.Load(), decisions.screened.Load()
			runtime.afterSave = nil
			runtime.beforeDispatch = nil
			decisions.beforeSource = nil
			resumed, err := svc.Resume(ctx, owner, current.ID)
			if err != nil || resumed.State != store.RoundRunning {
				t.Fatalf("resume: %+v %v", resumed, err)
			}
			finished := waitState(current.ID, store.RoundCompleted)
			choiceDelta, turnDelta := decisions.choices.Load()-beforeChoices, runtime.turns.Load()-beforeTurns
			if scenario.stopAt == "after_save" && (choiceDelta != 0 || turnDelta != 0 || finished.Used != paused.Used) || scenario.stopAt == "before_dispatch" && (choiceDelta != 0 || turnDelta != 1 || finished.Used.Turns != 1) || scenario.stopAt == "before_source" && (choiceDelta != 1 || turnDelta != 1 || finished.Used.Turns != 1) || decisions.screened.Load() != beforeScreened+1 {
				t.Fatalf("resume phase changed: choices=%d turns=%d screened=%d/%d used=%+v/%+v report=%s", choiceDelta, turnDelta, decisions.screened.Load(), beforeScreened, finished.Used, paused.Used, finished.Report)
			}
			if len(decisions.lastInput.Spans) == 0 || decisions.lastInput.Spans[0].SourceID == "" || providerCalls.Load() != 1 {
				t.Fatalf("resume did not assess saved source: %+v calls=%d", decisions.lastInput, providerCalls.Load())
			}
			var finalCursor agencyCursor
			if json.Unmarshal(finished.Cursor, &finalCursor) != nil || finalCursor.Selected == nil || finalCursor.Selected.BoardID != board.ID {
				t.Fatalf("resumed selection has wrong board: %s", finished.Cursor)
			}
			if scenario.stopAt == "before_dispatch" {
				if _, err := db.RoundAttemptForRequest(ctx, current.ID, oldExtractionKey); !errors.Is(err, store.ErrNotFound) || finalCursor.Selected.ExtractionRequestKey == oldExtractionKey {
					t.Fatalf("undispatched extraction was reused: key=%s next=%s err=%v", oldExtractionKey, finalCursor.Selected.ExtractionRequestKey, err)
				}
			}
		})
	}
}
