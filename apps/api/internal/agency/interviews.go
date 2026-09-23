package agency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/interviewprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (e *Engine) checkInterview(ctx context.Context, outcome string) error {
	if e == nil || e.Store == nil || e.Runtime == nil {
		return errors.New("interview preparation unavailable")
	}
	if err := e.Runtime.CheckRound(ctx, outcome); err != nil {
		return err
	}
	if outcome == "interview_prepare" {
		if e.InterviewFocus == nil {
			return errors.New("interview focus evaluator unavailable")
		}
		if e.InterviewSources == nil {
			return errors.New("approved career sources unavailable")
		}
		sources, err := e.InterviewSources.LoadPackSources(ctx)
		if err != nil || len(sources) < 3 {
			return errors.New("approved career sources unavailable")
		}
	}
	return nil
}

func (e *Engine) launchInterview(r store.Round) error {
	if r.State != store.RoundRunning || r.Outcome != "interview_prepare" && r.Outcome != "interview_debrief" ||
		(len(r.Scope.Resources) != 1 && len(r.Scope.Resources) != 2 || len(r.Scope.Resources) == 2 && r.Scope.Resources[1] != "campaign:active") {
		return store.ErrFenced
	}
	base := e.Context
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithDeadline(base, r.Deadline)
	if err := e.checkInterview(ctx, r.Outcome); err != nil {
		cancel()
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = map[string]*activeWorker{}
	}
	if _, exists := e.active[r.ID]; exists {
		cancel()
		return store.ErrConflict
	}
	worker := &activeWorker{cancel: cancel, done: make(chan struct{})}
	e.active[r.ID] = worker
	go func() { defer cancel(); defer e.workerDone(r.ID, worker); e.runInterview(ctx, r) }()
	return nil
}

type interviewOutcome struct {
	Code           string              `json:"code"`
	InterviewID    string              `json:"interviewId,omitempty"`
	DebriefID      string              `json:"debriefId,omitempty"`
	BriefSaved     bool                `json:"briefSaved"`
	FocusSaved     bool                `json:"focusSaved"`
	Unknowns       []string            `json:"unknowns"`
	Recommendation *homeRecommendation `json:"recommendation,omitempty"`
}

func (e *Engine) finishInterview(ctx context.Context, initial store.Round, result interviewOutcome) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	r, err := e.Store.Round(cleanup, initial.ID)
	if err != nil || r.State != store.RoundRunning {
		return
	}
	if !time.Now().Before(r.Deadline) {
		_, _ = e.Store.ExpireRound(cleanup, r.ID)
		return
	}
	if result.Unknowns == nil {
		result.Unknowns = []string{}
	}
	facts := outcomeRecommendationFacts{Outcome: r.Outcome, Code: result.Code, UnresolvedCount: len(result.Unknowns), FocusSaved: result.FocusSaved}
	if result.BriefSaved && r.Outcome == "interview_prepare" && result.InterviewID != "" {
		if saved, readErr := e.Store.Interview(cleanup, result.InterviewID); readErr == nil && saved.Current && saved.RoundID == r.ID && len(saved.Brief) > 0 {
			facts.ResultID, facts.ResultUpdatedAt = saved.ID, saved.UpdatedAt
		}
	}
	if result.BriefSaved && r.Outcome == "interview_debrief" && result.DebriefID != "" {
		if saved, readErr := e.Store.InterviewDebrief(cleanup, result.DebriefID); readErr == nil && saved.RoundID == r.ID && len(saved.Debrief) > 0 {
			facts.ResultID, facts.ResultUpdatedAt = saved.ID, saved.UpdatedAt
		}
	}
	result.Recommendation = e.computeOutcomeRecommendation(ctx, r, facts)
	encoded, _ := json.Marshal(result)
	status := "complete"
	if len(result.Unknowns) > 0 || !result.BriefSaved && result.DebriefID == "" || result.DebriefID != "" && !result.BriefSaved {
		status = "partial"
	}
	_, _ = e.Store.FinishRound(cleanup, initial.Actor, r.ID, store.RoundCompleted, result.Code, status, encoded)
}

