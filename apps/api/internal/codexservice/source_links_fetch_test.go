package codexservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type staticSourceResolver struct{ addresses []net.IPAddr }

func (r staticSourceResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}

type sourceRoundTrip func(*http.Request) (*http.Response, error)

func (f sourceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeSourceClient(contentType, body string, status int, location string) *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: sourceRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.String() != "https://www.shopify.com/" || req.Header.Get("Accept") != "text/html" {
			return nil, errors.New("unexpected request")
		}
		header := make(http.Header)
		if contentType != "" {
			header.Set("Content-Type", contentType)
		}
		if location != "" {
			header.Set("Location", location)
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
}
func TestSourceLinksRequirePublicPinnedHTTPS(t *testing.T) {
	for _, raw := range []string{"http://shopify.com/", "https://localhost/", "https://127.0.0.1/", "https://user@shopify.com/", "https://shopify.com:444/", "https://shopify.com/?token=x", "https://shopify.com/#x", "https://shopify.com.evil.local/"} {
		if _, err := officialSourceURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.1.2", "100.64.0.1", "192.0.2.1", "2001:db8::1", "64:ff9b::c0a8:1", "2002:c0a8:0101::", "::1"} {
		if publicSourceIP(net.ParseIP(ip)) {
			t.Errorf("accepted %s", ip)
		}
	}
	if !publicSourceIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("rejected public address")
	}
	client := pinnedSourceClient("www.shopify.com", net.ParseIP("1.1.1.1"))
	transport := client.Transport.(*http.Transport)
	if _, err := transport.DialContext(context.Background(), "tcp", "other.example:443"); !errors.Is(err, errSourceAddress) {
		t.Fatalf("pinned transport accepted another host: %v", err)
	}
	_, err := resolvePublicSource(context.Background(), staticSourceResolver{[]net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP("10.0.0.1")}}}, "www.shopify.com")
	if !errors.Is(err, errSourceAddress) {
		t.Fatalf("mixed public/private DNS accepted: %v", err)
	}
}
func TestSourceLinksReturnUnrankedEvidenceAndExplicitTruncation(t *testing.T) {
	body := `<a href="/about">About &amp; us</a><a href='https://www.shopify.com/careers'>Careers</a>`
	for i := 0; i < maxSourceLinks+3; i++ {
		body += fmt.Sprintf(`<a href="/item-%d">Item %d</a>`, i, i)
	}
	public := staticSourceResolver{[]net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}}
	observedHost := ""
	result, err := fetchOfficialLinks(context.Background(), "https://www.shopify.com/", public, func(host string, ip net.IP) *http.Client {
		observedHost = host
		if ip.String() != "1.1.1.1" {
			t.Fatal("address was not pinned")
		}
		return fakeSourceClient("text/html; charset=utf-8", body, 200, "")
	})
	if err != nil || observedHost != "www.shopify.com" || result.Status != "ok" || result.ContentSHA256 == "" || len(result.Links) != maxSourceLinks || result.Omitted != 5 || !result.Truncated || result.NextOffset != maxSourceLinks || result.TotalLinks != maxSourceLinks+5 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if result.Links[0].URL != "https://www.shopify.com/about" || result.Links[0].Text != "About & us" || result.Links[1].URL != "https://www.shopify.com/careers" {
		t.Fatalf("anchors %+v", result.Links[:2])
	}
	next, err := fetchOfficialLinksPage(context.Background(), "https://www.shopify.com/", public, func(string, net.IP) *http.Client { return fakeSourceClient("text/html", body, 200, "") }, SourceLinkPage{Offset: result.NextOffset, ExpectedContentSHA256: result.ContentSHA256})
	if err != nil || len(next.Links) != 5 || next.Omitted != 0 || next.NextOffset != 0 || next.ContentSHA256 != result.ContentSHA256 {
		t.Fatalf("continuation %+v %v", next, err)
	}
	if _, err := fetchOfficialLinksPage(context.Background(), "https://www.shopify.com/", public, func(string, net.IP) *http.Client { return fakeSourceClient("text/html", body, 200, "") }, SourceLinkPage{Offset: 64, ExpectedContentSHA256: strings.Repeat("0", 64)}); !errors.Is(err, errSourceChanged) {
		t.Fatalf("changed page accepted: %v", err)
	}
}
func TestSourceLinksUseRealAnchorHrefAndIgnoreNonRenderedMarkup(t *testing.T) {
	base, err := officialSourceURL("https://www.shopify.com/")
	if err != nil {
		t.Fatal(err)
	}
	body := `<!-- <a href="/comment">False</a> -->` +
		`<script>const fake = '<a href="/script">False</a>';</script>` +
		`<template><a href="/template">False</a></template>` +
		`<a data-href="/decoy" href="/careers"><span>Real &amp; current</span></a>` +
		`<a data-href="/missing">No href</a>`
	links, omitted, rejected, total := extractSourceLinks(base, body, 0)
	if omitted != 0 || rejected != 1 || total != 1 || len(links) != 1 || links[0].URL != "https://www.shopify.com/careers" || links[0].Text != "Real & current" {
		t.Fatalf("parsed non-anchor or wrong href: links=%+v omitted=%d rejected=%d total=%d", links, omitted, rejected, total)
	}
}
func TestSourceLinksRedirectAndOversizeAreBounded(t *testing.T) {
	public := staticSourceResolver{[]net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}}
	redirect, err := fetchOfficialLinks(context.Background(), "https://www.shopify.com/", public, func(string, net.IP) *http.Client { return fakeSourceClient("", "", 302, "/careers") })
	if err != nil || redirect.Status != "redirect" || redirect.RedirectURL != "https://www.shopify.com/careers" || len(redirect.Links) != 0 {
		t.Fatalf("redirect %+v %v", redirect, err)
	}
	_, err = fetchOfficialLinks(context.Background(), "https://www.shopify.com/", public, func(string, net.IP) *http.Client {
		return fakeSourceClient("text/html", strings.Repeat("x", maxSourcePageBytes+1), 200, "")
	})
	if !errors.Is(err, errSourceTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
}
