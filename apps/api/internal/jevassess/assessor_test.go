package jevassess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// All doubles are in-process fixtures: no network, no provider spend, no
// TYPESAFE key. The real capture reader binds at T23, the real authority at
// T12, the real store at T23, and dynamic-row persistence at T14.

type fakeAuthority struct {
	checkErr   error
	reserveErr error
	reserves   []string // idempotency keys in order
	releases   []string
	attempts   int
}

func (f *fakeAuthority) Check(context.Context, researchcontract.CheckInput) error { return f.checkErr }

func (f *fakeAuthority) Reserve(_ context.Context, _ string, _ string, key string, _ string) (researchcontract.Reservation, error) {
	if f.reserveErr != nil {
		return researchcontract.Reservation{}, f.reserveErr
	}
	f.attempts++
	f.reserves = append(f.reserves, key)
	return researchcontract.Reservation{ID: "res-" + key, AttemptID: "round-attempt-" + key,
		RunID: "run", IdempotencyKey: key, ReservedAt: time.Now()}, nil
}

func (f *fakeAuthority) Release(_ context.Context, id string) error {
	f.releases = append(f.releases, id)
	return nil
}

func (f *fakeAuthority) Usage(context.Context, string) (researchcontract.UsageLedger, error) {
	return researchcontract.UsageLedger{}, nil
}

func (f *fakeAuthority) ReconciliationFor(context.Context, string) (researchcontract.Reconciliation, error) {
	return researchcontract.Reconciliation{}, researchcontract.NewError(researchcontract.OutcomeNotFound, "", "none")
}

type fakeCapture struct {
	capture researchcontract.Capture
	body    string
	err     error
}

type fakeCaptures struct {
	items map[string]fakeCapture
	opens []string
}

func (f *fakeCaptures) ResolveReceipt(context.Context, string) (researchcontract.ExecutionReceipt, error) {
	return researchcontract.ExecutionReceipt{}, researchcontract.NewError(researchcontract.OutcomeNotFound, "", "unused")
}

func (f *fakeCaptures) OpenCapture(_ context.Context, id string) (researchcontract.Capture, io.ReadCloser, error) {
	f.opens = append(f.opens, id)
	item, ok := f.items[id]
	if !ok || item.err != nil {
		var err error = researchcontract.NewError(researchcontract.OutcomeNotFound, "", "no such capture")
		if item.err != nil {
			err = item.err
		}
		return researchcontract.Capture{}, nil, err
	}
	capture := item.capture
	capture.ID = id
	capture.Bytes = int64(len(item.body))
	return capture, io.NopCloser(strings.NewReader(item.body)), nil
}

type fakeBriefs struct {
	profile int64
	rubric  string
	err     error
}

func (f *fakeBriefs) CurrentBrief(context.Context, string) (int64, string, error) {
	return f.profile, f.rubric, f.err
}

type fakeProvider struct {
	model    string
	calls    int
	last     jev.Request
	encoded  []byte
	result   jev.Result
	exchange jev.CapturedExchange
	err      error
}

func (f *fakeProvider) RequestedModel() string { return f.model }

func (f *fakeProvider) EncodedRequest(jev.Request) ([]byte, error) {
	return append([]byte(nil), f.encoded...), nil
}

func (f *fakeProvider) EvaluateOnceCaptured(_ context.Context, r jev.Request) (jev.Result, jev.CapturedExchange, error) {
	f.calls++
	f.last = r
	return f.result, f.exchange, f.err
}

type roundFinish struct {
	actor     store.Actor
	runID     string
	attemptID string
	success   bool
	result    json.RawMessage
	errorCode string
}

type fakeExchanges struct {
	marks          []string // "runID:attemptID" in call order
	markErr        error
	begins         []store.JevAttemptStart
	finishes       []store.JevAttemptFinish
	reclassified   []string
	beginErr       error
	finishErr      error
	next           int
	order          []string // "mark"|"begin" in call order
	actor          store.Actor
	roundErr       error
	finished       []roundFinish
	finishRoundErr error // FinishJevAttempt failures use finishErr
}

func (f *fakeExchanges) MarkRoundDispatched(_ context.Context, runID, attemptID string) (store.RoundAttempt, error) {
	if f.markErr != nil {
		return store.RoundAttempt{}, f.markErr
	}
	f.marks = append(f.marks, runID+":"+attemptID)
	f.order = append(f.order, "mark")
	return store.RoundAttempt{ID: attemptID, RoundID: runID, State: store.AttemptDispatched}, nil
}

func (f *fakeExchanges) Round(_ context.Context, runID string) (store.Round, error) {
	if f.roundErr != nil {
		return store.Round{}, f.roundErr
	}
	return store.Round{ID: runID, Actor: f.actor}, nil
}

func (f *fakeExchanges) FinishRoundAttempt(_ context.Context, actor store.Actor, runID, attemptID string, success bool, result json.RawMessage, errorCode string) (store.RoundAttempt, error) {
	if f.finishRoundErr != nil {
		return store.RoundAttempt{}, f.finishRoundErr
	}
	f.finished = append(f.finished, roundFinish{actor: actor, runID: runID, attemptID: attemptID,
		success: success, result: append(json.RawMessage(nil), result...), errorCode: errorCode})
	state := store.AttemptFailed
	if success {
		state = store.AttemptSucceeded
	}
	return store.RoundAttempt{ID: attemptID, RoundID: runID, State: state}, nil
}

