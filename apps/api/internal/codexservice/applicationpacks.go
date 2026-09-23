package codexservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ApplicationPackRuntimeConfig identifies read-only approved career inputs and
// private rendering space. Configure it before exposing the MCP bridge.
type ApplicationPackRuntimeConfig struct {
	ProjectRoot    string
	TypstPath      string
	PrivateTempDir string
	RenderTimeout  time.Duration
	Relevance      jevservice.Service
}

type applicationPackPrepareArgs struct {
	RoundID          string                    `json:"roundId"`
	Capability       string                    `json:"capability"`
	RequestKey       string                    `json:"requestKey"`
	OpportunityID    string                    `json:"opportunityId"`
	Destination      string                    `json:"destination,omitempty"`
	RequirementQuote string                    `json:"requirementQuote"`
	SourceNames      []string                  `json:"sourceNames,omitempty"`
	Focus            applicationpacks.Line     `json:"focus"`
	Cover            []applicationpacks.Line   `json:"cover"`
	Answers          []applicationpacks.Answer `json:"answers,omitempty"`
	MaterialUnknowns []string                  `json:"materialUnknowns,omitempty"`
}

func (s *Service) applicationPackPrepareTool(ctx context.Context, args applicationPackPrepareArgs) (map[string]any, error) {
	if args.RoundID == "" || args.Capability == "" || len(args.RequestKey) < 1 || len(args.RequestKey) > 100 ||
		args.OpportunityID == "" || len(args.RequirementQuote) < 8 || len(args.RequirementQuote) > 1000 {
		return nil, store.ErrInvalid
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	round, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return nil, err
	}
	if round.State != store.RoundRunning || round.Generation != authority.Generation ||
		!scopeContains(round.Scope.Resources, "opportunity:"+args.OpportunityID) ||
		!scopeContains(round.Scope.Operations, store.RoundPrepareApplicationPack) ||
		!scopeContains(round.Scope.Operations, store.RoundJevRequest) {
		return nil, store.ErrFenced
	}
	if !time.Now().Before(round.Deadline) {
		_, _ = s.db.ExpireRound(ctx, round.ID)
		return nil, store.ErrExpired
	}
	ctx, cancel := context.WithDeadline(ctx, round.Deadline)
	defer cancel()
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		return nil, store.ErrInvalid
	}
	requestSHA := fmt.Sprintf("%x", sha256.Sum256(encodedArgs))
	previous, previousSHA, err := s.db.CompletedApplicationPackMutation(ctx, args.RoundID, args.RequestKey+"/pack")
	if err == nil {
		if previousSHA != requestSHA {
			return nil, store.ErrRoundIdempotencyConflict
		}
		pack, readErr := s.db.ApplicationPack(ctx, previous.EntityID)
		if readErr != nil {
			return nil, readErr
		}
		var snapshot struct {
			Draft struct {
				MaterialUnknowns []string                     `json:"materialUnknowns"`
				Relevance        []applicationpacks.Relevance `json:"relevance"`
			} `json:"draft"`
		}
		if readErr := json.Unmarshal(pack.ManifestJSON, &snapshot); readErr != nil {
			return nil, readErr
		}
		return map[string]any{"packId": previous.EntityID, "version": previous.Revision, "created": false,
			"contentSha256": pack.ContentSHA256, "materialUnknowns": snapshot.Draft.MaterialUnknowns,
			"relevanceCount": len(snapshot.Draft.Relevance)}, nil
	}
	if err != nil && err != store.ErrNotFound {
		return nil, err
	}
	opportunity, err := s.db.Opportunity(ctx, args.OpportunityID)
	if err != nil {
		return nil, err
	}
	if opportunity.ArchivedAt != "" || !strings.Contains(opportunity.OriginalText, args.RequirementQuote) {
		return nil, store.ErrInvalid
	}
	decision, err := s.db.OwnerOpportunityDecision(ctx, opportunity.ID)
	if err != nil {
		return nil, err
	}
	if decision.Decision != "selected" || decision.OpportunityRevision != opportunity.Revision {
		return nil, store.ErrFenced
	}
	if args.Destination != "" && !strings.Contains(opportunity.OriginalText, args.Destination) {
		return nil, store.ErrInvalid
	}
	profile, err := s.db.CurrentPreferences(ctx)
	if err != nil {
		return nil, err
	}
	if profile.Version != round.ProfileVersion {
		return nil, store.ErrConflict
	}
	s.mu.Lock()
	cfg := s.packConfig
	s.mu.Unlock()
	if cfg.ProjectRoot == "" || cfg.Relevance.Store == nil || cfg.Relevance.Client == nil {
		return nil, ErrUnavailable
	}
	names := args.SourceNames
	if len(names) == 0 {
		names = []string{"cv-vince-liem.typ", "cv-vince-liem.md", "github-evidence-review.md"}
	}
	sources, template, err := applicationpacks.LoadApprovedCareerSources(cfg.ProjectRoot, names)
	if err != nil {
		return nil, err
	}
	company, err := s.db.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]applicationpacks.Source, len(sources))
	for _, source := range sources {
		byID[source.ID] = source
	}
	draft := applicationpacks.Draft{Focus: args.Focus, Cover: args.Cover, Answers: args.Answers, MaterialUnknowns: append([]string(nil), args.MaterialUnknowns...)}
	if strings.TrimSpace(args.Destination) == "" {
		draft.MaterialUnknowns = append(draft.MaterialUnknowns, "The application destination is not confirmed; inspect the employer route before any delivery.")
	}
	if len(draft.Answers) == 0 {
		draft.MaterialUnknowns = append(draft.MaterialUnknowns, "Required employer questions have not been verified from the saved opening.")
	}
	input := applicationpacks.Input{Role: applicationpacks.Role{OpportunityID: opportunity.ID,
		OpportunityRevision: opportunity.Revision, ProfileRevision: profile.Version, Title: opportunity.Title,
		Company: company.Name, SourceURL: opportunity.SourceURL, Description: opportunity.OriginalText,
		Destination: args.Destination}, Sources: sources, Draft: draft, CVTemplate: template, TemplateSHA256: fmt.Sprintf("%x", sha256.Sum256(template)), PreparationRequestSHA256: requestSHA}
	if err := applicationpacks.ValidateInput(input); err != nil {
		return nil, err
	}
	citations := make([]applicationpacks.Citation, 0)
	citations = append(citations, draft.Focus.Citations...)
	for _, line := range draft.Cover {
		citations = append(citations, line.Citations...)
	}
	for _, answer := range draft.Answers {
		for _, line := range answer.Lines {
			citations = append(citations, line.Citations...)
		}
	}
	unique := make(map[string]bool)
	for _, citation := range citations {
		key := citation.SourceID + "\x00" + citation.Excerpt
		if unique[key] {
			continue
		}
		if len(unique) >= 6 {
			return nil, store.ErrInvalid
		}
		unique[key] = true
		source, ok := byID[citation.SourceID]
		if !ok || !strings.Contains(source.Body, citation.Excerpt) {
			return nil, store.ErrInvalid
		}
		sum := sha256.Sum256([]byte(key))
		keyDigest := hex.EncodeToString(sum[:8])
		binding := jevservice.Binding{Actor: authority.Actor, RoundID: args.RoundID, ResourceID: "opportunity:" + args.OpportunityID,
			RequestKeyPrefix: args.RequestKey + "/jev/" + keyDigest, ProfileVersion: profile.Version,
			BoundCapability: args.Capability, MaxReportedTokens: 2500}
		relevance, err := cfg.Relevance.AssessPackRelevance(ctx, binding, args.RequirementQuote, source, citation.Excerpt)
		if err != nil {
			return nil, err
		}
		draft.Relevance = append(draft.Relevance, relevance)
	}
	if err := s.requireCurrentPackRound(ctx, round, args.OpportunityID, opportunity.Revision); err != nil {
		return nil, err
	}
	input.Draft = draft
	renderer := applicationpacks.Renderer{TypstPath: cfg.TypstPath, PrivateTempDir: cfg.PrivateTempDir, Timeout: cfg.RenderTimeout}
	prepared, err := renderer.Prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	var snapshot struct {
		Role applicationpacks.Role `json:"role"`
	}
	if err := json.Unmarshal(prepared.ManifestJSON, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Role.OpportunityRevision != opportunity.Revision || snapshot.Role.ProfileRevision != profile.Version ||
		snapshot.Role.Description != opportunity.OriginalText {
		return nil, store.ErrConflict
	}
	if err := s.requireCurrentPackRound(ctx, round, args.OpportunityID, opportunity.Revision); err != nil {
		return nil, err
	}
	mutation := store.RoundMutationInput{RequestKey: args.RequestKey + "/pack", Operation: store.RoundPrepareApplicationPack,
		ResourceID: "opportunity:" + args.OpportunityID, ExpectedRevision: opportunity.Revision, Capability: args.Capability,
		ApplicationPack: &store.ApplicationPackMutationInput{OpportunityID: args.OpportunityID,
			ExpectedOpportunityRevision: opportunity.Revision, ExpectedProfileRevision: profile.Version,
			ContentSHA256: prepared.SHA256, ManifestJSON: prepared.ManifestJSON, TypstSource: prepared.TypstSource, PDF: prepared.PDF}}
	result, created, err := s.db.ApplyRoundMutation(ctx, authority.Actor, args.RoundID, mutation)
	if err != nil {
		return nil, err
	}
	return map[string]any{"packId": result.EntityID, "version": result.Revision, "created": created,
		"contentSha256": prepared.SHA256, "materialUnknowns": draft.MaterialUnknowns,
		"relevanceCount": len(draft.Relevance)}, nil
}

