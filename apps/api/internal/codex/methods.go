package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type RuntimeInfo struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

// Initialize is single-use, including on failure. Version checks are a
// compatibility check, not binary attestation: the supervisor must pin the hash.
func (c *Client) Initialize(ctx context.Context) (RuntimeInfo, error) {
	c.mu.Lock()
	if c.failed != nil {
		err := c.failed
		c.mu.Unlock()
		return RuntimeInfo{}, err
	}
	if c.initializing {
		c.mu.Unlock()
		return RuntimeInfo{}, ErrAlreadyInitialized
	}
	c.initializing = true
	c.mu.Unlock()
	params := struct {
		ClientInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
		Capabilities struct {
			ExperimentalAPI bool `json:"experimentalApi"`
		} `json:"capabilities"`
	}{}
	params.ClientInfo.Name = "jobseek_dashboard"
	params.ClientInfo.Version = "0.1.0"
	var info RuntimeInfo
	if err := c.call(ctx, "initialize", params, &info, true); err != nil {
		c.fail(ErrUnavailable)
		return RuntimeInfo{}, err
	}
	if !strings.HasPrefix(info.UserAgent, "jobseek_dashboard/"+PinnedVersion+" ") || info.CodexHome == "" || info.PlatformOS == "" || info.PlatformFamily == "" {
		c.fail(ErrIncompatible)
		return RuntimeInfo{}, ErrIncompatible
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	if err := c.send(ctx, struct {
		Method string `json:"method"`
	}{"initialized"}, nil); err != nil {
		c.fail(ErrUnavailable)
		return RuntimeInfo{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed != nil {
		return RuntimeInfo{}, c.failed
	}
	c.ready = true
	return info, nil
}

type Account struct {
	Type     string  `json:"type"`
	Email    *string `json:"email"`
	PlanType *string `json:"planType"`
}
type AccountState struct {
	Account            *Account `json:"account"`
	RequiresOpenAIAuth *bool    `json:"requiresOpenaiAuth"`
}

func (c *Client) ReadAccount(ctx context.Context) (AccountState, error) {
	var result AccountState
	err := c.call(ctx, "account/read", struct {
		RefreshToken bool `json:"refreshToken"`
	}{false}, &result, false)
	if err == nil && result.RequiresOpenAIAuth == nil {
		c.fail(ErrMalformedFrame)
		err = ErrMalformedFrame
	}
	if err == nil && result.Account != nil && result.Account.Type != "chatgpt" {
		err = ErrUnsupported
	}
	if err != nil {
		return AccountState{}, err
	}
	return result, nil
}

type LoginMode string

const (
	DeviceLogin  LoginMode = "chatgptDeviceCode"
	BrowserLogin LoginMode = "chatgpt"
)

type LoginAttempt struct {
	Type            LoginMode `json:"type"`
	LoginID         string    `json:"loginId"`
	VerificationURL string    `json:"verificationUrl,omitempty"`
	UserCode        string    `json:"userCode,omitempty"`
	AuthURL         string    `json:"authUrl,omitempty"`
}

// StartLogin must only be called by a future owner-authenticated account service.
// This wire method is not mounted on HTTP, and no login runs during construction.
func (c *Client) StartLogin(ctx context.Context, mode LoginMode) (LoginAttempt, error) {
	if mode != DeviceLogin && mode != BrowserLogin {
		return LoginAttempt{}, ErrUnsupported
	}
	var result LoginAttempt
	err := c.call(ctx, "account/login/start", struct {
		Type LoginMode `json:"type"`
	}{mode}, &result, false)
	if err == nil && (result.Type != mode || result.LoginID == "" || (mode == DeviceLogin && (result.VerificationURL == "" || result.UserCode == "")) || (mode == BrowserLogin && result.AuthURL == "")) {
		c.fail(ErrMalformedFrame)
		err = ErrMalformedFrame
	}
	if err != nil {
		return LoginAttempt{}, err
	}
	return result, nil
}

func (c *Client) CancelLogin(ctx context.Context, loginID string) error {
	if loginID == "" {
		return ErrInvalidArgument
	}
	return c.call(ctx, "account/login/cancel", struct {
		LoginID string `json:"loginId"`
	}{loginID}, nil, false)
}
func (c *Client) Logout(ctx context.Context) error {
	return c.call(ctx, "account/logout", nil, nil, false)
}

type LimitWindow struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *int64   `json:"windowDurationMins"`
	ResetsAt           *int64   `json:"resetsAt"`
}
type LimitSnapshot struct {
	LimitID   *string      `json:"limitId"`
	LimitName *string      `json:"limitName"`
	Primary   *LimitWindow `json:"primary"`
	Secondary *LimitWindow `json:"secondary"`
}
type RateLimits struct {
	RateLimits *LimitSnapshot           `json:"rateLimits"`
	ByLimitID  map[string]LimitSnapshot `json:"rateLimitsByLimitId"`
}

func (c *Client) ReadRateLimits(ctx context.Context) (RateLimits, error) {
	var result RateLimits
	err := c.call(ctx, "account/rateLimits/read", nil, &result, false)
	if err != nil {
		return RateLimits{}, err
	}
	return result, nil // nil windows are unavailable; never synthesize zero usage.
}

type EffortOption struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description"`
}
type Model struct {
	ID                        string         `json:"id"`
	Model                     string         `json:"model"`
	DisplayName               string         `json:"displayName"`
	DefaultReasoningEffort    string         `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []EffortOption `json:"supportedReasoningEfforts"`
}
type Models struct {
	Data       []Model `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

func (c *Client) ListModels(ctx context.Context, cursor *string) (Models, error) {
	var result Models
	err := c.call(ctx, "model/list", struct {
		Cursor *string `json:"cursor,omitempty"`
		Limit  int     `json:"limit"`
	}{cursor, 100}, &result, false)
	if err != nil {
		return Models{}, err
	}
	return result, nil
}

type MCPTool struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"inputSchema"`
}
type MCPServer struct {
	Name       string             `json:"name"`
	AuthStatus string             `json:"authStatus"`
	Tools      map[string]MCPTool `json:"tools"`
}
type MCPServers struct {
	Data       []MCPServer `json:"data"`
	NextCursor *string     `json:"nextCursor"`
}

// Discovery only. Actual dashboard tool calls and a fail-closed required bridge
// must be verified by the later runtime integration, not inferred from this DTO.
func (c *Client) ListMCPServers(ctx context.Context, threadID, cursor *string) (MCPServers, error) {
	var result MCPServers
	err := c.call(ctx, "mcpServerStatus/list", struct {
		ThreadID *string `json:"threadId,omitempty"`
		Cursor   *string `json:"cursor,omitempty"`
		Limit    int     `json:"limit"`
		Detail   string  `json:"detail"`
	}{threadID, cursor, 100, "toolsAndAuthOnly"}, &result, false)
	if err != nil {
		return MCPServers{}, err
	}
	return result, nil
}

// History remains internal wire data. A supported response is not proof that
// uncertain dispatch can be reconciled; that needs authenticated integration.
type Turn struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Items  []json.RawMessage `json:"items"`
}
type Thread struct {
	ID          string `json:"id"`
	HistoryMode string `json:"historyMode"`
	Turns       []Turn `json:"turns"`
}

// ResumeThread loads a previously persisted thread on a new connection.
// The thread must already be materialized (at least one started turn); a
// same-connection resume of a loaded thread is unnecessary and unsupported.
// Paginated history is never hydrated here; page thread/turns/list instead.
func (c *Client) ResumeThread(ctx context.Context, threadID string, excludeTurns bool) (Thread, error) {
	if threadID == "" {
		return Thread{}, ErrInvalidArgument
	}
	var result struct {
		Thread Thread `json:"thread"`
	}
	err := c.call(ctx, "thread/resume", struct {
		ThreadID     string `json:"threadId"`
		ExcludeTurns bool   `json:"excludeTurns"`
	}{threadID, excludeTurns}, &result, false)
	if err == nil && result.Thread.ID != threadID {
		c.fail(ErrMalformedFrame)
		err = ErrMalformedFrame
	}
	if err != nil {
		return Thread{}, err
	}
	return result.Thread, nil
}

// ReadThread reads thread metadata only. Full-history hydration is deprecated
// for paginated threads on this pin: includeTurns=true is rejected before the
// first turn and hangs on materialized threads. Page ListTurns instead.
func (c *Client) ReadThread(ctx context.Context, threadID string) (Thread, error) {
	if threadID == "" {
		return Thread{}, ErrInvalidArgument
	}
	var result struct {
		Thread Thread `json:"thread"`
	}
	err := c.call(ctx, "thread/read", struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns bool   `json:"includeTurns"`
	}{threadID, false}, &result, false)
	if err == nil && result.Thread.ID != threadID {
		c.fail(ErrMalformedFrame)
		err = ErrMalformedFrame
	}
	if err != nil {
		return Thread{}, err
	}
	return result.Thread, nil
}

type Turns struct {
	Data            []Turn  `json:"data"`
	NextCursor      *string `json:"nextCursor"`
	BackwardsCursor *string `json:"backwardsCursor"`
}

func (c *Client) ListTurns(ctx context.Context, threadID string, cursor *string) (Turns, error) {
	if threadID == "" {
		return Turns{}, ErrInvalidArgument
	}
	var result Turns
	err := c.call(ctx, "thread/turns/list", struct {
		ThreadID  string  `json:"threadId"`
		Cursor    *string `json:"cursor,omitempty"`
		Limit     int     `json:"limit"`
		ItemsView string  `json:"itemsView"`
	}{threadID, cursor, 20, "full"}, &result, false)
	if err != nil {
		return Turns{}, err
	}
	return result, nil
}

// ObserveTurn reads one known dispatch by its persisted IDs. Incomplete or
// unsupported history never authorizes a retry. The caller must still verify
// ownership and persist a terminal observation before resetting a job.
//
// The turn list is authoritative; no separate identity read is needed. On a
// fresh connection the persisted thread is not loaded yet, so one resume is
// attempted exactly when the first page reports the thread unavailable.
func (c *Client) ObserveTurn(ctx context.Context, threadID, turnID string) (string, error) {
	if threadID == "" || turnID == "" {
		return "", ErrInvalidArgument
	}
	var cursor *string
	seen := make(map[string]struct{}, 10)
	resumed := false
	for page := 0; page < 10; page++ {
		turns, err := c.ListTurns(ctx, threadID, cursor)
		if err != nil {
			if !resumed && isThreadUnavailable(err) {
				resumed = true
				if _, resumeErr := c.ResumeThread(ctx, threadID, true); resumeErr != nil {
					return "", ErrHistoryIncomplete
				}
				continue
			}
			return "", err
		}
		for _, turn := range turns.Data {
			if turn.ID == turnID {
				if !validTurnStatus(turn.Status) {
					return "", ErrMalformedFrame
				}
				return turn.Status, nil
			}
		}
		if turns.NextCursor == nil || *turns.NextCursor == "" {
			return "", ErrHistoryIncomplete
		}
		if _, duplicate := seen[*turns.NextCursor]; duplicate {
			return "", ErrHistoryIncomplete
		}
		seen[*turns.NextCursor] = struct{}{}
		cursor = turns.NextCursor
	}
	return "", ErrHistoryIncomplete
}

// isThreadUnavailable reports a rejected turn list that a fresh-connection
// resume may repair (unknown thread, unloaded thread, or a thread with no
// started turn yet). Upstream detail is discarded by RPCError by design.
func isThreadUnavailable(err error) bool {
	var rpc *RPCError
	return errors.As(err, &rpc) && rpc.Code == -32600
}

// Available in schema but explicitly unsupported by the pinned runtime probe.
func (c *Client) ListThreadItems(context.Context, string) error { return ErrUnsupported }

func (c *Client) Interrupt(ctx context.Context, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return ErrInvalidArgument
	}
	return c.call(ctx, "turn/interrupt", struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}{threadID, turnID}, nil, false)
}

// SteerTurn appends owner text to the active turn. The expected turn ID must
// match the running turn; steering any other turn is rejected by the server.
// The returned ID identifies the steered turn. Steering never starts a turn.
func (c *Client) SteerTurn(ctx context.Context, threadID, expectedTurnID, text string) (string, error) {
	if threadID == "" || expectedTurnID == "" || strings.TrimSpace(text) == "" {
		return "", ErrInvalidArgument
	}
	type input struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var result struct {
		TurnID string `json:"turnId"`
	}
	err := c.call(ctx, "turn/steer", struct {
		ThreadID       string  `json:"threadId"`
		ExpectedTurnID string  `json:"expectedTurnId"`
		Input          []input `json:"input"`
	}{threadID, expectedTurnID, []input{{"text", text}}}, &result, false)
	if err == nil && result.TurnID == "" {
		c.fail(ErrMalformedFrame)
		err = ErrMalformedFrame
	}
	if err != nil {
		return "", err
	}
	return result.TurnID, nil
}

// CallMCPTool dispatches one registered MCP tool call through the App Server
// on a loaded thread. Arguments are trusted application JSON, never model or
// browser input. The raw tool result is returned for typed interpretation by
// the caller; a failed call is an RPC error, never a synthesized result.
func (c *Client) CallMCPTool(ctx context.Context, server, threadID, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	if server == "" || threadID == "" || tool == "" {
		return nil, ErrInvalidArgument
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var result json.RawMessage
	err := c.call(ctx, "mcpServer/tool/call", struct {
		Server    string          `json:"server"`
		ThreadID  string          `json:"threadId"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}{server, threadID, tool, arguments}, &result, false)
	if err != nil {
		return nil, err
	}
	return result, nil
}

