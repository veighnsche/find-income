package musewire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// CommissionIntent scopes Muse discovery rounds for audit.
const CommissionIntent = "muse_contributor_discovery"

// ErrUnknownRun reports a run ref with no terminal row or cursor.
var ErrUnknownRun = errors.New("musewire: unknown run")

// ErrRunInProgress reports a same-key commission for a run that is still
// conducting. The admission already happened; poll the run instead of
// recommissioning.
var ErrRunInProgress = errors.New("musewire: run already in progress")

// Deps wires one discovery service. Production passes the live exec
// transport; unready facts or a disabled transport fail commissions
// closed at admission while reads stay honest. Saved journals saved
// records and merges them into the run checkpoint (nil skips: fixture
// services without a run supervisor); production passes the supervisor
// so run views and reports list classified opportunities.
// SavedRecorder journals saved records and merges them into the run
// checkpoint. *rounds.Supervisor satisfies it.
type SavedRecorder interface {
	NoteSavedRecords(ctx context.Context, runID, batchKey string, recordIDs []string) error
}

type Deps struct {
	Facts      musecode.Facts
	Bounds     musecode.Bounds
	Transport  musecode.Transport
	Cursors    musecode.CursorStore
	DB         *store.Store
	Actor      store.Actor
	Executor   researchcontract.Executor
	Captures   researchcontract.CaptureReader
	Assessor   researchcontract.Assessor
	Classifier Classifier
	Saved      SavedRecorder
	Workspaces string
}

// Service composes supervision, public tools, Jev classification and
// durable run records for the discovery slice.
type Service struct {
	facts      musecode.Facts
	bounds     musecode.Bounds
	transport  musecode.Transport
	cursors    musecode.CursorStore
	db         *store.Store
	actor      store.Actor
	executor   researchcontract.Executor
	captures   researchcontract.CaptureReader
	assessor   researchcontract.Assessor
	classifier Classifier
	saved      SavedRecorder
	workspaces string

	mu           sync.Mutex
	runs         map[string]*retainedRun
	commissioned int64
}

type retainedRun struct {
	roundID    string
	supervisor *musecode.Supervisor
	server     *publicresearch.Server
	terminal   musecode.TerminalResult
}

// NewService validates one composition. Missing backends fail here so the
// HTTP layer reports unavailable instead of half-built.
func NewService(deps Deps) (*Service, error) {
	if err := deps.Bounds.Validate(); err != nil {
		return nil, err
	}
	if deps.Transport == nil || deps.Cursors == nil || deps.DB == nil ||
		deps.Executor == nil || deps.Captures == nil || deps.Assessor == nil {
		return nil, errors.New("musewire: transport, cursors, store, executor, captures and assessor required")
	}
	if deps.Actor.Kind == "" || deps.Actor.ID == "" {
		return nil, errors.New("musewire: round actor required")
	}
	if deps.Workspaces == "" || !filepath.IsAbs(deps.Workspaces) {
		return nil, errors.New("musewire: absolute session workspace root required")
	}
	classifier := deps.Classifier
	if classifier == nil {
		classifier = StoreClassifier{DB: deps.DB, Assessor: deps.Assessor, Captures: deps.Captures}
	}
	return &Service{
		facts: deps.Facts, bounds: deps.Bounds, transport: deps.Transport,
		cursors: deps.Cursors, db: deps.DB, actor: deps.Actor,
		executor: deps.Executor, captures: deps.Captures, assessor: deps.Assessor,
		classifier: classifier, saved: deps.Saved,
		workspaces: deps.Workspaces, runs: map[string]*retainedRun{},
	}, nil
}

// Readiness reports the frozen check verdict without admitting session input.
// The discovery transport is a Contributor dependency; Standard preparation
// uses its own runner and must not inherit this transport's disabled state.
func (s *Service) Readiness(tier musecode.Tier) musecode.Status {
	status := musecode.Check(tier, s.facts)
	if tier != musecode.TierContributor || !status.Available {
		return status
	}
	switch s.transport.(type) {
	case musecode.UnavailableTransport, *musecode.UnavailableTransport:
		return musecode.Status{Tier: tier, Code: musecode.CodeProtocolUnverified,
			Detail: "Muse Contributor session transport is disabled."}
	}
	return status
}

