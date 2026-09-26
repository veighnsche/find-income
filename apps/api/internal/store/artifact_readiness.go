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

// artifactNegationPhrases mark a document source as explicitly not
// required. A keyword hit inside a negated label, question or
// requirement ("Cover letter not required") never forces requiredness:
// the verified text wins over keyword presence (M3/R09).
var artifactNegationPhrases = []string{
	"not required", "not necessary", "not needed", "no longer required",
	"none required", "not accepted", "optional", "if desired", "only if",
	"if available", "do not attach", "do not send", "don't attach",
	"don't send", "niet vereist", "niet nodig", "optioneel",
}

func negatesRequirement(text string) bool {
	lowered := strings.ToLower(text)
	for _, phrase := range artifactNegationPhrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

// documentBasis finds the first verified source naming a document: a
// required requested-document label, an attachment question text, or a
// requirement statement. Optional documents stay optional, negated
// sources never force requiredness, and a bare
// "motivation"/"motivatie" label names a letter only when it is the
// whole label; inside longer prose only compound forms count.
func documentBasis(check *CheckView, keywords []string, letter bool) (string, bool) {
	for _, doc := range check.RequestedDocuments {
		if !doc.Required || negatesRequirement(doc.Label) {
			continue
		}
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
		if negatesRequirement(question.Text) {
			continue
		}
		if matchesKeywords(question.Text, keywords) {
			return "attachment question " + strconv.Quote(question.Text), true
		}
	}
	for _, requirement := range check.Requirements {
		if negatesRequirement(requirement.Statement) {
			continue
		}
		if matchesKeywords(requirement.Statement, keywords) {
			return "requirement " + strconv.Quote(requirement.Statement), true
		}
	}
	return "", false
}

// basisStaleness compares one stored version's pins against the
// current verified inputs (M2/R07). An empty pin cites nothing and
// cannot contradict current inputs; the first mismatch wins with an
// owner-readable reason. facts maps approved source ids to current
// digests; a nil map skips the fact check (no loader on this read).
func basisStaleness(check *CheckView, current ArtifactView, answers map[string]QuestionAnswerValue, facts map[string]string) (string, bool) {
	basis := current.Basis
	if basis.CheckID != "" && basis.CheckID != check.ID {
		return "outdated: a recheck superseded the pinned check behind version " + strconv.FormatInt(current.Version, 10), true
	}
	if basis.QuestionSetSHA256 != "" && basis.QuestionSetSHA256 != check.QuestionSetSHA256 {
		return "outdated: the employer questions changed behind version " + strconv.FormatInt(current.Version, 10), true
	}
	for _, ref := range basis.AnswerRefs {
		saved, ok := answers[ref.QuestionID]
		if !ok {
			return "outdated: answer " + strconv.Quote(ref.QuestionID) + " left the current question set", true
		}
		if saved.Version != ref.AnswerVersion {
			return "outdated: answer " + strconv.Quote(ref.QuestionID) + " moved v" +
				strconv.FormatInt(ref.AnswerVersion, 10) + " to v" + strconv.FormatInt(saved.Version, 10), true
		}
	}
	if facts != nil {
		for id, digest := range basis.FactSHA256 {
			live, ok := facts[id]
			if !ok {
				return "outdated: source fact " + strconv.Quote(id) + " is no longer approved", true
			}
			if live != digest {
				return "outdated: source fact " + strconv.Quote(id) + " changed", true
			}
		}
	}
	return "", false
}

// ArtifactReadiness reads the per-type readiness set for one selected role.
// Unchecked, outdated, blocked or checking roles leave every type
// unresolved; only a checked role with a verified application route can
// mark types required, and every required verdict cites its basis.
// Stored versions whose basis pins contradict the current verified
// inputs read held with an outdated reason instead of ready (M2/R07),
// while the prior content stays attached for inspection.
func (s *Store) ArtifactReadiness(ctx context.Context, opportunityID string) (ArtifactReadinessSet, error) {
	return s.ArtifactReadinessWithFacts(ctx, opportunityID, nil)
}

// ArtifactReadinessWithFacts is ArtifactReadiness with live approved
// source digests (id -> hex sha256) so changed career facts also
// invalidate the affected stored versions. A nil map skips the fact
// check; answer, check and question-set pins always apply.
func (s *Store) ArtifactReadinessWithFacts(ctx context.Context, opportunityID string, facts map[string]string) (ArtifactReadinessSet, error) {
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
	set.Entries = evaluateApplicationRoute(check, stored, answers, facts)
	return set, nil
}

// evaluateApplicationRoute is the pure per-type verdict table for a checked
// role with a verified application route. Stored versions flip required
// types to ready unless their basis pins contradict the current inputs;
// form values derive from saved answers at call time. Pasteable form
// text is required only when a non-attachment question exists:
// upload-only routes carry their instructions in Handoff, not in text
// values (M3/R09).
func evaluateApplicationRoute(check *CheckView, stored map[string]ArtifactView, answers map[string]QuestionAnswerValue, facts map[string]string) []ArtifactReadinessEntry {
	required := map[string]string{}
	for _, question := range check.Questions {
		if question.Kind != CheckQuestionAttachment {
			required[ArtifactFormValues] = "vacancy states employer questions"
			break
		}
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
		if entry.Current != nil && artifactType != ArtifactFormValues {
			if reason, stale := basisStaleness(check, *entry.Current, answers, facts); stale {
				// Held, not silent ready: the stored content stays
				// attached under Current for inspection while the
				// state tells the owner it no longer matches.
				entry.State = ArtifactStateHeld
				entry.Reason = reason
			}
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