func roundInterviewRef(r store.Round, prefix string) (string, error) {
	var id string
	for _, ref := range r.Scope.InputRefs {
		if strings.HasPrefix(ref, prefix) {
			if id != "" {
				return "", store.ErrInvalid
			}
			id = strings.TrimPrefix(ref, prefix)
		}
	}
	if id == "" {
		return "", store.ErrInvalid
	}
	return id, nil
}

func (e *Engine) runInterview(ctx context.Context, initial store.Round) {
	result := interviewOutcome{Code: "interview_unresolved"}
	defer func() {
		if !errors.Is(ctx.Err(), context.Canceled) {
			e.finishInterview(ctx, initial, result)
		}
	}()
	r, err := e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		result.Code = terminalCode(err)
		return
	}
	if r.Outcome == "interview_debrief" {
		e.runInterviewDebrief(ctx, r, &result)
		return
	}
	id, err := roundInterviewRef(r, "interview:")
	if err != nil {
		result.Code = "interview_scope_invalid"
		return
	}
	result.InterviewID = id
	interview, err := e.Store.Interview(ctx, id)
	if err != nil || interview.RoundID != r.ID || !interview.Current || r.Scope.Resources[0] != "opportunity:"+interview.OpportunityID {
		result.Code = "interview_context_stale"
		return
	}
	if len(interview.Brief) == 0 {
		opportunity, err := e.Store.Opportunity(ctx, interview.OpportunityID)
		if err != nil {
			result.Code = "interview_role_unavailable"
			return
		}
		company, err := e.Store.Company(ctx, opportunity.CompanyID)
		if err != nil {
			result.Code = "interview_employer_unavailable"
			return
		}
		sources, err := e.InterviewSources.LoadPackSources(ctx)
		if err != nil {
			result.Code = "career_sources_unavailable"
			return
		}
		evidence, err := interviewTurnEvidence(interview, opportunity, company, sources)
		if err != nil {
			result.Code = "interview_context_too_large"
			result.Unknowns = []string{"The complete owner and role context exceeds this bounded turn."}
			return
		}
		key := "interview:" + id
		if prior, err := e.Store.RoundAttemptForRequest(ctx, r.ID, key); err == nil {
			result.Code = "interview_turn_already_dispatched"
			result.Unknowns = []string{"A prior interview turn exists; its outcome must be reviewed before another commission."}
			if prior.State == store.AttemptSucceeded {
				result.Code = "interview_brief_missing"
			}
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			result.Code = "interview_attempt_unavailable"
			return
		}
		_, err = e.Runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID,
			codexservice.RoundTurnInput{RequestKey: key, ResourceID: "opportunity:" + interview.OpportunityID,
				Brief: "Prepare one private interview brief from the complete owner invitation/context and saved role. Call interview_prepare once with exact citations, concise likely questions, relevant approved career examples, and explicit unknowns or attributed conflicts. Distinguish personal projects from employment. Cite any schedule claim to exact owner context. Do not invent a time, interview format, result, or experience; do not book, message, or send anything.", Evidence: evidence})
		if err != nil {
			if saved, readErr := e.Store.Interview(ctx, id); readErr == nil && len(saved.Brief) != 0 {
				result.BriefSaved = true
			}
			result.Code = terminalCode(err)
			result.Unknowns = []string{"The Codex turn did not settle; the saved brief remains readable while its status is reviewed."}
			return
		}
		if _, err = e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion); err != nil {
			result.Code = terminalCode(err)
			return
		}
		interview, err = e.Store.Interview(ctx, id)
		if err != nil || !interview.Current {
			result.Code = "interview_context_stale"
			return
		}
	}
	if len(interview.Brief) == 0 {
		result.Code = "interview_brief_missing"
		result.Unknowns = []string{"No sourced brief was saved."}
		return
	}
	result.BriefSaved = true
	if _, err = e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion); err != nil {
		result.Code = terminalCode(err)
		return
	}
	focusStatus := e.assessInterviewFocus(ctx, r, interview)
	refreshed, readErr := e.Store.Interview(ctx, id)
	if readErr == nil {
		result.FocusSaved = len(refreshed.Focus) != 0
	}
	switch focusStatus {
	case "selected":
		result.Code = "interview_prepared"
	case "unresolved":
		result.Code = "interview_focus_unresolved"
		result.Unknowns = []string{"Jev could not choose a supported focus; the sourced brief remains available."}
	default:
		result.Code = "interview_focus_" + focusStatus
		result.Unknowns = []string{"The sourced brief remains available; its focus decision requires review."}
	}
}