// Commissions counts admitted discovery runs. Reads never increment it.
func (s *Service) Commissions() int64 {
	return atomic.LoadInt64(&s.commissioned)
}

// DiscoveryCeiling reports the service bounds ceiling. Per-run bounds can
// only narrow it, never widen it.
func (s *Service) DiscoveryCeiling() musecode.Bounds {
	return s.bounds
}

// CommissionedRunRef resolves the discovery run ref behind a muse round:
// muse rounds carry the CommissionIntent and a "muse:"+runRef request key.
func CommissionedRunRef(round store.Round) (string, bool) {
	if round.Intent != CommissionIntent {
		return "", false
	}
	ref, ok := strings.CutPrefix(round.RequestKey, "muse:")
	if !ok || !validRunRef(ref) {
		return "", false
	}
	return ref, true
}

// ServerForRun returns the live public tool server of a retained run so the
// session host can connect its MCP tools while the session runs.
func (s *Service) ServerForRun(runRef string) (*publicresearch.Server, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	retained, ok := s.runs[runRef]
	if !ok || retained.server == nil {
		return nil, false
	}
	return retained.server, true
}

// Stop fences a retained run. Unknown runs report false.
func (s *Service) Stop(runRef, reason string) bool {
	s.mu.Lock()
	retained, ok := s.runs[runRef]
	s.mu.Unlock()
	if !ok {
		return false
	}
	retained.supervisor.Stop(runRef, reason)
	return true
}

// CommissionResult is the durable outcome of one discovery commission.
type CommissionResult struct {
	Admission      musecode.Admission
	Terminal       musecode.TerminalResult
	RoundID        string
	Findings       []store.Finding
	ClassifyErrors []string
}

func validRunRef(runRef string) bool {
	return runRef != "" && len(runRef) <= 100 && !strings.Contains(runRef, "/") &&
		!strings.Contains(runRef, "\\") && !strings.Contains(runRef, "..")
}

// AsyncAdmission acknowledges an admitted discovery run that conducts in
// the background. Created=false replays the existing run's identity:
// the caller polls the run instead of conducting again.
type AsyncAdmission struct {
	RunRef  string
	RoundID string
	Created bool
}

// conductSetup carries one admitted run from admission to conduction.
type conductSetup struct {
	spec       musecode.SessionSpec
	input      musecode.PublicInput
	supervisor *musecode.Supervisor
	server     *publicresearch.Server
	round      store.Round
}

// runBounds resolves per-run bounds: zero selects the service ceiling,
// otherwise each dimension clamps down to the ceiling and the result
// must validate. Bounds can only narrow, never widen.
func (s *Service) runBounds(bounds musecode.Bounds) (musecode.Bounds, error) {
	if bounds == (musecode.Bounds{}) {
		return s.bounds, nil
	}
	eff := s.bounds
	if bounds.MaxWallClock > 0 && bounds.MaxWallClock < eff.MaxWallClock {
		eff.MaxWallClock = bounds.MaxWallClock
	}
	if bounds.MaxModelSteps > 0 && bounds.MaxModelSteps < eff.MaxModelSteps {
		eff.MaxModelSteps = bounds.MaxModelSteps
	}
	if bounds.MaxToolCalls > 0 && bounds.MaxToolCalls < eff.MaxToolCalls {
		eff.MaxToolCalls = bounds.MaxToolCalls
	}
	if bounds.MaxBytesPerOp > 0 && bounds.MaxBytesPerOp < eff.MaxBytesPerOp {
		eff.MaxBytesPerOp = bounds.MaxBytesPerOp
	}
	if bounds.MaxBytesTotal > 0 && bounds.MaxBytesTotal < eff.MaxBytesTotal {
		eff.MaxBytesTotal = bounds.MaxBytesTotal
	}
	if err := eff.Validate(); err != nil {
		return musecode.Bounds{}, err
	}
	return eff, nil
}

