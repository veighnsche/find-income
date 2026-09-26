// Package musecode freezes the app's contract for driving the locally
// installed Muse Code CLI. E01 pins identifiers, DTO surfaces, bounds and
// terminal semantics; later tasks implement supervision (E02), public tools
// (E03) and readiness probes (E10) against this frozen surface.
package musecode

// PinnedCLIVersion is the only CLI release this contract targets. The owner
// authorized the re-pin from 1.3.0 to the installed Muse Code 1.4.0 on
// their MacBook (E11 proposal decision D1, 25 September 2026); any other
// version fails readiness closed until the pin is deliberately moved.
const PinnedCLIVersion = "1.4.0"

// ObservedCLIFullVersion records the exact local build string seen via
// `muse --version` (metadata only, no model call): 1.4.0-R4161.1.
const ObservedCLIFullVersion = "1.4.0-R4161.1"

// PinnedModelID is the only Meta model id recruitment sessions may run.
// Verified live 26 September 2026: `muse exec --provider meta --model
// muse-spark-1.3` reports provider_id meta / model_id muse-spark-1.3 on
// run.model.configured and completes; the --model flag wins over the
// settings-file model, and the route folder rejects anything else.
const PinnedModelID = "muse-spark-1.3"

// PinnedProviderID is the only model provider recruitment sessions use.
const PinnedProviderID = "meta"

// SessionTransport pins how recruitment sessions reach the CLI. Verified
// from installed `--help` output and live echo runs: `muse exec --json`
// conducts one bounded headless turn with native web tools on by default,
// --output-schema structured answers (meta provider) and --max-model-steps
// as the host-side backstop. No session server or MCP scoping is used.
const SessionTransport = "muse exec direct headless invocation"

// SessionDirectExec names the direct exec protocol the supervisor relies
// on: one `muse exec --json` turn whose JSONL folds to model steps, native
// retrieval-tool usage and one structured final text. Readiness proves the
// pinned CLI release plus owner credential presence; anything else fails
// the turn closed with the host's detail.
const SessionDirectExec = "direct muse exec JSONL + output-schema protocol"

// Tier selects which model route a session may use. Contributor sessions are
// public-only; Standard sessions are private preparation-only.
type Tier string

const (
	// TierContributor handles the public vacancy-discovery loop and
	// selected-role public checks. It must never receive personal,
	// sensitive or confidential owner data (Meta terms §6.2).
	TierContributor Tier = "contributor"
	// TierStandard drafts grounded preparation material and explicit
	// owner-requested rewrites through a separate private session.
	TierStandard Tier = "standard"
)
