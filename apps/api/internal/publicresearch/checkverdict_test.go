package publicresearch

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func readyFacts() musecode.Facts {
	return musecode.Facts{CLIPath: "/bin/muse", CLIReportVersion: musecode.PinnedCLIVersion,
		EffectiveModel: "muse-spark-1.3", SubscriptionLaneProved: true,
		SessionProtocolProved: true, WorkspaceIsolatedProved: true}
}

func TestValidateCheckPerformer(t *testing.T) {
	if err := ValidateCheckPerformer(readyFacts()); err != nil {
		t.Fatalf("ready facts: %v", err)
	}
	err := ValidateCheckPerformer(musecode.Facts{})
	unavailable, ok := err.(*PerformerUnavailableError)
	if !ok {
		t.Fatalf("unconfigured facts err=%v, want PerformerUnavailableError", err)
	}
	if unavailable.Code != musecode.CodeNotConfigured {
		t.Fatalf("code=%q, want %q", unavailable.Code, musecode.CodeNotConfigured)
	}
}

func TestDeriveCompleteness(t *testing.T) {
	cases := []struct {
		name     string
		evidence []CaptureEvidence
		want     string
	}{
		{"empty is partial", nil, store.CapturePartial},
		{"all complete", []CaptureEvidence{{CaptureID: "a", Completeness: "complete"}}, store.CaptureComplete},
		{"truncated wins", []CaptureEvidence{
			{CaptureID: "a", Completeness: "complete"},
			{CaptureID: "b", Completeness: "truncated"}}, store.CaptureTruncated},
		{"receipt truncation wins", []CaptureEvidence{{CaptureID: "a", Completeness: "complete", TruncatedReceipt: true}}, store.CaptureTruncated},
		{"paginated degrades", []CaptureEvidence{
			{CaptureID: "a", Completeness: "complete"},
			{CaptureID: "b", Completeness: "paginated"}}, store.CapturePaginated},
		{"snippet is partial", []CaptureEvidence{{CaptureID: "a", Completeness: "complete", IsSnippet: true}}, store.CapturePartial},
		{"partial stays partial", []CaptureEvidence{{CaptureID: "a", Completeness: "partial"}}, store.CapturePartial},
		{"unrecorded fails closed", []CaptureEvidence{{CaptureID: "a"}}, store.CapturePartial},
		{"unknown value fails closed", []CaptureEvidence{{CaptureID: "a", Completeness: "mystery"}}, store.CapturePartial},
	}
	for _, tc := range cases {
		if got := DeriveCompleteness(tc.evidence); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

type stubCaptureReader struct {
	descriptors map[string]researchcontract.Capture
	err         map[string]error
}

func (s *stubCaptureReader) ResolveReceipt(context.Context, string) (researchcontract.ExecutionReceipt, error) {
	return researchcontract.ExecutionReceipt{}, errors.New("stub: no receipts")
}

func (s *stubCaptureReader) OpenCapture(_ context.Context, captureID string) (researchcontract.Capture, io.ReadCloser, error) {
	if err, ok := s.err[captureID]; ok {
		return researchcontract.Capture{}, nil, err
	}
	descriptor, ok := s.descriptors[captureID]
	if !ok {
		return researchcontract.Capture{}, nil, errors.New("stub: unknown capture")
	}
	return descriptor, io.NopCloser(strings.NewReader("bytes")), nil
}

func TestInspectCaptures(t *testing.T) {
	reader := &stubCaptureReader{descriptors: map[string]researchcontract.Capture{
		"cap-complete": {Completeness: "complete"},
		"cap-snippet":  {Completeness: "complete", IsSnippet: true},
	}, err: map[string]error{"cap-broken": errors.New("blob gone")}}
	evidence, gaps := InspectCaptures(context.Background(), reader, []string{"cap-complete", "cap-snippet", "cap-broken"})
	if len(evidence) != 3 {
		t.Fatalf("evidence=%d, want 3", len(evidence))
	}
	if !evidence[1].IsSnippet {
		t.Fatalf("snippet flag lost: %+v", evidence[1])
	}
	if len(gaps) != 1 || !strings.Contains(gaps[0], "cap-broken") {
		t.Fatalf("gaps=%v, want the broken capture named", gaps)
	}
	if got := DeriveCompleteness(evidence); got != store.CapturePartial {
		t.Fatalf("derived=%q, want partial from snippet+unreadable", got)
	}
}

func TestClassifyGap(t *testing.T) {
	cases := []struct {
		diagnostic string
		want       string
	}{
		{`question 2 dropped: not a verbatim span of https://x`, store.CheckGapUnverifiedClaim},
		{"saved question q-0003 was not cited by the check turn", store.CheckGapUnverifiedClaim},
		{`route dropped: unknown kind "pigeon"`, store.CheckGapUnverifiedClaim},
		{"requirement 1 dropped: missing source citation", store.CheckGapAmbiguousSource},
		{"check turn returned no structured findings", store.CheckGapMissingFact},
		{"requirement 1 dropped: source https://x already failed this turn", store.CheckGapMissingFact},
		{"question 4 dropped: empty prompt text", store.CheckGapMissingFact},
		{"turn reported: the portal paginates after 10 jobs", store.CheckGapOther},
		{"question 201 dropped over the 200-item gate", store.CheckGapOther},
	}
	for _, tc := range cases {
		if got := ClassifyGap(tc.diagnostic); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.diagnostic, got, tc.want)
		}
	}
}