// admitDiscovery runs the synchronous admission preamble shared by sync and
// async commissions: availability, workspace, session, round, checkpoint,
// tools and retained registration. A same-key replay returns its stored
// result instead of a setup; callers must not conduct twice.
func (s *Service) admitDiscovery(ctx context.Context, runRef string, criteria musecode.PublicCriteria, profileVersion int64, rubricVersion string, bounds musecode.Bounds) (*conductSetup, *CommissionResult, error) {
	if !validRunRef(runRef) {
		return nil, nil, fmt.Errorf("%w: invalid run ref", ErrUnknownRun)
	}
	status := musecode.Check(musecode.TierContributor, s.facts)
	if !status.Available {
		return nil, nil, fmt.Errorf("musewire: contributor unavailable (%s): %s", status.Code, status.Detail)
	}
	workspace := filepath.Join(s.workspaces, "contributor", runRef)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, nil, err
	}
	spec, err := musecode.NewSession(status, workspace, bounds, nil)
	if err != nil {
		return nil, nil, err
	}
	// Lookup-first replay: the round digest covers the deadline, so a second
	// StartRound call can never replay. The guard below keeps the
	// same-key-different-input conflict honest instead.
	if existing, err := s.db.RoundByRequest(ctx, s.actor, "muse:"+runRef); err == nil {
		replayed, err := s.replayGuarded(ctx, runRef, existing, profileVersion, rubricVersion)
		if err != nil {
			return nil, nil, err
		}
		return nil, &replayed, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("musewire: lookup round: %w", err)
	}
	round, created, err := s.db.StartRound(ctx, s.actor, store.StartRoundInput{
		// Outcome research_run routes owner stop/resume to the research
		// run control; muse rounds are research runs.
		RequestKey: "muse:" + runRef, Intent: CommissionIntent, Outcome: "research_run",
		ProfileVersion: profileVersion,
		Scope: store.RoundScope{
			// research.search/fetch/api let the production executor
			// dispatch the run's public retrieval; browse and exec
			// stay out (no pinned browser; model-executed code is
			// outside the discovery boundary).
			Operations: []string{"muse_discover", "muse_classify", store.RoundJevRequest, store.RoundJevAssess,
				store.RoundResearchSearch, store.RoundResearchFetch, store.RoundResearchAPI},
			Resources: []string{store.ResearchAuthorityResource},
		},
		Limits: store.RoundAllowance{
			Requests: int64(bounds.MaxToolCalls), Items: 1000,
			Tools: int64(bounds.MaxToolCalls), Turns: int64(bounds.MaxModelSteps),
		},
		Deadline: time.Now().Add(bounds.MaxWallClock),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("musewire: start round: %w", err)
	}
	if !created {
		replayed, err := s.replay(ctx, runRef)
		if err != nil {
			return nil, nil, err
		}
		return nil, &replayed, nil
	}
	if _, err := s.db.ActivateRound(ctx, s.actor, round.ID); err != nil {
		return nil, nil, fmt.Errorf("musewire: activate round: %w", err)
	}
	if err := s.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SaveRunCheckpoint(ctx, db, round.ID, researchcontract.Checkpoint{
			ProfileVersion: profileVersion, RubricVersion: rubricVersion, Generation: 1,
		})
	}); err != nil {
		return nil, nil, fmt.Errorf("musewire: checkpoint brief: %w", err)
	}
	server, err := publicresearch.NewServer(publicresearch.Deps{
		Executor: s.executor, Captures: s.captures, Bounds: bounds,
		RunID: round.ID, Generation: 1,
	})
	if err != nil {
		return nil, nil, err
	}
	validate := func(ref string) error {
		if _, ok := server.Vacancy(ref); ok {
			return nil
		}
		if _, ok := server.Question(ref); ok {
			return nil
		}
		return fmt.Errorf("musewire: unknown save ref %q", ref)
	}
	supervisor := musecode.NewSupervisor(s.transport, s.cursors, validate, s.facts.EffectiveModel)
	s.mu.Lock()
	s.runs[runRef] = &retainedRun{supervisor: supervisor, server: server}
	s.mu.Unlock()
	return &conductSetup{spec: spec, input: musecode.PublicInput{Criteria: criteria},
		supervisor: supervisor, server: server, round: round}, nil, nil
}

