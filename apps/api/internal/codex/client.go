// Package codex implements a private, bounded App Server 0.153.4 transport.
// It does not launch processes, authenticate an owner, persist runs or enable tools.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const PinnedVersion = "0.153.4"
const SchemaSHA256 = "d3eace08be5dca386bfd1f1e8df650058b4113f1e10870a284d775d75517576a"

// Transport must support one concurrent Read and Write. Close MUST promptly
// unblock both, including a blocked Write. The supervisor owns process reaping.
type Transport interface {
	io.Reader
	io.Writer
	io.Closer
}

type Options struct {
	MaxFrameBytes int
	MaxPending    int
	QueueSize     int
	Timeout       time.Duration
	ApprovalTTL   time.Duration
}

func (o Options) defaults() (Options, error) {
	if o.MaxFrameBytes == 0 {
		o.MaxFrameBytes = 1 << 20
	}
	if o.MaxPending == 0 {
		o.MaxPending = 32
	}
	if o.QueueSize == 0 {
		o.QueueSize = 64
	}
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Second
	}
	if o.ApprovalTTL == 0 {
		o.ApprovalTTL = 5 * time.Minute
	}
	if o.MaxFrameBytes < 256 || o.MaxFrameBytes > 16<<20 || o.MaxPending < 1 || o.MaxPending > 1024 || o.QueueSize < 1 || o.QueueSize > 1024 || o.Timeout < time.Millisecond || o.Timeout > 5*time.Minute || o.ApprovalTTL < time.Millisecond || o.ApprovalTTL > time.Hour {
		return o, ErrInvalidArgument
	}
	return o, nil
}

// RequestToken is opaque and cannot be transferred to another connection or
// reused after resolution, expiry, reply, or reuse of the same wire request ID.
type RequestToken struct {
	generation, sequence uint64
	id                   string
}
type ServerRequest struct {
	Token  RequestToken
	Method string
	Params json.RawMessage
}
type Event struct {
	// Exactly one of Request or Method is populated. Params are untrusted internal
	// protocol data, not dashboard DTOs and never suitable for raw SSE/log output.
	Request *ServerRequest
	Method  string
	Params  json.RawMessage
}

type reply struct {
	result json.RawMessage
	err    error
}
type outbound struct {
	ctx   context.Context
	frame []byte
	ack   chan error
	token *RequestToken
}
type pendingRequest struct {
	token                    RequestToken
	method, threadID, turnID string
	expires                  time.Time
	replying                 bool
}
type Diagnostics struct{ UnknownResponses, UnknownNotifications uint64 }

var generations atomic.Uint64

type Client struct {
	transport           Transport
	opts                Options
	generation          uint64
	mu                  sync.Mutex
	failed              error
	initializing, ready bool
	nextID, nextRequest uint64
	pending             map[string]chan reply
	requests            map[string]pendingRequest
	diagnostics         Diagnostics
	writes              chan outbound
	events              chan Event
	done                chan struct{}
	wg                  sync.WaitGroup
}

func NewClient(transport Transport, opts Options) (*Client, error) {
	if transport == nil {
		return nil, ErrUnavailable
	}
	var err error
	opts, err = opts.defaults()
	if err != nil {
		return nil, err
	}
	c := &Client{transport: transport, opts: opts, generation: generations.Add(1), pending: make(map[string]chan reply), requests: make(map[string]pendingRequest), writes: make(chan outbound, opts.QueueSize), events: make(chan Event, opts.QueueSize), done: make(chan struct{})}
	c.wg.Add(2)
	go c.readLoop()
	go c.writeLoop()
	return c, nil
}

func (c *Client) Events() <-chan Event     { return c.events }
func (c *Client) Done() <-chan struct{}    { return c.done }
func (c *Client) Generation() uint64       { return c.generation }
func (c *Client) Err() error               { c.mu.Lock(); defer c.mu.Unlock(); return c.failed }
func (c *Client) Diagnostics() Diagnostics { c.mu.Lock(); defer c.mu.Unlock(); return c.diagnostics }

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.failed != nil {
		c.mu.Unlock()
		return
	}
	c.failed = err
	c.ready = false
	clear(c.pending)
	clear(c.requests)
	close(c.done)
	c.mu.Unlock()
	// Discard transport errors: arbitrary implementations may include secrets.
	_ = c.transport.Close()
}

func (c *Client) Close() error { c.fail(ErrClosed); c.wg.Wait(); return nil }

