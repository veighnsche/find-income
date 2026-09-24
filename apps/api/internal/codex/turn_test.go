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

func TestThreadStartLeavesNamedProfileActive(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	done := make(chan error, 1)
	go func() {
		_, err := c.StartThread(context.Background(), "Synthetic guarded turn", "test-model")
		done <- err
	}()
	r := p.read(t)
	if string(r["method"]) != `"thread/start"` || string(r["params"]) != `{"developerInstructions":"Synthetic guarded turn","model":"test-model","approvalPolicy":"never"}` {
		t.Fatalf("thread start overrides named profile or native approvals: %s", r["params"])
	}
	p.result(t, r, `{"thread":{"id":"thread-a"}}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
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

func TestTurnControllerRunResumedSkipsThreadStart(t *testing.T) {
	_, p, controller := turnSetup(t)
	hooks := turnHooks()
	var bound string
	hooks.BindTurn = func(_ context.Context, thread, turn string) error { bound = thread + "/" + turn; return nil }
	done := make(chan turnResult, 1)
	go func() {
		out, err := controller.RunResumed(context.Background(), "thread-saved", "Synthetic evidence", hooks)
		done <- turnResult{out, err}
	}()
	r := p.read(t)
	if string(r["method"]) != `"turn/start"` {
		t.Fatalf("resumed run must start a turn directly: %s", r["method"])
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(r["params"], &params); err != nil || string(params["threadId"]) != `"thread-saved"` {
		t.Fatalf("resumed run used the wrong thread: %s", r["params"])
	}
	p.result(t, r, `{"turn":{"id":"turn-b","status":"inProgress","items":[]}}`)
	p.write(t, `{"method":"turn/completed","params":{"threadId":"thread-saved","turn":{"id":"turn-b","status":"completed"}}}`)
	result := receiveTurn(t, done)
	if result.err != nil || result.out.State != "completed" || result.out.ThreadID != "thread-saved" || result.out.TurnID != "turn-b" || bound != "thread-saved/turn-b" {
		t.Fatalf("outcome %+v bound %q", result, bound)
	}
	if _, err := controller.RunResumed(context.Background(), "", "x", hooks); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("empty thread ID accepted")
	}
	hooks.BindTurn = nil
	if _, err := controller.RunResumed(context.Background(), "thread-saved", "x", hooks); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("missing BindTurn accepted")
	}
}

func TestTurnControllerObservesItemActivity(t *testing.T) {
	_, p, controller := turnSetup(t)
	hooks := turnHooks()
	items := make(chan ItemEvent, 8)
	hooks.OnItem = func(e ItemEvent) { items <- e }
	done := launchTurn(controller, context.Background(), hooks)
	replyThread(t, p)
	replyTurn(t, p)
	p.write(t, `{"method":"item/started","params":{"threadId":"thread-a","turnId":"turn-a","item":{"id":"item-a","type":"userMessage"}}}`)
	p.write(t, `{"method":"item/agentMessage/delta","params":{"threadId":"thread-a","turnId":"turn-a","itemId":"item-a","delta":"dropped"}}`)
	p.write(t, `{"method":"item/completed","params":{"threadId":"thread-a","turnId":"turn-a","item":{"id":"item-a","type":"userMessage"}}}`)
	p.write(t, `{"method":"turn/completed","params":{"threadId":"thread-a","turn":{"id":"turn-a","status":"completed"}}}`)
	result := receiveTurn(t, done)
	if result.err != nil || result.out.State != "completed" {
		t.Fatalf("outcome %+v", result)
	}
	var got []ItemEvent
	for len(items) > 0 {
		got = append(got, <-items)
	}
	if len(got) != 2 || got[0].State != "started" || got[1].State != "completed" || got[0].ItemID != "item-a" || got[0].ItemType != "userMessage" || got[0].ThreadID != "thread-a" || got[0].TurnID != "turn-a" {
		t.Fatalf("item activity lost: %+v", got)
	}
}

func TestTurnControllerMalformedItemFailsClosed(t *testing.T) {
	c, p, controller := turnSetup(t)
	done := launchTurn(controller, context.Background(), turnHooks())
	replyThread(t, p)
	replyTurn(t, p)
	p.write(t, `{"method":"item/started","params":{"threadId":"thread-a"}}`)
	result := receiveTurn(t, done)
	if !errors.Is(result.err, ErrMalformedFrame) || result.out.State != "uncertain" || c.Err() == nil {
		t.Fatalf("malformed item accepted: %+v", result)
	}
}

func TestTurnControllerRejectsNativeInputWithoutOwnerChat(t *testing.T) {
	c, p, controller := turnSetup(t)
	done := launchTurn(controller, context.Background(), turnHooks())
	replyThread(t, p)
	replyTurn(t, p)
	p.write(t, `{"id":"native-question","method":"item/tool/requestUserInput","params":{"threadId":"thread-a","turnId":"turn-a","itemId":"item-a","questions":[{"id":"q1","question":"Synthetic prompt"}]}}`)
	rejection := p.read(t)
	if string(rejection["id"]) != `"native-question"` || string(rejection["error"]) != `{"code":-32601,"message":"Native request unavailable"}` {
		t.Fatalf("native input was not denied: %s", rejection)
	}
	interrupt := p.read(t)
	if string(interrupt["method"]) != `"turn/interrupt"` {
		t.Fatalf("rejected turn was not interrupted: %s", interrupt)
	}
	p.result(t, interrupt, `{}`)
	result := receiveTurn(t, done)
	if !errors.Is(result.err, ErrUnsupported) || result.out.State != "uncertain" || result.out.Code != "native_request_rejected" || c.Err() == nil {
		t.Fatalf("native input gained owner authority: %+v", result)
	}
}
