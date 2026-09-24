package researchmemory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Executor-reported error codes with memory-level meaning (T11; T16 sets them).
const (
	// ErrorCodeHTTP404 marks a failed fetch of a missing resource: the
	// negative result stays cached for the 404 TTL instead of the failed TTL.
	ErrorCodeHTTP404 = "http_404"
	// ErrorCodeCanceled marks an observation for a canceled execution.
	ErrorCodeCanceled = "canceled"
)

// FreshnessConfig carries the T06 §3 TTL defaults. Zero values select the
// defaults, so a bare FreshnessConfig{} is valid.
type FreshnessConfig struct {
	SearchTTL   time.Duration
	FetchTTL    time.Duration
	DomTTL      time.Duration
	ExecTTL     time.Duration
	EmptyTTL    time.Duration
	NotFoundTTL time.Duration
	BlockedTTL  time.Duration
	FailedTTL   time.Duration
}

// DefaultFreshness returns the contract TTL defaults: positive search/query
// 6h, fetched page/API 72h, rendered DOM 72h, exec 24h; negative empty 1h,
// 404 24h, blocked/rate-limited 30m, failed 15m; uncertain never expires.
func DefaultFreshness() FreshnessConfig {
	return FreshnessConfig{
		SearchTTL: 6 * time.Hour, FetchTTL: 72 * time.Hour,
		DomTTL: 72 * time.Hour, ExecTTL: 24 * time.Hour,
		EmptyTTL: time.Hour, NotFoundTTL: 24 * time.Hour,
		BlockedTTL: 30 * time.Minute, FailedTTL: 15 * time.Minute,
	}
}

func (c FreshnessConfig) withDefaults() FreshnessConfig {
	d := DefaultFreshness()
	if c.SearchTTL <= 0 {
		c.SearchTTL = d.SearchTTL
	}
	if c.FetchTTL <= 0 {
		c.FetchTTL = d.FetchTTL
	}
	if c.DomTTL <= 0 {
		c.DomTTL = d.DomTTL
	}
	if c.ExecTTL <= 0 {
		c.ExecTTL = d.ExecTTL
	}
	if c.EmptyTTL <= 0 {
		c.EmptyTTL = d.EmptyTTL
	}
	if c.NotFoundTTL <= 0 {
		c.NotFoundTTL = d.NotFoundTTL
	}
	if c.BlockedTTL <= 0 {
		c.BlockedTTL = d.BlockedTTL
	}
	if c.FailedTTL <= 0 {
		c.FailedTTL = d.FailedTTL
	}
	return c
}

// PositiveTTL maps an operation to its success-result TTL.
func (c FreshnessConfig) PositiveTTL(op researchcontract.Operation) time.Duration {
	c = c.withDefaults()
	switch op {
	case researchcontract.OperationSearch:
		return c.SearchTTL
	case researchcontract.OperationBrowser:
		return c.DomTTL
	case researchcontract.OperationExec:
		return c.ExecTTL
	default:
		return c.FetchTTL
	}
}

// NegativeTTL maps a non-content outcome to its TTL. ok=false means the
// outcome never expires (uncertain: reconcile only, T06 §3).
func (c FreshnessConfig) NegativeTTL(outcome, errorCode string, httpStatus *int64) (time.Duration, bool) {
	c = c.withDefaults()
	switch outcome {
	case store.ObservationEmpty:
		return c.EmptyTTL, true
	case store.ObservationBlocked, store.ObservationRateLimited:
		return c.BlockedTTL, true
	case store.ObservationFailed:
		if errorCode == ErrorCodeHTTP404 || (httpStatus != nil && *httpStatus == 404) {
			return c.NotFoundTTL, true
		}
		return c.FailedTTL, true
	default:
		return 0, false
	}
}

// MemoryOptions tunes a Memory. A nil options pointer selects all defaults.
type MemoryOptions struct {
	// Now supplies the clock; defaults to time.Now. Tests inject a manual clock.
	Now func() time.Time
	// LeaseTTL is the claim-lease duration; defaults to 5 minutes.
	LeaseTTL time.Duration
	// Freshness carries TTLs; zero values select contract defaults.
	Freshness FreshnessConfig
}