func (c *Client) writeLoop() {
	defer c.wg.Done()
	for {
		select {
		case <-c.done:
			return
		case job := <-c.writes:
			if err := job.ctx.Err(); err != nil {
				job.ack <- err
				continue
			}
			if c.Err() != nil {
				return
			}
			if job.token != nil && !c.requestValid(*job.token) {
				job.ack <- ErrStaleRequest
				continue
			}
			timer := time.AfterFunc(c.opts.Timeout, func() { c.fail(ErrUnavailable) })
			n, err := c.transport.Write(job.frame)
			timer.Stop()
			if err != nil || n != len(job.frame) {
				c.fail(ErrUnavailable)
				return
			}
			job.ack <- nil
		}
	}
}

func (c *Client) send(ctx context.Context, message any, token *RequestToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame, err := json.Marshal(message)
	if err != nil {
		return ErrInvalidArgument
	}
	if len(frame) > c.opts.MaxFrameBytes {
		return ErrFrameTooLarge
	}
	job := outbound{ctx: ctx, frame: append(frame, '\n'), ack: make(chan error, 1), token: token}
	select {
	case <-c.done:
		return c.Err()
	default:
	}
	select {
	case c.writes <- job:
	default:
		return ErrBackpressure
	}
	return c.awaitWrite(ctx, job.ack)
}

func (c *Client) awaitWrite(ctx context.Context, ack <-chan error) error {
	select {
	case err := <-ack:
		return err
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		// select may choose cancellation even when the writer has already
		// acknowledged a complete frame. That outcome is no longer uncertain.
		select {
		case err := <-ack:
			return err
		default:
		}
		// A partially written frame cannot safely be retried or followed by another
		// request. Close the connection; the supervisor must reconcile outcomes.
		c.fail(ErrUnavailable)
		return ctx.Err()
	}
}

// call is deliberately private; no arbitrary RPC forwarding is exported.
func (c *Client) call(ctx context.Context, method string, params, result any, initializing bool) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.failed != nil {
		err := c.failed
		c.mu.Unlock()
		return err
	}
	if !initializing && !c.ready {
		c.mu.Unlock()
		return ErrNotInitialized
	}
	if len(c.pending) >= c.opts.MaxPending {
		c.mu.Unlock()
		return ErrBackpressure
	}
	c.nextID++
	id := strconv.FormatUint(c.nextID, 10)
	key := `"` + id + `"`
	response := make(chan reply, 1)
	c.pending[key] = response
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
	if err := c.send(ctx, struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{id, method, params}, nil); err != nil {
		return err
	}
	select {
	case r := <-response:
		if r.err != nil {
			return r.err
		}
		if result != nil && json.Unmarshal(r.result, result) != nil {
			c.fail(ErrMalformedFrame)
			return ErrMalformedFrame
		}
		return nil
	case <-ctx.Done():
		return ctx.Err() // Late responses are ignored, never replayed.
	case <-c.done:
		return c.Err()
	}
}

func (c *Client) readLoop() {
	defer c.wg.Done()
	defer close(c.events)
	r := bufio.NewReaderSize(c.transport, c.opts.MaxFrameBytes+1)
	for {
		frame, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull || len(frame) > c.opts.MaxFrameBytes+1 {
			c.fail(ErrFrameTooLarge)
			return
		}
		if err != nil {
			if len(frame) != 0 {
				c.fail(ErrMalformedFrame)
			} else {
				c.fail(ErrUnavailable)
			}
			return
		}
		env, err := decodeFrame(frame[:len(frame)-1])
		if err != nil {
			c.fail(err)
			return
		}
		if err = c.handle(env); err != nil {
			c.fail(err)
			return
		}
	}
}

func (c *Client) emit(event Event) error {
	select {
	case <-c.done:
		return c.Err()
	default:
	}
	select {
	case c.events <- event:
		return nil
	default:
		return ErrBackpressure
	}
}

func (c *Client) handle(env envelope) error {
	if env.method == "" {
		c.mu.Lock()
		ch := c.pending[env.id]
		delete(c.pending, env.id)
		if ch == nil {
			c.diagnostics.UnknownResponses++
		}
		c.mu.Unlock()
		if ch != nil {
			var err error
			if env.rpcError != nil {
				err = env.rpcError
			}
			ch <- reply{env.result, err}
		}
		return nil
	}
	if env.id != "" {
		return c.handleRequest(env)
	}
	if !knownNotification(env.method) {
		c.mu.Lock()
		c.diagnostics.UnknownNotifications++
		c.mu.Unlock()
		return nil
	}
	if err := c.resolveRequests(env); err != nil {
		return err
	}
	return c.emit(Event{Method: env.method, Params: env.params})
}

