package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestConfiguredServerStartupLeavesRecruitmentQueued(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Actor{Kind: "administrator", ID: "test-owner"}
	password := "synthetic-startup-password-2026"
	if err := auth.NewService(database).SetupAdministrator(ctx, []byte(password)); err != nil {
		t.Fatal(err)
	}
	intake, _, err := database.SubmitIngestion(ctx, owner, store.IngestionInput{
		Origin: "owner", OriginalText: "Synthetic queued vacancy.", IdempotencyKey: "idle-startup"})
	if err != nil {
		t.Fatal(err)
	}
	organisation, _, err := database.EnqueueJob(ctx, store.JobRequest{Kind: store.OrganisationJobKind,
		Payload: json.RawMessage(`{}`), Actor: owner, IdempotencyKey: "idle-organisation", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	// Any accidental HTTP provider request stays inside this fake proxy.
	providerCalls := make(chan struct{}, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case providerCalls <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-provider-key")
	t.Setenv("JOBSEEK_DATA_DIR", dataDir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	t.Setenv("JOBSEEK_LISTEN_ADDR", addr)
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	var runErr error
	go func() {
		runErr = runWithContext(serviceCtx, nil)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(6 * time.Second):
			t.Error("server did not finish cleanup")
		}
	})
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, requestErr := client.Get("http://" + addr + "/api/v1/health")
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("health status: %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready: %v", requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	login, err := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/auth/login",
		strings.NewReader(`{"password":"`+password+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Origin", "http://"+addr)
	login.Header.Set("Content-Type", "application/json")
	loginResponse, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, loginResponse.Body)
	loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusOK || len(loginResponse.Cookies()) != 1 {
		t.Fatalf("login: %d", loginResponse.StatusCode)
	}
	statusRequest, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/v1/runtime-status", nil)
	if err != nil {
		t.Fatal(err)
	}
	statusRequest.AddCookie(loginResponse.Cookies()[0])
	statusResponse, err := client.Do(statusRequest)
	if err != nil {
		t.Fatal(err)
	}
	var capabilities struct {
		IngestionAvailable    bool `json:"ingestionAvailable"`
		OrganisationAvailable bool `json:"organisationAvailable"`
	}
	decodeErr := json.NewDecoder(statusResponse.Body).Decode(&capabilities)
	statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK || decodeErr != nil ||
		capabilities.IngestionAvailable || capabilities.OrganisationAvailable {
		t.Fatalf("startup advertised recruitment capabilities: status=%d value=%+v decode=%v",
			statusResponse.StatusCode, capabilities, decodeErr)
	}
	// Finish this fixture's connections before measuring idle server shutdown.
	client.CloseIdleConnections()
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case <-finished:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
	select {
	case <-providerCalls:
		t.Fatal("startup made a provider request")
	default:
	}
	database, err = store.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	readIntake, err := database.Ingestion(ctx, intake.ID)
	if err != nil || readIntake.JobState != store.JobQueued || readIntake.Status != "pending" {
		t.Fatalf("intake claimed on startup: %+v %v", readIntake, err)
	}
	readOrganisation, err := database.Job(ctx, organisation.ID)
	if err != nil || readOrganisation.State != store.JobQueued || readOrganisation.AttemptCount != 0 {
		t.Fatalf("organisation claimed on startup: %+v %v", readOrganisation, err)
	}
}
