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

// Deps wires one discovery service. Transport is provider-disabled until
// the E11-authorized live transport exists; commissions fail closed meanwhile.
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
		classifier: classifier, workspaces: deps.Workspaces, runs: map[string]*retainedRun{},
	}, nil
}

// Readiness reports the frozen check verdict for one tier without admitting
// any session input.
func (s *Service) Readiness(tier musecode.Tier) musecode.Status {
	return musecode.Check(tier, s.facts)
}

// Commissions counts admitted discovery runs. Reads never increment it.
func (s *Service) Commissions() int64 {
	return atomic.LoadInt64(&s.commissioned)
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

// CommissionDiscovery admits one bounded Contributor discovery run, conducts
// it to a terminal result, classifies saved vacancies and persists the
// terminal row. Same-key replays return the stored result without
// re-conducting; unknown prior state fails closed instead of replaying blind.
func (s *Service) CommissionDiscovery(ctx context.Context, runRef string, criteria musecode.PublicCriteria, profileVersion int64, rubricVersion string) (CommissionResult, error) {
	if !validRunRef(runRef) {
		return CommissionResult{}, fmt.Errorf("%w: invalid run ref", ErrUnknownRun)
	}
	status := musecode.Check(musecode.TierContributor, s.facts)
	if !status.Available {
		return CommissionResult{}, fmt.Errorf("musewire: contributor unavailable (%s): %s", status.Code, status.Detail)
	}
	workspace := filepath.Join(s.workspaces, "contributor", runRef)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return CommissionResult{}, err
	}
	spec, err := musecode.NewSession(status, workspace, s.bounds, nil)
	if err != nil {
		return CommissionResult{}, err
	}
	// Lookup-first replay: the round digest covers the deadline, so a second
	// StartRound call can never replay. The guard below keeps the
	// same-key-different-input conflict honest instead.
	if existing, err := s.db.RoundByRequest(ctx, s.actor, "muse:"+runRef); err == nil {
		return s.replayGuarded(ctx, runRef, existing, profileVersion, rubricVersion)
	} else if !errors.Is(err, store.ErrNotFound) {
		return CommissionResult{}, fmt.Errorf("musewire: lookup round: %w", err)
	}
	round, created, err := s.db.StartRound(ctx, s.actor, store.StartRoundInput{
		RequestKey: "muse:" + runRef, Intent: CommissionIntent, Outcome: "pending",
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
			Requests: int64(s.bounds.MaxToolCalls), Items: 1000,
			Tools: int64(s.bounds.MaxToolCalls), Turns: int64(s.bounds.MaxModelSteps),
		},
		Deadline: time.Now().Add(s.bounds.MaxWallClock),
	})
	if err != nil {
		return CommissionResult{}, fmt.Errorf("musewire: start round: %w", err)
	}
	if !created {
		return s.replay(ctx, runRef)
	}
	if _, err := s.db.ActivateRound(ctx, s.actor, round.ID); err != nil {
		return CommissionResult{}, fmt.Errorf("musewire: activate round: %w", err)
	}
	if err := s.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SaveRunCheckpoint(ctx, db, round.ID, researchcontract.Checkpoint{
			ProfileVersion: profileVersion, RubricVersion: rubricVersion, Generation: 1,
		})
	}); err != nil {
		return CommissionResult{}, fmt.Errorf("musewire: checkpoint brief: %w", err)
	}
	server, err := publicresearch.NewServer(publicresearch.Deps{
		Executor: s.executor, Captures: s.captures, Bounds: s.bounds,
		RunID: round.ID, Generation: 1,
	})
	if err != nil {
		return CommissionResult{}, err
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
	admission, err := supervisor.StartRun(ctx, runRef, spec, musecode.PublicInput{Criteria: criteria}, s.facts)
	if err != nil {
		return CommissionResult{}, err
	}
	atomic.AddInt64(&s.commissioned, 1)
	terminal, ok := supervisor.Result(runRef)
	if !ok {
		return CommissionResult{}, errors.New("musewire: admitted run has no terminal result")
	}
	findings, classifyErrs := s.classifySaved(ctx, round.ID, profileVersion, rubricVersion, server, terminal.SavedRefs)
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
		return CommissionResult{}, err
	}
	deliverable := fmt.Sprintf("report saved, %d findings", len(findings))
	if _, err := s.db.FinishRound(ctx, s.actor, round.ID, terminalState, reason, deliverable, summary); err != nil {
		return CommissionResult{}, fmt.Errorf("musewire: finish round: %w", err)
	}
	if err := s.db.SaveMuseRunReport(ctx, store.MuseRunReport{
		RunRef: runRef, RoundID: round.ID, Tier: string(musecode.TierContributor),
		Outcome: string(terminal.Outcome), Detail: terminal.Detail,
		SavedRefs: terminal.SavedRefs, ClassifyErrors: classifyErrs, UpdatedAt: terminal.EndedAt,
	}); err != nil {
		return CommissionResult{}, fmt.Errorf("musewire: persist report: %w", err)
	}
	s.mu.Lock()
	s.runs[runRef].roundID = round.ID
	s.runs[runRef].terminal = terminal
	s.mu.Unlock()
	return CommissionResult{Admission: admission, Terminal: terminal, RoundID: round.ID,
		Findings: findings, ClassifyErrors: classifyErrs}, nil
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
			return CommissionResult{}, errors.New("musewire: run " + runRef + " is already in progress")
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