func TestClassifyQuestionKind(t *testing.T) {
	attachments := []string{
		"Please upload your CV as a PDF",
		"Attach a cover letter below",
		"Drag and drop your portfolio here",
		"Select file to add your resume (.docx)",
	}
	for _, text := range attachments {
		if got := ClassifyQuestionKind(text); got != store.CheckQuestionAttachment {
			t.Errorf("%q: got %q want attachment", text, got)
		}
	}
	texts := []string{
		"Why do you want this role?",
		"Describe your experience with Go.",
		"What salary do you expect?",
	}
	for _, text := range texts {
		if got := ClassifyQuestionKind(text); got != store.CheckQuestionFreeText {
			t.Errorf("%q: got %q want free_text", text, got)
		}
	}
}

func verdictFixture() CheckVerdictInput {
	return CheckVerdictInput{OpportunityID: "opp-1", CheckID: "check-1",
		Vacancy: store.CheckVacancyInput{CaptureIDs: []string{"cap-1"},
			Completeness: store.CaptureComplete, SourceURL: "https://employer.example/jobs/1",
			RetrievedAt: "2026-09-26T10:00:00Z"},
		RequestedDocuments: []store.RequestedDocumentInput{},
		Requirements:       []store.CheckRequirementInput{},
		Route: store.CheckRouteInput{Kind: store.CheckRouteDirect,
			DestinationText: "jobs@employer.example",
			Judgment:        store.CheckRouteJudgmentApplication,
			SourceExcerpt:   "apply via jobs@employer.example",
			ObservedAt:      "2026-09-26T10:00:00Z"},
		RouteVerified: true,
		Gaps:          []store.CheckGapInput{},
		TurnCompleted: true,
		TurnDetail:    "application page lists an email destination and no questions",
		Activity:      []store.CheckActivityInput{}}
}

func TestDecideCheckVerdictQuestioned(t *testing.T) {
	in := verdictFixture()
	in.Questions = []store.CheckQuestionInput{{Text: "Why this role?", Required: store.CheckRequired,
		Kind: store.CheckQuestionFreeText, SourceExcerpt: "Why this role?",
		SourceSpan: store.CheckSourceSpan{CaptureID: "cap-1", Start: 0, End: 14}}}
	verdict := DecideCheckVerdict(in)
	if verdict.Outcome != CheckOutcomeQuestionsVerified || verdict.Save.Blocked != nil {
		t.Fatalf("verdict=%+v, want completing questioned save", verdict)
	}
	if len(verdict.Save.Questions) != 1 || verdict.Save.Route.Judgment != store.CheckRouteJudgmentApplication {
		t.Fatalf("save=%+v, want questions and route carried", verdict.Save)
	}
}

func TestDecideCheckVerdictZeroQuestionsVerified(t *testing.T) {
	verdict := DecideCheckVerdict(verdictFixture())
	if verdict.Outcome != CheckOutcomeQuestionsNoneVerified || !verdict.ZeroQuestionsVerified {
		t.Fatalf("verdict=%+v, want zero-question completion", verdict)
	}
	if verdict.Save.Blocked != nil {
		t.Fatalf("blocked=%+v, want a completing save", verdict.Save.Blocked)
	}
	if len(verdict.Save.Questions) != 0 {
		t.Fatalf("questions=%d, want an empty set", len(verdict.Save.Questions))
	}
	if verdict.Save.Vacancy.Completeness == "" || verdict.Save.Route.DestinationText == "" {
		t.Fatalf("save=%+v, want route and vacancy retained", verdict.Save)
	}
}

