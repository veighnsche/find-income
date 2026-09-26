// Package materialprep runs grounded preparation for one selected role over
// the single canonical route-artifact set (opportunity_artifacts): it
// verifies the pinned check and saved answers, drafts required-but-held
// artifact types in one bounded Muse Standard turn over verified facts
// only, and commits each validated draft as a new artifact version.
//
// The service performs no research, fetch, or send of any kind. It holds
// no capability for employer contact: its only outbound dependencies are
// the injected Career and Artifacts collaborators plus the store. Pin
// verification (check, answers, workflow, opportunity) always precedes any
// Standard spend, and zero held types means zero Standard call. The
// optional Captures reader opens only already-stored immutable vacancy
// capture bytes for the role description; it performs no retrieval.
package materialprep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/agency"
	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ErrUnavailable reports a missing preparation dependency (store, career
// sources, or artifact drafter). The HTTP layer maps it to 503.
var ErrUnavailable = errors.New("material preparation unavailable")

const (
	// maxSavedAnswers caps the reusable-answer context handed to one draft
	// turn. The library is paged in store order until the cap.
	maxSavedAnswers = 500
	// maxDraftCareerRunes and maxDraftSavedRunes bound the verified-fact
	// context per turn. Truncation is marked in-prompt; excerpts still
	// validate against the full pinned bodies, so a truncated fact can
	// only be omitted (held), never mis-cited.
	maxDraftCareerRunes = 12000
	maxDraftSavedRunes  = 20000
)

// CareerLoader returns the pinned approved career sources plus the Typst
// template. Production wires applicationpacks.LoadApprovedCareerSources.
// The artifact path consumes the sources; the template travels with the
// loader signature and is ignored.
type CareerLoader func() ([]applicationpacks.Source, []byte, error)

// AnsweredFact is one E3 answered value supplied as verified draft context.
type AnsweredFact struct {
	QuestionID    string
	Question      string
	Text          string
	AnswerVersion int64
}

// SavedAnswerFact is one owner-approved library answer (current version)
// supplied as verified draft context.
type SavedAnswerFact struct {
	AnswerID    string
	Version     int64
	TextSHA256  string
	Text        string
	ScopeTags   []string
	ContextNote string
}

// Service prepares grounded application materials. The zero value is
// unusable; the coordinator wires Store plus the collaborators.
type Service struct {
	Store     *store.Store
	Career    CareerLoader
	Artifacts ArtifactDrafter
	// Captures opens already-stored vacancy capture bytes when the saved
	// opportunity carries no text (discovery-saved roles). Nil keeps the
	// previous behavior: undescribed roles resolve an empty description.
	Captures researchcontract.CaptureReader
	// Clarifications persists owner questions for genuinely unknown
	// personal facts (K4). Production wires
	// &agency.StoreClarifications{DB: store}. Nil disables
	// clarification opens and resume tracking: missing facts hold
	// with their reason and the owner sees no question.
	Clarifications agency.ClarificationStore
	// Rewrites runs explicit targeted rewrites of chosen current
	// items. Production wires the same StandardDrafter as Artifacts;
	// nil reports ErrUnavailable.
	Rewrites ArtifactRewriter
}

func validSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func validRequestKey(key string) bool {
	return key != "" && len(key) <= 200 && strings.TrimSpace(key) == key
}

// truncateRunes shortens text to at most max runes without splitting UTF-8.
func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	count := 0
	for i := range text {
		if count == max {
			return text[:i]
		}
		count++
	}
	return text
}

// draftSources indexes the pinned approved career sources by id.
func draftSources(sources []applicationpacks.Source) (map[string]applicationpacks.Source, error) {
	byID := make(map[string]applicationpacks.Source, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.ID) == "" || !source.Approved {
			return nil, fmt.Errorf("%w: draft source", store.ErrInvalid)
		}
		if _, dup := byID[source.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate draft source", store.ErrInvalid)
		}
		byID[source.ID] = source
	}
	return byID, nil
}

// draftPayloadText joins the collected messages and extracts the JSON
// payload: either the raw object or a single fenced block. Anything else is
// rejected so prose around the payload fails loudly.
func draftPayloadText(messages []string) (string, error) {
	trimmed := strings.TrimSpace(strings.Join(messages, "\n"))
	if trimmed == "" {
		return "", fmt.Errorf("empty draft response")
	}
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	_, rest, _ := strings.Cut(trimmed[3:], "\n")
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", fmt.Errorf("unclosed draft code fence")
	}
	return strings.TrimSpace(rest[:end]), nil
}

// pinned groups the verified reads one prepare is bound to.
type pinned struct {
	check       store.CheckView
	values      map[string]store.QuestionAnswerValue
	workflow    store.RoleWorkflow
	opportunity store.Opportunity
	company     store.Company
	profile     store.Preferences
	description string
}

