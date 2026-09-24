// Research capability registration: the six domain capabilities (plan §4,
// T06 §8) plus general research execution, served on the jobseek MCP bridge
// for commissioned research runs.
//
// Owner: lane B (runtime), T17. Every tool authenticates from the active
// execution context — the one-turn capability the supervisor issues for the
// live codex.turn attempt — plus account/action/run authority (runId,
// current generation, stable idempotencyKey). There are no saved-company
// prerequisites and no source/phase scoping on this registry: research
// tools accept arbitrary public requests and bind run authority, which is
// the B registry side of the T16-coordinated removal. Contract outcomes
// travel in-band as {"outcome": ...} (the bridge collapses handler errors);
// a non-nil handler error means an internal failure only.
package codexservice

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchmemory"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Research tool names (T06 §8; plan §4).
const (
	ResearchToolContextRead = "context_read"
	ResearchToolMemory      = "research_memory"
	ResearchToolEvidence    = "evidence_capture"
	ResearchToolJevAssess   = "jev_assess"
	ResearchToolMatch       = "opportunity_match"
	ResearchToolSave        = "records_save"
	ResearchToolExecute     = "research_execute"
)

// ResearchToolNames lists the seven registered research tools in a stable
// order: the six domain capabilities plus general research execution.
func ResearchToolNames() []string {
	return []string{
		ResearchToolContextRead, ResearchToolMemory, ResearchToolEvidence,
		ResearchToolJevAssess, ResearchToolMatch, ResearchToolSave,
		ResearchToolExecute,
	}
}

// ResearchDispatch executes one bounded research operation behind its
// supervisor: reserve → dispatch → capture → observation → receipt. The
// supervisor owns the executor; T17 codes to researchcontract.Executor with
// doubles until C lands the production executor (T16) and T23 wires it.
type ResearchDispatch func(ctx context.Context, in rounds.DispatchInput) (rounds.DispatchOutput, error)

// ResearchNoteSaved journals one committed records_save batch into the run
// checkpoint. It is required: without it saved counts and reports silently
// drop real records.
type ResearchNoteSaved func(ctx context.Context, runID, batchKey string, recordIDs []string) error

// ResearchToolchain binds the seven research tools to the agreed handler
// interfaces. NewMemory/NewSaver are per-request factories because memory
// leases and save audits attribute to the authenticated actor, which is
// only known once the tool-session capability verifies. OwnerBrief and
// Records are optional composition readers: when nil, brief facts and
// record subjects report an explicit deferral instead of invented data.
type ResearchToolchain struct {
	NewMemory  func(actor store.Actor) (*researchmemory.Memory, error)
	Captures   *researchmemory.Captures
	Authority  researchcontract.Authority
	Assessor   researchcontract.Assessor
	Matcher    researchcontract.Matcher
	NewSaver   func(actor store.Actor) (researchcontract.RecordSaver, error)
	Dispatch   ResearchDispatch
	NoteSaved  ResearchNoteSaved
	OwnerBrief OwnerBriefReader
	Records    RecordReader
}

// errResearchUnwired fails tool calls and readiness while the concrete
// research handlers are not wired (production integration is T23).
var errResearchUnwired = errors.New("research tools are not wired")

// errResearchStaleGeneration reports a presented generation behind the
// active tool session. It maps to an in-band stale outcome, never a tool
// error: the caller re-reads context and retries under the live generation.
var errResearchStaleGeneration = errors.New("presented generation is behind the active session")

// complete reports whether the concrete backends behind all seven tools are
// wired. OwnerBrief/Records stay optional: their absence defers honestly.
func (t *ResearchToolchain) complete() error {
	if t == nil || t.NewMemory == nil || t.Captures == nil || t.Authority == nil ||
		t.Assessor == nil || t.Matcher == nil || t.NewSaver == nil || t.Dispatch == nil ||
		t.NoteSaved == nil {
		return errResearchUnwired
	}
	return nil
}

// SetResearchTools wires (or replaces) the research toolchain and rebuilds
// the MCP bridge to serve it. A nil toolchain removes the research tools.
func (s *Service) SetResearchTools(tc *ResearchToolchain) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.research = tc
	s.bridge = s.newBridge()
}

func (s *Service) researchTools() *ResearchToolchain {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.research
}

