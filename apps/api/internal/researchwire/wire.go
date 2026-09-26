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
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
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
// MuseBin selects the discovery CLI (default "muse"); MuseWorkspaces
// roots discovery session dirs (default ArtifactRoot/muse-sessions).
// MuseTransport and MuseFacts override the live transport and live
// admission facts; tests set both, production leaves both unset.
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
	MuseBin              string
	MuseWorkspaces       string
	MuseTransport        musecode.Transport
	MuseFacts            *musecode.Facts
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
	Muse       *musewire.Service
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
		Reuse: func(ctx context.Context, runID, reuseKey string) (store.DynamicAssessment, error) {
			var row store.DynamicAssessment
			err := db.Read(ctx, func(r store.Reader) error {
				actor, err := store.ResearchRoundActor(ctx, r, runID)
				if err != nil {
					return err
				}
				stored, err := store.GetDynamicAssessmentByReuseKey(ctx, r, actor.Kind, actor.ID, reuseKey)
				if err != nil {
					return err
				}
				row = stored
				return nil
			})
			return row, err
		},
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
	museSvc, err := wireMuseDiscovery(db, owner, cfg, exec, captures, assessor, sup)
	if err != nil {
		return nil, err
	}
	s.Muse = museSvc
	adapter, err := researchservice.New(researchservice.Deps{
		DB: db, Supervisor: sup, Journal: journal, Captures: captures, AgentID: agentID,
		Muse: museSvc,
	})
	if err != nil {
		return nil, err
	}
	s.Research = adapter
	return s, nil
}

// wireMuseDiscovery composes the discovery slice behind the research
// stack: the live Contributor exec transport, session supervision,
// public tools and Jev classification with durable run records. A
// missing CLI or missing owner credentials does not fail wiring:
// admission facts stay unready and commissions fail closed while
// readiness and run reads stay honest.
func wireMuseDiscovery(db *store.Store, owner store.Actor, cfg Config, exec *researchexecute.Executor, captures *researchmemory.Captures, assessor *jevassess.Handler, saved musewire.SavedRecorder) (*musewire.Service, error) {
	bin := strings.TrimSpace(cfg.MuseBin)
	if bin == "" {
		bin = "muse"
	}
	workspaces := cfg.MuseWorkspaces
	if workspaces == "" {
		workspaces = filepath.Join(cfg.ArtifactRoot, "muse-sessions")
	}
	facts := musewire.LiveFacts(bin, musecode.PinnedModelID)
	if cfg.MuseFacts != nil {
		facts = *cfg.MuseFacts
	}
	var transport musecode.Transport = &musewire.LiveTransport{CLIPath: bin,
		ModelID: musecode.PinnedModelID, ProviderID: musecode.PinnedProviderID}
	if cfg.MuseTransport != nil {
		transport = cfg.MuseTransport
	}
	service, err := musewire.NewService(musewire.Deps{
		Facts: facts, Bounds: musecode.DefaultBounds(),
		Transport: transport, Cursors: musewire.StoreCursors{DB: db},
		DB: db, Actor: owner,
		Executor: exec, Captures: captures, Assessor: assessor,
		Saved: saved, Workspaces: workspaces,
	})
	if err != nil {
		return nil, err
	}
	if live, ok := transport.(*musewire.LiveTransport); ok {
		live.Servers = service.ServerForRun
	}
	return service, nil
}
