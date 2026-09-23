package codexservice

import "context"

// BindRoundToolSession returns a one-turn capability to pass into round MCP
// tool arguments. It is valid only for the named dispatched attempt and its
// current control generation. The transport BridgeToken alone grants no tool
// authority.
func (s *Service) BindRoundToolSession(ctx context.Context, roundID, attemptID, agentID string) (string, error) {
	return s.db.IssueRoundToolCapability(ctx, roundID, attemptID, agentID)
}

func (s *Service) RevokeRoundToolSession(ctx context.Context, capability string) error {
	return s.db.RevokeRoundToolCapability(ctx, capability)
}
