package agency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type adviceRecoveryReady struct{}

func (adviceRecoveryReady) CheckRound(context.Context, string) error { return nil }

func stoppingAdviceDecisions(t *testing.T, db *store.Store, capability string) (jevservice.Service, *atomic.Int32, func()) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var wire struct {
			State struct {
				Kind       string `json:"kind"`
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
			t.Error("invalid next-outcome request")
			return
		}
		choice := ""
		for _, candidate := range wire.State.Candidates {
			if candidate.CapabilityID == capability {
				choice = candidate.ID
			}
		}
		probabilities := map[string]float64{}
		for id := range wire.Questions["selected_candidate"].Criteria {
			probabilities[id] = 0
		}
		if choice == "" {
			t.Error("expected candidate absent")
			return
		}
		probabilities[choice] = 1
		active, err := db.ActiveRound(context.Background())
		if err == nil {
			_, _, err = db.StopRound(context.Background(), active.Actor, active.ID)
		}
		if err == nil {
			_, err = db.PauseStoppedRound(context.Background(), active.Actor, active.ID)
		}
		if err != nil {
			t.Errorf("stop during Jev response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"selected_candidate": map[string]any{"type": "choice", "choice": choice, "confidence": 0.8, "probabilities": probabilities}}, "usage": map[string]any{"input_tokens": 5, "output_tokens": 1}})
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

func resumeCapturedAdvice(t *testing.T, db *store.Store, engine *Engine, round store.Round, before store.RoundAllowance, calls *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	bridge, err := codexservice.New(ctx, db, codexservice.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	service := &rounds.Service{Store: db, Readiness: adviceRecoveryReady{}, Worker: engine, Reconciler: bridge}
	_, _ = service.Resume(ctx, round.Actor, round.ID)
	after, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := db.RoundAttemptForRequest(ctx, round.ID, homeRecommendationRequestKey+"/0")
	if err != nil || attempt.State != store.AttemptObservedSuccess || after.Used != before || calls.Load() != 1 {
		t.Fatalf("captured advice was replayed or reconciled remotely: attempt=%+v round=%+v before=%+v calls=%d err=%v", attempt, after, before, calls.Load(), err)
	}
}

func TestCapturedInputAdviceResumesLocallyWithoutAnotherCharge(t *testing.T) {
	ctx := context.Background()
	db, round := outcomeAdviceRound(t, 4, true)
	decisions, calls, closeServer := stoppingAdviceDecisions(t, db, "home_review_result")
	defer closeServer()
	engine := &Engine{Store: db, Decisions: decisions}
	_ = engine.computeOutcomeRecommendation(ctx, round, inputAdviceFacts(round))
	paused, err := db.Round(ctx, round.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("Jev response did not interrupt round: %+v %v", paused, err)
	}
	resumeCapturedAdvice(t, db, engine, paused, paused.Used, calls)
}

func TestMissingAdviceCaptureStaysPausedWithoutRemoteReconciliation(t *testing.T) {
	ctx := context.Background()
	db, round := outcomeAdviceRound(t, 2, true)
	attempt, _, err := db.ReserveRoundAttempt(ctx, round.Actor, round.ID, store.RoundAttemptInput{RequestKey: homeRecommendationRequestKey + "/0", Operation: store.RoundJevRequest, ResourceID: "campaign:active", Cost: store.RoundAllowance{Requests: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.StopRound(ctx, round.Actor, round.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := db.PauseStoppedRound(ctx, round.Actor, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := codexservice.New(ctx, db, codexservice.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	service := &rounds.Service{Store: db, Readiness: adviceRecoveryReady{}, Worker: &Engine{Store: db}, Reconciler: bridge}
	if _, err := service.Resume(ctx, round.Actor, round.ID); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("missing capture was resolved: %v", err)
	}
	after, err := db.Round(ctx, round.ID)
	if err != nil || after.State != store.RoundPaused || after.Used != paused.Used {
		t.Fatalf("missing capture spent remote reconciliation allowance: %+v before=%+v err=%v", after, paused, err)
	}
}
