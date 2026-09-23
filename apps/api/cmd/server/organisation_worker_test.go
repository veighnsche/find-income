package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/collector"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestOrganisationWorkerUnavailableWithoutKey(t *testing.T) {
	t.Setenv(jev.CredentialEnvironmentVariable, "")
	worker, err := newOrganisationWorker(nil, jev.DefaultConfig(), nil)
	if err != nil || worker != nil {
		t.Fatalf("worker without key: %v, %v", worker, err)
	}
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	if _, err := newOrganisationWorker(nil, cfg, nil); !errors.Is(err, &jev.Error{Kind: jev.ErrMissingKey}) {
		t.Fatalf("enabled without key: %v", err)
	}
}

func TestOrganisationWorkerStartsProcessesAndJoinsOnShutdown(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	_, err = database.UpdateOrganisationCategories(ctx, 1, []store.OrganisationCategory{
		{ID: "research", Description: "Vacancies for user research work."},
		{ID: "operations", Description: "Vacancies for running services."},
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = database.SubmitIngestion(ctx, owner, store.IngestionInput{Origin: "owner", OriginalText: "Interview users and analyse research findings.", IdempotencyKey: "synthetic-worker-startup"})
	if err != nil {
		t.Fatal(err)
	}
	claim, found, err := database.ClaimNextJob(ctx, "synthetic-ingestion", []string{store.IngestionJobKind}, time.Minute, time.Now())
	if err != nil || !found {
		t.Fatalf("ingestion claim: %v, %v", found, err)
	}
	opportunity, _, err := database.SaveIngestionOpportunity(ctx, claim, store.IngestionRecordInput{
		NewCompany: &store.CompanyInput{Name: "Synthetic Research Co"},
		Opportunity: store.OpportunityInput{Title: "User Researcher", Kind: "employment", Compensation: store.AdvertisedCompensation{
			Currency: "unknown", Period: "unknown", Basis: "unknown"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := database.CompleteJob(ctx, claim, store.JobResult{Ref: opportunity.ID}, time.Now()); err != nil || !completed {
		t.Fatalf("complete ingestion: %v, %v", completed, err)
	}

	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-provider-key")
	providerCalls := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer synthetic-provider-key" {
			t.Errorf("wrong synthetic provider request metadata")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
			len(request.Questions) != 1 || len(request.Questions["organisation_category"].Criteria) != 3 {
			t.Errorf("invalid synthetic provider input: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case providerCalls <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"organisation_category":{"type":"choice","choice":"research","probabilities":{"research":0.8,"operations":0.1,"__uncertain__":0.1},"confidence":0.8}},"usage":{"input_tokens":70,"output_tokens":8}}`))
	}))
	defer provider.Close()
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = provider.URL + "/v1/systemone"
	worker, err := newOrganisationWorker(database, cfg, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(worker.Handlers) != 1 || worker.Handlers[store.OrganisationJobKind] == nil || worker.Handlers[store.IngestionJobKind] != nil {
		t.Fatalf("unexpected worker registration: %#v", worker.Handlers)
	}
	worker.PollInterval = 5 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newAPIServer(listener.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- serveWithWorker(serviceCtx, server, listener, worker) }()
	select {
	case <-providerCalls:
	case <-time.After(3 * time.Second):
		t.Fatal("organisation worker did not call fake provider")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		view, err := database.Organisation(ctx, opportunity.ID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Status == "selected" && view.Current != nil && view.Current.CategoryID == "research" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("organisation never applied: %+v", view)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server and worker did not join after cancellation")
	}
}

func TestCollectorStartsWithNoBoardsAndJoinsOnShutdown(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	boards, err := database.ListCollectorBoards(ctx)
	if err != nil || len(boards) != 0 {
		t.Fatalf("fresh database unexpectedly has collector boards: %d, %v", len(boards), err)
	}
	service := &collector.Collector{Store: database, PollInterval: 5 * time.Millisecond, Lease: time.Minute}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newAPIServer(listener.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- serveWithBackground(serviceCtx, server, listener, backgroundRunner{name: "collector", run: service.Run})
	}()
	response, err := (&http.Client{Timeout: time.Second}).Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTP server did not start with collector: %d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not join after cancellation")
	}
}

func TestBackgroundFailureCancelsAndJoinsOtherRunner(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newAPIServer(listener.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	started := make(chan struct{})
	joined := make(chan struct{})
	want := errors.New("synthetic collector failure")
	err = serveWithBackground(context.Background(), server, listener,
		backgroundRunner{name: "other", run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(joined)
			return nil
		}},
		backgroundRunner{name: "collector", run: func(context.Context) error {
			<-started
			return want
		}})
	if !errors.Is(err, want) {
		t.Fatalf("background error lost: %v", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("other runner not joined before return")
	}
}
