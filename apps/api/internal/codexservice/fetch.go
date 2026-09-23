package codexservice

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}
func publicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		(u.Port() != "" && u.Port() != "443" && u.Port() != "80") {
		return errTool
	}
	return nil
}
func fetchPublic(ctx context.Context, raw string) (string, error) {
	if publicURL(raw) != nil {
		return "", errTool
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errTool
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errTool
		}
		for _, ip := range ips {
			if !publicIP(ip.IP) {
				return nil, errTool
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errTool
		}
		return publicURL(req.URL.String())
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", errTool
	}
	req.Header.Set("Accept", "text/html,text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return "", errTool
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errTool
	}
	media := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(media, "text/html") && !strings.HasPrefix(media, "text/plain") {
		return "", errTool
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200001))
	if err != nil || len(data) > 200000 || !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
		return "", errTool
	}
	// Preserve the returned text/HTML verbatim. The model extracts from this
	// snapshot; it never substitutes an inferred description for the source.
	return string(data), nil
}
func (s *Service) fetchTool(ctx context.Context, args capabilityArgs) (map[string]any, error) {
	return s.withScope(ctx, args.Capability, func(ctx context.Context, scope *toolScope, item store.IngestionRequest) (map[string]any, error) {
		if item.OriginalText != "" {
			return map[string]any{"originalText": item.OriginalText}, nil
		}
		if item.SourceURL == "" {
			return nil, errTool
		}
		text, err := fetchPublic(ctx, item.SourceURL)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err = s.db.MarkIngestionNeedsText(ctx, scope.claim); err != nil {
				return nil, err
			}
			return map[string]any{"status": "needs_text", "message": "The URL did not provide accessible vacancy text. Ask for the full text."}, nil
		}
		if err = s.db.AttachIngestionText(ctx, scope.claim, text); err != nil {
			return nil, err
		}
		return map[string]any{"originalText": text, "sourceUrl": item.SourceURL}, nil
	})
}
