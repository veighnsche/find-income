package jev

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func capturedClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	cfg := testConfig(server.URL)
	cfg.MaxAttempts = 2
	client, err := newClient(cfg, "synthetic-test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestEvaluateOnceCapturedInvalidAndMissingUsage(t *testing.T) {
	body := []byte(`{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":0}}`)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write(body)
	}))
	defer server.Close()
	result, capture, err := capturedClient(t, server).EvaluateOnceCaptured(context.Background(), testRequest())
	if !errors.Is(err, &Error{Kind: ErrInvalidResponse}) || result.ReturnedModel != "" || calls != 1 ||
		!bytes.Equal(capture.ResponseBytes, body) || capture.HTTPStatus != 200 || capture.InputTokens == nil || *capture.InputTokens != 0 || capture.OutputTokens != nil || len(capture.RequestBytes) == 0 {
		t.Fatalf("invalid response capture: result=%#v capture=%#v err=%v calls=%d", result, capture, err, calls)
	}
}

func TestEvaluateOnceCapturedFailureDoesNotRetry(t *testing.T) {
	body := []byte(`{"error":"rate limited"}`)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	_, capture, err := capturedClient(t, server).EvaluateOnceCaptured(context.Background(), testRequest())
	if !errors.Is(err, &Error{Kind: ErrRateLimited}) || calls != 1 || capture.HTTPStatus != 429 || !bytes.Equal(capture.ResponseBytes, body) {
		t.Fatalf("one charged attempt expected: %#v %v calls=%d", capture, err, calls)
	}
}

func TestEvaluateOnceCapturedMarksBoundedPrefix(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.MaxResponseBytes = 8
	client, err := newClient(cfg, "synthetic-test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, capture, err := client.EvaluateOnceCaptured(context.Background(), testRequest())
	if !errors.Is(err, &Error{Kind: ErrInvalidResponse}) || !capture.ResponseTruncated || len(capture.ResponseBytes) != 9 {
		t.Fatalf("bounded prefix not marked: %#v %v", capture, err)
	}
}
