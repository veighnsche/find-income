// Package collector reads configured public ATS boards in bounded batches and
// submits their sourced postings to the shared durable ingestion queue.
package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const pageLimit = 25
const maxPagesPerRun = 2
const maxResponseBytes = 6 << 20

type IntakeStore interface {
	ClaimDueCollectorBoard(context.Context, time.Time, time.Duration) (store.CollectorBoard, bool, error)
	FinishCollectorBoard(context.Context, store.CollectorBoard, store.CollectorBoardResult, time.Time) (bool, error)
	SubmitIngestion(context.Context, store.Actor, store.IngestionInput) (store.IngestionRequest, bool, error)
}

type Collector struct {
	Store        IntakeStore
	HTTPClient   *http.Client
	PollInterval time.Duration
	Lease        time.Duration
	Now          func() time.Time
	// endpoint is only overridden by package tests. Production always uses the
	// fixed Lever API host for the configured region.
	endpoint func(store.CollectorBoard) string
}

type RunReport struct {
	BoardID    string
	Found      bool
	Submitted  int
	Duplicates int
	NextOffset int
	ErrorCode  string
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Collector) client() *http.Client {
	if c.HTTPClient == nil {
		return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	clone := *c.HTTPClient
	if clone.Timeout <= 0 || clone.Timeout > 15*time.Second {
		clone.Timeout = 15 * time.Second
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

func leverEndpoint(board store.CollectorBoard) string {
	host := "api.lever.co"
	if board.Region == "eu" {
		host = "api.eu.lever.co"
	}
	return "https://" + host + "/v0/postings/" + board.Site
}

func (c *Collector) fetchPage(ctx context.Context, board store.CollectorBoard, offset int) ([]json.RawMessage, string) {
	base := leverEndpoint(board)
	if c.endpoint != nil {
		base = c.endpoint(board)
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, "collector_configuration"
	}
	q := u.Query()
	q.Set("skip", strconv.Itoa(offset))
	q.Set("limit", strconv.Itoa(pageLimit))
	q.Set("mode", "json")
	u.RawQuery = q.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "collector_configuration"
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client().Do(request)
	if err != nil {
		return nil, "lever_network"
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, "lever_rate_limited"
	}
	if response.StatusCode != http.StatusOK {
		return nil, "lever_http_status"
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" &&
		!strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		return nil, "lever_content_type"
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, "lever_read"
	}
	if len(body) > maxResponseBytes {
		return nil, "lever_response_too_large"
	}
	var postings []json.RawMessage
	if err := json.Unmarshal(body, &postings); err != nil || postings == nil || len(postings) > pageLimit {
		return nil, "lever_invalid_response"
	}
	return postings, ""
}

type leverPosting struct {
	ID               string `json:"id"`
	Text             string `json:"text"`
	HostedURL        string `json:"hostedUrl"`
	DescriptionPlain string `json:"descriptionPlain"`
	OpeningPlain     string `json:"openingPlain"`
}

func validateLeverPosting(board store.CollectorBoard, raw json.RawMessage) (leverPosting, string) {
	var posting leverPosting
	if len(raw) == 0 || len(raw) > 200000 || json.Unmarshal(raw, &posting) != nil ||
		!collectorPostingID(posting.ID) || strings.TrimSpace(posting.Text) == "" ||
		(strings.TrimSpace(posting.DescriptionPlain) == "" && strings.TrimSpace(posting.OpeningPlain) == "") {
		return leverPosting{}, "lever_invalid_posting"
	}
	u, err := url.Parse(posting.HostedURL)
	expectedHost := "jobs.lever.co"
	if board.Region == "eu" {
		expectedHost = "jobs.eu.lever.co"
	}
	if err != nil || u.Scheme != "https" || u.Host != expectedHost || u.User != nil ||
		u.Path != "/"+board.Site+"/"+posting.ID || u.RawQuery != "" || u.Fragment != "" {
		return leverPosting{}, "lever_invalid_posting"
	}
	return posting, ""
}

func collectorPostingID(value string) bool {
	if len(value) == 0 || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if char != '-' && char != '_' && (char < '0' || char > '9') &&
			(char < 'A' || char > 'Z') && (char < 'a' || char > 'z') {
			return false
		}
	}
	return true
}

// RunDueOnce scans at most two 25-posting pages from one due board. It stores
// the exact posting JSON supplied by Lever, not a claimed full web page. A
// provider failure is recorded on the board and returned as a safe code.
func (c *Collector) RunDueOnce(ctx context.Context) (RunReport, error) {
	if c.Store == nil || c.Lease <= 0 || c.Lease > 10*time.Minute {
		return RunReport{}, fmt.Errorf("%w: collector store and lease required", store.ErrInvalid)
	}
	board, found, err := c.Store.ClaimDueCollectorBoard(ctx, c.now(), c.Lease)
	if err != nil || !found {
		return RunReport{}, err
	}
	report := RunReport{BoardID: board.ID, Found: true, NextOffset: board.NextOffset}
	if board.Provider != "lever" {
		report.ErrorCode = "collector_unsupported_provider"
	} else {
		for page := 0; page < maxPagesPerRun; page++ {
			postings, code := c.fetchPage(ctx, board, report.NextOffset)
			if code != "" {
				report.ErrorCode = code
				break
			}
			for _, raw := range postings {
				posting, code := validateLeverPosting(board, raw)
				if code != "" {
					report.ErrorCode = code
					break
				}
				digest := sha256.Sum256(raw)
				input := store.IngestionInput{
					Origin: "collector", SourceURL: posting.HostedURL, OriginalText: string(raw),
					ConnectorID: "lever:" + board.ID, ExternalID: posting.ID,
					DiscoveredAt:   c.now().Format(time.RFC3339Nano),
					IdempotencyKey: "lever:" + board.ID + ":" + hex.EncodeToString(digest[:]),
				}
				_, created, err := c.Store.SubmitIngestion(ctx, store.Actor{Kind: "system", ID: "collector:" + board.ID}, input)
				if err != nil {
					// Preserve the cursor and surface database errors to the service.
					report.ErrorCode = "collector_submit"
					break
				}
				if created {
					report.Submitted++
				} else {
					report.Duplicates++
				}
			}
			if report.ErrorCode != "" {
				break
			}
			if len(postings) < pageLimit {
				report.NextOffset = 0
				break
			}
			report.NextOffset += len(postings)
		}
	}
	result := store.CollectorBoardResult{NextOffset: report.NextOffset, ErrorCode: report.ErrorCode}
	if applied, finishErr := c.Store.FinishCollectorBoard(ctx, board, result, c.now()); finishErr != nil {
		return report, finishErr
	} else if !applied {
		return report, store.ErrConflict
	}
	return report, nil
}

func (c *Collector) Run(ctx context.Context) error {
	if c.Store == nil || c.PollInterval <= 0 || c.Lease <= 0 || c.Lease > 10*time.Minute {
		return fmt.Errorf("%w: collector store, poll and lease required", store.ErrInvalid)
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		_, err := c.RunDueOnce(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		// Each poll scans at most one board, including when many are due.
		timer := time.NewTimer(c.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
