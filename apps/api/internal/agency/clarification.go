package agency

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Owner clarification (K4). When grounded preparation needs a genuinely
// unknown personal fact, it asks the owner one focused question tied to
// the sourced vacancy requirement that demands it — never an employer
// question, never a guess. The owner's exact answer becomes verified
// job-scoped context, resolves the same clarification exactly once, and
// resumes only the affected work. New clarification text is never
// silently library-approved: nothing on this path writes saved answers.
//
// The durable writer is I-side (store table plus migration); this file
// pins the contract — identities, origins, resolve-once semantics and
// resume scope — that the store, API and M lanes implement against.

// Question origins. Employer questions come from captured application
// pages; owner clarifications come from preparation gaps. The two never
// mix: a clarification is never presented as an employer question and
// never answered from the reusable library.
const (
	QuestionOriginEmployer           = "employer"
	QuestionOriginOwnerClarification = "owner_clarification"
)

// Clarification statuses.
const (
	ClarificationOpen     = "open"
	ClarificationAnswered = "answered"
)

const (
	clarificationMaxPrompt = 2000
	clarificationMaxAnswer = 2000
	clarificationMaxWork   = 50
)

// ClarificationRequirement ties the question to the sourced vacancy
// requirement that demands the fact: the verbatim statement plus the
// capture span it was verified against.
type ClarificationRequirement struct {
	Statement string `json:"statement"`
	CaptureID string `json:"captureId"`
	SpanStart int    `json:"spanStart"`
	SpanEnd   int    `json:"spanEnd"`
}

// ClarificationWorkRef names one affected work item the answer resumes,
// for example {"artifact", <artifact id>} or {"answer", <question id>}.
type ClarificationWorkRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Clarification is one saved owner question with its exact answer.
type Clarification struct {
	ID            string                   `json:"id"`
	OpportunityID string                   `json:"opportunityId"`
	CheckID       string                   `json:"checkId"`
	Origin        string                   `json:"origin"`
	Requirement   ClarificationRequirement `json:"requirement"`
	Prompt        string                   `json:"prompt"`
	AffectedWork  []ClarificationWorkRef   `json:"affectedWork"`
	Status        string                   `json:"status"`
	Answer        string                   `json:"answer,omitempty"`
	AnsweredAt    string                   `json:"answeredAt,omitempty"`
	AnsweredBy    store.Actor              `json:"answeredBy,omitempty"`
	CreatedAt     string                   `json:"createdAt"`
}

// ClarificationOpenInput carries one new owner question. RequestKey makes the
// open idempotent: replays return the same clarification while a reused
// key with different input conflicts.
type ClarificationOpenInput struct {
	RequestKey   string
	CheckID      string
	Requirement  ClarificationRequirement
	Prompt       string
	AffectedWork []ClarificationWorkRef
}

// ClarificationAnswer carries the owner's exact answer text.
type ClarificationAnswer struct {
	RequestKey string
	Text       string
}

// ClarificationStore persists clarifications. The production
// implementation is I-side; tests supply the memory fake.
type ClarificationStore interface {
	OpenClarification(ctx context.Context, actor store.Actor, opportunityID string, input ClarificationOpenInput) (Clarification, bool, error)
	AnswerClarification(ctx context.Context, actor store.Actor, id string, input ClarificationAnswer) (Clarification, error)
	GetClarification(ctx context.Context, id string) (Clarification, error)
	ListClarifications(ctx context.Context, opportunityID string) ([]Clarification, error)
}

// Clarification hold/conflict reasons.
const (
	ClarifyInvalidOpen     = "invalid_clarification"
	ClarifyInvalidAnswer   = "invalid_answer"
	ClarifyAlreadyAnswered = "already_answered"
)

// ClarificationError is a typed clarification failure.
type ClarificationError struct {
	Reason string
	ID     string
	Err    error
}

func (e *ClarificationError) Error() string {
	if e.ID != "" {
		return fmt.Sprintf("agency: clarification %s on %s: %s", e.Reason, e.ID, e.Err)
	}
	return fmt.Sprintf("agency: clarification %s: %s", e.Reason, e.Err)
}

func (e *ClarificationError) Unwrap() error { return e.Err }

