package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func inputRefScope(ref, replacement string) []string {
	refs := []string{ref}
	if replacement != "" {
		refs = append(refs, replacement)
	}
	return refs
}

func (h *Handler) processInputScope(ctx context.Context, kind, id string, profileVersion int64, ref, replacement string) (store.StartRoundInput, error) {
	input := store.StartRoundInput{Intent: "Apply the owner's contextual input to its named record and report saved changes or unresolved facts.", Outcome: "process_input", ProfileVersion: profileVersion,
		Scope:  store.RoundScope{InputRefs: inputRefScope(ref, replacement), Delegates: []string{"codex-runner"}},
		Limits: store.RoundAllowance{Requests: 6, Items: 3, Tools: 7, Turns: 1}, Deadline: time.Now().Add(30 * time.Minute).UTC()}
	common := []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundJevRequest}
	switch kind {
	case "campaign":
		input.Intent = "Save the supplied vacancy as one source linked opportunity and report unsupported facts."
		input.Limits = store.RoundAllowance{Requests: 7, Items: 3, Tools: 9, Turns: 2}
		input.Scope.Resources = []string{"campaign:active", ref}
		input.Scope.Operations = append(common, store.RoundCreateCompany, store.RoundSaveSourceOpportunity, store.RoundFetchSource)
		cursor := ""
		for {
			page, err := h.database.ListCompanies(ctx, store.CompanyListOptions{Cursor: cursor, Limit: 100})
			if err != nil {
				return store.StartRoundInput{}, err
			}
			for _, company := range page.Items {
				if company.ArchivedAt == "" {
					input.Scope.Resources = append(input.Scope.Resources, "company:"+company.ID)
				}
			}
			if len(input.Scope.Resources) > 950 {
				return store.StartRoundInput{}, store.ErrInvalid
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	case "profile":
		input.Intent = "Apply the owner's correction to the current profile and report the effective version."
		input.Scope.Resources = []string{"profile:current"}
		input.Scope.Operations = append(common, store.RoundCorrectPreferences)
	case "opportunity":
		opportunity, err := h.database.Opportunity(ctx, id)
		if err != nil {
			return store.StartRoundInput{}, err
		}
		input.Scope.Resources = []string{"opportunity:" + id, "company:" + opportunity.CompanyID}
		input.Scope.Operations = append(common, store.RoundCorrectOpportunity)
	case "evidence":
		evidence, err := h.database.Evidence(ctx, id)
		if err != nil {
			return store.StartRoundInput{}, err
		}
		opportunity, err := h.database.Opportunity(ctx, evidence.OpportunityID)
		if err != nil {
			return store.StartRoundInput{}, err
		}
		input.Scope.Resources = []string{"evidence:" + id, "opportunity:" + opportunity.ID, "company:" + opportunity.CompanyID}
		input.Scope.Operations = append(common, store.RoundCorrectEvidence)
	case "relationship":
		input.Scope.Resources = []string{"relationship:" + id}
		input.Scope.Operations = append(common, store.RoundRelationshipCorrect)
	case "application_pack":
		pack, err := h.database.ApplicationPack(ctx, id)
		if err != nil {
			return store.StartRoundInput{}, err
		}
		input.Intent = "Correct the one owner selected immutable application pack version."
		input.Scope.Resources = []string{"opportunity:" + pack.OpportunityID}
		input.Scope.Operations = append(common, store.RoundPrepareApplicationPack)
		input.Limits = store.RoundAllowance{Requests: 7, Items: 1, Tools: 3, Turns: 1}
	default:
		return store.StartRoundInput{}, store.ErrInvalid
	}
	return input, nil
}

func (h *Handler) processInput(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.ProcessInputRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	kind := string(body.TargetKind)
	if body.RequestKey == "" || len(body.RequestKey) > 180 || strings.TrimSpace(body.RequestKey) != body.RequestKey ||
		body.TargetId == "" || len(body.TargetId) > 300 || body.ExpectedRevision < 1 ||
		(body.ReplacePaused != nil && (body.ReplacePaused.RoundId == "" || body.ReplacePaused.ExpectedRevision < 1)) {
		failRound(w, store.ErrInvalid)
		return
	}
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	var ref, instructionID, ingestionID string
	if kind == "campaign" {
		if body.TargetId != "active" || body.Text != nil || body.SourceUrl == nil && body.OriginalText == nil {
			failRound(w, store.ErrInvalid)
			return
		}
		source := store.OwnerInputSourceInput{RequestKey: body.RequestKey, TargetID: body.TargetId, ExpectedRevision: body.ExpectedRevision,
			SourceURL: optionalString(body.SourceUrl), OriginalText: optionalString(body.OriginalText)}
		if body.ReplacePaused != nil {
			source.ReplacePausedID, source.ReplacePausedRev = body.ReplacePaused.RoundId, body.ReplacePaused.ExpectedRevision
		}
		ingestion, err := h.database.SubmitOwnerInputSource(r.Context(), actor, source)
		if err != nil {
			if errors.Is(err, store.ErrRoundIdempotencyConflict) || errors.Is(err, store.ErrInvalid) {
				failRound(w, err)
			} else {
				ingestionFailure(w, err)
			}
			return
		}
		ingestionID, ref = ingestion.ID, "ingestion:"+ingestion.ID
	} else {
		if body.Text == nil || strings.TrimSpace(*body.Text) == "" || body.SourceUrl != nil || body.OriginalText != nil {
			failRound(w, store.ErrInvalid)
			return
		}
		instruction, _, err := h.database.AddOwnerInstruction(r.Context(), actor, store.OwnerInstructionInput{RequestKey: body.RequestKey + ":instruction", TargetKind: kind, TargetID: body.TargetId, ExpectedRevision: body.ExpectedRevision, Text: *body.Text})
		if err != nil {
			failRound(w, err)
			return
		}
		instructionID, ref = instruction.ID, "instruction:"+instruction.ID
	}
	replacement := ""
	// Use decimal revision in the durable scope so a replay cannot silently
	// change which paused authority the owner chose to end.
	if body.ReplacePaused != nil {
		replacement = replacementRef(body.ReplacePaused.RoundId, body.ReplacePaused.ExpectedRevision)
	}
	previous, err := h.database.RoundByRequest(r.Context(), actor, body.RequestKey)
	if err == nil {
		if previous.Outcome != "process_input" || !roundHasInputRef(previous.Scope.InputRefs, ref) ||
			(kind == "campaign" && body.ExpectedRevision != previous.InitialProfileVersion) ||
			(replacement != "" && !roundHasInputRef(previous.Scope.InputRefs, replacement)) ||
			(replacement == "" && hasReplacementRef(previous.Scope.InputRefs)) {
			failRound(w, store.ErrRoundIdempotencyConflict)
			return
		}
		writeJSON(w, http.StatusOK, processInputResponse(previous, instructionID, ingestionID, body.ReplacePaused))
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		failRound(w, err)
		return
	}
	profile, err := h.database.CurrentPreferences(r.Context())
	if err != nil {
		failRound(w, err)
		return
	}
	if kind == "campaign" && body.ExpectedRevision != profile.Version {
		failRound(w, store.ErrConflict)
		return
	}
	input, err := h.processInputScope(r.Context(), kind, body.TargetId, profile.Version, ref, replacement)
	if err != nil {
		failRound(w, err)
		return
	}
	input.RequestKey = body.RequestKey
	var round store.Round
	var created bool
	if body.ReplacePaused == nil {
		round, created, err = h.rounds.Start(r.Context(), actor, input)
	} else {
		round, created, err = h.rounds.ReplacePaused(r.Context(), actor, body.ReplacePaused.RoundId, body.ReplacePaused.ExpectedRevision, input)
	}
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, processInputResponse(round, instructionID, ingestionID, body.ReplacePaused))
}

