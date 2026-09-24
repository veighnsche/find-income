package jev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// DecisionKind selects the meaning of one independent Choice request. A caller
// must make a later request if its candidates depend on an earlier answer.
type DecisionKind string

const (
	DecisionNextOutcome DecisionKind = "next_outcome"
	decisionUnresolved  string       = "__unresolved__"
	decisionQuestionID  string       = "selected_candidate"
)

type DecisionCapability struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type DecisionSource struct {
	ID             string `json:"id"`
	SourceRevision string `json:"source_revision"`
	SourceKind     string `json:"source_kind"`
	URL            string `json:"url,omitempty"`
	ObservedAt     string `json:"observed_at,omitempty"`
	Excerpt        string `json:"excerpt"`
}

type PreviousDecisionOutcome struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type DecisionAllowance struct {
	Operation string `json:"operation"`
	Remaining int64  `json:"remaining"`
}

type DecisionCandidate struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	Scope        string   `json:"scope"`
	CapabilityID string   `json:"capability_id"`
	SourceIDs    []string `json:"source_ids,omitempty"`
}

type DecisionInput struct {
	Kind               DecisionKind              `json:"kind"`
	CampaignIntent     string                    `json:"campaign_intent"`
	Capabilities       []DecisionCapability      `json:"capabilities"`
	Sources            []DecisionSource          `json:"sources"`
	PreviousOutcomes   []PreviousDecisionOutcome `json:"previous_outcomes"`
	RemainingAllowance []DecisionAllowance       `json:"remaining_allowance"`
	Candidates         []DecisionCandidate       `json:"candidates"`
	MaxReportedTokens  int64                     `json:"max_reported_tokens"`
}

type DecisionDisposition string

const (
	DecisionSelected     DecisionDisposition = "selected"
	DecisionUnresolved   DecisionDisposition = "unresolved"
	DecisionNoCandidates DecisionDisposition = "no_candidates"
)

// DecisionResult retains the typed provider output and logical request JSON
// (state and questions) for caller persistence. ProviderResult is empty when
// no candidates were supplied.
// ProviderResult.RawResponse contains the exact validated provider bytes;
// persist those bytes separately when byte identity is required.
type DecisionResult struct {
	Disposition     DecisionDisposition `json:"disposition"`
	SelectedID      string              `json:"selected_id,omitempty"`
	InputSHA256     string              `json:"input_sha256"`
	RequestSnapshot json.RawMessage     `json:"request_snapshot,omitempty"`
	ProviderResult  Result              `json:"provider_result"`
}

// SelectDecision only selects among caller-supplied, implementable candidates.
// It does not discover candidates, dispatch work or infer capability from text.
func SelectDecision(ctx context.Context, evaluator Evaluator, input DecisionInput) (DecisionResult, error) {
	canonical, err := canonicalDecisionInput(input)
	if err != nil {
		return DecisionResult{}, err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return DecisionResult{}, &Error{Kind: ErrInvalidRequest}
	}
	digest := sha256.Sum256(encoded)
	result := DecisionResult{Disposition: DecisionNoCandidates, InputSHA256: hex.EncodeToString(digest[:])}
	if len(canonical.Candidates) == 0 {
		return result, nil
	}
	if evaluator == nil {
		return DecisionResult{}, &Error{Kind: ErrInvalidConfig}
	}
	request := decisionRequest(canonical)
	requestBytes, err := marshalDecisionLogicalRequest(request)
	if err != nil {
		return DecisionResult{}, err
	}
	provider, err := evaluator.Evaluate(ctx, request)
	if err != nil {
		return DecisionResult{}, err
	}
	if !validScreeningResult(provider, request.Questions) {
		return DecisionResult{}, &Error{Kind: ErrInvalidResponse}
	}
	if exceedsScreeningBudget(provider.Usage, canonical.MaxReportedTokens) {
		return DecisionResult{}, &Error{Kind: ErrBudgetExceeded}
	}
	result.RequestSnapshot = requestBytes
	result.ProviderResult = provider
	choice := provider.Answers[decisionQuestionID].Choice.Choice
	if choice == decisionUnresolved {
		result.Disposition = DecisionUnresolved
		return result, nil
	}
	result.Disposition = DecisionSelected
	result.SelectedID = choice
	return result, nil
}

// DecisionLogicalRequest produces the exact state/questions JSON that a
// charged decision would send, before model transport wrapping. Callers may
// enforce a local context-size bound before reserving a provider attempt.
func DecisionLogicalRequest(input DecisionInput) ([]byte, error) {
	canonical, err := canonicalDecisionInput(input)
	if err != nil {
		return nil, err
	}
	if len(canonical.Candidates) == 0 {
		return nil, nil
	}
	return marshalDecisionLogicalRequest(decisionRequest(canonical))
}

// RecoverCapturedDecision replays validation of an already captured Choice.
// The evaluator reads only supplied bytes and never contacts a provider.
func RecoverCapturedDecision(input DecisionInput, logical, response []byte, requestedModel string) (DecisionResult, error) {
	if len(logical) == 0 || len(response) == 0 || requestedModel == "" {
		return DecisionResult{}, &Error{Kind: ErrInvalidResponse}
	}
	return SelectDecision(context.Background(), capturedOfferEvaluator{logical: logical, response: response, model: requestedModel}, input)
}

