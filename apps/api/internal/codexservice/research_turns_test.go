package codexservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func researchTurnInput() (store.Actor, rounds.RunnerTurnInput) {
	return store.Actor{Kind: "agent", ID: "codex-runner"}, rounds.RunnerTurnInput{
		RunID: "run-1", RequestKey: "turn-1", AttemptID: "attempt-1",
		Brief: "Investigate backend roles.", Evidence: "prior: none",
		Generation: 1, Capability: strings.Repeat("c", 64),
	}
}

func TestResearchTurnOpensFreshThread(t *testing.T) {
	svc, _ := testService(t, testConfig())
	f := installRuntime(t, svc)
	f.completeStatus = "completed"
	agent, in := researchTurnInput()
	out, err := svc.ResearchTurnRunner().RunTurn(boundedContext(t), agent, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.ThreadID == "" || out.TurnID == "" || out.Status != "completed" {
		t.Fatalf("fresh turn: %+v", out)
	}
	if f.count("thread/start") != 1 || f.count("thread/resume") != 0 || f.count("turn/start") != 1 {
		t.Fatal("fresh turn must open exactly one thread and start one turn")
	}
	var prompt struct {
		RunID, AttemptID, Capability, Brief, Evidence string
	}
	if json.Unmarshal([]byte(f.text()), &prompt) != nil {
		t.Fatalf("prompt is not JSON: %q", f.text())
	}
	if prompt.RunID != in.RunID || prompt.AttemptID != in.AttemptID ||
		prompt.Capability != in.Capability || prompt.Brief != in.Brief {
		t.Fatalf("prompt binding: %+v", prompt)
	}
	instructions := f.startedInstructions()
	for _, want := range []string{"research_memory", "research_execute", "records_save",
		"runId", "generation", "idempotencyKey", "active execution context"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions lack %q", want)
		}
	}
	for _, gone := range []string{"scoped", "saved website", "saved company", "phase", "round_context", "source_links"} {
		if strings.Contains(strings.ToLower(instructions), gone) {
			t.Fatalf("instructions keep removed phrasing %q", gone)
		}
	}
}

func TestResearchTurnResumesStoredThread(t *testing.T) {
	svc, _ := testService(t, testConfig())
	f := installRuntime(t, svc)
	f.completeStatus = "completed"
	agent, in := researchTurnInput()
	in.ThreadID = "thread-a"
	out, err := svc.ResearchTurnRunner().RunTurn(boundedContext(t), agent, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.ThreadID != "thread-a" || out.TurnID == "" || out.Status != "completed" {
		t.Fatalf("resumed turn: %+v", out)
	}
	if f.count("thread/resume") != 1 || f.count("thread/start") != 0 || f.count("turn/start") != 1 {
		t.Fatal("resumed turn must resume the stored thread without opening one")
	}
}

func TestResearchTurnResumeFailureNeverOpensFresh(t *testing.T) {
	svc, _ := testService(t, testConfig())
	f := installRuntime(t, svc)
	f.completeStatus = "completed"
	f.resumeFails = true
	agent, in := researchTurnInput()
	in.ThreadID = "thread-a"
	if _, err := svc.ResearchTurnRunner().RunTurn(boundedContext(t), agent, in); err == nil {
		t.Fatal("unresumable thread ran anyway")
	}
	if f.count("thread/resume") != 1 || f.count("thread/start") != 0 || f.count("turn/start") != 0 {
		t.Fatal("resume failure must not fall back to a fresh thread")
	}
}

func TestResearchTurnValidation(t *testing.T) {
	svc, _ := testService(t, testConfig())
	f := installRuntime(t, svc)
	f.completeStatus = "completed"
	agent, in := researchTurnInput()
	mutate := map[string]func(*rounds.RunnerTurnInput){
		"missing capability": func(in *rounds.RunnerTurnInput) { in.Capability = "" },
		"missing run":        func(in *rounds.RunnerTurnInput) { in.RunID = "" },
		"missing attempt":    func(in *rounds.RunnerTurnInput) { in.AttemptID = "" },
		"missing brief":      func(in *rounds.RunnerTurnInput) { in.Brief = "  " },
		"long brief":         func(in *rounds.RunnerTurnInput) { in.Brief = strings.Repeat("b", 12001) },
	}
	for name, change := range mutate {
		t.Run(name, func(t *testing.T) {
			candidate := in
			change(&candidate)
			if _, err := svc.ResearchTurnRunner().RunTurn(boundedContext(t), agent, candidate); !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
	if _, err := svc.ResearchTurnRunner().RunTurn(boundedContext(t),
		store.Actor{Kind: "administrator", ID: "owner"}, in); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("owner actor: %v", err)
	}
	if f.count("thread/start") != 0 || f.count("thread/resume") != 0 || f.count("turn/start") != 0 {
		t.Fatal("validation dispatched runtime calls")
	}
}

func TestResearchTurnUnavailableWithoutRuntime(t *testing.T) {
	svc, _ := testService(t, testConfig())
	svc.dial = func(context.Context, Config) (io.ReadWriteCloser, error) {
		return nil, ErrUnavailable
	}
	agent, in := researchTurnInput()
	if _, err := svc.ResearchTurnRunner().RunTurn(boundedContext(t), agent, in); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no runtime: %v", err)
	}
}

func TestResearchTurnInterruptSettlesAsError(t *testing.T) {
	svc, _ := testService(t, testConfig())
	f := installRuntime(t, svc)
	// No completeStatus: the turn stays in flight until interrupted.
	agent, in := researchTurnInput()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type interrupted struct {
		out rounds.RunnerTurnOutput
		err error
	}
	done := make(chan interrupted, 1)
	go func() {
		out, err := svc.ResearchTurnRunner().RunTurn(ctx, agent, in)
		done <- interrupted{out, err}
	}()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn never started")
	}
	cancel()
	select {
	case got := <-done:
		// Either the turn id arrived before the cancel (verified IDs
		// with an unknown outcome for the supervisor to bind and
		// settle uncertain) or it did not (canceled error, nothing to
		// preserve). Neither fabricates a terminal state.
		if got.err != nil {
			if !errors.Is(got.err, context.Canceled) {
				t.Fatalf("interrupt cause: %v", got.err)
			}
		} else {
			if got.out.ThreadID == "" || got.out.TurnID == "" || got.out.Status != "unknown" {
				t.Fatalf("interrupted turn: %+v", got.out)
			}
			if f.count("turn/interrupt") < 1 {
				t.Fatal("interrupted turn sent no interrupt RPC")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted turn never returned")
	}
	if f.count("turn/start") > 1 {
		t.Fatal("interrupted turn started more than once")
	}
}

func TestResearchTurnCanceledBeforeStart(t *testing.T) {
	svc, _ := testService(t, testConfig())
	f := installRuntime(t, svc)
	f.completeStatus = "completed"
	agent, in := researchTurnInput()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.ResearchTurnRunner().RunTurn(ctx, agent, in); err == nil {
		t.Fatal("canceled turn reported success")
	}
	if f.count("turn/start") != 0 {
		t.Fatal("canceled turn started remotely")
	}
}
