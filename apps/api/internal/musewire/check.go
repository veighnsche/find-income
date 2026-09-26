package musewire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// CheckPerformer runs one explicit selected-role check to a persisted body.
type CheckPerformer interface {
	Authorized() bool
	PerformCheck(ctx context.Context, opportunityID, checkID string) (store.CheckView, error)
}

// ErrCheckNotMuse reports an opportunity without Muse vacancy evidence. The
// caller leaves the check pending for other performers.
var ErrCheckNotMuse = errors.New("musewire: opportunity has no Muse vacancy evidence")

// CheckDeps binds one deterministic check performer. Retrieval runs only
// when Authorized is set; production stays unauthorized until the E12 live
// authorization, and fixtures authorize explicitly.
type CheckDeps struct {
	DB         *store.Store
	Actor      store.Actor
	Executor   researchcontract.Executor
	Captures   researchcontract.CaptureReader
	Bounds     musecode.Bounds
	Authorized bool
}

// Checker performs selected-role checks through E07's service and persists
// the sourced body. No session, model or Jev call is involved: retrieval is
// deterministic and bounded, and the E12 gate keeps it explicit.
type Checker struct {
	db         *store.Store
	actor      store.Actor
	executor   researchcontract.Executor
	captures   researchcontract.CaptureReader
	bounds     musecode.Bounds
	authorized bool
}

// NewChecker validates one performer composition.
func NewChecker(deps CheckDeps) (*Checker, error) {
	if err := deps.Bounds.Validate(); err != nil {
		return nil, err
	}
	if deps.DB == nil || deps.Executor == nil || deps.Captures == nil {
		return nil, errors.New("musewire: store, executor and capture reader required")
	}
	if deps.Actor.Kind == "" || deps.Actor.ID == "" {
		return nil, errors.New("musewire: check actor required")
	}
	return &Checker{db: deps.DB, actor: deps.Actor, executor: deps.Executor,
		captures: deps.Captures, bounds: deps.Bounds, authorized: deps.Authorized}, nil
}

// Authorized reports whether this performer may retrieve.
func (c *Checker) Authorized() bool { return c.authorized }

// PerformCheck runs E07 over the opportunity's saved vacancy evidence and
// persists the sourced body. Without authorization it refuses before any
// retrieval; without saved evidence it completes as blocked instead of
// inventing questions.
func (c *Checker) PerformCheck(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
	if !c.authorized {
		return store.CheckView{}, errors.New("musewire: check retrieval needs the E12 live authorization")
	}
	opportunity, err := c.db.Opportunity(ctx, opportunityID)
	if err != nil {
		return store.CheckView{}, fmt.Errorf("musewire: read opportunity: %w", err)
	}
	company, err := c.db.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return store.CheckView{}, fmt.Errorf("musewire: read company: %w", err)
	}
	finding, err := c.db.GetOpportunityFinding(ctx, opportunityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Not a Muse-discovered role: leave the check for other
			// performers instead of blocking a path this bridge cannot see.
			return store.CheckView{}, ErrCheckNotMuse
		}
		return store.CheckView{}, fmt.Errorf("musewire: read finding: %w", err)
	}
	if finding.SourceRef == nil {
		return c.saveBlocked(ctx, opportunityID, checkID, store.CheckBlockedSourceUnavailable,
			"saved finding carries no vacancy receipt")
	}
	seedServer, err := publicresearch.NewServer(publicresearch.Deps{
		Executor: c.executor, Captures: c.captures, Bounds: c.bounds,
		RunID: checkID, Generation: 1,
	})
	if err != nil {
		return store.CheckView{}, err
	}
	seeded, err := seedServer.SeedVacancies([]publicresearch.SeedVacancy{{
		PageURL: strings.TrimSpace(opportunity.SourceURL), EmployerName: company.Name,
		Title: strings.TrimSpace(opportunity.Title), LocationText: strings.TrimSpace(opportunity.LocationText),
		ReceiptRef: finding.SourceRef.SourceRevision,
	}})
	if err != nil {
		return c.saveBlocked(ctx, opportunityID, checkID, store.CheckBlockedSourceUnavailable,
			"saved vacancy evidence does not validate: "+err.Error())
	}
	// Retrieval runs under a real check round: the production executor
	// authorizes research.dispatch against the round ledger, so a bare
	// check id would fence every fetch.
	round, created, err := c.db.StartRound(ctx, c.actor, store.StartRoundInput{
		RequestKey: "muse-check:" + checkID, Intent: "muse.check", Outcome: "pending",
		ProfileVersion: finding.ProfileVersion,
		Scope: store.RoundScope{
			Operations: []string{store.RoundResearchSearch, store.RoundResearchFetch, store.RoundResearchAPI},
			Resources:  []string{store.ResearchAuthorityResource},
		},
		Limits:   store.RoundAllowance{Requests: 20, Items: 200, Tools: 20, Turns: 5},
		Deadline: time.Now().Add(c.bounds.MaxWallClock),
	})
	if err != nil {
		return store.CheckView{}, fmt.Errorf("musewire: start check round: %w", err)
	}
	if !created {
		return store.CheckView{}, fmt.Errorf("musewire: check %q was already attempted", checkID)
	}
	if _, err := c.db.ActivateRound(ctx, c.actor, round.ID); err != nil {
		return store.CheckView{}, fmt.Errorf("musewire: activate check round: %w", err)
	}
	view, err := c.runCheck(ctx, opportunity, checkID, round.ID, seedServer, seeded[0].VacancyRef)
	state := store.RoundCompleted
	reason := "check completed"
	if err != nil {
		state = store.RoundFailed
		reason = "check failed: " + err.Error()
	}
	if len(reason) > 100 {
		reason = reason[:100]
	}
	summary, summaryErr := json.Marshal(map[string]any{"checkID": checkID, "opportunityID": opportunityID})
	if summaryErr != nil {
		return store.CheckView{}, summaryErr
	}
	if _, finishErr := c.db.FinishRound(ctx, c.actor, round.ID, state, reason, reason, summary); finishErr != nil {
		return store.CheckView{}, fmt.Errorf("musewire: finish check round: %w", finishErr)
	}
	return view, err
}

