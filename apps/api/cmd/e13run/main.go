// Command e13run executes the authorized E13 Shopify preparation and
// nothing else. It refuses to run unless the saved brief matches the
// owner-confirmed values exactly, the career sources and Typst exist,
// a validation turn proves the pinned Standard route with a clean
// tool-free task boundary, and Standard readiness reports ready. One
// bounded preparation, then a printed report; exit status is non-zero
// unless materials reach the prepared state.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const (
	pinnedModel    = "muse-spark-1.3-standard"
	pinnedProvider = "meta"
	// The owner authorized Standard drafting for this role only. The
	// harness pins it; any other opportunity is refused.
	opportunityID = "b8a80e6102af9857415f6f68bb1bd8d6"
	runRefDefault = "e13-shopify-prepare"
)

var careerFiles = []string{"cv-vince-liem.typ", "cv-vince-liem.md", "github-evidence-review.md"}

func confirmedBrief() store.Preferences {
	return store.Preferences{
		PreferredLocation: "Amsterdam, Netherlands", AllowRemote: true, AllowHybrid: true,
		TargetHoursHundredths: 3200, MinMonthlyBaseCents: 450000,
		SalaryCurrency: "EUR", Timezone: "Europe/Amsterdam",
		RoleCriteria: []store.RoleCriterion{
			{ID: "senior-backend-platform", Label: "Senior backend/platform/systems scope",
				Description: "Senior backend, platform or systems engineering work.", Kind: "role", Mode: "require"},
			{ID: "tech-go", Label: "Go", Description: "Go in the working stack.", Kind: "technology", Mode: "prefer"},
			{ID: "tech-rust", Label: "Rust", Description: "Rust in the working stack.", Kind: "technology", Mode: "prefer"},
			{ID: "tech-typescript", Label: "TypeScript", Description: "TypeScript in the working stack.", Kind: "technology", Mode: "prefer"},
			{ID: "tech-linux", Label: "Linux and platforms", Description: "Linux, boot/update lifecycle or platform internals.", Kind: "technology", Mode: "prefer"},
			{ID: "tech-mcp", Label: "MCP and AI tooling", Description: "MCP servers, agent tooling or AI infrastructure.", Kind: "technology", Mode: "prefer"},
			{ID: "tech-python-avoid", Label: "No Python-centric roles", Description: "Exclude roles centered on Python.", Kind: "technology", Mode: "avoid"},
		},
	}
}