type ApprovalDecision string

const (
	AcceptOnce ApprovalDecision = "accept"
	Decline    ApprovalDecision = "decline"
	CancelTurn ApprovalDecision = "cancel"
)

// ReplyApproval deliberately excludes session-wide grants and policy amendments.
// The future owner service must authenticate and authorize each response.
func (c *Client) ReplyApproval(ctx context.Context, token RequestToken, decision ApprovalDecision) error {
	if decision != AcceptOnce && decision != Decline && decision != CancelTurn {
		return ErrUnsupported
	}
	return c.respond(ctx, token, []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval"}, struct {
		Decision ApprovalDecision `json:"decision"`
	}{decision})
}

type Answer struct {
	Answers []string `json:"answers"`
}

func (c *Client) ReplyUserInput(ctx context.Context, token RequestToken, answers map[string]Answer) error {
	if len(answers) == 0 {
		return ErrInvalidArgument
	}
	return c.respond(ctx, token, []string{"item/tool/requestUserInput"}, struct {
		Answers map[string]Answer `json:"answers"`
	}{answers})
}

// RejectNativeRequest closes a native approval or input prompt without giving
// the model authority or exposing its question to the dashboard.
func (c *Client) RejectNativeRequest(ctx context.Context, token RequestToken) error {
	return c.respondFrame(ctx, token, []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/tool/requestUserInput"}, struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{json.RawMessage(token.id), struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{-32601, "Native request unavailable"}})
}
