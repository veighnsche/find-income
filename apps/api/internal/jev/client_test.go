package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(endpoint string) Config {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint + "/v1/systemone"
	cfg.Timeout = time.Second
	cfg.BaseRetryDelay = time.Millisecond
	cfg.MaxRetryDelay = 10 * time.Millisecond
	return cfg
}

func testRequest() Request {
	return Request{
		State: map[string]any{"source_spans": []map[string]string{{"id": "synthetic-1", "text": "Own Go APIs; no frontend duties."}}},
		Questions: map[string]Question{
			"frontend_duties":  Choice("Does the candidate have frontend duties?", map[string]string{"required": "Must build UI", "not_required": "Explicitly excluded"}),
			"career_direction": Score("How aligned is actual work with backend/platform?", []string{"Outside backend/platform", "Mostly backend/platform", "Centred on backend/platform"}),
		},
	}
}

const validBody = `{"model":"jev-1.13.0","answers":{"frontend_duties":{"type":"choice","choice":"not_required","probabilities":{"required":0.1,"not_required":0.9},"confidence":0.8},"career_direction":{"type":"score","score":1.8,"legend":{"0":"Outside backend/platform","1":"Mostly backend/platform","2":"Centred on backend/platform"},"probabilities":{"0":0,"1":0.2,"2":0.8},"confidence":0.7}},"usage":{"input_tokens":123,"output_tokens":17}}`

func TestValidTypedResponseAndMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer synthetic-test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request metadata")
		}
		var wire struct {
			Model     string                     `json:"model"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if wire.Model != "jev-1.13.0" || len(wire.Questions) != 2 {
			t.Errorf("wrong requested model or questions")
		}
		_, _ = w.Write([]byte(validBody))
	}))
	defer server.Close()
	client, err := newClient(testConfig(server.URL), "synthetic-test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Evaluate(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestedModel != "jev-1.13.0" || result.ReturnedModel != "jev-1.13.0" || result.Usage != (Usage{123, 17}) ||
		result.Answers["frontend_duties"].Choice.Choice != "not_required" || result.Answers["career_direction"].Score.Score != 1.8 {
		t.Fatalf("lost typed response metadata: %+v", result)
	}
}

func TestDisabledAndMissingKey(t *testing.T) {
	cfg := DefaultConfig()
	client, err := NewFromEnvironment(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKind(t, evaluateError(client, testRequest()), ErrDisabled)
	cfg.Enabled = true
	t.Setenv(CredentialEnvironmentVariable, "")
	_, err = NewFromEnvironment(cfg, nil)
	assertKind(t, err, ErrMissingKey)
	if strings.Contains(err.Error(), "synthetic-test-key") {
		t.Fatal("credential leaked in error")
	}
}

func TestProviderFailures(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		kind       ErrorKind
		attempts   int
		retryAfter string
	}{
		{"unauthorized", 401, `{"error":"sensitive"}`, ErrUnauthorized, 1, ""},
		{"invalid request", 422, `{"error":"private payload"}`, ErrProviderRequest, 1, ""},
		{"rate limited", 429, `{"error":"slow"}`, ErrRateLimited, 2, "0"},
		{"overloaded", 529, `{"error":"busy"}`, ErrUnavailable, 2, "0"},
		{"unavailable", 503, `{"error":"busy"}`, ErrUnavailable, 1, ""},
		{"malformed JSON", 200, `{"model":`, ErrInvalidResponse, 1, ""},
		{"missing answer", 200, `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`, ErrInvalidResponse, 1, ""},
		{"extra answer", 200, strings.Replace(validBody, `"answers":{`, `"answers":{"surprise":{"type":"choice"},`, 1), ErrInvalidResponse, 1, ""},
		{"wrong option", 200, strings.Replace(validBody, `"choice":"not_required"`, `"choice":"invented"`, 1), ErrInvalidResponse, 1, ""},
		{"choice not highest", 200, strings.Replace(validBody, `"choice":"not_required"`, `"choice":"required"`, 1), ErrInvalidResponse, 1, ""},
		{"wrong type", 200, strings.Replace(validBody, `"type":"choice"`, `"type":"score"`, 1), ErrInvalidResponse, 1, ""},
		{"missing confidence", 200, strings.Replace(validBody, `,"confidence":0.8`, ``, 1), ErrInvalidResponse, 1, ""},
		{"wrong probability keys", 200, strings.Replace(validBody, `"not_required":0.9`, `"invented":0.9`, 1), ErrInvalidResponse, 1, ""},
		{"null probability with valid sum", 200, strings.Replace(validBody, `"required":0.1,"not_required":0.9`, `"required":1,"not_required":null`, 1), ErrInvalidResponse, 1, ""},
		{"duplicate probability key", 200, strings.Replace(validBody, `"required":0.1,"not_required":0.9`, `"required":1,"required":0.1,"not_required":0.9`, 1), ErrInvalidResponse, 1, ""},
		{"duplicate answer key", 200, strings.Replace(validBody, `"answers":{`, `"answers":{"frontend_duties":{},`, 1), ErrInvalidResponse, 1, ""},
		{"probability sum", 200, strings.Replace(validBody, `"not_required":0.9`, `"not_required":0.4`, 1), ErrInvalidResponse, 1, ""},
		{"wrong score legend", 200, strings.Replace(validBody, `"2":"Centred on backend/platform"`, `"2":"Different"`, 1), ErrInvalidResponse, 1, ""},
		{"wrong weighted score", 200, strings.Replace(validBody, `"score":1.8`, `"score":0.2`, 1), ErrInvalidResponse, 1, ""},
		{"missing usage", 200, strings.Replace(validBody, `,"usage":{"input_tokens":123,"output_tokens":17}`, ``, 1), ErrInvalidResponse, 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := newClient(testConfig(server.URL), "synthetic-test-key", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			err = evaluateError(client, testRequest())
			assertKind(t, err, tc.kind)
			if int(calls.Load()) != tc.attempts {
				t.Fatalf("got %d calls, want %d", calls.Load(), tc.attempts)
			}
			if strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "private payload") || strings.Contains(err.Error(), "synthetic-test-key") {
				t.Fatal("error exposed private provider data")
			}
		})
	}
}

func TestBoundedRetryThenSuccess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(validBody))
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	client, err := newClient(cfg, "synthetic-test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Evaluate(context.Background(), testRequest())
	if err != nil || result.ReturnedModel != "jev-1.13.0" || calls.Load() != 2 {
		t.Fatalf("bounded retry failed: calls=%d, err=%v", calls.Load(), err)
	}
	longDelay, permitted := retryDelay("60", 1, cfg)
	zeroDelay, zeroPermitted := retryDelay("0", 1, cfg)
	if longDelay != 60*time.Second || permitted || zeroDelay != 0 || !zeroPermitted {
		t.Fatal("Retry-After was not bounded")
	}
}

func TestLongRetryAfterDoesNotRetryEarly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header string
	}{
		{"integer", http.StatusTooManyRequests, "120"},
		{"HTTP date", 529, time.Now().Add(120 * time.Second).UTC().Format(http.TimeFormat)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			client, err := newClient(testConfig(server.URL), "synthetic-test-key", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			err = evaluateError(client, testRequest())
			var providerError *Error
			if !errors.As(err, &providerError) || calls.Load() != 1 || providerError.RetryAfter < 110*time.Second {
				t.Fatalf("retried too early or lost retry timing: calls=%d, err=%v", calls.Load(), err)
			}
			if tc.status == 429 && providerError.Kind != ErrRateLimited || tc.status == 529 && providerError.Kind != ErrUnavailable {
				t.Fatalf("wrong kind: %s", providerError.Kind)
			}
		})
	}
}

func TestTimeoutCancellationAndNoRedirect(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write([]byte(validBody))
		}))
		defer server.Close()
		cfg := testConfig(server.URL)
		cfg.Timeout = 10 * time.Millisecond
		client, _ := newClient(cfg, "synthetic-test-key", server.Client())
		assertKind(t, evaluateError(client, testRequest()), ErrTimeout)
	})
	t.Run("body deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write([]byte(validBody))
		}))
		defer server.Close()
		cfg := testConfig(server.URL)
		cfg.Timeout = 10 * time.Millisecond
		client, _ := newClient(cfg, "synthetic-test-key", server.Client())
		assertKind(t, evaluateError(client, testRequest()), ErrTimeout)
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client, _ := newClient(testConfig("http://127.0.0.1:1"), "synthetic-test-key", nil)
		_, err := client.Evaluate(ctx, testRequest())
		assertKind(t, err, ErrCanceled)
	})
	t.Run("redirect", func(t *testing.T) {
		var redirected atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirected.Store(true)
		}))
		defer target.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		defer source.Close()
		client, _ := newClient(testConfig(source.URL), "synthetic-test-key", source.Client())
		assertKind(t, evaluateError(client, testRequest()), ErrProviderRequest)
		if redirected.Load() {
			t.Fatal("followed provider redirect")
		}
	})
}

func TestLimitsAndInvalidInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(validBody))
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.MaxRequestBytes = 10
	client, _ := newClient(cfg, "synthetic-test-key", server.Client())
	assertKind(t, evaluateError(client, testRequest()), ErrRequestTooLarge)
	cfg.MaxRequestBytes = 10000
	cfg.MaxResponseBytes = 10
	client, _ = newClient(cfg, "synthetic-test-key", server.Client())
	assertKind(t, evaluateError(client, testRequest()), ErrInvalidResponse)
	client, _ = newClient(testConfig(server.URL), "synthetic-test-key", server.Client())
	bad := testRequest()
	bad.Questions = map[string]Question{"x": Choice("", map[string]string{"a": "A", "b": "B"})}
	assertKind(t, evaluateError(client, bad), ErrInvalidRequest)
	invalidConfig := testConfig("http://127.0.0.1:1")
	invalidConfig.Endpoint = "https://example.com/v1/systemone"
	_, err := newClient(invalidConfig, "synthetic-test-key", nil)
	assertKind(t, err, ErrInvalidConfig)
}

func evaluateError(client *Client, request Request) error {
	_, err := client.Evaluate(context.Background(), request)
	return err
}

func assertKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	var providerError *Error
	if !errors.As(err, &providerError) || providerError.Kind != kind {
		t.Fatalf("got %v, want kind %s", err, kind)
	}
	if strings.Contains(fmt.Sprint(err), "Bearer") {
		t.Fatal("authorization leaked")
	}
}