func (f *fakeExchanges) BeginJevAttempt(_ context.Context, in store.JevAttemptStart) (store.JevAttempt, error) {
	if f.beginErr != nil {
		return store.JevAttempt{}, f.beginErr
	}
	f.next++
	f.begins = append(f.begins, in)
	f.order = append(f.order, "begin")
	return store.JevAttempt{ID: "jev-attempt", Purpose: in.Purpose, Status: "dispatched"}, nil
}

func (f *fakeExchanges) FinishJevAttempt(_ context.Context, in store.JevAttemptFinish) (store.JevAttempt, error) {
	if f.finishErr != nil {
		return store.JevAttempt{}, f.finishErr
	}
	f.finishes = append(f.finishes, in)
	return store.JevAttempt{ID: in.ID, Status: in.Status}, nil
}

func (f *fakeExchanges) ReclassifyJevAttempt(_ context.Context, id, status, _ string) error {
	f.reclassified = append(f.reclassified, id+":"+status)
	return nil
}

type fakeSink struct {
	records []DynamicAssessmentRecord
	err     error
}

func (f *fakeSink) SaveDynamicAssessment(_ context.Context, r DynamicAssessmentRecord) error {
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, r)
	return nil
}

func fixture(t *testing.T) (*Handler, *fakeAuthority, *fakeCaptures, *fakeBriefs, *fakeProvider, *fakeExchanges, *fakeSink) {
	t.Helper()
	auth, captures, briefs, provider, exchanges, sink := &fakeAuthority{},
		&fakeCaptures{items: map[string]fakeCapture{
			"cap-alpha": {capture: researchcontract.Capture{Complete: true}, body: "senior backend role, Amsterdam, hybrid, posted Monday"},
			"cap-beta":  {capture: researchcontract.Capture{Complete: true}, body: "requires five years of Go and on-call experience"},
		}},
		&fakeBriefs{profile: 3, rubric: "rubric-v2"},
		&fakeProvider{model: "jev-1.13.0", encoded: []byte(`{"model":"jev-1.13.0"}`),
			exchange: jev.CapturedExchange{RequestBytes: []byte(`{"model":"jev-1.13.0"}`),
				ResponseBytes: []byte(`{"model":"jev-1.13.0"}`), HTTPStatus: 200, ReturnedModel: "jev-1.13.0"}},
		&fakeExchanges{actor: store.Actor{Kind: "administrator", ID: "owner"}}, &fakeSink{}
	h := &Handler{Authority: auth, Captures: captures, Briefs: briefs, Provider: provider, Exchanges: exchanges, Sink: sink}
	return h, auth, captures, briefs, provider, exchanges, sink
}

func validInput() researchcontract.AssessInput {
	return researchcontract.AssessInput{
		Purpose: "role_fit",
		Questions: []researchcontract.AssessQuestion{
			{ID: "q-location", Text: "Is the role in scope for location?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "yes", Label: "Yes, Amsterdam hybrid is in scope",
						EvidenceRefs: []researchcontract.EvidenceRef{{CaptureID: "cap-alpha", SpanStart: 20, SpanEnd: 37}}},
					{ID: "no", Label: "No, out of scope",
						EvidenceRefs: []researchcontract.EvidenceRef{{CaptureID: "cap-beta", SpanStart: 0, SpanEnd: 8}}},
				}},
			{ID: "q-seniority", Text: "Does the seniority fit?", AbstainAllowed: true,
				Alternatives: []researchcontract.AssessAlternative{
					{ID: "fit", Label: "Senior fits",
						EvidenceRefs: []researchcontract.EvidenceRef{{CaptureID: "cap-alpha", SpanStart: 0, SpanEnd: 6}}},
				}},
		},
		ProfileVersion:      3,
		RubricVersion:       "rubric-v2",
		CandidateIdentities: []researchcontract.CandidateIdentity{{CandidateID: "opp-1", Kind: "opportunity", Revision: 2}},
		SourceRefs:          []researchcontract.EvidenceRef{{CaptureID: "cap-alpha", SpanStart: 0, SpanEnd: 48}},
		IdempotencyKey:      "assess-1",
		RunID:               "run-1",
		Generation:          7,
	}
}

func choiceResult(answers map[string]string) jev.Result {
	out := jev.Result{RequestedModel: "jev-1.13.0", ReturnedModel: "jev-1.13.0",
		Answers: map[string]jev.Answer{}, RawResponse: json.RawMessage(`{"model":"jev-1.13.0"}`)}
	for id, choice := range answers {
		out.Answers[id] = jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{
			Choice: choice, Probabilities: map[string]float64{choice: 1}, Confidence: 0.9}}
	}
	return out
}

func contractCode(t *testing.T, err error) researchcontract.Outcome {
	t.Helper()
	var contractErr *researchcontract.Error
	if !errors.As(err, &contractErr) {
		t.Fatalf("expected typed contract error, got %T %v", err, err)
	}
	return contractErr.Code
}