// finalizeConducted classifies a conducted run's saves and persists the
// terminal round and report rows.
func (s *Service) finalizeConducted(ctx context.Context, setup *conductSetup, runRef string, profileVersion int64, rubricVersion string, terminal musecode.TerminalResult) ([]store.Finding, []string, error) {
	findings, classifyErrs := s.classifySaved(ctx, setup.round.ID, profileVersion, rubricVersion, setup.server, terminal.SavedRefs)
	terminalState := store.RoundCompleted
	reason := "run completed"
	if terminal.Outcome != musecode.OutcomeCompleted {
		terminalState = store.RoundFailed
		reason = "run " + string(terminal.Outcome) + ": " + terminal.Detail
	}
	if len(reason) > 100 {
		reason = reason[:100]
	}
	summary, err := json.Marshal(map[string]any{"runRef": runRef,
		"outcome": string(terminal.Outcome), "findings": len(findings)})
	if err != nil {
		return nil, nil, err
	}
	deliverable := fmt.Sprintf("report saved, %d findings", len(findings))
	if _, err := s.db.FinishRound(ctx, s.actor, setup.round.ID, terminalState, reason, deliverable, summary); err != nil {
		return nil, nil, fmt.Errorf("musewire: finish round: %w", err)
	}
	// Journal classified opportunities into the run checkpoint so run
	// views and reports list them. A lag here never fails the run: the
	// records are saved, and the gap says the checkpoint lagged.
	if s.saved != nil && len(findings) > 0 {
		ids := make([]string, 0, len(findings))
		for _, finding := range findings {
			if finding.OpportunityID != "" {
				ids = append(ids, finding.OpportunityID)
			}
		}
		if len(ids) > 0 {
			if err := s.saved.NoteSavedRecords(ctx, setup.round.ID, runRef, ids); err != nil {
				classifyErrs = append(classifyErrs, "musewire: note saved records: "+err.Error())
			}
		}
	}
	if err := s.db.SaveMuseRunReport(ctx, store.MuseRunReport{
		RunRef: runRef, RoundID: setup.round.ID, Tier: string(musecode.TierContributor),
		Outcome: string(terminal.Outcome), Detail: terminal.Detail,
		SavedRefs: terminal.SavedRefs, ClassifyErrors: classifyErrs, UpdatedAt: terminal.EndedAt,
	}); err != nil {
		return nil, nil, fmt.Errorf("musewire: persist report: %w", err)
	}
	s.mu.Lock()
	s.runs[runRef].roundID = setup.round.ID
	s.runs[runRef].terminal = terminal
	s.mu.Unlock()
	return findings, classifyErrs, nil
}

// persistConductFailure records a conduct failure as a terminal failed run
// so polling reads the failure instead of hanging on an active round.
// Failures here are best-effort: there is no caller left to report to.
func (s *Service) persistConductFailure(ctx context.Context, runRef, roundID, detail string) {
	terminal := musecode.TerminalResult{RunRef: runRef, Tier: musecode.TierContributor,
		Outcome: musecode.OutcomeFailed, Detail: detail, EndedAt: time.Now()}
	reason := "run failed: " + detail
	if len(reason) > 100 {
		reason = reason[:100]
	}
	summary, _ := json.Marshal(map[string]any{"runRef": runRef, "outcome": "failed", "findings": 0})
	_, _ = s.db.FinishRound(ctx, s.actor, roundID, store.RoundFailed, reason, "run failed", summary)
	_ = s.db.SaveMuseRunReport(ctx, store.MuseRunReport{RunRef: runRef, RoundID: roundID,
		Tier: string(musecode.TierContributor), Outcome: string(musecode.OutcomeFailed),
		Detail: detail, UpdatedAt: terminal.EndedAt})
	s.mu.Lock()
	if retained, ok := s.runs[runRef]; ok {
		retained.roundID = roundID
		retained.terminal = terminal
	}
	s.mu.Unlock()
}

