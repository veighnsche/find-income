// Check verdicts (K1). This file turns one finished Contributor check turn
// into the terminal check save: completing (checked) when the turn
// verified an application route with actual employer questions, completing
// with an empty question set when the turn verified a questionless route
// (C3 questions_none_verified), or blocked with the verified sections
// retained when anything stayed unresolved. Completeness derives from the
// actual capture metadata behind the verified spans, never from an
// unconditional stamp; diagnostics map to real gap kinds; question text
// maps to its field/upload kind. Nothing here invents evidence: every
// input section must already be verified verbatim against app-side
// captures by the caller.
package publicresearch

import (
	"context"
	"fmt"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Derived terminal check outcomes (C3). The stored status stays
// checked/blocked; the outcome refines a checked row by its verified
// question set so Answer/Prepare/Handoff read one honest value.
const (
	// CheckOutcomeQuestionsVerified marks a checked row with actual
	// verified employer questions.
	CheckOutcomeQuestionsVerified = "questions_verified"
	// CheckOutcomeQuestionsNoneVerified marks a checked row whose route,
	// documents and requirements verified with an empty question set. It
	// proceeds to preparation exactly like a questioned route.
	CheckOutcomeQuestionsNoneVerified = "questions_none_verified"
)

// CheckOutcomeOf derives the terminal outcome of one saved check view.
// Checked with zero questions is the verified questionless outcome, not
// an unresolved hold; any other status passes through unchanged.
func CheckOutcomeOf(view store.CheckView) string {
	if view.Status == store.CheckStatusChecked && len(view.Questions) == 0 {
		return CheckOutcomeQuestionsNoneVerified
	}
	if view.Status == store.CheckStatusChecked {
		return CheckOutcomeQuestionsVerified
	}
	return view.Status
}

// PerformerUnavailableError reports a Contributor lane that cannot be
// admitted. Code is the musecode readiness code; the caller blocks the
// check with the honest detail instead of leaving it inertly checking.
type PerformerUnavailableError struct {
	Code   string
	Detail string
}

func (e *PerformerUnavailableError) Error() string {
	return fmt.Sprintf("publicresearch: contributor check performer unavailable (%s): %s", e.Code, e.Detail)
}

// ValidateCheckPerformer fails closed unless the Contributor lane may run
// a check turn. It performs no I/O and never triggers a model call; the
// caller supplies already-probed facts and runs this synchronously before
// parking a job in checking.
func ValidateCheckPerformer(facts musecode.Facts) error {
	status := musecode.Check(musecode.TierContributor, facts)
	if status.Available {
		return nil
	}
	return &PerformerUnavailableError{Code: status.Code, Detail: status.Detail}
}

// CaptureEvidence is the per-capture metadata behind one verified span.
type CaptureEvidence struct {
	CaptureID        string
	Completeness     string // complete|truncated|paginated|partial|"" when unrecorded
	IsSnippet        bool
	TruncatedReceipt bool
}

// InspectCaptures opens each verified capture id and records its
// completeness metadata. Unreadable captures degrade to gaps with their
// ids intact; the caller still saves whatever verified.
func InspectCaptures(ctx context.Context, reader researchcontract.CaptureReader, captureIDs []string) ([]CaptureEvidence, []string) {
	evidence := make([]CaptureEvidence, 0, len(captureIDs))
	gaps := []string{}
	if reader == nil {
		for _, id := range captureIDs {
			gaps = append(gaps, fmt.Sprintf("capture %s has no reader; completeness degrades to partial", shortCapture(id)))
			evidence = append(evidence, CaptureEvidence{CaptureID: id})
		}
		return evidence, gaps
	}
	for _, id := range captureIDs {
		descriptor, body, err := reader.OpenCapture(ctx, id)
		if err != nil {
			gaps = append(gaps, fmt.Sprintf("capture %s unreadable (%s); completeness degrades to partial", shortCapture(id), shortCaptureErr(err)))
			evidence = append(evidence, CaptureEvidence{CaptureID: id})
			continue
		}
		body.Close()
		evidence = append(evidence, CaptureEvidence{CaptureID: id,
			Completeness: descriptor.Completeness, IsSnippet: descriptor.IsSnippet})
	}
	return evidence, gaps
}

func shortCapture(id string) string {
	if len(id) > 24 {
		return id[:24] + "…"
	}
	if id == "" {
		return "(no capture)"
	}
	return id
}

func shortCaptureErr(err error) string {
	detail := strings.TrimSpace(err.Error())
	if len(detail) > 160 {
		return detail[:160] + "…"
	}
	return detail
}

// DeriveCompleteness folds per-capture metadata into one check vacancy
// completeness value. Any truncation, pagination, snippet or partial
// evidence degrades the whole; unrecorded metadata degrades too. Empty
// evidence is partial, never complete: a turn that verified nothing
// cannot claim a complete capture.
func DeriveCompleteness(evidence []CaptureEvidence) string {
	if len(evidence) == 0 {
		return store.CapturePartial
	}
	degraded := store.CaptureComplete
	for _, item := range evidence {
		switch {
		case item.TruncatedReceipt || item.Completeness == store.CaptureTruncated:
			return store.CaptureTruncated
		case item.Completeness == store.CapturePaginated:
			degraded = store.CapturePaginated
		case item.IsSnippet || item.Completeness == store.CapturePartial || item.Completeness == "":
			if degraded == store.CaptureComplete {
				degraded = store.CapturePartial
			}
		case item.Completeness == store.CaptureComplete:
		default:
			if degraded == store.CaptureComplete {
				degraded = store.CapturePartial
			}
		}
	}
	return degraded
}

// ClassifyGap maps one adapter diagnostic to a real check gap kind.
// Verification failures name unverified claims or missing facts; source
// ambiguity stays ambiguous; turn-reported notes and gate overflows keep
// the honest other instead of masquerading as classified evidence.
func ClassifyGap(diagnostic string) string {
	lower := strings.ToLower(diagnostic)
	switch {
	case strings.Contains(lower, "not a verbatim span"),
		strings.Contains(lower, "was not cited"),
		strings.Contains(lower, "unknown kind"):
		return store.CheckGapUnverifiedClaim
	case strings.Contains(lower, "missing source citation"):
		return store.CheckGapAmbiguousSource
	case strings.Contains(lower, "no structured findings"),
		strings.Contains(lower, "already failed"),
		strings.Contains(lower, "fetch"),
		strings.Contains(lower, "untrusted receipt"),
		strings.Contains(lower, "carries no capture"),
		strings.Contains(lower, "empty prompt"):
		return store.CheckGapMissingFact
	default:
		return store.CheckGapOther
	}
}

// ClassifyQuestionKind maps verified employer-question text to its
// field/upload kind. Only strong upload signals yield attachment; a
// choice kind needs options evidence the turn does not report, so
// everything else stays free text. Conservative by design: M3 must never
// render an upload instruction as a pasteable text value, and the text
// itself is already verified — only the mapping is derived here.
func ClassifyQuestionKind(promptText string) string {
	lower := strings.ToLower(promptText)
	for _, signal := range []string{"upload", "attach", "drag and drop",
		"drop files", "browse files", "choose file", "select file",
		".pdf", ".doc", ".docx", ".rtf", ".txt"} {
		if strings.Contains(lower, signal) {
			return store.CheckQuestionAttachment
		}
	}
	return store.CheckQuestionFreeText
}

// CheckVerdictInput carries the verified sections of one finished turn.
// Questions holds verified actual employer questions only; it is empty
// when the turn verified none. RouteVerified reports a verbatim
// application destination; RouteUnsupported reports a structurally
// accepted unsupported-route claim; Vacancy.Completeness must already be
// derived from the captures behind the spans.
type CheckVerdictInput struct {
	OpportunityID      string
	CheckID            string
	Vacancy            store.CheckVacancyInput
	RequestedDocuments []store.RequestedDocumentInput
	Requirements       []store.CheckRequirementInput
	Route              store.CheckRouteInput
	RouteVerified      bool
	RouteUnsupported   bool
	Gaps               []store.CheckGapInput
	Questions          []store.CheckQuestionInput
	TurnCompleted      bool
	TurnDetail         string
	Activity           []store.CheckActivityInput
}

// CheckVerdict is the terminal save for one finished turn plus its
// derived outcome. ZeroQuestionsVerified marks the C3 questionless
// completion; the save carries QuestionsNoneVerified so the store gate
// accepts the explicitly verified empty question set.
type CheckVerdict struct {
	Save                  store.CheckSaveInput
	Outcome               string
	ZeroQuestionsVerified bool
}

// DecideCheckVerdict maps one finished turn to its terminal save.
// Verified questions complete the check; a completed turn with a verified
// route and captured evidence but zero verified questions completes the
// questionless route; anything else blocks with the coded hold while
// retaining every verified section. Route/documents/requirements saved
// under a hold stay readable for recheck and review.
func DecideCheckVerdict(in CheckVerdictInput) CheckVerdict {
	save := store.CheckSaveInput{OpportunityID: in.OpportunityID, CheckID: in.CheckID,
		RequestedDocuments: orEmptyDocuments(in.RequestedDocuments),
		Requirements:       orEmptyRequirements(in.Requirements),
		Gaps:               orEmptyGaps(in.Gaps),
		Questions:          in.Questions,
		Activity:           in.Activity}
	if activity := verdictActivity(in); activity != nil {
		save.Activity = append(append([]store.CheckActivityInput{}, in.Activity...), *activity)
	}
	if len(in.Questions) > 0 {
		save.Vacancy, save.Route = in.Vacancy, in.Route
		return CheckVerdict{Save: save, Outcome: CheckOutcomeQuestionsVerified}
	}
	if in.TurnCompleted && in.RouteVerified && vacancyEvidencePresent(in.Vacancy) {
		save.Vacancy, save.Route = in.Vacancy, in.Route
		save.QuestionsNoneVerified = true
		return CheckVerdict{Save: save, Outcome: CheckOutcomeQuestionsNoneVerified, ZeroQuestionsVerified: true}
	}
	code, detail := heldReason(in)
	save.Blocked = &store.CheckBlockedInput{Code: code, Detail: detail}
	if vacancySaveable(in.Vacancy) {
		save.Vacancy = in.Vacancy
	}
	if routeSaveable(in.Route) {
		save.Route = in.Route
	}
	return CheckVerdict{Save: save, Outcome: code}
}

// verdictActivity journals the verdict shape for the check activity feed.
// Lifecycle kinds stay server-issued; this entry is an honest performer
// note under the check's own namespace.
func verdictActivity(in CheckVerdictInput) *store.CheckActivityInput {
	if len(in.Questions) > 0 || !in.TurnCompleted || !in.RouteVerified || !vacancyEvidencePresent(in.Vacancy) {
		return nil
	}
	return &store.CheckActivityInput{Kind: "muse.check_zero_questions",
		Outcome: string(researchcontract.OutcomeOK)}
}

// heldReason codes an unresolved hold. A finished turn that verified no
// questions holds questions_unresolved however little else survived:
// the retained sections, gaps and detail disclose what held, and the
// route stays readable for recheck. An unfinished turn holds other; an
// unsupported application route holds route_unsupported since no route
// can carry preparation. Source_unavailable stays reserved for
// PerformCheck-time evidence failures before any turn conducts.
func heldReason(in CheckVerdictInput) (string, string) {
	detail := strings.TrimSpace(in.TurnDetail)
	if detail == "" {
		detail = "check turn verified no employer questions"
	}
	if len(detail) > 2000 {
		detail = detail[:2000]
	}
	if !in.TurnCompleted {
		return store.CheckBlockedOther, "check turn did not complete; " + detail
	}
	if in.RouteUnsupported {
		return store.CheckBlockedRouteUnsupported, "application route is unsupported; " + detail
	}
	return store.CheckBlockedQuestionsUnresolved, detail
}

func vacancyEvidencePresent(vacancy store.CheckVacancyInput) bool {
	return len(vacancy.CaptureIDs)+len(vacancy.EvidenceSourceIDs) > 0
}

// vacancySaveable mirrors the store's blocked-save presence gate: a hold
// carries the vacancy only when it is complete enough to validate.
func vacancySaveable(vacancy store.CheckVacancyInput) bool {
	if !vacancyEvidencePresent(vacancy) || vacancy.Completeness == "" ||
		vacancy.SourceURL == "" || vacancy.RetrievedAt == "" {
		return false
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, vacancy.CaptureIDs...), vacancy.EvidenceSourceIDs...) {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// routeSaveable mirrors the store's blocked-save presence gate for the
// route: a hold carries it only when a judgment with honest provenance
// survives.
func routeSaveable(route store.CheckRouteInput) bool {
	return route.Judgment != "" && strings.TrimSpace(route.SourceExcerpt) != "" && route.ObservedAt != ""
}

func orEmptyDocuments(in []store.RequestedDocumentInput) []store.RequestedDocumentInput {
	if in == nil {
		return []store.RequestedDocumentInput{}
	}
	return in
}

func orEmptyRequirements(in []store.CheckRequirementInput) []store.CheckRequirementInput {
	if in == nil {
		return []store.CheckRequirementInput{}
	}
	return in
}

func orEmptyGaps(in []store.CheckGapInput) []store.CheckGapInput {
	if in == nil {
		return []store.CheckGapInput{}
	}
	return in
}

// Recheck eligibility reasons for the explicit recheck action. Recheck is
// separate from the first start: only a held or outdated check may
// recheck, while a current check converges and a pending one must finish.
const (
	RecheckIneligibleNotStarted = "check_not_started"
	RecheckIneligibleInProgress = "check_in_progress"
	RecheckIneligibleCurrent    = "check_current"
	RecheckIneligibleUnknown    = "check_unknown"
)

// RecheckAllowed reports whether the owner may explicitly start a recheck
// from the current read-model status. The store start gate stays
// authoritative; this mirrors it for eligible-action display so held work
// offers recheck while fresh, pending and never-started work do not.
func RecheckAllowed(status store.CheckStatusView) (bool, string) {
	switch status.Status {
	case store.CheckOverallOutdated, store.CheckOverallBlocked:
		return true, ""
	case store.CheckOverallNotChecked:
		return false, RecheckIneligibleNotStarted
	case store.CheckOverallChecking:
		return false, RecheckIneligibleInProgress
	case store.CheckOverallChecked:
		return false, RecheckIneligibleCurrent
	default:
		return false, RecheckIneligibleUnknown
	}
}

// StartCheckAllowed reports whether the owner may explicitly start a
// check from the current read-model status: first starts plus restarts
// from held or outdated work. Pending checks must finish and current
// checks converge instead of starting again.
func StartCheckAllowed(status store.CheckStatusView) (bool, string) {
	switch status.Status {
	case store.CheckOverallNotChecked, store.CheckOverallOutdated, store.CheckOverallBlocked:
		return true, ""
	case store.CheckOverallChecking:
		return false, RecheckIneligibleInProgress
	case store.CheckOverallChecked:
		return false, RecheckIneligibleCurrent
	default:
		return false, RecheckIneligibleUnknown
	}
}