func TestAssessSuccess(t *testing.T) {
	h, auth, _, _, provider, exchanges, sink := fixture(t)
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": AbstainID})
	provider.exchange = jev.CapturedExchange{RequestBytes: provider.encoded,
		ResponseBytes: []byte(`{"model":"jev-1.13.0"}`), HTTPStatus: 200, ReturnedModel: "jev-1.13.0"}

	got, err := h.Assess(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || !strings.HasPrefix(got.ID, "jda_") {
		t.Fatalf("bad assessment id %q", got.ID)
	}
	if len(got.Results) != 2 || got.Model != "jev-1.13.0" || got.ModelVersion != "jev-1.13.0" {
		t.Fatalf("bad assessment header %+v", got)
	}
	if got.Results[0].AnswerID != "yes" || got.Results[0].Abstained || got.Results[0].Uncertainty != "confidence 0.9000" {
		t.Fatalf("bad first answer %+v", got.Results[0])
	}
	if len(got.Results[0].SourceBindings) != 1 || got.Results[0].SourceBindings[0].CaptureID != "cap-alpha" {
		t.Fatalf("answer must bind exactly the chosen alternative refs: %+v", got.Results[0])
	}
	if !got.Results[1].Abstained || got.Results[1].AnswerID != "" || len(got.Results[1].SourceBindings) != 0 {
		t.Fatalf("abstention must carry no answer or bindings: %+v", got.Results[1])
	}
	if len(got.ReuseKey) != 64 {
		t.Fatalf("bad reuse key %q", got.ReuseKey)
	}
	if provider.calls != 1 {
		t.Fatalf("one reservation allows exactly one provider call, got %d", provider.calls)
	}
	if len(exchanges.begins) != 1 || len(exchanges.finishes) != 1 || exchanges.finishes[0].Status != "succeeded" {
		t.Fatalf("exchange not retained as succeeded: %+v %+v", exchanges.begins, exchanges.finishes)
	}
	if exchanges.begins[0].RoundAttemptID != "round-attempt-assess-1" || exchanges.begins[0].RequestedModel != "jev-1.13.0" {
		t.Fatalf("exchange must carry the reservation identity and model: %+v", exchanges.begins[0])
	}
	if !json.Valid(exchanges.begins[0].SourceRefsJSON) || !json.Valid(exchanges.begins[0].CandidateSetJSON) ||
		!json.Valid(exchanges.begins[0].LogicalRequestJSON) || len(exchanges.begins[0].InputSHA256) != 64 {
		t.Fatalf("exchange log must retain exact canonical input: %+v", exchanges.begins[0])
	}
	if len(auth.reserves) != 1 || auth.reserves[0] != "assess-1" {
		t.Fatalf("expected one reservation, got %+v", auth.reserves)
	}
	if len(sink.records) != 1 || sink.records[0].ReuseKey != got.ReuseKey ||
		sink.records[0].JevAttemptID != "jev-attempt" || sink.records[0].Status != "partial_abstain" {
		t.Fatalf("sink record must carry the binding: %+v", sink.records)
	}
	if len(sink.records[0].CandidateSetHash) != 64 || !json.Valid(sink.records[0].AnswersJSON) {
		t.Fatalf("sink record must carry candidate hash and answers: %+v", sink.records[0])
	}
	// Codex-defined ids flow through verbatim: no fixed menu.
	sent, ok := provider.last.Questions["q-location"].(jev.ChoiceQuestion)
	if !ok || sent.Criteria["yes"] != "Yes, Amsterdam hybrid is in scope" || sent.Criteria[AbstainID] == "" {
		t.Fatalf("provider must see Codex criteria plus abstain: %+v", provider.last.Questions)
	}
	if sent.Instructions != "Is the role in scope for location?" {
		t.Fatalf("question text must pass through verbatim: %q", sent.Instructions)
	}
}

func TestAssessMissingEvidence(t *testing.T) {
	h, auth, _, _, provider, exchanges, _ := fixture(t)
	in := validInput()
	in.SourceRefs = []researchcontract.EvidenceRef{{CaptureID: "cap-gone", SpanStart: 0, SpanEnd: 4}}
	if _, err := h.Assess(context.Background(), in); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("missing capture must fail not_found, got %v", err)
	}
	if provider.calls != 0 || len(auth.reserves) != 0 || len(exchanges.begins) != 0 {
		t.Fatal("missing evidence must fail before reservation and dispatch")
	}
}

func TestAssessConflictingEvidence(t *testing.T) {
	h, _, _, _, provider, _, _ := fixture(t)
	in := validInput()
	// One span cannot support two competing alternatives of one question.
	in.Questions[0].Alternatives[1].EvidenceRefs = in.Questions[0].Alternatives[0].EvidenceRefs
	if _, err := h.Assess(context.Background(), in); contractCode(t, err) != researchcontract.OutcomeConflict {
		t.Fatalf("span behind two alternatives must fail conflict, got %v", err)
	}
	if provider.calls != 0 {
		t.Fatal("conflicting evidence must fail before dispatch")
	}
}

func TestAssessIncompleteCapture(t *testing.T) {
	h, _, captures, _, provider, _, _ := fixture(t)
	captures.items["cap-alpha"] = fakeCapture{capture: researchcontract.Capture{Complete: false}, body: "partial"}
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeCaptureIncomplete {
		t.Fatalf("incomplete capture must fail capture_incomplete, got %v", err)
	}
	if provider.calls != 0 {
		t.Fatal("incomplete capture must fail before dispatch")
	}
}

func TestAssessSpanBeyondBytes(t *testing.T) {
	h, _, _, _, provider, _, _ := fixture(t)
	in := validInput()
	in.SourceRefs = []researchcontract.EvidenceRef{{CaptureID: "cap-alpha", SpanStart: 0, SpanEnd: 5000}}
	if _, err := h.Assess(context.Background(), in); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("span beyond bytes must fail invalid, got %v", err)
	}
	if provider.calls != 0 {
		t.Fatal("unbound span must fail before dispatch")
	}
}

