package researchexecute

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchmemory"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Backend names accepted in RequestDescriptor.Backend, per kind. The executor
// validates them strictly: a descriptor naming another backend is a different
// exact request and must not dispatch here.
const (
	BackendHTTP    = "generic-http"
	BackendBrowser = "chromium-headless-shell"
	BackendExec    = "python3-sandbox"
)

// Executor-reported error codes (beyond researchmemory.ErrorCodeHTTP404 /
// ErrorCodeCanceled, which carry TTL/observation meaning in T11).
const (
	ErrorCodeDestinationForbidden = "destination_forbidden"
	ErrorCodeRedirectLimit        = "redirect_limit"
	ErrorCodeSandboxUnavailable   = "sandbox_unavailable"
	ErrorCodeBackendNotConfigured = "backend_not_configured"
	ErrorCodeBackendUntrusted     = "backend_untrusted"
	ErrorCodeRequestFailed        = "request_failed"
	ErrorCodeDeadlineExceeded     = "deadline_exceeded"
	ErrorCodeTooManyRequests      = "request_bound"
)

// Config tunes the executor. Zero values select the canary defaults (plan
// §8: at most 2 concurrent external operations; per-request and per-run
// byte/network limits from the T02 proof).
type Config struct {
	MaxConcurrent        int
	MaxBodyBytes         int64
	MaxRequests          int
	MaxRedirects         int
	OpTimeout            time.Duration
	ChromePath           string
	ExpectedChromeSHA256 string
	PythonPath           string
	SandboxBinary        string
	ScratchRoot          string
	// PermitLoopback allows loopback fixture destinations. Test-only:
	// production dispatch refuses every non-public address.
	PermitLoopback bool
}

func (c Config) withDefaults() Config {
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 2
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 1 << 20
	}
	if c.MaxRequests <= 0 {
		c.MaxRequests = 32
	}
	if c.MaxRedirects <= 0 {
		c.MaxRedirects = 5
	}
	if c.OpTimeout <= 0 {
		c.OpTimeout = 30 * time.Second
	}
	if c.SandboxBinary == "" {
		c.SandboxBinary = defaultSandboxBinary
	}
	return c
}

// opDeps carries the per-operation dispatch dependencies.
type opDeps struct {
	backends       *backends
	resolver       resolver
	sandboxBinary  string
	scratchRoot    string
	permitLoopback bool
	maxRedirects   int
	// browserProfileHook observes the per-op profile dir. Test-only
	// (keychain-silence proof); never set in production.
	browserProfileHook func(string)
}

func (d *opDeps) forwardClient() *guardedClient {
	return newGuardedClient(d.maxRedirects, d.permitLoopback, d.resolver)
}

// Executor implements researchcontract.Executor: automatic
// claim/reservation → dispatch → capture → observation → receipt per call.
// Codex performs no ceremonial memory calls before dispatch; reuse,
// fencing and uncertainty surface as in-band outcomes.
type Executor struct {
	authority researchcontract.Authority
	memory    *researchmemory.Memory
	captures  *researchmemory.Captures
	db        *store.Store
	cfg       Config
	deps      *opDeps
	sem       chan struct{}
	// seen tracks reservation IDs already returned to this process. The
	// authority interface does not report replays, but reservation IDs are
	// stable per key: a repeated ID is a replay of a hold owned by an
	// earlier call, which must never be released here (releasing it would
	// refund the earlier execution's charge). Only fresh IDs are released
	// on non-dispatch paths. T23 settles every dispatch from its receipt.
	seenMu sync.Mutex
	seen   map[string]bool
}

var _ researchcontract.Executor = (*Executor)(nil)

// NewExecutor binds an executor to its authority, memory, capture reader and
// store handle. Construction validates shape only and launches no subprocesses.
func NewExecutor(authority researchcontract.Authority, memory *researchmemory.Memory, captures *researchmemory.Captures, db *store.Store, cfg Config) (*Executor, error) {
	if authority == nil || memory == nil || captures == nil || db == nil {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"deps", "executor requires authority, memory, captures and store")
	}
	cfg = cfg.withDefaults()
	if cfg.ScratchRoot == "" {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"scratchRoot", "executor requires a scratch root for per-op temp dirs")
	}
	// T23: create the scratch root at construction. Fresh deployments and
	// wired stacks pass a not-yet-existing dir; without this, every
	// browse/exec op fails when its per-op profile dir cannot be created.
	if err := os.MkdirAll(cfg.ScratchRoot, 0700); err != nil {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"scratchRoot", "executor cannot create scratch root")
	}
	e := &Executor{
		authority: authority, memory: memory, captures: captures, db: db, cfg: cfg,
		deps: &opDeps{
			backends:       &backends{chromePath: cfg.ChromePath, expectedChrome: cfg.ExpectedChromeSHA256, pythonPath: cfg.PythonPath, versionTimeout: 15 * time.Second},
			resolver:       net.DefaultResolver,
			sandboxBinary:  cfg.SandboxBinary,
			scratchRoot:    cfg.ScratchRoot,
			permitLoopback: cfg.PermitLoopback,
			maxRedirects:   cfg.MaxRedirects,
		},
		sem:  make(chan struct{}, cfg.MaxConcurrent),
		seen: map[string]bool{},
	}
	return e, nil
}

