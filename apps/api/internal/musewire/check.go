package musewire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ErrCheckNotMuse reports a check for a role no Muse finding backs.
// Other performers own those paths; the bridge never blocks them.
var ErrCheckNotMuse = errors.New("musewire: check is not Muse-backed")

// CheckPerformer runs the selected-role check behind the Check action.
type CheckPerformer interface {
	Authorized() bool
	PerformCheck(ctx context.Context, opportunityID, checkID string) (store.CheckView, error)
}

// CheckDeps composes the model-driven selected-role check performer. The
// transport conducts one bounded Contributor check turn per started
// check; tests replay scripts while production runs the live CLI.
type CheckDeps struct {
	DB       *store.Store
	Actor    store.Actor
	Executor researchcontract.Executor
	Captures researchcontract.CaptureReader
	Bounds   musecode.Bounds
	// Transport conducts check turns. Facts pins the session; Workspaces
	// roots the per-check session dirs. Cursors persists crash-recovery
	// cursors under check refs.
	Transport  musecode.Transport
	Cursors    musecode.CursorStore
	Facts      musecode.Facts
	Workspaces string
	// Authorized gates retrieval. Production wires it from the
	// Contributor lane readiness; fixtures set it directly.
	Authorized bool
}

// Checker conducts one bounded Contributor check turn per started check
// and persists the adapter-verified body. PerformCheck returns the
// checking view immediately; the turn conducts behind it and the owner
// polls the check.
type Checker struct {
	db         *store.Store
	actor      store.Actor
	executor   researchcontract.Executor
	captures   researchcontract.CaptureReader
	bounds     musecode.Bounds
	transport  musecode.Transport
	cursors    musecode.CursorStore
	facts      musecode.Facts
	workspaces string
	authorized bool

	mu      sync.Mutex
	servers map[string]*publicresearch.Server
}

func NewChecker(deps CheckDeps) (*Checker, error) {
	if deps.DB == nil || deps.Executor == nil || deps.Captures == nil {
		return nil, errors.New("musewire: checker needs database, executor and captures")
	}
	if err := deps.Bounds.Validate(); err != nil {
		return nil, err
	}
	if deps.Transport == nil || deps.Cursors == nil {
		return nil, errors.New("musewire: checker needs a check transport and cursors")
	}
	if deps.Workspaces == "" || !filepath.IsAbs(deps.Workspaces) {
		return nil, errors.New("musewire: checker needs an absolute workspaces root")
	}
	return &Checker{db: deps.DB, actor: deps.Actor, executor: deps.Executor,
		captures: deps.Captures, bounds: deps.Bounds, transport: deps.Transport,
		cursors: deps.Cursors, facts: deps.Facts, workspaces: deps.Workspaces,
		authorized: deps.Authorized, servers: map[string]*publicresearch.Server{}}, nil
}

// Authorized reports whether this performer may retrieve.
func (c *Checker) Authorized() bool { return c.authorized }

// ServerForCheck returns the retained run server of one conducting check.
// The live CLI never touches it (direct seam); fixtures observe the
// seeded vacancy through it. R2 removes this with the harness.
func (c *Checker) ServerForCheck(checkRef string) (*publicresearch.Server, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	server, ok := c.servers[checkRef]
	return server, ok
}

// checkRefOf names the session, workspace and cursor of one check.
func checkRefOf(checkID string) string { return "check-" + checkID }