func TestAssessStaleBrief(t *testing.T) {
	h, _, _, briefs, provider, _, _ := fixture(t)
	briefs.profile = 4
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeStale {
		t.Fatalf("brief mismatch must fail stale, got %v", err)
	}
	if provider.calls != 0 {
		t.Fatal("stale brief must fail before dispatch")
	}
}

func TestAssessMalformedProviderResponse(t *testing.T) {
	h, _, _, _, provider, exchanges, sink := fixture(t)
	provider.err = &jev.Error{Kind: jev.ErrInvalidResponse, Attempts: 1}
	provider.exchange = jev.CapturedExchange{RequestBytes: provider.encoded, ResponseBytes: []byte(`{broken`), HTTPStatus: 200}
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("malformed provider response must fail invalid, got %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("exactly one attempt per reservation, got %d", provider.calls)
	}
	if len(exchanges.finishes) != 1 || exchanges.finishes[0].Status != "invalid_response" {
		t.Fatalf("malformed response must be logged invalid_response: %+v", exchanges.finishes)
	}
	if string(exchanges.finishes[0].RawResponseBytes) != "{broken" {
		t.Fatal("exact malformed bytes must be retained")
	}
	if len(sink.records) != 0 {
		t.Fatal("no assessment row on provider failure")
	}
}

