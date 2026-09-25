// Package musecode freezes the app's contract for driving the locally
// installed Muse Code CLI. E01 pins identifiers, DTO surfaces, bounds and
// terminal semantics; later tasks implement supervision (E02), public tools
// (E03) and readiness probes (E10) against this frozen surface.
package musecode

// PinnedCLIVersion is the only CLI release this contract targets. The owner
// selected the installed Muse Code 1.3.0 on their MacBook; any other version
// fails readiness closed until the pin is deliberately moved.
const PinnedCLIVersion = "1.3.0"

// ObservedCLIFullVersion records the exact local build string seen via
// `muse --version` (metadata only, no model call): 1.3.0-R3401.1.
const ObservedCLIFullVersion = "1.3.0-R3401.1"

// SessionTransport pins how recruitment sessions reach the CLI. Verified from
// installed `--help` output: `muse serve` hosts MSP sessions over stdio with
// sandbox posture fixed at host start, while `muse exec` exposes no
// per-invocation MCP options and is therefore not a tool-capable path.
const SessionTransport = "muse serve stdio MSP session host"

// SessionMCPNegotiation names the per-session MCP scoping the supervisor must
// establish before any input: initialize with sessionMcp and a narrow
// config.mcpServers. Unverified against the installed CLI; E02/E10 prove it.
const SessionMCPNegotiation = "initialize sessionMcp + config.mcpServers"

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
