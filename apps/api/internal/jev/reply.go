package jev

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

const replyIntentQuestionID = "reply_intent"
const replyIntentUnresolved = "__unresolved__"

// ReplyIntentTaxonomy is the fixed supported intent set for one inbound
// employer or recruiter message. It is routine domain design, not a Jev
// consultation result: each label names an observable message kind, and the
// classifier abstains when the supplied thread cannot support a choice.
var ReplyIntentTaxonomy = []string{
	"interview_invitation",
	"scheduling_exchange",
	"information_request",
	"offer_terms",
	"rejection",
	"referral_introduction",
	"follow_up_nudge",
	"not_actionable",
}

type ReplyIntentContext struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
	Body     string `json:"body"`
}

type ReplyIntentEvidence struct {
	ID             string `json:"id"`
	SourceID       string `json:"source_id"`
	SourceRevision string `json:"source_revision"`
	SourceKind     string `json:"source_kind"`
	SourceSHA256   string `json:"source_sha256"`
	Excerpt        string `json:"excerpt"`
}

type ReplyIntentCandidate struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type ReplyIntentInput struct {
	ThreadID          string                 `json:"thread_id"`
	Context           []ReplyIntentContext   `json:"context"`
	Evidence          []ReplyIntentEvidence  `json:"evidence"`
	Candidates        []ReplyIntentCandidate `json:"candidates"`
	MaxReportedTokens int64                  `json:"max_reported_tokens"`
}

type ReplyIntentDisposition string

const (
	ReplyIntentSelected   ReplyIntentDisposition = "selected"
	ReplyIntentUnresolved ReplyIntentDisposition = "unresolved"
)

type ReplyIntentResult struct {
	Disposition     ReplyIntentDisposition `json:"disposition"`
	SelectedID      string                 `json:"selected_id,omitempty"`
	InputSHA256     string                 `json:"input_sha256"`
	RequestSnapshot json.RawMessage        `json:"request_snapshot"`
	ProviderResult  Result                 `json:"provider_result"`
}

// SelectReplyIntent classifies only supplied, evidence-linked intents. The
// caller must reserve and charge commissioned Jev allowance before passing a
// real evaluator, then persist the request/response and recheck thread state.
func SelectReplyIntent(ctx context.Context, evaluator Evaluator, input ReplyIntentInput) (ReplyIntentResult, error) {
	canonical, err := canonicalReplyIntent(input)
	if err != nil {
		return ReplyIntentResult{}, err
	}
	if evaluator == nil {
		return ReplyIntentResult{}, &Error{Kind: ErrInvalidConfig}
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return ReplyIntentResult{}, &Error{Kind: ErrInvalidRequest}
	}
	digest := sha256.Sum256(encoded)
	request := replyIntentRequest(canonical)
	snapshot, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{request.State, request.Questions})
	if err != nil {
		return ReplyIntentResult{}, &Error{Kind: ErrInvalidRequest}
	}
	provider, err := evaluator.Evaluate(ctx, request)
	if err != nil {
		return ReplyIntentResult{}, err
	}
	if !validScreeningResult(provider, request.Questions) {
		return ReplyIntentResult{}, &Error{Kind: ErrInvalidResponse}
	}
	if exceedsScreeningBudget(provider.Usage, canonical.MaxReportedTokens) {
		return ReplyIntentResult{}, &Error{Kind: ErrBudgetExceeded}
	}
	selected := provider.Answers[replyIntentQuestionID].Choice.Choice
	result := ReplyIntentResult{Disposition: ReplyIntentSelected, SelectedID: selected,
		InputSHA256: hex.EncodeToString(digest[:]), RequestSnapshot: snapshot, ProviderResult: provider}
	if selected == replyIntentUnresolved {
		result.Disposition, result.SelectedID = ReplyIntentUnresolved, ""
	}
	return result, nil
}

// RecoverCapturedReplyIntent derives the decision from the exact recorded
// request and response. It performs no provider operation.
func RecoverCapturedReplyIntent(input ReplyIntentInput, logical, raw []byte, requestedModel string) (ReplyIntentResult, error) {
	canonical, err := canonicalReplyIntent(input)
	if err != nil {
		return ReplyIntentResult{}, err
	}
	request := replyIntentRequest(canonical)
	expected, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{request.State, request.Questions})
	if err != nil || !bytes.Equal(expected, logical) {
		return ReplyIntentResult{}, &Error{Kind: ErrInvalidResponse}
	}
	parsed, err := parseResponse(raw, request.Questions, requestedModel)
	if err != nil || !validScreeningResult(parsed, request.Questions) || exceedsScreeningBudget(parsed.Usage, canonical.MaxReportedTokens) {
		return ReplyIntentResult{}, &Error{Kind: ErrInvalidResponse}
	}
	encoded, _ := json.Marshal(canonical)
	digest := sha256.Sum256(encoded)
	selected := parsed.Answers[replyIntentQuestionID].Choice.Choice
	result := ReplyIntentResult{Disposition: ReplyIntentSelected, SelectedID: selected,
		InputSHA256: hex.EncodeToString(digest[:]), RequestSnapshot: expected, ProviderResult: parsed}
	if selected == replyIntentUnresolved {
		result.Disposition, result.SelectedID = ReplyIntentUnresolved, ""
	}
	return result, nil
}

