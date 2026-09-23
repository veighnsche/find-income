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
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
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
	return runWithContext(ctx, os.Args[1:])
}

func runWithContext(ctx context.Context, args []string) error {
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
	if len(args) > 0 {
		if len(args) != 1 || args[0] != "setup-admin" {
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
	// Recruitment work requires a commissioned round. Until that boundary exists,
	// queued collector, intake and organisation jobs stay inert even with keys.
	// The controller is available for saved round reads and Stop. No readiness
	// provider is attached until a bounded runtime/collector is implemented.
	options.Rounds = &rounds.Service{Store: database}
	server := newAPIServer(addr, newHandler(database, service, options))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Printf("jobseek API listening on %s", addr)
	if err := serveUntil(ctx, server, listener); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
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
