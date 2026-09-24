// T02 bounded capability proof backend.
//
// ProofResearch is an instrumented research backend used only to prove the
// runtime path: pre-dispatch interception (allowance, Stop fence, freshness),
// authentic capture (content-addressed bodies under a disposable root),
// cancellation, usage reporting and uncertain-outcome recovery. It owns HTTP
// dispatch for search/fetch/API operations; browser and executable research
// have their own mediated boundaries in proof_research_exec.go.
//
// This is proof infrastructure, not the production executor (T16). It keeps a
// disposable file-backed index under RootDir and never touches the
// application store. PermitLoopback exists only so instrumented httptest
// fixtures (127.0.0.1) can be exercised; production dispatch must refuse
// private-network destinations.
package codexservice

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrProofStopped   = errors.New("proof research stopped")
	ErrProofBudget    = errors.New("proof research allowance exhausted")
	ErrProofUncertain = errors.New("proof research outcome uncertain; reconcile before retry")
	ErrProofForbidden = errors.New("proof research destination forbidden")
	ErrProofTruncated = errors.New("proof research body exceeds bound")
)

// Proof operation names.
const (
	ProofSearch  = "search"
	ProofFetch   = "fetch"
	ProofAPI     = "api"
	ProofBrowser = "browser"
	ProofExecute = "execute"
)

// Proof receipt statuses.
const (
	ProofOK        = "ok"
	ProofFailed    = "failed"
	ProofCanceled  = "canceled"
	ProofUncertain = "uncertain"
	ProofLate      = "late"
	ProofReused    = "reused"
	ProofShared    = "shared"
)

type ProofConfig struct {
	RootDir       string
	MaxActions    int
	MaxConcurrent int
	MaxBodyBytes  int64
	OpTimeout     time.Duration
	// PermitLoopback allows loopback/private fixture destinations. Test-only.
	PermitLoopback bool
}

func (c ProofConfig) withDefaults() ProofConfig {
	if c.MaxActions <= 0 {
		c.MaxActions = 60
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 2
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 1 << 20
	}
	if c.OpTimeout <= 0 {
		c.OpTimeout = 30 * time.Second
	}
	return c
}

// ProofReceipt is the trusted execution receipt for one proof operation. It is
// issued by this backend, never by the model, and binds to an immutable
// capture through CaptureID.
type ProofReceipt struct {
	ID          string    `json:"id"`
	Operation   string    `json:"operation"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
	CaptureID   string    `json:"captureId,omitempty"`
	Attempts    int       `json:"attempts"`
	Subrequests int       `json:"subrequests"`
	BytesIn     int64     `json:"bytesIn"`
	BytesOut    int64     `json:"bytesOut"`
	Unknown     bool      `json:"unknownUsage"`
	Truncated   bool      `json:"truncated"`
	ErrorCode   string    `json:"errorCode,omitempty"`
	FinalURL    string    `json:"finalUrl,omitempty"`
	Redirects   []string  `json:"redirects,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	EndedAt     time.Time `json:"endedAt"`
}