// Memory owns exact-request memory for one actor. It implements
// researchcontract.ClaimStore (Lookup/Claim/Renew/Release/Refresh) and adds
// T-observe (Observe), T-note (AddNote) and the expiry sweep. All writers run
// in single BEGIN IMMEDIATE transactions with no network inside and no nested
// pooled queries: the takeover reconciliation check runs BEFORE the write
// txn (re-validated inside), because the pool holds a single connection and
// B's T12 authority reads through the same handle.
type Memory struct {
	db        *store.Store
	actor     store.Actor
	artifacts *ArtifactStore
	authority researchcontract.Authority
	now       func() time.Time
	leaseTTL  time.Duration
	freshness FreshnessConfig
}

var _ researchcontract.ClaimStore = (*Memory)(nil)

// NewMemory binds a claim store to one actor. authority is required (takeover
// reconciliation); T12 supplies the real one, tests a contract double.
func NewMemory(db *store.Store, actor store.Actor, artifacts *ArtifactStore, authority researchcontract.Authority, opts *MemoryOptions) (*Memory, error) {
	if db == nil || artifacts == nil || authority == nil {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"deps", "memory requires a store, artifact root and authority")
	}
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"actor", "memory requires an authenticated actor")
	}
	m := &Memory{db: db, actor: actor, artifacts: artifacts,
		authority: authority, now: time.Now, leaseTTL: 5 * time.Minute,
		freshness: DefaultFreshness()}
	if opts != nil {
		if opts.Now != nil {
			m.now = opts.Now
		}
		if opts.LeaseTTL > 0 {
			m.leaseTTL = opts.LeaseTTL
		}
		m.freshness = opts.Freshness.withDefaults()
	}
	return m, nil
}

// scopeFor derives the cache scope from the operation: browser actions run
// against a stateful live context, everything else produces stateless
// reusable artifacts (T06 §3).
func scopeFor(op researchcontract.Operation) researchcontract.CacheScope {
	if op == researchcontract.OperationBrowser {
		return researchcontract.CacheStatefulContext
	}
	return researchcontract.CacheStatelessReusable
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05.000000000Z", s)
}

func newEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint")
}

// leaseValid reports whether a claimed row still holds a live lease.
// Unparseable timestamps fail closed (treated as expired).
func leaseValid(req store.ResearchRequest, now time.Time) bool {
	if req.State != store.ResearchStateClaimed || req.Lease == nil {
		return false
	}
	until, err := parseTime(req.Lease.Until)
	if err != nil {
		return false
	}
	return until.After(now)
}

// freshPositive reports whether a fresh row still holds within its positive
// TTL. Unparseable timestamps fail toward re-fetch (not fresh).
func freshPositive(req store.ResearchRequest, now time.Time) bool {
	if req.State != store.ResearchStateFresh || req.FreshUntil == "" {
		return false
	}
	until, err := parseTime(req.FreshUntil)
	if err != nil {
		return false
	}
	return until.After(now)
}

// freshNegative reports whether an exhausted row still holds within its
// negative TTL. Unparseable timestamps fail toward re-fetch (not fresh).
func freshNegative(req store.ResearchRequest, now time.Time) bool {
	if req.State != store.ResearchStateExhausted || req.NegativeUntil == "" {
		return false
	}
	until, err := parseTime(req.NegativeUntil)
	if err != nil {
		return false
	}
	return until.After(now)
}

func leaseFrom(req store.ResearchRequest) *researchcontract.Lease {
	until, _ := parseTime(req.Lease.Until)
	return &researchcontract.Lease{LeaseID: req.ID, Owner: req.Lease.Owner,
		Generation: req.Lease.Generation, ExpiresAt: until}
}

func reusableFrom(req store.ResearchRequest) []string {
	if req.LatestObservationID == "" {
		return nil
	}
	out := []string{req.LatestObservationID}
	if req.LatestCaptureID != "" {
		out = append(out, req.LatestCaptureID)
	}
	return out
}

func failedFrom(req store.ResearchRequest) []string {
	if req.LatestObservationID == "" {
		return nil
	}
	return []string{req.LatestObservationID}
}

func eventPayload(fields map[string]string) json.RawMessage {
	raw, _ := json.Marshal(fields)
	return raw
}