// OpenClarification validates and opens one owner question. The
// requirement must be sourced, the prompt focused, and at least one
// affected work item named so the answer resumes only dependent work.
func OpenClarification(ctx context.Context, db ClarificationStore, actor store.Actor, opportunityID string, input ClarificationOpenInput) (Clarification, bool, error) {
	if db == nil || opportunityID == "" {
		return Clarification{}, false, &ClarificationError{Reason: ClarifyInvalidOpen,
			Err: fmt.Errorf("clarification needs a store and an opportunity")}
	}
	if err := validClarificationOpen(input); err != nil {
		return Clarification{}, false, &ClarificationError{Reason: ClarifyInvalidOpen, Err: err}
	}
	return db.OpenClarification(ctx, actor, opportunityID, input)
}

func validClarificationOpen(input ClarificationOpenInput) error {
	if strings.TrimSpace(input.RequestKey) == "" || input.CheckID == "" {
		return fmt.Errorf("open needs a request key and the current check id")
	}
	requirement := input.Requirement
	if strings.TrimSpace(requirement.Statement) == "" || len(requirement.Statement) > 2000 ||
		!utf8.ValidString(requirement.Statement) || requirement.CaptureID == "" ||
		requirement.SpanStart < 0 || requirement.SpanEnd <= requirement.SpanStart ||
		requirement.SpanEnd-requirement.SpanStart > 2000 {
		return fmt.Errorf("open needs the sourced requirement with its capture span")
	}
	if strings.TrimSpace(input.Prompt) == "" || len(input.Prompt) > clarificationMaxPrompt || !utf8.ValidString(input.Prompt) {
		return fmt.Errorf("open needs a focused owner-facing prompt")
	}
	if len(input.AffectedWork) == 0 || len(input.AffectedWork) > clarificationMaxWork {
		return fmt.Errorf("open needs the affected work, 1..%d items", clarificationMaxWork)
	}
	seen := map[ClarificationWorkRef]bool{}
	for _, ref := range input.AffectedWork {
		if ref.Kind == "" || ref.ID == "" || len(ref.Kind) > 64 || len(ref.ID) > 128 || seen[ref] {
			return fmt.Errorf("affected work needs distinct kind/id refs")
		}
		seen[ref] = true
	}
	return nil
}

// AnswerClarification saves the owner's exact answer text verbatim. The
// answer resolves the clarification exactly once: a second answer
// conflicts instead of forking a second truth. Empty text resolves
// nothing and fails validation.
func AnswerClarification(ctx context.Context, db ClarificationStore, actor store.Actor, id string, input ClarificationAnswer) (Clarification, error) {
	if db == nil || id == "" {
		return Clarification{}, &ClarificationError{Reason: ClarifyInvalidAnswer, ID: id,
			Err: fmt.Errorf("answer needs a store and a clarification id")}
	}
	if strings.TrimSpace(input.RequestKey) == "" {
		return Clarification{}, &ClarificationError{Reason: ClarifyInvalidAnswer, ID: id,
			Err: fmt.Errorf("answer needs a request key")}
	}
	if input.Text == "" || len(input.Text) > clarificationMaxAnswer || !utf8.ValidString(input.Text) {
		return Clarification{}, &ClarificationError{Reason: ClarifyInvalidAnswer, ID: id,
			Err: fmt.Errorf("answer needs the owner's exact non-empty text")}
	}
	return db.AnswerClarification(ctx, actor, id, input)
}

// VerifiedJobContext returns the answered clarifications for one
// opportunity as verified job-scoped context for grounded preparation.
// Open questions are excluded: only saved owner answers count as
// verified facts.
func VerifiedJobContext(ctx context.Context, db ClarificationStore, opportunityID string) ([]Clarification, error) {
	if db == nil || opportunityID == "" {
		return nil, &ClarificationError{Reason: ClarifyInvalidOpen,
			Err: fmt.Errorf("job context needs a store and an opportunity")}
	}
	all, err := db.ListClarifications(ctx, opportunityID)
	if err != nil {
		return nil, err
	}
	verified := []Clarification{}
	for _, item := range all {
		if item.Status == ClarificationAnswered && item.OpportunityID == opportunityID {
			verified = append(verified, item)
		}
	}
	sort.Slice(verified, func(i, j int) bool { return verified[i].ID < verified[j].ID })
	return verified, nil
}

// ResumeScope returns the distinct affected work of one answered
// clarification so M resumes only dependent items. Open clarifications
// resume nothing.
func ResumeScope(item Clarification) []ClarificationWorkRef {
	if item.Status != ClarificationAnswered {
		return nil
	}
	seen := map[ClarificationWorkRef]bool{}
	out := []ClarificationWorkRef{}
	for _, ref := range item.AffectedWork {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}