// CommissionDiscovery admits one bounded Contributor discovery run, conducts
// it to a terminal result, classifies saved vacancies and persists the
// terminal row. Same-key replays return the stored result without
// re-conducting; unknown prior state fails closed instead of replaying blind.
func (s *Service) CommissionDiscovery(ctx context.Context, runRef string, criteria musecode.PublicCriteria, profileVersion int64, rubricVersion string) (CommissionResult, error) {
	setup, replayed, err := s.admitDiscovery(ctx, runRef, criteria, profileVersion, rubricVersion, s.bounds)
	if err != nil {
		return CommissionResult{}, err
	}
	if replayed != nil {
		return *replayed, nil
	}
	admission, err := setup.supervisor.StartRun(ctx, runRef, setup.spec, setup.input, s.facts)
	if err != nil {
		s.persistConductFailure(context.WithoutCancel(ctx), runRef, setup.round.ID, err.Error())
		return CommissionResult{}, err
	}
	atomic.AddInt64(&s.commissioned, 1)
	terminal, ok := setup.supervisor.Result(runRef)
	if !ok {
		s.persistConductFailure(context.WithoutCancel(ctx), runRef, setup.round.ID, "admitted run has no terminal result")
		return CommissionResult{}, errors.New("musewire: admitted run has no terminal result")
	}
	findings, classifyErrs, err := s.finalizeConducted(ctx, setup, runRef, profileVersion, rubricVersion, terminal)
	if err != nil {
		return CommissionResult{}, err
	}
	return CommissionResult{Admission: admission, Terminal: terminal, RoundID: setup.round.ID,
		Findings: findings, ClassifyErrors: classifyErrs}, nil
}

// CommissionDiscoveryAsync admits one bounded Contributor discovery run and
// conducts it in the background; the caller polls the run. Zero bounds
// select the service ceiling, otherwise bounds narrow it. Admission-time
// failures (unavailable Contributor, brief conflict, store errors) return
// synchronously; conduct failures persist as a terminal failed run. A
// same-key replay returns the existing run with Created=false.
func (s *Service) CommissionDiscoveryAsync(ctx context.Context, runRef string, criteria musecode.PublicCriteria, profileVersion int64, rubricVersion string, bounds musecode.Bounds) (AsyncAdmission, error) {
	eff, err := s.runBounds(bounds)
	if err != nil {
		return AsyncAdmission{}, err
	}
	setup, replayed, err := s.admitDiscovery(ctx, runRef, criteria, profileVersion, rubricVersion, eff)
	if err != nil {
		if errors.Is(err, ErrRunInProgress) {
			if round, lerr := s.db.RoundByRequest(ctx, s.actor, "muse:"+runRef); lerr == nil {
				return AsyncAdmission{RunRef: runRef, RoundID: round.ID}, nil
			}
		}
		return AsyncAdmission{}, err
	}
	if replayed != nil {
		return AsyncAdmission{RunRef: runRef, RoundID: replayed.RoundID}, nil
	}
	atomic.AddInt64(&s.commissioned, 1)
	go s.conductAsync(context.WithoutCancel(ctx), runRef, setup, profileVersion, rubricVersion)
	return AsyncAdmission{RunRef: runRef, RoundID: setup.round.ID, Created: true}, nil
}

// conductAsync conducts one admitted run to its terminal record in the
// background. The context is detached: cancelling the commissioning
// request never aborts the run; owner stop and session bounds still apply.
func (s *Service) conductAsync(ctx context.Context, runRef string, setup *conductSetup, profileVersion int64, rubricVersion string) {
	_, err := setup.supervisor.StartRun(ctx, runRef, setup.spec, setup.input, s.facts)
	if err != nil {
		s.persistConductFailure(ctx, runRef, setup.round.ID, err.Error())
		return
	}
	terminal, ok := setup.supervisor.Result(runRef)
	if !ok {
		s.persistConductFailure(ctx, runRef, setup.round.ID, "admitted run has no terminal result")
		return
	}
	_, _, _ = s.finalizeConducted(ctx, setup, runRef, profileVersion, rubricVersion, terminal)
}

// replayGuarded replays a same-key commission only when the brief matches;
// a different profile or rubric on the same key conflicts instead of
// silently returning another run's result.
func (s *Service) replayGuarded(ctx context.Context, runRef string, existing store.Round, profileVersion int64, rubricVersion string) (CommissionResult, error) {
	if existing.Intent != CommissionIntent || existing.ProfileVersion != profileVersion {
		return CommissionResult{}, fmt.Errorf("%w: run %q was commissioned against a different brief",
			store.ErrRoundIdempotencyConflict, runRef)
	}
	var checkpoint researchcontract.Checkpoint
	if err := s.db.Read(ctx, func(r store.Reader) error {
		var err error
		checkpoint, err = store.GetRunCheckpoint(ctx, r, existing.ID)
		return err
	}); err != nil || checkpoint.RubricVersion != rubricVersion {
		return CommissionResult{}, fmt.Errorf("%w: run %q was commissioned against a different brief",
			store.ErrRoundIdempotencyConflict, runRef)
	}
	return s.replay(ctx, runRef)
}

