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
// check sections. Every statement must cite the capture it came from,
// and the adapter accepts it only when the cited bytes contain the
// statement verbatim. Anything ungrounded becomes an honest gap instead
// of a persisted claim; malformed turns fail the adaptation itself.
type CheckAdapter struct {
	Captures researchcontract.CaptureReader
	Saved    *publicresearch.Server
}

type checkClaim struct {
	Text    string `json:"text"`
	Capture string `json:"capture"`
}

type checkRouteClaim struct {
	Kind        string `json:"kind"`
	Destination string `json:"destination"`
	Capture     string `json:"capture"`
}

type checkDocumentClaim struct {
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Text     string `json:"text"`
	Capture  string `json:"capture"`
}

type checkQuestionClaim struct {
	Ref     string `json:"ref"`
	Capture string `json:"capture"`
}

type checkFindings struct {
	Requirements []checkClaim         `json:"requirements"`
	Route        checkRouteClaim      `json:"route"`
	Documents    []checkDocumentClaim `json:"documents"`
	Questions    []checkQuestionClaim `json:"questions"`
}

// AdaptedCheck is the verified payload plus per-item verification gaps.
// Gaps name dropped claims; the checker maps them into check gaps or a
// blocked verdict.
type AdaptedCheck struct {
	Requirements []store.CheckRequirementInput
	Route        store.CheckRouteInput
	Documents    []store.RequestedDocumentInput
	Questions    []store.CheckQuestionInput
	Gaps         []string
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
	if a == nil || a.Captures == nil || a.Saved == nil {
		return out, fmt.Errorf("musewire: check adapter needs captures and saved evidence")
	}
	if len(text) > maxAdaptedTextBytes {
		return out, fmt.Errorf("musewire: check text is %d bytes, over the %d-byte budget", len(text), maxAdaptedTextBytes)
	}
	if strings.TrimSpace(text) == "" {
		out.Gaps = append(out.Gaps, "check turn returned no structured findings")
		return out, nil
	}
	var findings checkFindings
	decoder := json.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(&findings); err != nil {
		return out, fmt.Errorf("musewire: check text is not the findings object: %w", err)
	}
	captures := map[string]string{}
	bytesFor := func(captureID string) (string, error) {
		if held, ok := captures[captureID]; ok {
			return held, nil
		}
		_, reader, err := a.Captures.OpenCapture(ctx, captureID)
		if err != nil {
			return "", err
		}
		defer reader.Close()
		raw, err := io.ReadAll(reader)
		if err != nil {
			return "", err
		}
		captures[captureID] = string(raw)
		return captures[captureID], nil
	}
	locate := func(captureID, want string, maxLen int) (store.CheckSourceSpan, string, error) {
		if captureID == "" || strings.TrimSpace(want) == "" {
			return store.CheckSourceSpan{}, "", fmt.Errorf("missing capture citation")
		}
		if len(want) > maxLen {
			return store.CheckSourceSpan{}, "", fmt.Errorf("%d bytes over the %d-byte text gate", len(want), maxLen)
		}
		raw, err := bytesFor(captureID)
		if err != nil {
			return store.CheckSourceSpan{}, "", err
		}
		at := strings.Index(raw, want)
		if at < 0 {
			return store.CheckSourceSpan{}, "", fmt.Errorf("not a verbatim span of capture %s", captureID)
		}
		return store.CheckSourceSpan{CaptureID: captureID, Start: at, End: at + len(want)}, want, nil
	}
	for i, claim := range findings.Requirements {
		if i >= maxAdaptedRequirements {
			out.Gaps = append(out.Gaps, fmt.Sprintf("requirement %d dropped over the %d-item gate", i+1, maxAdaptedRequirements))
			continue
		}
		span, excerpt, err := locate(claim.Capture, claim.Text, 2000)
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
		span, excerpt, err := locate(claim.Capture, claim.Text, 2000)
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
		question, ok := a.Saved.Question(claim.Ref)
		if !ok {
			out.Gaps = append(out.Gaps, fmt.Sprintf("question %d dropped: unknown saved ref %q", i+1, claim.Ref))
			continue
		}
		span, excerpt, err := locate(claim.Capture, question.PromptText, 2000)
		if err != nil {
			out.Gaps = append(out.Gaps, fmt.Sprintf("question %d dropped: %s", i+1, err))
			continue
		}
		required := store.CheckOptional
		if question.Required {
			required = store.CheckRequired
		}
		out.Questions = append(out.Questions, store.CheckQuestionInput{
			Text: question.PromptText, Required: required, SourceSpan: span, SourceExcerpt: excerpt})
	}
	return out, nil
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
	_, excerpt, err := locate(claim.Capture, claim.Destination, 1000)
	if err != nil {
		return store.CheckRouteInput{}, fmt.Sprintf("route dropped: %s", err)
	}
	return store.CheckRouteInput{Kind: claim.Kind, DestinationText: claim.Destination,
		Judgment: store.CheckRouteJudgmentApplication, SourceExcerpt: excerpt, ObservedAt: observedAt}, ""
}