func briefEqual(a, b store.Preferences) bool {
	if a.PreferredLocation != b.PreferredLocation || a.AllowRemote != b.AllowRemote ||
		a.AllowHybrid != b.AllowHybrid || a.TargetHoursHundredths != b.TargetHoursHundredths ||
		a.MinMonthlyBaseCents != b.MinMonthlyBaseCents || a.SalaryCurrency != b.SalaryCurrency ||
		a.Timezone != b.Timezone || len(a.RoleCriteria) != len(b.RoleCriteria) {
		return false
	}
	for i := range a.RoleCriteria {
		if a.RoleCriteria[i] != b.RoleCriteria[i] {
			return false
		}
	}
	return true
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

// verifyStandardValidationTrace machine-checks the lane prerequisites from
// a validation-turn trace: pinned Standard route configured, run
// completed, the marker text returned, and no task outside model and
// reminder kinds (a Standard turn must never touch tools).
func verifyStandardValidationTrace(path, marker string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	route, completed, markerSeen := false, false, false
	for _, line := range strings.Split(string(raw), "\n") {
		var entry struct {
			Dir  string `json:"dir"`
			Line string `json:"line"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Dir != "host->client" {
			continue
		}
		var event struct {
			PayloadType string `json:"payload_type"`
			Payload     struct {
				ModelID    string `json:"model_id"`
				ProviderID string `json:"provider_id"`
				Terminal   string `json:"terminal"`
				Text       string `json:"text"`
				Event      struct {
					TaskKind string `json:"task_kind"`
				} `json:"event"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(entry.Line), &event); err != nil {
			continue
		}
		switch event.PayloadType {
		case "run.model.configured":
			if event.Payload.ModelID != pinnedModel || event.Payload.ProviderID != pinnedProvider {
				return fmt.Errorf("validation route %s/%s is not the pinned lane",
					event.Payload.ProviderID, event.Payload.ModelID)
			}
			route = true
		case "run.terminal.completed":
			if event.Payload.Terminal == "completed" {
				completed = true
			}
			if strings.Contains(event.Payload.Text, marker) {
				markerSeen = true
			}
		case "run.output.delta":
			if strings.Contains(event.Payload.Text, marker) {
				markerSeen = true
			}
		case "task.lifecycle.proposed":
			kind := event.Payload.Event.TaskKind
			if kind == "" {
				continue
			}
			if !strings.HasPrefix(kind, "model.") && !strings.HasPrefix(kind, "reminder.") {
				return fmt.Errorf("validation trace contains foreign task %q", kind)
			}
		}
	}
	if !route {
		return errors.New("validation trace has no pinned route proof")
	}
	if !completed {
		return errors.New("validation trace has no completed run")
	}
	if !markerSeen {
		return errors.New("validation trace returned no marker text")
	}
	return nil
}

type echoSink struct{}

func (echoSink) Emit(musecode.Event) {}

func runValidationTurn(ctx context.Context, cliPath, workspace, marker string) (string, error) {
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return "", err
	}
	tracePath := filepath.Join(workspace, "trace.jsonl")
	traceFile, err := os.Create(tracePath)
	if err != nil {
		return "", err
	}
	defer traceFile.Close()
	transport := &musewire.StandardTransport{CLIPath: cliPath, ModelID: pinnedModel,
		ProviderID: pinnedProvider, Trace: traceFile,
		ValidationPrompt: "Reply with exactly: " + marker}
	spec := musecode.SessionSpec{Tier: musecode.TierStandard, Workspace: workspace, Public: false,
		Bounds: musecode.Bounds{MaxWallClock: 3 * time.Minute, MaxModelSteps: 3, MaxToolCalls: 5,
			MaxBytesPerOp: 64 << 10, MaxBytesTotal: 256 << 10}}
	input := musecode.StandardInput{Purpose: "prepare-draft-required-answers",
		BundleRef: "e13-validation", Context: map[string]string{"prompt": "validation"}}
	if err := transport.Run(ctx, spec, input, musecode.Cursor{}, echoSink{}); err != nil {
		return "", fmt.Errorf("validation turn: %w", err)
	}
	return tracePath, nil
}

