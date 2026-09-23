package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrTurnBusy = errors.New("codex turn already active")
var ErrTurnPersistence = errors.New("codex turn identifier persistence failed")

type TurnOutcome struct{ State, Code, ThreadID, TurnID string }
type TurnHooks struct {
	BindThread func(context.Context, string) error
	BindTurn   func(context.Context, string, string) error
}

// TurnController owns the event stream for one previously persisted dispatch.
// Its result is protocol evidence; it never accepts model prose as a result.
type TurnController struct {
	client                      *Client
	instructions, model, effort string
	mu                          sync.Mutex
}

func NewTurnController(client *Client, instructions, model, effort string) (*TurnController, error) {
	if client == nil || strings.TrimSpace(instructions) == "" || strings.TrimSpace(model) == "" || strings.TrimSpace(effort) == "" {
		return nil, ErrInvalidArgument
	}
	return &TurnController{client: client, instructions: instructions, model: model, effort: effort}, nil
}

type turnSignal struct {
	threadID, turnID, status string
	attention                bool
}

func (c *TurnController) Run(ctx context.Context, text string, hooks TurnHooks) (out TurnOutcome, err error) {
	if ctx == nil || strings.TrimSpace(text) == "" || hooks.BindThread == nil || hooks.BindTurn == nil {
		return out, ErrInvalidArgument
	}
	if !c.mu.TryLock() {
		return out, ErrTurnBusy
	}
	defer c.mu.Unlock()
	out.State, out.Code = "uncertain", "dispatch_uncertain"
	signals, pumpErr := make(chan turnSignal, 16), make(chan error, 1)
	stop, pumpDone := make(chan struct{}), make(chan struct{})
	go c.pump(stop, pumpDone, signals, pumpErr)
	defer func() { close(stop); <-pumpDone }()
	settled := false
	defer func() {
		if !settled {
			c.stopTurn(out.ThreadID, out.TurnID)
		}
	}()
	thread, err := c.client.StartThread(ctx, c.instructions, c.model)
	if err != nil {
		return out, err
	}
	out.ThreadID = thread.ID
	if hooks.BindThread(ctx, out.ThreadID) != nil {
		return out, ErrTurnPersistence
	}
	turn, err := c.client.StartTurn(ctx, out.ThreadID, text, c.effort)
	if err != nil {
		return out, err
	}
	out.TurnID = turn.ID
	if hooks.BindTurn(ctx, out.ThreadID, out.TurnID) != nil {
		return out, ErrTurnPersistence
	}
	if turn.Status != "inProgress" {
		out.State, out.Code, settled = turn.Status, "turn_"+turn.Status, true
		return out, nil
	}
	for {
		select {
		case <-ctx.Done():
			out.Code = "interrupted_outcome_unconfirmed"
			return out, ctx.Err()
		case <-c.client.Done():
			out.Code = "runtime_disconnected"
			return out, c.client.Err()
		case e := <-pumpErr:
			out.Code = "invalid_runtime_event"
			return out, e
		case signal := <-signals:
			if signal.threadID != out.ThreadID || signal.turnID != out.TurnID {
				continue
			}
			if signal.attention {
				out.State, out.Code = "needs_attention", "runtime_request_requires_owner"
				return out, nil
			}
			out.State, out.Code, settled = signal.status, "turn_"+signal.status, true
			return out, nil
		}
	}
}
func (c *TurnController) pump(stop <-chan struct{}, done chan<- struct{}, signals chan<- turnSignal, failures chan<- error) {
	defer close(done)
	fail := func(err error) { failures <- err; c.client.fail(err) }
	for {
		select {
		case <-stop:
			return
		case <-c.client.Done():
			return
		case event, ok := <-c.client.Events():
			if !ok {
				return
			}
			var signal turnSignal
			if event.Request != nil {
				var p struct {
					ThreadID string `json:"threadId"`
					TurnID   string `json:"turnId"`
				}
				if json.Unmarshal(event.Request.Params, &p) != nil || p.ThreadID == "" || p.TurnID == "" {
					fail(ErrMalformedFrame)
					return
				}
				signal = turnSignal{threadID: p.ThreadID, turnID: p.TurnID, attention: true}
			} else if event.Method == "turn/completed" {
				var p struct {
					ThreadID string `json:"threadId"`
					Turn     Turn   `json:"turn"`
				}
				if json.Unmarshal(event.Params, &p) != nil || p.ThreadID == "" || p.Turn.ID == "" || !validTurnStatus(p.Turn.Status) || p.Turn.Status == "inProgress" {
					fail(ErrMalformedFrame)
					return
				}
				signal = turnSignal{threadID: p.ThreadID, turnID: p.Turn.ID, status: p.Turn.Status}
			} else {
				continue
			}
			select {
			case signals <- signal:
			case <-stop:
				return
			default:
				fail(ErrBackpressure)
				return
			}
		}
	}
}
func (c *TurnController) stopTurn(threadID, turnID string) {
	if threadID != "" && turnID != "" && c.client.Err() == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.client.Interrupt(ctx, threadID, turnID)
		cancel()
	}
	_ = c.client.Close()
}
