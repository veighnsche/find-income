package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const CredentialEnvironmentVariable = "TYPESAFE_API_KEY"

type Config struct {
	Enabled          bool
	Endpoint         string
	Model            string
	Timeout          time.Duration // Total time, including retry waits.
	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxAttempts      int
	BaseRetryDelay   time.Duration
	MaxRetryDelay    time.Duration
}

func DefaultConfig() Config {
	return Config{
		Enabled:          false,
		Endpoint:         "https://api.typesafe.ai/v1/systemone",
		Model:            "jev-1.13.0",
		Timeout:          20 * time.Second,
		MaxRequestBytes:  256 << 10,
		MaxResponseBytes: 1 << 20,
		MaxAttempts:      2,
		BaseRetryDelay:   250 * time.Millisecond,
		MaxRetryDelay:    2 * time.Second,
	}
}

type Client struct {
	cfg        Config
	credential string
	httpClient *http.Client
}

func (c *Client) RequestedModel() string {
	if c == nil {
		return ""
	}
	return c.cfg.Model
}

// NewFromEnvironment is the sole production path for reading the provider key.
// Disabled mode does not read it. The key is never included in an error.
func NewFromEnvironment(cfg Config, httpClient *http.Client) (*Client, error) {
	if !cfg.Enabled {
		return newClient(cfg, "", httpClient)
	}
	return newClient(cfg, os.Getenv(CredentialEnvironmentVariable), httpClient)
}

func newClient(cfg Config, credential string, httpClient *http.Client) (*Client, error) {
	if !cfg.Enabled {
		return &Client{cfg: cfg}, nil
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(credential) == "" {
		return nil, &Error{Kind: ErrMissingKey}
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	copyClient := *httpClient
	// Even an injected client cannot redirect the Authorization header or body.
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, credential: credential, httpClient: &copyClient}, nil
}

func validateConfig(cfg Config) error {
	if cfg.Model == "" || strings.TrimSpace(cfg.Model) != cfg.Model ||
		cfg.Timeout <= 0 || cfg.MaxRequestBytes <= 0 || cfg.MaxRequestBytes > 8<<20 ||
		cfg.MaxResponseBytes <= 0 || cfg.MaxResponseBytes > 16<<20 ||
		cfg.MaxAttempts < 1 || cfg.MaxAttempts > 3 || cfg.BaseRetryDelay <= 0 ||
		cfg.MaxRetryDelay < cfg.BaseRetryDelay {
		return &Error{Kind: ErrInvalidConfig}
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" ||
		u.Path != "/v1/systemone" || u.Hostname() == "" {
		return &Error{Kind: ErrInvalidConfig}
	}
	if u.Scheme == "https" && u.Hostname() == "api.typesafe.ai" && u.Port() == "" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "localhost" || isLoopbackIP(u.Hostname())) {
		return nil // For an injected local fake provider in tests.
	}
	return &Error{Kind: ErrInvalidConfig}
}

func isLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) Evaluate(ctx context.Context, request Request) (Result, error) {
	body, err := c.EncodedRequest(request)
	if err != nil {
		return Result{}, err
	}
	return c.evaluateEncoded(ctx, request, body)
}

// EncodedRequest returns the exact JSON body Evaluate sends. It excludes the
// Authorization header. A round-bound caller can persist it before dispatch.
func (c *Client) EncodedRequest(request Request) ([]byte, error) {
	if c == nil || !c.cfg.Enabled {
		return nil, &Error{Kind: ErrDisabled}
	}
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{State: request.State, Model: c.cfg.Model, Questions: request.Questions})
	if err != nil {
		return nil, &Error{Kind: ErrInvalidRequest}
	}
	if int64(len(body)) > c.cfg.MaxRequestBytes {
		return nil, &Error{Kind: ErrRequestTooLarge}
	}
	return body, nil
}

