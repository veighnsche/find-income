// Package interviewprep validates one Codex-drafted interview brief against
// supplied saved context. It does not fetch, persist, book, or send anything.
package interviewprep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

var ErrInvalid = errors.New("invalid interview preparation")
var ErrContextTooLarge = errors.New("interview context exceeds 30000 bytes")

type ContextSource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // invitation, owner_input, role, or employer
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
	Body     string `json:"body"`
}

type CitedText struct {
	Text      string                      `json:"text"`
	Citations []applicationpacks.Citation `json:"citations"`
}

type FocusAlternative struct {
	ID  string    `json:"id"`
	Why CitedText `json:"why"`
}

type Question struct {
	Text string    `json:"text"`
	Why  CitedText `json:"why"`
}

type ExampleOutline struct {
	Title          string                    `json:"title"`
	ExperienceKind string                    `json:"experienceKind"` // personal_project, employment, education, volunteer, or other
	ContextBasis   applicationpacks.Citation `json:"contextBasis"`
	Situation      CitedText                 `json:"situation"`
	Action         CitedText                 `json:"action"`
	Result         *CitedText                `json:"result,omitempty"`
	UnknownResult  string                    `json:"unknownResult,omitempty"`
}

type ScheduleClaim struct {
	StartRFC3339 string                    `json:"startRfc3339,omitempty"`
	EndRFC3339   string                    `json:"endRfc3339,omitempty"`
	Mode         string                    `json:"mode,omitempty"` // video, phone, in_person
	Venue        string                    `json:"venue,omitempty"`
	Citation     applicationpacks.Citation `json:"citation"`
}

type Draft struct {
	Focus          []FocusAlternative `json:"focus"`
	Questions      []Question         `json:"questions"`
	Examples       []ExampleOutline   `json:"examples"`
	ScheduleClaims []ScheduleClaim    `json:"scheduleClaims,omitempty"`
	Unknowns       []string           `json:"unknowns,omitempty"`
}

type Input struct {
	InterviewID   string                    `json:"interviewId"`
	OpportunityID string                    `json:"opportunityId"`
	RoleTitle     string                    `json:"roleTitle"`
	EmployerName  string                    `json:"employerName"`
	Context       []ContextSource           `json:"context"`
	CareerSources []applicationpacks.Source `json:"careerSources"`
	Draft         Draft                     `json:"draft"`
}

type Brief struct {
	Input       Input  `json:"input"`
	InputSHA256 string `json:"inputSha256"`
}

type PreparedBrief struct {
	Brief     Brief                    `json:"brief"`
	Focus     *FocusAlternative        `json:"focus,omitempty"`
	Selection jev.InterviewFocusResult `json:"selection"`
}

type sourceIndex struct {
	body     string
	sha      string
	kind     string
	revision string
}