func (e *Engine) assessInterviewFocus(ctx context.Context, r store.Round, interview store.Interview) string {
	if len(interview.Focus) != 0 {
		if interviewFocusUnresolved(interview.Focus) {
			return "unresolved"
		}
		return "selected"
	}
	if e.InterviewFocus == nil {
		return "unavailable"
	}
	var brief interviewprep.Brief
	if json.Unmarshal(interview.Brief, &brief) != nil {
		return "invalid"
	}
	input, err := brief.FocusInput(2500)
	if err != nil {
		return "invalid"
	}
	prefix := "interview-focus:" + interview.ID
	attempt, err := e.Store.RoundAttemptForRequest(ctx, r.ID, prefix+"/0")
	var selected jev.InterviewFocusResult
	var jevID string
	if err == nil {
		if attempt.Operation != store.RoundJevRequest || attempt.ResourceID != "opportunity:"+interview.OpportunityID {
			return "invalid"
		}
		if attempt.State != store.AttemptSucceeded && attempt.State != store.AttemptObservedSuccess {
			return "uncertain"
		}
		saved, err := e.Store.JevAttemptsForRound(ctx, r.ID)
		if err != nil {
			return "unavailable"
		}
		var capture *store.JevAttempt
		for i := range saved {
			if saved[i].RoundAttemptID == attempt.ID {
				capture = &saved[i]
				break
			}
		}
		if capture == nil || capture.Status != "succeeded" || capture.ResponseTruncated || capture.ResponseReadError || capture.InputTokens == nil || capture.OutputTokens == nil {
			return "uncertain"
		}
		selected, err = jev.RecoverCapturedInterviewFocus(input, capture.LogicalRequestJSON, capture.RawResponseBytes, capture.RequestedModel)
		if err != nil || selected.ProviderResult.ReturnedModel != capture.ReturnedModel || selected.ProviderResult.Usage.InputTokens != *capture.InputTokens || selected.ProviderResult.Usage.OutputTokens != *capture.OutputTokens {
			return "invalid"
		}
		jevID = capture.ID
	} else if errors.Is(err, store.ErrNotFound) {
		if _, err := e.live(ctx, r.ID, r.Generation, r.ProfileVersion); err != nil {
			return "stale"
		}
		selected, err = e.InterviewFocus.RunInterviewFocus(ctx, jevservice.Binding{Actor: store.Actor{Kind: "agent", ID: "codex-runner"}, RoundID: r.ID,
			ResourceID: "opportunity:" + interview.OpportunityID, RequestKeyPrefix: prefix, ProfileVersion: r.ProfileVersion, MaxReportedTokens: 2500}, input)
		if err != nil {
			return "unavailable"
		}
		ids, err := e.Store.JevAttemptIDsForRequestPrefix(ctx, r.ID, prefix)
		if err != nil || len(ids) != 1 {
			return "uncertain"
		}
		jevID = ids[0]
	} else {
		return "unavailable"
	}
	if _, err = e.live(ctx, r.ID, r.Generation, r.ProfileVersion); err != nil {
		return "stale"
	}
	prepared, err := brief.BindFocusSelection(selected, 2500)
	if err != nil {
		return "invalid"
	}
	encoded, _ := json.Marshal(prepared)
	if _, err := e.Store.ApplyInterviewFocus(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, r.Generation, interview.ID, jevID, encoded); err != nil {
		return "unavailable"
	}
	return string(selected.Disposition)
}

func interviewFocusUnresolved(raw json.RawMessage) bool {
	var v struct {
		Selection struct {
			Disposition string `json:"disposition"`
		} `json:"selection"`
	}
	return json.Unmarshal(raw, &v) != nil || v.Selection.Disposition != "selected"
}