// Lookup is a read-only reuse/claimability probe: it never grants a lease and
// never writes. runID is accepted for interface symmetry; reads are not
// run-scoped.
func (m *Memory) Lookup(ctx context.Context, _ string, request researchcontract.RequestDescriptor) (researchcontract.MemoryClaim, error) {
	fingerprint, err := request.Fingerprint()
	if err != nil {
		return researchcontract.MemoryClaim{}, err
	}
	var out researchcontract.MemoryClaim
	now := m.now()
	err = m.db.Read(ctx, func(r store.Reader) error {
		req, err := store.GetResearchRequest(ctx, r, m.actor.Kind, m.actor.ID, fingerprint)
		if errors.Is(err, store.ErrNotFound) {
			out = researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeOK}
			return nil
		}
		if err != nil {
			return err
		}
		out = m.describe(req, now)
		return nil
	})
	return out, err
}

// describe maps a stored row to its read-only claim view.
func (m *Memory) describe(req store.ResearchRequest, now time.Time) researchcontract.MemoryClaim {
	switch {
	case req.State == store.ResearchStateClaimed && leaseValid(req, now):
		return researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeClaimedElsewhere}
	case req.State == store.ResearchStateClaimed:
		return researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeClaimedElsewhere,
			RefreshFrom: req.LatestObservationID}
	case freshPositive(req, now):
		return researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeReused,
			Reusable: reusableFrom(req)}
	case freshNegative(req, now):
		return researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeReused,
			Failed: failedFrom(req)}
	case req.State == store.ResearchStateUncertain:
		return researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeUncertain,
			Failed: failedFrom(req)}
	default:
		return researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeOK,
			RefreshFrom: req.LatestObservationID}
	}
}

// Claim runs the T-claim transaction: claim/reuse/refresh-decision plus the
// journaled outcome event in one BEGIN IMMEDIATE txn. A fresh result is
// reused (no lease); an uncertain request refuses without a justified
// refresh; an expired lease is taken over only after reconciliation.
func (m *Memory) Claim(ctx context.Context, runID, owner string, generation int64, request researchcontract.RequestDescriptor, idempotencyKey string) (researchcontract.MemoryClaim, error) {
	return m.claim(ctx, runID, owner, generation, request, idempotencyKey, "", false)
}

// Refresh runs a justified-refresh T-claim: like Claim, but a valid reason
// also re-opens fresh, exhausted and uncertain requests into a new lease.
func (m *Memory) Refresh(ctx context.Context, runID, owner string, generation int64, request researchcontract.RequestDescriptor, reason string) (researchcontract.MemoryClaim, error) {
	switch reason {
	case store.RefreshReasonStale, store.RefreshReasonChangedSource,
		store.RefreshReasonCoverageGap, store.RefreshReasonOwnerCorrection:
	default:
		return researchcontract.MemoryClaim{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "refreshReason",
			"refresh requires one of stale|changed_source|coverage_gap|owner_correction")
	}
	return m.claim(ctx, runID, owner, generation, request, "", reason, true)
}

