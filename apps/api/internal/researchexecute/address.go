package researchexecute

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// Public-destination policy, adapted from the B-owned official-source fetch
// (codexservice/source_links_fetch.go) to arbitrary public URLs: any http(s)
// host or port is accepted as long as every resolved address is globally
// routable. Redirects re-resolve and re-check at each hop (plan §8); scripts
// and browser traffic cannot reach app/admin/private services through an
// alternate path because subprocesses only ever reach the recording proxy,
// which forwards through this same policy.
var (
	errDestinationForbidden = errors.New("destination is not a public http(s) URL")
	errAddressNotPublic     = errors.New("destination address is not public")
)

// blockedPrefixes are never valid research targets, even though some parse
// as global unicast.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

// publicIP reports whether ip is a globally routable target.
func publicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// loopbackIP reports whether ip is loopback (v4 or v6). Test fixtures serve
// on 127.0.0.1; production dispatch refuses them.
func loopbackIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return addr.Unmap().IsLoopback()
}

// addressAllowed reports whether ip may be dialed under the given policy.
func addressAllowed(ip net.IP, permitLoopback bool) bool {
	if publicIP(ip) {
		return true
	}
	return permitLoopback && loopbackIP(ip)
}

// parseResearchURL validates the static shape of a research URL: http(s),
// no userinfo, no control characters, host present. Literal-IP hosts are
// checked immediately; named hosts are resolved and checked at dial time and
// again at every redirect hop.
func parseResearchURL(raw string, permitLoopback bool) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: empty URL", errDestinationForbidden)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: unparseable URL", errDestinationForbidden)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q", errDestinationForbidden, u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: userinfo never enters a research URL", errDestinationForbidden)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: missing host", errDestinationForbidden)
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return nil, fmt.Errorf("%w: whitespace in URL", errDestinationForbidden)
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if !addressAllowed(ip, permitLoopback) {
			return nil, fmt.Errorf("%w: literal host %q", errAddressNotPublic, u.Hostname())
		}
	}
	return u, nil
}

// resolver abstracts DNS for the dial path.
type resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// resolveChecked resolves host and requires every returned address to be an
// allowed target. A name that resolves to a mix of public and private
// addresses is refused outright (no silent subset dialing).
func resolveChecked(ctx context.Context, r resolver, host string, permitLoopback bool) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !addressAllowed(ip, permitLoopback) {
			return nil, fmt.Errorf("%w: %q", errAddressNotPublic, host)
		}
		return []net.IP{ip}, nil
	}
	addrs, err := r.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 || len(addrs) > 16 {
		return nil, fmt.Errorf("%w: cannot resolve %q", errAddressNotPublic, host)
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if !addressAllowed(a.IP, permitLoopback) {
			return nil, fmt.Errorf("%w: %q resolves to a non-public address", errAddressNotPublic, host)
		}
		out = append(out, a.IP)
	}
	return out, nil
}

// checkURLResolves validates the static shape and, for named hosts, the
// current DNS resolution of a URL. It is the fail-fast pre-dispatch check and
// the per-redirect-hop revalidation; the dialer pins to a checked resolution
// so the address actually connected is always one that was checked.
func checkURLResolves(ctx context.Context, r resolver, raw string, permitLoopback bool) (*url.URL, error) {
	u, err := parseResearchURL(raw, permitLoopback)
	if err != nil {
		return nil, err
	}
	if _, err := resolveChecked(ctx, r, u.Hostname(), permitLoopback); err != nil {
		return nil, err
	}
	return u, nil
}

// pinnedDialer dials only checked addresses: it re-resolves the dial host,
// refuses any non-allowed address, and connects to the resolved IP with the
// original host preserved for TLS ServerName via the http.Transport.
func pinnedDialer(r resolver, permitLoopback bool, timeoutDialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("%w: network %q", errAddressNotPublic, network)
		}
		ips, err := resolveChecked(ctx, r, host, permitLoopback)
		if err != nil {
			return nil, err
		}
		if port == "" {
			port = "443"
		}
		var last error
		for _, ip := range ips {
			conn, err := timeoutDialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last == nil {
			last = errAddressNotPublic
		}
		return nil, last
	}
}
