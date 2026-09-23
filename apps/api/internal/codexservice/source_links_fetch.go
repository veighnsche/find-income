package codexservice

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const maxSourcePageBytes = 1 << 20
const maxSourceLinks = 64
const maxSourceLinkOffset = 1024
const sourceFetchTimeout = 10 * time.Second

var errSourceURL = errors.New("invalid official source URL")
var errSourceAddress = errors.New("source address is not public")
var errSourceFetch = errors.New("source fetch failed")
var errSourceTooLarge = errors.New("source page is too large")
var errSourceContentType = errors.New("source page is not HTML")
var errSourceChanged = errors.New("source page changed during link pagination")

// SourceLink is a URL and bounded anchor text from the exact fetched page.
// It is only a candidate; no opening or employer identity is inferred.
type SourceLink struct {
	URL  string `json:"url"`
	Text string `json:"text"`
}
type SourceLinksSnapshot struct {
	AttemptID       string       `json:"attemptId"`
	CompanyID       string       `json:"companyId"`
	CompanyRevision int64        `json:"companyRevision"`
	SourceURL       string       `json:"sourceUrl"`
	ObservedAt      string       `json:"observedAt"`
	ContentSHA256   string       `json:"contentSha256,omitempty"`
	Status          string       `json:"status"`
	RedirectURL     string       `json:"redirectUrl,omitempty"`
	Links           []SourceLink `json:"links"`
	Omitted         int          `json:"omitted"`
	Rejected        int          `json:"rejected"`
	Truncated       bool         `json:"truncated"`
	Offset          int          `json:"offset"`
	NextOffset      int          `json:"nextOffset,omitempty"`
	TotalLinks      int          `json:"totalLinks"`
}
type SourceLinkPage struct {
	Offset                int
	ExpectedContentSHA256 string
}

var sourceHostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var whitespacePattern = regexp.MustCompile(`\s+`)

// officialSourceURL accepts a stored employer site, never a URL supplied by a
// model/tool call. A single HTTPS page is fetched; redirects are not followed.
func officialSourceURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, errSourceURL
	}
	host := strings.ToLower(u.Hostname())
	if len(host) > 253 || !sourceHostPattern.MatchString(host) || !strings.Contains(host, ".") || strings.Contains(host, "..") || net.ParseIP(host) != nil {
		return nil, errSourceURL
	}
	for _, suffix := range []string{".local", ".localhost", ".internal", ".test", ".invalid", ".example"} {
		if strings.HasSuffix(host, suffix) {
			return nil, errSourceURL
		}
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return nil, errSourceURL
		}
	}
	u.Host = host
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawPath = ""
	return u, nil
}

func publicSourceIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	blocked := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"),
	}
	for _, prefix := range blocked {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

type sourceResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

func resolvePublicSource(ctx context.Context, resolver sourceResolver, host string) (net.IP, error) {
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 || len(addresses) > 16 {
		return nil, errSourceAddress
	}
	for _, address := range addresses {
		if !publicSourceIP(address.IP) {
			return nil, errSourceAddress
		}
	}
	return addresses[0].IP, nil
}

func pinnedSourceClient(host string, ip net.IP) *http.Client {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSHandshakeTimeout: 4 * time.Second, ResponseHeaderTimeout: 8 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		requestedHost, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(requestedHost, host) || port != "443" {
			return nil, errSourceAddress
		}
		return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), "443"))
	}
	return &http.Client{Transport: transport, Timeout: sourceFetchTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// fetchOfficialLinks uses one pinned public address and one bounded HTTPS GET.
// A test can inject its resolver/client without granting production redirects.
func fetchOfficialLinks(ctx context.Context, raw string, resolver sourceResolver, clientFor func(string, net.IP) *http.Client) (SourceLinksSnapshot, error) {
	return fetchOfficialLinksPage(ctx, raw, resolver, clientFor, SourceLinkPage{})
}
func fetchOfficialLinksPage(ctx context.Context, raw string, resolver sourceResolver, clientFor func(string, net.IP) *http.Client, page SourceLinkPage) (SourceLinksSnapshot, error) {
	if page.Offset < 0 || page.Offset > maxSourceLinkOffset || page.Offset%maxSourceLinks != 0 || page.Offset > 0 && len(page.ExpectedContentSHA256) != 64 || page.Offset == 0 && page.ExpectedContentSHA256 != "" {
		return SourceLinksSnapshot{}, errSourceURL
	}
	u, err := officialSourceURL(raw)
	if err != nil {
		return SourceLinksSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, sourceFetchTimeout)
	defer cancel()
	ip, err := resolvePublicSource(ctx, resolver, u.Hostname())
	if err != nil {
		return SourceLinksSnapshot{}, err
	}
	client := clientFor(u.Hostname(), ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SourceLinksSnapshot{}, errSourceURL
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "jobseek-source-links/1.0")
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return SourceLinksSnapshot{}, ctx.Err()
		}
		return SourceLinksSnapshot{}, errSourceFetch
	}
	defer response.Body.Close()
	result := SourceLinksSnapshot{SourceURL: u.String(), ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Links: []SourceLink{}}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		result.Status = "redirect"
		if location, e := url.Parse(response.Header.Get("Location")); e == nil && location != nil {
			target := u.ResolveReference(location)
			if target.Scheme == "https" && target.User == nil && target.Hostname() != "" {
				result.RedirectURL = target.String()
			}
		}
		return result, nil
	}
	if response.StatusCode != http.StatusOK {
		result.Status = "http_error"
		return result, nil
	}
	if ct := strings.ToLower(response.Header.Get("Content-Type")); ct != "" && !strings.HasPrefix(ct, "text/html") {
		return SourceLinksSnapshot{}, errSourceContentType
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSourcePageBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return SourceLinksSnapshot{}, ctx.Err()
		}
		return SourceLinksSnapshot{}, errSourceFetch
	}
	if len(body) > maxSourcePageBytes {
		return SourceLinksSnapshot{}, errSourceTooLarge
	}
	sum := sha256.Sum256(body)
	result.ContentSHA256 = hex.EncodeToString(sum[:])
	if page.Offset > 0 && result.ContentSHA256 != page.ExpectedContentSHA256 {
		return SourceLinksSnapshot{}, errSourceChanged
	}
	result.Status = "ok"
	result.Offset = page.Offset
	result.Links, result.Omitted, result.Rejected, result.TotalLinks = extractSourceLinks(u, string(body), page.Offset)
	if result.Omitted > 0 && page.Offset+maxSourceLinks <= maxSourceLinkOffset {
		result.NextOffset = page.Offset + maxSourceLinks
	}
	result.Truncated = page.Offset > 0 || result.Omitted > 0
	return result, nil
}

func extractSourceLinks(base *url.URL, body string, offset int) ([]SourceLink, int, int, int) {
	links := make([]SourceLink, 0, maxSourceLinks)
	omitted := 0
	rejected := 0
	total := 0
	type anchor struct {
		href string
		text strings.Builder
	}
	var current *anchor
	var ignored []string
	finish := func() {
		if current == nil {
			return
		}
		defer func() { current = nil }()
		if current.href == "" {
			rejected++
			return
		}
		parsed, err := url.Parse(strings.TrimSpace(current.href))
		if err != nil {
			rejected++
			return
		}
		target := base.ResolveReference(parsed)
		target.Fragment = ""
		target.RawFragment = ""
		if target.Scheme != "https" || target.User != nil || target.Hostname() == "" || (target.Port() != "" && target.Port() != "443") || len(target.String()) > 1000 {
			rejected++
			return
		}
		label := strings.TrimSpace(whitespacePattern.ReplaceAllString(current.text.String(), " "))
		if len(label) > 200 {
			label = label[:200]
		}
		total++
		if total <= offset {
			return
		}
		if len(links) >= maxSourceLinks {
			omitted++
			return
		}
		links = append(links, SourceLink{URL: target.String(), Text: label})
	}
	z := html.NewTokenizer(strings.NewReader(body))
	for {
		typeID := z.Next()
		if typeID == html.ErrorToken {
			break
		}
		tok := z.Token()
		switch typeID {
		case html.StartTagToken:
			if len(ignored) > 0 {
				if tok.Data == "script" || tok.Data == "style" || tok.Data == "template" {
					ignored = append(ignored, tok.Data)
				}
				continue
			}
			if tok.Data == "script" || tok.Data == "style" || tok.Data == "template" {
				ignored = append(ignored, tok.Data)
				continue
			}
			if tok.Data == "a" {
				finish()
				current = &anchor{}
				for _, attr := range tok.Attr {
					if attr.Key == "href" {
						current.href = attr.Val
						break
					}
				}
			}
		case html.EndTagToken:
			if len(ignored) > 0 {
				if tok.Data == ignored[len(ignored)-1] {
					ignored = ignored[:len(ignored)-1]
				}
				continue
			}
			if tok.Data == "a" {
				finish()
			}
		case html.TextToken:
			if current != nil && len(ignored) == 0 && current.text.Len() < 1024 {
				current.text.WriteString(tok.Data)
			}
		}
	}
	finish()
	return links, omitted, rejected, total
}
