package materialprep

import (
	"context"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Standard preparation adapter (E09). This file replaces the legacy Codex
// one-shot binding for drafting and rewrite with a Muse Standard adapter
// that constructs musecode.StandardInput only.
//
// Construction rule: every StandardInput is built ONLY from verified owner
// facts in the DraftRequest or rewrite pins (Purpose/BundleRef/Context/
// Targets). Context carries the verified-fact prompt bytes assembled by
// draftPrompt/rewritePrompt (saved answers, pinned career sources, profile
// scalars, current texts); it never carries employer contact handles,
// credentials, send authority, discovery criteria, or Jev/Answer routing.
// Targets are the pinned question ids in scope. Purpose and BundleRef are
// fixed/verified (draft vs rewrite, CheckID). By construction the adapter
// has no route into discovery, checks, or Answer: its runner interface
// accepts ONLY musecode.StandardInput (TierStandard, private-only), never
// musecode.PublicInput, and this package never imports publicresearch,
// researchwire, researchexecute, researchmemory, jevservice answer matching,
// httpapi, or codexservice for the Standard path.
//
// Validation is preserved: scope fencing (draftScope/checkDrafts), citation
// exactness against pinned bodies (checkModelDrafts +
// applicationpacks.ValidateInput), grounded required answers, optional
// blanks, missing-fact holds, exact byte edits with zero model calls, and
// explicit rewrite creating a new reviewable version. Draft and rewrite
// share one Standard entry point (StandardRunner) with exactly one call per
// operation (bounded turns); failures fail closed with no retry.
//
// E13 SEAM (exact, for M — main implementer):
//   1. Implement StandardRunner with a supervisor-backed runner in M-owned
//      code (NOT in materialprep). Suggested shape:
//
//        type SupervisorStandardRunner struct {
//          Supervisor *musecode.Supervisor
//          Spec       musecode.SessionSpec // TierStandard, private workspace, Bounds
//          Facts      musecode.Facts       // pinned effective model + proved lane
//        }
//        func (r *SupervisorStandardRunner) RunStandard(ctx context.Context, input musecode.StandardInput) (materialprep.StandardResult, error)
//
//      The implementation must:
//      - Admit ONLY TierStandard: build Spec via musecode.NewSession(
//        musecode.Check(musecode.TierStandard, facts), workspace, bounds, peers)
//        with an absolute private workspace disjoint from every Contributor
//        workspace, and bounds from musecode.DefaultBounds() narrowed as
//        needed (bounds.Validate must pass).
//      - Call Supervisor.StartRun(ctx, runRef, spec, input, currentFacts)
//        with input.inputTier()==TierStandard and currentFacts matching the
//        pinned model/lane; do NOT start Contributor, discovery, checks, or
//        Answer work from this path.
//      - Translate StandardInput to CLI session input as: Purpose selects
//        the trusted discipline (draft vs rewrite, equivalent to the legacy
//        DraftInstructions/RewriteInstructions but hardcoded in M-owned
//        runner code, never from caller input); Context["prompt"] carries
//        the verified-fact prompt bytes; Targets are the question ids in
//        scope (for logging/fencing only, never expanded).
//      - Collect the single-turn model texts and return them as
//        StandardResult.Messages (same shape as the legacy one-shot
//        Messages: one JSON object, raw or single-fenced). NOTE: the frozen
//        musecode.Event currently has no dedicated model-text payload
//        (only Tool/BytesOut/SaveRef/Detail); M at E13 must define and
//        document the text return path (e.g. Detail-carried chunks, a new
//        EventKind, or a Transport extension) in M-owned musecode code.
//        Enforce Bounds via the Supervisor (model-step, tool-call, byte,
//        wall-clock); bound breach fails the run closed.
//      - Return musecode.ErrSessionUnavailable (mapped to 503 upstream) when
//        readiness is not proved; never fall back to Contributor or Codex.
//   2. Wire main.go (M-owned, apps/api/cmd/server/main.go): replace the
//      legacy `drafter = &materialprep.CodexDrafter{Turns: &codexservice.OneShot{...}}`
//      with `drafter = &materialprep.StandardDrafter{Runner: <M runner>}`.
//      Delete the JOBSEEK_CODEX_MODEL/EFFORT branch for materials; gate on
//      Standard readiness instead. Remove the legacy CodexDrafter,
//      OneShotTurn, DraftInstructions, and RewriteInstructions types (marked
//      TODO E13) from materialprep once main.go no longer references them.
//   3. No changes to httpapi routes, store schemas, or applicationpacks
//      validation are needed for E09; E13 integrates review/send on top.
//
// Provider-disabled fixtures supply a fake StandardRunner (see tests); no
// live model, Jev, network, send, or secrets are touched on this path.

const (
	// StandardDraftPurpose selects grounded required-answer drafting.
	StandardDraftPurpose = "prepare-draft-required-answers"
	// StandardRewritePurpose selects explicit owner-requested rewrite.
	StandardRewritePurpose = "prepare-rewrite-materials"
)

// StandardResult is the collected model output of one bounded Standard turn.
// Messages carry the raw model texts (JSON object, raw or single-fenced);
// validation (checkModelDrafts/checkModelRewrite) treats them as untrusted.
type StandardResult struct {
	Messages []string
}

// StandardRunner runs one bounded Standard turn over a verified-fact input.
// Implementations are injected: fakes in fixtures, M's supervisor-backed
// runner in production (see E13 seam above). Exactly one call is made per
// draft or rewrite; no retries.
type StandardRunner interface {
	RunStandard(ctx context.Context, input musecode.StandardInput) (StandardResult, error)
}

// StandardDrafter is the production Drafter. Runner is required; a nil
// runner reports ErrUnavailable so the HTTP layer stays honestly 503.
type StandardDrafter struct {
	Runner StandardRunner
}

var _ Drafter = (*StandardDrafter)(nil)

// DraftRequiredAnswers drafts the required+unset questions in one bounded
// Standard turn. It returns a subset when the model omits unsupported
// questions (they stay held) and an empty slice when nothing is drafted.
func (d *StandardDrafter) DraftRequiredAnswers(ctx context.Context, request DraftRequest) ([]RequiredDraft, error) {
	if d == nil || d.Runner == nil {
		return nil, ErrUnavailable
	}
	scope, err := draftScope(request)
	if err != nil {
		return nil, err
	}
	sources, err := draftSources(request.CareerSources)
	if err != nil {
		return nil, err
	}
	result, err := d.Runner.RunStandard(ctx, buildStandardDraftInput(request))
	if err != nil {
		return nil, err
	}
	return checkModelDrafts(scope, sources, result.Messages)
}

// buildStandardDraftInput packages verified facts into a private Standard
// session input. Purpose is fixed, BundleRef is the verified CheckID,
// Context["prompt"] is the verified-fact prompt bytes (no contact handles,
// credentials, send authority, discovery, or Answer routing), and Targets
// are the required+unset question ids in scope.
func buildStandardDraftInput(request DraftRequest) musecode.StandardInput {
	targets := make([]string, 0, len(request.RequiredUnset))
	for _, question := range request.RequiredUnset {
		targets = append(targets, question.ID)
	}
	return musecode.StandardInput{
		Purpose:   StandardDraftPurpose,
		BundleRef: request.CheckID,
		Context:   map[string]string{"prompt": draftPrompt(request)},
		Targets:   targets,
	}
}

// buildStandardRewriteInput packages one explicit rewrite into a private
// Standard session input. Purpose is fixed, BundleRef is the verified
// CheckID, Context["prompt"] is the verified-fact rewrite prompt (owner
// instruction, current texts, saved answers, career sources, profile;
// no contact handles or send authority), and Targets are every pinned
// question id exactly once.
func buildStandardRewriteInput(checkID, instruction string, questions []store.CheckQuestionView, current map[string]string, combined string, answered []AnsweredFact, saved []SavedAnswerFact, career []applicationpacks.Source, profile store.Preferences) musecode.StandardInput {
	targets := make([]string, 0, len(questions))
	for _, question := range questions {
		targets = append(targets, question.ID)
	}
	return musecode.StandardInput{
		Purpose:   StandardRewritePurpose,
		BundleRef: checkID,
		Context: map[string]string{"prompt": rewritePrompt(instruction, questions,
			current, combined, answered, saved, career, profile)},
		Targets: targets,
	}
}

// standardRewriteRunner returns the shared Standard runner behind the
// Service's Draft collaborator. Rewrite owns no model dials and builds no
// turn primitive of its own: it reuses the production Standard drafter's
// runner, so rewrite can never run without the drafting path's bounded
// primitive and can never run implicitly. A nil or foreign Drafter reports
// ErrUnavailable instead of failing open.
func standardRewriteRunner(draft Drafter) (StandardRunner, error) {
	drafter, ok := draft.(*StandardDrafter)
	if !ok || drafter == nil || drafter.Runner == nil {
		return nil, ErrUnavailable
	}
	return drafter.Runner, nil
}
