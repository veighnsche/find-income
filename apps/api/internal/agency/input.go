package agency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type inputReport struct {
	Code                    string                    `json:"code"`
	InstructionID           string                    `json:"instructionId,omitempty"`
	IngestionID             string                    `json:"ingestionId,omitempty"`
	OriginalProfileVersion  int64                     `json:"originalProfileVersion"`
	EffectiveProfileVersion int64                     `json:"effectiveProfileVersion"`
	AppliedChanges          []store.RoundHistoryEvent `json:"appliedChanges"`
	Unresolved              []string                  `json:"unresolved"`
	Recommendation          *homeRecommendation       `json:"recommendation,omitempty"`
}

func (e *Engine) CheckRoundInput(ctx context.Context, owner store.Actor, input store.StartRoundInput) error {
	if input.Outcome != "process_input" {
		return nil
	}
	instructionID, _, ok := inputReference(input.Scope)
	if !ok {
		return store.ErrInvalid
	}
	if instructionID == "" {
		return nil
	}
	instruction, err := e.Store.OwnerInstruction(ctx, owner, instructionID)
	if err != nil {
		return err
	}
	if instruction.RevokedAt != "" {
		return store.ErrFenced
	}
	return nil
}

func inputReference(scope store.RoundScope) (instructionID, ingestionID string, ok bool) {
	for _, ref := range scope.InputRefs {
		if strings.HasPrefix(ref, "instruction:") {
			if instructionID != "" || ingestionID != "" || len(ref) == len("instruction:") {
				return "", "", false
			}
			instructionID = strings.TrimPrefix(ref, "instruction:")
		} else if strings.HasPrefix(ref, "ingestion:") {
			if instructionID != "" || ingestionID != "" || len(ref) == len("ingestion:") {
				return "", "", false
			}
			ingestionID = strings.TrimPrefix(ref, "ingestion:")
		}
	}
	return instructionID, ingestionID, instructionID != "" || ingestionID != ""
}

func (e *Engine) launchInput(r store.Round) error {
	if r.State != store.RoundRunning || r.Outcome != "process_input" {
		return store.ErrFenced
	}
	if _, _, ok := inputReference(r.Scope); !ok {
		return store.ErrInvalid
	}
	if err := e.CheckRound(context.Background(), r.Outcome); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = map[string]*activeWorker{}
	}
	if _, exists := e.active[r.ID]; exists {
		return store.ErrConflict
	}
	base := e.Context
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithDeadline(base, r.Deadline)
	worker := &activeWorker{cancel: cancel, done: make(chan struct{})}
	e.active[r.ID] = worker
	go func() {
		defer cancel()
		defer e.workerDone(r.ID, worker)
		e.runInput(ctx, r)
	}()
	return nil
}

func (e *Engine) finishInput(ctx context.Context, roundID string, owner store.Actor, detail inputReport) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	r, err := e.Store.Round(cleanup, roundID)
	if err != nil || r.State != store.RoundRunning {
		return
	}
	if !time.Now().Before(r.Deadline) {
		_, _ = e.Store.ExpireRound(cleanup, roundID)
		return
	}
	detail.OriginalProfileVersion, detail.EffectiveProfileVersion = r.InitialProfileVersion, r.ProfileVersion
	if detail.AppliedChanges == nil {
		detail.AppliedChanges = []store.RoundHistoryEvent{}
	}
	if detail.Unresolved == nil {
		detail.Unresolved = []string{}
	}
	status := "complete"
	if len(detail.Unresolved) != 0 || len(detail.AppliedChanges) == 0 {
		status = "partial"
	}
	facts := outcomeRecommendationFacts{Outcome: r.Outcome, Code: detail.Code, ResultID: r.ID,
		AppliedChanges: len(detail.AppliedChanges), UnresolvedCount: len(detail.Unresolved)}
	detail.Recommendation = e.computeOutcomeRecommendation(ctx, r, facts)
	encoded, _ := json.Marshal(detail)
	_, _ = e.Store.FinishRound(cleanup, owner, roundID, store.RoundCompleted, detail.Code, status, encoded)
}