// Prepare checks exact citations and structural bounds; it cannot prove that
// Codex prose is semantically entailed by an excerpt. That remains reviewable.
func Prepare(input Input) (Brief, error) {
	if !bounded(input.InterviewID, 100) || !optionalBounded(input.OpportunityID, 100) || !optionalBounded(input.RoleTitle, 200) ||
		!optionalBounded(input.EmployerName, 200) || len(input.Context) < 1 || len(input.Context) > 12 ||
		len(input.CareerSources) > 8 ||
		len(input.Draft.Focus) < 1 || len(input.Draft.Focus) > 6 ||
		len(input.Draft.Questions) < 1 || len(input.Draft.Questions) > 12 ||
		len(input.Draft.Examples) > 6 ||
		len(input.Draft.ScheduleClaims) > 4 || len(input.Draft.Unknowns) > 20 {
		return Brief{}, ErrInvalid
	}
	if len(input.Draft.Examples) == 0 && len(input.Draft.Unknowns) == 0 {
		return Brief{}, ErrInvalid
	}
	index := make(map[string]sourceIndex, len(input.Context)+len(input.CareerSources))
	contextBytes := 0
	for _, source := range input.Context {
		if len(source.Body) > 30000 {
			return Brief{}, ErrContextTooLarge
		}
		if !bounded(source.ID, 100) || !bounded(source.Revision, 100) || !validBody(source.Body, 30000) ||
			(source.Kind != "invitation" && source.Kind != "owner_input" && source.Kind != "role" && source.Kind != "employer") ||
			index[source.ID].body != "" || digest(source.Body) != source.SHA256 {
			return Brief{}, ErrInvalid
		}
		contextBytes += len(source.Body)
		if contextBytes > 30000 {
			return Brief{}, ErrContextTooLarge
		}
		index[source.ID] = sourceIndex{body: source.Body, sha: source.SHA256, kind: source.Kind, revision: source.Revision}
	}
	for _, source := range input.CareerSources {
		if !source.Approved || !bounded(source.ID, 100) || !bounded(source.Name, 200) || !validBody(source.Body, 100000) ||
			index[source.ID].body != "" || digest(source.Body) != source.SHA256 {
			return Brief{}, ErrInvalid
		}
		index[source.ID] = sourceIndex{body: source.Body, sha: source.SHA256, kind: "career", revision: source.SHA256}
	}
	seenFocus := map[string]bool{}
	for _, focus := range input.Draft.Focus {
		if !bounded(focus.ID, 80) || seenFocus[focus.ID] || len(focus.Why.Text) > 1000 || !validCited(focus.Why, index) {
			return Brief{}, ErrInvalid
		}
		seenFocus[focus.ID] = true
	}
	for _, question := range input.Draft.Questions {
		if !bounded(question.Text, 500) || !validCited(question.Why, index) {
			return Brief{}, ErrInvalid
		}
	}
	for _, example := range input.Draft.Examples {
		if !bounded(example.Title, 200) || (example.ExperienceKind != "personal_project" && example.ExperienceKind != "employment" && example.ExperienceKind != "education" && example.ExperienceKind != "volunteer" && example.ExperienceKind != "other") ||
			!validCitation(example.ContextBasis, index) || index[example.ContextBasis.SourceID].kind != "career" ||
			!validCited(example.Situation, index) || !validCited(example.Action, index) {
			return Brief{}, ErrInvalid
		}
		if (example.Result == nil) == (example.UnknownResult == "") ||
			(example.Result != nil && !validCited(*example.Result, index)) ||
			(example.UnknownResult != "" && !bounded(example.UnknownResult, 500)) {
			return Brief{}, ErrInvalid
		}
	}
	for _, unknown := range input.Draft.Unknowns {
		if !bounded(unknown, 500) {
			return Brief{}, ErrInvalid
		}
	}
	for _, claim := range input.Draft.ScheduleClaims {
		if !validCitation(claim.Citation, index) || (index[claim.Citation.SourceID].kind != "invitation" && index[claim.Citation.SourceID].kind != "owner_input") ||
			(claim.Mode != "" && claim.Mode != "video" && claim.Mode != "phone" && claim.Mode != "in_person") ||
			len(claim.Venue) > 300 || !utf8.ValidString(claim.Venue) {
			return Brief{}, ErrInvalid
		}
		start, end := time.Time{}, time.Time{}
		var err error
		if claim.StartRFC3339 != "" {
			start, err = time.Parse(time.RFC3339, claim.StartRFC3339)
			if err != nil {
				return Brief{}, ErrInvalid
			}
		}
		if claim.EndRFC3339 != "" {
			end, err = time.Parse(time.RFC3339, claim.EndRFC3339)
			if err != nil || start.IsZero() || !end.After(start) {
				return Brief{}, ErrInvalid
			}
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 500000 {
		return Brief{}, ErrInvalid
	}
	var snapshot Input
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return Brief{}, err
	}
	return Brief{Input: snapshot, InputSHA256: digestBytes(encoded)}, nil
}

func (b Brief) FocusInput(maxReportedTokens int64) (jev.InterviewFocusInput, error) {
	validated, err := Prepare(b.Input)
	if err != nil || b.InputSHA256 == "" || validated.InputSHA256 != b.InputSHA256 {
		return jev.InterviewFocusInput{}, ErrInvalid
	}
	index := make(map[string]sourceIndex)
	for _, source := range b.Input.Context {
		index[source.ID] = sourceIndex{body: source.Body, sha: source.SHA256, kind: source.Kind, revision: source.Revision}
	}
	for _, source := range b.Input.CareerSources {
		index[source.ID] = sourceIndex{body: source.Body, sha: source.SHA256, kind: "career", revision: source.SHA256}
	}
	input := jev.InterviewFocusInput{InterviewID: b.Input.InterviewID, Role: b.Input.RoleTitle, Employer: b.Input.EmployerName, MaxReportedTokens: maxReportedTokens}
	for _, source := range b.Input.Context {
		input.Context = append(input.Context, jev.InterviewFocusContext{ID: source.ID, Kind: source.Kind,
			Revision: source.Revision, SHA256: source.SHA256, Body: source.Body})
	}
	seen := map[string]string{}
	for _, focus := range b.Input.Draft.Focus {
		candidate := jev.InterviewFocusCandidate{ID: focus.ID, Description: focus.Why.Text}
		for _, citation := range focus.Why.Citations {
			key := citation.SourceID + "\x00" + citation.Excerpt
			id := seen[key]
			if id == "" {
				id = "e-" + digest(key)[:16]
				seen[key] = id
				source := index[citation.SourceID]
				input.Evidence = append(input.Evidence, jev.InterviewFocusEvidence{ID: id, SourceID: citation.SourceID,
					SourceRevision: source.revision, SourceKind: source.kind, SourceSHA256: source.sha, Excerpt: citation.Excerpt})
			}
			candidate.EvidenceIDs = append(candidate.EvidenceIDs, id)
		}
		input.Candidates = append(input.Candidates, candidate)
	}
	return input, nil
}

// BindFocusSelection checks that a commissioned Jev result applies to these
// exact supplied alternatives. Unresolved leaves the brief usable without a
// fabricated recommended focus.
func (b Brief) BindFocusSelection(result jev.InterviewFocusResult, maxReportedTokens int64) (PreparedBrief, error) {
	input, err := b.FocusInput(maxReportedTokens)
	if err != nil {
		return PreparedBrief{}, err
	}
	digest, err := jev.InterviewFocusInputDigest(input)
	if err != nil || digest != result.InputSHA256 || len(result.RequestSnapshot) == 0 {
		return PreparedBrief{}, ErrInvalid
	}
	answer, ok := result.ProviderResult.Answers["interview_focus"]
	if !ok || answer.Choice == nil {
		return PreparedBrief{}, ErrInvalid
	}
	prepared := PreparedBrief{Brief: b, Selection: result}
	if result.Disposition == jev.InterviewFocusUnresolved {
		if result.SelectedID != "" || answer.Choice.Choice != "__unresolved__" {
			return PreparedBrief{}, ErrInvalid
		}
		return prepared, nil
	}
	if result.Disposition != jev.InterviewFocusSelected || result.SelectedID != answer.Choice.Choice {
		return PreparedBrief{}, ErrInvalid
	}
	for _, focus := range b.Input.Draft.Focus {
		if focus.ID == result.SelectedID {
			copy := focus
			prepared.Focus = &copy
			return prepared, nil
		}
	}
	return PreparedBrief{}, ErrInvalid
}

func validCited(line CitedText, sources map[string]sourceIndex) bool {
	if !bounded(line.Text, 1200) || len(line.Citations) < 1 || len(line.Citations) > 5 {
		return false
	}
	seen := map[string]bool{}
	for _, citation := range line.Citations {
		key := citation.SourceID + "\x00" + citation.Excerpt
		if seen[key] || !validCitation(citation, sources) {
			return false
		}
		seen[key] = true
	}
	return true
}

func validCitation(citation applicationpacks.Citation, sources map[string]sourceIndex) bool {
	source, ok := sources[citation.SourceID]
	return ok && validBody(citation.Excerpt, 1200) && strings.Contains(source.body, citation.Excerpt)
}

func bounded(value string, max int) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= max && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func optionalBounded(value string, max int) bool { return value == "" || bounded(value, max) }

func validBody(value string, max int) bool {
	return len(value) > 0 && len(value) <= max && utf8.ValidString(value) && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, 0)
}

func digest(value string) string { return digestBytes([]byte(value)) }

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
