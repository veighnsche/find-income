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

	"github.com/veighnsche/find-income-dashboard/api/internal/agency"
	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/delivery"
	"github.com/veighnsche/find-income-dashboard/api/internal/deliveryservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
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
	// Constructing these adapters does not connect to a provider or start work.
	// Only owner Start/Resume crosses the commissioned execution boundary.
	runtime := codexservice.NewLazy(ctx, database)
	defer runtime.Close()
	jevConfig := jev.DefaultConfig()
	jevConfig.Enabled = os.Getenv(jev.CredentialEnvironmentVariable) != ""
	jevClient, err := jev.NewFromEnvironment(jevConfig, nil)
	if err != nil {
		return err
	}
	var decisions agency.Decisions
	var packSources agency.PackSourceLoader
	var interviewSources agency.PackSourceLoader
	var interviewFocus agency.InterviewFocusEvaluator
	var prepService *materialprep.Service
	if jevConfig.Enabled {
		decisions = jevservice.Service{Store: database, Client: jevClient}
		options.AnswerMatcher = jevservice.Service{Store: database, Client: jevClient}
		if root := os.Getenv("JOBSEEK_APPROVED_CAREER_ROOT"); root != "" {
			interviewSources = &agency.LocalPackSources{ProjectRoot: root}
			interviewFocus = jevservice.Service{Store: database, Client: jevClient}
			runtime.SetInterviewConfig(codexservice.InterviewRuntimeConfig{ProjectRoot: root})
		}
		if root, typst := os.Getenv("JOBSEEK_APPROVED_CAREER_ROOT"), os.Getenv("JOBSEEK_TYPST_PATH"); root != "" && typst != "" {
			packSources = &agency.LocalPackSources{ProjectRoot: root}
			runtime.SetApplicationPackConfig(codexservice.ApplicationPackRuntimeConfig{ProjectRoot: root, TypstPath: typst, PrivateTempDir: filepath.Join(dataDir, "application-pack-tmp"), RenderTimeout: 20 * time.Second, Relevance: jevservice.Service{Store: database, Client: jevClient}})
			// Grounded preparation: required+unset drafting through one
			// bounded private Standard turn per operation. Without a
			// proved Standard lane the drafter stays nil and drafting
			// roles keep their honest 503 instead of invented drafts.
			var drafter materialprep.Drafter
			museBin := os.Getenv("JOBSEEK_MUSE_BIN")
			if museBin == "" {
				museBin = "muse"
			}
			standardBounds := musecode.DefaultBounds()
			standardBounds.MaxWallClock = 10 * time.Minute
			standardBounds.MaxModelSteps = 10
			standardBounds.MaxToolCalls = 5
			standardBounds.MaxBytesPerOp = 1 << 20
			standardBounds.MaxBytesTotal = 4 << 20
			if factory, err := musewire.NewStandardRunnerFactory(musewire.StandardRunnerConfig{
				CLIPath: museBin, ModelID: "muse-spark-1.3", ProviderID: "meta",
				Cursors: musewire.StoreCursors{DB: database},
				Facts:   musecode.ProbeLocalFacts(museBin), Bounds: standardBounds,
				Workspaces: filepath.Join(dataDir, "muse-sessions"),
			}); err != nil {
				log.Printf("standard drafting unavailable: %v", err)
			} else {
				drafter = &materialprep.StandardDrafter{Runner: factory}
			}
			materials := &materialprep.Service{Store: database,
				Career: func() ([]applicationpacks.Source, []byte, error) {
					return applicationpacks.LoadApprovedCareerSources(root, []string{"cv-vince-liem.typ", "cv-vince-liem.md", "github-evidence-review.md"})
				},
				Draft:     drafter,
				Relevance: materialprep.JevRelevance{Evaluator: jevClient},
				Render: applicationpacks.Renderer{TypstPath: typst,
					PrivateTempDir: filepath.Join(dataDir, "material-prep-tmp"), Timeout: 10 * time.Second},
			}
			options.Materials = materials
			prepService = materials
		}
	}
	worker := &agency.Engine{Store: database, Runtime: runtime, Decisions: decisions, PackSources: packSources, InterviewSources: interviewSources, InterviewFocus: interviewFocus, Context: ctx}
	if tradeoffs, ok := decisions.(jevservice.Service); ok {
		worker.Tradeoffs = tradeoffs
	}
	if replyIntent, ok := decisions.(jevservice.Service); ok {
		worker.ReplyIntent = replyIntent
	}
	options.Codex = runtime
	options.Rounds = &rounds.Service{Store: database, Readiness: worker, Canceller: runtime, Reconciler: runtime, Worker: worker}
	if stack := wireResearch(database, runtime, &options, dataDir, jevClient, jevConfig.Enabled); stack != nil && prepService != nil {
		// Discovery-saved roles carry no opportunity text; the capture
		// reader lets preparation describe the role from the verified
		// vacancy bytes the check read. It opens stored bytes only.
		prepService.Captures = stack.Captures
	}
	options.Delivery = &deliveryservice.Service{Store: database, Advisor: worker, From: os.Getenv("JOBSEEK_SMTP_FROM")}
	if address := os.Getenv("JOBSEEK_SMTP_ADDRESS"); address != "" && os.Getenv("JOBSEEK_SMTP_FROM") != "" &&
		os.Getenv("JOBSEEK_SMTP_SERVER_NAME") != "" && os.Getenv("JOBSEEK_SMTP_HELLO_NAME") != "" &&
		os.Getenv("JOBSEEK_SMTP_USERNAME") != "" && os.Getenv("JOBSEEK_SMTP_PASSWORD") != "" {
		sender, err := delivery.NewSMTP(delivery.SMTPConfig{Address: address, ServerName: os.Getenv("JOBSEEK_SMTP_SERVER_NAME"),
			HelloName: os.Getenv("JOBSEEK_SMTP_HELLO_NAME"), Username: os.Getenv("JOBSEEK_SMTP_USERNAME"),
			Password: os.Getenv("JOBSEEK_SMTP_PASSWORD"), Timeout: 45 * time.Second})
		if err != nil {
			return fmt.Errorf("configure delivery sender: %w", err)
		}
		options.Delivery.Sender = sender
	}
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
		server.SetKeepAlivesEnabled(false)
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