func (e *Engine) inputHistory(ctx context.Context, roundID string) ([]store.RoundHistoryEvent, error) {
	history, err := e.Store.RoundHistory(ctx, roundID)
	if err != nil {
		return nil, err
	}
	changes := make([]store.RoundHistoryEvent, 0, len(history))
	for _, event := range history {
		switch event.Operation {
		case store.RoundCreateCompany, store.RoundSaveSourceOpportunity, "opportunity.source_create", "opportunity.source_refresh", store.RoundCorrectPreferences, store.RoundCorrectOpportunity,
			store.RoundCorrectEvidence, store.RoundRelationshipCorrect:
			changes = append(changes, event)
		}
	}
	return changes, nil
}

func inputHasTargetChange(changes []store.RoundHistoryEvent, instructionKind, ingestionID string) bool {
	wanted := ""
	if ingestionID != "" {
		wanted = store.RoundSaveSourceOpportunity
	} else {
		switch instructionKind {
		case "profile":
			wanted = store.RoundCorrectPreferences
		case "opportunity":
			wanted = store.RoundCorrectOpportunity
		case "evidence":
			wanted = store.RoundCorrectEvidence
		case "relationship":
			wanted = store.RoundRelationshipCorrect
		}
	}
	for _, change := range changes {
		if change.Operation == wanted || ingestionID != "" && (change.Operation == "opportunity.source_create" || change.Operation == "opportunity.source_refresh") {
			return true
		}
	}
	return false
}

func (e *Engine) inputEvidence(ctx context.Context, r store.Round, instruction store.OwnerInstruction) (string, string, error) {
	var current any
	resource := ""
	switch instruction.TargetKind {
	case "profile":
		resource = "profile:current"
		profile, err := e.Store.CurrentPreferences(ctx)
		if err != nil || profile.Version != instruction.ExpectedRevision {
			return "", "", store.ErrConflict
		}
		current = profile
	case "opportunity":
		resource = "opportunity:" + instruction.TargetID
		value, err := e.Store.Opportunity(ctx, instruction.TargetID)
		if err != nil || value.Revision != instruction.ExpectedRevision || value.ArchivedAt != "" {
			return "", "", store.ErrConflict
		}
		current = value
	case "evidence":
		resource = "evidence:" + instruction.TargetID
		value, err := e.Store.Evidence(ctx, instruction.TargetID)
		if err != nil || instruction.ExpectedRevision != 1 {
			return "", "", store.ErrConflict
		}
		current = value
	case "relationship":
		resource = "relationship:" + instruction.TargetID
		value, err := e.Store.RelationshipTarget(ctx, instruction.TargetID)
		if err != nil {
			return "", "", err
		}
		revision := int64(0)
		if value.Counterparty != nil {
			revision = value.Counterparty.Revision
		} else if value.Event != nil {
			revision = value.Event.Revision
		} else if value.Route != nil {
			revision = value.Route.Revision
		}
		if revision != instruction.ExpectedRevision {
			return "", "", store.ErrConflict
		}
		current = value
	default:
		return "", "", store.ErrInvalid
	}
	if !hasRoundResource(r.Scope.Resources, resource) {
		return "", "", store.ErrFenced
	}
	encoded, err := json.Marshal(struct {
		Instruction store.OwnerInstruction `json:"instruction"`
		Current     any                    `json:"current"`
	}{instruction, current})
	if err != nil || len(encoded) > 32000 {
		return "", "", store.ErrInvalid
	}
	return resource, string(encoded), nil
}

