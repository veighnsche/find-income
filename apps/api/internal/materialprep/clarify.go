package materialprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/agency"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Owner-clarification hold and resume (M4/K4). When grounded
// preparation meets a genuinely unknown personal fact, it asks the
// owner one focused question tied to the sourced vacancy requirement
// that demands it, holds only the dependent items, and resumes only
// those items once the owner answers. All persistence flows through
// the agency contract; production wires agency.StoreClarifications.

// clarificationState reads one role's clarifications: the answered
// ones as verified turn context, the open ones as a held-by map from
// artifact type to clarification id, and the resume map from artifact
// type to the answered clarification ids covering it. A nil
// Clarifications store yields empty state without an error.
func (s *Service) clarificationState(ctx context.Context, opportunityID string) ([]ClarificationFact, map[string]string, map[string][]string, error) {
	clarified := []ClarificationFact{}
	openHolds := map[string]string{}
	resumed := map[string][]string{}
	if s == nil || s.Clarifications == nil {
		return clarified, openHolds, resumed, nil
	}
	items, err := s.Clarifications.ListClarifications(ctx, opportunityID)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, item := range items {
		switch item.Status {
		case agency.ClarificationAnswered:
			clarified = append(clarified, ClarificationFact{Prompt: item.Prompt, Text: item.Answer})
			for _, ref := range agency.ResumeScope(item) {
				if ref.Kind == ClarifyWorkArtifact {
					resumed[ref.ID] = append(resumed[ref.ID], item.ID)
				}
			}
		case agency.ClarificationOpen:
			for _, ref := range item.AffectedWork {
				if ref.Kind == ClarifyWorkArtifact {
					if _, seen := openHolds[ref.ID]; !seen {
						openHolds[ref.ID] = item.ID
					}
				}
			}
		}
	}
	return clarified, openHolds, resumed, nil
}

// confirmPins re-verifies the preparation pins after the Standard
// turn and before any commit: the check, question set, workflow and
// opportunity revisions must be exactly what the turn consumed, and
// every cited answer version must be unmoved. A concurrent input
// change fails with ErrConflict/ErrNotFound instead of committing a
// fresh-looking stale draft.
func (s *Service) confirmPins(ctx context.Context, opportunityID string, resolved pinned, cited []string) error {
	fresh, err := s.verifyPins(ctx, opportunityID, resolved.check.ID,
		resolved.check.QuestionSetSHA256, resolved.workflow.Revision)
	if err != nil {
		return err
	}
	for _, id := range cited {
		before, ok := resolved.values[id]
		after, still := fresh.values[id]
		if !ok || !still || after.Version != before.Version {
			return fmt.Errorf("%w: answer %q moved during preparation", store.ErrConflict, id)
		}
	}
	return nil
}

// clarifyMissingFact opens one focused owner question for a held
// target whose draft named an unsupported fact. It returns the
// clarification id and true when a question now covers the hold:
// either an already-open one or a newly opened one. It returns false
// without an error when no question applies: a nil store, a hold
// without a missing fact, an email target with no sourced span, or a
// failed open (the hold journal keeps the reason either way).
func (s *Service) clarifyMissingFact(ctx context.Context, actor store.Actor, opportunityID string, resolved pinned, openHolds map[string]string, hold ArtifactHold) (string, bool) {
	if s == nil || s.Clarifications == nil || hold.MissingFact == "" {
		return "", false
	}
	if id, ok := openHolds[hold.Type]; ok {
		return id, true
	}
	requirement, ok := clarificationRequirement(resolved.check, hold.Type)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(hold.Type + "\x00" + hold.MissingFact))
	key := "prepare-clarify:" + resolved.check.ID + ":" + hold.Type + ":" + hex.EncodeToString(sum[:])[:16]
	prompt := "The vacancy asks for " + strconv.Quote(requirement.label) + ". To draft your " +
		hold.Type + ", what should it say about this: " + strconv.Quote(hold.MissingFact)
	item, _, err := agency.OpenClarification(ctx, s.Clarifications, actor, opportunityID, agency.ClarificationOpenInput{
		RequestKey: key, CheckID: resolved.check.ID,
		Requirement: agency.ClarificationRequirement{Statement: requirement.statement,
			CaptureID: requirement.captureID, SpanStart: requirement.start, SpanEnd: requirement.end},
		Prompt:       truncateRunes(prompt, 2000),
		AffectedWork: []agency.ClarificationWorkRef{{Kind: ClarifyWorkArtifact, ID: hold.Type}},
	})
	if err != nil {
		return "", false
	}
	return item.ID, true
}

// clarificationSource is one sourced vacancy demand for a document.
type clarificationSource struct {
	label     string
	statement string
	captureID string
	start     int
	end       int
}

// clarificationRequirement ties one held document type to the sourced
// vacancy demand behind it: the requesting document, attachment
// question or requirement text with its capture span. Email targets
// carry no capture span on the verified route, so they never open a
// question and hold with their reason instead.
func clarificationRequirement(check store.CheckView, artifactType string) (clarificationSource, bool) {
	keywords := map[string][]string{
		store.ArtifactCV:          {"cv", "resume", "curriculum vitae"},
		store.ArtifactCoverLetter: {"cover letter", "covering letter", "motivation letter", "motivational letter", "motivation", "motivatie", "sollicitatiebrief"},
	}[artifactType]
	if keywords == nil {
		return clarificationSource{}, false
	}
	matches := func(text string) bool {
		lowered := strings.ToLower(text)
		for _, keyword := range keywords {
			if strings.Contains(lowered, keyword) {
				return true
			}
		}
		return false
	}
	for _, document := range check.RequestedDocuments {
		if !matches(document.Label) {
			continue
		}
		statement := document.SourceExcerpt
		if strings.TrimSpace(statement) == "" {
			statement = document.Label
		}
		if source, ok := spannedSource(document.Label, statement, document.SourceSpan); ok {
			return source, true
		}
	}
	for _, question := range check.Questions {
		if question.Kind != store.CheckQuestionAttachment || !matches(question.Text) {
			continue
		}
		statement := question.SourceExcerpt
		if strings.TrimSpace(statement) == "" {
			statement = question.Text
		}
		if source, ok := spannedSource(question.Text, statement, question.SourceSpan); ok {
			return source, true
		}
	}
	for _, requirement := range check.Requirements {
		if !matches(requirement.Statement) {
			continue
		}
		statement := requirement.SourceExcerpt
		if strings.TrimSpace(statement) == "" {
			statement = requirement.Statement
		}
		if source, ok := spannedSource(statement, statement, requirement.SourceSpan); ok {
			return source, true
		}
	}
	return clarificationSource{}, false
}

// spannedSource accepts one sourced text only when its capture span
// satisfies the agency contract: a capture id with a positive span.
func spannedSource(label, statement string, span store.CheckSourceSpan) (clarificationSource, bool) {
	if strings.TrimSpace(statement) == "" || span.CaptureID == "" || span.Start < 0 || span.End <= span.Start {
		return clarificationSource{}, false
	}
	return clarificationSource{label: label, statement: statement,
		captureID: span.CaptureID, start: span.Start, end: span.End}, true
}