// Execute runs one general-research call: validate (pre-dispatch, zero
// allowance) → authority check → reservation → exact-request claim → dispatch
// → authentic capture → observation → receipt. Memory-level outcomes (reused,
// claimed_elsewhere, uncertain) return in-band with a nil error; authority
// refusals and validation failures return typed contract errors. Refusals from
// the pre-claim stages (validation, check, reservation, claim) are wrapped in
// PreClaimRefusal so the supervisor can release its hold (T24 F2/F3).
func (e *Executor) Execute(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
	plan, err := planRequest(in, e.cfg)
	if err != nil {
		return researchcontract.ExecuteOutput{}, refusePreClaim(err)
	}
	if err := e.authority.Check(ctx, researchcontract.CheckInput{
		RunID: in.RunID, Generation: in.Generation,
		Permission: researchcontract.PermissionResearchDispatch, Now: time.Now(),
	}); err != nil {
		return researchcontract.ExecuteOutput{}, refusePreClaim(err)
	}
	key := in.IdempotencyKey
	if key == "" {
		key = "research:" + plan.fingerprint
	}
	reservation, isReplay, err := e.reserve(ctx, in.RunID, plan.reserveOp, key, plan.fingerprint)
	if err != nil {
		return researchcontract.ExecuteOutput{}, refusePreClaim(err)
	}
	// A replayed reservation belongs to an earlier call: only a fresh hold
	// may be released when this call does not dispatch.
	maybeAbandon := func() {
		if !isReplay {
			e.abandonReservation(ctx, reservation.ID)
		}
	}
	claim, err := e.memory.Claim(ctx, in.RunID, reservation.AttemptID, in.Generation, in.Request, key)
	if err != nil {
		maybeAbandon()
		return researchcontract.ExecuteOutput{}, refusePreClaim(err)
	}
	switch claim.Outcome {
	case researchcontract.OutcomeReused:
		return e.resolveReuse(ctx, claim, reservation.ID, isReplay)
	case researchcontract.OutcomeClaimedElsewhere:
		maybeAbandon()
		return researchcontract.ExecuteOutput{Outcome: researchcontract.OutcomeClaimedElsewhere}, nil
	case researchcontract.OutcomeUncertain:
		maybeAbandon()
		return researchcontract.ExecuteOutput{Outcome: researchcontract.OutcomeUncertain}, nil
	case researchcontract.OutcomeOK:
	default:
		maybeAbandon()
		return researchcontract.ExecuteOutput{}, refusePreClaim(researchcontract.NewError(researchcontract.OutcomeInvalid,
			"claim", "unexpected claim outcome "+string(claim.Outcome)))
	}
	if claim.Lease == nil {
		maybeAbandon()
		return researchcontract.ExecuteOutput{}, refusePreClaim(researchcontract.NewError(researchcontract.OutcomeInvalid,
			"claim", "granted claim carries no lease"))
	}
	owner := reservation.AttemptID
	leaseID := claim.Lease.LeaseID

	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		e.abandonLease(ctx, leaseID, owner, in.Generation)
		maybeAbandon()
		return researchcontract.ExecuteOutput{Outcome: researchcontract.OutcomeStopped}, ctx.Err()
	}
	defer func() { <-e.sem }()

	opCtx, cancel := context.WithTimeout(ctx, plan.opTimeout)
	defer cancel()
	started := time.Now()
	outcome, capture, receipt := e.dispatch(opCtx, ctx, plan, started)
	if capture.capture != nil {
		// The receipt binds the capture id up front (content sha256); Observe
		// re-verifies it against the bytes before committing.
		sum := sha256.Sum256(capture.capture.Bytes)
		receipt.CaptureID = hex.EncodeToString(sum[:])
	}
	// The commit outlives caller cancellation: what happened must be recorded
	// honestly (canceled/uncertain included) instead of dropped, or a retry
	// could blindly replay. Local SQLite only, bounded.
	commitCtx, commitCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer commitCancel()
	receipt.ID = newReceiptID()
	receipt.Operation = plan.operation
	receipt.Fingerprint = plan.fingerprint
	receipt.StartedAt = started

	obsIn := researchmemory.ObserveInput{
		LeaseID: leaseID, Owner: owner, Generation: in.Generation,
		RoundID: in.RunID, RoundAttemptID: owner,
		ActualURLOrQuery: plan.actualURLOrQuery,
		ActualParamsJSON: plan.actualParamsJSON,
		StartedAt:        started,
		FinishedAt:       receipt.EndedAt,
		Outcome:          string(outcome),
		Provenance:       plan.provenance,
		Receipt:          receipt,
		Capture:          capture.capture,
		ErrorCode:        receipt.ErrorCode,
		TruncationNote:   capture.truncationNote,
	}
	obs, obsErr := e.memory.Observe(commitCtx, obsIn)
	var obsOut researchcontract.ExecuteOutput
	obsOut.Outcome = outcome.ToContract()
	obsOut.Receipt = receipt
	if obsErr != nil {
		var cerr *researchcontract.Error
		if errors.As(obsErr, &cerr) && cerr.Code == researchcontract.OutcomeStale && obs.Late {
			// Fenced mid-flight (Stop rotated the generation): the bytes are
			// retained as an inert late observation (T06 late→ok). The work
			// is reported, never silently dropped nor replayed.
			obsOut.Outcome = researchcontract.OutcomeOK
			obsOut.ObservationID = obs.ObservationID
			obsOut.CaptureID = obs.CaptureID
			obsOut.Usage = usageOf(receipt)
			return obsOut, nil
		}
		// The observation did not commit; the lease stays live for its TTL
		// and takeover/retry converges explicitly. Loud, never silent.
		return researchcontract.ExecuteOutput{}, obsErr
	}
	obsOut.ObservationID = obs.ObservationID
	obsOut.CaptureID = obs.CaptureID
	obsOut.Usage = usageOf(receipt)
	if err := e.memory.Release(commitCtx, leaseID, owner, in.Generation, obs.ObservationID); err != nil {
		var cerr *researchcontract.Error
		if errors.As(err, &cerr) && cerr.Code == researchcontract.OutcomeStale {
			// Lost the lease after committing: the observation stands and a
			// retry converges to reuse. Benign.
			return obsOut, nil
		}
		return obsOut, err
	}
	return obsOut, nil
}