// replay returns the stored result of a same-key commission without
// re-conducting. A round with neither terminal row nor cursor fails closed:
// the earlier attempt died mid-flight and must resume explicitly (E11).
func (s *Service) replay(ctx context.Context, runRef string) (CommissionResult, error) {
	s.mu.Lock()
	retained, ok := s.runs[runRef]
	s.mu.Unlock()
	if ok {
		if retained.terminal.RunRef == "" {
			return CommissionResult{}, fmt.Errorf("%w: run %q", ErrRunInProgress, runRef)
		}
		return s.storedResult(ctx, runRef, retained.roundID, retained.terminal)
	}
	row, err := s.db.LoadMuseRunReport(ctx, runRef)
	if err == nil {
		terminal := musecode.TerminalResult{RunRef: row.RunRef, Tier: musecode.Tier(row.Tier),
			Outcome: musecode.Outcome(row.Outcome), Detail: row.Detail,
			SavedRefs: row.SavedRefs, EndedAt: row.UpdatedAt}
		return s.storedResult(ctx, runRef, row.RoundID, terminal)
	}
	if _, err := s.cursors.LoadCursor(ctx, runRef); err == nil {
		return CommissionResult{}, errors.New("musewire: run " + runRef + " was interrupted; resume arrives with the first live run")
	}
	return CommissionResult{}, fmt.Errorf("%w: round exists without run records", ErrUnknownRun)
}

func (s *Service) storedResult(ctx context.Context, runRef, roundID string, terminal musecode.TerminalResult) (CommissionResult, error) {
	findings, err := s.runFindings(ctx, roundID)
	if err != nil {
		return CommissionResult{}, err
	}
	row, err := s.db.LoadMuseRunReport(ctx, runRef)
	if err != nil {
		return CommissionResult{}, err
	}
	return CommissionResult{Terminal: terminal, RoundID: roundID,
		Findings: findings, ClassifyErrors: row.ClassifyErrors}, nil
}

// classifySaved judges every saved vacancy ref. Non-vacancy refs and
// per-vacancy failures become honest gaps, never silent drops.
func (s *Service) classifySaved(ctx context.Context, roundID string, profileVersion int64, rubricVersion string, server *publicresearch.Server, savedRefs []string) ([]store.Finding, []string) {
	findings := []store.Finding{}
	gaps := []string{}
	for _, ref := range savedRefs {
		vacancy, ok := server.Vacancy(ref)
		if !ok {
			gaps = append(gaps, "classify "+ref+": not a saved vacancy at discovery")
			continue
		}
		finding, err := s.classifier.ClassifyVacancy(ctx, VacancyClassification{
			RoundID: roundID, ProfileVersion: profileVersion, RubricVersion: rubricVersion,
			Generation: 1, Actor: s.actor, Vacancy: vacancy,
		})
		if err != nil {
			gaps = append(gaps, "classify "+ref+": "+err.Error())
			continue
		}
		findings = append(findings, finding)
	}
	return findings, gaps
}

// CheckpointView mirrors one durable checkpoint for reads.
type CheckpointView struct {
	RunRef           string
	Tier             musecode.Tier
	SavedCount       int
	LastSavedReceipt string
	SavedRefs        []string
	UpdatedAt        time.Time
}

// Checkpoints serves durable cursor and terminal rows, oldest first. Reads
// commission nothing.
func (s *Service) Checkpoints(ctx context.Context, runRef string) ([]CheckpointView, error) {
	views := []CheckpointView{}
	if cursor, err := s.cursors.LoadCursor(ctx, runRef); err == nil {
		last := ""
		if len(cursor.SavedRefs) > 0 {
			last = cursor.SavedRefs[len(cursor.SavedRefs)-1]
		}
		views = append(views, CheckpointView{RunRef: runRef, Tier: cursor.Tier,
			SavedCount: cursor.SavedCount, LastSavedReceipt: last,
			SavedRefs: cursor.SavedRefs, UpdatedAt: cursor.UpdatedAt})
	}
	if row, err := s.db.LoadMuseRunReport(ctx, runRef); err == nil {
		last := ""
		if len(row.SavedRefs) > 0 {
			last = row.SavedRefs[len(row.SavedRefs)-1]
		}
		views = append(views, CheckpointView{RunRef: runRef, Tier: musecode.Tier(row.Tier),
			SavedCount: len(row.SavedRefs), LastSavedReceipt: last,
			SavedRefs: row.SavedRefs, UpdatedAt: row.UpdatedAt})
	}
	if len(views) == 0 {
		return nil, ErrUnknownRun
	}
	if len(views) == 2 && views[1].UpdatedAt.Before(views[0].UpdatedAt) {
		views[0], views[1] = views[1], views[0]
	}
	return views, nil
}

