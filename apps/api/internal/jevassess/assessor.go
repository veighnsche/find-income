// Package jevassess implements researchcontract.Assessor for dynamic Jev
// judgment (lane D, T09). Codex frames the questions and alternatives from
// found evidence; this handler binds them to immutable captures, runs exactly
// one Jev call per reservation, validates the provider result, and retains
// the exact exchange via jev_attempts. It defines no fixed source or question
// menu and performs no internal retries: a retry is an explicit new Assess
// call with a distinct idempotency key and a fresh reservation.
package jevassess

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// AbstainID is the reserved answer id appended to every question. Codex
// alternatives must not use it. Abstention stays allowed on every question.
const AbstainID = "abstain"

// AbstainLabel explains the reserved abstain alternative to the judge.
const AbstainLabel = "None of the alternatives is supported by the cited evidence"

// ReserveOperation is the authority operation name for one dynamic assessment.
// B registers research operations at T12/T16; the literal binds then.
const ReserveOperation = "jev_assess"

// Input bounds. Generous caps that keep payloads bounded; all enforced.
const (
	MaxQuestions    = 32
	MaxAlternatives = 64 // plus the reserved abstain alternative
	MaxEvidenceRefs = 256
	MaxQuestionText = 4000
	MaxLabel        = 500
	MaxTextField    = 128
	MaxPurpose      = 80
	MaxRubric       = 80
	MaxSpanBytes    = 1 << 18
	MaxCaptureBytes = 8 << 20
)

// Provider issues exactly one captured Jev call per reservation. *jev.Client
// satisfies it; tests use fixture doubles (no live provider spend).
type Provider interface {
	EncodedRequest(jev.Request) ([]byte, error)
	EvaluateOnceCaptured(context.Context, jev.Request) (jev.Result, jev.CapturedExchange, error)
	RequestedModel() string
}

// ExchangeLog is the jev_attempts exchange log used verbatim (T06 §6):
// immutable request plus terminal response per charged call. *store.Store
// satisfies it; the real store binds at T23, tests use doubles.
type ExchangeLog interface {
	BeginJevAttempt(context.Context, store.JevAttemptStart) (store.JevAttempt, error)
	FinishJevAttempt(context.Context, store.JevAttemptFinish) (store.JevAttempt, error)
	ReclassifyJevAttempt(ctx context.Context, id, status, errorKind string) error
}

// Briefs reports the current brief versions for run-scoped binding. The real
// reader binds at T14/T23; tests use doubles.
type Briefs interface {
	CurrentBrief(ctx context.Context, runID string) (profileVersion int64, rubricVersion string, err error)
}

// Dispatcher marks a reserved round attempt dispatched before its exchange
// begins. *store.Store satisfies it; tests use doubles.
type Dispatcher interface {
	MarkRoundDispatched(context.Context, string, string) (store.RoundAttempt, error)
}

// Finisher settles the reserved round attempt after its exchange ends.
// Round resolves the run so the finish audits under the run owner — the
// same rounds.actor_kind/actor_id value ResearchRoundActor returns for the
// assessment sink. *store.Store satisfies it; tests use doubles.
type Finisher interface {
	Round(context.Context, string) (store.Round, error)
	FinishRoundAttempt(context.Context, store.Actor, string, string, bool, json.RawMessage, string) (store.RoundAttempt, error)
}

// DynamicAssessmentRecord is the queryable binding row over one jev_attempts
// exchange (T06-D §2.2). Actor scope is resolved from the run at persistence
// time (T14), so the handler does not carry it. SupersedesID links a
// reassessment (brief change, T04 B01) to its predecessor; empty otherwise.
type DynamicAssessmentRecord struct {
	ID               string
	RunID            string
	JevAttemptID     string
	Purpose          string
	QuestionsJSON    []byte
	EvidenceRefsJSON []byte
	ProfileVersion   int64
	RubricVersion    string
	CandidatesJSON   []byte
	CandidateSetHash string
	RequestedModel   string
	ReuseKey         string
	Status           string // succeeded|partial_abstain
	AnswersJSON      []byte
	SupersedesID     string
	Model            string
	ModelVersion     string
	Usage            researchcontract.ExecuteUsage
}

// AssessmentSink persists the dynamic binding row. New-table persistence
// binds at T14; until then the handler accepts a nil sink and returns the
// assessment with its reuse key without persisting the row.
type AssessmentSink interface {
	SaveDynamicAssessment(context.Context, DynamicAssessmentRecord) error
}

// Handler answers Codex-framed questions from cited captures only.
type Handler struct {
	Authority researchcontract.Authority
	Captures  researchcontract.CaptureReader
	Briefs    Briefs
	Provider  Provider
	Exchanges ExchangeLog
	// Dispatch marks the reservation dispatched before BeginJevAttempt. It is
	// optional: when nil the handler uses Exchanges if it implements
	// Dispatcher (*store.Store does, which is what the T23 wire passes as
	// Exchanges), so struct-literal construction keeps working unchanged.
	Dispatch Dispatcher
	// Finish settles the reservation after a terminal exchange. It is
	// optional: when nil the handler uses Exchanges if it implements
	// Finisher (*store.Store does, which is what the T23 wire passes as
	// Exchanges), so struct-literal construction keeps working unchanged.
	Finish Finisher
	Sink   AssessmentSink // optional until T14 binds persistence
	// Supersedes optionally resolves the predecessor assessment id a new
	// record supersedes (brief-change reassessment chains, T06-D §2.2).
	// It runs only when Sink is set; a resolution failure after a
	// computed assessment reports outcome_uncertain for reconciliation.
	Supersedes func(context.Context, researchcontract.AssessInput) (string, error)
	// Reuse optionally resolves a persisted succeeded assessment by reuse
	// key before any reservation or provider call. It runs only when Sink
	// is set; nil disables reuse and every assessment calls the provider
	// and persists fresh. A hit returns the stored answers without
	// spending: the reuse key binds evidence, questions, brief, model and
	// purpose, so the stored verdicts answer the new ask identically.
	Reuse func(ctx context.Context, runID, reuseKey string) (store.DynamicAssessment, error)
}

