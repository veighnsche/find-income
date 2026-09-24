package researchexecute

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// redirectChainKey carries the redirect witness list through the request
// context so the redirect hook records every hop URL.
type redirectChainKey struct{}

// httpResult is one fully observed HTTP exchange.
type httpResult struct {
	status      int
	finalURL    string
	redirects   []string
	contentType string
	body        []byte
	truncated   bool
	subrequests int
	bytesIn     int64
	bytesOut    int64
	duration    time.Duration
}

// guardedClient is an http.Client whose dial path pins to checked public
// addresses and whose redirect hook revalidates every hop.
type guardedClient struct {
	client         *http.Client
	maxRedirects   int
	permitLoopback bool
	resolver       resolver
}

// newGuardedClient builds the policy-enforcing client. redirectsTo records
// each followed hop URL; overLimit is returned when the redirect cap trips.
func newGuardedClient(maxRedirects int, permitLoopback bool, r resolver) *guardedClient {
	g := &guardedClient{maxRedirects: maxRedirects, permitLoopback: permitLoopback, resolver: r}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 10 * time.Second}
	g.client = &http.Client{
		Transport: &http.Transport{
			DialContext:           pinnedDialer(r, permitLoopback, dialer),
			Proxy:                 nil,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			DisableCompression:    false,
			MaxConnsPerHost:       4,
		},
		CheckRedirect: g.checkRedirect,
		Jar:           nil, // no cookie jar: every request carries exactly what the descriptor says
	}
	return g
}

func (g *guardedClient) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= g.maxRedirects {
		return fmt.Errorf("%w: more than %d redirects", errTooManyRedirects, g.maxRedirects)
	}
	next := req.URL.String()
	// Per-hop revalidation (plan §8): scheme, literal-IP fencing and fresh
	// DNS resolution are all re-checked before the hop is followed.
	if _, err := checkURLResolves(req.Context(), g.resolver, next, g.permitLoopback); err != nil {
		return err
	}
	if chain, ok := req.Context().Value(redirectChainKey{}).(*[]string); ok && chain != nil {
		*chain = append(*chain, next)
	}
	return nil
}

var errTooManyRedirects = fmt.Errorf("redirect limit reached")

// do issues one bounded request. maxBytes caps the response body with an
// explicit truncated flag (never hidden truncation); the body is always read
// to the cap even on error statuses so byte accounting stays honest.
func (g *guardedClient) do(ctx context.Context, method, rawURL string, header http.Header, body []byte, maxBytes int64) (httpResult, error) {
	var out httpResult
	u, err := checkURLResolves(ctx, g.resolver, rawURL, g.permitLoopback)
	if err != nil {
		return out, err
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return out, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "find-income-research/1.0")
	}
	var chain []string
	req = req.WithContext(context.WithValue(req.Context(), redirectChainKey{}, &chain))
	out.bytesOut = int64(len(body))
	resp, err := g.client.Do(req)
	out.redirects = chain
	out.subrequests = len(chain) + 1
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	out.status = resp.StatusCode
	out.finalURL = resp.Request.URL.String()
	out.contentType = resp.Header.Get("Content-Type")
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return out, err
	}
	if int64(len(data)) > maxBytes {
		out.truncated = true
		data = data[:maxBytes]
	}
	out.body = data
	out.bytesIn = int64(len(data))
	out.duration = time.Since(start)
	return out, nil
}

// buildQueryURL renders Params in order (repeats preserved), then non-empty
// pagination fields, onto the base URL's existing query.
func buildQueryURL(base string, params [][2]string, page, cursor, limit string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if _, err := url.ParseQuery(u.RawQuery); err != nil {
		return "", err
	}
	// url.Values.Encode sorts keys; to preserve descriptor order exactly, the
	// merged query is encoded manually below.
	var b strings.Builder
	b.WriteString(u.RawQuery)
	add := func(k, v string) {
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(v))
	}
	for _, p := range params {
		add(p[0], p[1])
	}
	if page != "" {
		add("page", page)
	}
	if cursor != "" {
		add("cursor", cursor)
	}
	if limit != "" {
		add("limit", limit)
	}
	u.RawQuery = b.String()
	return u.String(), nil
}
