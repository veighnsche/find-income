package musecode

import "errors"

// Status codes for Muse readiness. Any code other than CodeReady reports
// the path unavailable without invoking the CLI.
const (
	CodeReady               = "muse_ready"
	CodeNotConfigured       = "muse_not_configured"
	CodeVersionMismatch     = "muse_version_mismatch"
	CodeLaneUnverified      = "muse_lane_unverified"
	CodeProtocolUnverified  = "muse_protocol_unverified"
	CodeWorkspaceUnverified = "muse_workspace_unverified"
)

// Status is the fail-closed readiness verdict for one tier.
type Status struct {
	Tier      Tier
	Available bool
	Code      string
	Detail    string
}

// Facts are observed, no-model-call inputs to Check. The checker performs
// no I/O: E02/E10 supply facts from supported local probes, and tests
// supply them by hand, so readiness can never trigger a model call.
type Facts struct {
	CLIPath                 string
	CLIReportVersion        string
	EffectiveModel          string
	SubscriptionLaneProved  bool
	SessionProtocolProved   bool
	WorkspaceIsolatedProved bool
}

// Check verifies effective model and subscription lane before any session
// input is admitted. Missing or mismatched facts fail closed.
func Check(tier Tier, facts Facts) Status {
	if tier != TierContributor && tier != TierStandard {
		return Status{Tier: tier, Code: CodeNotConfigured, Detail: "unknown muse tier"}
	}
	if facts.CLIPath == "" {
		return Status{Tier: tier, Code: CodeNotConfigured, Detail: "muse CLI path is not configured"}
	}
	if facts.CLIReportVersion == "" || !versionMatchesPin(facts.CLIReportVersion) {
		return Status{Tier: tier, Code: CodeVersionMismatch, Detail: "muse CLI is not the pinned " + PinnedCLIVersion}
	}
	if facts.EffectiveModel == "" || !facts.SubscriptionLaneProved {
		return Status{Tier: tier, Code: CodeLaneUnverified, Detail: "effective model/subscription lane is not verified"}
	}
	if !facts.SessionProtocolProved {
		return Status{Tier: tier, Code: CodeProtocolUnverified, Detail: SessionMCPNegotiation + " is not verified"}
	}
	if !facts.WorkspaceIsolatedProved {
		return Status{Tier: tier, Code: CodeWorkspaceUnverified, Detail: "tier workspace isolation is not verified"}
	}
	return Status{Tier: tier, Available: true, Code: CodeReady, Detail: "muse " + string(tier) + " session may be admitted"}
}

// versionMatchesPin accepts the pinned release with any local build suffix
// (for example 1.3.0-R3401.1) and rejects everything else.
func versionMatchesPin(reported string) bool {
	if reported == PinnedCLIVersion {
		return true
	}
	if len(reported) > len(PinnedCLIVersion)+1 &&
		reported[:len(PinnedCLIVersion)] == PinnedCLIVersion &&
		(reported[len(PinnedCLIVersion)] == '-' || reported[len(PinnedCLIVersion)] == '+') {
		return true
	}
	return false
}

// ErrSessionUnavailable is returned when session input is attempted without
// a ready status.
var ErrSessionUnavailable = errors.New("musecode: session unavailable")