var (
	_ researchcontract.Assessor = (*Handler)(nil)
	_ ExchangeLog               = (*store.Store)(nil)
	_ Dispatcher                = (*store.Store)(nil)
	_ Finisher                  = (*store.Store)(nil)
	_ Provider                  = (*jev.Client)(nil)
)

func (h *Handler) dispatcher() (Dispatcher, bool) {
	if h.Dispatch != nil {
		return h.Dispatch, true
	}
	d, ok := h.Exchanges.(Dispatcher)
	return d, ok
}

func (h *Handler) finisher() (Finisher, bool) {
	if h.Finish != nil {
		return h.Finish, true
	}
	f, ok := h.Exchanges.(Finisher)
	return f, ok
}

func (h *Handler) ready() error {
	if h == nil || h.Authority == nil || h.Captures == nil || h.Briefs == nil || h.Provider == nil || h.Exchanges == nil {
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "", "jevassess handler missing a required dependency")
	}
	return nil
}

// Assess implements researchcontract.Assessor: validate, bind, reserve,
// dispatch once, validate the result, log the exchange, persist the binding.
func (h *Handler) Assess(ctx context.Context, in researchcontract.AssessInput) (researchcontract.Assessment, error) {
	if err := h.ready(); err != nil {
		return researchcontract.Assessment{}, err
	}
	bound, err := validateInput(in)
	if err != nil {
		return researchcontract.Assessment{}, err
	}
	if err := h.Authority.Check(ctx, researchcontract.CheckInput{
		RunID: in.RunID, Generation: in.Generation,
		Permission: researchcontract.PermissionJevRequest, Now: time.Now(),
	}); err != nil {
		return researchcontract.Assessment{}, err
	}
	profile, rubric, err := h.Briefs.CurrentBrief(ctx, in.RunID)
	if err != nil {
		return researchcontract.Assessment{}, err
	}
	if profile != in.ProfileVersion || rubric != in.RubricVersion {
		return researchcontract.Assessment{}, researchcontract.NewError(researchcontract.OutcomeStale,
			"briefVersion", "request brief does not match the current brief; reframe against the current brief")
	}
	excerpts, err := h.bindCaptures(ctx, bound.refs)
	if err != nil {
		return researchcontract.Assessment{}, err
	}
	requestedModel := h.Provider.RequestedModel()
	reuseKey := reuseKey(bound, in.ProfileVersion, in.RubricVersion, requestedModel, in.Purpose)
	if h.Sink != nil && h.Reuse != nil {
		if reused, ok := h.reusedAssessment(ctx, in.RunID, reuseKey, requestedModel); ok {
			return reused, nil
		}
	}
	request := buildRequest(in, bound, excerpts)
	logical, err := json.Marshal(struct {
		State     any                     `json:"state"`
		Questions map[string]jev.Question `json:"questions"`
	}{request.State, request.Questions})
	if err != nil {
		return researchcontract.Assessment{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"questions", "questions do not encode; no assessment attempted")
	}
	transport, prepErr := h.Provider.EncodedRequest(request)
	sum := sha256.Sum256(logical)
	reservation, err := h.Authority.Reserve(ctx, in.RunID, ReserveOperation, in.IdempotencyKey, payloadHash(in))
	if err != nil {
		return researchcontract.Assessment{}, err
	}
	mark, ok := h.dispatcher()
	if !ok {
		_ = h.Authority.Release(ctx, reservation.ID)
		return researchcontract.Assessment{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"", "no dispatcher marks the reservation dispatched; nothing was dispatched")
	}
	fin, ok := h.finisher()
	if !ok {
		_ = h.Authority.Release(ctx, reservation.ID)
		return researchcontract.Assessment{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"", "no finisher settles the reservation; nothing was dispatched")
	}
	round, err := fin.Round(ctx, in.RunID)
	if err != nil {
		_ = h.Authority.Release(ctx, reservation.ID)
		return researchcontract.Assessment{}, &researchcontract.Error{
			Code: researchcontract.OutcomeInvalid, Detail: "run owner unresolvable; nothing was dispatched",
			Wrapped: err,
		}
	}
	if round.Actor.Kind == "" || round.Actor.ID == "" {
		_ = h.Authority.Release(ctx, reservation.ID)
		return researchcontract.Assessment{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			"", "run has no owning actor; nothing was dispatched")
	}
	if _, err := mark.MarkRoundDispatched(ctx, in.RunID, reservation.AttemptID); err != nil {
		_ = h.Authority.Release(ctx, reservation.ID)
		return researchcontract.Assessment{}, &researchcontract.Error{
			Code: researchcontract.OutcomeInvalid, Detail: "reservation could not be marked dispatched; nothing was dispatched",
			Wrapped: err,
		}
	}
	attempt, err := h.Exchanges.BeginJevAttempt(ctx, store.JevAttemptStart{
		RoundID: in.RunID, RoundAttemptID: reservation.AttemptID, StepIndex: 0,
		Purpose: in.Purpose, InputSHA256: hex.EncodeToString(sum[:]),
		SourceRefsJSON: bound.refsJSON, CandidateSetJSON: bound.candidatesJSON,
		ProfileVersion: in.ProfileVersion, RubricVersion: in.RubricVersion,
		RequestedModel: requestedModel, LogicalRequestJSON: logical,
		TransportRequestBytes: transport,
	})
	if err != nil {
		_ = h.Authority.Release(ctx, reservation.ID)
		return researchcontract.Assessment{}, &researchcontract.Error{
			Code: researchcontract.OutcomeInvalid, Detail: "exchange log rejected the attempt; nothing was dispatched",
			Wrapped: err,
		}
	}
	result, exchange, dispatchErr := jev.Result{}, jev.CapturedExchange{}, prepErr
	if dispatchErr == nil {
		result, exchange, dispatchErr = h.Provider.EvaluateOnceCaptured(ctx, request)
		if dispatchErr == nil && !bytes.Equal(exchange.RequestBytes, transport) {
			dispatchErr = &jev.Error{Kind: jev.ErrInvalidResponse}
			result = jev.Result{}
		}
	}
	if dispatchErr != nil {
		status, kind, outErr := mapProviderError(dispatchErr, attempt.ID)
		h.finishExchange(ctx, attempt.ID, status, exchange, kind)
		if ferr := h.finishRound(ctx, fin, round.Actor, in.RunID, reservation.AttemptID, false, nil, safeErrorCode(dispatchErr)); ferr != nil {
			return researchcontract.Assessment{}, finishFailure(attempt.ID, outErr, ferr)
		}
		return researchcontract.Assessment{}, outErr
	}
	answers, allAnswered, err := bindAnswers(in.Questions, result)
	if err != nil {
		h.finishExchange(ctx, attempt.ID, "succeeded", exchange, "")
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// The answer failed validation, so the round attempt fails as an
		// invalid provider response however the reclassification lands.
		invalidCode := safeErrorCode(&jev.Error{Kind: jev.ErrInvalidResponse})
		if rerr := h.Exchanges.ReclassifyJevAttempt(cleanup, attempt.ID, "invalid_response", "invalid_response"); rerr != nil {
			unreconciled := &researchcontract.Error{
				Code: researchcontract.OutcomeUncertain, Detail: "provider answer failed validation and reclassification failed; attempt " + attempt.ID + " holds the exact bytes; reconcile before retry",
				Wrapped: errors.Join(err, rerr),
			}
			if ferr := h.finishRound(ctx, fin, round.Actor, in.RunID, reservation.AttemptID, false, nil, invalidCode); ferr != nil {
				return researchcontract.Assessment{}, finishFailure(attempt.ID, errors.Join(err, rerr), ferr)
			}
			return researchcontract.Assessment{}, unreconciled
		}
		rejected := &researchcontract.Error{
			Code: researchcontract.OutcomeInvalid, Field: "providerResponse",
			Detail: "provider answer failed validation (" + err.Error() + "); no assessment fabricated; attempt " + attempt.ID + " retains the exact bytes",
		}
		if ferr := h.finishRound(ctx, fin, round.Actor, in.RunID, reservation.AttemptID, false, nil, invalidCode); ferr != nil {
			return researchcontract.Assessment{}, finishFailure(attempt.ID, err, ferr)
		}
		return researchcontract.Assessment{}, rejected
	}
	if _, ferr := h.finishExchange(ctx, attempt.ID, "succeeded", exchange, ""); ferr != nil {
		unlogged := &researchcontract.Error{
			Code: researchcontract.OutcomeUncertain, Detail: "provider result observed but the exchange log write failed; attempt " + attempt.ID + "; reconcile before retry, never auto-replay",
			Wrapped: ferr,
		}
		if rerr := h.finishRound(ctx, fin, round.Actor, in.RunID, reservation.AttemptID, false, nil, safeErrorCode(ferr)); rerr != nil {
			return researchcontract.Assessment{}, finishFailure(attempt.ID, ferr, rerr)
		}
		return researchcontract.Assessment{}, unlogged
	}
	assessment, record := buildAssessment(in, bound, attempt.ID, requestedModel, exchange, reuseKey, answers, allAnswered)
	if h.Sink != nil {
		if h.Supersedes != nil {
			predecessor, perr := h.Supersedes(ctx, in)
			if perr != nil {
				unresolved := &researchcontract.Error{
					Code: researchcontract.OutcomeUncertain, Detail: "assessment computed and exchange " + attempt.ID + " logged, but predecessor resolution failed; reconcile before retry",
					Wrapped: perr,
				}
				if ferr := h.finishRound(ctx, fin, round.Actor, in.RunID, reservation.AttemptID, false, nil, safeErrorCode(perr)); ferr != nil {
					return researchcontract.Assessment{}, finishFailure(attempt.ID, perr, ferr)
				}
				return researchcontract.Assessment{}, unresolved
			}
			record.SupersedesID = predecessor
		}
		if serr := h.Sink.SaveDynamicAssessment(ctx, record); serr != nil {
			unpersisted := &researchcontract.Error{
				Code: researchcontract.OutcomeUncertain, Detail: "assessment computed and exchange " + attempt.ID + " logged, but dynamic-row persistence failed; reconcile before retry",
				Wrapped: serr,
			}
			if ferr := h.finishRound(ctx, fin, round.Actor, in.RunID, reservation.AttemptID, false, nil, safeErrorCode(serr)); ferr != nil {
				return researchcontract.Assessment{}, finishFailure(attempt.ID, serr, ferr)
			}
			return researchcontract.Assessment{}, unpersisted
		}
	}
	if ferr := h.finishRound(ctx, fin, round.Actor, in.RunID, reservation.AttemptID, true, record.AnswersJSON, ""); ferr != nil {
		return researchcontract.Assessment{}, finishFailure(attempt.ID, nil, ferr)
	}
	return assessment, nil
}