// PerformCheck starts the bounded check turn behind one started check
// and returns the checking view. Without authorization it refuses
// before any retrieval; without saved evidence it completes as blocked
// instead of inventing questions. A repeated start while the turn is
// conducting replays the current view instead of conducting twice.
func (c *Checker) PerformCheck(ctx context.Context, opportunityID, checkID string) (store.CheckView, error) {
	if !c.authorized {
		return store.CheckView{}, errors.New("musewire: check retrieval needs the Contributor live authorization")
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
	checkRef := checkRefOf(checkID)
	server, err := publicresearch.NewServer(publicresearch.Deps{
		Executor: c.executor, Captures: c.captures, Bounds: c.bounds,
		RunID: checkID, Generation: 1,
	})
	if err != nil {
		return store.CheckView{}, err
	}
	seeded, err := server.SeedVacancies([]publicresearch.SeedVacancy{{
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
		// A turn is already conducting (or converged) behind this
		// check: replay the current view instead of conducting twice.
		return c.db.GetJobCheck(ctx, opportunityID, checkID)
	}
	if _, err := c.db.ActivateRound(ctx, c.actor, round.ID); err != nil {
		return store.CheckView{}, fmt.Errorf("musewire: activate check round: %w", err)
	}
	c.mu.Lock()
	c.servers[checkRef] = server
	c.mu.Unlock()
	input := musecode.CheckInput{VacancyRef: seeded[0].VacancyRef,
		PageURL: strings.TrimSpace(opportunity.SourceURL), ReceiptRef: finding.SourceRef.SourceRevision}
	go c.conductCheck(context.WithoutCancel(ctx), opportunity, checkID, checkRef, round.ID, server, input)
	return c.db.GetJobCheck(ctx, opportunityID, checkID)
}

// conductCheck runs one check turn to its verdict in the background. A
// verified body completes the check; unverifiable turns complete as
// blocked with the coded reason. Only infrastructure failures fail the
// round: a blocked verdict is the delivered product.
func (c *Checker) conductCheck(ctx context.Context, opportunity store.Opportunity, checkID, checkRef, roundID string, server *publicresearch.Server, input musecode.CheckInput) {
	finish := func(state store.RoundState, reason string) {
		if len(reason) > 100 {
			reason = reason[:100]
		}
		summary, _ := json.Marshal(map[string]any{"checkID": checkID, "opportunityID": opportunity.ID})
		_, _ = c.db.FinishRound(ctx, c.actor, roundID, state, reason, reason, summary)
	}
	blocked := func(code, detail string) {
		if _, err := c.saveBlocked(ctx, opportunity.ID, checkID, code, detail); err != nil {
			finish(store.RoundFailed, "check failed: "+err.Error())
			return
		}
		finish(store.RoundCompleted, "check completed")
	}
	status := musecode.Check(musecode.TierContributor, c.facts)
	if !status.Available {
		blocked(store.CheckBlockedOther, "contributor lane unavailable ("+status.Code+"): "+status.Detail)
		return
	}
	workspace := filepath.Join(c.workspaces, "checks", checkRef)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		blocked(store.CheckBlockedOther, "check workspace unavailable: "+err.Error())
		return
	}
	spec, err := musecode.NewSession(status, workspace, c.bounds, nil)
	if err != nil {
		blocked(store.CheckBlockedOther, "check session refused: "+err.Error())
		return
	}
	var texts []string
	supervisor := musecode.NewSupervisor(&captureTransport{next: c.transport, texts: &texts},
		c.cursors, saveValidator(server), c.facts.EffectiveModel)
	if _, err := supervisor.StartRun(ctx, checkRef, spec, input, c.facts); err != nil {
		blocked(store.CheckBlockedOther, "check turn refused: "+err.Error())
		return
	}
	terminal, ok := supervisor.Result(checkRef)
	if !ok {
		blocked(store.CheckBlockedOther, "check turn has no terminal result")
		return
	}
	if terminal.Outcome != musecode.OutcomeCompleted {
		blocked(store.CheckBlockedOther, "check turn ended "+string(terminal.Outcome)+": "+terminal.Detail)
		return
	}
	text := ""
	for _, candidate := range texts {
		if strings.TrimSpace(candidate) != "" {
			text = candidate
		}
	}
	// The adapter re-fetches every cited source URL through the run
	// server and verifies each quote verbatim; verified questions save
	// under the checked vacancy via the deterministic saver.
	adapted, err := (&CheckAdapter{Captures: c.captures, Saved: server,
		Fetch: func(fetchCtx context.Context, sourceURL string) (string, error) {
			fetched, err := server.FetchURL(fetchCtx, sourceURL)
			if err != nil {
				return "", err
			}
			return fetched.CaptureID, nil
		},
		VacancyRef: input.VacancyRef,
	}).Adapt(ctx, time.Now().UTC().Format(time.RFC3339), text)
	if err != nil {
		blocked(store.CheckBlockedOther, "check turn returned malformed findings: "+err.Error())
		return
	}
	adapted.Gaps = append(adapted.Gaps, uncitedQuestionGaps(server, adapted.CitedQuestions)...)
	if len(adapted.Questions) == 0 {
		detail := "check verified no employer questions"
		if len(adapted.Gaps) > 0 {
			shown := adapted.Gaps
			if len(shown) > 3 {
				shown = shown[:3]
			}
			detail += ": " + strings.Join(shown, "; ")
			if len(detail) > 2000 {
				detail = detail[:2000]
			}
		}
		blocked(store.CheckBlockedQuestionsUnresolved, detail)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	captures := verifiedCaptures(adapted)
	_, err = c.db.SaveJobCheckBody(ctx, c.actor, store.CheckSaveInput{
		OpportunityID: opportunity.ID, CheckID: checkID,
		Vacancy: store.CheckVacancyInput{CaptureIDs: captures,
			Completeness: store.CaptureComplete, SourceURL: strings.TrimSpace(opportunity.SourceURL),
			RetrievedAt: now},
		RequestedDocuments: nonNilDocuments(adapted.Documents),
		Requirements:       nonNilRequirements(adapted.Requirements),
		Route:              orUnresolvedRoute(adapted),
		Gaps:               checkGaps(adapted.Gaps),
		Questions:          adapted.Questions,
		Activity: []store.CheckActivityInput{{Kind: "muse.check_performed",
			Outcome: string(researchcontract.OutcomeOK), CaptureID: firstCapture(captures),
			Payload: checkActivityPayload(adapted)}},
	})
	if err != nil {
		finish(store.RoundFailed, "check failed: "+err.Error())
		return
	}
	finish(store.RoundCompleted, "check completed")
}

// uncitedQuestionGaps names server-saved questions the turn never cited.
// Saved but unverifiable questions stay disclosed instead of silently
// dropped.
func uncitedQuestionGaps(server *publicresearch.Server, cited []string) []string {
	seen := map[string]bool{}
	for _, ref := range cited {
		seen[ref] = true
	}
	gaps := []string{}
	for _, ref := range server.SavedQuestionRefs() {
		if seen[ref] {
			continue
		}
		if _, ok := server.Question(ref); !ok {
			continue
		}
		gaps = append(gaps, "saved question "+ref+" was not cited by the check turn")
	}
	return gaps
}

func verifiedCaptures(adapted AdaptedCheck) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, requirement := range adapted.Requirements {
		add(requirement.SourceSpan.CaptureID)
	}
	for _, document := range adapted.Documents {
		add(document.SourceSpan.CaptureID)
	}
	for _, question := range adapted.Questions {
		add(question.SourceSpan.CaptureID)
	}
	return out
}

func firstCapture(captures []string) string {
	if len(captures) == 0 {
		return ""
	}
	return captures[0]
}

func nonNilDocuments(in []store.RequestedDocumentInput) []store.RequestedDocumentInput {
	if in == nil {
		return []store.RequestedDocumentInput{}
	}
	return in
}

func nonNilRequirements(in []store.CheckRequirementInput) []store.CheckRequirementInput {
	if in == nil {
		return []store.CheckRequirementInput{}
	}
	return in
}

// orUnresolvedRoute keeps the verified route, or an explicitly
// unresolved one anchored on the first verified excerpt when the turn's
// route claim dropped.
func orUnresolvedRoute(adapted AdaptedCheck) store.CheckRouteInput {
	if adapted.Route.Judgment != "" {
		return adapted.Route
	}
	excerpt := ""
	if len(adapted.Requirements) > 0 {
		excerpt = adapted.Requirements[0].SourceExcerpt
	} else if len(adapted.Documents) > 0 {
		excerpt = adapted.Documents[0].SourceExcerpt
	} else if len(adapted.Questions) > 0 {
		excerpt = adapted.Questions[0].SourceExcerpt
	}
	return store.CheckRouteInput{Judgment: store.CheckRouteJudgmentUnresolved,
		SourceExcerpt: excerpt, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
}

// checkGaps maps adapter diagnostics into check gaps, bounded by the
// store gate with an honest overflow note.
func checkGaps(diagnostics []string) []store.CheckGapInput {
	gaps := make([]store.CheckGapInput, 0, len(diagnostics)+1)
	for _, diagnostic := range diagnostics {
		if len(gaps) >= 100 {
			break
		}
		gaps = append(gaps, store.CheckGapInput{Description: diagnostic, Kind: store.CheckGapOther})
	}
	if len(diagnostics) > len(gaps) {
		gaps = append(gaps[:99], store.CheckGapInput{
			Description: fmt.Sprintf("%d further verification gaps withheld over the gate", len(diagnostics)-99),
			Kind:        store.CheckGapOther})
	}
	return gaps
}

func checkActivityPayload(adapted AdaptedCheck) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"questions": len(adapted.Questions),
		"requirements": len(adapted.Requirements), "documents": len(adapted.Documents),
		"gaps": len(adapted.Gaps)})
	return raw
}

func (c *Checker) saveBlocked(ctx context.Context, opportunityID, checkID, code, detail string) (store.CheckView, error) {
	return c.db.SaveJobCheckBody(ctx, c.actor, store.CheckSaveInput{
		OpportunityID: opportunityID, CheckID: checkID,
		Blocked: &store.CheckBlockedInput{Code: code, Detail: detail},
	})
}
