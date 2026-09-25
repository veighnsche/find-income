package musewire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// StandardRunnerConfig builds StandardRunnerFactory. Facts must carry the
// pinned effective model and proved lane; Workspaces roots the session
// dirs (standard runs land in standard/<runRef>, disjoint from the
// contributor tree). Bounds should narrow musecode.DefaultBounds to one
// short drafting turn.
type StandardRunnerConfig struct {
	CLIPath    string
	ModelID    string
	ProviderID string
	Provider   string
	Cursors    musecode.CursorStore
	Validate   musecode.ValidateSaveFunc
	Facts      musecode.Facts
	Bounds     musecode.Bounds
	Workspaces string
}

// StandardRunnerFactory is the long-lived materialprep.StandardRunner for
// server wiring: every RunStandard call builds one fresh single-use
// SupervisorStandardRunner with a unique run ref and private workspace,
// so server lifetime never collides with single-use runs.
type StandardRunnerFactory struct {
	config StandardRunnerConfig
}

var _ materialprep.StandardRunner = (*StandardRunnerFactory)(nil)

// NewStandardRunnerFactory gates on Standard readiness without a model
// call: an unproved lane fails here so callers keep their honest
// unavailable state instead of a runner that could never run.
func NewStandardRunnerFactory(config StandardRunnerConfig) (*StandardRunnerFactory, error) {
	if config.CLIPath == "" || config.ModelID == "" || config.ProviderID == "" {
		return nil, errors.New("musewire: standard factory needs CLI path, model and provider")
	}
	if config.Cursors == nil {
		return nil, errors.New("musewire: standard factory needs cursors")
	}
	if config.Workspaces == "" || !filepath.IsAbs(config.Workspaces) {
		return nil, errors.New("musewire: standard factory needs an absolute workspaces root")
	}
	if status := musecode.Check(musecode.TierStandard, config.Facts); !status.Available {
		return nil, fmt.Errorf("musewire: standard unavailable (%s): %s", status.Code, status.Detail)
	}
	if config.Validate == nil {
		config.Validate = func(ref string) error {
			return fmt.Errorf("musewire: standard runs save nothing, rejecting %q", ref)
		}
	}
	return &StandardRunnerFactory{config: config}, nil
}

// RunStandard admits one bounded Standard turn under a fresh run ref and
// returns its collected model texts. Exactly one supervisor run backs
// each call; failures fail closed with no retry.
func (f *StandardRunnerFactory) RunStandard(ctx context.Context, input musecode.StandardInput) (materialprep.StandardResult, error) {
	runRef, err := freshStandardRunRef()
	if err != nil {
		return materialprep.StandardResult{}, err
	}
	workspace := filepath.Join(f.config.Workspaces, "standard", runRef)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return materialprep.StandardResult{}, err
	}
	status := musecode.Check(musecode.TierStandard, f.config.Facts)
	spec, err := musecode.NewSession(status, workspace, f.config.Bounds,
		[]string{filepath.Join(f.config.Workspaces, "contributor")})
	if err != nil {
		return materialprep.StandardResult{}, err
	}
	traceFile, err := os.Create(filepath.Join(workspace, "trace.jsonl"))
	if err != nil {
		return materialprep.StandardResult{}, err
	}
	defer traceFile.Close()
	transport := &StandardTransport{CLIPath: f.config.CLIPath, ModelID: f.config.ModelID,
		ProviderID: f.config.ProviderID, Provider: f.config.Provider, Trace: traceFile}
	runner, err := NewSupervisorStandardRunner(transport, f.config.Cursors,
		f.config.Validate, spec, f.config.Facts, runRef)
	if err != nil {
		return materialprep.StandardResult{}, err
	}
	return runner.RunStandard(ctx, input)
}

func freshStandardRunRef() (string, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return fmt.Sprintf("prepare-%d-%s", time.Now().Unix(), hex.EncodeToString(nonce)), nil
}