func (e *Engine) runInput(ctx context.Context, initial store.Round) {
	owner := initial.Actor
	detail := inputReport{Code: "input_unresolved"}
	defer func() {
		if !errors.Is(ctx.Err(), context.Canceled) {
			e.finishInput(ctx, initial.ID, owner, detail)
		}
	}()
	r, err := e.Store.RebindInputProfile(ctx, owner, initial.ID)
	if err != nil {
		detail.Code, detail.Unresolved = "profile_context_changed", []string{"The current profile changed outside this input round."}
		return
	}
	r, err = e.live(ctx, r.ID, r.Generation, r.ProfileVersion)
	if err != nil {
		detail.Code = terminalCode(err)
		return
	}
	instructionID, ingestionID, ok := inputReference(r.Scope)
	if !ok {
		detail.Code = "input_scope_invalid"
		return
	}
	detail.InstructionID, detail.IngestionID = instructionID, ingestionID
	changes, err := e.inputHistory(ctx, r.ID)
	if err != nil {
		detail.Code = "input_history_unavailable"
		return
	}
	instructionKind := ""
	if instructionID != "" {
		instruction, readErr := e.Store.OwnerInstruction(ctx, owner, instructionID)
		if readErr != nil {
			detail.Code = "input_instruction_unavailable"
			return
		}
		instructionKind = instruction.TargetKind
	}
	if inputHasTargetChange(changes, instructionKind, ingestionID) {
		detail.AppliedChanges = changes
		detail.Code = "input_applied"
		if ingestionID != "" {
			if err := e.organiseInputVacancy(ctx, r, ingestionID); err != nil {
				detail.Code, detail.Unresolved = "organisation_unresolved", []string{"The vacancy was saved, but semantic organisation could not be recorded."}
			}
		}
		return
	}
	resource, evidence, brief := "", "", ""
	if instructionID != "" {
		instruction, err := e.Store.OwnerInstruction(ctx, owner, instructionID)
		if err != nil || instruction.RevokedAt != "" || instruction.RoundID != "" && instruction.RoundID != r.ID {
			detail.Code, detail.Unresolved = "input_authority_changed", []string{"The selected owner instruction is no longer active for this round."}
			return
		}
		resource, evidence, err = e.inputEvidence(ctx, r, instruction)
		if err != nil {
			detail.Code, detail.Unresolved = "input_target_changed", []string{"The selected record changed before its correction could be applied."}
			return
		}
		brief = "Apply the saved owner instruction to exactly the supplied current record. Use round_context and the matching guarded round_mutation or round_evidence_correction tool. Use ownerInstructionId and expectedRevision from evidence. Preserve fields not addressed by the instruction. Do not invent personal facts, create unrelated records, or begin discovery. If the desired change cannot be supported, leave it unresolved and do not make a speculative row change."
	} else {
		resource, evidence, err = e.ownerVacancyEvidence(ctx, r, ingestionID)
		if err != nil {
			detail.Code = "source_unresolved"
			detail.Unresolved = []string{"The saved vacancy source could not be processed."}
			if errors.Is(err, ErrUnsupportedOwnerSource) {
				detail.Code = "unsupported_source_url"
				detail.Unresolved = []string{"The saved vacancy URL could not be verified from a supported source. Paste the complete vacancy text to process it."}
			} else if errors.Is(err, store.ErrInvalid) {
				detail.Code = "source_exceeds_turn_evidence"
				detail.Unresolved = []string{"The complete vacancy was saved, but its text exceeds the bounded processing capacity for this action."}
			}
			return
		}
		brief = "Process only the exact owner supplied vacancy in evidence. Use round_context, choose the matching scoped company or create it with round_mutation company.create, then use round_mutation opportunity.source_save for the supplied sourceOpeningId, company revision and current source text. Do not choose a different opening, infer missing employer facts, browse unrelated jobs or claim an application was sent. Leave unsupported fields unresolved."
	}
	_, err = e.Runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, codexservice.RoundTurnInput{
		RequestKey: fmt.Sprintf("process:%s:g%d", strings.TrimPrefix(strings.TrimPrefix(resource, "opportunity:"), "campaign:"), r.Generation),
		ResourceID: resource, Brief: brief, Evidence: evidence})
	if err != nil {
		detail.Code, detail.Unresolved = terminalCode(err), []string{"The commissioned turn did not complete its record work."}
		return
	}
	changes, err = e.inputHistory(ctx, r.ID)
	if err != nil {
		detail.Code = "input_history_unavailable"
		return
	}
	if !inputHasTargetChange(changes, instructionKind, ingestionID) {
		detail.AppliedChanges = changes
		detail.Code, detail.Unresolved = "input_not_applied", []string{"The selected record change was not made from the supplied input."}
		return
	}
	detail.AppliedChanges, detail.Code = changes, "input_applied"
	for _, event := range changes {
		if event.Operation == store.RoundCorrectPreferences {
			if _, err := e.Store.RebindInputProfile(ctx, owner, r.ID); err != nil {
				detail.Code, detail.Unresolved = "profile_rebind_unresolved", []string{"The profile change was saved, but this round could not bind its new effective version."}
			}
			return
		}
	}
	if ingestionID != "" {
		if err := e.organiseInputVacancy(ctx, r, ingestionID); err != nil {
			detail.Code, detail.Unresolved = "organisation_unresolved", []string{"The vacancy was saved, but semantic organisation could not be recorded."}
		}
	}
}