// reserve takes a pre-dispatch hold and reports whether the reservation ID
// was already seen by this process (a stable-ID replay owned by an earlier
// call, which the caller must never release).
func (e *Executor) reserve(ctx context.Context, runID, operation, key, payloadHash string) (researchcontract.Reservation, bool, error) {
	reservation, err := e.authority.Reserve(ctx, runID, operation, key, payloadHash)
	if err != nil {
		return researchcontract.Reservation{}, false, err
	}
	e.seenMu.Lock()
	_, isReplay := e.seen[reservation.ID]
	e.seen[reservation.ID] = true
	e.seenMu.Unlock()
	return reservation, isReplay, nil
}

// abandonReservation refunds a hold that never dispatched. The hold must be
// fresh (see reserve): invalid/not_found means nothing was held and is
// benign; anything else is loud. Cleanup outlives caller cancellation.
func (e *Executor) abandonReservation(ctx context.Context, reservationID string) {
	if reservationID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := e.authority.Release(ctx, reservationID); err != nil {
		var cerr *researchcontract.Error
		if errors.As(err, &cerr) && (cerr.Code == researchcontract.OutcomeInvalid || cerr.Code == researchcontract.OutcomeNotFound) {
			return
		}
		// Best-effort cleanup has no channel; the hold expires with its
		// attempt row, which T23 settles. This branch is unreachable with
		// the real authority (Release only fails invalid/not_found/internal).
	}
}

// abandonLease returns an undispatched claim to free. Cleanup outlives caller
// cancellation.
func (e *Executor) abandonLease(ctx context.Context, leaseID, owner string, generation int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_ = e.memory.Release(ctx, leaseID, owner, generation, "")
}

// resolveReuse serves a fresh result without dispatch: it returns the
// ORIGINAL backend-issued receipt (resolvable, never fabricated) with the
// reuse outcome. A recorded-but-unresolvable result is an internal
// inconsistency and fails loudly instead of inventing bytes.
func (e *Executor) resolveReuse(ctx context.Context, claim researchcontract.MemoryClaim, reservationID string, isReplay bool) (researchcontract.ExecuteOutput, error) {
	if !isReplay {
		e.abandonReservation(ctx, reservationID)
	}
	obsID := firstID(claim.Reusable, claim.Failed)
	if obsID == "" {
		return researchcontract.ExecuteOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"reused", "reuse names no recorded observation")
	}
	var obs store.ResearchObservation
	if err := e.db.Read(ctx, func(r store.Reader) error {
		var err error
		obs, err = store.GetResearchObservation(ctx, r, obsID)
		return err
	}); err != nil {
		return researchcontract.ExecuteOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"reused", "recorded reuse observation is unreadable")
	}
	rec, err := e.captures.ResolveReceipt(ctx, obs.ReceiptRef)
	if err != nil {
		return researchcontract.ExecuteOutput{}, err
	}
	return researchcontract.ExecuteOutput{
		Outcome:       researchcontract.OutcomeReused,
		ObservationID: obs.ID, CaptureID: obs.CaptureID, Receipt: rec,
	}, nil
}