// researchReady gates research_run readiness on concrete handler wiring.
func (s *Service) researchReady() error {
	return s.researchTools().complete()
}

// verifyResearchSession binds the active tool-session credential to the run:
// the one-turn capability issued for the live codex.turn attempt. Generation
// mismatches report errResearchStaleGeneration for an in-band stale outcome;
// anything else fails closed as a handler error (the bridge collapses it).
func (s *Service) verifyResearchSession(ctx context.Context, capability, runID string, generation int64) (store.RoundToolAuthority, error) {
	if capability == "" || runID == "" {
		return store.RoundToolAuthority{}, errTool
	}
	auth, err := s.db.VerifyRoundToolCapability(ctx, capability, runID)
	if err != nil {
		return store.RoundToolAuthority{}, err
	}
	if generation != 0 && generation != auth.Generation {
		return auth, errResearchStaleGeneration
	}
	return auth, nil
}

// staleGenerationOutcome is the in-band stale outcome for a behind
// generation. The detail names the live generation so the caller can retry.
func staleGenerationOutcome(live int64) map[string]any {
	return map[string]any{"outcome": string(researchcontract.OutcomeStale),
		"field":      string(researchcontract.AuthorityGeneration),
		"detail":     "presented generation is behind the active session; re-read context and retry",
		"generation": live}
}

// researchContractOutcome maps a handler error to its in-band response.
// ok=false means err is internal and must propagate as a handler error.
func researchContractOutcome(err error) (map[string]any, bool) {
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) {
		return nil, false
	}
	out := map[string]any{"outcome": string(cerr.Code)}
	if cerr.Field != "" {
		out["field"] = cerr.Field
	}
	if cerr.Detail != "" {
		out["detail"] = cerr.Detail
	}
	return out, true
}

func validResearchKey(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value
}

func registerResearchTools(server *mcp.Server, s *Service) {
	registerTool(server, ResearchToolContextRead, "Read versioned run context under run authority: checkpoint, live claims, journaled activity, notes and run limits, plus the current owner brief when requested.", s.contextReadTool)
	registerTool(server, ResearchToolMemory, "Investigative memory under run authority: lookup, claim, renew, release, conclude, checkpoint and justified refresh of exact-request and intent work.", s.researchMemoryTool)
	registerTool(server, ResearchToolEvidence, "Resolve a trusted execution receipt server-side and verify its immutable capture and quoted excerpts.", s.evidenceCaptureTool)
	registerTool(server, ResearchToolJevAssess, "Judge Codex-framed questions from cited capture spans only, under run authority with the run brief pair.", s.jevAssessTool)
	registerTool(server, ResearchToolMatch, "Retrieve employer/vacancy identity candidates under run authority. Advice until commit: records_save rechecks identity.", s.opportunityMatchTool)
	registerTool(server, ResearchToolSave, "Commit one all-or-nothing batch of research-sourced company/opportunity records with evidence, assessments and identity decisions.", s.recordsSaveTool)
	registerTool(server, ResearchToolExecute, "Execute one bounded public research operation (search, fetch, browse, api, exec) under run authority with automatic claim, capture, observation and receipt. No saved-company prerequisite: arbitrary public URLs, queries, parameters, bodies and content types.", s.researchExecuteTool)
}

// ---------------------------------------------------------------------------
// context_read
// ---------------------------------------------------------------------------