func TestDecideCheckVerdictHeldRetainsSections(t *testing.T) {
	in := verdictFixture()
	in.RouteVerified = false
	in.Route = store.CheckRouteInput{Judgment: store.CheckRouteJudgmentUnresolved,
		SourceExcerpt: "destination not stated in cited evidence", ObservedAt: "2026-09-26T10:00:00Z"}
	verdict := DecideCheckVerdict(in)
	if verdict.Save.Blocked == nil || verdict.Save.Blocked.Code != store.CheckBlockedQuestionsUnresolved {
		t.Fatalf("verdict=%+v, want a questions hold", verdict)
	}
	if verdict.Save.Vacancy.SourceURL == "" || verdict.Save.Route.Judgment == "" {
		t.Fatalf("save=%+v, want verified sections retained under the hold", verdict.Save)
	}

	in = verdictFixture()
	in.Vacancy = store.CheckVacancyInput{}
	in.RouteVerified = false
	in.Route = store.CheckRouteInput{}
	verdict = DecideCheckVerdict(in)
	if verdict.Save.Blocked == nil || verdict.Save.Blocked.Code != store.CheckBlockedQuestionsUnresolved {
		t.Fatalf("verdict=%+v, want a questions hold even with nothing retained", verdict)
	}
	if len(verdict.Save.Vacancy.CaptureIDs) != 0 || verdict.Save.Route.Judgment != "" {
		t.Fatalf("save=%+v, want unvalidatable sections dropped, not fabricated", verdict.Save)
	}

	in = verdictFixture()
	in.RouteVerified = false
	in.RouteUnsupported = true
	in.Route = store.CheckRouteInput{Kind: store.CheckRouteUnsupported,
		Judgment:      store.CheckRouteJudgmentUnresolved,
		SourceExcerpt: "no supported application destination in cited evidence",
		ObservedAt:    "2026-09-26T10:00:00Z"}
	verdict = DecideCheckVerdict(in)
	if verdict.Save.Blocked == nil || verdict.Save.Blocked.Code != store.CheckBlockedRouteUnsupported {
		t.Fatalf("verdict=%+v, want an unsupported-route hold", verdict)
	}

	in = verdictFixture()
	in.TurnCompleted = false
	in.RouteVerified = false
	verdict = DecideCheckVerdict(in)
	if verdict.Save.Blocked == nil || verdict.Save.Blocked.Code != store.CheckBlockedOther {
		t.Fatalf("verdict=%+v, want an unfinished-turn hold", verdict)
	}
}

func TestCheckOutcomeOf(t *testing.T) {
	checked := store.CheckView{Status: store.CheckStatusChecked,
		Questions: []store.CheckQuestionView{{ID: "q-1"}}}
	if got := CheckOutcomeOf(checked); got != CheckOutcomeQuestionsVerified {
		t.Fatalf("questioned outcome=%q", got)
	}
	checked.Questions = nil
	if got := CheckOutcomeOf(checked); got != CheckOutcomeQuestionsNoneVerified {
		t.Fatalf("questionless outcome=%q", got)
	}
	blocked := store.CheckView{Status: store.CheckStatusBlocked}
	if got := CheckOutcomeOf(blocked); got != store.CheckStatusBlocked {
		t.Fatalf("blocked outcome=%q", got)
	}
}

func TestCheckEligibility(t *testing.T) {
	starts := map[string]bool{
		store.CheckOverallNotChecked: true, store.CheckOverallOutdated: true,
		store.CheckOverallBlocked: true, store.CheckOverallChecking: false,
		store.CheckOverallChecked: false,
	}
	for status, want := range starts {
		got, reason := StartCheckAllowed(store.CheckStatusView{Status: status})
		if got != want {
			t.Errorf("start %s: got %v want %v (reason %s)", status, got, want, reason)
		}
		if got && reason != "" {
			t.Errorf("start %s: reason %q on allow", status, reason)
		}
		if !got && reason == "" {
			t.Errorf("start %s: empty reason on deny", status)
		}
	}
	rechecks := map[string]bool{
		store.CheckOverallNotChecked: false, store.CheckOverallOutdated: true,
		store.CheckOverallBlocked: true, store.CheckOverallChecking: false,
		store.CheckOverallChecked: false,
	}
	for status, want := range rechecks {
		got, reason := RecheckAllowed(store.CheckStatusView{Status: status})
		if got != want {
			t.Errorf("recheck %s: got %v want %v (reason %s)", status, got, want, reason)
		}
		if !got && reason == "" {
			t.Errorf("recheck %s: empty reason on deny", status)
		}
	}
	if _, reason := RecheckAllowed(store.CheckStatusView{Status: store.CheckOverallNotChecked}); reason != RecheckIneligibleNotStarted {
		t.Errorf("not-started recheck reason=%q", reason)
	}
}
