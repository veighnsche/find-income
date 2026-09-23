package codexservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/interviewprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type InterviewRuntimeConfig struct {
	ProjectRoot string
	LoadSources func(context.Context) ([]applicationpacks.Source, error)
}

func (s *Service) ConfigureInterviews(cfg InterviewRuntimeConfig) error {
	if cfg.ProjectRoot == "" && cfg.LoadSources == nil {
		return store.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrUnavailable
	}
	s.interviewConfig = cfg
	return nil
}

type interviewPrepareArgs struct {
	RoundID     string              `json:"roundId"`
	Capability  string              `json:"capability"`
	RequestKey  string              `json:"requestKey"`
	InterviewID string              `json:"interviewId"`
	Draft       interviewprep.Draft `json:"draft"`
}

type interviewDebriefArgs struct {
	RoundID      string                             `json:"roundId"`
	Capability   string                             `json:"capability"`
	RequestKey   string                             `json:"requestKey"`
	DebriefID    string                             `json:"debriefId"`
	Observations []interviewprep.DebriefObservation `json:"observations"`
	Unknowns     []string                           `json:"unknowns,omitempty"`
}

func interviewSHA(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func (s *Service) interviewPrepareTool(ctx context.Context, args interviewPrepareArgs) (map[string]any, error) {
	if args.RoundID == "" || args.Capability == "" || args.RequestKey == "" || args.InterviewID == "" {
		return nil, store.ErrInvalid
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	r, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return nil, err
	}
	if r.State != store.RoundRunning || r.Outcome != "interview_prepare" || !scopeContains(r.Scope.InputRefs, "interview:"+args.InterviewID) || !scopeContains(r.Scope.Operations, store.RoundInterviewBriefSave) {
		return nil, store.ErrFenced
	}
	interview, err := s.db.Interview(ctx, args.InterviewID)
	if err != nil {
		return nil, err
	}
	if !interview.Current || interview.RoundID != r.ID || !scopeContains(r.Scope.Resources, "opportunity:"+interview.OpportunityID) {
		return nil, store.ErrConflict
	}
	opportunity, err := s.db.Opportunity(ctx, interview.OpportunityID)
	if err != nil {
		return nil, err
	}
	company, err := s.db.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	cfg := s.interviewConfig
	s.mu.Unlock()
	if cfg.ProjectRoot == "" && cfg.LoadSources == nil {
		return nil, ErrUnavailable
	}
	var career []applicationpacks.Source
	if cfg.LoadSources != nil {
		career, err = cfg.LoadSources(ctx)
	} else {
		career, _, err = applicationpacks.LoadApprovedCareerSources(cfg.ProjectRoot, []string{"cv-vince-liem.typ", "cv-vince-liem.md", "github-evidence-review.md"})
		if err == nil {
			career = career[1:]
		}
	}
	if err != nil || len(career) == 0 {
		return nil, store.ErrInvalid
	}
	input := interviewprep.Input{InterviewID: interview.ID, OpportunityID: interview.OpportunityID, RoleTitle: opportunity.Title, EmployerName: company.Name, CareerSources: career, Draft: args.Draft,
		Context: []interviewprep.ContextSource{{ID: "owner-input", Kind: "owner_input", Revision: interview.ContextSHA256, SHA256: interview.ContextSHA256, Body: interview.Context}, {ID: "role", Kind: "role", Revision: fmt.Sprint(opportunity.Revision), SHA256: interviewSHA(opportunity.OriginalText), Body: opportunity.OriginalText}}}
	brief, err := interviewprep.Prepare(input)
	if err != nil {
		return nil, err
	}
	briefJSON, _ := json.Marshal(brief)
	_, created, err := s.db.ApplyRoundMutation(ctx, authority.Actor, r.ID, store.RoundMutationInput{RequestKey: args.RequestKey + "/brief", Operation: store.RoundInterviewBriefSave, ResourceID: "opportunity:" + interview.OpportunityID, ExpectedRevision: interview.OpportunityRevision, InterviewBrief: &store.InterviewBriefMutation{InterviewID: interview.ID, OpportunityID: interview.OpportunityID, InputSHA256: brief.InputSHA256, BriefJSON: briefJSON}, Capability: args.Capability})
	if err != nil {
		return nil, err
	}
	return map[string]any{"interviewId": interview.ID, "brief": briefJSON, "created": created}, nil
}

func (s *Service) interviewDebriefTool(ctx context.Context, args interviewDebriefArgs) (map[string]any, error) {
	if args.RoundID == "" || args.Capability == "" || args.RequestKey == "" || args.DebriefID == "" {
		return nil, store.ErrInvalid
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	r, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return nil, err
	}
	if r.State != store.RoundRunning || r.Outcome != "interview_debrief" || !scopeContains(r.Scope.InputRefs, "debrief:"+args.DebriefID) || !scopeContains(r.Scope.Operations, store.RoundInterviewDebriefSave) {
		return nil, store.ErrFenced
	}
	d, err := s.db.InterviewDebrief(ctx, args.DebriefID)
	if err != nil {
		return nil, err
	}
	if d.RoundID != r.ID || !scopeContains(r.Scope.Resources, "interview:"+d.InterviewID) {
		return nil, store.ErrFenced
	}
	validated, err := interviewprep.ValidateDebrief(interviewprep.DebriefInput{InterviewID: d.InterviewID, OwnerNotes: d.Notes, Observations: args.Observations, Unknowns: args.Unknowns})
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(validated)
	_, created, err := s.db.ApplyRoundMutation(ctx, authority.Actor, r.ID, store.RoundMutationInput{RequestKey: args.RequestKey, Operation: store.RoundInterviewDebriefSave, ResourceID: "interview:" + d.InterviewID, ExpectedRevision: 1, InterviewDebrief: &store.InterviewDebriefMutation{DebriefID: d.ID, InterviewID: d.InterviewID, DebriefJSON: encoded}, Capability: args.Capability})
	if err != nil {
		return nil, err
	}
	return map[string]any{"debriefId": d.ID, "created": created}, nil
}

// recoverInterviewDispatch settles only a complete captured Jev focus request.
// Unknown Codex turns continue through normal terminal observation.
func (s *Service) recoverInterviewDispatch(ctx context.Context, roundID, attemptID string, generation int64) (bool, bool, error) {
	if s == nil || s.db == nil {
		return false, false, ErrUnavailable
	}
	round, err := s.db.Round(ctx, roundID)
	if err != nil {
		return false, false, err
	}
	if round.Outcome != "interview_prepare" {
		return false, false, nil
	}
	attempt, err := s.db.RoundAttempt(ctx, attemptID)
	if err != nil {
		return false, false, err
	}
	if attempt.Operation != store.RoundJevRequest {
		return false, false, nil
	}
	if attempt.RoundID != roundID || attempt.State != store.AttemptUncertain || round.State != store.RoundPaused || round.Generation != generation {
		return true, false, store.ErrFenced
	}
	items, err := s.db.JevAttemptsForRound(ctx, roundID)
	if err != nil {
		return true, false, err
	}
	var captured *store.JevAttempt
	for i := range items {
		if items[i].RoundAttemptID == attemptID {
			captured = &items[i]
			break
		}
	}
	if captured == nil || captured.Purpose != "interview_focus" || captured.Status != "succeeded" || captured.ResponseTruncated || captured.ResponseReadError || captured.InputTokens == nil || captured.OutputTokens == nil {
		return true, false, nil
	}
	var recorded struct {
		State jev.InterviewFocusInput `json:"state"`
	}
	if json.Unmarshal(captured.LogicalRequestJSON, &recorded) != nil {
		return true, false, nil
	}
	interview, err := s.db.Interview(ctx, recorded.State.InterviewID)
	if err != nil || interview.RoundID != roundID || !interview.Current || len(interview.Brief) == 0 {
		return true, false, nil
	}
	var brief interviewprep.Brief
	if json.Unmarshal(interview.Brief, &brief) != nil {
		return true, false, nil
	}
	input, err := brief.FocusInput(2500)
	if err != nil {
		return true, false, nil
	}
	result, err := jev.RecoverCapturedInterviewFocus(input, captured.LogicalRequestJSON, captured.RawResponseBytes, captured.RequestedModel)
	if err != nil || result.ProviderResult.ReturnedModel != captured.ReturnedModel || result.ProviderResult.Usage.InputTokens != *captured.InputTokens || result.ProviderResult.Usage.OutputTokens != *captured.OutputTokens {
		return true, false, nil
	}
	if _, err := brief.BindFocusSelection(result, 2500); err != nil {
		return true, false, nil
	}
	digest := sha256.Sum256(interview.Brief)
	resolved, err := s.db.RecoverCapturedInterviewFocusAttempt(ctx, roundID, attemptID, captured.ID, generation, hex.EncodeToString(digest[:]))
	return true, resolved, err
}