func (h *Handler) finishExchange(ctx context.Context, attemptID, status string, exchange jev.CapturedExchange, kind string) (store.JevAttempt, error) {
	var httpStatus *int
	if exchange.HTTPStatus != 0 {
		httpStatus = &exchange.HTTPStatus
	}
	if exchange.ResponseTruncated {
		kind = "response_truncated"
	}
	if exchange.ResponseReadError && status == "succeeded" {
		status, kind = "invalid_response", "response_read_error"
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return h.Exchanges.FinishJevAttempt(cleanup, store.JevAttemptFinish{ID: attemptID,
		Status: status, ReturnedModel: exchange.ReturnedModel, RawResponseBytes: exchange.ResponseBytes,
		ResponseTruncated: exchange.ResponseTruncated, ResponseReadError: exchange.ResponseReadError,
		ErrorKind: kind, HTTPStatus: httpStatus,
		InputTokens: exchange.InputTokens, OutputTokens: exchange.OutputTokens})
}

// finishRound settles the reserved round attempt exactly once over a bounded
// cleanup context: the caller context may already be canceled (notably the
// stopped path), and a canceled context must not strand a dispatched attempt
// to flip uncertain at Stop. Callers surface any error honestly; success is
// never reported when the ledger write failed.
func (h *Handler) finishRound(ctx context.Context, fin Finisher, actor store.Actor, runID, attemptID string, success bool, result json.RawMessage, errorCode string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := fin.FinishRoundAttempt(cleanup, actor, runID, attemptID, success, result, errorCode)
	return err
}

// finishFailure converts a failed round-attempt finish into the honest
// terminal error: the ledger write failed, so the outcome is uncertain
// regardless of the path that led here (a Stop-race fence included). The
// path error joins for reconciliation context; success is never faked.
func finishFailure(attemptID string, pathErr, finishErr error) error {
	return &researchcontract.Error{
		Code:    researchcontract.OutcomeUncertain,
		Detail:  "round-attempt finish failed for attempt " + attemptID + "; reconcile before retry, never auto-replay",
		Wrapped: errors.Join(pathErr, finishErr),
	}
}

// safeErrorCode maps a terminal failure to a bounded round error code,
// mirroring jevservice: provider kinds travel verbatim, allowance and fence
// failures keep their signal, everything else is a plain failure.
func safeErrorCode(err error) string {
	var provider *jev.Error
	if errors.As(err, &provider) {
		return "jev_" + string(provider.Kind)
	}
	if errors.Is(err, store.ErrAllowance) {
		return "jev_allowance"
	}
	if errors.Is(err, store.ErrFenced) {
		return "jev_fenced"
	}
	return "jev_failed"
}

// mapProviderError converts a dispatch failure into an exchange status, an
// error kind, and an explicit typed error. Nothing is retried internally.
func mapProviderError(err error, attemptID string) (string, string, error) {
	var provider *jev.Error
	if !errors.As(err, &provider) {
		return "failed", "internal_error", researchcontract.NewError(researchcontract.OutcomeInvalid,
			"", "jev provider error; no assessment fabricated; attempt "+attemptID+" retains the exchange")
	}
	switch provider.Kind {
	case jev.ErrInvalidResponse:
		return "invalid_response", string(provider.Kind), researchcontract.NewError(researchcontract.OutcomeInvalid,
			"providerResponse", "jev returned a malformed response; no assessment fabricated; attempt "+attemptID+" retains the exact bytes")
	case jev.ErrRateLimited:
		return "failed", string(provider.Kind), researchcontract.NewError(researchcontract.OutcomeRateLimited,
			"", "jev rate limited; retry explicitly with a new idempotency key and a fresh reservation")
	case jev.ErrTimeout, jev.ErrTransport, jev.ErrUnavailable:
		return "uncertain", string(provider.Kind), researchcontract.NewError(researchcontract.OutcomeUncertain,
			"", "jev outcome uncertain ("+string(provider.Kind)+"); attempt "+attemptID+" recorded; reconcile before retry, never auto-replay")
	case jev.ErrCanceled:
		return "failed", string(provider.Kind), researchcontract.NewError(researchcontract.OutcomeStopped,
			"", "jev call stopped; attempt "+attemptID+" recorded; retry explicitly if still wanted")
	case jev.ErrBudgetExceeded:
		return "budget_exceeded", string(provider.Kind), researchcontract.NewError(researchcontract.OutcomeBudgetExhausted,
			"", "jev budget exceeded; attempt "+attemptID+" recorded")
	case jev.ErrUnauthorized:
		return "failed", string(provider.Kind), researchcontract.NewError(researchcontract.OutcomeForbidden,
			"", "jev provider rejected credentials; attempt "+attemptID+" recorded")
	case jev.ErrDisabled, jev.ErrMissingKey, jev.ErrInvalidConfig:
		return "failed", string(provider.Kind), researchcontract.NewError(researchcontract.OutcomeInvalid,
			"", "jev provider not configured ("+string(provider.Kind)+"); attempt "+attemptID+" recorded")
	default:
		return "failed", string(provider.Kind), researchcontract.NewError(researchcontract.OutcomeInvalid,
			"", "jev request failed ("+string(provider.Kind)+"); no assessment fabricated; attempt "+attemptID+" retains the exchange")
	}
}

// boundInput is validated input plus canonical encodings.
type boundInput struct {
	questions      []researchcontract.AssessQuestion
	refs           []researchcontract.EvidenceRef // union of source + per-alternative refs, sorted, deduped
	refsJSON       []byte
	candidatesJSON []byte
	candidateHash  string
}

func validateInput(in researchcontract.AssessInput) (boundInput, error) {
	var bound boundInput
	invalid := func(field, detail string) (boundInput, error) {
		return boundInput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, field, detail)
	}
	if in.Purpose == "" || strings.TrimSpace(in.Purpose) != in.Purpose || len(in.Purpose) > MaxPurpose {
		return invalid("purpose", "purpose must be trimmed non-empty text")
	}
	if in.RunID == "" || len(in.RunID) > MaxTextField {
		return invalid("runId", "runId is required")
	}
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > MaxTextField {
		return invalid("idempotencyKey", "idempotencyKey is required")
	}
	if in.ProfileVersion < 1 {
		return invalid("profileVersion", "profileVersion must be >= 1")
	}
	if in.RubricVersion == "" || strings.TrimSpace(in.RubricVersion) != in.RubricVersion || len(in.RubricVersion) > MaxRubric {
		return invalid("rubricVersion", "rubricVersion must be trimmed non-empty text")
	}
	if len(in.Questions) == 0 || len(in.Questions) > MaxQuestions {
		return invalid("questions", "at least one question is required")
	}
	if len(in.SourceRefs) == 0 {
		return invalid("sourceRefs", "at least one cited capture is required; questions are answered from cited captures only")
	}
	for _, c := range in.CandidateIdentities {
		if c.CandidateID == "" || len(c.CandidateID) > MaxTextField {
			return invalid("candidateIdentities", "candidateId is required")
		}
		if c.Kind != "company" && c.Kind != "opportunity" {
			return invalid("candidateIdentities", "candidate kind must be company|opportunity")
		}
		if c.Revision < 1 {
			return invalid("candidateIdentities", "candidate revision must be >= 1")
		}
	}
	seenQuestions := map[string]struct{}{}
	for _, q := range in.Questions {
		if q.ID == "" || len(q.ID) > MaxTextField {
			return invalid("questions", "question id is required")
		}
		if _, dup := seenQuestions[q.ID]; dup {
			return invalid("questions", "duplicate question id "+q.ID)
		}
		seenQuestions[q.ID] = struct{}{}
		if q.Text == "" || len(q.Text) > MaxQuestionText {
			return invalid("questions", "question "+q.ID+" needs non-empty text")
		}
		if !q.AbstainAllowed {
			return invalid("questions", "question "+q.ID+" must allow abstention")
		}
		if len(q.Alternatives) == 0 || len(q.Alternatives) > MaxAlternatives {
			return invalid("questions", "question "+q.ID+" needs at least one alternative")
		}
		seenAlts := map[string]struct{}{}
		perAlt := map[string]string{} // canonical ref key -> alternative id, for conflict detection
		for _, a := range q.Alternatives {
			if a.ID == "" || len(a.ID) > MaxTextField {
				return invalid("questions", "question "+q.ID+" has an alternative with no id")
			}
			if a.ID == AbstainID {
				return invalid("questions", "question "+q.ID+": "+AbstainID+" is reserved")
			}
			if _, dup := seenAlts[a.ID]; dup {
				return invalid("questions", "question "+q.ID+" has a duplicate alternative id "+a.ID)
			}
			seenAlts[a.ID] = struct{}{}
			if a.Label == "" || len(a.Label) > MaxLabel {
				return invalid("questions", "question "+q.ID+" alternative "+a.ID+" needs non-empty label")
			}
			for _, r := range a.EvidenceRefs {
				if err := validateRef(r); err != nil {
					return invalid("questions", "question "+q.ID+" alternative "+a.ID+": "+err.Error())
				}
				key := refKey(r)
				if owner, ok := perAlt[key]; ok && owner != a.ID {
					return boundInput{}, researchcontract.NewError(researchcontract.OutcomeConflict,
						"questions", "question "+q.ID+": one capture span cannot support two competing alternatives ("+owner+" vs "+a.ID+")")
				}
				perAlt[key] = a.ID
			}
		}
	}
	for _, r := range in.SourceRefs {
		if err := validateRef(r); err != nil {
			return invalid("sourceRefs", err.Error())
		}
	}
	refs := unionRefs(in.SourceRefs, in.Questions)
	if len(refs) > MaxEvidenceRefs {
		return invalid("sourceRefs", "too many distinct evidence refs")
	}
	bound.questions = in.Questions
	bound.refs = refs
	bound.refsJSON = canonicalRefsJSON(refs)
	bound.candidatesJSON = canonicalCandidatesJSON(in.CandidateIdentities)
	bound.candidateHash = candidateSetHash(in.CandidateIdentities)
	return bound, nil
}