func (c *Client) evaluateEncoded(ctx context.Context, request Request, body []byte) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Result{}, contextError(err, attempt-1)
		}
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
		if err != nil {
			return Result{}, &Error{Kind: ErrInvalidConfig, Attempts: attempt}
		}
		httpRequest.Header.Set("Authorization", "Bearer "+c.credential)
		httpRequest.Header.Set("Content-Type", "application/json")
		response, err := c.httpClient.Do(httpRequest)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, contextError(ctx.Err(), attempt)
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return Result{}, &Error{Kind: ErrTimeout, Attempts: attempt}
			}
			return Result{}, &Error{Kind: ErrTransport, Attempts: attempt}
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			kind := statusKind(response.StatusCode)
			if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == 529 {
				delay, permitted := retryDelay(response.Header.Get("Retry-After"), attempt, c.cfg)
				deadline, hasDeadline := ctx.Deadline()
				if attempt < c.cfg.MaxAttempts && permitted && (!hasDeadline || delay < time.Until(deadline)) {
					if err := wait(ctx, delay); err != nil {
						return Result{}, contextError(err, attempt)
					}
					continue
				}
				return Result{}, &Error{Kind: kind, StatusCode: response.StatusCode, Attempts: attempt, RetryAfter: delay}
			}
			return Result{}, &Error{Kind: kind, StatusCode: response.StatusCode, Attempts: attempt}
		}
		limited, err := io.ReadAll(io.LimitReader(response.Body, c.cfg.MaxResponseBytes+1))
		_ = response.Body.Close()
		if ctx.Err() != nil {
			return Result{}, contextError(ctx.Err(), attempt)
		}
		if err != nil || int64(len(limited)) > c.cfg.MaxResponseBytes {
			return Result{}, &Error{Kind: ErrInvalidResponse, Attempts: attempt}
		}
		result, err := parseResponse(limited, request.Questions, c.cfg.Model)
		if err != nil {
			return Result{}, &Error{Kind: ErrInvalidResponse, Attempts: attempt}
		}
		return result, nil
	}
	return Result{}, &Error{Kind: ErrUnavailable, Attempts: c.cfg.MaxAttempts}
}

func statusKind(status int) ErrorKind {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case 529:
		return ErrUnavailable
	default:
		if status >= 500 {
			return ErrUnavailable
		}
		return ErrProviderRequest
	}
}

func contextError(err error, attempts int) *Error {
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: ErrCanceled, Attempts: attempts}
	}
	return &Error{Kind: ErrTimeout, Attempts: attempts}
}

