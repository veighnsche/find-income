package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/collector"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jobs"
	"github.com/veighnsche/find-income-dashboard/api/internal/organisation"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dataDir, err := privateDataDir()
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("open private data: %w", err)
	}
	defer database.Close()
	service := auth.NewService(database)
	if len(os.Args) > 1 {
		if len(os.Args) != 2 || os.Args[1] != "setup-admin" {
			return errors.New("usage: jobseek [setup-admin]; passwords are read from stdin, never arguments")
		}
		if err := setupAdmin(ctx, service, os.Stdin, os.Stderr); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Administrator credential configured.")
		return nil
	}
	addr := os.Getenv("JOBSEEK_LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	options, err := handlerOptions(addr, os.Getenv("JOBSEEK_PUBLIC_ORIGIN"))
	if err != nil {
		return err
	}
	jevConfig := jev.DefaultConfig()
	jevConfig.Enabled = strings.TrimSpace(os.Getenv(jev.CredentialEnvironmentVariable)) != ""
	organisationWorker, err := newOrganisationWorker(database, jevConfig, nil)
	if err != nil {
		return fmt.Errorf("configure organisation worker: %w", err)
	}
	collectorService := &collector.Collector{Store: database, PollInterval: time.Minute, Lease: 5 * time.Minute}
	options.OrganisationAvailable = organisationWorker != nil
	options.CollectionAvailable = true
	server := newAPIServer(addr, newHandler(database, service, options))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Printf("jobseek API listening on %s", addr)
	background := []backgroundRunner{{name: "collector", run: collectorService.Run}}
	if organisationWorker != nil {
		background = append(background, backgroundRunner{name: "organisation worker", run: organisationWorker.Run})
	}
	if err := serveWithBackground(ctx, server, listener, background...); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// No credential means no worker claims: queued organisation jobs remain
// pending until the server is configured with a valid provider key.
func newOrganisationWorker(database *store.Store, cfg jev.Config, httpClient *http.Client) (*jobs.Worker, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	client, err := jev.NewFromEnvironment(cfg, httpClient)
	if err != nil {
		return nil, err
	}
	return &jobs.Worker{
		Queue: database, ID: fmt.Sprintf("organisation-%d", os.Getpid()),
		Handlers:     map[string]jobs.Handler{store.OrganisationJobKind: organisation.Handler(database, client)},
		PollInterval: time.Second, LeaseDuration: time.Minute,
	}, nil
}

type backgroundRunner struct {
	name string
	run  func(context.Context) error
}

type backgroundResult struct {
	name string
	err  error
}

// serveWithBackground gives each configured service the same cancellation
// boundary, and joins every service before run closes the database.
func serveWithBackground(ctx context.Context, server *http.Server, listener net.Listener, runners ...backgroundRunner) error {
	if len(runners) == 0 {
		return serveUntil(ctx, server, listener)
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runnerDone := make(chan backgroundResult, len(runners))
	serverDone := make(chan error, 1)
	for _, runner := range runners {
		go func() { runnerDone <- backgroundResult{name: runner.name, err: runner.run(serviceCtx)} }()
	}
	go func() { serverDone <- serveUntil(serviceCtx, server, listener) }()
	select {
	case result := <-runnerDone:
		unexpectedStop := ctx.Err() == nil && result.err == nil
		cancel()
		serverErr := <-serverDone
		for range len(runners) - 1 {
			other := <-runnerDone
			if result.err == nil && other.err != nil {
				result = other
			}
		}
		if result.err != nil {
			return fmt.Errorf("%s: %w", result.name, result.err)
		}
		if unexpectedStop {
			return fmt.Errorf("%s stopped unexpectedly", result.name)
		}
		return serverErr
	case serverErr := <-serverDone:
		cancel()
		var runnerErr error
		for range runners {
			result := <-runnerDone
			if runnerErr == nil && result.err != nil {
				runnerErr = fmt.Errorf("%s: %w", result.name, result.err)
			}
		}
		if serverErr != nil {
			return serverErr
		}
		return runnerErr
	}
}

func newAPIServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
	}
}

// serveUntil joins graceful shutdown before main closes the database. Serve
// itself stops accepting connections before active handlers have drained.
func serveUntil(ctx context.Context, server *http.Server, listener net.Listener) error {
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			_ = server.Close()
		}
		serveErr := <-serveDone
		if shutdownErr != nil {
			return fmt.Errorf("shutdown: %w", shutdownErr)
		}
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}

func newHandler(database *store.Store, service *auth.Service, options httpapi.Options) http.Handler {
	return httpapi.NewHandler(database, service, options)
}

func privateDataDir() (string, error) {
	if value := os.Getenv("JOBSEEK_DATA_DIR"); value != "" {
		return value, nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user data directory: %w", err)
	}
	return filepath.Join(config, "jobseek-dashboard", "data"), nil
}

func handlerOptions(addr, publicOrigin string) (httpapi.Options, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return httpapi.Options{}, fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || ip != nil && ip.IsLoopback()
	if !loopback && publicOrigin == "" {
		return httpapi.Options{}, errors.New("non-loopback listen requires JOBSEEK_PUBLIC_ORIGIN=https://... behind TLS")
	}
	origins := []string{}
	secure := false
	if loopback {
		origins = append(origins, "http://127.0.0.1:5173", "http://"+addr)
	}
	if publicOrigin != "" {
		parsed, err := url.Parse(publicOrigin)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
			(parsed.Scheme != "https" && !(loopback && parsed.Scheme == "http")) {
			return httpapi.Options{}, errors.New("JOBSEEK_PUBLIC_ORIGIN must be an exact HTTPS origin (HTTP only on loopback)")
		}
		origins = append(origins, parsed.Scheme+"://"+parsed.Host)
		secure = parsed.Scheme == "https"
	}
	return httpapi.Options{AllowedOrigins: origins, SecureCookies: secure}, nil
}

func setupAdmin(ctx context.Context, service *auth.Service, input *os.File, prompts io.Writer) error {
	var first, second []byte
	var err error
	if term.IsTerminal(int(input.Fd())) {
		fmt.Fprint(prompts, "New administrator password: ")
		first, err = term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(prompts)
		if err != nil {
			return err
		}
		fmt.Fprint(prompts, "Confirm password: ")
		second, err = term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(prompts)
	} else {
		reader := bufio.NewReader(io.LimitReader(input, 4096))
		first, err = reader.ReadBytes('\n')
		if err != nil {
			return errors.New("read password and confirmation from stdin")
		}
		second, err = reader.ReadBytes('\n')
	}
	defer clearBytes(first)
	defer clearBytes(second)
	if err != nil {
		return err
	}
	first = bytes.TrimRight(first, "\r\n")
	second = bytes.TrimRight(second, "\r\n")
	if len(first) != len(second) || subtle.ConstantTimeCompare(first, second) != 1 {
		return errors.New("password confirmation did not match")
	}
	return service.SetupAdministrator(ctx, first)
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