func validateRef(r researchcontract.EvidenceRef) error {
	if r.CaptureID == "" || len(r.CaptureID) > MaxTextField {
		return errors.New("captureId is required")
	}
	if r.SpanStart < 0 || r.SpanEnd <= r.SpanStart {
		return errors.New("span_end must exceed span_start with span_start >= 0")
	}
	if r.SpanEnd-r.SpanStart > MaxSpanBytes {
		return errors.New("span exceeds the excerpt bound")
	}
	return nil
}

func refKey(r researchcontract.EvidenceRef) string {
	return r.CaptureID + "\x00" + itoa(r.SpanStart) + "\x00" + itoa(r.SpanEnd)
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}

// unionRefs merges source refs with every per-alternative ref, sorted and
// deduped. The union is the full dependency set behind the reuse key.
func unionRefs(source []researchcontract.EvidenceRef, questions []researchcontract.AssessQuestion) []researchcontract.EvidenceRef {
	seen := map[string]struct{}{}
	var out []researchcontract.EvidenceRef
	add := func(r researchcontract.EvidenceRef) {
		if _, dup := seen[refKey(r)]; dup {
			return
		}
		seen[refKey(r)] = struct{}{}
		out = append(out, r)
	}
	for _, r := range source {
		add(r)
	}
	for _, q := range questions {
		for _, a := range q.Alternatives {
			for _, r := range a.EvidenceRefs {
				add(r)
			}
		}
	}
	slices.SortFunc(out, func(a, b researchcontract.EvidenceRef) int {
		if a.CaptureID != b.CaptureID {
			return strings.Compare(a.CaptureID, b.CaptureID)
		}
		if a.SpanStart != b.SpanStart {
			return compareInt(a.SpanStart, b.SpanStart)
		}
		return compareInt(a.SpanEnd, b.SpanEnd)
	})
	return out
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

type canonicalRef struct {
	CaptureID string `json:"capture_id"`
	SpanStart int64  `json:"span_start"`
	SpanEnd   int64  `json:"span_end"`
}

func canonicalRefsJSON(refs []researchcontract.EvidenceRef) []byte {
	out := make([]canonicalRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, canonicalRef{CaptureID: r.CaptureID, SpanStart: r.SpanStart, SpanEnd: r.SpanEnd})
	}
	raw, _ := json.Marshal(out)
	return raw
}