func replyIntentRequest(canonical ReplyIntentInput) Request {
	criteria := make(map[string]string, len(canonical.Candidates)+1)
	for _, candidate := range canonical.Candidates {
		criteria[candidate.ID] = candidate.Description + " Grounding evidence IDs are supplied in state; select only if those excerpts support this intent for the latest inbound message."
	}
	criteria[replyIntentUnresolved] = "The supplied thread excerpts are too sparse, conflicting, or weakly grounded to choose a useful intent."
	return Request{State: canonical, Questions: map[string]Question{replyIntentQuestionID: Choice(
		"Classify the latest inbound employer or recruiter message using the complete bounded thread context and cited excerpts. Attribute conflicting claims to their exact message; never merge speakers. State unknown dates, parties, or terms as unknown. Do not infer a hiring decision, invent a sender fact, or approve a reply. Choose __unresolved__ when evidence does not justify an intent. This choice does not send, book, or change records.", criteria)}}
}

func canonicalReplyIntent(input ReplyIntentInput) (ReplyIntentInput, error) {
	if !boundedOrganisationText(input.ThreadID, 100) || len(input.Context) < 1 || len(input.Context) > 12 ||
		len(input.Evidence) < 1 || len(input.Evidence) > 32 ||
		len(input.Candidates) < 1 || len(input.Candidates) > 8 || input.MaxReportedTokens < 1 || input.MaxReportedTokens > 100000 {
		return ReplyIntentInput{}, &Error{Kind: ErrInvalidRequest}
	}
	out := input
	out.Context = append([]ReplyIntentContext(nil), input.Context...)
	out.Evidence = append([]ReplyIntentEvidence(nil), input.Evidence...)
	out.Candidates = append([]ReplyIntentCandidate(nil), input.Candidates...)
	taxonomy := map[string]bool{}
	for _, id := range ReplyIntentTaxonomy {
		taxonomy[id] = true
	}
	contextIDs := make(map[string]bool, len(out.Context))
	contextBytes := 0
	for _, item := range out.Context {
		if !boundedOrganisationText(item.ID, 100) || contextIDs[item.ID] || !boundedOrganisationText(item.Revision, 100) ||
			(item.Kind != "inbound" && item.Kind != "outbound" && item.Kind != "subject") ||
			!boundedExactExcerpt(item.Body, 30000) || len(item.SHA256) != 64 {
			return ReplyIntentInput{}, &Error{Kind: ErrInvalidRequest}
		}
		sum := sha256.Sum256([]byte(item.Body))
		if item.SHA256 != hex.EncodeToString(sum[:]) {
			return ReplyIntentInput{}, &Error{Kind: ErrInvalidRequest}
		}
		contextBytes += len(item.Body)
		if contextBytes > 30000 {
			return ReplyIntentInput{}, &Error{Kind: ErrRequestTooLarge}
		}
		contextIDs[item.ID] = true
	}
	evidenceIDs := make(map[string]bool, len(out.Evidence))
	for _, evidence := range out.Evidence {
		if !boundedOrganisationText(evidence.ID, 100) || evidenceIDs[evidence.ID] ||
			!boundedOrganisationText(evidence.SourceID, 100) || !boundedOrganisationText(evidence.SourceRevision, 100) ||
			!boundedOrganisationText(evidence.SourceKind, 50) || len(evidence.SourceSHA256) != 64 ||
			!boundedExactExcerpt(evidence.Excerpt, 2000) {
			return ReplyIntentInput{}, &Error{Kind: ErrInvalidRequest}
		}
		if _, err := hex.DecodeString(evidence.SourceSHA256); err != nil {
			return ReplyIntentInput{}, &Error{Kind: ErrInvalidRequest}
		}
		evidenceIDs[evidence.ID] = true
	}
	candidateIDs := make(map[string]bool, len(out.Candidates))
	for i, candidate := range out.Candidates {
		if !taxonomy[candidate.ID] || candidateIDs[candidate.ID] ||
			!boundedOrganisationText(candidate.Description, 1000) || len(candidate.EvidenceIDs) < 1 || len(candidate.EvidenceIDs) > 8 {
			return ReplyIntentInput{}, &Error{Kind: ErrInvalidRequest}
		}
		candidateIDs[candidate.ID] = true
		out.Candidates[i].EvidenceIDs = append([]string(nil), candidate.EvidenceIDs...)
		seen := map[string]bool{}
		for _, id := range candidate.EvidenceIDs {
			if !evidenceIDs[id] || seen[id] {
				return ReplyIntentInput{}, &Error{Kind: ErrInvalidRequest}
			}
			seen[id] = true
		}
		sort.Strings(out.Candidates[i].EvidenceIDs)
	}
	sort.Slice(out.Context, func(i, j int) bool { return out.Context[i].ID < out.Context[j].ID })
	sort.Slice(out.Evidence, func(i, j int) bool { return out.Evidence[i].ID < out.Evidence[j].ID })
	sort.Slice(out.Candidates, func(i, j int) bool { return out.Candidates[i].ID < out.Candidates[j].ID })
	return out, nil
}