func TestAssessUnknownAnswerIDRejected(t *testing.T) {
	h, _, _, _, provider, exchanges, sink := fixture(t)
	provider.result = choiceResult(map[string]string{"q-location": "invented", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("unknown answer id must fail invalid, got %v", err)
	}
	if len(exchanges.reclassified) != 1 {
		t.Fatalf("helper rejection must reclassify the exchange: %+v", exchanges.reclassified)
	}
	if len(sink.records) != 0 {
		t.Fatal("no fabricated assessment may persist")
	}
}

func TestAssessOutageIsUncertainWithoutRetry(t *testing.T) {
	h, auth, _, _, provider, exchanges, _ := fixture(t)
	provider.err = &jev.Error{Kind: jev.ErrTimeout, Attempts: 1}
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeUncertain {
		t.Fatalf("outage must fail outcome_uncertain, got %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("no internal retry loop: exactly one call, got %d", provider.calls)
	}
	if len(exchanges.finishes) != 1 || exchanges.finishes[0].Status != "uncertain" {
		t.Fatalf("outage must be logged uncertain: %+v", exchanges.finishes)
	}
	if len(auth.reserves) != 1 {
		t.Fatalf("one call consumes one reservation, got %+v", auth.reserves)
	}
}

func TestAssessExplicitReservedRetry(t *testing.T) {
	h, auth, _, _, provider, exchanges, _ := fixture(t)
	provider.err = &jev.Error{Kind: jev.ErrUnavailable, Attempts: 1}
	first := validInput()
	if _, err := h.Assess(context.Background(), first); contractCode(t, err) != researchcontract.OutcomeUncertain {
		t.Fatalf("first attempt must fail uncertain, got %v", err)
	}
	provider.err = nil
	provider.result = choiceResult(map[string]string{"q-location": "no", "q-seniority": "fit"})
	provider.exchange = jev.CapturedExchange{RequestBytes: provider.encoded, ResponseBytes: []byte(`{}`), HTTPStatus: 200}
	second := validInput()
	second.IdempotencyKey = "assess-2"
	got, err := h.Assess(context.Background(), second)
	if err != nil {
		t.Fatalf("explicit retry must succeed: %v", err)
	}
	if got.Results[0].AnswerID != "no" {
		t.Fatalf("retry answer not returned: %+v", got.Results[0])
	}
	if len(auth.reserves) != 2 || auth.reserves[0] == auth.reserves[1] {
		t.Fatalf("each attempt needs its own reservation: %+v", auth.reserves)
	}
	if provider.calls != 2 || len(exchanges.begins) != 2 {
		t.Fatal("retry is a separately logged attempt, not a loop")
	}
}

func TestAssessReserveConflict(t *testing.T) {
	h, auth, _, _, provider, exchanges, _ := fixture(t)
	auth.reserveErr = researchcontract.NewError(researchcontract.OutcomeConflict, "", "same key, different payload")
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeConflict {
		t.Fatalf("reservation conflict must surface, got %v", err)
	}
	if provider.calls != 0 || len(exchanges.begins) != 0 {
		t.Fatal("conflicted reservation must not dispatch")
	}
}

func TestAssessMalformedInputs(t *testing.T) {
	cases := map[string]func(*researchcontract.AssessInput){
		"empty purpose":         func(in *researchcontract.AssessInput) { in.Purpose = "" },
		"no questions":          func(in *researchcontract.AssessInput) { in.Questions = nil },
		"duplicate questions":   func(in *researchcontract.AssessInput) { in.Questions[1].ID = "q-location" },
		"empty question text":   func(in *researchcontract.AssessInput) { in.Questions[0].Text = "" },
		"abstain forbidden":     func(in *researchcontract.AssessInput) { in.Questions[0].AbstainAllowed = false },
		"no alternatives":       func(in *researchcontract.AssessInput) { in.Questions[0].Alternatives = nil },
		"duplicate alt ids":     func(in *researchcontract.AssessInput) { in.Questions[0].Alternatives[1].ID = "yes" },
		"reserved alt id":       func(in *researchcontract.AssessInput) { in.Questions[0].Alternatives[0].ID = AbstainID },
		"empty alt label":       func(in *researchcontract.AssessInput) { in.Questions[0].Alternatives[0].Label = "" },
		"no source refs":        func(in *researchcontract.AssessInput) { in.SourceRefs = nil },
		"empty span":            func(in *researchcontract.AssessInput) { in.SourceRefs[0].SpanEnd = in.SourceRefs[0].SpanStart },
		"negative span":         func(in *researchcontract.AssessInput) { in.SourceRefs[0].SpanStart = -1 },
		"bad candidate kind":    func(in *researchcontract.AssessInput) { in.CandidateIdentities[0].Kind = "source" },
		"zero revision":         func(in *researchcontract.AssessInput) { in.CandidateIdentities[0].Revision = 0 },
		"zero profile":          func(in *researchcontract.AssessInput) { in.ProfileVersion = 0 },
		"empty rubric":          func(in *researchcontract.AssessInput) { in.RubricVersion = "" },
		"empty run":             func(in *researchcontract.AssessInput) { in.RunID = "" },
		"empty idempotency key": func(in *researchcontract.AssessInput) { in.IdempotencyKey = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, _, _, provider, _, _ := fixture(t)
			in := validInput()
			mutate(&in)
			if _, err := h.Assess(context.Background(), in); contractCode(t, err) != researchcontract.OutcomeInvalid {
				t.Fatalf("%s must fail invalid, got %v", name, err)
			}
			if provider.calls != 0 {
				t.Fatalf("%s must fail before dispatch", name)
			}
		})
	}
}

func TestReuseKey(t *testing.T) {
	key := func(in researchcontract.AssessInput) string {
		bound, err := validateInput(in)
		if err != nil {
			t.Fatal(err)
		}
		return reuseKey(bound, in.ProfileVersion, in.RubricVersion, "jev-1.13.0", in.Purpose)
	}
	base := key(validInput())
	if len(base) != 64 {
		t.Fatalf("reuse key must be sha256 hex, got %q", base)
	}
	// Canonical: ref order does not matter.
	reordered := validInput()
	reordered.SourceRefs = []researchcontract.EvidenceRef{
		{CaptureID: "cap-beta", SpanStart: 0, SpanEnd: 8},
		{CaptureID: "cap-alpha", SpanStart: 0, SpanEnd: 48},
	}
	if key(reordered) != base {
		t.Fatal("reuse key must be order-insensitive")
	}
	// Every binding input affects the key.
	mutations := map[string]func(*researchcontract.AssessInput){
		"evidence":  func(in *researchcontract.AssessInput) { in.SourceRefs[0].SpanEnd = 40 },
		"alt ref":   func(in *researchcontract.AssessInput) { in.Questions[0].Alternatives[0].EvidenceRefs[0].SpanEnd = 30 },
		"label":     func(in *researchcontract.AssessInput) { in.Questions[0].Alternatives[0].Label += "!" },
		"profile":   func(in *researchcontract.AssessInput) { in.ProfileVersion++ },
		"rubric":    func(in *researchcontract.AssessInput) { in.RubricVersion += "-x" },
		"purpose":   func(in *researchcontract.AssessInput) { in.Purpose += "-x" },
		"candidate": func(in *researchcontract.AssessInput) { in.CandidateIdentities[0].Revision++ },
	}
	for name, mutate := range mutations {
		in := validInput()
		mutate(&in)
		bound, err := validateInput(in)
		if err != nil {
			t.Fatal(err)
		}
		got := reuseKey(bound, in.ProfileVersion, in.RubricVersion, "jev-1.13.0", in.Purpose)
		if name == "candidate" {
			if got != base {
				t.Fatal("contract reuse key excludes candidate identities")
			}
			continue
		}
		if got == base {
			t.Fatalf("%s must change the reuse key", name)
		}
	}
	if key(validInput()) != base {
		t.Fatal("reuse key must be stable")
	}
}

func TestCandidateSetHash(t *testing.T) {
	a := []researchcontract.CandidateIdentity{
		{CandidateID: "opp-1", Kind: "opportunity", Revision: 2},
		{CandidateID: "co-1", Kind: "company", Revision: 1},
	}
	b := []researchcontract.CandidateIdentity{
		{CandidateID: "co-1", Kind: "company", Revision: 1},
		{CandidateID: "opp-1", Kind: "opportunity", Revision: 2},
	}
	if candidateSetHash(a) != candidateSetHash(b) {
		t.Fatal("candidate set hash must be order-insensitive")
	}
	c := []researchcontract.CandidateIdentity{{CandidateID: "opp-1", Kind: "opportunity", Revision: 3}}
	if candidateSetHash(a) == candidateSetHash(c) {
		t.Fatal("candidate set hash must include revisions")
	}
}

func TestAssessSinkFailureIsExplicit(t *testing.T) {
	h, _, _, _, provider, _, sink := fixture(t)
	sink.err = errors.New("disk gone")
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeUncertain {
		t.Fatalf("sink failure must fail uncertain, got %v", err)
	}
}

func TestAssessFinishFailureIsExplicit(t *testing.T) {
	h, _, _, _, provider, exchanges, _ := fixture(t)
	exchanges.finishErr = errors.New("log gone")
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeUncertain {
		t.Fatalf("exchange-log failure must fail uncertain, got %v", err)
	}
}

func TestAssessSupersedesHookChainsReassessment(t *testing.T) {
	h, _, _, _, provider, _, sink := fixture(t)
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	h.Supersedes = func(context.Context, researchcontract.AssessInput) (string, error) {
		return "jda_v1", nil
	}
	if _, err := h.Assess(context.Background(), validInput()); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 1 || sink.records[0].SupersedesID != "jda_v1" {
		t.Fatalf("hook predecessor missing: %+v", sink.records)
	}
}

func TestAssessSupersedesFailureIsExplicit(t *testing.T) {
	h, _, _, _, provider, _, _ := fixture(t)
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	h.Supersedes = func(context.Context, researchcontract.AssessInput) (string, error) {
		return "", errors.New("chain lookup gone")
	}
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeUncertain {
		t.Fatalf("hook failure must fail uncertain, got %v", err)
	}
}

func TestAssessMarksReservationDispatchedBeforeBegin(t *testing.T) {
	h, _, _, _, provider, exchanges, _ := fixture(t)
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); err != nil {
		t.Fatal(err)
	}
	if len(exchanges.marks) != 1 || exchanges.marks[0] != "run-1:round-attempt-assess-1" {
		t.Fatalf("reservation must be marked dispatched exactly once: %+v", exchanges.marks)
	}
	if len(exchanges.order) != 2 || exchanges.order[0] != "mark" || exchanges.order[1] != "begin" {
		t.Fatalf("mark must precede begin: %+v", exchanges.order)
	}
}

