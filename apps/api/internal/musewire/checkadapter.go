package musewire

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// CheckAdapter turns one check turn's final model text into verified
// check sections. Every statement cites the browsed source URL it was
// copied from; the adapter re-fetches each URL through the app's own
// deterministic fetch and accepts a statement only when the fetched bytes
// contain it verbatim. Verified questions save under the checked vacancy
// via the deterministic saver. Anything ungrounded becomes an honest gap
// instead of a persisted claim; malformed turns fail the adaptation
// itself.
type CheckAdapter struct {
	Captures researchcontract.CaptureReader
	Saved    *publicresearch.Server
	// Fetch resolves one cited source URL to a fetched capture id. The
	// checker supplies it as a closure over the run server's FetchURL;
	// the adapter calls it at most once per distinct URL.
	Fetch func(ctx context.Context, sourceURL string) (string, error)
	// VacancyRef is the checked vacancy's saved ref. Verified questions
	// save under it.
	VacancyRef string
}

type checkClaim struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type checkRouteClaim struct {
	Kind        string `json:"kind"`
	Destination string `json:"destination"`
	Source      string `json:"source"`
}

type checkDocumentClaim struct {
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Text     string `json:"text"`
	Source   string `json:"source"`
}

type checkQuestionClaim struct {
	PromptText string `json:"prompt_text"`
	Required   bool   `json:"required"`
	Source     string `json:"source"`
}

type checkFindings struct {
	Requirements []checkClaim         `json:"requirements"`
	Route        checkRouteClaim      `json:"route"`
	Documents    []checkDocumentClaim `json:"documents"`
	Questions    []checkQuestionClaim `json:"questions"`
	Gaps         []string             `json:"gaps"`
}

// AdaptedCheck is the verified payload plus per-item verification gaps.
// Gaps name dropped claims; the checker maps them into check gaps or a
// blocked verdict. CitedQuestions lists the saved question refs the turn
// produced, so the checker can disclose saved-but-uncited questions
// instead of dropping them silently.
type AdaptedCheck struct {
	Requirements   []store.CheckRequirementInput
	Route          store.CheckRouteInput
	Documents      []store.RequestedDocumentInput
	Questions      []store.CheckQuestionInput
	CitedQuestions []string
	Gaps           []string
}

const (
	maxAdaptedRequirements = 100
	maxAdaptedDocuments    = 100
	maxAdaptedQuestions    = 200
	maxAdaptedTextBytes    = 1 << 20
)

