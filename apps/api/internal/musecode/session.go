package musecode

import (
	"errors"
	"path/filepath"
	"strings"
)

// SessionSpec is the frozen admission contract for one local CLI session.
// The supervisor (E02) may only construct it from a ready Status; callers
// pass specs, never raw prompts, so check-before-input holds by
// construction.
type SessionSpec struct {
	Tier      Tier
	Workspace string
	Public    bool
	Bounds    Bounds
}

// NewSession admits one session spec. Contributor sessions are public-only;
// Standard sessions are private-only. Shared or nested tier workspaces are
// rejected so a private Standard transcript, checkpoint or workspace can
// never be reused or downgraded into Contributor.
func NewSession(status Status, workspace string, bounds Bounds, peerWorkspaces []string) (SessionSpec, error) {
	if !status.Available || status.Code != CodeReady {
		return SessionSpec{}, ErrSessionUnavailable
	}
	if err := bounds.Validate(); err != nil {
		return SessionSpec{}, err
	}
	clean := filepath.Clean(workspace)
	if !filepath.IsAbs(clean) {
		return SessionSpec{}, errors.New("musecode: session workspace must be absolute")
	}
	for _, peer := range peerWorkspaces {
		other := filepath.Clean(peer)
		if other == clean || strings.HasPrefix(clean, other+string(filepath.Separator)) ||
			strings.HasPrefix(other, clean+string(filepath.Separator)) {
			return SessionSpec{}, errors.New("musecode: tier workspaces must be disjoint")
		}
	}
	return SessionSpec{Tier: status.Tier, Workspace: clean, Public: status.Tier == TierContributor, Bounds: bounds}, nil
}