func firstID(lists ...[]string) string {
	for _, l := range lists {
		if len(l) > 0 {
			return l[0]
		}
	}
	return ""
}

func usageOf(r researchcontract.ExecutionReceipt) researchcontract.ExecuteUsage {
	return researchcontract.ExecuteUsage{
		Requests: r.Subrequests, Bytes: r.BytesIn + r.BytesOut, Redirects: len(r.Redirects),
	}
}

func newReceiptID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// plannedRequest is a validated, dispatch-ready interpretation of ExecuteInput.
type plannedRequest struct {
	kind             researchcontract.ExecuteKind
	operation        researchcontract.Operation
	reserveOp        string
	fingerprint      string
	method           string
	url              string
	header           http.Header
	body             []byte
	code             string
	locale           string
	viewport         string
	provenance       researchcontract.ProvenanceKind
	actualURLOrQuery string
	actualParamsJSON string
	maxBytes         int64
	maxRequests      int
	opTimeout        time.Duration
}

var viewportPattern = regexp.MustCompile(`\A[0-9]{2,4}x[0-9]{2,4}\z`)

// planRequest validates the input before any authority or memory call: an
// invalid descriptor consumes zero allowance and reserves nothing.
func planRequest(in researchcontract.ExecuteInput, cfg Config) (*plannedRequest, error) {
	invalid := func(field, detail string) (*plannedRequest, error) {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid, field, detail)
	}
	plan := &plannedRequest{kind: in.Kind}
	switch in.Kind {
	case researchcontract.ExecuteSearch:
		plan.operation, plan.reserveOp = researchcontract.OperationSearch, store.RoundResearchSearch
		plan.provenance = researchcontract.ProvenanceSearchResult
	case researchcontract.ExecuteFetch:
		plan.operation, plan.reserveOp = researchcontract.OperationFetch, store.RoundResearchFetch
		plan.provenance = researchcontract.ProvenanceFetchedResponse
	case researchcontract.ExecuteBrowse:
		plan.operation, plan.reserveOp = researchcontract.OperationBrowser, store.RoundResearchBrowse
		plan.provenance = researchcontract.ProvenanceRenderedDOM
	case researchcontract.ExecuteAPI:
		plan.operation, plan.reserveOp = researchcontract.OperationAPI, store.RoundResearchAPI
		plan.provenance = researchcontract.ProvenanceFetchedResponse
	case researchcontract.ExecuteExec:
		plan.operation, plan.reserveOp = researchcontract.OperationExec, store.RoundResearchExec
		plan.provenance = researchcontract.ProvenanceFetchedResponse
	default:
		return invalid("kind", "unknown execution kind "+string(in.Kind))
	}
	d := in.Request
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if d.Operation != plan.operation {
		return invalid("request.operation", fmt.Sprintf("kind %s requires operation %s, got %s",
			in.Kind, plan.operation, d.Operation))
	}
	wantBackend := BackendHTTP
	if in.Kind == researchcontract.ExecuteBrowse {
		wantBackend = BackendBrowser
	} else if in.Kind == researchcontract.ExecuteExec {
		wantBackend = BackendExec
	}
	if d.Backend != wantBackend {
		return invalid("request.backend", fmt.Sprintf("kind %s dispatches through backend %q, got %q",
			in.Kind, wantBackend, d.Backend))
	}
	method := strings.ToUpper(strings.TrimSpace(d.Method))
	switch in.Kind {
	case researchcontract.ExecuteAPI:
		if method == "" {
			method = http.MethodGet
		}
		if method != http.MethodGet && method != http.MethodPost {
			return invalid("request.method", fmt.Sprintf("method %q is not read-only; rejected pre-dispatch", d.Method))
		}
	case researchcontract.ExecuteExec:
		if method != "" {
			return invalid("request.method", "exec carries no method")
		}
	default:
		if method == "" {
			method = http.MethodGet
		}
		if method != http.MethodGet {
			return invalid("request.method", fmt.Sprintf("kind %s is GET-only, got %q", in.Kind, d.Method))
		}
	}
	plan.method = method
	if err := checkBodyConsistency(d); err != nil {
		return nil, err
	}
	hasBody := d.Body != "" || d.BodySHA256 != ""
	switch {
	case in.Kind == researchcontract.ExecuteExec:
		if d.Body == "" {
			return invalid("request.body", "exec requires the program in body")
		}
		plan.code = d.Body
	case in.Kind == researchcontract.ExecuteAPI && method == http.MethodPost:
		plan.body = []byte(d.Body)
	case hasBody:
		return invalid("request.body", fmt.Sprintf("kind %s carries no body", in.Kind))
	}
	// Session fields: exactly the honored set, each non-empty and validated.
	for k, v := range d.SessionFields {
		switch k {
		case "locale":
			if strings.TrimSpace(v) == "" {
				return invalid("locale_session_context", "locale must be non-empty")
			}
			if in.Kind == researchcontract.ExecuteExec {
				return invalid("locale_session_context", "exec honors no session fields")
			}
			plan.locale = v
		case "viewport":
			if in.Kind != researchcontract.ExecuteBrowse {
				return invalid("locale_session_context", "viewport applies to browse only")
			}
			if !viewportPattern.MatchString(v) {
				return invalid("locale_session_context", "viewport must match WxH (e.g. 1280x800)")
			}
			plan.viewport = v
		default:
			return invalid("locale_session_context", "unknown session field "+k+" (honored: locale, viewport)")
		}
	}
	if d.Pagination != (researchcontract.Pagination{}) {
		switch in.Kind {
		case researchcontract.ExecuteSearch, researchcontract.ExecuteFetch, researchcontract.ExecuteAPI:
		default:
			return invalid("pagination", fmt.Sprintf("kind %s carries no pagination", in.Kind))
		}
	}
	if in.Kind == researchcontract.ExecuteExec {
		if d.URLOrQuery != "python3" {
			return invalid("request.url_or_query", `exec requires url_or_query "python3" (the only runtime)`)
		}
		if len(d.Params) > 0 {
			return invalid("request.params", "exec carries no params")
		}
		plan.actualURLOrQuery = "python3"
	} else {
		var query [][2]string
		header := http.Header{}
		for _, p := range d.Params {
			if strings.EqualFold(p.Name, "Content-Type") {
				if in.Kind != researchcontract.ExecuteAPI || method != http.MethodPost {
					return invalid("request.params", "Content-Type applies to api POST only")
				}
				header.Set("Content-Type", p.Value) // last wins, deterministic
				continue
			}
			query = append(query, [2]string{p.Name, p.Value})
		}
		target, err := buildQueryURL(d.URLOrQuery, query, d.Pagination.Page, d.Pagination.Cursor, d.Pagination.Limit)
		if err != nil {
			return invalid("request.url_or_query", "unparseable URL: "+err.Error())
		}
		if _, err := parseResearchURL(target, true); err != nil {
			// Static shape only here (permitLoopback irrelevant for shape);
			// resolution policy is enforced at dispatch with the real policy.
			return invalid("request.url_or_query", err.Error())
		}
		plan.url = target
		plan.header = header
		if plan.locale != "" && in.Kind != researchcontract.ExecuteBrowse {
			plan.header.Set("Accept-Language", plan.locale)
		}
		plan.actualURLOrQuery = d.URLOrQuery
		if raw, err := json.Marshal(d.Params); err == nil && len(d.Params) > 0 {
			plan.actualParamsJSON = string(raw)
		}
	}
	fingerprint, err := d.Fingerprint()
	if err != nil {
		return nil, err
	}
	plan.fingerprint = fingerprint
	if in.Bounds.MaxBytes < 0 || in.Bounds.MaxRequests < 0 || in.Bounds.DeadlineMs < 0 {
		return invalid("bounds", "bounds must not be negative (zero selects the default)")
	}
	plan.maxBytes = cfg.MaxBodyBytes
	if in.Bounds.MaxBytes > 0 {
		plan.maxBytes = in.Bounds.MaxBytes
	}
	plan.maxRequests = cfg.MaxRequests
	if in.Bounds.MaxRequests > 0 {
		plan.maxRequests = in.Bounds.MaxRequests
	}
	plan.opTimeout = cfg.OpTimeout
	if in.Bounds.DeadlineMs > 0 {
		plan.opTimeout = time.Duration(in.Bounds.DeadlineMs) * time.Millisecond
	}
	if in.RunID == "" || in.Generation <= 0 {
		return invalid("run", "execute requires runId and a positive generation")
	}
	return plan, nil
}

