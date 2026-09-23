package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type turnResult struct {
	out TurnOutcome
	err error
}

func turnSetup(t *testing.T) (*Client, *peer, *TurnController) {
	t.Helper()
	c, p := setup(t, Options{})
	initialize(t, c, p)
	controller, err := NewTurnController(c, "Use scoped tools only", "test-model", "medium")
	if err != nil {
		t.Fatal(err)
	}
	return c, p, controller
}
func turnHooks() TurnHooks {
	return TurnHooks{BindThread: func(context.Context, string) error { return nil }, BindTurn: func(context.Context, string, string) error { return nil }}
}
func launchTurn(c *TurnController, ctx context.Context, hooks TurnHooks) <-chan turnResult {
	ch := make(chan turnResult, 1)
	go func() { out, err := c.Run(ctx, "Synthetic evidence", hooks); ch <- turnResult{out, err} }()
	return ch
}
func receiveTurn(t *testing.T, ch <-chan turnResult) turnResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not settle")
		return turnResult{}
	}
}
func replyThread(t *testing.T, p *peer) {
	t.Helper()
	r := p.read(t)
	if string(r["method"]) != `"thread/start"` {
		t.Fatal("thread start missing")
	}
	p.result(t, r, `{"thread":{"id":"thread-a"}}`)
}
func replyTurn(t *testing.T, p *peer) map[string]json.RawMessage {
	t.Helper()
	r := p.read(t)
	if string(r["method"]) != `"turn/start"` {
		t.Fatal("turn start missing")
	}
	p.result(t, r, `{"turn":{"id":"turn-a","status":"inProgress","items":[]}}`)
	return r
}

func TestTurnControllerEarlyTerminalAndExactIDs(t *testing.T) {
	_, p, c := turnSetup(t)
	var bound []string
	hooks := TurnHooks{BindThread: func(_ context.Context, id string) error { bound = append(bound, id); return nil }, BindTurn: func(_ context.Context, thread, turn string) error { bound = append(bound, thread+"/"+turn); return nil }}
	done := launchTurn(c, context.Background(), hooks)
	replyThread(t, p)
	r := p.read(t)
	if string(r["method"]) != `"turn/start"` {
		t.Fatal("missing turn")
	}
	p.write(t, `{"method":"turn/completed","params":{"threadId":"other","turn":{"id":"other","status":"completed"}}}`)
	p.write(t, `{"method":"item/agentMessage/delta","params":{"delta":"Invented saved result"}}`)
	p.write(t, `{"method":"turn/completed","params":{"threadId":"thread-a","turn":{"id":"turn-a","status":"completed"}}}`)
	p.result(t, r, `{"turn":{"id":"turn-a","status":"inProgress","items":[]}}`)
	result := receiveTurn(t, done)
	if result.err != nil || result.out.State != "completed" || len(bound) != 2 || bound[0] != "thread-a" || bound[1] != "thread-a/turn-a" {
		t.Fatalf("outcome %+v bound %v", result, bound)
	}
}
func TestTurnControllerFailedIDWriteStopsBeforeTurn(t *testing.T) {
	c, p, controller := turnSetup(t)
	hooks := turnHooks()
	hooks.BindThread = func(context.Context, string) error { return errors.New("synthetic write failure") }
	done := launchTurn(controller, context.Background(), hooks)
	replyThread(t, p)
	result := receiveTurn(t, done)
	if !errors.Is(result.err, ErrTurnPersistence) || result.out.TurnID != "" || c.Err() == nil {
		t.Fatalf("outcome %+v", result)
	}
}
func TestTurnControllerCancellationInterruptsAndCloses(t *testing.T) {
	c, p, controller := turnSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bound := make(chan struct{})
	hooks := turnHooks()
	hooks.BindTurn = func(context.Context, string, string) error { close(bound); return nil }
	done := launchTurn(controller, ctx, hooks)
	replyThread(t, p)
	replyTurn(t, p)
	select {
	case <-bound:
	case <-time.After(time.Second):
		t.Fatal("turn ID not bound")
	}
	cancel()
	r := p.read(t)
	if string(r["method"]) != `"turn/interrupt"` {
		t.Fatal("missing interrupt")
	}
	p.result(t, r, `{}`)
	result := receiveTurn(t, done)
	if !errors.Is(result.err, context.Canceled) || result.out.State != "uncertain" || c.Err() == nil {
		t.Fatalf("outcome %+v", result)
	}
}