// retryDelay returns a wait and whether it fits this client's local retry
// window. A provider Retry-After is a minimum, never an early-retry target.
func retryDelay(header string, attempt int, cfg Config) (time.Duration, bool) {
	delay := cfg.BaseRetryDelay
	for i := 1; i < attempt && delay < cfg.MaxRetryDelay; i++ {
		if delay > cfg.MaxRetryDelay/2 {
			delay = cfg.MaxRetryDelay
		} else {
			delay *= 2
		}
	}
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); errors.Is(err, strconv.ErrRange) {
		return time.Duration(math.MaxInt64), false
	} else if err == nil && seconds >= 0 {
		if seconds > math.MaxInt64/int64(time.Second) {
			return time.Duration(math.MaxInt64), false
		}
		delay = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(header); err == nil {
		delay = time.Until(date)
		if delay < 0 {
			delay = 0
		}
	}
	if delay > cfg.MaxRetryDelay {
		return delay, false
	}
	return delay, true
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type wireResponse struct {
	Model   *string                    `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   *struct {
		InputTokens  *int64 `json:"input_tokens"`
		OutputTokens *int64 `json:"output_tokens"`
	} `json:"usage"`
}

func parseResponse(body []byte, questions map[string]Question, requestedModel string) (Result, error) {
	if err := rejectDuplicateKeys(body); err != nil {
		return Result{}, err
	}
	var wire wireResponse
	if err := json.Unmarshal(body, &wire); err != nil || wire.Model == nil || strings.TrimSpace(*wire.Model) == "" ||
		len(wire.Answers) != len(questions) || wire.Usage == nil || wire.Usage.InputTokens == nil || wire.Usage.OutputTokens == nil ||
		*wire.Usage.InputTokens < 0 || *wire.Usage.OutputTokens < 0 {
		return Result{}, errors.New("invalid provider response")
	}
	result := Result{
		RequestedModel: requestedModel,
		ReturnedModel:  *wire.Model,
		Answers:        make(map[string]Answer, len(questions)),
		Usage:          Usage{InputTokens: *wire.Usage.InputTokens, OutputTokens: *wire.Usage.OutputTokens},
		RawResponse:    append(json.RawMessage(nil), body...),
	}
	for id, question := range questions {
		raw, ok := wire.Answers[id]
		if !ok {
			return Result{}, errors.New("missing provider answer")
		}
		answer, err := parseAnswer(raw, question)
		if err != nil {
			return Result{}, err
		}
		result.Answers[id] = answer
	}
	return result, nil
}

func parseAnswer(raw json.RawMessage, question Question) (Answer, error) {
	var tagged struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &tagged); err != nil {
		return Answer{}, err
	}
	switch q := question.(type) {
	case ChoiceQuestion:
		if tagged.Type != "choice" {
			break
		}
		var a struct {
			Type          string              `json:"type"`
			Choice        *string             `json:"choice"`
			Probabilities strictProbabilities `json:"probabilities"`
			Confidence    *float64            `json:"confidence"`
		}
		if err := decodeStrict(raw, &a); err != nil || a.Choice == nil || a.Confidence == nil || !validProbability(*a.Confidence) {
			break
		}
		if _, ok := q.Criteria[*a.Choice]; !ok {
			break
		}
		expected := make(map[string]struct{}, len(q.Criteria))
		for option := range q.Criteria {
			expected[option] = struct{}{}
		}
		if !validDistribution(map[string]float64(a.Probabilities), expected) {
			break
		}
		for _, probability := range a.Probabilities {
			if probability > a.Probabilities[*a.Choice]+0.000001 {
				return Answer{}, errors.New("choice is not a highest-probability option")
			}
		}
		return Answer{Type: "choice", Choice: &ChoiceAnswer{Choice: *a.Choice, Probabilities: map[string]float64(a.Probabilities), Confidence: *a.Confidence}}, nil
	case ScoreQuestion:
		if tagged.Type != "score" {
			break
		}
		var a struct {
			Type          string              `json:"type"`
			Score         *float64            `json:"score"`
			Legend        map[string]string   `json:"legend"`
			Probabilities strictProbabilities `json:"probabilities"`
			Confidence    *float64            `json:"confidence"`
		}
		if err := decodeStrict(raw, &a); err != nil || a.Score == nil || a.Confidence == nil || !validProbability(*a.Confidence) ||
			math.IsNaN(*a.Score) || math.IsInf(*a.Score, 0) || *a.Score < 0 || *a.Score > float64(len(q.Criteria)-1) ||
			len(a.Legend) != len(q.Criteria) {
			break
		}
		expected := make(map[string]struct{}, len(q.Criteria))
		for index, level := range q.Criteria {
			key := strconv.Itoa(index)
			if a.Legend[key] != level {
				return Answer{}, errors.New("invalid score legend")
			}
			expected[key] = struct{}{}
		}
		if !validDistribution(map[string]float64(a.Probabilities), expected) {
			break
		}
		weighted := 0.0
		for index := range q.Criteria {
			weighted += float64(index) * a.Probabilities[strconv.Itoa(index)]
		}
		if math.Abs(weighted-*a.Score) > 0.02 {
			break
		}
		return Answer{Type: "score", Score: &ScoreAnswer{Score: *a.Score, Legend: a.Legend, Probabilities: map[string]float64(a.Probabilities), Confidence: *a.Confidence}}, nil
	}
	return Answer{}, errors.New("invalid provider answer")
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
