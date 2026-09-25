package musecode

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProbeLocalFacts observes no-model-call facts about the installed CLI:
// path presence and the `muse --version` release string. An unresolvable
// bare name reports no path (not configured); a configured path with a bad
// or missing version fails closed on mismatch. Effective model,
// subscription lane, session protocol and workspace isolation stay
// unverified here; E10 establishes them through supported probes.
func ProbeLocalFacts(cliPath string) Facts {
	facts := Facts{}
	bin := strings.TrimSpace(cliPath)
	if bin == "" {
		return facts
	}
	if !filepath.IsAbs(bin) {
		resolved, err := exec.LookPath(bin)
		if err != nil {
			return facts
		}
		bin = resolved
	}
	facts.CLIPath = bin
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return facts
	}
	facts.CLIReportVersion = parseVersionLine(string(out))
	return facts
}

// parseVersionLine reads "Muse Code <release> ..." and returns the release
// token. Anything else yields "" so readiness fails closed on mismatch.
func parseVersionLine(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != "Muse" || fields[1] != "Code" {
		return ""
	}
	return fields[2]
}

// UnavailableTransport fails every session run closed. Production uses it
// until the E11-authorized live transport exists; readiness already
// reports the path unavailable, and this is the defense in depth.
type UnavailableTransport struct {
	Detail string
}

// Run implements Transport without starting any session.
func (UnavailableTransport) Run(ctx context.Context, _ SessionSpec, _ SessionInput, _ Cursor, _ EventSink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("musecode: session transport unavailable")
}
