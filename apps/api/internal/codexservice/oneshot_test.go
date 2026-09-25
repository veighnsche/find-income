package codexservice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
)

// Fake app-server transport over net.Pipe, mirroring the codex package test
// pattern (its helpers are unexported there, so this file owns a local copy).

type oneShotPeer struct {
	conn   net.Conn
	reader *bufio.Reader
}

func oneShotSetup(t *testing.T) (*codex.Client, *oneShotPeer) {
	t.Helper()
	a, b := net.Pipe()
	client, err := codex.NewClient(a, codex.Options{})
	if err != nil {
		t.Fatal(err)
	}
	peer := &oneShotPeer{conn: b, reader: bufio.NewReader(b)}
	t.Cleanup(func() { b.Close(); client.Close() })
	return client, peer
}

func (p *oneShotPeer) read(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := p.reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(line, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func (p *oneShotPeer) write(t *testing.T, frame string) {
	t.Helper()
	p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(p.conn, frame+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (p *oneShotPeer) result(t *testing.T, request map[string]json.RawMessage, result string) {
	t.Helper()
	p.write(t, `{"id":`+string(request["id"])+`,"result":`+result+`}`)
}

func oneShotInitialize(t *testing.T, c *codex.Client, p *oneShotPeer) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := c.Initialize(context.Background()); done <- err }()
	r := p.read(t)
	if string(r["method"]) != `"initialize"` {
		t.Fatalf("handshake missing: %s", r["method"])
	}
	p.result(t, r, `{"userAgent":"jobseek_dashboard/0.153.4 (Linux; x86_64)","codexHome":"/runner/state","platformFamily":"unix","platformOs":"linux"}`)
	r = p.read(t)
	if string(r["method"]) != `"initialized"` || r["id"] != nil {
		t.Fatal("missing initialized notification")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initialize did not settle")
	}
}

type oneShotOutcome struct {
	result OneShotResult
	err    error
}

func runOneShotAsync(o *OneShot, prompt string) <-chan oneShotOutcome {
	ch := make(chan oneShotOutcome, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := o.Run(ctx, prompt)
		ch <- oneShotOutcome{result, err}
	}()
	return ch
}

func receiveOneShot(t *testing.T, ch <-chan oneShotOutcome) oneShotOutcome {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-time.After(6 * time.Second):
		t.Fatal("one-shot run did not settle")
		return oneShotOutcome{}
	}
}

func oneShotRunner(t *testing.T, client *codex.Client) (*OneShot, *int) {
	t.Helper()
	calls := new(int)
	return &OneShot{
		Dial:         func(context.Context) (*codex.Client, error) { *calls++; return client, nil },
		Instructions: "Draft from verified facts only.",
		Model:        "test-model",
		Effort:       "medium",
	}, calls
}

func TestOneShotCollectsAgentMessage(t *testing.T) {
	client, peer := oneShotSetup(t)
	oneShotInitialize(t, client, peer)
	shot, calls := oneShotRunner(t, client)
	done := runOneShotAsync(shot, "Draft one answer.")

	thread := peer.read(t)
	if string(thread["method"]) != `"thread/start"` {
		t.Fatalf("thread start missing: %s", thread["method"])
	}
	if !strings.Contains(string(thread["params"]), `"approvalPolicy":"never"`) {
		t.Fatalf("one-shot must forbid native approvals: %s", thread["params"])
	}
	peer.result(t, thread, `{"thread":{"id":"thread-one"}}`)
	turn := peer.read(t)
	if string(turn["method"]) != `"turn/start"` {
		t.Fatalf("turn start missing: %s", turn["method"])
	}
	if !strings.Contains(string(turn["params"]), `"threadId":"thread-one"`) ||
		!strings.Contains(string(turn["params"]), `Draft one answer.`) {
		t.Fatalf("turn prompt wrong: %s", turn["params"])
	}
	peer.result(t, turn, `{"turn":{"id":"turn-one","status":"inProgress","items":[]}}`)
	peer.write(t, `{"method":"item/completed","params":{"threadId":"thread-one","turnId":"turn-one","item":{"id":"item-a","type":"agentMessage","text":"{\"drafts\":[]}"}}}`)
	peer.write(t, `{"method":"turn/completed","params":{"threadId":"thread-one","turn":{"id":"turn-one","status":"completed"}}}`)
	out := receiveOneShot(t, done)
	if out.err != nil {
		t.Fatal(out.err)
	}
	if *calls != 1 || out.result.ThreadID != "thread-one" || out.result.TurnID != "turn-one" ||
		out.result.State != "completed" || len(out.result.Messages) != 1 ||
		out.result.Messages[0] != `{"drafts":[]}` {
		t.Fatalf("one-shot result: %+v", out.result)
	}
}

func TestOneShotIgnoresNonFinalItems(t *testing.T) {
	client, peer := oneShotSetup(t)
	oneShotInitialize(t, client, peer)
	shot, _ := oneShotRunner(t, client)
	done := runOneShotAsync(shot, "Draft.")

	thread := peer.read(t)
	peer.result(t, thread, `{"thread":{"id":"thread-i"}}`)
	turn := peer.read(t)
	peer.result(t, turn, `{"turn":{"id":"turn-i","status":"inProgress","items":[]}}`)
	// Deltas, started events, other-thread items, and non-message items
	// must never become collected output.
	peer.write(t, `{"method":"item/agentMessage/delta","params":{"threadId":"thread-i","turnId":"turn-i","itemId":"item-a","delta":"Invented draft"}}`)
	peer.write(t, `{"method":"item/started","params":{"threadId":"thread-i","turnId":"turn-i","item":{"id":"item-a","type":"agentMessage"}}}`)
	peer.write(t, `{"method":"item/completed","params":{"threadId":"thread-i","turnId":"turn-i","item":{"id":"item-b","type":"commandExecution"}}}`)
	peer.write(t, `{"method":"item/completed","params":{"threadId":"other","turnId":"other","item":{"id":"item-x","type":"agentMessage","text":"Wrong turn"}}}`)
	peer.write(t, `{"method":"item/completed","params":{"threadId":"thread-i","turnId":"turn-i","item":{"id":"item-a","type":"agentMessage","text":"Final text"}}}`)
	peer.write(t, `{"method":"turn/completed","params":{"threadId":"thread-i","turn":{"id":"turn-i","status":"completed"}}}`)
	out := receiveOneShot(t, done)
	if out.err != nil {
		t.Fatal(out.err)
	}
	if len(out.result.Messages) != 1 || out.result.Messages[0] != "Final text" {
		t.Fatalf("collected: %+v", out.result.Messages)
	}
}

func TestOneShotBounds(t *testing.T) {
	t.Run("prompt budget", func(t *testing.T) {
		shot := &OneShot{Dial: func(context.Context) (*codex.Client, error) {
			t.Fatal("dial must not run for an over-budget prompt")
			return nil, nil
		}, Instructions: "i", Model: "m", Effort: "e", MaxPromptBytes: 8}
		if _, err := shot.Run(context.Background(), "too long!"); !errors.Is(err, codex.ErrInvalidArgument) {
			t.Fatalf("over-budget prompt: %v", err)
		}
		if _, err := shot.Run(context.Background(), "   "); !errors.Is(err, codex.ErrInvalidArgument) {
			t.Fatalf("blank prompt: %v", err)
		}
	})
	t.Run("output budget", func(t *testing.T) {
		client, peer := oneShotSetup(t)
		oneShotInitialize(t, client, peer)
		shot, _ := oneShotRunner(t, client)
		shot.MaxOutputBytes = 4
		done := runOneShotAsync(shot, "Draft.")
		thread := peer.read(t)
		peer.result(t, thread, `{"thread":{"id":"thread-o"}}`)
		turn := peer.read(t)
		peer.result(t, turn, `{"turn":{"id":"turn-o","status":"inProgress","items":[]}}`)
		peer.write(t, `{"method":"item/completed","params":{"threadId":"thread-o","turnId":"turn-o","item":{"id":"item-a","type":"agentMessage","text":"way too long"}}}`)
		peer.write(t, `{"method":"turn/completed","params":{"threadId":"thread-o","turn":{"id":"turn-o","status":"completed"}}}`)
		out := receiveOneShot(t, done)
		if out.err == nil || out.result.Messages != nil {
			t.Fatalf("overflow accepted: %+v", out.result)
		}
	})
	t.Run("message count", func(t *testing.T) {
		client, peer := oneShotSetup(t)
		oneShotInitialize(t, client, peer)
		shot, _ := oneShotRunner(t, client)
		shot.MaxMessages = 1
		done := runOneShotAsync(shot, "Draft.")
		thread := peer.read(t)
		peer.result(t, thread, `{"thread":{"id":"thread-c"}}`)
		turn := peer.read(t)
		peer.result(t, turn, `{"turn":{"id":"turn-c","status":"inProgress","items":[]}}`)
		peer.write(t, `{"method":"item/completed","params":{"threadId":"thread-c","turnId":"turn-c","item":{"id":"item-a","type":"agentMessage","text":"one"}}}`)
		peer.write(t, `{"method":"item/completed","params":{"threadId":"thread-c","turnId":"turn-c","item":{"id":"item-b","type":"agentMessage","text":"two"}}}`)
		peer.write(t, `{"method":"turn/completed","params":{"threadId":"thread-c","turn":{"id":"turn-c","status":"completed"}}}`)
		out := receiveOneShot(t, done)
		if out.err == nil || out.result.Messages != nil {
			t.Fatalf("count overflow accepted: %+v", out.result)
		}
	})
	t.Run("silent completion", func(t *testing.T) {
		client, peer := oneShotSetup(t)
		oneShotInitialize(t, client, peer)
		shot, _ := oneShotRunner(t, client)
		done := runOneShotAsync(shot, "Draft.")
		thread := peer.read(t)
		peer.result(t, thread, `{"thread":{"id":"thread-s"}}`)
		turn := peer.read(t)
		peer.result(t, turn, `{"turn":{"id":"turn-s","status":"inProgress","items":[]}}`)
		peer.write(t, `{"method":"turn/completed","params":{"threadId":"thread-s","turn":{"id":"turn-s","status":"completed"}}}`)
		out := receiveOneShot(t, done)
		if out.err == nil {
			t.Fatal("silent completion accepted")
		}
	})
	t.Run("failed turn", func(t *testing.T) {
		client, peer := oneShotSetup(t)
		oneShotInitialize(t, client, peer)
		shot, _ := oneShotRunner(t, client)
		done := runOneShotAsync(shot, "Draft.")
		thread := peer.read(t)
		peer.result(t, thread, `{"thread":{"id":"thread-f"}}`)
		turn := peer.read(t)
		peer.result(t, turn, `{"turn":{"id":"turn-f","status":"inProgress","items":[]}}`)
		peer.write(t, `{"method":"item/completed","params":{"threadId":"thread-f","turnId":"turn-f","item":{"id":"item-a","type":"agentMessage","text":"Partial"}}}`)
		peer.write(t, `{"method":"turn/completed","params":{"threadId":"thread-f","turn":{"id":"turn-f","status":"failed"}}}`)
		out := receiveOneShot(t, done)
		if out.err == nil || out.result.Messages != nil || out.result.State != "failed" {
			t.Fatalf("failed turn accepted: %+v %v", out.result, out.err)
		}
	})
}

func TestOneShotDialing(t *testing.T) {
	t.Run("nil dial", func(t *testing.T) {
		shot := &OneShot{Instructions: "i", Model: "m", Effort: "e"}
		if _, err := shot.Run(context.Background(), "Draft."); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("nil dial: %v", err)
		}
	})
	t.Run("dial failure", func(t *testing.T) {
		want := errors.New("synthetic dial failure")
		shot := &OneShot{Dial: func(context.Context) (*codex.Client, error) { return nil, want },
			Instructions: "i", Model: "m", Effort: "e"}
		if _, err := shot.Run(context.Background(), "Draft."); !errors.Is(err, want) {
			t.Fatalf("dial failure: %v", err)
		}
	})
	t.Run("missing instructions", func(t *testing.T) {
		client, peer := oneShotSetup(t)
		oneShotInitialize(t, client, peer)
		shot, _ := oneShotRunner(t, client)
		shot.Instructions = ""
		done := runOneShotAsync(shot, "Draft.")
		out := receiveOneShot(t, done)
		if !errors.Is(out.err, codex.ErrInvalidArgument) {
			t.Fatalf("missing instructions: %v", out.err)
		}
	})
	t.Run("environment dial without runner", func(t *testing.T) {
		for _, key := range []string{"JOBSEEK_CODEX_SSH_HOST", "JOBSEEK_CODEX_SSH_USER",
			"JOBSEEK_CODEX_SSH_IDENTITY_FILE", "JOBSEEK_CODEX_SSH_KNOWN_HOSTS",
			"JOBSEEK_CODEX_REMOTE_LAUNCHER", "JOBSEEK_CODEX_LOCAL_RUNNER"} {
			t.Setenv(key, "")
		}
		if _, err := DialOneShot(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unconfigured dial: %v", err)
		}
	})
}
