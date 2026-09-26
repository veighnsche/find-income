package materialprep

import (
	"context"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// Standard preparation adapter. Drafting and explicit rewrites run through
// one bounded private Muse Standard turn per operation (see StandardRunner):
// the adapter builds a musecode.StandardInput ONLY from verified owner
// facts (Purpose/BundleRef/Context/Targets), and the preparation caller
// validates the returned texts as untrusted (scope, citations, shape).
//
// Construction rule: every StandardInput carries verified facts only.
// Context["prompt"] is the verified-fact prompt bytes assembled by the
// caller (saved answers, pinned career sources, profile scalars, current
// texts); it never carries employer contact handles, credentials, send
// authority, discovery criteria, or Jev/Answer routing. Purpose is fixed
// per operation, BundleRef is the verified CheckID, and Targets are the
// pinned in-scope ids (for logging/fencing only, never expanded). The
// adapter has no route into discovery, checks, or Answer: its runner
// interface accepts ONLY musecode.StandardInput (TierStandard,
// private-only), and this package never imports publicresearch,
// researchwire, researchexecute, researchmemory, jevservice answer
// matching, httpapi, or codexservice for the Standard path.
//
// Direct seam (R1, landed): musewire.StandardRunnerFactory is the
// production StandardRunner. Each RunStandard call conducts exactly one
// `muse exec --json --output-schema <purpose schema> --max-model-steps N
// --model muse-spark-1.3` turn with web tools disabled, folds its JSONL to
// one structured final text, and returns it unvalidated. Bounds come from
// the factory config; a breach fails the run closed with no retry. An
// unproved Standard lane reports musecode.ErrSessionUnavailable (mapped to
// 503 upstream); the path never falls back to Contributor or Codex. Exact
// byte edits bypass the runner entirely (zero model calls); explicit
// rewrites re-enter through it and create a new reviewable version.
//
// Provider-disabled fixtures supply a fake StandardRunner (see tests); no
// live model, Jev, network, send, or secrets are touched on this path.

const (
	// StandardDraftPurpose selects grounded required-answer drafting.
	// No producer exists yet: K3/M4 reintroduce required-answer
	// drafting against this purpose.
	StandardDraftPurpose = "prepare-draft-required-answers"
	// StandardRewritePurpose selects explicit owner-requested rewrite.
	// No producer exists yet: M5 reintroduces targeted rewrite against
	// this purpose.
	StandardRewritePurpose = "prepare-rewrite-materials"
)

// StandardResult is the collected model output of one bounded Standard turn.
// Messages carry the raw model texts (JSON object, raw or single-fenced);
// validation treats them as untrusted.
type StandardResult struct {
	Messages []string
}

// StandardRunner runs one bounded Standard turn over a verified-fact input.
// Implementations are injected: fakes in fixtures, the supervisor-backed
// factory in production (see the direct seam above). Exactly one call is
// made per draft or rewrite; no retries.
type StandardRunner interface {
	RunStandard(ctx context.Context, input musecode.StandardInput) (StandardResult, error)
}

// StandardDrafter is the production drafter. Runner is required; a nil
// runner reports ErrUnavailable so the HTTP layer stays honestly 503.
// Operation methods (DraftArtifacts, and later required-answer drafting
// and targeted rewrite) live with their request shapes.
type StandardDrafter struct {
	Runner StandardRunner
}
