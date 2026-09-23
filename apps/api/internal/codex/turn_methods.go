package codex

import (
	"context"
	"strings"
)

// StartThread accepts trusted application instructions only. Runtime paths,
// providers, tool configuration and sandbox overrides are never user inputs.
func (c *Client) StartThread(ctx context.Context, instructions, model string) (Thread, error) {
	if strings.TrimSpace(instructions) == "" || strings.TrimSpace(model) == "" {
		return Thread{}, ErrInvalidArgument
	}
	var result struct {
		Thread Thread `json:"thread"`
	}
	err := c.call(ctx, "thread/start", struct {
		DeveloperInstructions string `json:"developerInstructions"`
		Model                 string `json:"model"`
		ApprovalPolicy        string `json:"approvalPolicy"`
	}{instructions, model, "never"}, &result, false)
	if err == nil && result.Thread.ID == "" {
		c.fail(ErrMalformedFrame)
		err = ErrMalformedFrame
	}
	if err != nil {
		return Thread{}, err
	}
	return result.Thread, nil
}

// StartTurn sends text only. It makes no retry or remote idempotency promise.
func (c *Client) StartTurn(ctx context.Context, threadID, text, effort string) (Turn, error) {
	if threadID == "" || strings.TrimSpace(text) == "" || strings.TrimSpace(effort) == "" {
		return Turn{}, ErrInvalidArgument
	}
	type input struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var result struct {
		Turn Turn `json:"turn"`
	}
	err := c.call(ctx, "turn/start", struct {
		ThreadID string  `json:"threadId"`
		Effort   string  `json:"effort"`
		Input    []input `json:"input"`
	}{threadID, effort, []input{{"text", text}}}, &result, false)
	if err == nil && (result.Turn.ID == "" || !validTurnStatus(result.Turn.Status)) {
		c.fail(ErrMalformedFrame)
		err = ErrMalformedFrame
	}
	if err != nil {
		return Turn{}, err
	}
	return result.Turn, nil
}

func validTurnStatus(status string) bool {
	return status == "inProgress" || status == "completed" || status == "failed" || status == "interrupted"
}