type canonicalCandidate struct {
	CandidateID string `json:"candidate_id"`
	Kind        string `json:"kind"`
	Revision    int64  `json:"revision"`
}

func canonicalCandidates(in []researchcontract.CandidateIdentity) []canonicalCandidate {
	out := make([]canonicalCandidate, 0, len(in))
	for _, c := range in {
		out = append(out, canonicalCandidate{CandidateID: c.CandidateID, Kind: c.Kind, Revision: c.Revision})
	}
	slices.SortFunc(out, func(a, b canonicalCandidate) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.CandidateID, b.CandidateID)
	})
	return out
}

func canonicalCandidatesJSON(in []researchcontract.CandidateIdentity) []byte {
	raw, _ := json.Marshal(canonicalCandidates(in))
	return raw
}

// candidateSetHash is sha256 over canonical entries sorted by
// (kind, candidateId) with revisions included (T06 §6 C2).
func candidateSetHash(in []researchcontract.CandidateIdentity) string {
	sum := sha256.Sum256(canonicalCandidatesJSON(in))
	return hex.EncodeToString(sum[:])
}

type canonicalAlternative struct {
	ID           string         `json:"id"`
	Label        string         `json:"label"`
	EvidenceRefs []canonicalRef `json:"evidence_refs"`
}