func decisionRequest(canonical DecisionInput) Request {
	options := make(map[string]string, len(canonical.Candidates)+1)
	for _, candidate := range canonical.Candidates {
		options[candidate.ID] = candidate.Description + " Permitted scope: " + candidate.Scope + ". Requires supplied capability " + candidate.CapabilityID + "."
	}
	options[decisionUnresolved] = "The supplied candidate set or context is insufficient, materially conflicting, or does not contain a useful supported choice. Abstain without inventing another candidate or claiming that no other possibility exists."
	instructions := "Choose the most useful supplied next outcome for the commissioned campaign using current evidence, previous outcomes, supported capabilities and remaining allowance. Select only an implemented candidate; do not invent an unavailable action, infer an employer fact, approve an external action or dispatch work. Select __unresolved__ when the supplied set cannot support a useful decision."
	return Request{State: canonical, Questions: map[string]Question{decisionQuestionID: Choice(instructions, options)}}
}

func marshalDecisionLogicalRequest(request Request) ([]byte, error) {
	requestBytes, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{State: request.State, Questions: request.Questions})
	if err != nil {
		return nil, &Error{Kind: ErrInvalidRequest}
	}
	return requestBytes, nil
}

func canonicalDecisionInput(input DecisionInput) (DecisionInput, error) {
	if input.Kind != DecisionNextOutcome ||
		!boundedOrganisationText(input.CampaignIntent, 1500) || input.MaxReportedTokens < 1 || input.MaxReportedTokens > 1_000_000 ||
		len(input.Capabilities) > 16 || len(input.Sources) > 12 ||
		len(input.PreviousOutcomes) > 16 || len(input.RemainingAllowance) > 16 || len(input.Candidates) > 16 ||
		(len(input.Candidates) > 0 && (len(input.Capabilities) == 0 || len(input.RemainingAllowance) == 0)) {
		return DecisionInput{}, &Error{Kind: ErrInvalidRequest}
	}
	out := DecisionInput{Kind: input.Kind, CampaignIntent: input.CampaignIntent, MaxReportedTokens: input.MaxReportedTokens,
		Capabilities: append([]DecisionCapability(nil), input.Capabilities...), Sources: append([]DecisionSource(nil), input.Sources...),
		PreviousOutcomes: append([]PreviousDecisionOutcome(nil), input.PreviousOutcomes...), RemainingAllowance: append([]DecisionAllowance(nil), input.RemainingAllowance...),
		Candidates: append([]DecisionCandidate(nil), input.Candidates...)}
	capabilities := map[string]bool{}
	for _, item := range out.Capabilities {
		if !boundedOrganisationText(item.ID, 80) || capabilities[item.ID] || !boundedOrganisationText(item.Description, 500) {
			return DecisionInput{}, &Error{Kind: ErrInvalidRequest}
		}
		capabilities[item.ID] = true
	}
	sources := map[string]bool{}
	for _, item := range out.Sources {
		if !boundedOrganisationText(item.ID, 128) || sources[item.ID] || !boundedOrganisationText(item.SourceRevision, 128) || !boundedOrganisationText(item.SourceKind, 80) ||
			(item.URL != "" && !boundedOrganisationText(item.URL, 1000)) || (item.ObservedAt != "" && !boundedOrganisationText(item.ObservedAt, 80)) || !boundedOrganisationText(item.Excerpt, 2000) {
			return DecisionInput{}, &Error{Kind: ErrInvalidRequest}
		}
		sources[item.ID] = true
	}
	previous := map[string]bool{}
	for _, item := range out.PreviousOutcomes {
		if !boundedOrganisationText(item.ID, 128) || previous[item.ID] || !boundedOrganisationText(item.Description, 500) {
			return DecisionInput{}, &Error{Kind: ErrInvalidRequest}
		}
		previous[item.ID] = true
	}
	allowances := map[string]bool{}
	for _, item := range out.RemainingAllowance {
		if !boundedOrganisationText(item.Operation, 80) || allowances[item.Operation] || item.Remaining < 0 || item.Remaining > 1_000_000 {
			return DecisionInput{}, &Error{Kind: ErrInvalidRequest}
		}
		allowances[item.Operation] = true
	}
	candidates := map[string]bool{}
	for i, item := range out.Candidates {
		if !boundedOrganisationText(item.ID, 80) || item.ID == decisionUnresolved || candidates[item.ID] ||
			!boundedOrganisationText(item.Description, 1000) || !boundedOrganisationText(item.Scope, 500) || !capabilities[item.CapabilityID] || len(item.SourceIDs) > 12 {
			return DecisionInput{}, &Error{Kind: ErrInvalidRequest}
		}
		candidates[item.ID] = true
		seenRefs := map[string]bool{}
		out.Candidates[i].SourceIDs = append([]string(nil), item.SourceIDs...)
		for _, id := range item.SourceIDs {
			if !sources[id] || seenRefs[id] {
				return DecisionInput{}, &Error{Kind: ErrInvalidRequest}
			}
			seenRefs[id] = true
		}
		sort.Strings(out.Candidates[i].SourceIDs)
	}
	sort.Slice(out.Capabilities, func(i, j int) bool { return out.Capabilities[i].ID < out.Capabilities[j].ID })
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	sort.Slice(out.PreviousOutcomes, func(i, j int) bool { return out.PreviousOutcomes[i].ID < out.PreviousOutcomes[j].ID })
	sort.Slice(out.RemainingAllowance, func(i, j int) bool { return out.RemainingAllowance[i].Operation < out.RemainingAllowance[j].Operation })
	sort.Slice(out.Candidates, func(i, j int) bool { return out.Candidates[i].ID < out.Candidates[j].ID })
	return out, nil
}