// checkBodyConsistency requires BodySHA256 to match Body when both are set.
func checkBodyConsistency(d researchcontract.RequestDescriptor) error {
	if d.Body == "" || d.BodySHA256 == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(d.Body))
	if hex.EncodeToString(sum[:]) != strings.ToLower(d.BodySHA256) {
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			"request.body", "body does not match body_sha256")
	}
	return nil
}

// dispatchResult is the capture half of a dispatch.
type dispatchCapture struct {
	capture        *researchmemory.CaptureInput
	truncationNote string
}

// dispatch runs the planned request and maps the authentic result to an
// observation outcome, capture input and backend-issued receipt. opCtx carries
// the operation deadline; ctx is the caller's context: caller cancellation
// reports canceled, an internal deadline reports uncertain, anything else
// failed. No path invents bytes.
func (e *Executor) dispatch(opCtx, ctx context.Context, plan *plannedRequest, started time.Time) (observeOutcome, dispatchCapture, researchcontract.ExecutionReceipt) {
	receipt := researchcontract.ExecutionReceipt{
		Attempts: 1, StartedAt: started, EndedAt: started,
		Executor: researchcontract.ExecutorIdentity{Backend: BackendHTTP, Version: runtime.Version()},
	}
	switch plan.kind {
	case researchcontract.ExecuteSearch, researchcontract.ExecuteFetch, researchcontract.ExecuteAPI:
		return e.dispatchHTTP(opCtx, ctx, plan, receipt)
	case researchcontract.ExecuteBrowse:
		return e.dispatchBrowse(opCtx, ctx, plan, receipt)
	case researchcontract.ExecuteExec:
		return e.dispatchExec(opCtx, ctx, plan, receipt)
	default:
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = ErrorCodeRequestFailed
		return outcomeFailed, dispatchCapture{}, receipt
	}
}

