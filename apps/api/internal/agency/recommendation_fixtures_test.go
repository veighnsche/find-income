package agency

import (
	"context"
	"encoding/json"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func recommendationDecisions(t *testing.T, db *store.Store, wantedCapability string) (jevservice.Service, *atomic.Int32, func()) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var wire struct {
			State struct {
				Kind    string `json:"kind"`
				Sources []struct {
					ID      string `json:"id"`
					Excerpt string `json:"excerpt"`
				} `json:"sources"`
				Candidates []struct {
					ID           string `json:"id"`
					CapabilityID string `json:"capability_id"`
				} `json:"candidates"`
			} `json:"state"`
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil || wire.State.Kind != string(jev.DecisionNextOutcome) {
			t.Errorf("invalid saved recommendation request")
			return
		}
		foundProfile, foundSelection := false, false
		for _, source := range wire.State.Sources {
			if strings.HasPrefix(source.ID, "profile:") && strings.Contains(source.Excerpt, "roleCriteria") {
				foundProfile = true
			}
			if strings.HasPrefix(source.ID, "opportunity:") && strings.Contains(source.Excerpt, "owner decision=selected") {
				foundSelection = true
			}
		}
		if !foundProfile || wantedCapability == "home_prepare" && !foundSelection {
			t.Errorf("profile or attributed owner selection missing: %+v", wire.State.Sources)
			return
		}
		chosen := "__unresolved__"
		for _, candidate := range wire.State.Candidates {
			if candidate.CapabilityID == wantedCapability {
				chosen = candidate.ID
			}
		}
		probabilities := map[string]float64{}
		for option := range wire.Questions["selected_candidate"].Criteria {
			probabilities[option] = 0
		}
		if _, ok := probabilities[chosen]; !ok {
			t.Errorf("requested capability %s absent", wantedCapability)
			return
		}
		probabilities[chosen] = 1
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"selected_candidate": map[string]any{"type": "choice", "choice": chosen, "confidence": 0.8, "probabilities": probabilities}}, "usage": map[string]any{"input_tokens": 5, "output_tokens": 1}})
	}))
	t.Setenv(jev.CredentialEnvironmentVariable, "fixture-only")
	cfg := jev.DefaultConfig()
	cfg.Enabled, cfg.Endpoint, cfg.Timeout, cfg.MaxAttempts = true, server.URL+"/v1/systemone", time.Second, 1
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return jevservice.Service{Store: db, Client: client}, calls, server.Close
}

type interruptRecommendationDecision struct {
	base    jevservice.Service
	stop    func(context.Context, string)
	trigger func(jev.DecisionInput) bool
	calls   atomic.Int32
	stopped atomic.Bool
}

func (d *interruptRecommendationDecision) RunDecision(ctx context.Context, binding jevservice.Binding, input jev.DecisionInput) (jev.DecisionResult, error) {
	if d.trigger(input) {
		d.calls.Add(1)
	}
	result, err := d.base.RunDecision(ctx, binding, input)
	if err == nil && d.trigger(input) && d.stopped.CompareAndSwap(false, true) {
		d.stop(ctx, binding.RoundID)
	}
	return result, err
}
func (d *interruptRecommendationDecision) RunScreening(ctx context.Context, binding jevservice.Binding, input jev.ScreeningInput) (jev.ScreeningResult, error) {
	return d.base.RunScreening(ctx, binding, input)
}
func (d *interruptRecommendationDecision) RunOrganisation(ctx context.Context, binding jevservice.Binding, input jev.OrganisationInput) (jev.OrganisationResult, error) {
	return d.base.RunOrganisation(ctx, binding, input)
}