func TestAssessMarkFailureReleasesReservation(t *testing.T) {
	h, auth, _, _, provider, exchanges, _ := fixture(t)
	exchanges.markErr = errors.New("round stopped")
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("mark failure must fail invalid, got %v", err)
	}
	if len(auth.releases) != 1 || auth.releases[0] != "res-assess-1" {
		t.Fatalf("mark failure must release the reservation: %+v", auth.releases)
	}
	if provider.calls != 0 {
		t.Fatal("mark failure must fail before provider dispatch")
	}
	if len(exchanges.begins) != 0 {
		t.Fatal("mark failure must fail before the exchange begins")
	}
}

// bareExchanges implements ExchangeLog without Dispatcher: the handler must
// fail explicitly and release the reservation instead of leaking it.
type bareExchanges struct{}

func (bareExchanges) BeginJevAttempt(context.Context, store.JevAttemptStart) (store.JevAttempt, error) {
	return store.JevAttempt{}, errors.New("must not begin without dispatch")
}

func (bareExchanges) FinishJevAttempt(context.Context, store.JevAttemptFinish) (store.JevAttempt, error) {
	return store.JevAttempt{}, errors.New("unreachable")
}

func (bareExchanges) ReclassifyJevAttempt(context.Context, string, string, string) error {
	return errors.New("unreachable")
}

func TestAssessWithoutDispatcherReleasesReservation(t *testing.T) {
	h, auth, _, _, provider, _, _ := fixture(t)
	h.Exchanges = bareExchanges{}
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("missing dispatcher must fail invalid, got %v", err)
	}
	if len(auth.releases) != 1 {
		t.Fatalf("missing dispatcher must release the reservation: %+v", auth.releases)
	}
	if provider.calls != 0 {
		t.Fatal("missing dispatcher must fail before provider dispatch")
	}
}

func TestAssessSuccessFinishesRoundAttempt(t *testing.T) {
	h, _, _, _, provider, exchanges, _ := fixture(t)
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	got, err := h.Assess(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(exchanges.finished) != 1 {
		t.Fatalf("usable outcome must finish its round attempt exactly once: %+v", exchanges.finished)
	}
	fin := exchanges.finished[0]
	if !fin.success || fin.errorCode != "" {
		t.Fatalf("usable outcome must finish succeeded without an error code: %+v", fin)
	}
	if fin.runID != "run-1" || fin.attemptID != "round-attempt-assess-1" {
		t.Fatalf("finish must target the reservation: %+v", fin)
	}
	if fin.actor.Kind != "administrator" || fin.actor.ID != "owner" {
		t.Fatalf("finisher must audit under the run owner: %+v", fin.actor)
	}
	var answers []researchcontract.AssessAnswer
	if err := json.Unmarshal(fin.result, &answers); err != nil || len(answers) != len(got.Results) {
		t.Fatalf("finished result must carry the assessment answers: %s %v", fin.result, err)
	}
	want, _ := json.Marshal(got.Results)
	if string(fin.result) != string(want) {
		t.Fatalf("finished result must equal the returned answers: %s vs %s", fin.result, want)
	}
}

func TestAssessOutageFinishesRoundAttemptFailed(t *testing.T) {
	h, _, _, _, provider, exchanges, _ := fixture(t)
	provider.err = &jev.Error{Kind: jev.ErrTimeout, Attempts: 1}
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeUncertain {
		t.Fatalf("outage must fail outcome_uncertain, got %v", err)
	}
	if len(exchanges.finished) != 1 {
		t.Fatalf("outage must still settle its round attempt exactly once: %+v", exchanges.finished)
	}
	fin := exchanges.finished[0]
	if fin.success || fin.errorCode != "jev_timeout" || len(fin.result) != 0 {
		t.Fatalf("outage must finish failed with a safe code and no result: %+v", fin)
	}
}

func TestAssessValidationRejectionFinishesRoundAttemptFailed(t *testing.T) {
	h, _, _, _, provider, exchanges, _ := fixture(t)
	provider.result = choiceResult(map[string]string{"q-location": "invented", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("unknown answer id must fail invalid, got %v", err)
	}
	if len(exchanges.finished) != 1 {
		t.Fatalf("rejected answer must still settle its round attempt exactly once: %+v", exchanges.finished)
	}
	fin := exchanges.finished[0]
	if fin.success || fin.errorCode != "jev_invalid_response" {
		t.Fatalf("rejected answer must finish failed as invalid_response: %+v", fin)
	}
}

func TestAssessWithoutFinisherReleasesReservation(t *testing.T) {
	h, auth, _, _, provider, exchanges, _ := fixture(t)
	h.Dispatch = exchanges // dispatcher present; the bare log has no finisher
	h.Exchanges = bareExchanges{}
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("missing finisher must fail invalid, got %v", err)
	}
	if len(auth.releases) != 1 {
		t.Fatalf("missing finisher must release the reservation: %+v", auth.releases)
	}
	if provider.calls != 0 || len(exchanges.marks) != 0 {
		t.Fatal("missing finisher must fail before dispatch or provider spend")
	}
}

func TestAssessRoundOwnerFailureReleasesReservation(t *testing.T) {
	h, auth, _, _, provider, exchanges, _ := fixture(t)
	exchanges.roundErr = errors.New("round gone")
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	if _, err := h.Assess(context.Background(), validInput()); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("unresolvable run owner must fail invalid, got %v", err)
	}
	if len(auth.releases) != 1 {
		t.Fatalf("unresolvable run owner must release the reservation: %+v", auth.releases)
	}
	if provider.calls != 0 || len(exchanges.marks) != 0 {
		t.Fatal("unresolvable run owner must fail before dispatch or provider spend")
	}
}

