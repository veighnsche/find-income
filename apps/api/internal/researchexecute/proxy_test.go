package researchexecute

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// proxyTestClient builds a client that routes through the given proxy URL.
func proxyTestClient(t *testing.T, proxyURL string) *http.Client {
	t.Helper()
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
		Timeout:   15 * time.Second,
	}
}

func TestRecordingProxyObservesAndForwards(t *testing.T) {
	fix := newFixtureServer(t)
	fix.set("/res", []byte("proxied bytes"))
	p, err := newRecordingProxy(newGuardedClient(5, true, nilResolver{}), 1<<20, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	resp, err := proxyTestClient(t, p.url()).Get(fix.url("/res"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "proxied bytes" || resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	obs := p.observed()
	if len(obs) != 1 {
		t.Fatalf("observed = %+v", obs)
	}
	if obs[0].Method != "GET" || obs[0].URL != fix.url("/res") || obs[0].Status != 200 {
		t.Fatalf("entry = %+v", obs[0])
	}
	if obs[0].BytesIn != int64(len("proxied bytes")) || obs[0].Blocked || obs[0].Tunneled {
		t.Fatalf("entry = %+v", obs[0])
	}
}

func TestRecordingProxyFencesPrivateTarget(t *testing.T) {
	p, err := newRecordingProxy(newGuardedClient(5, true, nilResolver{}), 1<<20, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	resp, err := proxyTestClient(t, p.url()).Get("http://169.254.169.254/latest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode == 200 {
		t.Fatal("private target must not succeed")
	}
	obs := p.observed()
	if len(obs) != 1 || !obs[0].Blocked || obs[0].Error != "destination_forbidden" {
		t.Fatalf("observed = %+v", obs)
	}
}

func TestRecordingProxyEnforcesRequestBound(t *testing.T) {
	fix := newFixtureServer(t)
	fix.set("/a", []byte("a"))
	p, err := newRecordingProxy(newGuardedClient(5, true, nilResolver{}), 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	c := proxyTestClient(t, p.url())
	if _, err := c.Get(fix.url("/a")); err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(fix.url("/a"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	obs := p.observed()
	if len(obs) != 2 || !obs[1].Blocked || obs[1].Error != "request_bound" {
		t.Fatalf("observed = %+v", obs)
	}
}

func TestRecordingProxyTruncatesPerResponseBound(t *testing.T) {
	fix := newFixtureServer(t)
	fix.set("/big", []byte(strings.Repeat("z", 1000)))
	p, err := newRecordingProxy(newGuardedClient(5, true, nilResolver{}), 64, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	resp, err := proxyTestClient(t, p.url()).Get(fix.url("/big"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 64 {
		t.Fatalf("len = %d", len(body))
	}
	obs := p.observed()
	if len(obs) != 1 || !obs[0].Truncated || obs[0].BytesIn != 64 {
		t.Fatalf("observed = %+v", obs)
	}
}

func TestRecordingProxyConnectFencesBeforeDial(t *testing.T) {
	p, err := newRecordingProxy(newGuardedClient(5, false, nilResolver{}), 1<<20, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	// CONNECT to a loopback target with loopback forbidden: refused by policy,
	// never dialed (a dial attempt would fail differently).
	if _, err := dialThroughConnect(p.url(), "127.0.0.1:1"); err == nil {
		t.Fatal("fenced CONNECT succeeded")
	} else {
		t.Logf("connect refused: %v", err)
	}
	obs := p.observed()
	if len(obs) != 1 || !obs[0].Tunneled || !obs[0].Blocked {
		t.Fatalf("observed = %+v", obs)
	}
	if obs[0].Error != "destination_forbidden" {
		t.Fatalf("observed = %+v", obs[0])
	}
}

func TestRecordingProxyConnectRelaysLoopbackFixture(t *testing.T) {
	// A CONNECT tunnel to an allowed fixture: opaque relay with byte counts.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "tunneled-ok")
	}))
	t.Cleanup(backend.Close)
	host := strings.TrimPrefix(backend.URL, "http://")
	p, err := newRecordingProxy(newGuardedClient(5, true, nilResolver{}), 1<<20, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	// Raw CONNECT to the fixture port, then speak HTTP/1.1 inside the tunnel.
	conn, err := dialThroughConnect(p.url(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	raw, _ := io.ReadAll(io.LimitReader(conn, 1<<16))
	if !strings.Contains(string(raw), "tunneled-ok") {
		t.Fatalf("tunnel body missing: %q", raw)
	}
	// The tunnel entry is recorded when the relay finishes: close our side,
	// then wait for the entry.
	_ = conn.Close()
	var obs []ObservedRequest
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if obs = p.observed(); len(obs) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(obs) != 1 || !obs[0].Tunneled || obs[0].Blocked || obs[0].BytesIn <= 0 {
		t.Fatalf("observed = %+v", obs)
	}
}

// nilResolver resolves nothing; literal-IP URLs never consult it.
type nilResolver struct{}

func (nilResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	return nil, fmt.Errorf("no DNS in proxy tests")
}

// dialThroughConnect opens a raw CONNECT tunnel via the proxy and returns the
// hijacked connection after the 200 response.
func dialThroughConnect(proxyURL, host string) (net.Conn, error) {
	proxyHost := strings.TrimPrefix(proxyURL, "http://")
	conn, err := net.DialTimeout("tcp", proxyHost, 10*time.Second)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(string(buf[:n]), "200") {
		conn.Close()
		return nil, fmt.Errorf("CONNECT refused: %q", buf[:n])
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn, nil
}
