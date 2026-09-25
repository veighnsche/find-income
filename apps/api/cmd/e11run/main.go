// Command e11run executes the authorized E11 first discovery operation and
// nothing else. It refuses to run unless a validation trace proving the
// live route, MCP negotiation and tool boundary is supplied, the saved
// brief matches the owner-confirmed values exactly, Contributor readiness
// reports ready, and Jev credentials are present. One bounded commission,
// then a printed report; exit status is non-zero unless the run completed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const (
	pinnedModel    = "muse-spark-1.3-contributor"
	pinnedProvider = "meta"
	runRefDefault  = "e11-first-discovery"
	jevCallBound   = 8
)

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

func confirmedCriteria() musecode.PublicCriteria {
	return musecode.PublicCriteria{
		RoleKeywords:  []string{"senior software engineer", "backend engineer", "platform engineer"},
		RegionText:    "Amsterdam, Netherlands; remote EU",
		SkillKeywords: []string{"Go", "Rust", "TypeScript", "Linux", "MCP"},
	}
}

func confirmedCatalog(profile int64) store.ReasonCatalogInput {
	positive := []store.ReasonChoice{
		{ID: "senior-scope-fit", Label: "Senior backend/platform scope", Detail: "The listing describes senior backend, platform or systems engineering work."},
		{ID: "location-fit", Label: "Amsterdam or remote-EU", Detail: "The listing offers Amsterdam, Netherlands, or remote work usable from the EU."},
		{ID: "stack-fit", Label: "Preferred stack", Detail: "The listing names Go, Rust, TypeScript, Linux/platform internals, MCP or AI tooling."},
		{ID: "pattern-fit", Label: "Hours and work pattern", Detail: "The listing supports 32-hour weeks with remote or hybrid work."},
	}
	negative := []store.ReasonChoice{
		{ID: "python-centric", Label: "Python-centric role", Detail: "The role centers on Python."},
		{ID: "junior-scope", Label: "Below senior scope", Detail: "The listing targets junior or medior level only."},
		{ID: "onsite-elsewhere", Label: "Onsite outside Amsterdam", Detail: "The role is onsite-only outside Amsterdam with no remote option."},
		{ID: "below-floor", Label: "Below salary floor", Detail: "The stated base is below EUR 4500/month."},
	}
	missing := []store.ReasonChoice{
		{ID: "missing-location", Label: "Location unstated", Detail: "Work location and remote policy are unstated."},
		{ID: "missing-seniority", Label: "Seniority unstated", Detail: "Seniority level is unstated."},
		{ID: "missing-stack", Label: "Stack unstated", Detail: "The working stack is unstated."},
		{ID: "missing-salary-hours", Label: "Salary or hours unstated", Detail: "Base salary or weekly hours are unstated."},
	}
	return store.ReasonCatalogInput{
		ProfileVersion: profile,
		Rubric:         "Senior backend/platform/systems roles in Amsterdam or remote-EU; Go, Rust, TypeScript, Linux or MCP/AI-tooling stack; 32-hour weeks, remote or hybrid, base at or above EUR 4500/month; Python-centric roles excluded.",
		Positive:       positive, Negative: negative, MissingInformation: missing,
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

// verifyValidationTrace machine-checks the P2/P3 prerequisites from a
// validation-turn trace: pinned route configured, run completed, our MCP
// tool succeeded, and no task outside model, reminder and our-MCP kinds.
func verifyValidationTrace(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	route, completed, toolOK := false, false, false
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
				Event      struct {
					TaskKind string `json:"task_kind"`
				} `json:"event"`
				CorrelationFacts struct {
					ToolName string `json:"tool_name"`
					Outcome  string `json:"outcome"`
				} `json:"correlation_facts"`
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
		case "tool.result":
			name := event.Payload.CorrelationFacts.ToolName
			if event.Payload.CorrelationFacts.Outcome == "success" &&
				strings.HasPrefix(name, "mcp__find_income_public__") {
				toolOK = true
			}
		case "task.lifecycle.proposed":
			kind := event.Payload.Event.TaskKind
			if kind == "" {
				continue
			}
			allowed := strings.HasPrefix(kind, "model.") || strings.HasPrefix(kind, "reminder.") ||
				strings.HasPrefix(kind, "tool.mcp__find_income_public__")
			if !allowed {
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
	if !toolOK {
		return errors.New("validation trace has no successful MCP tool result")
	}
	return nil
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

func run() error {
	validationTrace := os.Getenv("E11_VALIDATION_TRACE")
	if validationTrace == "" {
		return errors.New("E11_VALIDATION_TRACE must point at the validation-turn trace")
	}
	runRef := os.Getenv("E11_RUN_REF")
	if runRef == "" {
		runRef = runRefDefault
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

	fmt.Println("e11: verifying validation trace...")
	if err := verifyValidationTrace(validationTrace); err != nil {
		return fmt.Errorf("P2/P3 prerequisite failed: %w", err)
	}
	fmt.Println("e11: validation trace proves pinned route, MCP success and clean task boundary")

	current, err := database.CurrentPreferences(ctx)
	if err != nil {
		return err
	}
	want := confirmedBrief()
	if current.Version == 1 {
		fmt.Println("e11: saving owner-confirmed brief as v2...")
		saved, _, err := database.UpdatePreferences(ctx, 1, want, actor)
		if err != nil {
			return fmt.Errorf("save brief: %w", err)
		}
		current = saved
	}
	if current.Version < 2 || !briefEqual(current, want) {
		return fmt.Errorf("saved brief v%d does not match the owner-confirmed brief; refusing the run", current.Version)
	}
	fmt.Printf("e11: brief v%d read back and matches the confirmed values\n", current.Version)

	fmt.Println("e11: authoring reason catalog (operator, zero model calls)...")
	catalog, err := database.AuthorReasonCatalog(ctx, store.Actor{Kind: "agent", ID: "muse-e11"}, confirmedCatalog(current.Version))
	if err != nil {
		return fmt.Errorf("author catalog: %w", err)
	}
	fmt.Printf("e11: catalog %s rubric %s\n", catalog.CatalogVersion, catalog.RubricVersion)

	bin := os.Getenv("JOBSEEK_MUSE_BIN")
	if bin == "" {
		bin = "muse"
	}
	facts := musecode.ProbeLocalFacts(bin)
	facts.EffectiveModel = pinnedModel
	facts.SubscriptionLaneProved = true
	facts.SessionProtocolProved = true
	facts.WorkspaceIsolatedProved = true
	if status := musecode.Check(musecode.TierContributor, facts); !status.Available {
		return fmt.Errorf("contributor unavailable (%s): %s", status.Code, status.Detail)
	}
	fmt.Println("e11: contributor readiness reports ready")

	if os.Getenv(jev.CredentialEnvironmentVariable) == "" {
		return fmt.Errorf("%s is not set; Jev classification cannot run", jev.CredentialEnvironmentVariable)
	}
	jevConfig := jev.DefaultConfig()
	jevConfig.Enabled = true
	jevClient, err := jev.NewFromEnvironment(jevConfig, nil)
	if err != nil {
		return err
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
		return fmt.Errorf("wire research: %w", err)
	}
	bounds := musecode.DefaultBounds()
	bounds.MaxModelSteps = 100
	runDir := filepath.Join(dataDir, "muse-runs", runRef)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return err
	}
	traceFile, err := os.Create(filepath.Join(runDir, "trace.jsonl"))
	if err != nil {
		return err
	}
	defer traceFile.Close()
	classifier := &musewire.BoundClassifier{
		Inner: musewire.StoreClassifier{DB: database, Assessor: stack.Assessor, Captures: stack.Captures},
		Max:   jevCallBound,
	}
	transport := &musewire.LiveTransport{CLIPath: facts.CLIPath,
		ModelID: pinnedModel, ProviderID: pinnedProvider, Trace: traceFile}
	service, err := musewire.NewService(musewire.Deps{
		Facts: facts, Bounds: bounds, Cursors: musewire.StoreCursors{DB: database},
		DB: database, Actor: actor, Executor: stack.Executor, Captures: stack.Captures,
		Assessor: stack.Assessor, Classifier: classifier,
		Workspaces: filepath.Join(dataDir, "muse-sessions"),
		Transport:  transport,
	})
	if err != nil {
		return err
	}
	transport.Servers = service.ServerForRun
	fmt.Printf("e11: commissioning %s (brief v%d, %s)...\n", runRef, current.Version, catalog.RubricVersion)
	result, err := service.CommissionDiscovery(ctx, runRef, confirmedCriteria(), current.Version, catalog.RubricVersion)
	if err != nil {
		return fmt.Errorf("commission: %w", err)
	}
	if validationCopy, err := os.ReadFile(validationTrace); err == nil {
		_ = os.WriteFile(filepath.Join(runDir, "validation-trace.jsonl"), validationCopy, 0o600)
	}
	fmt.Printf("e11: outcome=%s detail=%s findings=%d jevJudgments=%d gaps=%d trace=%s\n",
		result.Terminal.Outcome, result.Terminal.Detail, len(result.Findings), classifier.Used(), len(result.ClassifyErrors), traceFile.Name())
	for _, finding := range result.Findings {
		url := ""
		if finding.SourceRef != nil {
			url = finding.SourceRef.ObservedURL
		}
		fmt.Printf("e11: finding %s opportunity=%s assessment=%s url=%s reasons=%d\n",
			finding.Group, finding.OpportunityID, finding.AssessmentID, url, len(finding.Reasons))
	}
	for _, gap := range result.ClassifyErrors {
		fmt.Printf("e11: gap %s\n", gap)
	}
	if result.Terminal.Outcome != musecode.OutcomeCompleted {
		return fmt.Errorf("run did not complete: %s", result.Terminal.Detail)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "e11run:", err)
		os.Exit(1)
	}
}
