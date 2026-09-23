package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type peer struct {
	net.Conn
	reader *bufio.Reader
}

func setup(t *testing.T, opts Options) (*Client, *peer) {
	t.Helper()
	a, b := net.Pipe()
	c, err := NewClient(a, opts)
	if err != nil {
		t.Fatal(err)
	}
	p := &peer{b, bufio.NewReader(b)}
	t.Cleanup(func() { b.Close(); c.Close() })
	return c, p
}
func (p *peer) read(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	p.SetReadDeadline(time.Now().Add(2 * time.Second))
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
func (p *peer) write(t *testing.T, frame string) {
	t.Helper()
	p.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(p.Conn, frame+"\n"); err != nil {
		t.Fatal(err)
	}
}
func (p *peer) result(t *testing.T, request map[string]json.RawMessage, result string) {
	t.Helper()
	p.write(t, `{"id":`+string(request["id"])+`,"result":`+result+`}`)
}
func callAsync(f func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- f() }()
	return ch
}
func receive(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("call did not settle")
		return nil
	}
}
func event(t *testing.T, c *Client) Event {
	t.Helper()
	select {
	case e, ok := <-c.Events():
		if !ok {
			t.Fatalf("events closed: %v", c.Err())
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("missing event")
		return Event{}
	}
}
func waitFailed(t *testing.T, c *Client, want error) {
	t.Helper()
	select {
	case <-c.Done():
		if !errors.Is(c.Err(), want) {
			t.Fatalf("got %v, want %v", c.Err(), want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connection did not fail")
	}
}
func initialize(t *testing.T, c *Client, p *peer) {
	t.Helper()
	ch := callAsync(func() error { _, err := c.Initialize(context.Background()); return err })
	r := p.read(t)
	if string(r["method"]) != `"initialize"` || strings.Contains(string(r["params"]), `"experimentalApi":true`) {
		t.Fatalf("unexpected handshake: %s", r["method"])
	}
	p.result(t, r, `{"userAgent":"jobseek_dashboard/0.153.4 (Linux; x86_64)","codexHome":"/runner/state","platformFamily":"unix","platformOs":"linux"}`)
	r = p.read(t)
	if string(r["method"]) != `"initialized"` || r["id"] != nil {
		t.Fatal("missing initialized notification")
	}
	if err := receive(t, ch); err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeAndUnavailable(t *testing.T) {
	if _, err := NewClient(nil, Options{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	c, p := setup(t, Options{})
	if _, err := c.ReadAccount(context.Background()); !errors.Is(err, ErrNotInitialized) {
		t.Fatal(err)
	}
	initialize(t, c, p)
	if _, err := c.Initialize(context.Background()); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatal(err)
	}
	if err := c.ListThreadItems(context.Background(), "thread"); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := c.StartLogin(context.Background(), LoginMode("apiKey")); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadAccount(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestWrongVersionFailsHandshake(t *testing.T) {
	c, p := setup(t, Options{})
	ch := callAsync(func() error { _, err := c.Initialize(context.Background()); return err })
	r := p.read(t)
	p.result(t, r, `{"userAgent":"jobseek_dashboard/999.0.0 (Linux)","codexHome":"/state","platformFamily":"unix","platformOs":"linux"}`)
	if err := receive(t, ch); !errors.Is(err, ErrIncompatible) {
		t.Fatal(err)
	}
	waitFailed(t, c, ErrIncompatible)
}

func TestOutOfOrderResponsesAndIDTypes(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	var account AccountState
	var limits RateLimits
	a := callAsync(func() error { var err error; account, err = c.ReadAccount(context.Background()); return err })
	ar := p.read(t)
	b := callAsync(func() error { var err error; limits, err = c.ReadRateLimits(context.Background()); return err })
	br := p.read(t)
	// Same characters, different JSON type: must not satisfy the string ID.
	p.write(t, `{"id":`+strings.Trim(string(ar["id"]), `"`)+`,"result":{"requiresOpenaiAuth":false}}`)
	p.write(t, `{"id":"never-issued","result":{"secret":"do-not-log"}}`)
	p.result(t, br, `{"rateLimits":{"limitId":"codex","primary":null,"secondary":null}}`)
	if err := receive(t, b); err != nil {
		t.Fatal(err)
	}
	p.result(t, ar, `{"account":null,"requiresOpenaiAuth":true}`)
	if err := receive(t, a); err != nil {
		t.Fatal(err)
	}
	if account.RequiresOpenAIAuth == nil || !*account.RequiresOpenAIAuth || limits.RateLimits.Primary != nil {
		t.Fatal("response correlation or missing-quota handling failed")
	}
	if c.Diagnostics().UnknownResponses != 2 {
		t.Fatal(c.Diagnostics())
	}
	// Duplicate completed response is ignored, not applied twice.
	p.result(t, ar, `{"account":null,"requiresOpenaiAuth":true}`)
	barrier := callAsync(func() error { return c.Logout(context.Background()) })
	r := p.read(t)
	p.result(t, r, `{}`)
	if err := receive(t, barrier); err != nil {
		t.Fatal(err)
	}
	if c.Diagnostics().UnknownResponses != 3 {
		t.Fatal(c.Diagnostics())
	}
}

func TestObserveTurnRequiresExactSupportedHistory(t *testing.T) {
	for _, test := range []struct {
		name, page, want string
	}{
		{"terminal", `{"data":[{"id":"other","status":"completed"},{"id":"turn-a","status":"interrupted"}]}`, "interrupted"},
		{"still_running", `{"data":[{"id":"turn-a","status":"inProgress"}]}`, "inProgress"},
		{"missing", `{"data":[{"id":"other","status":"completed"}]}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, p := setup(t, Options{})
			initialize(t, c, p)
			var observed string
			done := callAsync(func() error {
				var err error
				observed, err = c.ObserveTurn(context.Background(), "thread-a", "turn-a")
				return err
			})
			read := p.read(t)
			if string(read["method"]) != `"thread/read"` {
				t.Fatal("history identity read missing")
			}
			p.result(t, read, `{"thread":{"id":"thread-a"}}`)
			list := p.read(t)
			if string(list["method"]) != `"thread/turns/list"` {
				t.Fatal("supported turn list missing")
			}
			p.result(t, list, test.page)
			err := receive(t, done)
			if test.want == "" {
				if !errors.Is(err, ErrHistoryIncomplete) {
					t.Fatal(err)
				}
			} else if err != nil || observed != test.want {
				t.Fatal(observed, err)
			}
		})
	}
	if _, err := (&Client{}).ObserveTurn(context.Background(), "", "turn-a"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal(err)
	}
}

func TestTimeoutCancellationAndLateReply(t *testing.T) {
	c, p := setup(t, Options{Timeout: time.Second})
	initialize(t, c, p)
	ctx, cancel := context.WithCancel(context.Background())
	a := callAsync(func() error { _, err := c.ReadAccount(ctx); return err })
	r := p.read(t)
	// Reading a subsequent outgoing frame proves the serial writer has sent
	// the first acknowledgement. An incoming notification only synchronizes
	// the independent reader and cannot serve as this barrier.
	barrier := callAsync(func() error { return c.Logout(context.Background()) })
	br := p.read(t)
	p.result(t, br, `{}`)
	if err := receive(t, barrier); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := receive(t, a); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p.result(t, r, `{"account":null,"requiresOpenaiAuth":true}`)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	b := callAsync(func() error { _, err := c.ReadAccount(ctx2); return err })
	p.read(t)
	if err := receive(t, b); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if c.Err() != nil {
		t.Fatalf("completed writes should keep transport alive: %v", c.Err())
	}
	if c.Diagnostics().UnknownResponses != 1 {
		t.Fatal(c.Diagnostics())
	}
}

func TestAcknowledgedWriteWinsConcurrentCancellation(t *testing.T) {
	c, _ := setup(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Both select cases are ready: the completed write must always win, even
	// if cancellation is the branch initially selected by the scheduler.
	for range 100 {
		ack := make(chan error, 1)
		ack <- nil
		if err := c.awaitWrite(ctx, ack); err != nil {
			t.Fatal(err)
		}
		if c.Err() != nil {
			t.Fatalf("acknowledged write closed connection: %v", c.Err())
		}
	}
}

func TestCancellationDuringBlockedWriteClosesTransport(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := callAsync(func() error { _, err := c.ReadAccount(ctx); return err })
	// Reading only one byte proves Write has started and cannot have completed:
	// net.Pipe blocks the writer until the peer consumes the rest of the frame.
	p.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := p.Conn.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := receive(t, ch); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitFailed(t, c, ErrUnavailable)
	c.Close() // would hang if the writer were leaked
}

func TestPendingLimitAndEOFFailAllCalls(t *testing.T) {
	c, p := setup(t, Options{MaxPending: 2})
	initialize(t, c, p)
	a := callAsync(func() error { _, err := c.ReadAccount(context.Background()); return err })
	p.read(t)
	b := callAsync(func() error { _, err := c.ReadRateLimits(context.Background()); return err })
	p.read(t)
	if _, err := c.ReadAccount(context.Background()); !errors.Is(err, ErrBackpressure) {
		t.Fatal(err)
	}
	p.Close()
	for _, ch := range []<-chan error{a, b} {
		if err := receive(t, ch); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) != 0 {
		t.Fatal("pending calls retained")
	}
}

func TestMalformedFrames(t *testing.T) {
	for _, frame := range []string{
		`no-json`, `[]`, `{}`, `{"id":null,"result":{}}`, `{"id":1.0,"result":{}}`,
		`{"id":"1","id":"2","result":{}}`, `{"id":"1","result":{},"error":{"code":1,"message":"x"}}`,
		`{"method":"x","result":{}}`, `{"method":1}`, `{"id":"1","error":{"code":"bad","message":"secret"}}`,
		`{"id":"1","error":null}`, `{"method":"x"} {"method":"y"}`,
		`{"method":"serverRequest/resolved","params":{"threadId":"t","requestId":null}}`,
	} {
		t.Run(frame, func(t *testing.T) {
			c, p := setup(t, Options{})
			p.write(t, frame)
			waitFailed(t, c, ErrMalformedFrame)
		})
	}
}

func TestFrameLimitAndTruncatedEOF(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		c, p := setup(t, Options{MaxFrameBytes: 256})
		io.WriteString(p.Conn, strings.Repeat("x", 257))
		waitFailed(t, c, ErrFrameTooLarge)
	})
	t.Run("truncated", func(t *testing.T) {
		c, p := setup(t, Options{})
		io.WriteString(p.Conn, `{"id":"1","result":{}}`)
		p.Close()
		waitFailed(t, c, ErrMalformedFrame)
	})
}

func TestNotificationBackpressure(t *testing.T) {
	c, p := setup(t, Options{QueueSize: 1})
	initialize(t, c, p)
	p.write(t, `{"method":"future/unknown","params":{"secret":"not logged"}}`)
	p.write(t, `{"method":"account/updated","params":{"authMode":null}}`)
	p.write(t, `{"method":"account/updated","params":{"authMode":null}}`)
	waitFailed(t, c, ErrBackpressure)
	if c.Diagnostics().UnknownNotifications != 1 {
		t.Fatal(c.Diagnostics())
	}
}

func approval(id string) string {
	return `{"id":` + id + `,"method":"item/commandExecution/requestApproval","params":{"threadId":"t","turnId":"turn","itemId":"item","startedAtMs":1,"command":"synthetic"}}`
}

func TestApprovalIdentityResolutionAndReuse(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	p.write(t, approval(`9007199254740993`))
	first := event(t, c).Request
	p.write(t, `{"method":"serverRequest/resolved","params":{"threadId":"t","requestId":9007199254740993}}`)
	event(t, c)
	if err := c.ReplyApproval(context.Background(), first.Token, AcceptOnce); !errors.Is(err, ErrStaleRequest) {
		t.Fatal(err)
	}
	p.write(t, approval(`9007199254740993`))
	second := event(t, c).Request
	if err := c.ReplyApproval(context.Background(), first.Token, AcceptOnce); !errors.Is(err, ErrStaleRequest) {
		t.Fatal(err)
	}
	ch := callAsync(func() error { return c.ReplyApproval(context.Background(), second.Token, Decline) })
	r := p.read(t)
	if string(r["id"]) != `9007199254740993` || string(r["result"]) != `{"decision":"decline"}` {
		t.Fatal("approval precision/decision corrupted")
	}
	if err := receive(t, ch); err != nil {
		t.Fatal(err)
	}
	if err := c.ReplyApproval(context.Background(), second.Token, AcceptOnce); !errors.Is(err, ErrStaleRequest) {
		t.Fatal(err)
	}
}

func TestApprovalCannotCrossConnection(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	p.write(t, approval(`"1"`))
	old := event(t, c).Request
	d, q := setup(t, Options{})
	initialize(t, d, q)
	q.write(t, approval(`"1"`))
	current := event(t, d).Request
	if err := d.ReplyApproval(context.Background(), old.Token, AcceptOnce); !errors.Is(err, ErrStaleRequest) {
		t.Fatal(err)
	}
	if old.Token == current.Token || c.Generation() == d.Generation() {
		t.Fatal("generation reused")
	}
	c.Close()
	if err := c.ReplyApproval(context.Background(), old.Token, AcceptOnce); !errors.Is(err, ErrStaleRequest) {
		t.Fatal(err)
	}
}

func TestApprovalExpiryAndTurnCompletion(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		c, p := setup(t, Options{ApprovalTTL: time.Millisecond})
		initialize(t, c, p)
		p.write(t, approval(`"x"`))
		a := event(t, c).Request
		time.Sleep(3 * time.Millisecond)
		if err := c.ReplyApproval(context.Background(), a.Token, AcceptOnce); !errors.Is(err, ErrStaleRequest) {
			t.Fatal(err)
		}
	})
	for _, method := range []string{"turn/completed", "turn/started"} {
		t.Run(method, func(t *testing.T) {
			c, p := setup(t, Options{})
			initialize(t, c, p)
			p.write(t, approval(`"x"`))
			a := event(t, c).Request
			p.write(t, `{"method":"`+method+`","params":{"threadId":"t","turn":{"id":"turn","status":"interrupted"}}}`)
			event(t, c)
			if err := c.ReplyApproval(context.Background(), a.Token, AcceptOnce); !errors.Is(err, ErrStaleRequest) {
				t.Fatal(err)
			}
		})
	}
}

func TestServerRequestCapacityAndDuplicate(t *testing.T) {
	t.Run("capacity", func(t *testing.T) {
		c, p := setup(t, Options{MaxPending: 1})
		initialize(t, c, p)
		p.write(t, approval(`1`))
		event(t, c)
		p.write(t, approval(`2`))
		waitFailed(t, c, ErrBackpressure)
	})
	t.Run("duplicate", func(t *testing.T) {
		c, p := setup(t, Options{})
		initialize(t, c, p)
		p.write(t, approval(`1`))
		event(t, c)
		p.write(t, approval(`1`))
		waitFailed(t, c, ErrMalformedFrame)
	})
}

func TestUnsupportedServerRequestAndSafeRPCError(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	p.write(t, `{"id":"server-id","method":"account/chatgptAuthTokens/refresh","params":{"secret":"no"}}`)
	r := p.read(t)
	if string(r["id"]) != `"server-id"` || string(r["error"]) != `{"code":-32601,"message":"Unsupported client operation"}` {
		t.Fatal("unsupported request not rejected")
	}
	ch := callAsync(func() error { _, err := c.ReadRateLimits(context.Background()); return err })
	r = p.read(t)
	p.write(t, `{"id":`+string(r["id"])+`,"error":{"code":-32601,"message":"SENTINEL_PRIVATE_TOKEN","data":{"secret":"SENTINEL_PRIVATE_TOKEN"}}}`)
	err := receive(t, ch)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	for _, s := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(s, "SENTINEL") {
			t.Fatal("upstream error payload leaked")
		}
	}
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Code != -32601 {
		t.Fatal("lost safe error code")
	}
}

func TestWriteQueueIsBounded(t *testing.T) {
	c, p := setup(t, Options{QueueSize: 1})
	initialize(t, c, p)
	// Hold the sole writer on a frame whose bytes the peer intentionally ignores.
	first := callAsync(func() error { return c.Logout(context.Background()) })
	// Synchronize on a partial read: writer is now blocked finishing this frame.
	one := make([]byte, 1)
	if _, err := p.Conn.Read(one); err != nil {
		t.Fatal(err)
	}
	c.writes <- outbound{ctx: context.Background(), frame: []byte("{}\n"), ack: make(chan error, 1)}
	if err := c.Logout(context.Background()); !errors.Is(err, ErrBackpressure) {
		t.Fatal(err)
	}
	c.Close()
	if err := receive(t, first); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestOutboundFrameBound(t *testing.T) {
	c, p := setup(t, Options{MaxFrameBytes: 512})
	initialize(t, c, p)
	if err := c.CancelLogin(context.Background(), strings.Repeat("x", 1024)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal(err)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}

func TestUserInputReplyAndWrongResponseKind(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	p.write(t, `{"id":"q","method":"item/tool/requestUserInput","params":{"threadId":"t","turnId":"turn","itemId":"item","isBlocking":true,"questions":[{"id":"q1","header":"Choice","question":"Which?"}]}}`)
	a := event(t, c).Request
	if err := c.ReplyApproval(context.Background(), a.Token, AcceptOnce); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	ch := callAsync(func() error {
		return c.ReplyUserInput(context.Background(), a.Token, map[string]Answer{"q1": {Answers: []string{"synthetic"}}})
	})
	r := p.read(t)
	if string(r["result"]) != `{"answers":{"q1":{"answers":["synthetic"]}}}` {
		t.Fatal("incorrect answer shape")
	}
	if err := receive(t, ch); err != nil {
		t.Fatal(err)
	}
}

func TestTypedMethodsUsePinnedWireShapes(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	cases := []struct {
		name           string
		invoke         func() error
		params, result string
	}{
		{"device login", func() error {
			r, e := c.StartLogin(context.Background(), DeviceLogin)
			if e == nil && r.LoginID != "attempt" {
				return errors.New("wrong login")
			}
			return e
		}, `{"type":"chatgptDeviceCode"}`, `{"type":"chatgptDeviceCode","loginId":"attempt","verificationUrl":"https://auth.openai.com/codex/device","userCode":"SYNTHETIC"}`},
		{"browser login", func() error { _, e := c.StartLogin(context.Background(), BrowserLogin); return e }, `{"type":"chatgpt"}`, `{"type":"chatgpt","loginId":"attempt","authUrl":"https://chatgpt.com/synthetic"}`},
		{"cancel", func() error { return c.CancelLogin(context.Background(), "attempt") }, `{"loginId":"attempt"}`, `{}`},
		{"models", func() error { _, e := c.ListModels(context.Background(), nil); return e }, `{"limit":100}`, `{"data":[],"nextCursor":null}`},
		{"MCP", func() error {
			r, e := c.ListMCPServers(context.Background(), nil, nil)
			if e == nil && len(r.Data) != 1 {
				return errors.New("missing server")
			}
			return e
		}, `{"limit":100,"detail":"toolsAndAuthOnly"}`, `{"data":[{"name":"jobseek","authStatus":"unsupported","tools":{}}],"nextCursor":null}`},
		{"read thread", func() error { _, e := c.ReadThread(context.Background(), "t"); return e }, `{"threadId":"t","includeTurns":true}`, `{"thread":{"id":"t","historyMode":"legacy","turns":[]}}`},
		{"turns", func() error { _, e := c.ListTurns(context.Background(), "t", nil); return e }, `{"threadId":"t","limit":20,"itemsView":"full"}`, `{"data":[],"nextCursor":null}`},
		{"interrupt", func() error { return c.Interrupt(context.Background(), "t", "turn") }, `{"threadId":"t","turnId":"turn"}`, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := callAsync(tc.invoke)
			r := p.read(t)
			if string(r["params"]) != tc.params {
				t.Fatalf("unexpected params: %s", r["params"])
			}
			p.result(t, r, tc.result)
			if err := receive(t, ch); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMalformedTypedResponseFailsClosed(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	ch := callAsync(func() error { _, err := c.ReadAccount(context.Background()); return err })
	r := p.read(t)
	p.result(t, r, `{"account":null}`)
	if err := receive(t, ch); !errors.Is(err, ErrMalformedFrame) {
		t.Fatal(err)
	}
	waitFailed(t, c, ErrMalformedFrame)
}

type brokenTransport struct{ cause string }

func (b brokenTransport) Read([]byte) (int, error)  { return 0, errors.New(b.cause) }
func (b brokenTransport) Write([]byte) (int, error) { return 0, errors.New(b.cause) }
func (b brokenTransport) Close() error              { return errors.New(b.cause) }
func TestTransportErrorsDoNotExposePayload(t *testing.T) {
	c, err := NewClient(brokenTransport{"PRIVATE_ERROR_VALUE"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitFailed(t, c, ErrUnavailable)
	if strings.Contains(fmt.Sprintf("%+v", c.Err()), "PRIVATE") {
		t.Fatal("transport payload leaked")
	}
}

func TestQueuedApprovalResolvedBeforeWrite(t *testing.T) {
	c, p := setup(t, Options{})
	initialize(t, c, p)
	blocked := callAsync(func() error { return c.Logout(context.Background()) })
	one := make([]byte, 1)
	if _, err := p.Conn.Read(one); err != nil {
		t.Fatal(err)
	}
	p.write(t, approval(`"queued"`))
	a := event(t, c).Request
	response := callAsync(func() error { return c.ReplyApproval(context.Background(), a.Token, AcceptOnce) })
	// Wait for the reply to enter the bounded writer queue, without sleeps.
	deadline := time.Now().Add(time.Second)
	for len(c.writes) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(c.writes) == 0 {
		t.Fatal("approval not queued")
	}
	p.write(t, `{"method":"serverRequest/resolved","params":{"threadId":"t","requestId":"queued"}}`)
	event(t, c)
	// Complete the blocked frame; no approval frame may follow it.
	line, err := p.reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]json.RawMessage
	if json.Unmarshal(append(one, line...), &r) != nil {
		t.Fatal("bad partial frame")
	}
	p.result(t, r, `{}`)
	if err := receive(t, blocked); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, response); !errors.Is(err, ErrStaleRequest) {
		t.Fatal(err)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}