func (c *Checker) runCheck(ctx context.Context, opportunity store.Opportunity, checkID, roundID string, server *publicresearch.Server, vacancyRef string) (store.CheckView, error) {
	checkService, err := publicresearch.NewCheckService(publicresearch.CheckDeps{
		Executor: c.executor, Captures: c.captures, Saved: server,
		Bounds: c.bounds, RunID: roundID, Generation: 1,
	})
	if err != nil {
		return store.CheckView{}, err
	}
	report, err := checkService.Check(ctx, publicresearch.NewSelection(vacancyRef))
	if err != nil {
		return store.CheckView{}, err
	}
	if len(report.Roles) != 1 {
		return store.CheckView{}, fmt.Errorf("musewire: check returned %d roles, want 1", len(report.Roles))
	}
	return c.complete(ctx, opportunity, checkID, report.Roles[0])
}

func (c *Checker) saveBlocked(ctx context.Context, opportunityID, checkID, code, detail string) (store.CheckView, error) {
	return c.db.SaveJobCheckBody(ctx, c.actor, store.CheckSaveInput{
		OpportunityID: opportunityID, CheckID: checkID,
		Blocked: &store.CheckBlockedInput{Code: code, Detail: detail},
	})
}

// complete translates one E07 role into the check body. Checked roles with
// zero questions complete as blocked/question-unresolved: the store gate
// needs at least one sourced question and the bridge never invents one.
func (c *Checker) complete(ctx context.Context, opportunity store.Opportunity, checkID string, role publicresearch.RoleCheck) (store.CheckView, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if role.Outcome == publicresearch.RoleBlocked {
		if role.BlockReason == "unknown_vacancy" {
			return store.CheckView{}, fmt.Errorf("musewire: seeded vacancy %q not found", role.VacancyRef)
		}
		return c.saveBlocked(ctx, opportunity.ID, checkID, store.CheckBlockedSourceUnavailable, blockDetail(role))
	}
	if len(role.Questions) == 0 {
		save := store.CheckSaveInput{OpportunityID: opportunity.ID, CheckID: checkID,
			Blocked: &store.CheckBlockedInput{Code: store.CheckBlockedQuestionsUnresolved,
				Detail: "retrieved capture held no employer questions"}}
		if len(role.CaptureIDs) > 0 && strings.TrimSpace(opportunity.SourceURL) != "" {
			save.Vacancy = c.vacancyInput(opportunity, role, now)
		}
		return c.db.SaveJobCheckBody(ctx, c.actor, save)
	}
	questions := make([]store.CheckQuestionInput, 0, len(role.Questions))
	for _, question := range role.Questions {
		mapped, err := mapQuestion(role, question)
		if err != nil {
			return store.CheckView{}, err
		}
		questions = append(questions, mapped)
	}
	excerpt, err := c.routeExcerpt(ctx, role, opportunity)
	if err != nil {
		return store.CheckView{}, err
	}
	payload, err := json.Marshal(map[string]any{"reused": role.ReusedCapture, "questions": len(questions)})
	if err != nil {
		return store.CheckView{}, err
	}
	activity := []store.CheckActivityInput{}
	if len(role.CaptureIDs) > 0 {
		activity = append(activity, store.CheckActivityInput{Kind: "muse.check_performed",
			Outcome: string(researchcontract.OutcomeOK), CaptureID: role.CaptureIDs[0], Payload: payload})
	}
	return c.db.SaveJobCheckBody(ctx, c.actor, store.CheckSaveInput{
		OpportunityID: opportunity.ID, CheckID: checkID,
		Vacancy:            c.vacancyInput(opportunity, role, now),
		RequestedDocuments: []store.RequestedDocumentInput{},
		Requirements:       []store.CheckRequirementInput{},
		Route: store.CheckRouteInput{Judgment: store.CheckRouteJudgmentUnresolved,
			SourceExcerpt: excerpt, ObservedAt: now},
		Gaps: []store.CheckGapInput{{Kind: store.CheckGapOther, Consequential: false,
			Description: "Requested documents and requirements are not assessed; this check covers sourced questions only."}},
		Questions: questions,
		Activity:  activity,
	})
}