func run() error {
	runRef := os.Getenv("E13_RUN_REF")
	if runRef == "" {
		runRef = runRefDefault
	}
	requestKey := os.Getenv("E13_REQUEST_KEY")
	if requestKey == "" {
		requestKey = "e13-shopify-prepare-1"
	}
	careerRoot := os.Getenv("JOBSEEK_APPROVED_CAREER_ROOT")
	if careerRoot == "" {
		return errors.New("JOBSEEK_APPROVED_CAREER_ROOT must point at the approved career sources")
	}
	typstPath := os.Getenv("JOBSEEK_TYPST_PATH")
	if typstPath == "" {
		return errors.New("JOBSEEK_TYPST_PATH must point at the Typst binary")
	}
	if os.Getenv(jev.CredentialEnvironmentVariable) == "" {
		return fmt.Errorf("%s is not set; relevance assessment cannot run", jev.CredentialEnvironmentVariable)
	}
	ctx := context.Background()
	dataDir, err := privateDataDir()
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("open private data: %w", err)
	}
	defer database.Close()
	actor := researchwire.OwnerActor()

	fmt.Println("e13: checking brief, career sources and Typst...")
	current, err := database.CurrentPreferences(ctx)
	if err != nil {
		return err
	}
	if current.Version < 2 || !briefEqual(current, confirmedBrief()) {
		return fmt.Errorf("saved brief v%d does not match the owner-confirmed brief; refusing the run", current.Version)
	}
	fmt.Printf("e13: brief v%d read back and matches the confirmed values\n", current.Version)
	if _, _, err := applicationpacks.LoadApprovedCareerSources(careerRoot, careerFiles); err != nil {
		return fmt.Errorf("career sources: %w", err)
	}
	fmt.Printf("e13: career sources present (%d files)\n", len(careerFiles))
	typstCtx, typstCancel := context.WithTimeout(ctx, 30*time.Second)
	defer typstCancel()
	if out, err := exec.CommandContext(typstCtx, typstPath, "--version").Output(); err != nil {
		return fmt.Errorf("typst check: %w", err)
	} else {
		fmt.Printf("e13: typst: %s", out)
	}

	bin := os.Getenv("JOBSEEK_MUSE_BIN")
	if bin == "" {
		bin = "muse"
	}
	facts := musecode.ProbeLocalFacts(bin)
	if facts.CLIPath == "" {
		return errors.New("muse CLI not found")
	}
	fmt.Println("e13: running Standard validation turn...")
	validationTrace, err := runValidationTurn(ctx, facts.CLIPath,
		filepath.Join(dataDir, "muse-runs", runRef+"-validation"), "E13-STANDARD-VALIDATION-OK")
	if err != nil {
		return err
	}
	if err := verifyStandardValidationTrace(validationTrace, "E13-STANDARD-VALIDATION-OK"); err != nil {
		return fmt.Errorf("lane prerequisite failed: %w", err)
	}
	fmt.Println("e13: validation proves pinned Standard route, completion, text return and a tool-free boundary")

	facts.EffectiveModel = pinnedModel
	facts.SubscriptionLaneProved = true
	facts.SessionProtocolProved = true
	facts.WorkspaceIsolatedProved = true
	if status := musecode.Check(musecode.TierStandard, facts); !status.Available {
		return fmt.Errorf("standard unavailable (%s): %s", status.Code, status.Detail)
	}
	fmt.Println("e13: standard readiness reports ready")

	jevConfig := jev.DefaultConfig()
	jevConfig.Enabled = true
	jevClient, err := jev.NewFromEnvironment(jevConfig, nil)
	if err != nil {
		return err
	}
	bounds := musecode.DefaultBounds()
	bounds.MaxWallClock = 10 * time.Minute
	bounds.MaxModelSteps = 10
	bounds.MaxToolCalls = 5
	bounds.MaxBytesPerOp = 1 << 20
	bounds.MaxBytesTotal = 4 << 20
	factory, err := musewire.NewStandardRunnerFactory(musewire.StandardRunnerConfig{
		CLIPath: facts.CLIPath, ModelID: pinnedModel, ProviderID: pinnedProvider,
		Cursors: musewire.StoreCursors{DB: database}, Facts: facts, Bounds: bounds,
		Workspaces: filepath.Join(dataDir, "muse-sessions"),
	})
	if err != nil {
		return err
	}
	service := &materialprep.Service{Store: database,
		Career: func() ([]applicationpacks.Source, []byte, error) {
			return applicationpacks.LoadApprovedCareerSources(careerRoot, careerFiles)
		},
		Draft:     &materialprep.StandardDrafter{Runner: factory},
		Relevance: materialprep.JevRelevance{Evaluator: jevClient},
		Render: applicationpacks.Renderer{TypstPath: typstPath,
			PrivateTempDir: filepath.Join(dataDir, "material-prep-tmp"), Timeout: 10 * time.Second},
	}

	status, err := database.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		return fmt.Errorf("current check: %w", err)
	}
	if status.Status != store.CheckStatusChecked || status.Check == nil {
		return fmt.Errorf("opportunity %s is not checked; refusing the run", opportunityID)
	}
	workflow, err := database.RoleWorkflow(ctx, opportunityID)
	if err != nil {
		return fmt.Errorf("role workflow: %w", err)
	}
	fmt.Printf("e13: pins check=%s set=%.12s workflow=%d\n",
		status.Check.ID, status.Check.QuestionSetSHA256, workflow.Revision)
	view, replayed, err := service.PrepareOpportunityMaterials(ctx, actor, opportunityID,
		requestKey, status.Check.ID, status.Check.QuestionSetSHA256, workflow.Revision)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	fmt.Printf("e13: materials %s (replayed=%v)\n", view.Status, replayed)
	if view.Current != nil {
		fmt.Printf("e13: version=%d pack=%s\n", view.Current.Version, view.Current.PackID)
	}
	if view.Status != store.MaterialStatusPrepared {
		return fmt.Errorf("e13: materials ended %q, want prepared", view.Status)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "e13run: %v\n", err)
		os.Exit(1)
	}
}
