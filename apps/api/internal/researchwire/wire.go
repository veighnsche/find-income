package researchwire

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/identity"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/recordsave"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchmemory"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// OwnerActor is the single account holder. Research memory is account-scoped
// (T06 contract: calls are account/run-scoped from the active execution
// context): every delegate acts within the owner's account, so all memory
// instances bind this actor while lease owners (attempt ids) and audit
// actors still distinguish workers. This is a binding T23 interpretation —
// without it, executor-side captures would be invisible to tool-side
// lookups and exact-request dedup would split by caller kind.
func OwnerActor() store.Actor { return store.Actor{Kind: "administrator", ID: "owner"} }

// DefaultAgentID is the delegated agent commissioned runs run as. It matches
// the established delegate naming in round scopes.
const DefaultAgentID = "codex-runner"

// Config wires one research stack. ArtifactRoot must be absolute and live
// outside the model-writable workspace and the web root (production: under
// the private data dir). Empty ChromePath/PythonPath fail those executor
// kinds closed honestly; PermitLoopback is test-only and refused in
// production wiring. JevProvider nil means Jev is unavailable and wiring
// fails closed: research tools stay unwired rather than half-built.
type Config struct {
	ArtifactRoot         string
	ScratchRoot          string
	ChromePath           string
	ExpectedChromeSHA256 string
	PythonPath           string
	SandboxBinary        string
	AgentID              string
	PermitLoopback       bool
	JevProvider          jevassess.Provider
}

// Stack is the composed autonomous recruitment backend.
type Stack struct {
	DB         *store.Store
	Artifacts  *researchmemory.ArtifactStore
	Captures   *researchmemory.Captures
	Authority  *rounds.Authority
	Memory     *researchmemory.Memory // owner account scope; factories below share it
	Executor   *researchexecute.Executor
	Assessor   *jevassess.Handler
	Matcher    *identity.Handler
	Journal    *store.RunEventJournal
	Supervisor *rounds.Supervisor
	Toolchain  *codexservice.ResearchToolchain
	Research   httpapi.ResearchService
	AgentID    string
}

// NewMemoryFor binds account memory. The caller actor is accepted for the
// toolchain signature but memory stays owner-scoped per OwnerActor.
func (s *Stack) NewMemoryFor(actor store.Actor) (*researchmemory.Memory, error) {
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return nil, errors.New("researchwire: memory caller actor required")
	}
	return s.Memory, nil
}

// NewSaverFor binds a record saver charging the account authority while
// auditing under the caller's delegate identity.
func (s *Stack) NewSaverFor(actor store.Actor) (researchcontract.RecordSaver, error) {
	return recordsave.NewHandler(s.DB, s.Authority, actor, s.Captures, codexservice.RunBriefs{DB: s.DB})
}

// ExpireLeases runs one idempotent lease-expiry sweep. Production schedules
// it on a ticker; tests call it directly.
func (s *Stack) ExpireLeases(ctx context.Context, limit int) (int, error) {
	return s.Memory.ExpireLeases(ctx, limit)
}

// Wire composes the full stack. The codexservice-bound turn runner,
// conversation control and observer attach later through lane B's lazy
// wiring hook (service construction stays lazy); until then turns degrade
// honestly while dispatch, tools, saves and HTTP run on real backends.
func Wire(db *store.Store, cfg Config) (*Stack, error) {
	if db == nil {
		return nil, errors.New("researchwire: store required")
	}
	if !filepath.IsAbs(cfg.ArtifactRoot) {
		return nil, errors.New("researchwire: absolute artifact root required")
	}
	if cfg.JevProvider == nil {
		return nil, errors.New("researchwire: Jev provider required; research stays unwired without judgments")
	}
	agentID := cfg.AgentID
	if agentID == "" {
		agentID = DefaultAgentID
	}
	owner := OwnerActor()
	auth, err := rounds.NewAuthority(db, owner)
	if err != nil {
		return nil, err
	}
	artifacts, err := researchmemory.OpenArtifactStore(cfg.ArtifactRoot)
	if err != nil {
		return nil, err
	}
	captures, err := researchmemory.NewCaptures(db, artifacts)
	if err != nil {
		return nil, err
	}
	mem, err := researchmemory.NewMemory(db, owner, artifacts, auth, nil)
	if err != nil {
		return nil, err
	}
	scratch := cfg.ScratchRoot
	if scratch == "" {
		scratch = filepath.Join(cfg.ArtifactRoot, "scratch")
	}
	exec, err := researchexecute.NewExecutor(auth, mem, captures, db, researchexecute.Config{
		ChromePath:           cfg.ChromePath,
		ExpectedChromeSHA256: cfg.ExpectedChromeSHA256,
		PythonPath:           cfg.PythonPath,
		SandboxBinary:        cfg.SandboxBinary,
		ScratchRoot:          scratch,
		PermitLoopback:       cfg.PermitLoopback,
	})
	if err != nil {
		return nil, err
	}
	briefs := codexservice.RunBriefs{DB: db}
	assessor := &jevassess.Handler{
		Authority: auth,
		Captures:  captures,
		Briefs:    briefs,
		Provider:  cfg.JevProvider,
		Exchanges: db,
		Sink:      &identity.StoreSink{Store: db},
		// Supersedes stays nil: no D-owned predecessor resolver exists yet
		// (T23 follow-up for lane D; fresh assessments are unaffected).
	}
	matcher := &identity.Handler{Authority: auth, Store: db, Briefs: briefs, Assessor: assessor}
	journal, err := store.NewRunEventJournal(db)
	if err != nil {
		return nil, err
	}
	sup, err := rounds.NewSupervisor(rounds.SupervisorDeps{
		DB: db, Authority: auth, Executor: exec, Journal: journal,
		Config: rounds.SupervisorConfig{MaxConcurrent: 2},
	})
	if err != nil {
		return nil, err
	}
	s := &Stack{
		DB: db, Artifacts: artifacts, Captures: captures, Authority: auth,
		Memory: mem, Executor: exec, Assessor: assessor, Matcher: matcher,
		Journal: journal, Supervisor: sup, AgentID: agentID,
	}
	s.Toolchain = &codexservice.ResearchToolchain{
		NewMemory: s.NewMemoryFor,
		Captures:  captures,
		Authority: auth,
		Assessor:  assessor,
		Matcher:   matcher,
		NewSaver:  s.NewSaverFor,
		Dispatch:  sup.Dispatch,
		NoteSaved: sup.NoteSavedRecords,
		OwnerBrief: codexservice.OwnerBriefFunc(func(ctx context.Context) (codexservice.OwnerBrief, error) {
			return codexservice.CurrentOwnerBrief(ctx, db)
		}),
		Records: codexservice.StoreRecordReader{DB: db},
	}
	adapter, err := researchservice.New(researchservice.Deps{
		DB: db, Supervisor: sup, Journal: journal, Captures: captures, AgentID: agentID,
	})
	if err != nil {
		return nil, err
	}
	s.Research = adapter
	return s, nil
}