// Adapt verifies one check turn's final text. Empty text adapts to an
// empty payload with a gap: the turn produced no structured findings.
func (a *CheckAdapter) Adapt(ctx context.Context, observedAt, text string) (AdaptedCheck, error) {
	out := AdaptedCheck{}
	if a == nil || a.Captures == nil || a.Saved == nil || a.Fetch == nil || a.VacancyRef == "" {
		return out, fmt.Errorf("musewire: check adapter needs captures, saved evidence, a source fetch and the checked vacancy")
	}
	if len(text) > maxAdaptedTextBytes {
		return out, fmt.Errorf("musewire: check text is %d bytes, over the %d-byte budget", len(text), maxAdaptedTextBytes)
	}
	if strings.TrimSpace(text) == "" {
		out.Gaps = append(out.Gaps, "check turn returned no structured findings")
		return out, nil
	}
	trimmed := strings.TrimSpace(text)
	if fenced, ok := unfenceJSON(trimmed); ok {
		trimmed = fenced
	}
	var findings checkFindings
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	if err := decoder.Decode(&findings); err != nil {
		return out, fmt.Errorf("musewire: check text is not the findings object: %w", err)
	}
	for _, gap := range findings.Gaps {
		if strings.TrimSpace(gap) != "" {
			out.Gaps = append(out.Gaps, "turn reported: "+strings.TrimSpace(gap))
		}
	}
	resolved := map[string]string{}
	blobs := map[string]string{}
	resolve := func(sourceURL string) (captureID, raw string, err error) {
		id, ok := resolved[sourceURL]
		if !ok {
			id, err = a.Fetch(ctx, sourceURL)
			if err != nil {
				resolved[sourceURL] = ""
				return "", "", err
			}
			resolved[sourceURL] = id
		}
		if id == "" {
			return "", "", fmt.Errorf("source %s already failed this turn", shortSource(sourceURL))
		}
		raw, ok = blobs[id]
		if !ok {
			raw, err = errCaptureBytes(ctx, a.Captures, id)
			if err != nil {
				return "", "", err
			}
			blobs[id] = raw
		}
		return id, raw, nil
	}
	locate := func(sourceURL, want string, maxLen int) (store.CheckSourceSpan, string, error) {
		if strings.TrimSpace(sourceURL) == "" || strings.TrimSpace(want) == "" {
			return store.CheckSourceSpan{}, "", fmt.Errorf("missing source citation")
		}
		if len(want) > maxLen {
			return store.CheckSourceSpan{}, "", fmt.Errorf("%d bytes over the %d-byte text gate", len(want), maxLen)
		}
		captureID, raw, err := resolve(sourceURL)
		if err != nil {
			return store.CheckSourceSpan{}, "", err
		}
		at := strings.Index(raw, want)
		if at < 0 {
			return store.CheckSourceSpan{}, "", fmt.Errorf("not a verbatim span of %s", shortSource(sourceURL))
		}
		return store.CheckSourceSpan{CaptureID: captureID, Start: at, End: at + len(want)}, want, nil
	}
	for i, claim := range findings.Requirements {
		if i >= maxAdaptedRequirements {
			out.Gaps = append(out.Gaps, fmt.Sprintf("requirement %d dropped over the %d-item gate", i+1, maxAdaptedRequirements))
			continue
		}
		span, excerpt, err := locate(claim.Source, claim.Text, 2000)
		if err != nil {
			out.Gaps = append(out.Gaps, fmt.Sprintf("requirement %d dropped: %s", i+1, err))
			continue
		}
		out.Requirements = append(out.Requirements, store.CheckRequirementInput{
			Statement: claim.Text, SourceExcerpt: excerpt, SourceSpan: span})
	}
	for i, claim := range findings.Documents {
		if i >= maxAdaptedDocuments {
			out.Gaps = append(out.Gaps, fmt.Sprintf("document %d dropped over the %d-item gate", i+1, maxAdaptedDocuments))
			continue
		}
		if strings.TrimSpace(claim.Label) == "" || len(claim.Label) > 200 {
			out.Gaps = append(out.Gaps, fmt.Sprintf("document %d dropped: label fails the text gate", i+1))
			continue
		}
		span, excerpt, err := locate(claim.Source, claim.Text, 2000)
		if err != nil {
			out.Gaps = append(out.Gaps, fmt.Sprintf("document %d dropped: %s", i+1, err))
			continue
		}
		out.Documents = append(out.Documents, store.RequestedDocumentInput{
			Label: claim.Label, Required: claim.Required, SourceExcerpt: excerpt, SourceSpan: span})
	}
	route, routeGap := a.adaptRoute(locate, findings.Route, observedAt)
	if routeGap != "" {
		out.Gaps = append(out.Gaps, routeGap)
	} else {
		out.Route = route
	}
	for i, claim := range findings.Questions {
		if i >= maxAdaptedQuestions {
			out.Gaps = append(out.Gaps, fmt.Sprintf("question %d dropped over the %d-item gate", i+1, maxAdaptedQuestions))
			continue
		}
		if strings.TrimSpace(claim.PromptText) == "" {
			out.Gaps = append(out.Gaps, fmt.Sprintf("question %d dropped: empty prompt text", i+1))
			continue
		}
		span, excerpt, err := locate(claim.Source, claim.PromptText, 2000)
		if err != nil {
			out.Gaps = append(out.Gaps, fmt.Sprintf("question %d dropped: %s", i+1, err))
			continue
		}
		saved, err := a.Saved.SaveQuestion(ctx, publicresearch.SaveQuestionInput{
			VacancyRef: a.VacancyRef, PromptText: claim.PromptText,
			Required: claim.Required, SourceURL: strings.TrimSpace(claim.Source),
		})
		if err != nil {
			out.Gaps = append(out.Gaps, fmt.Sprintf("question %d dropped: %s", i+1, err))
			continue
		}
		out.CitedQuestions = append(out.CitedQuestions, saved.QuestionRef)
		required := store.CheckOptional
		if claim.Required {
			required = store.CheckRequired
		}
		out.Questions = append(out.Questions, store.CheckQuestionInput{
			Text: claim.PromptText, Required: required, SourceSpan: span, SourceExcerpt: excerpt})
	}
	return out, nil
}

// errCaptureBytes opens one immutable capture and reads its bytes.
func errCaptureBytes(ctx context.Context, captures researchcontract.CaptureReader, captureID string) (string, error) {
	_, reader, err := captures.OpenCapture(ctx, captureID)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// shortSource caps an echoed source URL so a hostile citation cannot bloat
// the gap envelope.
func shortSource(source string) string {
	trimmed := strings.TrimSpace(source)
	if len(trimmed) > 120 {
		return trimmed[:120] + "…"
	}
	if trimmed == "" {
		return "(no source)"
	}
	return trimmed
}

// adaptRoute maps one route claim to the store's honest states. A
// verbatim destination on a supported kind judges an application route;
// anything else leaves the route unresolved rather than guessing. An
// unsupported claim records the dead end with the model's own
// destination text dropped: only verified destinations persist.
func (a *CheckAdapter) adaptRoute(locate func(string, string, int) (store.CheckSourceSpan, string, error), claim checkRouteClaim, observedAt string) (store.CheckRouteInput, string) {
	switch claim.Kind {
	case store.CheckRouteDirect, store.CheckRouteReferral, store.CheckRouteRecruiter, store.CheckRouteUnsupported:
	default:
		return store.CheckRouteInput{}, fmt.Sprintf("route dropped: unknown kind %q", claim.Kind)
	}
	if claim.Kind == store.CheckRouteUnsupported {
		return store.CheckRouteInput{Kind: claim.Kind, Judgment: store.CheckRouteJudgmentUnresolved,
			SourceExcerpt: "no supported application destination in cited evidence", ObservedAt: observedAt}, ""
	}
	if strings.TrimSpace(claim.Destination) == "" {
		return store.CheckRouteInput{Kind: "", Judgment: store.CheckRouteJudgmentUnresolved,
			SourceExcerpt: "destination not stated in cited evidence", ObservedAt: observedAt}, ""
	}
	_, excerpt, err := locate(claim.Source, claim.Destination, 1000)
	if err != nil {
		return store.CheckRouteInput{}, fmt.Sprintf("route dropped: %s", err)
	}
	return store.CheckRouteInput{Kind: claim.Kind, DestinationText: claim.Destination,
		Judgment: store.CheckRouteJudgmentApplication, SourceExcerpt: excerpt, ObservedAt: observedAt}, ""
}
