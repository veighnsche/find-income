// Command e12run executes authorized E12 selected-role checks and nothing
// else. Opportunity IDs arrive explicitly through E12_OPPORTUNITIES (comma
// separated); running this command with those IDs is the deliberate live
// act. Retrieval is deterministic and bounded; no model or Jev call is
// made. Exit status is non-zero unless every check persisted a body.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchwire"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

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
	raw := os.Getenv("E12_OPPORTUNITIES")
	if raw == "" {
		return fmt.Errorf("E12_OPPORTUNITIES must list the selected opportunity IDs")
	}
	var opportunities []string
	for _, id := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			opportunities = append(opportunities, trimmed)
		}
	}
	if len(opportunities) == 0 {
		return fmt.Errorf("E12_OPPORTUNITIES lists no opportunity IDs")
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

	if os.Getenv(jev.CredentialEnvironmentVariable) == "" {
		return fmt.Errorf("%s is not set; research stays unwired", jev.CredentialEnvironmentVariable)
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
	checker, err := musewire.NewChecker(musewire.CheckDeps{
		DB: database, Actor: actor, Executor: stack.Executor, Captures: stack.Captures,
		Bounds: musecode.DefaultBounds(), Authorized: true,
	})
	if err != nil {
		return err
	}
	failed := false
	for _, opportunityID := range opportunities {
		opportunity, err := database.Opportunity(ctx, opportunityID)
		if err != nil {
			fmt.Printf("e12: %s: %v\n", opportunityID, err)
			failed = true
			continue
		}
		// Record the owner's explicit role selection, then walk the
		// server-owned stages around the check.
		decision, err := database.OwnerOpportunityDecision(ctx, opportunityID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			fmt.Printf("e12: %s: %v\n", opportunityID, err)
			failed = true
			continue
		}
		if errors.Is(err, store.ErrNotFound) || decision.Decision != "selected" ||
			decision.OpportunityRevision != opportunity.Revision {
			expectedDecision := int64(0)
			if err == nil {
				expectedDecision = decision.Revision
			}
			if _, _, err := database.SetOwnerOpportunityDecision(ctx, actor, opportunityID, store.OwnerDecisionInput{
				RequestKey:                  fmt.Sprintf("e12-select:%s:%d", opportunityID, time.Now().UnixNano()),
				ExpectedOpportunityRevision: opportunity.Revision,
				ExpectedDecisionRevision:    expectedDecision, Decision: "selected",
			}); err != nil {
				fmt.Printf("e12: %s: record selection: %v\n", opportunityID, err)
				failed = true
				continue
			}
		}
		workflow, err := database.RoleWorkflow(ctx, opportunityID)
		if err != nil {
			fmt.Printf("e12: %s: %v\n", opportunityID, err)
			failed = true
			continue
		}
		// A stage left at checking with no pending check (an
		// interrupted attempt) retreats to blocked so a fresh start
		// may proceed; the completing save advances stages itself.
		if workflow.Stage == store.RoleStageChecking {
			if workflow, err = database.AdvanceRoleWorkflow(ctx, opportunityID, workflow.Revision, store.RoleStageBlocked, "e12: check never started"); err != nil {
				fmt.Printf("e12: %s: retreat to blocked: %v\n", opportunityID, err)
				failed = true
				continue
			}
		}
		started, _, err := database.StartJobCheck(ctx, actor, opportunityID, store.CheckStartInput{
			RequestKey:                  fmt.Sprintf("e12-start:%s:%d", opportunityID, time.Now().UnixNano()),
			ExpectedOpportunityRevision: opportunity.Revision,
			ExpectedWorkflowRevision:    workflow.Revision,
		})
		if err != nil {
			fmt.Printf("e12: %s: start check: %v\n", opportunityID, err)
			failed = true
			continue
		}
		fmt.Printf("e12: checking %s (%s) as %s...\n", opportunity.Title, opportunityID, started.ID)
		view, err := checker.PerformCheck(ctx, opportunityID, started.ID)
		if err != nil {
			fmt.Printf("e12: %s: %v\n", opportunityID, err)
			failed = true
			continue
		}
		if view.BlockedReason != nil {
			fmt.Printf("e12: %s blocked %s: %s\n", opportunityID, view.BlockedReason.Code, view.BlockedReason.Detail)
			continue
		}
		fmt.Printf("e12: %s complete: %d questions, captures=%d\n",
			opportunityID, len(view.Questions), len(view.Vacancy.CaptureIDs))
		for _, question := range view.Questions {
			fmt.Printf("e12: question required=%s span=%s[%d:%d] %q\n",
				question.Required, question.SourceSpan.CaptureID,
				question.SourceSpan.Start, question.SourceSpan.End, question.Text)
		}
	}
	if failed {
		return fmt.Errorf("one or more checks failed")
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "e12run:", err)
		os.Exit(1)
	}
}
