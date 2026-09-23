package codexservice

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/veighnsche/find-income-dashboard/api/internal/discovery"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var requiredTools = []string{"round_context", "round_mutation", "round_evidence_correction", "source_links", "source_discovery", "discovery_candidate_stage", "discovery_official_links", "discovery_board_register", "application_pack_prepare"}
var errTool = errors.New("Round tool input or authority is invalid; refresh round_context.")

type roundContextArgs struct {
	RoundID    string `json:"roundId"`
	Capability string `json:"capability"`
	RequestKey string `json:"requestKey"`
}
type roundMutationArgs struct {
	RoundID    string `json:"roundId"`
	Capability string `json:"capability"`
	store.RoundMutationInput
}
type roundEvidenceCorrectionArgs struct {
	RoundID    string `json:"roundId"`
	Capability string `json:"capability"`
	store.RoundEvidenceCorrectionInput
}
type sourceDiscoveryArgs struct {
	RoundID     string `json:"roundId"`
	Capability  string `json:"capability"`
	RequestKey  string `json:"requestKey"`
	Method      string `json:"method"`
	Keyword     string `json:"keyword,omitempty"`
	Country     string `json:"country,omitempty"`
	Page        int    `json:"page,omitempty"`
	CompanySlug string `json:"companySlug,omitempty"`
	JobSlug     string `json:"jobSlug,omitempty"`
}
type discoveryCandidateArgs struct {
	RoundID         string `json:"roundId"`
	Capability      string `json:"capability"`
	RequestKey      string `json:"requestKey"`
	SourceAttemptID string `json:"sourceAttemptId"`
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	CompanyURL      string `json:"companyUrl,omitempty"`
	EvidenceQuote   string `json:"evidenceQuote"`
}

func registerTool[I any](server *mcp.Server, name, description string, handler func(context.Context, I) (map[string]any, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, input I) (*mcp.CallToolResult, map[string]any, error) {
		out, err := handler(ctx, input)
		if err != nil {
			return nil, nil, errTool
		}
		return nil, out, nil
	})
}
func (s *Service) newBridge() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "jobseek", Version: "0.1.0"}, nil)
	registerTool(server, "round_context", "Read one delegated active round and its exact scope and remaining allowance.", s.roundContextTool)
	registerTool(server, "round_mutation", "Create a company or opportunity in a delegated running round with an exact resource, revision and idempotency key.", s.roundMutationTool)
	registerTool(server, "round_evidence_correction", "Supersede one owner-selected evidence claim with an exact source quote and round authority.", s.roundEvidenceCorrectionTool)
	registerTool(server, "source_links", "Inspect bounded public career links from a scoped company's saved website.", s.sourceLinksTool)
	registerTool(server, "source_discovery", "Read a bounded public Himalayas search or detail page under the current round and capability; results are unverified candidates.", s.sourceDiscoveryTool)
	registerTool(server, "discovery_candidate_stage", "Stage one exact public search candidate with a quote copied from its saved discovery response.", s.discoveryCandidateTool)
	registerTool(server, "discovery_official_links", "Read the staged candidate's claimed company website from a matching saved company detail, or one exact same-origin careers link from that first read.", s.discoveryOfficialLinksTool)
	registerTool(server, "discovery_board_register", "Register an exact Lever link from the saved official-site read as a verified board in this round's scope.", s.discoveryBoardRegisterTool)
	registerTool(server, "application_pack_prepare", "Prepare a private application pack from a current sourced opportunity after recorded relevance review.", s.applicationPackPrepareTool)
	bridge := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Dedicated bridge authentication: browser cookies and Origin-bearing
		// requests cannot substitute for this credential.
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(s.cfg.BridgeToken) < 32 || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" ||
			!strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.BridgeToken)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		bridge.ServeHTTP(w, r)
	})
}