func TestAssessRoundFinishFailureIsUncertain(t *testing.T) {
	h, _, _, _, provider, exchanges, _ := fixture(t)
	exchanges.finishRoundErr = store.ErrFenced // Stop-race shape: never a fake success
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	_, err := h.Assess(context.Background(), validInput())
	if contractCode(t, err) != researchcontract.OutcomeUncertain {
		t.Fatalf("failed round finish must fail outcome_uncertain, got %v", err)
	}
	if !errors.Is(err, store.ErrFenced) {
		t.Fatalf("failed round finish must surface the fence, got %v", err)
	}
}

func TestSafeErrorCode(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"provider kind travels verbatim":  {&jev.Error{Kind: jev.ErrTransport}, "jev_transport"},
		"invalid response keeps its kind": {&jev.Error{Kind: jev.ErrInvalidResponse}, "jev_invalid_response"},
		"allowance keeps its signal":      {store.ErrAllowance, "jev_allowance"},
		"fence keeps its signal":          {store.ErrFenced, "jev_fenced"},
		"anything else is plain failure":  {errors.New("boom"), "jev_failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := safeErrorCode(tc.err); got != tc.want {
				t.Fatalf("safeErrorCode = %q, want %q", got, tc.want)
			}
		})
	}
}

// storeAuthority reserves and releases against the real store so the
// reserve→mark→begin→finish path below runs through the real exchange log,
// the shape the T09 doubles hid.
type storeAuthority struct {
	db       *store.Store
	actor    store.Actor
	releases []string
}

func (a *storeAuthority) Check(context.Context, researchcontract.CheckInput) error { return nil }

func (a *storeAuthority) Reserve(ctx context.Context, runID, operation, key, payloadHash string) (researchcontract.Reservation, error) {
	cost, ok := store.RoundOperationCost(operation)
	if !ok {
		return researchcontract.Reservation{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "operation", "unknown operation")
	}
	attempt, _, err := a.db.ReserveRoundAttempt(ctx, a.actor, runID, store.RoundAttemptInput{
		RequestKey: key, Operation: operation, ResourceID: "source:example", Cost: cost, CursorAttemptID: payloadHash})
	if err != nil {
		return researchcontract.Reservation{}, err
	}
	return researchcontract.Reservation{ID: attempt.ID, AttemptID: attempt.ID, Operation: attempt.Operation,
		RunID: attempt.RoundID, Generation: attempt.Generation,
		IdempotencyKey: key, PayloadHash: payloadHash, ReservedAt: time.Now()}, nil
}

func (a *storeAuthority) Release(ctx context.Context, id string) error {
	a.releases = append(a.releases, id)
	return a.db.ReleaseRoundAttempt(ctx, a.actor, id)
}

func (a *storeAuthority) Usage(context.Context, string) (researchcontract.UsageLedger, error) {
	return researchcontract.UsageLedger{}, nil
}

func (a *storeAuthority) ReconciliationFor(context.Context, string) (researchcontract.Reconciliation, error) {
	return researchcontract.Reconciliation{}, researchcontract.NewError(researchcontract.OutcomeNotFound, "", "none")
}