func (m *Memory) claim(ctx context.Context, runID, owner string, generation int64, request researchcontract.RequestDescriptor, idempotencyKey, reason string, isRefresh bool) (researchcontract.MemoryClaim, error) {
	fingerprint, err := request.Fingerprint()
	if err != nil {
		return researchcontract.MemoryClaim{}, err
	}
	if runID == "" || strings.TrimSpace(owner) == "" || generation <= 0 {
		return researchcontract.MemoryClaim{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "claim",
			"claim requires runId, owner and a positive generation")
	}
	var out researchcontract.MemoryClaim
	now := m.now()
	// Expired-lease reconciliation is decided OUTSIDE the write txn (see
	// decideTakeover); the txn re-validates owner and expiry before granting.
	takeover, err := m.decideTakeover(ctx, fingerprint, now)
	if err != nil {
		return researchcontract.MemoryClaim{}, err
	}
	err = m.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		ok, err := store.ResearchRoundExists(ctx, db, runID)
		if err != nil {
			return err
		}
		if !ok {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"runId", "unknown run "+runID)
		}
		req, err := store.GetResearchRequest(ctx, db, m.actor.Kind, m.actor.ID, fingerprint)
		if errors.Is(err, store.ErrNotFound) {
			req, err = store.CreateResearchRequest(ctx, db, m.actor, request, scopeFor(request.Operation))
			if isUniqueViolation(err) {
				// Lost a concurrent-create race; adopt the winner's row.
				req, err = store.GetResearchRequest(ctx, db, m.actor.Kind, m.actor.ID, fingerprint)
			}
		}
		if err != nil {
			return err
		}
		grant := func(takeoverOf string) error {
			updated, err := store.UpdateResearchRequest(ctx, db, req.ID, store.ResearchRequestMutation{
				State: store.ResearchStateClaimed,
				Lease: &store.ResearchLease{Owner: owner, Generation: generation,
					Until: formatTime(now.Add(m.leaseTTL))},
				LatestObservationID: req.LatestObservationID,
				LatestCaptureID:     req.LatestCaptureID,
				FreshUntil:          req.FreshUntil,
				NegativeUntil:       req.NegativeUntil,
				RefreshReason:       reasonOr(req.RefreshReason, reason),
			})
			if err != nil {
				return err
			}
			kind := researchcontract.EventClaim
			payload := map[string]string{"leaseOwner": owner,
				"generation": fmt.Sprint(generation), "fingerprint": fingerprint}
			if idempotencyKey != "" {
				payload["idempotencyKey"] = idempotencyKey
			}
			if takeoverOf != "" {
				kind = researchcontract.EventLeaseTakeover
				payload["takeoverOf"] = takeoverOf
			}
			if isRefresh {
				kind = researchcontract.EventRefresh
				payload["refreshReason"] = reason
			}
			out = researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeOK,
				Lease: leaseFrom(updated), RefreshFrom: req.LatestObservationID}
			return store.AppendRunEvent(ctx, db, researchcontract.Event{
				ID: newEventID(), RunID: runID, AttemptID: eventAttemptID(ctx, db, owner), Kind: kind,
				RequestFingerprint: fingerprint, Outcome: researchcontract.OutcomeOK,
				Payload: eventPayload(payload), ObservedAt: now, RecordedAt: now,
			})
		}
		switch {
		case req.State == store.ResearchStateClaimed && leaseValid(req, now):
			if req.Lease.Owner == owner && req.Lease.Generation == generation {
				// Replay/share: the holder re-asserts its own live lease.
				out = researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeOK,
					Lease: leaseFrom(req)}
				return nil
			}
			out = researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeClaimedElsewhere}
			return nil
		case req.State == store.ResearchStateClaimed:
			// Expired lease: takeover only with a pre-decided, still-valid
			// reconciliation for the same prior owner.
			if !takeover.checked || takeover.priorOwner != req.Lease.Owner || !takeover.allowed {
				out = researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeClaimedElsewhere,
					RefreshFrom: req.LatestObservationID}
				return nil
			}
			return grant(req.Lease.Owner)
		case freshPositive(req, now) && !isRefresh:
			out = researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeReused,
				Reusable: reusableFrom(req)}
			return store.AppendRunEvent(ctx, db, researchcontract.Event{
				ID: newEventID(), RunID: runID, Kind: researchcontract.EventReuse,
				RequestFingerprint: fingerprint, ObservationID: req.LatestObservationID,
				CaptureID: req.LatestCaptureID, Outcome: researchcontract.OutcomeReused,
				ObservedAt: now, RecordedAt: now,
			})
		case freshNegative(req, now) && !isRefresh:
			out = researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeReused,
				Failed: failedFrom(req)}
			return store.AppendRunEvent(ctx, db, researchcontract.Event{
				ID: newEventID(), RunID: runID, Kind: researchcontract.EventExhausted,
				RequestFingerprint: fingerprint, ObservationID: req.LatestObservationID,
				Outcome:    researchcontract.OutcomeReused,
				ObservedAt: now, RecordedAt: now,
			})
		case req.State == store.ResearchStateUncertain && !isRefresh:
			out = researchcontract.MemoryClaim{Outcome: researchcontract.OutcomeUncertain,
				Failed: failedFrom(req)}
			return nil
		default:
			return grant("")
		}
	})
	return out, err
}

func reasonOr(current, next string) string {
	if next != "" {
		return next
	}
	return current
}

// takeoverDecision is a pre-write-txn reconciliation verdict for one expired
// lease. The claim txn re-validates that the same prior owner still holds
// an expired lease before granting, so a stale verdict only ever refuses
// (the caller retries) and never wrongly grants.
type takeoverDecision struct {
	checked    bool
	priorOwner string
	allowed    bool
}

