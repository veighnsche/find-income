package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrIntakeBusy         = errors.New("codex intake already active")
	ErrIntakePersistence  = errors.New("codex intake persistence failed")
	ErrIntakeDisconnected = errors.New("codex intake needs ChatGPT connection")
)

// IntakeOutcome describes runtime outcome, not an assertion that a model's
// answer was saved. OpportunityIDs come only from trusted database readback.
type IntakeOutcome struct {
	State          string
	Code           string
	ThreadID       string
	TurnID         string
	OpportunityIDs []string
}

// IntakeHooks are supplied by the application for one claimed ingestion job.
// Every write must fence that claim. RecordDispatch("", "") records intent
// before any runtime creation, rejects duplicate dispatch, and does not retry
// an uncertain previous attempt. Later calls add the returned identifiers.
// ReadSaved returns only persisted source-backed records linked to this intake.
// Finish records the runtime outcome while preserving any partial saved result.
type IntakeHooks struct {
	Ready          func(context.Context) error
	RecordDispatch func(context.Context, string, string) error
	ReadSaved      func(context.Context) ([]string, error)
	Finish         func(context.Context, IntakeOutcome) error
}

// IntakeController exclusively consumes this client's events while Run is
// active. The application uses one controller for its single active intake.
// It does not launch a runtime or establish isolation. Ready MUST check the
// approved isolated runner and required scoped bridge, not merely socket health.
// Account/login UI and the durable queue remain outside this package.
type IntakeController struct {
	client       *Client
	instructions string
	mu           sync.Mutex
}

func NewIntakeController(client *Client, instructions string) (*IntakeController, error) {
	if client == nil || strings.TrimSpace(instructions) == "" {
		return nil, ErrInvalidArgument
	}
	return &IntakeController{client: client, instructions: instructions}, nil
}

type intakeSignal struct {
	threadID  string
	turnID    string
	status    string
	attention bool
}

// Run accepts prepared source/profile text from the trusted ingestion adapter.
// Collectors and URL/paste submissions use the same method. The caller owns
// the job lease and renews it while running. Lease loss must cancel ctx.
func (c *IntakeController) Run(ctx context.Context, text string, hooks IntakeHooks) (out IntakeOutcome, err error) {
	if ctx == nil || strings.TrimSpace(text) == "" || hooks.Ready == nil ||
		hooks.RecordDispatch == nil || hooks.ReadSaved == nil || hooks.Finish == nil {
		return out, ErrInvalidArgument
	}
	if !c.mu.TryLock() {
		return out, ErrIntakeBusy
	}
	defer c.mu.Unlock()
	if err := hooks.Ready(ctx); err != nil {
		return out, ErrUnavailable
	}
	account, err := c.client.ReadAccount(ctx)
	if err != nil {
		return out, err
	}
	if account.Account == nil || account.Account.Type != "chatgpt" {
		return out, ErrIntakeDisconnected
	}
	if err := hooks.RecordDispatch(ctx, "", ""); err != nil {
		return out, ErrIntakePersistence
	}
	out.State, out.Code = "uncertain", "dispatch_uncertain"
	// Persistence must still get a bounded opportunity after caller cancellation.
	defer func() {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		ids, readErr := hooks.ReadSaved(finishCtx)
		if readErr == nil {
			out.OpportunityIDs = ids
		}
		if readErr != nil {
			out.State, out.Code = "uncertain", "result_readback_failed"
		} else if out.State == "completed" && len(ids) == 0 {
			out.State, out.Code = "failed", "no_saved_opportunity"
		}
		finishErr := hooks.Finish(finishCtx, out)
		if readErr != nil || finishErr != nil {
			err = ErrIntakePersistence
		}
	}()

	signals := make(chan intakeSignal, 16)
	pumpErr := make(chan error, 1)
	stop := make(chan struct{})
	pumpDone := make(chan struct{})
	go c.pumpIntake(stop, pumpDone, signals, pumpErr)
	defer func() { close(stop); <-pumpDone }()
	// If dispatch or its acknowledgement is lost, close the owned connection.
	// A later run cannot silently overlap the unresolved turn on this client.
	settled := false
	defer func() {
		if !settled {
			c.stopIntake(out.ThreadID, out.TurnID)
		}
	}()

	thread, err := c.client.StartThread(ctx, c.instructions)
	if err != nil {
		return out, err
	}
	out.ThreadID = thread.ID
	if hooks.RecordDispatch(ctx, out.ThreadID, "") != nil {
		return out, ErrIntakePersistence
	}
	turn, err := c.client.StartTurn(ctx, out.ThreadID, text)
	if err != nil {
		return out, err
	}
	out.TurnID = turn.ID
	if hooks.RecordDispatch(ctx, out.ThreadID, out.TurnID) != nil {
		return out, ErrIntakePersistence
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
		case err := <-pumpErr:
			out.Code = "invalid_runtime_event"
			return out, err
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

// Drain progress continuously, including while start RPC replies are pending.
// Preserve bounded terminal/request events; never persist raw payloads or prose.
func (c *IntakeController) pumpIntake(stop <-chan struct{}, done chan<- struct{}, signals chan<- intakeSignal, failures chan<- error) {
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
			var signal intakeSignal
			if event.Request != nil {
				var p struct {
					ThreadID string `json:"threadId"`
					TurnID   string `json:"turnId"`
				}
				if json.Unmarshal(event.Request.Params, &p) != nil || p.ThreadID == "" || p.TurnID == "" {
					fail(ErrMalformedFrame)
					return
				}
				signal = intakeSignal{threadID: p.ThreadID, turnID: p.TurnID, attention: true}
			} else if event.Method == "turn/completed" {
				var p struct {
					ThreadID string `json:"threadId"`
					Turn     Turn   `json:"turn"`
				}
				if json.Unmarshal(event.Params, &p) != nil || p.ThreadID == "" || p.Turn.ID == "" ||
					!validTurnStatus(p.Turn.Status) || p.Turn.Status == "inProgress" {
					fail(ErrMalformedFrame)
					return
				}
				signal = intakeSignal{threadID: p.ThreadID, turnID: p.Turn.ID, status: p.Turn.Status}
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

func (c *IntakeController) stopIntake(threadID, turnID string) {
	if threadID != "" && turnID != "" && c.client.Err() == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.client.Interrupt(ctx, threadID, turnID)
		cancel()
	}
	_ = c.client.Close()
}