func knownNotification(method string) bool {
	switch method {
	case "thread/started", "thread/status/changed", "turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta", "item/mcpToolCall/progress", "account/login/completed", "account/updated", "account/rateLimits/updated", "serverRequest/resolved", "error", "mcpServer/startupStatus/updated":
		return true
	default:
		return false
	}
}

func (c *Client) handleRequest(env envelope) error {
	switch env.method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/tool/requestUserInput":
	default:
		// No grants, external-token refresh, dynamic tools or elicitation support in
		// this slice. Never silently approve an unsupported server request.
		frame, _ := json.Marshal(struct {
			ID    json.RawMessage `json:"id"`
			Error any             `json:"error"`
		}{json.RawMessage(env.id), struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{-32601, "Unsupported client operation"}})
		if len(frame) > c.opts.MaxFrameBytes {
			return ErrFrameTooLarge
		}
		job := outbound{ctx: context.Background(), frame: append(frame, '\n'), ack: make(chan error, 1)}
		select {
		case c.writes <- job:
			return nil
		default:
			return ErrBackpressure
		}
	}
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
	}
	if json.Unmarshal(env.params, &params) != nil || params.ThreadID == "" || params.TurnID == "" || params.ItemID == "" {
		return ErrMalformedFrame
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, duplicate := c.requests[env.id]; duplicate {
		return ErrMalformedFrame
	}
	for id, request := range c.requests {
		if time.Now().After(request.expires) {
			delete(c.requests, id)
		}
	}
	if len(c.requests) >= c.opts.MaxPending {
		return ErrBackpressure
	}
	c.nextRequest++
	token := RequestToken{c.generation, c.nextRequest, env.id}
	c.requests[env.id] = pendingRequest{token: token, method: env.method, threadID: params.ThreadID, turnID: params.TurnID, expires: time.Now().Add(c.opts.ApprovalTTL)}
	// Only the reader writes events; queue overflow terminates the connection.
	select {
	case c.events <- Event{Request: &ServerRequest{token, env.method, env.params}}:
		return nil
	default:
		return ErrBackpressure
	}
}

func (c *Client) resolveRequests(env envelope) error {
	if env.method != "serverRequest/resolved" && env.method != "turn/completed" && env.method != "turn/started" {
		return nil
	}
	var p struct {
		ThreadID  string          `json:"threadId"`
		RequestID json.RawMessage `json:"requestId"`
		Turn      struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(env.params, &p) != nil || p.ThreadID == "" {
		return ErrMalformedFrame
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if env.method == "serverRequest/resolved" {
		id, err := idKey(p.RequestID)
		if err != nil {
			return err
		}
		if request, ok := c.requests[id]; ok && request.threadID == p.ThreadID {
			delete(c.requests, id)
		}
	} else {
		if p.Turn.ID == "" {
			return ErrMalformedFrame
		}
		for id, request := range c.requests {
			if request.threadID == p.ThreadID && (env.method == "turn/started" || request.turnID == p.Turn.ID) {
				delete(c.requests, id)
			}
		}
	}
	return nil
}

func (c *Client) requestValid(token RequestToken) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.requests[token.id]
	return c.failed == nil && ok && r.token == token && time.Now().Before(r.expires)
}

func (c *Client) respond(ctx context.Context, token RequestToken, allowed []string, result any) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	r, ok := c.requests[token.id]
	if c.failed != nil || !ok || r.token != token || r.replying || time.Now().After(r.expires) {
		c.mu.Unlock()
		return ErrStaleRequest
	}
	matched := false
	for _, method := range allowed {
		if method == r.method {
			matched = true
		}
	}
	if !matched {
		c.mu.Unlock()
		return ErrUnsupported
	}
	r.replying = true
	c.requests[token.id] = r
	c.mu.Unlock()
	err := c.send(ctx, struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
	}{json.RawMessage(token.id), result}, &token)
	c.mu.Lock()
	if current, exists := c.requests[token.id]; exists && current.token == token {
		delete(c.requests, token.id)
	}
	c.mu.Unlock()
	// Failed/uncertain approval sends are not retryable. Invalidate connection so
	// the runtime and dashboard cannot disagree about whether permission was given.
	if err != nil && err != ErrStaleRequest {
		c.fail(ErrUnavailable)
	}
	return err
}