type canonicalQuestion struct {
	ID             string                 `json:"id"`
	Text           string                 `json:"text"`
	Alternatives   []canonicalAlternative `json:"alternatives"`
	AbstainAllowed bool                   `json:"abstain_allowed"`
}

type canonicalReuse struct {
	EvidenceRefs   []canonicalRef      `json:"evidence_refs"`
	Questions      []canonicalQuestion `json:"questions"`
	ProfileVersion int64               `json:"profile_version"`
	RubricVersion  string              `json:"rubric_version"`
	RequestedModel string              `json:"requested_model"`
	Purpose        string              `json:"purpose"`
}

// reuseKey = sha256(canonical(evidence_refs + questions/alternatives +
// profile/rubric + requested_model + purpose)) (T06 §6). Evidence refs are
// the sorted union of source and per-alternative refs; questions carry their
// per-alternative refs, so every binding input affects the key.
func reuseKey(bound boundInput, profile int64, rubric, requestedModel, purpose string) string {
	refs := make([]canonicalRef, 0, len(bound.refs))
	for _, r := range bound.refs {
		refs = append(refs, canonicalRef{CaptureID: r.CaptureID, SpanStart: r.SpanStart, SpanEnd: r.SpanEnd})
	}
	questions := make([]canonicalQuestion, 0, len(bound.questions))
	for _, q := range bound.questions {
		alts := make([]canonicalAlternative, 0, len(q.Alternatives))
		for _, a := range q.Alternatives {
			arefs := make([]canonicalRef, 0, len(a.EvidenceRefs))
			for _, r := range a.EvidenceRefs {
				arefs = append(arefs, canonicalRef{CaptureID: r.CaptureID, SpanStart: r.SpanStart, SpanEnd: r.SpanEnd})
			}
			slices.SortFunc(arefs, func(x, y canonicalRef) int {
				if x.CaptureID != y.CaptureID {
					return strings.Compare(x.CaptureID, y.CaptureID)
				}
				if x.SpanStart != y.SpanStart {
					return compareInt(x.SpanStart, y.SpanStart)
				}
				return compareInt(x.SpanEnd, y.SpanEnd)
			})
			alts = append(alts, canonicalAlternative{ID: a.ID, Label: a.Label, EvidenceRefs: arefs})
		}
		slices.SortFunc(alts, func(x, y canonicalAlternative) int { return strings.Compare(x.ID, y.ID) })
		questions = append(questions, canonicalQuestion{ID: q.ID, Text: q.Text, Alternatives: alts, AbstainAllowed: q.AbstainAllowed})
	}
	slices.SortFunc(questions, func(x, y canonicalQuestion) int { return strings.Compare(x.ID, y.ID) })
	raw, _ := json.Marshal(canonicalReuse{EvidenceRefs: refs, Questions: questions,
		ProfileVersion: profile, RubricVersion: rubric, RequestedModel: requestedModel, Purpose: purpose})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// payloadHash covers the full caller payload for stable reservation replay:
// same key + same payload replays, same key + different payload conflicts.
// Scope keys (run, generation, idempotency key) are excluded.
func payloadHash(in researchcontract.AssessInput) string {
	refs := make([]canonicalRef, 0, len(in.SourceRefs))
	for _, r := range in.SourceRefs {
		refs = append(refs, canonicalRef{CaptureID: r.CaptureID, SpanStart: r.SpanStart, SpanEnd: r.SpanEnd})
	}
	slices.SortFunc(refs, func(x, y canonicalRef) int {
		if x.CaptureID != y.CaptureID {
			return strings.Compare(x.CaptureID, y.CaptureID)
		}
		if x.SpanStart != y.SpanStart {
			return compareInt(x.SpanStart, y.SpanStart)
		}
		return compareInt(x.SpanEnd, y.SpanEnd)
	})
	questions := make([]canonicalQuestion, 0, len(in.Questions))
	for _, q := range in.Questions {
		alts := make([]canonicalAlternative, 0, len(q.Alternatives))
		for _, a := range q.Alternatives {
			arefs := make([]canonicalRef, 0, len(a.EvidenceRefs))
			for _, r := range a.EvidenceRefs {
				arefs = append(arefs, canonicalRef{CaptureID: r.CaptureID, SpanStart: r.SpanStart, SpanEnd: r.SpanEnd})
			}
			alts = append(alts, canonicalAlternative{ID: a.ID, Label: a.Label, EvidenceRefs: arefs})
		}
		questions = append(questions, canonicalQuestion{ID: q.ID, Text: q.Text, Alternatives: alts, AbstainAllowed: q.AbstainAllowed})
	}
	raw, _ := json.Marshal(struct {
		Purpose        string               `json:"purpose"`
		Questions      []canonicalQuestion  `json:"questions"`
		ProfileVersion int64                `json:"profile_version"`
		RubricVersion  string               `json:"rubric_version"`
		Candidates     []canonicalCandidate `json:"candidates"`
		SourceRefs     []canonicalRef       `json:"source_refs"`
	}{in.Purpose, questions, in.ProfileVersion, in.RubricVersion, canonicalCandidates(in.CandidateIdentities), refs})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// excerpt is one bound capture span snapshotted for the judge.
type excerpt struct {
	CaptureID string `json:"capture_id"`
	SpanStart int64  `json:"span_start"`
	SpanEnd   int64  `json:"span_end"`
	Text      string `json:"text"`
}

// bindCaptures resolves every cited ref against immutable captures: missing
// captures fail, incomplete captures fail, and spans must lie within the
// stored bytes. Each capture is read once.
func (h *Handler) bindCaptures(ctx context.Context, refs []researchcontract.EvidenceRef) ([]excerpt, error) {
	bodies := map[string][]byte{}
	var out []excerpt
	for _, r := range refs {
		body, ok := bodies[r.CaptureID]
		if !ok {
			capture, reader, err := h.Captures.OpenCapture(ctx, r.CaptureID)
			if err != nil {
				var contractErr *researchcontract.Error
				if errors.As(err, &contractErr) {
					return nil, err
				}
				return nil, researchcontract.NewError(researchcontract.OutcomeNotFound,
					"sourceRefs", "capture "+r.CaptureID+" is not bound to an immutable capture")
			}
			body, err = io.ReadAll(io.LimitReader(reader, MaxCaptureBytes+1))
			_ = reader.Close()
			if err != nil {
				return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
					"sourceRefs", "capture "+r.CaptureID+" bytes are unreadable")
			}
			if int64(len(body)) > MaxCaptureBytes {
				return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
					"sourceRefs", "capture "+r.CaptureID+" exceeds the readable bound")
			}
			if capture.ID != "" && capture.ID != r.CaptureID {
				return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
					"sourceRefs", "capture descriptor does not match the cited id")
			}
			if !capture.Complete {
				return nil, researchcontract.NewError(researchcontract.OutcomeCaptureIncomplete,
					"sourceRefs", "capture "+r.CaptureID+" is incomplete; no assessment from partial bytes")
			}
			bodies[r.CaptureID] = body
		}
		if r.SpanEnd > int64(len(body)) {
			return nil, researchcontract.NewError(researchcontract.OutcomeInvalid,
				"sourceRefs", "span exceeds the stored bytes of capture "+r.CaptureID)
		}
		out = append(out, excerpt{CaptureID: r.CaptureID, SpanStart: r.SpanStart,
			SpanEnd: r.SpanEnd, Text: string(body[r.SpanStart:r.SpanEnd])})
	}
	return out, nil
}