// observeOutcome is the T11 observation outcome vocabulary.
type observeOutcome string

const (
	outcomeSuccess     observeOutcome = "success"
	outcomeEmpty       observeOutcome = "empty"
	outcomeBlocked     observeOutcome = "blocked"
	outcomeFailed      observeOutcome = "failed"
	outcomeRateLimited observeOutcome = "rate_limited"
	outcomeUncertain   observeOutcome = "uncertain"
)

// ToContract maps the observation outcome to the caller-facing tool outcome.
func (o observeOutcome) ToContract() researchcontract.Outcome {
	switch o {
	case outcomeSuccess, outcomeEmpty, outcomeBlocked, outcomeRateLimited:
		return researchcontract.OutcomeOK
	case outcomeFailed:
		return researchcontract.OutcomeInvalid
	case outcomeUncertain:
		return researchcontract.OutcomeUncertain
	}
	return researchcontract.OutcomeInvalid
}

func (e *Executor) dispatchHTTP(opCtx, ctx context.Context, plan *plannedRequest, receipt researchcontract.ExecutionReceipt) (observeOutcome, dispatchCapture, researchcontract.ExecutionReceipt) {
	var cap dispatchCapture
	res, err := e.deps.forwardClient().do(opCtx, plan.method, plan.url, plan.header, plan.body, plan.maxBytes)
	receipt.EndedAt = time.Now()
	receipt.Subrequests = res.subrequests
	receipt.BytesIn = res.bytesIn
	receipt.BytesOut = res.bytesOut
	receipt.FinalURL = res.finalURL
	receipt.Redirects = res.redirects
	receipt.Truncated = res.truncated
	if err != nil {
		return e.failedHTTP(opCtx, ctx, receipt, cap, err)
	}
	if res.subrequests > plan.maxRequests {
		receipt.Truncated = true
		cap.truncationNote = fmt.Sprintf("subrequest bound exceeded: %d > %d", res.subrequests, plan.maxRequests)
	}
	status := int64(res.status)
	httpStatus := &status
	mediaType := res.contentType
	redirectJSON, _ := json.Marshal(res.redirects)
	base := &researchmemory.CaptureInput{
		Bytes: res.body, MediaType: mediaType, HTTPStatus: httpStatus,
		OriginalURL: plan.url, FinalURL: res.finalURL, RedirectChainJSON: string(redirectJSON),
		Provenance: plan.provenance, Completeness: store.CaptureComplete,
	}
	if receipt.Truncated {
		base.Completeness = store.CaptureTruncated
		if cap.truncationNote == "" {
			cap.truncationNote = fmt.Sprintf("body bound exceeded: response cut to %d bytes", plan.maxBytes)
		}
	}
	switch {
	case res.status >= 200 && res.status < 300 && len(res.body) > 0:
		receipt.Status = researchcontract.ReceiptOK
		cap.capture = base
		return outcomeSuccess, cap, receipt
	case res.status >= 200 && res.status < 300:
		receipt.Status = researchcontract.ReceiptOK
		// No bytes, no capture: truncation cannot attach to an empty body,
		// so the flag is forced off (a truncated receipt requires an
		// explicitly incomplete capture).
		receipt.Truncated = false
		return outcomeEmpty, cap, receipt
	case res.status == http.StatusNotFound:
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = researchmemory.ErrorCodeHTTP404
		receipt.Truncated = false // no capture claimed: nothing was truncated
		return outcomeFailed, cap, receipt
	case res.status == http.StatusTooManyRequests:
		// Execution succeeded (an authentic 429 was observed); the result is
		// rate-limited. An ok receipt backs the negative observation (T06
		// maps failed→invalid, so a failed receipt cannot back an ok outcome).
		receipt.Status = researchcontract.ReceiptOK
		receipt.ErrorCode = "http_429"
		receipt.Truncated = false
		return outcomeRateLimited, cap, receipt
	case res.status == http.StatusForbidden || res.status == http.StatusUnauthorized:
		receipt.Status = researchcontract.ReceiptOK
		receipt.ErrorCode = "http_" + strconv.Itoa(res.status)
		receipt.Truncated = false
		return outcomeBlocked, cap, receipt
	default:
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = "http_" + strconv.Itoa(res.status)
		receipt.Truncated = false
		return outcomeFailed, cap, receipt
	}
}