func (s *Service) sourceDiscoveryTool(ctx context.Context, args sourceDiscoveryArgs) (map[string]any, error) {
	reader := &discovery.Reader{Store: s.db}
	page, err := reader.Read(ctx, discovery.Input{RoundID: args.RoundID, Capability: args.Capability, RequestKey: args.RequestKey, ResourceID: "discovery:himalayas", Method: args.Method, Keyword: args.Keyword, Country: args.Country, Page: args.Page, CompanySlug: args.CompanySlug, JobSlug: args.JobSlug})
	if err != nil {
		return nil, err
	}
	return map[string]any{"page": page}, nil
}

func (s *Service) discoveryCandidateTool(ctx context.Context, args discoveryCandidateArgs) (map[string]any, error) {
	result, err := s.db.StageDiscoveryCandidate(ctx, store.DiscoveryStageInput{RoundID: args.RoundID, Capability: args.Capability, RequestKey: args.RequestKey, Candidate: store.DiscoveryCandidate{AttemptID: args.SourceAttemptID, Kind: args.Kind, Title: args.Title, URL: args.URL, CompanyURL: args.CompanyURL, EvidenceQuote: args.EvidenceQuote}})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": result}, nil
}

func (s *Service) sourceLinksTool(ctx context.Context, args SourceLinksArgs) (map[string]any, error) {
	snapshot, err := s.RoundSourceLinks(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"snapshot": snapshot}, nil
}

func (s *Service) roundContextTool(ctx context.Context, args roundContextArgs) (map[string]any, error) {
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	cost, _ := store.RoundOperationCost(store.RoundContextTool)
	attempt, created, err := s.db.ReserveRoundAttempt(ctx, authority.Actor, args.RoundID,
		store.RoundAttemptInput{RequestKey: args.RequestKey, Operation: store.RoundContextTool,
			ResourceID: "campaign:active", Cost: cost, BoundCapability: args.Capability})
	if err != nil {
		return nil, err
	}
	if !created && attempt.State == store.AttemptSucceeded {
		var saved map[string]any
		if err := json.Unmarshal(attempt.Result, &saved); err != nil {
			return nil, err
		}
		return saved, nil
	}
	if !created {
		return nil, errTool
	}
	if _, err := s.db.MarkRoundDispatched(ctx, args.RoundID, attempt.ID); err != nil {
		return nil, err
	}
	r, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return nil, err
	}
	allInstructions, err := s.db.OwnerInstructions(ctx, "")
	if err != nil {
		return nil, err
	}
	instructions := make([]store.OwnerInstruction, 0)
	for _, instruction := range allInstructions {
		if instruction.ActorID == r.Actor.ID && (instruction.RoundID == r.ID || instruction.RoundID == "" && scopeContains(r.Scope.InputRefs, "instruction:"+instruction.ID)) {
			instructions = append(instructions, instruction)
		}
	}
	result := map[string]any{"roundId": r.ID, "outcome": r.Outcome, "intent": r.Intent,
		"profileVersion": r.ProfileVersion, "scope": r.Scope, "generation": r.Generation,
		"revision": r.Revision, "deadline": r.Deadline, "limits": r.Limits, "used": r.Used,
		"ownerInstructions": instructions}
	encoded, _ := json.Marshal(result)
	if _, err := s.db.FinishRoundAttempt(ctx, authority.Actor, args.RoundID, attempt.ID, true, encoded, ""); err != nil {
		return nil, err
	}
	return result, nil
}

func scopeContains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func (s *Service) roundMutationTool(ctx context.Context, args roundMutationArgs) (map[string]any, error) {
	if args.Operation == store.RoundPrepareApplicationPack || args.ApplicationPack != nil {
		return nil, store.ErrFenced
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	input := args.RoundMutationInput
	input.Capability = args.Capability
	result, created, err := s.db.ApplyRoundMutation(ctx, authority.Actor, args.RoundID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": result, "created": created}, nil
}

func (s *Service) roundEvidenceCorrectionTool(ctx context.Context, args roundEvidenceCorrectionArgs) (map[string]any, error) {
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	input := args.RoundEvidenceCorrectionInput
	input.Capability = args.Capability
	result, created, err := s.db.CorrectRoundEvidence(ctx, authority.Actor, args.RoundID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": result, "created": created}, nil
}