// wireResearch composes the autonomous recruitment backend (T23) into the
// HTTP API and the lazy codex runtime. Degradation is honest: without Jev or
// when wiring fails, research stays unavailable (HTTP 503, readiness gate
// closed) instead of half-built. Executor binary paths are environment-driven
// with no machine defaults; empty paths fail those kinds closed at dispatch.
// PermitLoopback is test-only and never set here.
func wireResearch(database *store.Store, runtime *codexservice.Lazy, options *httpapi.Options, dataDir string, jevClient *jev.Client, jevEnabled bool) *researchwire.Stack {
	if !jevEnabled {
		log.Print("research unavailable: TYPESAFE_API_KEY is not set")
		return nil
	}
	artifactRoot := os.Getenv("JOBSEEK_ARTIFACT_ROOT")
	if artifactRoot == "" {
		artifactRoot = filepath.Join(dataDir, "research-artifacts")
	}
	stack, err := researchwire.Wire(database, researchwire.Config{
		ArtifactRoot:         artifactRoot,
		ScratchRoot:          os.Getenv("JOBSEEK_RESEARCH_SCRATCH_ROOT"),
		ChromePath:           os.Getenv("JOBSEEK_RESEARCH_CHROME_PATH"),
		ExpectedChromeSHA256: os.Getenv("JOBSEEK_RESEARCH_CHROME_SHA256"),
		PythonPath:           os.Getenv("JOBSEEK_RESEARCH_PYTHON_PATH"),
		SandboxBinary:        os.Getenv("JOBSEEK_RESEARCH_SANDBOX_BINARY"),
		JevProvider:          jevClient,
	})
	if err != nil {
		log.Printf("research unavailable: %v", err)
		return nil
	}
	options.Research = stack.Research
	options.ResearchControl = stack.Supervisor
	runtime.SetResearchWiring(stack.Toolchain, stack.Supervisor)
	go sweepResearchLeases(stack)
	log.Printf("research wired: artifacts=%s agent=%s", artifactRoot, stack.AgentID)
	wireMuse(database, options, dataDir, stack)
	return stack
}

// wireMuse composes the discovery slice behind the research stack: session
// supervision, public tools and Jev classification with durable run records.
// The session transport stays provider-disabled until the E11-authorized live
// run, so commissions fail closed while readiness and run reads stay honest.
func wireMuse(database *store.Store, options *httpapi.Options, dataDir string, stack *researchwire.Stack) {
	bin := os.Getenv("JOBSEEK_MUSE_BIN")
	if bin == "" {
		bin = "muse"
	}
	service, err := musewire.NewService(musewire.Deps{
		Facts: musecode.ProbeLocalFacts(bin), Bounds: musecode.DefaultBounds(),
		Transport: musecode.UnavailableTransport{}, Cursors: musewire.StoreCursors{DB: database},
		DB: database, Actor: researchwire.OwnerActor(),
		Executor: stack.Executor, Captures: stack.Captures, Assessor: stack.Assessor,
		Workspaces: filepath.Join(dataDir, "muse-sessions"),
	})
	if err != nil {
		log.Printf("muse unavailable: %v", err)
		return
	}
	options.Muse = service
	// The deterministic check performer stays unauthorized until the E12
	// live authorization; checks remain pending through the existing path.
	checker, err := musewire.NewChecker(musewire.CheckDeps{
		DB: database, Actor: researchwire.OwnerActor(),
		Executor: stack.Executor, Captures: stack.Captures,
		Bounds: musecode.DefaultBounds(), Authorized: false,
	})
	if err != nil {
		log.Printf("muse check unavailable: %v", err)
	} else {
		options.MuseCheck = checker
	}
	log.Printf("muse wired: cli=%s", bin)
}

func sweepResearchLeases(stack *researchwire.Stack) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if _, err := stack.ExpireLeases(ctx, 100); err != nil {
			log.Printf("research lease sweep: %v", err)
		}
		cancel()
	}
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