// failedHTTP maps a transport failure honestly: caller cancellation →
// canceled, internal deadline → uncertain, policy refusal → failed with the
// policy code, anything else → failed. Interrupted transfers carry no capture
// (a torn body is not evidence) and report unknown usage.
func (e *Executor) failedHTTP(opCtx, ctx context.Context, receipt researchcontract.ExecutionReceipt, cap dispatchCapture, err error) (observeOutcome, dispatchCapture, researchcontract.ExecutionReceipt) {
	switch {
	case ctx.Err() != nil:
		receipt.Status = researchcontract.ReceiptCanceled
		receipt.UnknownUsage = true
		receipt.ErrorCode = researchmemory.ErrorCodeCanceled
		return outcomeFailed, cap, receipt
	case opCtx.Err() != nil:
		receipt.Status = researchcontract.ReceiptUncertain
		receipt.UnknownUsage = true
		receipt.ErrorCode = ErrorCodeDeadlineExceeded
		return outcomeUncertain, cap, receipt
	case errors.Is(err, errDestinationForbidden) || errors.Is(err, errAddressNotPublic):
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = ErrorCodeDestinationForbidden
		return outcomeFailed, cap, receipt
	case errors.Is(err, errTooManyRedirects):
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = ErrorCodeRedirectLimit
		return outcomeFailed, cap, receipt
	default:
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = ErrorCodeRequestFailed
		return outcomeFailed, cap, receipt
	}
}

func (e *Executor) dispatchBrowse(opCtx, ctx context.Context, plan *plannedRequest, receipt researchcontract.ExecutionReceipt) (observeOutcome, dispatchCapture, researchcontract.ExecutionReceipt) {
	var cap dispatchCapture
	chromeTimeout := plan.opTimeout - 2*time.Second
	if chromeTimeout < 5*time.Second {
		chromeTimeout = 5 * time.Second
	}
	res, err := renderBrowser(opCtx, e.deps, plan.url, plan.locale, plan.viewport, plan.maxBytes, plan.maxRequests, chromeTimeout)
	receipt.EndedAt = time.Now()
	receipt.Executor = res.identity
	receipt.Subrequests = res.subrequests
	receipt.BytesIn = res.bytesIn
	receipt.BytesOut = res.bytesOut
	receipt.FinalURL = plan.url
	receipt.Truncated = res.truncated
	if err != nil {
		receipt.Truncated = false // no capture claimed on error paths
		if errors.Is(err, errBackendNotConfigured) {
			receipt.Status = researchcontract.ReceiptFailed
			receipt.ErrorCode = ErrorCodeBackendNotConfigured
			return outcomeFailed, cap, receipt
		}
		if errors.Is(err, errBackendUntrusted) {
			receipt.Status = researchcontract.ReceiptFailed
			receipt.ErrorCode = ErrorCodeBackendUntrusted
			return outcomeFailed, cap, receipt
		}
		if errors.Is(err, errSandboxUnavailable) {
			receipt.Status = researchcontract.ReceiptFailed
			receipt.ErrorCode = ErrorCodeSandboxUnavailable
			return outcomeFailed, cap, receipt
		}
		if errors.Is(err, errDestinationForbidden) || errors.Is(err, errAddressNotPublic) {
			receipt.Status = researchcontract.ReceiptFailed
			receipt.ErrorCode = ErrorCodeDestinationForbidden
			return outcomeFailed, cap, receipt
		}
		if ctx.Err() != nil {
			receipt.Status = researchcontract.ReceiptCanceled
			receipt.UnknownUsage = true
			receipt.ErrorCode = researchmemory.ErrorCodeCanceled
			return outcomeFailed, cap, receipt
		}
		if opCtx.Err() != nil {
			receipt.Status = researchcontract.ReceiptUncertain
			receipt.UnknownUsage = true
			receipt.ErrorCode = ErrorCodeDeadlineExceeded
			return outcomeUncertain, cap, receipt
		}
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = ErrorCodeRequestFailed
		return outcomeFailed, cap, receipt
	}
	if res.subrequests > plan.maxRequests {
		receipt.Truncated = true
	}
	completeness := store.CaptureComplete
	note := ""
	if receipt.Truncated {
		completeness = store.CapturePartial
		note = browserTruncationNote(plan, res)
	}
	cap.capture = &researchmemory.CaptureInput{
		Bytes: res.dom, MediaType: "text/html", HTTPStatus: res.mainStatus,
		OriginalURL: plan.url, FinalURL: plan.url,
		Provenance: plan.provenance, Completeness: completeness, ExtentJSON: browserExtent(res),
	}
	cap.truncationNote = note
	receipt.Status = researchcontract.ReceiptOK
	if len(res.dom) == 0 {
		cap.capture = nil
		cap.truncationNote = ""
		receipt.Truncated = false
		return outcomeEmpty, cap, receipt
	}
	return outcomeSuccess, cap, receipt
}

