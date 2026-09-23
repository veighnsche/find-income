package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// CapturedExchange is one round-bound HTTP attempt. A nil ResponseBytes means
// transport did not yield a response; empty non-nil bytes mean an empty body.
// ResponseTruncated marks the bounded prefix of an oversized body.
type CapturedExchange struct {
	RequestBytes      []byte
	ResponseBytes     []byte
	ResponseTruncated bool
	ResponseReadError bool
	HTTPStatus        int
	ReturnedModel     string
	InputTokens       *int64
	OutputTokens      *int64
}

type captureTransport struct {
	base     http.RoundTripper
	maximum  int64
	exchange *CapturedExchange
}

func (t captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil {
		return response, err
	}
	t.exchange.HTTPStatus = response.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(response.Body, t.maximum+1))
	_ = response.Body.Close()
	t.exchange.ResponseBytes = append([]byte{}, body...)
	t.exchange.ResponseTruncated = int64(len(body)) > t.maximum
	t.exchange.ResponseReadError = readErr != nil
	response.Body = io.NopCloser(bytes.NewReader(body))
	var metadata struct {
		Model *string `json:"model"`
		Usage *struct {
			InputTokens  *int64 `json:"input_tokens"`
			OutputTokens *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &metadata) == nil {
		if metadata.Model != nil {
			t.exchange.ReturnedModel = *metadata.Model
		}
		if metadata.Usage != nil {
			t.exchange.InputTokens = metadata.Usage.InputTokens
			t.exchange.OutputTokens = metadata.Usage.OutputTokens
		}
	}
	return response, nil
}

// EvaluateOnceCaptured uses the ordinary adapter parser and validation but
// makes exactly one HTTP attempt. A retry requires a fresh round reservation.
// It returns bounded exact response bytes even for non-200 or invalid bodies.
func (c *Client) EvaluateOnceCaptured(ctx context.Context, request Request) (Result, CapturedExchange, error) {
	var exchange CapturedExchange
	body, err := c.EncodedRequest(request)
	if err != nil {
		return Result{}, exchange, err
	}
	exchange.RequestBytes = append([]byte(nil), body...)
	copyClient := *c.httpClient
	base := copyClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copyClient.Transport = captureTransport{base: base, maximum: c.cfg.MaxResponseBytes, exchange: &exchange}
	copyProvider := *c
	copyProvider.cfg.MaxAttempts = 1
	copyProvider.httpClient = &copyClient
	result, err := copyProvider.evaluateEncoded(ctx, request, body)
	if err == nil && exchange.ResponseReadError {
		return Result{}, exchange, &Error{Kind: ErrInvalidResponse, Attempts: 1}
	}
	return result, exchange, err
}