// contextReadArgs is the context_read tool input. Checkpoint defaults to
// the verified run; brief/profile compose the current owner brief.
type contextReadArgs struct {
	Capability string `json:"capability"`
	RunID      string `json:"runId"`
	Generation int64  `json:"generation,omitempty"`
	Brief      bool   `json:"brief,omitempty"`
	Profile    bool   `json:"profile,omitempty"`
	Subject    *struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"subject,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

func (s *Service) contextReadTool(ctx context.Context, args contextReadArgs) (map[string]any, error) {
	tc := s.researchTools()
	if err := tc.complete(); err != nil {
		return nil, err
	}
	auth, err := s.verifyResearchSession(ctx, args.Capability, args.RunID, args.Generation)
	if errors.Is(err, errResearchStaleGeneration) {
		return staleGenerationOutcome(auth.Generation), nil
	}
	if err != nil {
		return nil, err
	}
	if args.Subject != nil && (args.Subject.Kind == "company" || args.Subject.Kind == "opportunity") {
		return s.contextRecordSubject(ctx, tc, args.Subject.Kind, args.Subject.ID)
	}
	mem, err := tc.NewMemory(auth.Actor)
	if err != nil {
		return nil, err
	}
	checkpoint := args.Checkpoint
	if checkpoint == "" {
		checkpoint = args.RunID
	}
	var subject *researchmemory.ContextSubject
	if args.Subject != nil {
		subject = &researchmemory.ContextSubject{Kind: args.Subject.Kind, ID: args.Subject.ID}
	}
	out, err := researchmemory.NewContextReadHandler(s.db, mem, tc.Authority).Handle(ctx,
		researchmemory.ContextReadArgs{Brief: args.Brief, Profile: args.Profile,
			Subject: subject, Checkpoint: checkpoint, Cursor: args.Cursor, Limit: args.Limit})
	if err != nil || out["outcome"] != string(researchcontract.OutcomeOK) ||
		(!args.Brief && !args.Profile) || tc.OwnerBrief == nil {
		return out, err
	}
	brief, err := tc.OwnerBrief.OwnerBrief(ctx)
	if err != nil {
		if o, ok := researchContractOutcome(err); ok {
			return o, nil
		}
		return nil, err
	}
	// The run briefVersion stays binding (assessments bind it, never the
	// live owner brief); current owner facts ride a separate key.
	out["currentBrief"] = map[string]any{"profileVersion": brief.ProfileVersion,
		"rubricVersion": brief.RubricVersion, "source": brief.Source,
		"facts": brief.Facts, "preferences": brief.Preferences}
	out["deferred"] = dropDeferral(out["deferred"],
		"owner brief facts and preferences are served by the T17-composed brief reader")
	return out, nil
}

func dropDeferral(value any, served string) any {
	list, ok := value.([]string)
	if !ok {
		return value
	}
	kept := make([]string, 0, len(list))
	for _, item := range list {
		if item != served {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// contextRecordSubject serves one company/opportunity subject through the
// D-owned record reader composed at T17. Without the reader the subject is
// explicitly deferred, never invented.
func (s *Service) contextRecordSubject(ctx context.Context, tc *ResearchToolchain, kind, id string) (map[string]any, error) {
	if strings.TrimSpace(id) == "" {
		return map[string]any{"outcome": string(researchcontract.OutcomeInvalid),
			"field": "subject", "detail": "subject id is required"}, nil
	}
	if tc.Records == nil {
		return map[string]any{"outcome": string(researchcontract.OutcomeInvalid),
			"field": "subject", "detail": "record reads need the D-owned record reader; it is not wired"}, nil
	}
	var summary map[string]any
	var err error
	if kind == "company" {
		summary, err = tc.Records.CompanySummary(ctx, id)
	} else {
		summary, err = tc.Records.OpportunitySummary(ctx, id)
	}
	if err != nil {
		if o, ok := researchContractOutcome(err); ok {
			return o, nil
		}
		return nil, err
	}
	return map[string]any{"outcome": string(researchcontract.OutcomeOK), "subject": summary}, nil
}

// ---------------------------------------------------------------------------
// research_memory
// ---------------------------------------------------------------------------

// researchMemoryToolArgs is the research_memory tool input. Owner defaults
// to the live attempt id (lease owners SHOULD be round_attempt ids).
type researchMemoryToolArgs struct {
	Capability     string                              `json:"capability"`
	RunID          string                              `json:"runId"`
	Generation     int64                               `json:"generation,omitempty"`
	Op             string                              `json:"op"`
	Owner          string                              `json:"owner,omitempty"`
	IdempotencyKey string                              `json:"idempotencyKey,omitempty"`
	Request        *researchcontract.RequestDescriptor `json:"requestDescriptor,omitempty"`
	LeaseID        string                              `json:"leaseId,omitempty"`
	ObservationID  string                              `json:"observationId,omitempty"`
	RefreshReason  string                              `json:"refreshReason,omitempty"`
	Intent         string                              `json:"intent,omitempty"`
	Conclusion     *researchmemory.ConclusionArgs      `json:"conclusion,omitempty"`
}

func (s *Service) researchMemoryTool(ctx context.Context, args researchMemoryToolArgs) (map[string]any, error) {
	tc := s.researchTools()
	if err := tc.complete(); err != nil {
		return nil, err
	}
	auth, err := s.verifyResearchSession(ctx, args.Capability, args.RunID, args.Generation)
	if errors.Is(err, errResearchStaleGeneration) {
		return staleGenerationOutcome(auth.Generation), nil
	}
	if err != nil {
		return nil, err
	}
	mem, err := tc.NewMemory(auth.Actor)
	if err != nil {
		return nil, err
	}
	owner := args.Owner
	if owner == "" {
		owner = auth.AttemptID
	}
	// The T11 handler returns contract outcomes in-band.
	return researchmemory.NewResearchMemoryHandler(mem).Handle(ctx, researchmemory.ResearchMemoryArgs{
		Op: args.Op, RunID: args.RunID, Owner: owner, Generation: auth.Generation,
		IdempotencyKey: args.IdempotencyKey, Request: args.Request, LeaseID: args.LeaseID,
		ObservationID: args.ObservationID, RefreshReason: args.RefreshReason,
		Intent: args.Intent, Conclusion: args.Conclusion})
}

// ---------------------------------------------------------------------------
// evidence_capture
// ---------------------------------------------------------------------------

// evidenceCaptureToolArgs is the evidence_capture tool input.
type evidenceCaptureToolArgs struct {
	Capability     string                      `json:"capability"`
	RunID          string                      `json:"runId"`
	Receipt        string                      `json:"receipt"`
	Excerpt        *researchmemory.ExcerptArgs `json:"excerpt,omitempty"`
	AssertComplete bool                        `json:"assertComplete,omitempty"`
	ProvenanceNote string                      `json:"provenanceNote,omitempty"`
}

func (s *Service) evidenceCaptureTool(ctx context.Context, args evidenceCaptureToolArgs) (map[string]any, error) {
	tc := s.researchTools()
	if err := tc.complete(); err != nil {
		return nil, err
	}
	if _, err := s.verifyResearchSession(ctx, args.Capability, args.RunID, 0); err != nil {
		return nil, err
	}
	// The T11 handler returns contract outcomes in-band.
	return researchmemory.NewEvidenceCaptureHandler(tc.Captures).Handle(ctx,
		researchmemory.EvidenceCaptureArgs{Receipt: args.Receipt, Excerpt: args.Excerpt,
			AssertComplete: args.AssertComplete, ProvenanceNote: args.ProvenanceNote})
}

// ---------------------------------------------------------------------------
// jev_assess
// ---------------------------------------------------------------------------

// jevAssessToolArgs is the jev_assess tool input.
type jevAssessToolArgs struct {
	Capability          string                               `json:"capability"`
	RunID               string                               `json:"runId"`
	Generation          int64                                `json:"generation,omitempty"`
	Purpose             string                               `json:"purpose"`
	Questions           []researchcontract.AssessQuestion    `json:"questions"`
	ProfileVersion      int64                                `json:"profileVersion"`
	RubricVersion       string                               `json:"rubricVersion"`
	CandidateIdentities []researchcontract.CandidateIdentity `json:"candidateIdentities,omitempty"`
	SourceRefs          []researchcontract.EvidenceRef       `json:"sourceRefs"`
	IdempotencyKey      string                               `json:"idempotencyKey"`
}

func (s *Service) jevAssessTool(ctx context.Context, args jevAssessToolArgs) (map[string]any, error) {
	tc := s.researchTools()
	if err := tc.complete(); err != nil {
		return nil, err
	}
	auth, err := s.verifyResearchSession(ctx, args.Capability, args.RunID, args.Generation)
	if errors.Is(err, errResearchStaleGeneration) {
		return staleGenerationOutcome(auth.Generation), nil
	}
	if err != nil {
		return nil, err
	}
	if !validResearchKey(args.IdempotencyKey) {
		return map[string]any{"outcome": string(researchcontract.OutcomeInvalid),
			"field": "idempotencyKey", "detail": "idempotency key required (1..128 chars, no surrounding space)"}, nil
	}
	assessment, err := tc.Assessor.Assess(ctx, researchcontract.AssessInput{
		Purpose: args.Purpose, Questions: args.Questions,
		ProfileVersion: args.ProfileVersion, RubricVersion: args.RubricVersion,
		CandidateIdentities: args.CandidateIdentities, SourceRefs: args.SourceRefs,
		IdempotencyKey: args.IdempotencyKey, RunID: args.RunID, Generation: auth.Generation})
	if err != nil {
		if o, ok := researchContractOutcome(err); ok {
			return o, nil
		}
		return nil, err
	}
	return map[string]any{"outcome": string(researchcontract.OutcomeOK),
		"assessmentId": assessment.ID, "results": assessment.Results,
		"model": assessment.Model, "modelVersion": assessment.ModelVersion,
		"usage": assessment.Usage, "reuseKey": assessment.ReuseKey,
		"evidenceRefs": assessment.EvidenceRefs}, nil
}

// ---------------------------------------------------------------------------
// opportunity_match
// ---------------------------------------------------------------------------

// opportunityMatchToolArgs is the opportunity_match tool input.
type opportunityMatchToolArgs struct {
	Capability      string                               `json:"capability"`
	RunID           string                               `json:"runId"`
	Generation      int64                                `json:"generation,omitempty"`
	Attributes      researchcontract.MatchAttributes     `json:"attributes"`
	KnownIDs        []string                             `json:"knownIds,omitempty"`
	ExtraCandidates []researchcontract.CandidateIdentity `json:"extraCandidates,omitempty"`
	EvidenceRefs    []researchcontract.EvidenceRef       `json:"evidenceRefs"`
}

func (s *Service) opportunityMatchTool(ctx context.Context, args opportunityMatchToolArgs) (map[string]any, error) {
	tc := s.researchTools()
	if err := tc.complete(); err != nil {
		return nil, err
	}
	auth, err := s.verifyResearchSession(ctx, args.Capability, args.RunID, args.Generation)
	if errors.Is(err, errResearchStaleGeneration) {
		return staleGenerationOutcome(auth.Generation), nil
	}
	if err != nil {
		return nil, err
	}
	match, err := tc.Matcher.Match(ctx, researchcontract.MatchInput{
		Attributes: args.Attributes, KnownIDs: args.KnownIDs,
		ExtraCandidates: args.ExtraCandidates, EvidenceRefs: args.EvidenceRefs,
		RunID: args.RunID, Generation: auth.Generation})
	if err != nil {
		if o, ok := researchContractOutcome(err); ok {
			return o, nil
		}
		return nil, err
	}
	out := map[string]any{"outcome": string(match.Outcome)}
	if len(match.ExactMatches) > 0 {
		out["exactMatches"] = match.ExactMatches
	}
	if len(match.PossibleMatches) > 0 {
		out["possibleMatches"] = match.PossibleMatches
	}
	if match.Unresolved != nil {
		out["unresolved"] = match.Unresolved
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// records_save
// ---------------------------------------------------------------------------

// recordsSaveToolArgs is the records_save tool input.
type recordsSaveToolArgs struct {
	Capability     string                      `json:"capability"`
	RunID          string                      `json:"runId"`
	Generation     int64                       `json:"generation,omitempty"`
	Batch          []researchcontract.SaveItem `json:"batch"`
	IdempotencyKey string                      `json:"idempotencyKey"`
}

func (s *Service) recordsSaveTool(ctx context.Context, args recordsSaveToolArgs) (map[string]any, error) {
	tc := s.researchTools()
	if err := tc.complete(); err != nil {
		return nil, err
	}
	auth, err := s.verifyResearchSession(ctx, args.Capability, args.RunID, args.Generation)
	if errors.Is(err, errResearchStaleGeneration) {
		return staleGenerationOutcome(auth.Generation), nil
	}
	if err != nil {
		return nil, err
	}
	if !validResearchKey(args.IdempotencyKey) {
		return map[string]any{"outcome": string(researchcontract.OutcomeInvalid),
			"field": "idempotencyKey", "detail": "idempotency key required (1..128 chars, no surrounding space)"}, nil
	}
	saver, err := tc.NewSaver(auth.Actor)
	if err != nil {
		return nil, err
	}
	saved, err := saver.Save(ctx, researchcontract.SaveBatch{
		Items: args.Batch, IdempotencyKey: args.IdempotencyKey,
		RunID: args.RunID, Generation: auth.Generation})
	if err != nil {
		if o, ok := researchContractOutcome(err); ok {
			return o, nil
		}
		return nil, err
	}
	// The commit is durable; the run checkpoint must learn its record ids
	// or saved counts and reports drop real records. A lag here is
	// explicit: retrying the same key replays the save and records once.
	if saved.Outcome == researchcontract.OutcomeOK && len(saved.Saved) > 0 {
		ids := make([]string, 0, len(saved.Saved))
		for _, rec := range saved.Saved {
			ids = append(ids, rec.RecordID)
		}
		if err := tc.NoteSaved(ctx, args.RunID, args.IdempotencyKey, ids); err != nil {
			return nil, err
		}
	}
	out := map[string]any{"outcome": string(saved.Outcome)}
	if len(saved.Saved) > 0 {
		out["saved"] = saved.Saved
	}
	if len(saved.ReusedIDs) > 0 {
		out["reusedIds"] = saved.ReusedIDs
	}
	if len(saved.Items) > 0 {
		out["items"] = saved.Items
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// research_execute
// ---------------------------------------------------------------------------

// researchExecuteToolArgs is the research_execute tool input.
type researchExecuteToolArgs struct {
	Capability     string                             `json:"capability"`
	RunID          string                             `json:"runId"`
	Generation     int64                              `json:"generation,omitempty"`
	Kind           researchcontract.ExecuteKind       `json:"kind"`
	Request        researchcontract.RequestDescriptor `json:"request"`
	Bounds         researchcontract.Bounds            `json:"bounds"`
	IdempotencyKey string                             `json:"idempotencyKey"`
}

func (s *Service) researchExecuteTool(ctx context.Context, args researchExecuteToolArgs) (map[string]any, error) {
	tc := s.researchTools()
	if err := tc.complete(); err != nil {
		return nil, err
	}
	auth, err := s.verifyResearchSession(ctx, args.Capability, args.RunID, args.Generation)
	if errors.Is(err, errResearchStaleGeneration) {
		return staleGenerationOutcome(auth.Generation), nil
	}
	if err != nil {
		return nil, err
	}
	switch args.Kind {
	case researchcontract.ExecuteSearch, researchcontract.ExecuteFetch,
		researchcontract.ExecuteBrowse, researchcontract.ExecuteAPI,
		researchcontract.ExecuteExec:
	default:
		return map[string]any{"outcome": string(researchcontract.OutcomeInvalid),
			"field": "kind", "detail": "unknown execution kind " + string(args.Kind)}, nil
	}
	if err := args.Request.Validate(); err != nil {
		if o, ok := researchContractOutcome(err); ok {
			return o, nil
		}
		return nil, err
	}
	if args.Bounds.MaxBytes < 0 || args.Bounds.MaxRequests < 0 || args.Bounds.DeadlineMs < 0 {
		return map[string]any{"outcome": string(researchcontract.OutcomeInvalid),
			"field": "bounds", "detail": "bounds must be >= 0"}, nil
	}
	if !validResearchKey(args.IdempotencyKey) {
		return map[string]any{"outcome": string(researchcontract.OutcomeInvalid),
			"field": "idempotencyKey", "detail": "idempotency key required (1..128 chars, no surrounding space)"}, nil
	}
	// The supervisor reserves, dispatches, observes and checkpoints; the
	// actual outcome (ok, reused, claimed_elsewhere, ...) aggregates into
	// the run checkpoint and report from durable state.
	out, err := tc.Dispatch(ctx, rounds.DispatchInput{RunID: args.RunID,
		Generation: auth.Generation, Kind: args.Kind, Request: args.Request,
		Bounds: args.Bounds, IdempotencyKey: args.IdempotencyKey})
	if err != nil {
		if o, ok := researchContractOutcome(err); ok {
			return o, nil
		}
		return nil, err
	}
	result := map[string]any{"outcome": string(out.Outcome), "attemptId": out.AttemptID,
		"receipt": out.Receipt, "usage": out.Usage, "replayed": out.Replayed}
	if out.ObservationID != "" {
		result["observationId"] = out.ObservationID
	}
	if out.CaptureID != "" {
		result["captureId"] = out.CaptureID
	}
	return result, nil
}
