package jevservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func serviceRound(t *testing.T, requestLimit int64) (*store.Store, Binding) {
	t.Helper()
	return serviceRoundUntil(t, requestLimit, time.Now().Add(time.Hour).UTC().Round(0))
}

func serviceRoundUntil(t *testing.T, requestLimit int64, deadline time.Time) (*store.Store, Binding) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{Kind: "administrator", ID: "jev-fixture-owner"}
	r, created, err := s.StartRound(ctx, actor, store.StartRoundInput{RequestKey: "jev-fixture-round", Intent: "Find bounded sourced opportunities.",
		Outcome: "discover", ProfileVersion: p.Version, Scope: store.RoundScope{InputRefs: []string{"campaign:fixture"},
			Resources: []string{"source:fixture"}, Operations: []string{store.RoundJevRequest}},
		Limits: store.RoundAllowance{Requests: requestLimit}, Deadline: deadline})
	if err != nil || !created {
		t.Fatalf("start: %+v %v", r, err)
	}
	r, err = s.ActivateRound(ctx, actor, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, Binding{Actor: actor, RoundID: r.ID, ResourceID: "source:fixture", RequestKeyPrefix: "jev-fixture", ProfileVersion: p.Version}
}

func serviceClient(t *testing.T, server *httptest.Server) *jev.Client {
	t.Helper()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-key")
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = server.URL + "/v1/systemone"
	cfg.Timeout = time.Second
	cfg.MaxAttempts = 2 // Round service must still send only once per reservation.
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func serviceDecision() jev.DecisionInput {
	return jev.DecisionInput{Kind: jev.DecisionSourceResearch, CampaignIntent: "Find supported platform work.", MaxReportedTokens: 100,
		Capabilities:       []jev.DecisionCapability{{ID: "fetch", Description: "Fetch one public source."}},
		Sources:            []jev.DecisionSource{{ID: "source-1", SourceRevision: "rev-2", SourceKind: "employer_page", Excerpt: "Exact relevant employer text."}},
		RemainingAllowance: []jev.DecisionAllowance{{Operation: "fetch", Remaining: 1}},
		Candidates:         []jev.DecisionCandidate{{ID: "candidate-a", Description: "Fetch this source.", Scope: "One supported source", CapabilityID: "fetch", SourceIDs: []string{"source-1"}}}}
}

const validDecisionBody = `{"model":"jev-1.13.0","answers":{"selected_candidate":{"type":"choice","choice":"candidate-a","probabilities":{"candidate-a":0.9,"__unresolved__":0.1},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`

func TestRunDecisionRecordsSuccessAndOneCharge(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(validDecisionBody))
	}))
	defer server.Close()
	s, binding := serviceRound(t, 2)
	defer s.Close()
	service := Service{Store: s, Client: serviceClient(t, server)}
	got, err := service.RunDecision(context.Background(), binding, serviceDecision())
	if err != nil || got.SelectedID != "candidate-a" || calls != 1 {
		t.Fatalf("decision: %#v %v calls=%d", got, err, calls)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts: %#v %v", attempts, err)
	}
	a := attempts[0]
	if a.Status != "succeeded" || !bytes.Equal(a.RawResponseBytes, []byte(validDecisionBody)) || a.InputTokens == nil || *a.InputTokens != 10 || a.OutputTokens == nil || *a.OutputTokens != 2 || len(a.TransportRequestBytes) == 0 || a.ResponseTruncated || a.ResponseReadError {
		t.Fatalf("attempt evidence: %#v", a)
	}
	var transport struct {
		Model     string          `json:"model"`
		State     json.RawMessage `json:"state"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(a.TransportRequestBytes, &transport); err != nil || transport.Model != "jev-1.13.0" || len(transport.State) == 0 || len(transport.Questions) == 0 {
		t.Fatalf("transport request lost: %#v %v", transport, err)
	}
	round, err := s.Round(context.Background(), binding.RoundID)
	if err != nil || round.Used.Requests != 1 {
		t.Fatalf("round charge: %+v %v", round, err)
	}
}

func TestRunDecisionPersistsInvalidFailedAndOverBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		budget int64
		want   string
		kind   jev.ErrorKind
		usage  bool
	}{
		{"invalid", 200, `{"model":"jev-1.13.0","answers":{"selected_candidate":{"type":"choice","choice":"invented","probabilities":{"candidate-a":0.9,"__unresolved__":0.1},"confidence":0.8}},"usage":{"input_tokens":3,"output_tokens":0}}`, 100, "invalid_response", jev.ErrInvalidResponse, true},
		{"failed", 503, `{"error":"provider unavailable"}`, 100, "failed", jev.ErrUnavailable, false},
		{"over_budget", 200, validDecisionBody, 1, "budget_exceeded", jev.ErrBudgetExceeded, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			s, binding := serviceRound(t, 2)
			defer s.Close()
			input := serviceDecision()
			input.MaxReportedTokens = tc.budget
			_, err := (Service{Store: s, Client: serviceClient(t, server)}).RunDecision(context.Background(), binding, input)
			if !errors.Is(err, &jev.Error{Kind: tc.kind}) || calls != 1 {
				t.Fatalf("got %v calls=%d", err, calls)
			}
			attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("attempts: %#v %v", attempts, err)
			}
			a := attempts[0]
			if a.Status != tc.want || !bytes.Equal(a.RawResponseBytes, []byte(tc.body)) || (a.InputTokens != nil) != tc.usage || a.HTTPStatus == nil || *a.HTTPStatus != tc.status {
				t.Fatalf("lost failed evidence: %#v", a)
			}
			if tc.name == "invalid" && (a.OutputTokens == nil || *a.OutputTokens != 0) {
				t.Fatalf("zero usage became missing: %#v", a)
			}
		})
	}
}

func TestRunScreeningChargesEachDependentProviderCall(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var wire struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Errorf("request: %v", err)
			return
		}
		id, selected := "scope_0", jev.ScopePresent
		if calls == 2 {
			id, selected = "support_0", "span_0"
		}
		options, ok := wire.Questions[id]
		if !ok {
			t.Errorf("wrong staged question: %#v", wire.Questions)
			return
		}
		probabilities := make(map[string]float64, len(options.Criteria))
		for key := range options.Criteria {
			probabilities[key] = 0
		}
		probabilities[selected] = 1
		body, _ := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": map[string]any{id: map[string]any{
			"type": "choice", "choice": selected, "probabilities": probabilities, "confidence": 0.8}},
			"usage": map[string]int{"input_tokens": 5, "output_tokens": 1}})
		_, _ = w.Write(body)
	}))
	defer server.Close()
	s, binding := serviceRound(t, 2)
	defer s.Close()
	input := jev.ScreeningInput{PreferenceVersion: binding.ProfileVersion, MaxTotalTokens: 100,
		Criteria: []jev.ScreeningCriterion{{ID: "role-1", Label: "Platform work", Description: "Primary platform engineering responsibility.", Kind: "role", Mode: "require"}},
		Spans:    []jev.ScreeningSpan{{ID: "a", SourceID: "source-1", SourceRevision: "rev-2", SourceKind: "vacancy_page", Excerpt: "The role owns platform engineering."}}}
	result, err := (Service{Store: s, Client: serviceClient(t, server)}).RunScreening(context.Background(), binding, input)
	if err != nil || calls != 2 || len(result.Observations) != 1 || result.Observations[0].SupportState != jev.SupportProposed {
		t.Fatalf("staged screening: %#v %v calls=%d", result, err, calls)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 2 || attempts[0].RoundAttemptID == attempts[1].RoundAttemptID || attempts[0].Status != "succeeded" || attempts[1].Status != "succeeded" {
		t.Fatalf("dependent calls reused a charge: %#v %v", attempts, err)
	}
	round, err := s.Round(context.Background(), binding.RoundID)
	if err != nil || round.Used.Requests != 2 {
		t.Fatalf("charges: %+v %v", round, err)
	}
}

func TestRunOrganisationUsesChargedSnapshot(t *testing.T) {
	body := `{"model":"jev-1.13.0","answers":{"organisation_category":{"type":"choice","choice":"research","probabilities":{"research":0.9,"__uncertain__":0.1},"confidence":0.8}},"usage":{"input_tokens":4,"output_tokens":1}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	s, binding := serviceRound(t, 1)
	defer s.Close()
	input := jev.OrganisationInput{CategorySetVersion: 1, Categories: []jev.OrganisationCategory{{ID: "research", Description: "Research role."}},
		Facts: []jev.OrganisationFact{{ID: "fact-1", SourceID: "source-1", SourceRevision: "rev-2", SourceKind: "vacancy_page", Excerpt: "Research interviews are the main work."}}}
	result, err := (Service{Store: s, Client: serviceClient(t, server)}).RunOrganisation(context.Background(), binding, input)
	if err != nil || result.CategoryID != "research" {
		t.Fatalf("organisation: %#v %v", result, err)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 1 || attempts[0].Purpose != "organisation" || !bytes.Equal(attempts[0].RawResponseBytes, []byte(body)) {
		t.Fatalf("organisation evidence: %#v %v", attempts, err)
	}
}

type stoppingCaptureClient struct {
	store   *store.Store
	binding Binding
	cancel  context.CancelFunc
}

func (c stoppingCaptureClient) RequestedModel() string { return "jev-1.13.0" }
func (c stoppingCaptureClient) EncodedRequest(jev.Request) ([]byte, error) {
	return []byte(`{"model":"jev-1.13.0","state":{},"questions":{}}`), nil
}
func (c stoppingCaptureClient) EvaluateOnceCaptured(_ context.Context, request jev.Request) (jev.Result, jev.CapturedExchange, error) {
	if _, _, err := c.store.StopRound(context.Background(), c.binding.Actor, c.binding.RoundID); err != nil {
		return jev.Result{}, jev.CapturedExchange{}, err
	}
	c.cancel()
	in, out := int64(4), int64(0)
	answer := jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{Choice: "candidate-a",
		Probabilities: map[string]float64{"candidate-a": 1, "__unresolved__": 0}, Confidence: 0.8}}
	result := jev.Result{RequestedModel: "jev-1.13.0", ReturnedModel: "jev-1.13.0",
		Answers: map[string]jev.Answer{"selected_candidate": answer}, Usage: jev.Usage{InputTokens: in, OutputTokens: out}}
	return result, jev.CapturedExchange{RequestBytes: []byte(`{"model":"jev-1.13.0","state":{},"questions":{}}`), ResponseBytes: []byte(validDecisionBody), HTTPStatus: 200,
		ReturnedModel: "jev-1.13.0", InputTokens: &in, OutputTokens: &out}, nil
}

func TestRunDecisionPreservesLateResponseWithoutApplying(t *testing.T) {
	s, binding := serviceRound(t, 1)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := stoppingCaptureClient{store: s, binding: binding, cancel: cancel}
	result, err := (Service{Store: s, Client: client}).RunDecision(ctx, binding, serviceDecision())
	if err == nil || result.SelectedID != "" {
		t.Fatalf("stopped decision applied: %#v %v", result, err)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 1 || !bytes.Equal(attempts[0].RawResponseBytes, []byte(validDecisionBody)) ||
		attempts[0].InputTokens == nil || *attempts[0].InputTokens != 4 || attempts[0].OutputTokens == nil || *attempts[0].OutputTokens != 0 {
		t.Fatalf("late evidence lost: %#v %v", attempts, err)
	}
	results, err := s.RoundResults(context.Background(), binding.RoundID)
	if err != nil || len(results) != 0 {
		t.Fatalf("late response became round result: %#v %v", results, err)
	}
}

type deadlineCaptureClient struct {
	stoppingCaptureClient
	observedDeadline time.Time
}

func (c *deadlineCaptureClient) EvaluateOnceCaptured(ctx context.Context, request jev.Request) (jev.Result, jev.CapturedExchange, error) {
	c.observedDeadline, _ = ctx.Deadline()
	<-ctx.Done()
	body, _ := c.EncodedRequest(request)
	return jev.Result{}, jev.CapturedExchange{RequestBytes: body}, ctx.Err()
}

func TestRunDecisionEnforcesSavedRoundDeadline(t *testing.T) {
	deadline := time.Now().Add(500 * time.Millisecond).UTC().Round(0)
	s, binding := serviceRoundUntil(t, 1, deadline)
	defer s.Close()
	client := &deadlineCaptureClient{}
	// A caller may have a longer timeout, but cannot extend this commission.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := (Service{Store: s, Client: client}).RunDecision(ctx, binding, serviceDecision())
	if err == nil || result.SelectedID != "" || !client.observedDeadline.Equal(deadline) {
		t.Fatalf("deadline not enforced: deadline=%s want=%s result=%#v err=%v", client.observedDeadline, deadline, result, err)
	}
	attempts, err := s.JevAttemptsForRound(context.Background(), binding.RoundID)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "failed" {
		t.Fatalf("deadline evidence lost: %#v %v", attempts, err)
	}
	round, err := s.Round(context.Background(), binding.RoundID)
	if err != nil || round.State != store.RoundFailed || round.StopReason != "deadline_reached" {
		t.Fatalf("expired commission retained the active slot: %#v %v", round, err)
	}
	results, err := s.RoundResults(context.Background(), binding.RoundID)
	if err != nil || len(results) != 0 {
		t.Fatalf("expired response applied: %#v %v", results, err)
	}
}