// ProofCapture is the immutable server-side record of one dispatch.
type ProofCapture struct {
	ID          string            `json:"id"`
	Operation   string            `json:"operation"`
	Fingerprint string            `json:"fingerprint"`
	Request     json.RawMessage   `json:"request"`
	Status      int               `json:"status,omitempty"`
	FinalURL    string            `json:"finalUrl,omitempty"`
	Redirects   []string          `json:"redirects,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	Complete    bool              `json:"complete"`
	ObservedAt  time.Time         `json:"observedAt"`
	DurationMs  int64             `json:"durationMs"`
	SHA256      string            `json:"sha256"`
	Bytes       int64             `json:"bytes"`
	Extra       map[string]string `json:"extra,omitempty"`
}

// ProofUsage is the run ledger: enforced allowance, observed usage and an
// explicit unknown flag. Unknown is never silently false after uncertain work.
type ProofUsage struct {
	Actions   int   `json:"actions"`
	BytesIn   int64 `json:"bytesIn"`
	BytesOut  int64 `json:"bytesOut"`
	Unknown   bool  `json:"unknown"`
	Allowance int   `json:"allowance"`
	Remaining int   `json:"remaining"`
	InFlight  int   `json:"inFlight"`
}

type proofCall struct {
	done    chan struct{}
	receipt ProofReceipt
	err     error
}

type proofIndex struct {
	Fresh     map[string]string `json:"fresh"`
	Uncertain map[string]string `json:"uncertain"`
}

// ProofResearch is safe for concurrent use.
type ProofResearch struct {
	cfg    ProofConfig
	client *http.Client

	mu        sync.Mutex
	stopped   bool
	actions   int
	bytesIn   int64
	bytesOut  int64
	unknown   bool
	inflight  map[string]*proofCall
	fresh     map[string]string
	uncertain map[string]string
	sem       chan struct{}
	stopCh    chan struct{}
}

func OpenProofResearch(cfg ProofConfig) (*ProofResearch, error) {
	cfg = cfg.withDefaults()
	if cfg.RootDir == "" {
		return nil, errors.New("proof research requires a root directory")
	}
	for _, dir := range []string{"receipts", "captures", "bodies"} {
		if err := os.MkdirAll(filepath.Join(cfg.RootDir, dir), 0700); err != nil {
			return nil, err
		}
	}
	p := &ProofResearch{
		cfg:       cfg,
		inflight:  make(map[string]*proofCall),
		fresh:     make(map[string]string),
		uncertain: make(map[string]string),
		sem:       make(chan struct{}, cfg.MaxConcurrent),
		stopCh:    make(chan struct{}),
	}
	if raw, err := os.ReadFile(filepath.Join(cfg.RootDir, "index.json")); err == nil {
		var index proofIndex
		if json.Unmarshal(raw, &index) == nil {
			if index.Fresh != nil {
				p.fresh = index.Fresh
			}
			if index.Uncertain != nil {
				p.uncertain = index.Uncertain
			}
		}
	}
	p.client = &http.Client{
		Transport: &http.Transport{
			DialContext:           p.dialContext,
			ResponseHeaderTimeout: cfg.OpTimeout,
			DisableCompression:    false,
			Proxy:                 nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if chain, ok := req.Context().Value(redirectKey{}).(*[]string); ok && chain != nil {
				*chain = append(*chain, req.URL.String())
			}
			return nil
		},
		Jar: proofJar{},
	}
	return p, nil
}

type redirectKey struct{}

type proofJar struct{}

func (proofJar) SetCookies(*url.URL, []*http.Cookie) {}
func (proofJar) Cookies(*url.URL) []*http.Cookie     { return nil }

// Stop fences new dispatch and cancels in-flight operations. It never blocks.
func (p *ProofResearch) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stopped {
		p.stopped = true
		close(p.stopCh)
	}
}

func (p *ProofResearch) Stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

func (p *ProofResearch) Usage() ProofUsage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ProofUsage{Actions: p.actions, BytesIn: p.bytesIn, BytesOut: p.bytesOut,
		Unknown: p.unknown, Allowance: p.cfg.MaxActions, Remaining: p.cfg.MaxActions - p.actions,
		InFlight: len(p.inflight)}
}

// AcknowledgeUncertain permits one explicit retry after the caller has
// reconciled the prior uncertain receipt. There is no automatic replay.
func (p *ProofResearch) AcknowledgeUncertain(fingerprint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.uncertain, fingerprint)
	p.saveIndexLocked()
}

func (p *ProofResearch) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !p.cfg.PermitLoopback && !isPublicIP(ip) {
			return nil, ErrProofForbidden
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if !p.cfg.PermitLoopback {
		for _, ip := range ips {
			if !isPublicIP(ip) {
				return nil, ErrProofForbidden
			}
		}
	}
	if port == "" {
		port = "443"
	}
	var last error
	for _, ip := range ips {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = ErrProofForbidden
	}
	return nil, last
}

func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		// Carrier-grade NAT and documentation ranges are not public targets.
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return false
		}
		if ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2 {
			return false
		}
		if ip4[0] == 198 && (ip4[1] == 51 || ip4[1] == 18) && ip4[2] == 0 {
			return false
		}
		if ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113 {
			return false
		}
	}
	return true
}

func proofID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func fingerprintParts(operation string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(operation))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func encodeParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(params[k]))
	}
	return b.String()
}

// dispatch runs fn under the shared interception rules: Stop fence, freshness
// reuse, uncertain reconciliation, single-flight claims and allowance.
func (p *ProofResearch) dispatch(ctx context.Context, fingerprint string, fn func(ctx context.Context) (ProofReceipt, error)) (ProofReceipt, error) {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return ProofReceipt{}, ErrProofStopped
	}
	if captureID, ok := p.fresh[fingerprint]; ok {
		p.mu.Unlock()
		now := time.Now()
		return ProofReceipt{ID: proofID(), Fingerprint: fingerprint, Status: ProofReused,
			CaptureID: captureID, StartedAt: now, EndedAt: now}, nil
	}
	if receiptID, ok := p.uncertain[fingerprint]; ok {
		p.mu.Unlock()
		return ProofReceipt{Fingerprint: fingerprint, Status: ProofUncertain, ErrorCode: receiptID}, ErrProofUncertain
	}
	if call, ok := p.inflight[fingerprint]; ok {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			now := time.Now()
			return ProofReceipt{ID: proofID(), Fingerprint: fingerprint, Status: ProofCanceled,
				ErrorCode: "waiter_canceled", StartedAt: now, EndedAt: now}, ctx.Err()
		case <-call.done:
			shared := call.receipt
			shared.ID = proofID()
			shared.Status = ProofShared
			return shared, call.err
		}
	}
	if p.actions >= p.cfg.MaxActions {
		p.mu.Unlock()
		return ProofReceipt{}, ErrProofBudget
	}
	p.actions++
	call := &proofCall{done: make(chan struct{})}
	p.inflight[fingerprint] = call
	p.mu.Unlock()

	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		p.finishFlight(fingerprint, call, ProofReceipt{}, ctx.Err())
		now := time.Now()
		return ProofReceipt{ID: proofID(), Fingerprint: fingerprint, Status: ProofCanceled,
			ErrorCode: "queue_canceled", StartedAt: now, EndedAt: now}, ctx.Err()
	case <-p.stopCh:
		p.finishFlight(fingerprint, call, ProofReceipt{}, ErrProofStopped)
		return ProofReceipt{}, ErrProofStopped
	}
	defer func() { <-p.sem }()

	opCtx, cancel := context.WithTimeout(ctx, p.cfg.OpTimeout)
	defer cancel()
	stopCtx, stopCancel := context.WithCancel(opCtx)
	go func() {
		select {
		case <-p.stopCh:
			stopCancel()
		case <-stopCtx.Done():
		}
	}()
	defer stopCancel()

	receipt, err := fn(stopCtx)
	if stopCtx.Err() != nil && ctx.Err() == nil && p.Stopped() {
		// Stopped mid-flight: retain what completed late without permitting
		// new dispatch. The outcome stays explicit, never a false success.
		if err == nil {
			receipt.Status = ProofLate
			receipt.Unknown = true
		} else {
			receipt.Status = ProofUncertain
			receipt.Unknown = true
			receipt.ErrorCode = "stopped_in_flight"
		}
		err = nil
	} else if err != nil && stopCtx.Err() != nil {
		receipt.Status = ProofUncertain
		receipt.Unknown = true
		if receipt.ErrorCode == "" {
			receipt.ErrorCode = "canceled_in_flight"
		}
		err = nil
	}
	p.mu.Lock()
	p.bytesIn += receipt.BytesIn
	p.bytesOut += receipt.BytesOut
	if receipt.Unknown {
		p.unknown = true
	}
	persistErr := p.persistReceiptLocked(receipt)
	if receipt.Status == ProofOK || receipt.Status == ProofLate {
		if receipt.CaptureID != "" {
			p.fresh[fingerprint] = receipt.CaptureID
		}
	} else if receipt.Status == ProofUncertain {
		p.uncertain[fingerprint] = receipt.ID
	}
	p.saveIndexLocked()
	p.mu.Unlock()
	if persistErr != nil && err == nil {
		receipt.Status = ProofUncertain
		receipt.Unknown = true
		receipt.ErrorCode = "receipt_persist_failed"
		p.mu.Lock()
		p.unknown = true
		p.uncertain[fingerprint] = receipt.ID
		p.saveIndexLocked()
		p.mu.Unlock()
		err = nil
	}
	p.finishFlight(fingerprint, call, receipt, err)
	return receipt, err
}

func (p *ProofResearch) finishFlight(fingerprint string, call *proofCall, receipt ProofReceipt, err error) {
	p.mu.Lock()
	if p.inflight[fingerprint] == call {
		delete(p.inflight, fingerprint)
	}
	p.mu.Unlock()
	call.receipt, call.err = receipt, err
	close(call.done)
}

func (p *ProofResearch) persistReceiptLocked(receipt ProofReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.cfg.RootDir, "receipts", receipt.ID+".json"), raw, 0600)
}

func (p *ProofResearch) saveIndexLocked() {
	raw, err := json.Marshal(proofIndex{Fresh: p.fresh, Uncertain: p.uncertain})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(p.cfg.RootDir, "index.json"), raw, 0600)
}

func (p *ProofResearch) storeCapture(operation, fingerprint string, request any, status int, finalURL string, redirects []string, contentType string, body []byte, complete bool, observedAt time.Time, duration time.Duration, extra map[string]string) (ProofCapture, error) {
	sum := sha256.Sum256(body)
	capture := ProofCapture{
		ID: hex.EncodeToString(sum[:]), Operation: operation, Fingerprint: fingerprint,
		Status: status, FinalURL: finalURL, Redirects: redirects, ContentType: contentType,
		Complete: complete, ObservedAt: observedAt, DurationMs: duration.Milliseconds(),
		SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)), Extra: extra,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return ProofCapture{}, err
	}
	capture.Request = raw
	meta, err := json.Marshal(capture)
	if err != nil {
		return ProofCapture{}, err
	}
	if err := os.WriteFile(filepath.Join(p.cfg.RootDir, "captures", capture.ID+".json"), meta, 0600); err != nil {
		return ProofCapture{}, err
	}
	if err := os.WriteFile(filepath.Join(p.cfg.RootDir, "bodies", capture.ID+".bin"), body, 0600); err != nil {
		return ProofCapture{}, err
	}
	return capture, nil
}

// CaptureBody returns the immutable captured bytes for a receipt capture ID.
func (p *ProofResearch) CaptureBody(captureID string) ([]byte, error) {
	if captureID == "" || strings.Contains(captureID, "/") || len(captureID) != 64 {
		return nil, errors.New("proof research: invalid capture reference")
	}
	raw, err := os.ReadFile(filepath.Join(p.cfg.RootDir, "bodies", captureID+".bin"))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != captureID {
		return nil, errors.New("proof research: capture integrity mismatch")
	}
	return raw, nil
}

// CaptureMeta returns the immutable capture record for a capture ID.
func (p *ProofResearch) CaptureMeta(captureID string) (ProofCapture, error) {
	var capture ProofCapture
	if captureID == "" || strings.Contains(captureID, "/") || len(captureID) != 64 {
		return capture, errors.New("proof research: invalid capture reference")
	}
	raw, err := os.ReadFile(filepath.Join(p.cfg.RootDir, "captures", captureID+".json"))
	if err != nil {
		return capture, err
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		return ProofCapture{}, err
	}
	return capture, nil
}

type proofHTTPResult struct {
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

func (p *ProofResearch) doHTTP(ctx context.Context, method, rawURL string, params map[string]string, body []byte, contentType string) (proofHTTPResult, error) {
	var out proofHTTPResult
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return out, fmt.Errorf("%w: invalid URL", ErrProofForbidden)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return out, fmt.Errorf("%w: scheme %q", ErrProofForbidden, parsed.Scheme)
	}
	if parsed.Host == "" {
		return out, fmt.Errorf("%w: missing host", ErrProofForbidden)
	}
	query := parsed.Query()
	for k, v := range params {
		query.Set(k, v)
	}
	parsed.RawQuery = query.Encode()
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), reader)
	if err != nil {
		return out, err
	}
	if contentType != "" && len(body) > 0 {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "find-income-t02-proof/1.0")
	var chain []string
	ctx = context.WithValue(ctx, redirectKey{}, &chain)
	req = req.WithContext(ctx)
	gotFirst := false
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() { gotFirst = true },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := p.client.Do(req)
	_ = gotFirst
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	out.status = resp.StatusCode
	out.finalURL = resp.Request.URL.String()
	out.redirects = chain
	out.contentType = resp.Header.Get("Content-Type")
	out.subrequests = len(chain) + 1
	out.bytesOut = int64(len(body))
	limited := io.LimitReader(resp.Body, p.cfg.MaxBodyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return out, err
	}
	if int64(len(data)) > p.cfg.MaxBodyBytes {
		out.truncated = true
		data = data[:p.cfg.MaxBodyBytes]
	}
	out.body = data
	out.bytesIn = int64(len(data))
	out.duration = time.Since(start)
	return out, nil
}

// Search issues an unseeded-style query: the caller supplies the base URL,
// query text and parameters. The exact descriptor is fingerprinted for reuse.
func (p *ProofResearch) Search(ctx context.Context, baseURL, query string, params map[string]string) (ProofReceipt, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(query) == "" {
		return ProofReceipt{}, errors.New("proof research: search requires base URL and query")
	}
	merged := map[string]string{"q": query}
	for k, v := range params {
		merged[k] = v
	}
	fp := fingerprintParts(ProofSearch, "GET", baseURL, encodeParams(merged))
	now := time.Now()
	return p.dispatch(ctx, fp, func(ctx context.Context) (ProofReceipt, error) {
		res, err := p.doHTTP(ctx, http.MethodGet, baseURL, merged, nil, "")
		receipt := ProofReceipt{ID: proofID(), Operation: ProofSearch, Fingerprint: fp,
			StartedAt: now, Attempts: 1, Subrequests: res.subrequests,
			BytesIn: res.bytesIn, BytesOut: res.bytesOut, FinalURL: res.finalURL, Redirects: res.redirects}
		if err != nil {
			receipt.EndedAt = time.Now()
			receipt.Status = ProofFailed
			receipt.ErrorCode = proofErrorCode(err)
			return receipt, err
		}
		capture, err := p.storeCapture(ProofSearch, fp,
			map[string]any{"method": "GET", "base": baseURL, "params": merged, "final": res.finalURL},
			res.status, res.finalURL, res.redirects, res.contentType, res.body, !res.truncated, now, res.duration, nil)
		if err != nil {
			receipt.EndedAt = time.Now()
			receipt.Status = ProofFailed
			receipt.ErrorCode = "capture_failed"
			return receipt, err
		}
		receipt.EndedAt = time.Now()
		receipt.Status = ProofOK
		receipt.CaptureID = capture.ID
		receipt.Truncated = res.truncated
		return receipt, nil
	})
}

// Fetch retrieves an arbitrary public URL, recording redirects and bytes.
func (p *ProofResearch) Fetch(ctx context.Context, rawURL string) (ProofReceipt, error) {
	if strings.TrimSpace(rawURL) == "" {
		return ProofReceipt{}, errors.New("proof research: fetch requires a URL")
	}
	fp := fingerprintParts(ProofFetch, "GET", rawURL)
	now := time.Now()
	return p.dispatch(ctx, fp, func(ctx context.Context) (ProofReceipt, error) {
		res, err := p.doHTTP(ctx, http.MethodGet, rawURL, nil, nil, "")
		receipt := ProofReceipt{ID: proofID(), Operation: ProofFetch, Fingerprint: fp,
			StartedAt: now, Attempts: 1, Subrequests: res.subrequests,
			BytesIn: res.bytesIn, BytesOut: res.bytesOut, FinalURL: res.finalURL, Redirects: res.redirects}
		if err != nil {
			receipt.EndedAt = time.Now()
			receipt.Status = ProofFailed
			receipt.ErrorCode = proofErrorCode(err)
			return receipt, err
		}
		capture, err := p.storeCapture(ProofFetch, fp,
			map[string]any{"method": "GET", "url": rawURL, "final": res.finalURL},
			res.status, res.finalURL, res.redirects, res.contentType, res.body, !res.truncated, now, res.duration, nil)
		if err != nil {
			receipt.EndedAt = time.Now()
			receipt.Status = ProofFailed
			receipt.ErrorCode = "capture_failed"
			return receipt, err
		}
		receipt.EndedAt = time.Now()
		receipt.Status = ProofOK
		receipt.CaptureID = capture.ID
		receipt.Truncated = res.truncated
		return receipt, nil
	})
}

// API performs a general read-only API call: GET or POST with caller-selected
// URL, parameters, body and content type. Read-only POST queries are allowed;
// state-changing verbs are rejected without dispatch.
func (p *ProofResearch) API(ctx context.Context, method, rawURL string, params map[string]string, body []byte, contentType string) (ProofReceipt, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method != http.MethodGet && method != http.MethodPost {
		return ProofReceipt{}, fmt.Errorf("%w: method %q is not read-only", ErrProofForbidden, method)
	}
	if strings.TrimSpace(rawURL) == "" {
		return ProofReceipt{}, errors.New("proof research: API call requires a URL")
	}
	bodySum := sha256.Sum256(body)
	fp := fingerprintParts(ProofAPI, method, rawURL, encodeParams(params), contentType, hex.EncodeToString(bodySum[:]))
	now := time.Now()
	return p.dispatch(ctx, fp, func(ctx context.Context) (ProofReceipt, error) {
		res, err := p.doHTTP(ctx, method, rawURL, params, body, contentType)
		receipt := ProofReceipt{ID: proofID(), Operation: ProofAPI, Fingerprint: fp,
			StartedAt: now, Attempts: 1, Subrequests: res.subrequests,
			BytesIn: res.bytesIn, BytesOut: int64(len(body)),
			FinalURL: res.finalURL, Redirects: res.redirects}
		if err != nil {
			receipt.EndedAt = time.Now()
			receipt.Status = ProofFailed
			receipt.ErrorCode = proofErrorCode(err)
			return receipt, err
		}
		capture, err := p.storeCapture(ProofAPI, fp,
			map[string]any{"method": method, "url": rawURL, "params": params, "contentType": contentType, "bodySha256": hex.EncodeToString(bodySum[:]), "final": res.finalURL},
			res.status, res.finalURL, res.redirects, res.contentType, res.body, !res.truncated, now, res.duration, nil)
		if err != nil {
			receipt.EndedAt = time.Now()
			receipt.Status = ProofFailed
			receipt.ErrorCode = "capture_failed"
			return receipt, err
		}
		receipt.EndedAt = time.Now()
		receipt.Status = ProofOK
		receipt.CaptureID = capture.ID
		receipt.Truncated = res.truncated
		return receipt, nil
	})
}

func proofErrorCode(err error) string {
	if errors.Is(err, ErrProofForbidden) {
		return "destination_forbidden"
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "request_timeout"
		}
		return "request_failed"
	}
	return "request_failed"
}