func TestAssessRealStoreReserveMarkBeginFinish(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actor := store.Actor{Kind: "administrator", ID: "owner"}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	round, created, err := db.StartRound(ctx, actor, store.StartRoundInput{
		RequestKey: "assess-real", Intent: "Assess one role-fit question against cited captures.",
		Outcome: "process_input", ProfileVersion: prefs.Version,
		Scope: store.RoundScope{InputRefs: []string{"campaign:active"}, Resources: []string{"source:example"},
			Operations: []string{store.RoundJevAssess}},
		Limits: store.RoundAllowance{Requests: 4, Tools: 2}, Deadline: time.Now().Add(time.Hour).UTC().Round(0)})
	if err != nil || !created {
		t.Fatalf("start: %+v %v", round, err)
	}
	if round, err = db.ActivateRound(ctx, actor, round.ID); err != nil {
		t.Fatal(err)
	}
	h, _, captures, _, provider, _, sink := fixture(t)
	h.Authority = &storeAuthority{db: db, actor: actor}
	h.Exchanges = db // T23 wire shape: real store as log, dispatcher via fallback
	in := validInput()
	in.RunID = round.ID
	in.ProfileVersion = prefs.Version
	h.Briefs = &fakeBriefs{profile: prefs.Version, rubric: "rubric-v2"}
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	got, err := h.Assess(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 2 || len(sink.records) != 1 {
		t.Fatalf("real-store assessment incomplete: %+v %+v", got, sink.records)
	}
	attempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != "succeeded" {
		t.Fatalf("real exchange log must hold one succeeded attempt: %+v", attempts)
	}
	roundAttempt, err := db.RoundAttemptForRequest(ctx, round.ID, "assess-1")
	if err != nil {
		t.Fatal(err)
	}
	if roundAttempt.Operation != store.RoundJevAssess || roundAttempt.State != store.AttemptSucceeded {
		t.Fatalf("reservation must be a finished jev_assess attempt: %+v", roundAttempt)
	}
	if roundAttempt.ErrorCode != "" || !json.Valid(roundAttempt.Result) || len(roundAttempt.Result) == 0 {
		t.Fatalf("usable outcome must finish with the assessment result JSON: %+v", roundAttempt)
	}
	var finished []researchcontract.AssessAnswer
	if err := json.Unmarshal(roundAttempt.Result, &finished); err != nil || len(finished) != 2 {
		t.Fatalf("finished result must carry the two answers: %s %v", roundAttempt.Result, err)
	}
	// The finisher audits under the run owner: the same value the sink's
	// ResearchRoundActor resolves, read here through both spellings.
	var sinkActor store.Actor
	if err := db.Read(ctx, func(r store.Reader) error {
		var err error
		sinkActor, err = store.ResearchRoundActor(ctx, r, round.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if sinkActor != actor {
		t.Fatalf("run owner must match the sink actor: %+v vs %+v", actor, sinkActor)
	}
	if len(captures.opens) == 0 {
		t.Fatal("evidence must bind from captures")
	}
}

// TestAssessRealStoreProviderFailureFinishesAndResumes proves the T23
// unblock at D scope: a usable assessment and a provider outage in one run
// both settle their round attempts (succeeded + failed with a safe code),
// so Stop leaves nothing uncertain and Resume succeeds.
func TestAssessRealStoreProviderFailureFinishesAndResumes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actor := store.Actor{Kind: "administrator", ID: "owner"}
	prefs, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	round, created, err := db.StartRound(ctx, actor, store.StartRoundInput{
		RequestKey: "assess-fail-real", Intent: "Assess twice: one usable verdict, one provider outage.",
		Outcome: "process_input", ProfileVersion: prefs.Version,
		Scope: store.RoundScope{InputRefs: []string{"campaign:active"}, Resources: []string{"source:example"},
			Operations: []string{store.RoundJevAssess}},
		Limits: store.RoundAllowance{Requests: 4, Tools: 2}, Deadline: time.Now().Add(time.Hour).UTC().Round(0)})
	if err != nil || !created {
		t.Fatalf("start: %+v %v", round, err)
	}
	if round, err = db.ActivateRound(ctx, actor, round.ID); err != nil {
		t.Fatal(err)
	}
	h, _, _, _, provider, _, _ := fixture(t)
	h.Authority = &storeAuthority{db: db, actor: actor}
	h.Exchanges = db // T23 wire shape: real store as log, dispatcher/finisher via fallback
	h.Briefs = &fakeBriefs{profile: prefs.Version, rubric: "rubric-v2"}
	provider.result = choiceResult(map[string]string{"q-location": "yes", "q-seniority": "fit"})
	first := validInput()
	first.RunID, first.ProfileVersion, first.IdempotencyKey = round.ID, prefs.Version, "assess-ok-1"
	if _, err := h.Assess(ctx, first); err != nil {
		t.Fatal(err)
	}
	provider.err = &jev.Error{Kind: jev.ErrTransport, Attempts: 1}
	provider.exchange = jev.CapturedExchange{RequestBytes: provider.encoded}
	failed := validInput()
	failed.RunID, failed.ProfileVersion, failed.IdempotencyKey = round.ID, prefs.Version, "assess-out-1"
	if _, err := h.Assess(ctx, failed); contractCode(t, err) != researchcontract.OutcomeUncertain {
		t.Fatalf("outage must fail outcome_uncertain, got %v", err)
	}
	attempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("two exchanges must be logged: %+v", attempts)
	}
	okAttempt, err := db.RoundAttemptForRequest(ctx, round.ID, "assess-ok-1")
	if err != nil {
		t.Fatal(err)
	}
	if okAttempt.State != store.AttemptSucceeded || len(okAttempt.Result) == 0 {
		t.Fatalf("usable outcome must finish succeeded with result JSON: %+v", okAttempt)
	}
	outAttempt, err := db.RoundAttemptForRequest(ctx, round.ID, "assess-out-1")
	if err != nil {
		t.Fatal(err)
	}
	if outAttempt.State != store.AttemptFailed || outAttempt.ErrorCode != "jev_transport" {
		t.Fatalf("outage must finish failed with a safe code: %+v", outAttempt)
	}
	stopped, uncertain, err := db.StopRound(ctx, actor, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(uncertain) != 0 {
		t.Fatalf("settled attempts must not flip uncertain at Stop: %+v", uncertain)
	}
	if _, err := db.PauseStoppedRound(ctx, actor, round.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResumeRound(ctx, actor, round.ID, stopped.Generation); err != nil {
		t.Fatalf("resume must succeed with no unsettled attempts: %v", err)
	}
}
