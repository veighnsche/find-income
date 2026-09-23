// Package discovery performs bounded, round-charged public candidate reads.
package discovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const endpoint = "https://mcp.himalayas.app/mcp"
const maxBody = 128 << 10
const maxText = 96 << 10

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var countPattern = regexp.MustCompile(`(?m)^Found ([0-9]+) jobs .*\(showing page ([0-9]+)\)`)

type Store interface {
	Round(context.Context, string) (store.Round, error)
	VerifyRoundToolCapability(context.Context, string, string) (store.RoundToolAuthority, error)
	ReserveRoundAttempt(context.Context, store.Actor, string, store.RoundAttemptInput) (store.RoundAttempt, bool, error)
	MarkRoundDispatched(context.Context, string, string) (store.RoundAttempt, error)
	RoundAttempt(context.Context, string) (store.RoundAttempt, error)
	FinishRoundAttempt(context.Context, store.Actor, string, string, bool, json.RawMessage, string) (store.RoundAttempt, error)
	ExpireRound(context.Context, string) (store.Round, error)
	PrepareDiscoveryHTTP(context.Context, store.DiscoveryHTTP) error
	CompleteDiscoveryHTTP(context.Context, store.DiscoveryHTTP) error
	DiscoveryHTTP(context.Context, string) (store.DiscoveryHTTP, error)
}

type Reader struct {
	Store       Store
	Client      *http.Client
	Now         func() time.Time
	endpointURL string // test fixture only; production uses the fixed public endpoint
}

type Input struct {
	RoundID, RequestKey, ResourceID string
	Capability                      string
	Method                          string
	Keyword, Country                string
	Page                            int
	CompanySlug, JobSlug            string
}

type Page struct {
	AttemptID, Method, Text, ResponseSHA256, ObservedAt string
	Page, Total, NextPage                               int
	Omitted                                             int
}

func (r *Reader) clock() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func validate(in Input) (map[string]any, error) {
	if in.RoundID == "" || in.RequestKey == "" || in.ResourceID != "discovery:himalayas" || in.Capability == "" {
		return nil, store.ErrInvalid
	}
	switch in.Method {
	case "search_jobs":
		if strings.TrimSpace(in.Keyword) != in.Keyword || len(in.Keyword) < 2 || len(in.Keyword) > 120 || len(in.Country) > 80 || strings.TrimSpace(in.Country) != in.Country || in.Page < 1 || in.Page > 100 || in.CompanySlug != "" || in.JobSlug != "" {
			return nil, store.ErrInvalid
		}
		args := map[string]any{"keyword": in.Keyword, "page": in.Page}
		if in.Country != "" {
			args["country"] = in.Country
		}
		return args, nil
	case "get_company_details":
		if !slugPattern.MatchString(in.CompanySlug) || in.Keyword != "" || in.Country != "" || in.Page != 0 || in.JobSlug != "" {
			return nil, store.ErrInvalid
		}
		return map[string]any{"company_slug": in.CompanySlug}, nil
	case "get_job_details":
		if !slugPattern.MatchString(in.CompanySlug) || !slugPattern.MatchString(in.JobSlug) || in.Keyword != "" || in.Country != "" || in.Page != 0 {
			return nil, store.ErrInvalid
		}
		return map[string]any{"company_slug": in.CompanySlug, "job_slug": in.JobSlug}, nil
	default:
		return nil, store.ErrInvalid
	}
}