func replacementRef(id string, revision int64) string {
	return "replacement:" + id + ":" + strconv.FormatInt(revision, 10)
}
func roundHasInputRef(refs []string, expected string) bool {
	for _, ref := range refs {
		if ref == expected {
			return true
		}
	}
	return false
}
func hasReplacementRef(refs []string) bool {
	for _, ref := range refs {
		if strings.HasPrefix(ref, "replacement:") {
			return true
		}
	}
	return false
}
func replacementMatches(refs []string, replacement *generated.ReplacePausedRound) bool {
	if replacement == nil {
		return !hasReplacementRef(refs)
	}
	return roundHasInputRef(refs, replacementRef(replacement.RoundId, replacement.ExpectedRevision))
}
func processInputResponse(round store.Round, instructionID, ingestionID string, replacement *generated.ReplacePausedRound) any {
	response := struct {
		Round           roundResponse `json:"round"`
		InstructionID   string        `json:"instructionId,omitempty"`
		IngestionID     string        `json:"ingestionId,omitempty"`
		ReplacedRoundID string        `json:"replacedRoundId,omitempty"`
	}{Round: roundModel(round), InstructionID: instructionID, IngestionID: ingestionID}
	if replacement != nil {
		response.ReplacedRoundID = replacement.RoundId
	}
	return response
}
