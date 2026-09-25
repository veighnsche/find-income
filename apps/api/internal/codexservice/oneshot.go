package codexservice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
)

// Bounded one-shot Codex turns (D4). A OneShot runs a single text-in /
// text-out turn on a fresh connection and returns the settled outcome plus
// the accumulated agent message texts. It is the only Codex entry point
// for grounded drafting (D3) and explicit rewrite (D4): both pass through
// this primitive, so rewrite can never happen implicitly.
//
// Trust boundary: collected message text is untrusted model prose. Callers
// must validate it against pinned bytes (scope, exact citations) before it
// becomes pack content. The turn itself cannot touch anything else: threads
// start with approvalPolicy=never, the controller rejects every native
// request, no tools are configured, and no transcript, shell, filesystem,
// or network capability exists on this path.

const (
	// DefaultOneShotMaxPromptBytes bounds one turn prompt. Career sources
	// and saved answers are assembled by the caller; overflow fails the
	// turn instead of truncating verified facts silently.
	DefaultOneShotMaxPromptBytes = 65536
	// DefaultOneShotMaxOutputBytes bounds the collected agent texts.
	DefaultOneShotMaxOutputBytes = 24576
	// DefaultOneShotMaxMessages bounds the number of collected messages.
	DefaultOneShotMaxMessages = 4
)

// OneShotResult is one settled turn. Messages holds the completed
// agentMessage item texts in arrival order; it is nil unless the turn
// completed within bounds.
type OneShotResult struct {
	ThreadID string
	TurnID   string
	State    string
	Code     string
	Messages []string
}

// OneShot runs single bounded turns. Dial opens a fresh initialized
// connection per run; production uses DialOneShot. Instructions, Model, and
// Effort are trusted application values, never user input. Zero bounds
// select the defaults. Each run owns its connection, so concurrent runs do
// not share turn state; callers that need serialization own it.
type OneShot struct {
	Dial                           func(ctx context.Context) (*codex.Client, error)
	Instructions, Model, Effort    string
	MaxPromptBytes, MaxOutputBytes int
	MaxMessages                    int
}

// DialOneShot opens one initialized app-server connection from the process
// environment, mirroring service construction: SSH-isolated dial by
// default, explicit local runner only when configured. It performs no tool
// check: one-shot turns use no tools.
func DialOneShot(ctx context.Context) (*codex.Client, error) {
	if ctx == nil {
		return nil, codex.ErrInvalidArgument
	}
	cfg := configFromEnvironment()
	if reason := cfg.unavailableCode(); reason != "" {
		return nil, ErrUnavailable
	}
	dial := dialSSH
	if cfg.Local() {
		dial = dialLocal
	}
	transport, err := dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	client, err := codex.NewClient(transport, codex.Options{})
	if err != nil {
		transport.Close()
		return nil, err
	}
	if _, err := client.Initialize(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// oneShotCollector keeps completed agentMessage texts within bounds. It
// runs on the controller event pump and must stay non-blocking; overflow is
// recorded and reported after the turn settles. Items are attributed to the
// settled outcome after the run: filtering inside the hook would race the
// turn-ID binding, so every completed agent message is recorded with its
// ids and only this run's thread/turn is returned.
type oneShotCollector struct {
	mu         sync.Mutex
	items      []oneShotItem
	bytes      int
	maxBytes   int
	maxCount   int
	overflowed bool
}

type oneShotItem struct {
	thread, turn, text string
}

func (c *oneShotCollector) observe(event codex.ItemEvent) {
	if event.State != "completed" || event.ItemType != "agentMessage" {
		return
	}
	var params struct {
		Item struct {
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(event.Raw, &params) != nil || params.Item.Text == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.maxCount || c.bytes+len(params.Item.Text) > c.maxBytes {
		c.overflowed = true
		return
	}
	c.items = append(c.items, oneShotItem{thread: event.ThreadID, turn: event.TurnID, text: params.Item.Text})
	c.bytes += len(params.Item.Text)
}

func (c *oneShotCollector) result(thread, turn string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, item := range c.items {
		if item.thread == thread && item.turn == turn {
			out = append(out, item.text)
		}
	}
	return out, c.overflowed
}

// Run executes one bounded turn and collects its agent texts. It fails when
// the prompt is blank or over budget, when the turn does not complete, when
// output exceeds budget, or when a completed turn carries no agent message.
func (o *OneShot) Run(ctx context.Context, prompt string) (OneShotResult, error) {
	var out OneShotResult
	maxPrompt, maxOutput, maxMessages := o.MaxPromptBytes, o.MaxOutputBytes, o.MaxMessages
	if maxPrompt <= 0 {
		maxPrompt = DefaultOneShotMaxPromptBytes
	}
	if maxOutput <= 0 {
		maxOutput = DefaultOneShotMaxOutputBytes
	}
	if maxMessages <= 0 {
		maxMessages = DefaultOneShotMaxMessages
	}
	if ctx == nil || strings.TrimSpace(prompt) == "" {
		return out, codex.ErrInvalidArgument
	}
	if len(prompt) > maxPrompt {
		return out, fmt.Errorf("%w: one-shot prompt exceeds %d bytes", codex.ErrInvalidArgument, maxPrompt)
	}
	if o == nil || o.Dial == nil {
		return out, ErrUnavailable
	}
	client, err := o.Dial(ctx)
	if err != nil {
		return out, err
	}
	defer client.Close()
	controller, err := codex.NewTurnController(client, o.Instructions, o.Model, o.Effort)
	if err != nil {
		return out, err
	}
	collector := &oneShotCollector{maxBytes: maxOutput, maxCount: maxMessages}
	hooks := codex.TurnHooks{
		BindThread: func(context.Context, string) error { return nil },
		BindTurn:   func(context.Context, string, string) error { return nil },
		OnItem:     collector.observe,
	}
	outcome, err := controller.Run(ctx, prompt, hooks)
	out.ThreadID, out.TurnID, out.State, out.Code = outcome.ThreadID, outcome.TurnID, outcome.State, outcome.Code
	if err != nil {
		return out, err
	}
	messages, overflowed := collector.result(outcome.ThreadID, outcome.TurnID)
	if overflowed {
		return out, fmt.Errorf("codex one-shot output exceeds %d bytes in %d messages", maxOutput, maxMessages)
	}
	if outcome.State != "completed" {
		return out, fmt.Errorf("codex one-shot turn %s: %s", outcome.State, outcome.Code)
	}
	if len(messages) == 0 {
		return out, fmt.Errorf("codex one-shot turn completed without an agent message")
	}
	out.Messages = messages
	return out, nil
}