// decideTakeover reports whether an expired lease may be taken over: the
// prior attempt is terminal in the local ledger, or the run authority
// records it as terminal/reconciled-uncertain (T06 §4). It runs entirely
// outside the write txn: authority reads through the same single-connection
// pool and must never nest inside ResearchWrite.
func (m *Memory) decideTakeover(ctx context.Context, fingerprint string, now time.Time) (takeoverDecision, error) {
	var d takeoverDecision
	err := m.db.Read(ctx, func(r store.Reader) error {
		var err error
		req, err := store.GetResearchRequest(ctx, r, m.actor.Kind, m.actor.ID, fingerprint)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if req.State != store.ResearchStateClaimed || req.Lease == nil || leaseValid(req, now) {
			return nil
		}
		d.checked = true
		d.priorOwner = req.Lease.Owner
		state, err := store.ResearchAttemptState(ctx, r, req.Lease.Owner)
		if err == nil {
			switch state {
			case store.AttemptSucceeded, store.AttemptFailed,
				store.AttemptObservedSuccess, store.AttemptObservedFailure,
				store.AttemptCancelled:
				d.allowed = true
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return nil
	})
	if err != nil {
		return d, err
	}
	if d.checked && !d.allowed && d.priorOwner != "" {
		rec, err := m.authority.ReconciliationFor(ctx, d.priorOwner)
		if err != nil {
			var cerr *researchcontract.Error
			if errors.As(err, &cerr) && cerr.Code == researchcontract.OutcomeNotFound {
				return d, nil
			}
			return d, err
		}
		switch rec.State {
		case "reconciled_uncertain", "succeeded", "failed",
			"observed_success", "observed_failure", "cancelled":
			d.allowed = true
		}
	}
	return d, nil
}

// Renew extends a live lease. Only the lease owner with the matching
// generation may renew; stale, foreign or expired leases fail fenced.
func (m *Memory) Renew(ctx context.Context, leaseID, owner string, generation int64) (researchcontract.Lease, error) {
	var out researchcontract.Lease
	now := m.now()
	err := m.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		req, err := m.ownedClaim(ctx, db, leaseID, owner, generation)
		if err != nil {
			return err
		}
		if !leaseValid(req, now) {
			return researchcontract.NewError(researchcontract.OutcomeStale,
				"lease", "lease "+leaseID+" has expired and cannot be renewed")
		}
		updated, err := store.UpdateResearchRequest(ctx, db, req.ID, store.ResearchRequestMutation{
			State: store.ResearchStateClaimed,
			Lease: &store.ResearchLease{Owner: owner, Generation: generation,
				Until: formatTime(now.Add(m.leaseTTL))},
			LatestObservationID: req.LatestObservationID,
			LatestCaptureID:     req.LatestCaptureID,
			FreshUntil:          req.FreshUntil,
			NegativeUntil:       req.NegativeUntil,
			RefreshReason:       req.RefreshReason,
		})
		if err != nil {
			return err
		}
		out = *leaseFrom(updated)
		return nil
	})
	return out, err
}