func (c *Checker) vacancyInput(opportunity store.Opportunity, role publicresearch.RoleCheck, now string) store.CheckVacancyInput {
	return store.CheckVacancyInput{
		CaptureIDs: role.CaptureIDs, Completeness: store.CaptureComplete,
		SourceURL: strings.TrimSpace(opportunity.SourceURL), RetrievedAt: now,
	}
}

func blockDetail(role publicresearch.RoleCheck) string {
	if strings.TrimSpace(role.Detail) != "" {
		return role.Detail
	}
	return "check blocked: " + role.BlockReason
}

// mapQuestion converts one verbatim question with its recorded capture span.
// A missing span or an out-of-gate prompt fails the check instead of
// persisting an unsourced question.
func mapQuestion(role publicresearch.RoleCheck, question musecode.PublicQuestion) (store.CheckQuestionInput, error) {
	if len(question.PromptText) < 1 || len(question.PromptText) > 2000 {
		return store.CheckQuestionInput{}, fmt.Errorf("musewire: question %q prompt fails the text gate", question.QuestionRef)
	}
	for _, source := range role.Sources {
		if source.QuestionRef != question.QuestionRef {
			continue
		}
		required := store.CheckOptional
		if question.Required {
			required = store.CheckRequired
		}
		return store.CheckQuestionInput{Text: question.PromptText, Required: required,
			SourceSpan:    store.CheckSourceSpan{CaptureID: source.CaptureID, Start: source.Start, End: source.End},
			SourceExcerpt: question.PromptText}, nil
	}
	return store.CheckQuestionInput{}, fmt.Errorf("musewire: question %q has no recorded source span", question.QuestionRef)
}

// routeExcerpt cites the head of the first retrieved capture. The
// deterministic check judges no routes; the excerpt keeps the route section
// honestly sourced to observed bytes.
func (c *Checker) routeExcerpt(ctx context.Context, role publicresearch.RoleCheck, opportunity store.Opportunity) (string, error) {
	if len(role.CaptureIDs) > 0 {
		_, reader, err := c.captures.OpenCapture(ctx, role.CaptureIDs[0])
		if err == nil {
			defer reader.Close()
			if head, err := io.ReadAll(io.LimitReader(reader, 501)); err == nil && strings.TrimSpace(string(head)) != "" {
				return string(head), nil
			}
		}
	}
	fallback := strings.TrimSpace(opportunity.Title)
	if fallback == "" {
		return "", fmt.Errorf("musewire: no sourced text for route excerpt")
	}
	return fallback, nil
}
