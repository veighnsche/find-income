package researchexecute

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ObservedRequest is one outbound request seen at a mediation proxy: method,
// absolute URL, status, byte counts in both directions, and how it was
// handled. Tunneled CONNECT relays report host and bytes only.
type ObservedRequest struct {
	Method    string `json:"method"`
	URL       string `json:"url"`
	Status    int    `json:"status"`
	BytesIn   int64  `json:"bytesIn"`
	BytesOut  int64  `json:"bytesOut"`
	Tunneled  bool   `json:"tunneled,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Blocked   bool   `json:"blocked,omitempty"`
	Error     string `json:"error,omitempty"`
}

// recordingProxy is a minimal forward proxy that mediates one operation's
// subprocess egress. Plain HTTP requests are forwarded through the guarded
// client (per-hop public checks, redirect fencing); CONNECT tunnels resolve
// and fence the target before relaying opaque bytes with host and byte counts
// only. Every request is observed; nothing bypasses the log.
type recordingProxy struct {
	listener net.Listener
	addr     string
	server   *http.Server
	forward  *guardedClient
	maxBytes int64
	maxReqs  int
	count    atomic.Int64

	mu       sync.Mutex
	requests []ObservedRequest
}

// newRecordingProxy binds a loopback-only proxy for one operation.
func newRecordingProxy(forward *guardedClient, maxBytes int64, maxReqs int) (*recordingProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &recordingProxy{listener: listener, addr: listener.Addr().String(),
		forward: forward, maxBytes: maxBytes, maxReqs: maxReqs}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go p.server.Serve(listener) //nolint:errcheck // closed per operation; Serve error is terminal noise
	return p, nil
}

func (p *recordingProxy) close() {
	_ = p.server.Close()
	_ = p.listener.Close()
}

// url returns the proxy URL for child environments and browser flags.
func (p *recordingProxy) url() string { return "http://" + p.addr }

// port returns the loopback port for the sandbox profile.
func (p *recordingProxy) port() string {
	_, port, _ := net.SplitHostPort(p.addr)
	return port
}

func (p *recordingProxy) observed() []ObservedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ObservedRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

func (p *recordingProxy) record(entry ObservedRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, entry)
}

// overBudget refuses requests past MaxRequests with an explicit blocked entry.
func (p *recordingProxy) overBudget(entry *ObservedRequest) bool {
	if p.maxReqs <= 0 {
		return false
	}
	if p.count.Add(1) > int64(p.maxReqs) {
		entry.Blocked = true
		entry.Error = ErrorCodeTooManyRequests
		entry.Status = http.StatusForbidden
		return true
	}
	return false
}

func (p *recordingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}
	entry := ObservedRequest{Method: r.Method, URL: r.URL.String()}
	if p.overBudget(&entry) {
		p.record(entry)
		http.Error(w, "request bound exceeded", http.StatusForbidden)
		return
	}
	target := r.URL
	if !target.IsAbs() {
		entry.Blocked = true
		entry.Error = "relative_uri"
		entry.Status = http.StatusBadRequest
		p.record(entry)
		http.Error(w, "proxy requires absolute URI", http.StatusBadRequest)
		return
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		entry.Blocked = true
		entry.Error = "scheme_forbidden"
		entry.Status = http.StatusForbidden
		p.record(entry)
		http.Error(w, "proxy forbids scheme", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, p.maxBytes+1))
	_ = r.Body.Close()
	if err != nil {
		entry.Blocked = true
		entry.Error = "read_failed"
		entry.Status = http.StatusBadGateway
		p.record(entry)
		http.Error(w, "proxy read failed", http.StatusBadGateway)
		return
	}
	if int64(len(body)) > p.maxBytes {
		entry.Truncated = true
		body = body[:p.maxBytes]
	}
	entry.BytesOut = int64(len(body))
	header := http.Header{}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		header.Set("Content-Type", ct)
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		header.Set("Accept", accept)
	}
	if lang := r.Header.Get("Accept-Language"); lang != "" {
		header.Set("Accept-Language", lang)
	}
	if ua := r.Header.Get("User-Agent"); ua != "" {
		header.Set("User-Agent", ua)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := p.forward.do(ctx, r.Method, target.String(), header, body, p.maxBytes)
	if err != nil {
		entry.Blocked = true
		entry.Error = proxyErrorCode(err)
		entry.Status = http.StatusBadGateway
		p.record(entry)
		http.Error(w, "proxy forward failed", http.StatusBadGateway)
		return
	}
	entry.Status = res.status
	entry.BytesIn = res.bytesIn
	entry.Truncated = entry.Truncated || res.truncated
	p.record(entry)
	if res.contentType != "" {
		w.Header().Set("Content-Type", res.contentType)
	}
	w.WriteHeader(res.status)
	_, _ = w.Write(res.body)
}

// serveConnect relays a fenced CONNECT tunnel: the target host is resolved
// and checked (ALL addresses must be allowed) before any byte flows, and the
// relay counts bytes in both directions under the byte bound.
func (p *recordingProxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Host
	if host == "" {
		host = r.Host
	}
	entry := ObservedRequest{Method: http.MethodConnect, URL: "https://" + host, Tunneled: true}
	if p.overBudget(&entry) {
		p.record(entry)
		http.Error(w, "request bound exceeded", http.StatusForbidden)
		return
	}
	targetHost, targetPort, err := net.SplitHostPort(host)
	if err != nil || !validPort(targetPort) {
		entry.Blocked = true
		entry.Error = "tunnel_target_invalid"
		entry.Status = http.StatusForbidden
		p.record(entry)
		http.Error(w, "tunnel target invalid", http.StatusForbidden)
		return
	}
	ips, err := resolveChecked(r.Context(), p.forward.resolver, targetHost, p.forward.permitLoopback)
	if err != nil {
		entry.Blocked = true
		entry.Error = proxyErrorCode(err)
		entry.Status = http.StatusForbidden
		p.record(entry)
		http.Error(w, "tunnel target forbidden", http.StatusForbidden)
		return
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	upstream, err := dialer.DialContext(r.Context(), "tcp", net.JoinHostPort(ips[0].String(), targetPort))
	if err != nil {
		entry.Blocked = true
		entry.Error = "tunnel_failed"
		entry.Status = http.StatusBadGateway
		p.record(entry)
		http.Error(w, "tunnel failed", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		entry.Blocked = true
		entry.Error = "tunnel_unsupported"
		entry.Status = http.StatusInternalServerError
		p.record(entry)
		http.Error(w, "tunnel unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		entry.Blocked = true
		entry.Error = "tunnel_failed"
		entry.Status = http.StatusBadGateway
		p.record(entry)
		http.Error(w, "tunnel failed", http.StatusBadGateway)
		return
	}
	defer client.Close()
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	up, down := relayBounded(client, upstream, p.maxBytes)
	entry.BytesOut, entry.BytesIn = up, down
	entry.Status = 200
	if up >= p.maxBytes || down >= p.maxBytes {
		entry.Truncated = true
	}
	p.record(entry)
}

// relayBounded copies both directions until EOF, error, or the per-direction
// byte bound, then half-closes.
func relayBounded(a, b net.Conn, bound int64) (ab, ba int64) {
	done := make(chan struct{}, 2)
	copyOne := func(dst, src net.Conn, bound int64, out *int64) {
		defer func() { done <- struct{}{} }()
		n, _ := io.Copy(dst, io.LimitReader(src, bound))
		*out = n
		closeWrite(dst)
	}
	var up, down int64
	go copyOne(b, a, bound, &up)
	go copyOne(a, b, bound, &down)
	<-done
	<-done
	return up, down
}

func validPort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	n := 0
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
		n = n*10 + int(port[i]-'0')
	}
	return n > 0 && n <= 65535
}

func closeWrite(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	if tlsConn, ok := c.(*tls.Conn); ok {
		_ = tlsConn.CloseWrite()
	}
}

func proxyErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errDestinationForbidden) || errors.Is(err, errAddressNotPublic):
		return "destination_forbidden"
	case errors.Is(err, errTooManyRedirects):
		return "redirect_limit"
	default:
		return "request_failed"
	}
}