// ReportView is the honest terminal report for one run.
type ReportView struct {
	RunRef     string
	Tier       musecode.Tier
	Outcome    musecode.Outcome
	Detail     string
	SavedRefs  []string
	Searched   []string
	Reused     []string
	Gaps       []string
	NextAction string
}

// Report serves the durable terminal row plus finding-derived coverage.
// Unknown runs fail with ErrUnknownRun; reads commission nothing.
func (s *Service) Report(ctx context.Context, runRef string) (ReportView, error) {
	row, err := s.db.LoadMuseRunReport(ctx, runRef)
	if err != nil {
		return ReportView{}, ErrUnknownRun
	}
	findings, err := s.runFindings(ctx, row.RoundID)
	if err != nil {
		return ReportView{}, err
	}
	outcome := musecode.Outcome(row.Outcome)
	gaps := []string{}
	if outcome != musecode.OutcomeCompleted {
		gaps = append(gaps, "run "+string(outcome)+": "+row.Detail)
	}
	gaps = append(gaps, row.ClassifyErrors...)
	if outcome == musecode.OutcomeCompleted && len(row.SavedRefs) == 0 {
		gaps = append(gaps, "no vacancy met the save bar during this run")
	}
	return ReportView{
		RunRef: row.RunRef, Tier: musecode.Tier(row.Tier), Outcome: outcome,
		Detail: row.Detail, SavedRefs: row.SavedRefs,
		Searched: distinctSources(findings), Reused: reusedCaptures(findings),
		Gaps: gaps, NextAction: nextActionFor(outcome, len(findings)),
	}, nil
}

func (s *Service) runFindings(ctx context.Context, roundID string) ([]store.Finding, error) {
	findings := []store.Finding{}
	cursor := ""
	for {
		page, err := s.db.ListRunFindings(ctx, roundID, "", cursor, 0)
		if err != nil {
			return nil, err
		}
		findings = append(findings, page.Items...)
		if page.NextCursor == "" {
			return findings, nil
		}
		cursor = page.NextCursor
	}
}

func distinctSources(findings []store.Finding) []string {
	seen := map[string]bool{}
	sources := []string{}
	for _, finding := range findings {
		host := ""
		if finding.SourceRef != nil {
			host = finding.SourceRef.SourceID
		}
		if host == "" && finding.SourceRef != nil {
			if parsed, err := url.Parse(finding.SourceRef.ObservedURL); err == nil {
				host = parsed.Hostname()
			}
		}
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		sources = append(sources, host)
	}
	if sources == nil {
		return []string{}
	}
	return sources
}

func reusedCaptures(findings []store.Finding) []string {
	counts := map[string]int{}
	order := []string{}
	for _, finding := range findings {
		for _, link := range finding.EvidenceLinks {
			if counts[link.CaptureID] == 0 {
				order = append(order, link.CaptureID)
			}
			counts[link.CaptureID]++
		}
	}
	reused := []string{}
	for _, captureID := range order {
		if counts[captureID] > 1 {
			reused = append(reused, captureID)
		}
	}
	return reused
}

func nextActionFor(outcome musecode.Outcome, findings int) string {
	switch {
	case outcome == musecode.OutcomeCompleted && findings > 0:
		return "Review recommended roles"
	case outcome == musecode.OutcomeCompleted:
		return "Adjust the search brief or run again"
	case outcome == musecode.OutcomeStopped:
		return "Review partial checkpoints, then re-commission"
	default:
		return "Inspect gaps, then re-commission"
	}
}
