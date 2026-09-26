package musewire

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// LiveFacts establishes admission facts from no-spend local evidence: the
// pinned CLI release plus owner credential presence. It never makes a
// model call, so token validity is NOT proved here: the first live run
// confirms the lane, and an expired or revoked credential fails that run
// closed with the host's detail instead of admitting blindly.
//
// The protocol and workspace flags rest on verified construction: direct
// exec JSONL routing and terminal semantics observed live against CLI
// 1.4.0 on 26 September 2026 (--json events, --output-schema structured
// answers, --model flag precedence, model.* task kinds, native web_search
// and web_fetch tool identifiers), with every failure failing closed;
// per-run 0700 workspaces hold the prompt, schema and trace. Anything
// unverifiable here (missing CLI, version mismatch, absent credentials)
// stays unset so Check fails closed with the exact missing piece.
func LiveFacts(cliPath, modelID string) musecode.Facts {
	facts := musecode.ProbeLocalFacts(cliPath)
	if facts.CLIPath == "" || facts.CLIReportVersion == "" || modelID == "" {
		return facts
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return facts
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "muse", "auth.json"))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return facts
	}
	facts.EffectiveModel = modelID
	facts.SubscriptionLaneProved = true
	facts.SessionProtocolProved = true
	facts.WorkspaceIsolatedProved = true
	return facts
}