func browserTruncationNote(plan *plannedRequest, res browserResult) string {
	blocked := 0
	for _, o := range res.observed {
		if o.Blocked {
			blocked++
		}
	}
	switch {
	case res.subrequests > plan.maxRequests:
		return fmt.Sprintf("subrequest bound exceeded: %d observed > %d; some subresources never loaded", res.subrequests, plan.maxRequests)
	case blocked > 0:
		return fmt.Sprintf("%d subresource(s) blocked by destination policy; DOM captured as rendered", blocked)
	default:
		return fmt.Sprintf("render bound exceeded: DOM cut to %d bytes", plan.maxBytes)
	}
}

func (e *Executor) dispatchExec(opCtx, ctx context.Context, plan *plannedRequest, receipt researchcontract.ExecutionReceipt) (observeOutcome, dispatchCapture, researchcontract.ExecutionReceipt) {
	var cap dispatchCapture
	res, err := runExec(opCtx, e.deps, plan.code, plan.maxRequests)
	receipt.EndedAt = time.Now()
	receipt.Executor = res.identity
	receipt.Subrequests = res.subrequests
	receipt.BytesIn = res.bytesIn
	receipt.BytesOut = res.bytesOut
	if err != nil {
		receipt.Truncated = false // no capture claimed on error paths
		if errors.Is(err, errBackendNotConfigured) {
			receipt.Status = researchcontract.ReceiptFailed
			receipt.ErrorCode = ErrorCodeBackendNotConfigured
			return outcomeFailed, cap, receipt
		}
		if errors.Is(err, errSandboxUnavailable) {
			receipt.Status = researchcontract.ReceiptFailed
			receipt.ErrorCode = ErrorCodeSandboxUnavailable
			return outcomeFailed, cap, receipt
		}
		if ctx.Err() != nil {
			receipt.Status = researchcontract.ReceiptCanceled
			receipt.UnknownUsage = true
			receipt.ErrorCode = researchmemory.ErrorCodeCanceled
			return outcomeFailed, cap, receipt
		}
		if opCtx.Err() != nil {
			receipt.Status = researchcontract.ReceiptUncertain
			receipt.UnknownUsage = true
			receipt.ErrorCode = ErrorCodeDeadlineExceeded
			return outcomeUncertain, cap, receipt
		}
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = ErrorCodeRequestFailed
		return outcomeFailed, cap, receipt
	}
	completeness := store.CaptureComplete
	note := ""
	if res.cut || res.subrequests > plan.maxRequests {
		completeness = store.CapturePartial
		receipt.Truncated = true
		note = fmt.Sprintf("exec bound exceeded: output cut=%v subrequests=%d/%d", res.cut, res.subrequests, plan.maxRequests)
	}
	cap.capture = &researchmemory.CaptureInput{
		Bytes: res.combined, MediaType: "text/plain",
		OriginalURL: "python3", FinalURL: "",
		Provenance: plan.provenance, Completeness: completeness, ExtentJSON: execExtent(res),
	}
	cap.truncationNote = note
	if res.runErr != nil && ctx.Err() == nil && opCtx.Err() == nil {
		receipt.Status = researchcontract.ReceiptFailed
		receipt.ErrorCode = "exit_" + res.exitCode
		return outcomeFailed, cap, receipt
	}
	if ctx.Err() != nil {
		receipt.Status = researchcontract.ReceiptCanceled
		receipt.UnknownUsage = true
		receipt.ErrorCode = researchmemory.ErrorCodeCanceled
		return outcomeFailed, cap, receipt
	}
	if opCtx.Err() != nil {
		receipt.Status = researchcontract.ReceiptUncertain
		receipt.UnknownUsage = true
		receipt.ErrorCode = ErrorCodeDeadlineExceeded
		return outcomeUncertain, cap, receipt
	}
	receipt.Status = researchcontract.ReceiptOK
	return outcomeSuccess, cap, receipt
}
