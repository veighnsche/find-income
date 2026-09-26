package store

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Artifact readiness set (A4). Requiredness is deterministic evidence
// matching over the verified check: requested documents, attachment
// questions, requirement statements and the route destination each cite
// their source. Three Jev consultations unanimously advised this
// evidence-keyword rule over a fixed kind table or deferring the judgment
// to the drafting turn; see docs/design-consultations/a4-artifact-readiness.
// Agreement is advisory: the gate tests below prove the behavior.
const (
	ArtifactStateReady       = "ready"
	ArtifactStateHeld        = "held"
	ArtifactStateNotRequired = "not_required"
	ArtifactStateUnresolved  = "unresolved"
)

// ArtifactReadinessTypes is the stable set order: stored types first,
// derived form values last.
var ArtifactReadinessTypes = []string{
	ArtifactCV, ArtifactCoverLetter, ArtifactEmailSubject, ArtifactEmailBody, ArtifactFormValues,
}

// ArtifactFormValue is one derived form-field value: the employer's actual
// question plus the owner answer text. Values derive at read time and are
// never stored; see CurrentQuestionAnswers.
type ArtifactFormValue struct {
	QuestionID   string `json:"questionId"`
	QuestionText string `json:"questionText"`
	Required     string `json:"required"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Text         string `json:"text"`
}

// ArtifactReadinessEntry is one type's readiness verdict. Current is set
// when a stored version exists; FormValues is set for form_values only.
// Basis cites the verified source behind a required verdict.
type ArtifactReadinessEntry struct {
	Type       string              `json:"type"`
	Required   bool                `json:"required"`
	State      string              `json:"state"`
	Reason     string              `json:"reason"`
	Basis      string              `json:"basis,omitempty"`
	Current    *ArtifactView       `json:"current,omitempty"`
	FormValues []ArtifactFormValue `json:"formValues,omitempty"`
}

// ArtifactReadinessSet is the whole per-role readiness read.
type ArtifactReadinessSet struct {
	OpportunityID string                   `json:"opportunityId"`
	CheckID       string                   `json:"checkId,omitempty"`
	CheckStatus   string                   `json:"checkStatus"`
	Entries       []ArtifactReadinessEntry `json:"entries"`
}

var artifactEmailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

var artifactCVKeywords = []string{"cv", "curriculum vitae", "resume", "resumé", "résumé"}

var artifactLetterKeywords = []string{
	"cover letter", "covering letter", "motivation letter", "motivational letter",
	"motivatiebrief", "sollicitatiebrief", "begeleidende brief", "sollicitatie brief",
}

func containsKeyword(haystack, phrase string) bool {
	text := strings.ToLower(haystack)
	needle := strings.ToLower(phrase)
	for start := 0; start+len(needle) <= len(text); {
		at := strings.Index(text[start:], needle)
		if at < 0 {
			return false
		}
		at += start
		before := at == 0 || !unicode.IsLetter(rune(text[at-1]))
		after := at+len(needle) == len(text) || !unicode.IsLetter(rune(text[at+len(needle)]))
		if before && after {
			return true
		}
		start = at + 1
	}
	return false
}

func matchesKeywords(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if containsKeyword(text, keyword) {
			return true
		}
	}
	return false
}

// documentBasis finds the first verified source naming a document: a
// requested-document label, an attachment question text, or a requirement
// statement. A bare "motivation"/"motivatie" label names a letter only when
// it is the whole label; inside longer prose only compound forms count.
func documentBasis(check *CheckView, keywords []string, letter bool) (string, bool) {
	for _, doc := range check.RequestedDocuments {
		if matchesKeywords(doc.Label, keywords) ||
			(letter && (strings.EqualFold(strings.TrimSpace(doc.Label), "motivation") ||
				strings.EqualFold(strings.TrimSpace(doc.Label), "motivatie"))) {
			return "requested document " + strconv.Quote(doc.Label), true
		}
	}
	for _, question := range check.Questions {
		if question.Kind != CheckQuestionAttachment {
			continue
		}
		if matchesKeywords(question.Text, keywords) {
			return "attachment question " + strconv.Quote(question.Text), true
		}
	}
	for _, requirement := range check.Requirements {
		if matchesKeywords(requirement.Statement, keywords) {
			return "requirement " + strconv.Quote(requirement.Statement), true
		}
	}
	return "", false
}

// ArtifactReadiness reads the per-type readiness set for one selected role.
// Unchecked, outdated, blocked or checking roles leave every type
// unresolved; only a checked role with a verified application route can
// mark types required, and every required verdict cites its basis.
func (s *Store) ArtifactReadiness(ctx context.Context, opportunityID string) (ArtifactReadinessSet, error) {
	if opportunityID == "" {
		return ArtifactReadinessSet{}, ErrInvalid
	}
	status, err := s.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		return ArtifactReadinessSet{}, err
	}
	stored := map[string]ArtifactView{}
	listed, err := s.ListOpportunityArtifacts(ctx, opportunityID)
	if err != nil {
		return ArtifactReadinessSet{}, err
	}
	for _, view := range listed {
		stored[view.Type] = view
	}
	set := ArtifactReadinessSet{OpportunityID: opportunityID, CheckStatus: status.Status, Entries: []ArtifactReadinessEntry{}}
	if status.Status != CheckOverallChecked || status.Check == nil {
		reason := "no verified check yet"
		switch status.Status {
		case CheckOverallChecking:
			reason = "check still running"
		case CheckOverallBlocked:
			reason = "check blocked"
		case CheckOverallOutdated:
			reason = "check outdated; recheck before preparing"
		}
		for _, artifactType := range ArtifactReadinessTypes {
			entry := ArtifactReadinessEntry{Type: artifactType, State: ArtifactStateUnresolved, Reason: reason}
			if current, ok := stored[artifactType]; ok {
				current := current
				entry.Current = &current
			}
			set.Entries = append(set.Entries, entry)
		}
		return set, nil
	}
	check := status.Check
	set.CheckID = check.ID
	route := check.Route
	blanket := func(state, reason string) {
		for _, artifactType := range ArtifactReadinessTypes {
			entry := ArtifactReadinessEntry{Type: artifactType, State: state, Reason: reason}
			if current, ok := stored[artifactType]; ok {
				current := current
				entry.Current = &current
			}
			set.Entries = append(set.Entries, entry)
		}
	}
	switch route.Judgment {
	case CheckRouteJudgmentUnresolved:
		blanket(ArtifactStateUnresolved, "application route unresolved")
		return set, nil
	case CheckRouteJudgmentOther:
		blanket(ArtifactStateNotRequired, "observed contact is not an application route")
		return set, nil
	}
	if route.Kind != CheckRouteDirect && route.Kind != CheckRouteReferral && route.Kind != CheckRouteRecruiter {
		blanket(ArtifactStateUnresolved, "application route unresolved")
		return set, nil
	}
	answers := map[string]QuestionAnswerValue{}
	if len(check.Questions) > 0 {
		list, err := s.CurrentQuestionAnswers(ctx, opportunityID)
		if err != nil {
			return ArtifactReadinessSet{}, err
		}
		for _, value := range list.Values {
			answers[value.QuestionID] = value
		}
	}
	set.Entries = evaluateApplicationRoute(check, stored, answers)
	return set, nil
}

// evaluateApplicationRoute is the pure per-type verdict table for a checked
// role with a verified application route. Stored versions flip required
// types to ready; form values derive from saved answers at call time.
func evaluateApplicationRoute(check *CheckView, stored map[string]ArtifactView, answers map[string]QuestionAnswerValue) []ArtifactReadinessEntry {
	required := map[string]string{}
	if len(check.Questions) > 0 {
		required[ArtifactFormValues] = "vacancy states employer questions"
	}
	if artifactEmailPattern.MatchString(check.Route.DestinationText) {
		required[ArtifactEmailSubject] = "route destination " + strconv.Quote(check.Route.DestinationText)
		required[ArtifactEmailBody] = "route destination " + strconv.Quote(check.Route.DestinationText)
	}
	if basis, ok := documentBasis(check, artifactCVKeywords, false); ok {
		required[ArtifactCV] = basis
	}
	if basis, ok := documentBasis(check, artifactLetterKeywords, true); ok {
		required[ArtifactCoverLetter] = basis
	}
	entries := make([]ArtifactReadinessEntry, 0, len(ArtifactReadinessTypes))
	for _, artifactType := range ArtifactReadinessTypes {
		basis, ok := required[artifactType]
		entry := ArtifactReadinessEntry{Type: artifactType, Required: ok, Basis: basis}
		if current, storedOK := stored[artifactType]; storedOK {
			current := current
			entry.Current = &current
		}
		switch {
		case artifactType == ArtifactFormValues && ok:
			entry.FormValues = deriveArtifactFormValues(check.Questions, answers)
			entry.State = ArtifactStateReady
			for _, value := range entry.FormValues {
				if value.Required == CheckRequired && strings.TrimSpace(value.Text) == "" {
					entry.State = ArtifactStateHeld
					entry.Reason = "required answers missing"
					break
				}
			}
			if entry.State == ArtifactStateReady {
				entry.Reason = "all required answers saved"
			}
		case artifactType == ArtifactFormValues:
			entry.State = ArtifactStateNotRequired
			entry.Reason = "vacancy states no employer questions"
		case !ok:
			entry.State = ArtifactStateNotRequired
			entry.Reason = "no verified source calls for " + artifactType
			if entry.Current != nil {
				entry.State = ArtifactStateReady
				entry.Reason = "stored version kept although no verified source calls for " + artifactType
			}
		case entry.Current != nil:
			entry.State = ArtifactStateReady
			entry.Reason = "current version stored"
		default:
			entry.State = ArtifactStateHeld
			entry.Reason = "awaiting draft"
		}
		entries = append(entries, entry)
	}
	return entries
}

func deriveArtifactFormValues(questions []CheckQuestionView, answers map[string]QuestionAnswerValue) []ArtifactFormValue {
	out := make([]ArtifactFormValue, 0, len(questions))
	for _, question := range questions {
		value := ArtifactFormValue{QuestionID: question.ID, QuestionText: question.Text,
			Required: question.Required, Kind: question.Kind, State: "unset"}
		if saved, ok := answers[question.ID]; ok {
			value.State = saved.State
			value.Text = saved.Text
			if saved.Required != "" {
				value.Required = saved.Required
			}
		}
		out = append(out, value)
	}
	return out
}