// buildRequest maps Codex questions to Jev choice primitives. Question text
// and alternative labels pass through verbatim; the only addition is the
// reserved abstain option. State carries the snapshotted excerpts only.
func buildRequest(in researchcontract.AssessInput, bound boundInput, excerpts []excerpt) jev.Request {
	questions := make(map[string]jev.Question, len(bound.questions))
	for _, q := range bound.questions {
		criteria := make(map[string]string, len(q.Alternatives)+1)
		for _, a := range q.Alternatives {
			criteria[a.ID] = a.Label
		}
		criteria[AbstainID] = AbstainLabel
		questions[q.ID] = jev.Choice(q.Text, criteria)
	}
	return jev.Request{
		State: struct {
			Purpose        string               `json:"purpose"`
			ProfileVersion int64                `json:"profile_version"`
			RubricVersion  string               `json:"rubric_version"`
			Excerpts       []excerpt            `json:"excerpts"`
			Candidates     []canonicalCandidate `json:"candidates"`
		}{in.Purpose, in.ProfileVersion, in.RubricVersion, excerpts, canonicalCandidates(in.CandidateIdentities)},
		Questions: questions,
	}
}

// bindAnswers validates every provider answer against the asked questions:
// unknown question or answer ids fail explicitly, abstentions carry no
// bindings, and answered questions bind exactly the chosen alternative's
// cited refs. Nothing is invented.
func bindAnswers(questions []researchcontract.AssessQuestion, result jev.Result) ([]researchcontract.AssessAnswer, bool, error) {
	byID := map[string]researchcontract.AssessQuestion{}
	for _, q := range questions {
		byID[q.ID] = q
	}
	if len(result.Answers) != len(questions) {
		return nil, false, errors.New("answer count does not match the question count")
	}
	answers := make([]researchcontract.AssessAnswer, 0, len(questions))
	allAnswered := true
	for _, q := range questions {
		answer, ok := result.Answers[q.ID]
		if !ok || answer.Type != "choice" || answer.Choice == nil {
			return nil, false, errors.New("missing or mistyped answer for question " + q.ID)
		}
		choice := answer.Choice.Choice
		if choice == AbstainID {
			allAnswered = false
			answers = append(answers, researchcontract.AssessAnswer{QuestionID: q.ID,
				Abstained: true, Uncertainty: uncertainty(answer.Choice.Confidence)})
			continue
		}
		var alt *researchcontract.AssessAlternative
		for i := range byID[q.ID].Alternatives {
			if byID[q.ID].Alternatives[i].ID == choice {
				alt = &byID[q.ID].Alternatives[i]
			}
		}
		if alt == nil {
			return nil, false, errors.New("unknown answer id for question " + q.ID)
		}
		bindings := append([]researchcontract.EvidenceRef(nil), alt.EvidenceRefs...)
		answers = append(answers, researchcontract.AssessAnswer{QuestionID: q.ID,
			AnswerID: choice, Uncertainty: uncertainty(answer.Choice.Confidence), SourceBindings: bindings})
	}
	return answers, allAnswered, nil
}