func (e *Engine) runInterviewDebrief(ctx context.Context, r store.Round, result *interviewOutcome) {
	id, err := roundInterviewRef(r, "debrief:")
	if err != nil {
		result.Code = "debrief_scope_invalid"
		return
	}
	result.DebriefID = id
	d, err := e.Store.InterviewDebrief(ctx, id)
	if err != nil || d.RoundID != r.ID || r.Scope.Resources[0] != "interview:"+d.InterviewID {
		result.Code = "debrief_context_stale"
		return
	}
	result.InterviewID = d.InterviewID
	if len(d.Debrief) != 0 {
		result.BriefSaved = true
		result.Code = "debrief_saved"
		return
	}
	key := "debrief:" + id
	if _, err := e.Store.RoundAttemptForRequest(ctx, r.ID, key); err == nil {
		result.Code = "debrief_turn_already_dispatched"
		result.Unknowns = []string{"The existing debrief turn requires review; it was not repeated."}
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		result.Code = "debrief_attempt_unavailable"
		return
	}
	evidence, _ := json.Marshal(struct {
		InterviewID string `json:"interviewId"`
		DebriefID   string `json:"debriefId"`
		OwnerNotes  string `json:"ownerNotes"`
	}{d.InterviewID, d.ID, d.Notes})
	if len(evidence) > 32000 {
		result.Code = "debrief_context_too_large"
		return
	}
	_, err = e.Runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, codexservice.RoundTurnInput{RequestKey: key, ResourceID: "interview:" + d.InterviewID, Brief: "Extract a concise owner-reported debrief from the complete notes. Call interview_debrief once with exact excerpts from owner notes for every observation. Record unclear points and follow-up ideas as drafts only. Do not infer an employer decision, send, book, or schedule anything.", Evidence: string(evidence)})
	if err != nil {
		result.Code = terminalCode(err)
		return
	}
	d, err = e.Store.InterviewDebrief(ctx, id)
	if err != nil {
		result.Code = "debrief_result_unavailable"
		return
	}
	if len(d.Debrief) != 0 {
		result.BriefSaved = true
		result.Code = "debrief_saved"
	} else {
		result.Code = "debrief_missing"
		result.Unknowns = []string{"No sourced debrief was saved."}
	}
}

func interviewTurnEvidence(interview store.Interview, opportunity store.Opportunity, company store.Company, sources []applicationpacks.Source) (string, error) {
	type excerpt struct {
		ID           string `json:"id"`
		SHA256       string `json:"sha256"`
		Body         string `json:"body"`
		OmittedBytes int    `json:"omittedBytes"`
	}
	var career []excerpt
	for _, s := range sources {
		if s.ID == "cv-vince-liem.typ" {
			continue
		}
		part := prefixUTF8(s.Body, 5000)
		career = append(career, excerpt{s.ID, s.SHA256, part, len(s.Body) - len(part)})
	}
	encode := func() ([]byte, error) {
		return json.Marshal(struct {
			InterviewID         string    `json:"interviewId"`
			OpportunityID       string    `json:"opportunityId"`
			OpportunityRevision int64     `json:"opportunityRevision"`
			RoleTitle           string    `json:"roleTitle"`
			Employer            string    `json:"employer"`
			OwnerContext        string    `json:"ownerContext"`
			RoleText            string    `json:"roleText"`
			Career              []excerpt `json:"career"`
		}{interview.ID, opportunity.ID, opportunity.Revision, opportunity.Title, company.Name, interview.Context, opportunity.OriginalText, career})
	}
	data, err := encode()
	if err == nil && len(data) > 32000 {
		for i := range career {
			full := career[i].Body
			career[i].Body = prefixUTF8(full, 1200)
			career[i].OmittedBytes += len(full) - len(career[i].Body)
		}
		data, err = encode()
	}
	if err != nil || len(data) > 32000 {
		return "", fmt.Errorf("%w: interview evidence", store.ErrInvalid)
	}
	return string(data), nil
}