// Release drops a lease. With an observation id it commits the recorded
// result (fresh/exhausted/uncertain per outcome); without one it abandons
// the claim back to free. Only the lease owner with the matching generation
// may release; anything else fails fenced. Releasing an already-committed
// observation is idempotent.
func (m *Memory) Release(ctx context.Context, leaseID, owner string, generation int64, observationID string) error {
	now := m.now()
	return m.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		req, err := store.GetResearchRequestByID(ctx, db, leaseID)
		if errors.Is(err, store.ErrNotFound) || req.ActorKind != m.actor.Kind || req.ActorID != m.actor.ID {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"lease", "unknown lease "+leaseID)
		}
		if err != nil {
			return err
		}
		if req.State != store.ResearchStateClaimed {
			if observationID != "" && req.LatestObservationID == observationID {
				return nil // already committed via Observe; idempotent replay
			}
			return researchcontract.NewError(researchcontract.OutcomeStale,
				"lease", "lease "+leaseID+" is no longer live")
		}
		if req.Lease.Owner != owner || req.Lease.Generation != generation {
			return researchcontract.NewError(researchcontract.OutcomeStale,
				"lease", "lease "+leaseID+" is held by another worker/generation")
		}
		if observationID == "" {
			// Abandon (also the cleanup path for the owner's own expired lease).
			_, err := store.UpdateResearchRequest(ctx, db, req.ID, store.ResearchRequestMutation{
				State:               store.ResearchStateFree,
				LatestObservationID: req.LatestObservationID,
				LatestCaptureID:     req.LatestCaptureID,
				FreshUntil:          req.FreshUntil,
				NegativeUntil:       req.NegativeUntil,
				RefreshReason:       req.RefreshReason,
			})
			return err
		}
		if !leaseValid(req, now) {
			return researchcontract.NewError(researchcontract.OutcomeStale,
				"lease", "lease "+leaseID+" has expired; late content must go through Observe")
		}
		obs, err := store.GetResearchObservation(ctx, db, observationID)
		if errors.Is(err, store.ErrNotFound) || obs.RequestID != req.ID {
			return researchcontract.NewError(researchcontract.OutcomeNotFound,
				"observation", "unknown observation "+observationID+" for this request")
		}
		if err != nil {
			return err
		}
		if obs.IsLate || obs.Outcome == store.ObservationLate {
			return researchcontract.NewError(researchcontract.OutcomeStale,
				"observation", "a late observation cannot commit request state")
		}
		state, freshUntil, negativeUntil, err := m.commitOutcome(ctx, db, req, obs, now)
		if err != nil {
			return err
		}
		_, err = store.UpdateResearchRequest(ctx, db, req.ID, store.ResearchRequestMutation{
			State:               state,
			LatestObservationID: obs.ID,
			LatestCaptureID:     obs.CaptureID,
			FreshUntil:          freshUntil,
			NegativeUntil:       negativeUntil,
			RefreshReason:       req.RefreshReason,
		})
		return err
	})
}

// ownedClaim loads a live actor-owned claim or fails fenced/not-found.
func (m *Memory) ownedClaim(ctx context.Context, db store.ResearchDB, leaseID, owner string, generation int64) (store.ResearchRequest, error) {
	req, err := store.GetResearchRequestByID(ctx, db, leaseID)
	if errors.Is(err, store.ErrNotFound) || req.ActorKind != m.actor.Kind || req.ActorID != m.actor.ID {
		return store.ResearchRequest{}, researchcontract.NewError(
			researchcontract.OutcomeNotFound, "lease", "unknown lease "+leaseID)
	}
	if err != nil {
		return store.ResearchRequest{}, err
	}
	if req.State != store.ResearchStateClaimed || req.Lease.Owner != owner || req.Lease.Generation != generation {
		return store.ResearchRequest{}, researchcontract.NewError(
			researchcontract.OutcomeStale, "lease",
			"lease "+leaseID+" is not held by this worker/generation")
	}
	return req, nil
}

// commitOutcome maps a committed observation to its request state and TTLs.
func (m *Memory) commitOutcome(ctx context.Context, db store.ResearchDB, req store.ResearchRequest, obs store.ResearchObservation, now time.Time) (state, freshUntil, negativeUntil string, err error) {
	switch obs.Outcome {
	case store.ObservationSuccess:
		return store.ResearchStateFresh,
			formatTime(now.Add(m.freshness.PositiveTTL(req.Request.Operation))), "", nil
	case store.ObservationEmpty, store.ObservationBlocked,
		store.ObservationFailed, store.ObservationRateLimited:
		var httpStatus *int64
		if obs.CaptureID != "" {
			capt, cerr := store.GetSourceCapture(ctx, db, obs.CaptureID)
			if cerr != nil {
				return "", "", "", cerr
			}
			httpStatus = capt.HTTPStatus
		}
		ttl, ok := m.freshness.NegativeTTL(obs.Outcome, obs.ErrorCode, httpStatus)
		if !ok {
			return store.ResearchStateUncertain, "", "", nil
		}
		return store.ResearchStateExhausted, "",
			formatTime(now.Add(ttl)), nil
	case store.ObservationUncertain:
		return store.ResearchStateUncertain, "", "", nil
	default:
		return "", "", "", researchcontract.NewError(researchcontract.OutcomeInvalid,
			"outcome", "observation outcome "+obs.Outcome+" cannot commit request state")
	}
}
