package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestHealthRouteUsesContract(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	database, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	newHandler(database, auth.NewService(database), httpapi.Options{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	var health generated.HealthResponse
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != generated.Ok || health.Service != generated.JobseekApi || health.Version == "" {
		t.Fatalf("health response = %+v", health)
	}
}

func TestSetupAdminReadsStdinWithoutEchoAndCannotOverwrite(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := auth.NewService(database)
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	password := "synthetic-cli-password-2026"
	if _, err := writer.Write([]byte(password + "\n" + password + "\n")); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	var prompts bytes.Buffer
	if err := setupAdmin(ctx, service, input, &prompts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompts.String(), password) {
		t.Fatal("setup echoed password")
	}
	if err := service.SetupAdministrator(ctx, []byte("replacement-password-2026")); !errors.Is(err, store.ErrAlreadyConfigured) {
		t.Fatalf("setup retry: %v", err)
	}
}

func TestHostedOriginConfigurationRequiresTLS(t *testing.T) {
	if _, err := handlerOptions("0.0.0.0:8080", ""); err == nil {
		t.Fatal("non-loopback without TLS origin accepted")
	}
	if _, err := handlerOptions("0.0.0.0:8080", "http://example.test"); err == nil {
		t.Fatal("hosted HTTP origin accepted")
	}
	if _, err := handlerOptions("0.0.0.0:8080", "https://example.test/path"); err == nil {
		t.Fatal("origin path accepted")
	}
	options, err := handlerOptions("0.0.0.0:8080", "https://example.test")
	if err != nil || !options.SecureCookies || len(options.AllowedOrigins) != 1 || options.AllowedOrigins[0] != "https://example.test" {
		t.Fatalf("hosted options: %+v %v", options, err)
	}
}

func TestServeUntilDrainsHandlerBeforeDatabaseClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		if _, err := database.CurrentPreferences(context.Background()); err != nil {
			http.Error(w, "database closed while handler active", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- serveUntil(ctx, server, listener) }()
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			requestDone <- err
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			requestDone <- errors.New("handler lost database during drain")
			return
		}
		requestDone <- nil
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached handler")
	}
	cancel()
	select {
	case err := <-serveDone:
		t.Fatalf("server returned before handler drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not join")
	}
}