// uncertainty renders the provider confidence losslessly. Bands would invent
// meaning; the raw confidence travels with the exact exchange instead.
func uncertainty(confidence float64) string {
	return fmt.Sprintf("confidence %.4f", confidence)
}

// reusedAssessment returns the stored answers for a reuse-key hit. A miss,
// a non-succeeded row or undecodable JSON falls through to a fresh
// assessment; reuse never fails the ask. Usage stays zero: nothing was
// spent. ModelVersion falls back to the requested model because the
// persisted row does not record the provider's returned version.
func (h *Handler) reusedAssessment(ctx context.Context, runID, reuseKey, requestedModel string) (researchcontract.Assessment, bool) {
	stored, err := h.Reuse(ctx, runID, reuseKey)
	if err != nil || stored.Status != "succeeded" {
		return researchcontract.Assessment{}, false
	}
	var answers []researchcontract.AssessAnswer
	if err := json.Unmarshal([]byte(stored.AnswersJSON), &answers); err != nil || len(answers) == 0 {
		return researchcontract.Assessment{}, false
	}
	var refs []researchcontract.EvidenceRef
	if err := json.Unmarshal([]byte(stored.EvidenceRefsJSON), &refs); err != nil {
		return researchcontract.Assessment{}, false
	}
	return researchcontract.Assessment{ID: stored.ID, Results: answers,
		Model: requestedModel, ModelVersion: requestedModel,
		ReuseKey: reuseKey, EvidenceRefs: refs}, true
}

func buildAssessment(in researchcontract.AssessInput, bound boundInput, attemptID, requestedModel string,
	exchange jev.CapturedExchange, reuseKey string, answers []researchcontract.AssessAnswer, allAnswered bool) (researchcontract.Assessment, DynamicAssessmentRecord) {
	id := newAssessmentID()
	modelVersion := exchange.ReturnedModel
	if modelVersion == "" {
		modelVersion = requestedModel
	}
	usage := researchcontract.ExecuteUsage{Requests: 1,
		Bytes: int64(len(exchange.RequestBytes) + len(exchange.ResponseBytes))}
	assessment := researchcontract.Assessment{ID: id, Results: answers,
		Model: requestedModel, ModelVersion: modelVersion, Usage: usage,
		ReuseKey: reuseKey, EvidenceRefs: append([]researchcontract.EvidenceRef(nil), bound.refs...)}
	answersJSON, _ := json.Marshal(answers)
	questionsJSON, _ := json.Marshal(bound.questions)
	status := "succeeded"
	if !allAnswered {
		status = "partial_abstain"
	}
	record := DynamicAssessmentRecord{ID: id, RunID: in.RunID, JevAttemptID: attemptID,
		Purpose: in.Purpose, QuestionsJSON: questionsJSON, EvidenceRefsJSON: bound.refsJSON,
		ProfileVersion: in.ProfileVersion, RubricVersion: in.RubricVersion,
		CandidatesJSON: bound.candidatesJSON, CandidateSetHash: bound.candidateHash,
		RequestedModel: requestedModel, ReuseKey: reuseKey, Status: status,
		AnswersJSON: answersJSON, Model: requestedModel, ModelVersion: modelVersion, Usage: usage}
	return assessment, record
}

func newAssessmentID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return "jda_" + hex.EncodeToString(raw[:])
}
