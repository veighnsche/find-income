package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestIncompleteRequestBodyHasReadDeadlineAndServerRemainsUsable(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := auth.NewService(database)
	password := "synthetic-read-deadline-password-2026"
	if err := service.SetupAdministrator(ctx, []byte(password)); err != nil {
		t.Fatal(err)
	}
	server := newAPIServer("127.0.0.1:0", newHandler(database, service, httpapi.Options{AllowedOrigins: []string{"http://dashboard.test"}}))
	if server.ReadTimeout != 5*time.Second || server.WriteTimeout != 0 {
		t.Fatalf("unexpected production timeouts: read=%s write=%s", server.ReadTimeout, server.WriteTimeout)
	}
	server.ReadTimeout = 250 * time.Millisecond
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	}()

	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	partial := "POST /api/v1/auth/login HTTP/1.1\r\nHost: " + listener.Addr().String() + "\r\nOrigin: http://dashboard.test\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"
	if _, err := io.WriteString(connection, partial); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatalf("incomplete body did not receive a bounded response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("incomplete body status = %d, want 400", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/api/v1/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "http://dashboard.test")
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	validResponse, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	validResponse.Body.Close()
	if validResponse.StatusCode != http.StatusOK {
		t.Fatalf("valid login after incomplete body = %d, want 200", validResponse.StatusCode)
	}
}

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