func (r *Reader) Read(ctx context.Context, in Input) (Page, error) {
	if r == nil || r.Store == nil || ctx == nil {
		return Page{}, store.ErrInvalid
	}
	args, err := validate(in)
	if err != nil {
		return Page{}, err
	}
	round, err := r.Store.Round(ctx, in.RoundID)
	if err != nil {
		return Page{}, err
	}
	if !r.clock().Before(round.Deadline) {
		_, _ = r.Store.ExpireRound(ctx, in.RoundID)
		return Page{}, store.ErrExpired
	}
	authority, err := r.Store.VerifyRoundToolCapability(ctx, in.Capability, in.RoundID)
	if err != nil {
		return Page{}, err
	}
	request, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", 1, "tools/call", map[string]any{"name": in.Method, "arguments": args}})
	if err != nil {
		return Page{}, err
	}
	cost, _ := store.RoundOperationCost(store.RoundSearchSource)
	a, created, err := r.Store.ReserveRoundAttempt(ctx, authority.Actor, in.RoundID, store.RoundAttemptInput{RequestKey: in.RequestKey, Operation: store.RoundSearchSource, ResourceID: in.ResourceID, Cost: cost, BoundCapability: in.Capability})
	if err != nil {
		if errors.Is(err, store.ErrExpired) {
			_, _ = r.Store.ExpireRound(ctx, in.RoundID)
		}
		return Page{}, err
	}
	if !created {
		if a.State != store.AttemptSucceeded {
			return Page{}, store.ErrUncertain
		}
		saved, err := r.Store.DiscoveryHTTP(ctx, a.ID)
		if err != nil || saved.RequestJSON != string(request) {
			return Page{}, store.ErrRoundIdempotencyConflict
		}
		var p Page
		if json.Unmarshal(a.Result, &p) != nil {
			return Page{}, store.ErrUncertain
		}
		return p, nil
	}
	if _, err = r.Store.MarkRoundDispatched(ctx, in.RoundID, a.ID); err != nil {
		return Page{}, err
	}
	current, err := r.Store.RoundAttempt(ctx, a.ID)
	if err != nil || current.State != store.AttemptDispatched || current.Generation != a.Generation {
		return Page{}, store.ErrFenced
	}
	url := endpoint
	if r.endpointURL != "" {
		url = r.endpointURL
	}
	preflight := store.DiscoveryHTTP{AttemptID: a.ID, RoundID: in.RoundID, Method: in.Method, RequestJSON: string(request), Endpoint: url, ObservedAt: r.clock().Format(time.RFC3339Nano)}
	if err := r.Store.PrepareDiscoveryHTTP(ctx, preflight); err != nil {
		return Page{}, err
	}
	deadline := r.clock().Add(15 * time.Second)
	if round.Deadline.Before(deadline) {
		deadline = round.Deadline
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(request))
	if err != nil {
		return Page{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if r.Client != nil {
		copy := *r.Client
		copy.Timeout = 15 * time.Second
		copy.CheckRedirect = client.CheckRedirect
		client = &copy
	}
	res, callErr := client.Do(httpReq)
	var body []byte
	status := 0
	code := ""
	if callErr == nil {
		status = res.StatusCode
		body, err = io.ReadAll(io.LimitReader(res.Body, maxBody+1))
		closeErr := res.Body.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			code = "read_failed"
		} else if len(body) > maxBody {
			body = body[:maxBody]
			code = "response_too_large"
		}
	} else {
		code = "transport_failed"
	}
	if code == "" && status != 200 {
		code = "http_" + strconv.Itoa(status)
	}
	var p Page
	if code == "" {
		p, err = parse(body, in.Method, in.Page)
		if err != nil {
			code = "malformed_response"
		}
	}
	observed := r.clock().Format(time.RFC3339Nano)
	sum := sha256.Sum256(body)
	evidence := store.DiscoveryHTTP{AttemptID: a.ID, RoundID: in.RoundID, Method: in.Method, RequestJSON: string(request), Endpoint: url, StatusCode: status, ResponseBody: body, ResponseSHA256: hex.EncodeToString(sum[:]), ObservedAt: observed, ErrorCode: code}
	finishCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if saveErr := r.Store.CompleteDiscoveryHTTP(finishCtx, evidence); saveErr != nil {
		return Page{}, saveErr
	}
	if !r.clock().Before(round.Deadline) {
		_, _ = r.Store.ExpireRound(finishCtx, in.RoundID)
		return Page{}, store.ErrExpired
	}
	if code != "" {
		_, finishErr := r.Store.FinishRoundAttempt(finishCtx, authority.Actor, in.RoundID, a.ID, false, nil, code)
		if finishErr != nil {
			if errors.Is(finishErr, store.ErrExpired) {
				_, _ = r.Store.ExpireRound(finishCtx, in.RoundID)
			}
			return Page{}, finishErr
		}
		if callErr != nil {
			return Page{}, fmt.Errorf("discovery %s: %w", code, callErr)
		}
		return Page{}, errors.New("discovery " + code)
	}
	p.AttemptID = a.ID
	p.Method = in.Method
	p.ResponseSHA256 = evidence.ResponseSHA256
	p.ObservedAt = observed
	encoded, _ := json.Marshal(p)
	if _, err = r.Store.FinishRoundAttempt(finishCtx, authority.Actor, in.RoundID, a.ID, true, encoded, ""); err != nil {
		if errors.Is(err, store.ErrExpired) {
			_, _ = r.Store.ExpireRound(finishCtx, in.RoundID)
		}
		return Page{}, err
	}
	return p, nil
}

func parse(raw []byte, method string, page int) (Page, error) {
	data := raw
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("event:")) {
		var found bool
		for _, line := range bytes.Split(raw, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data: ")) {
				data = line[len("data: "):]
				found = true
				break
			}
		}
		if !found {
			return Page{}, store.ErrInvalid
		}
	}
	var envelope struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Error) > 0 || envelope.Result.IsError || len(envelope.Result.Content) != 1 || envelope.Result.Content[0].Type != "text" {
		return Page{}, store.ErrInvalid
	}
	t := envelope.Result.Content[0].Text
	if t == "" || len(t) > maxText {
		return Page{}, store.ErrInvalid
	}
	p := Page{Text: t, Page: page}
	if method == "search_jobs" {
		m := countPattern.FindStringSubmatch(t)
		if len(m) != 3 {
			return Page{}, store.ErrInvalid
		}
		total, _ := strconv.Atoi(m[1])
		observedPage, _ := strconv.Atoi(m[2])
		if total < 0 || observedPage != page {
			return Page{}, store.ErrInvalid
		}
		p.Total = total
		// The provider's page size is observed from result blocks; the next page
		// remains explicit and is fetched only in another charged request.
		blocks := strings.Count(t, "🔗 **Apply on Himalayas:**")
		if blocks == 0 && total > 0 {
			return Page{}, store.ErrInvalid
		}
		if blocks >= 20 && total > page*20 {
			p.NextPage = page + 1
			p.Omitted = total - blocks
		}
	}
	return p, nil
}