// verifyPins loads the pinned check, saved answers, workflow, opportunity,
// company, and profile, failing with ErrNotFound/ErrConflict before any
// Standard spend. The store re-verifies every pin at commit; these reads
// only fail fast.
func (s *Service) verifyPins(ctx context.Context, opportunityID, expectedCheckID, expectedQuestionSet string, expectedWorkflowRevision int64) (pinned, error) {
	var out pinned
	if opportunityID == "" || expectedCheckID == "" || !validSHA(expectedQuestionSet) || expectedWorkflowRevision < 0 {
		return out, store.ErrInvalid
	}
	status, err := s.Store.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if status.Status == store.CheckOverallNotChecked || status.Check == nil {
		return out, store.ErrNotFound
	}
	if status.Status != store.CheckStatusChecked {
		return out, store.ErrConflict
	}
	check := *status.Check
	if check.ID != expectedCheckID || check.QuestionSetSHA256 != expectedQuestionSet {
		return out, store.ErrConflict
	}
	answers, err := s.Store.CurrentQuestionAnswers(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if answers.CheckID != check.ID || answers.QuestionSetSHA256 != check.QuestionSetSHA256 {
		return out, store.ErrConflict
	}
	workflow, err := s.Store.RoleWorkflow(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if workflow.Revision != expectedWorkflowRevision ||
		(workflow.Stage != store.RoleStageAnswered && workflow.Stage != store.RoleStagePreparing &&
			workflow.Stage != store.RoleStagePrepared) {
		return out, store.ErrConflict
	}
	opportunity, err := s.Store.Opportunity(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if opportunity.ArchivedAt != "" {
		return out, store.ErrNotFound
	}
	if check.OpportunityRevision != opportunity.Revision {
		return out, store.ErrConflict
	}
	company, err := s.Store.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return out, err
	}
	profile, err := s.Store.CurrentPreferences(ctx)
	if err != nil {
		return out, err
	}
	description, err := s.roleDescription(ctx, opportunity, check.Vacancy.CaptureIDs)
	if err != nil {
		return out, err
	}
	out = pinned{check: check, values: make(map[string]store.QuestionAnswerValue, len(answers.Values)),
		workflow: workflow, opportunity: opportunity, company: company, profile: profile,
		description: description}
	for _, value := range answers.Values {
		out.values[value.QuestionID] = value
	}
	return out, nil
}

// savedAnswerContext pages the owner-approved library (current versions
// only) as draft context. It runs only when a draft turn is needed.
func (s *Service) savedAnswerContext(ctx context.Context) ([]SavedAnswerFact, error) {
	out := make([]SavedAnswerFact, 0)
	cursor := ""
	for {
		page, err := s.Store.ListSavedAnswers(ctx, store.SavedAnswerListOptions{Cursor: cursor, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, answer := range page.Items {
			for _, version := range answer.Versions {
				if version.Version != answer.CurrentVersion {
					continue
				}
				out = append(out, SavedAnswerFact{AnswerID: answer.ID, Version: version.Version,
					TextSHA256: version.TextSHA256, Text: version.Text,
					ScopeTags: append([]string(nil), answer.ScopeTags...), ContextNote: answer.ContextNote})
				break
			}
			if len(out) >= maxSavedAnswers {
				return out, nil
			}
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// maxRoleDescriptionBytes bounds the resolved role description. Longer
// vacancy bytes fail closed instead of truncating the role record.
const maxRoleDescriptionBytes = 30000

// roleDescription resolves the role description: the saved opportunity
// text when present, else the check's first vacancy capture bytes verbatim
// (discovery-saved roles carry no opportunity text; the capture is the
// same verified bytes the check read). A nil Captures reader or no
// capture ids leaves the role undescribed.
func (s *Service) roleDescription(ctx context.Context, opportunity store.Opportunity, captureIDs []string) (string, error) {
	if strings.TrimSpace(opportunity.OriginalText) != "" {
		return opportunity.OriginalText, nil
	}
	if s == nil || s.Captures == nil || len(captureIDs) == 0 {
		return "", nil
	}
	_, reader, err := s.Captures.OpenCapture(ctx, captureIDs[0])
	if err != nil {
		return "", fmt.Errorf("materialprep: open vacancy capture: %w", err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, maxRoleDescriptionBytes+1))
	if err != nil {
		return "", fmt.Errorf("materialprep: read vacancy capture: %w", err)
	}
	if len(raw) > maxRoleDescriptionBytes {
		return "", fmt.Errorf("%w: vacancy capture exceeds the role description bound", store.ErrInvalid)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", nil
	}
	return string(raw), nil
}