func (s *Service) requireCurrentPackRound(ctx context.Context, initial store.Round, opportunityID string, revision int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.db.Round(ctx, initial.ID)
	if err != nil {
		return err
	}
	if current.State != store.RoundRunning || current.Generation != initial.Generation {
		return store.ErrFenced
	}
	if !time.Now().Before(current.Deadline) {
		_, _ = s.db.ExpireRound(ctx, current.ID)
		return store.ErrExpired
	}
	profile, err := s.db.CurrentPreferences(ctx)
	if err != nil {
		return err
	}
	if profile.Version != initial.ProfileVersion {
		return store.ErrConflict
	}
	opportunity, err := s.db.Opportunity(ctx, opportunityID)
	if err != nil {
		return err
	}
	if opportunity.ArchivedAt != "" || opportunity.Revision != revision {
		return store.ErrConflict
	}
	decision, err := s.db.OwnerOpportunityDecision(ctx, opportunityID)
	if err != nil {
		return err
	}
	if decision.Decision != "selected" || decision.OpportunityRevision != revision {
		return store.ErrFenced
	}
	return nil
}

func (s *Service) ConfigureApplicationPacks(cfg ApplicationPackRuntimeConfig) error {
	if !filepath.IsAbs(cfg.ProjectRoot) || !filepath.IsAbs(cfg.TypstPath) ||
		!filepath.IsAbs(cfg.PrivateTempDir) || cfg.RenderTimeout <= 0 || cfg.RenderTimeout > 30*time.Second ||
		cfg.Relevance.Store == nil || cfg.Relevance.Client == nil {
		return fmt.Errorf("invalid application pack runtime config")
	}
	if info, err := os.Stat(cfg.ProjectRoot); err != nil || !info.IsDir() {
		return fmt.Errorf("approved career root unavailable")
	}
	if info, err := os.Stat(cfg.TypstPath); err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return fmt.Errorf("Typst unavailable")
	}
	if err := os.MkdirAll(cfg.PrivateTempDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.PrivateTempDir, 0700); err != nil {
		return err
	}
	s.mu.Lock()
	s.packConfig = cfg
	s.mu.Unlock()
	return nil
}
