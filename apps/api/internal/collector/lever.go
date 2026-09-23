// Package collector acquires exact public ATS postings for a commissioned
// round. The caller owns round authority, allowances and durable staging.
package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
const maxResponseBytes = 6 << 20

type Collector struct {
	HTTPClient *http.Client
	Now        func() time.Time
	// endpoint is only overridden by package tests. Production always uses the
	// fixed Lever API host for the configured region.
	endpoint func(store.CollectorBoard) string
}

// Cursor is a serialized page boundary. Pending keeps the exact unread
// response items if an item allowance stops in the middle of a page.
type Cursor struct {
	BoardID    string           `json:"boardId"`
	NextOffset int              `json:"nextOffset"`
	Pending    []PendingPosting `json:"pending,omitempty"`
	EndOfBoard bool             `json:"endOfBoard,omitempty"`
}

type PendingPosting struct {
	Raw        []byte `json:"raw"`
	ObservedAt string `json:"observedAt"`
}

type Request struct {
	Board    store.CollectorBoard
	Cursor   Cursor
	MaxPages int // reserved source-page requests, including failed responses
	MaxItems int // reserved response items, including invalid postings
}

type StagedPosting struct {
	Provider      string `json:"provider"`
	BoardID       string `json:"boardId"`
	ExternalID    string `json:"externalId"`
	SourceURL     string `json:"sourceUrl"`
	OriginalText  []byte `json:"originalText"`
	ContentSHA256 string `json:"contentSha256"`
	ObservedAt    string `json:"observedAt"`
}

type Batch struct {
	Postings      []StagedPosting `json:"postings"`
	Next          *Cursor         `json:"next,omitempty"`
	PagesFetched  int             `json:"pagesFetched"`
	ItemsExamined int             `json:"itemsExamined"`
	Rejected      int             `json:"rejected"`
	ErrorCode     string          `json:"errorCode,omitempty"`
	WarningCode   string          `json:"warningCode,omitempty"`
	Interrupted   bool            `json:"interrupted,omitempty"`
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

// AcquireLever performs only the requests and item reads explicitly allowed
// by this invocation. A non-nil Next is serializable for the next commission.
// Provider errors retain the cursor and never imply a role has closed.
func (c *Collector) AcquireLever(ctx context.Context, request Request) (Batch, error) {
	board := request.Board
	if board.ID == "" || board.Provider != "lever" || board.Site == "" ||
		(board.Region != "global" && board.Region != "eu") ||
		request.MaxPages < 0 || request.MaxItems < 0 ||
		(request.MaxPages == 0 && request.MaxItems == 0) ||
		request.Cursor.NextOffset < 0 || len(request.Cursor.Pending) > pageLimit ||
		(request.Cursor.BoardID != "" && request.Cursor.BoardID != board.ID) ||
		(request.Cursor.BoardID == "" && (request.Cursor.NextOffset != 0 || len(request.Cursor.Pending) != 0 || request.Cursor.EndOfBoard)) {
		return Batch{}, fmt.Errorf("%w: invalid commissioned Lever request", store.ErrInvalid)
	}
	if request.MaxItems > 0 && request.MaxPages == 0 && len(request.Cursor.Pending) == 0 {
		return Batch{}, fmt.Errorf("%w: page allowance required", store.ErrInvalid)
	}
	cursor := request.Cursor
	cursor.BoardID = board.ID
	batch := Batch{Postings: []StagedPosting{}, Next: &cursor}
	for batch.ItemsExamined < request.MaxItems {
		if ctx.Err() != nil {
			batch.Interrupted = true
			return batch, nil
		}
		if len(cursor.Pending) == 0 {
			if cursor.EndOfBoard {
				batch.Next = nil
				return batch, nil
			}
			if batch.PagesFetched >= request.MaxPages {
				return batch, nil
			}
			rawPostings, code := c.fetchPage(ctx, board, cursor.NextOffset)
			batch.PagesFetched++
			if ctx.Err() != nil {
				batch.Interrupted = true
				return batch, nil
			}
			if code != "" {
				batch.ErrorCode = code
				return batch, nil
			}
			observed := c.now().Format(time.RFC3339Nano)
			for _, raw := range rawPostings {
				cursor.Pending = append(cursor.Pending, PendingPosting{Raw: append([]byte(nil), raw...), ObservedAt: observed})
			}
			cursor.NextOffset += len(rawPostings)
			cursor.EndOfBoard = len(rawPostings) < pageLimit
			if len(cursor.Pending) == 0 {
				batch.Next = nil
				return batch, nil
			}
		}
		if ctx.Err() != nil {
			batch.Interrupted = true
			return batch, nil
		}
		entry := cursor.Pending[0]
		cursor.Pending = cursor.Pending[1:]
		batch.ItemsExamined++
		posting, code := validateLeverPosting(board, json.RawMessage(entry.Raw))
		if code != "" {
			batch.Rejected++
			batch.WarningCode = code
			continue
		}
		digest := sha256.Sum256(entry.Raw)
		batch.Postings = append(batch.Postings, StagedPosting{
			Provider: "lever", BoardID: board.ID, ExternalID: posting.ID,
			SourceURL: posting.HostedURL, OriginalText: append([]byte(nil), entry.Raw...),
			ContentSHA256: hex.EncodeToString(digest[:]), ObservedAt: entry.ObservedAt,
		})
	}
	if len(cursor.Pending) == 0 && cursor.EndOfBoard {
		batch.Next = nil
	}
	return batch, nil
}
