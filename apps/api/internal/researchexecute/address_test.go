package researchexecute

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestPublicIPPolicy(t *testing.T) {
	public := []string{"93.184.215.14", "8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, s := range public {
		if !publicIP(net.ParseIP(s)) {
			t.Fatalf("%s should be public", s)
		}
	}
	nonPublic := []string{
		"127.0.0.1", "::1",
		"10.0.0.1", "192.168.0.1", "172.16.0.1", "fd00::1", "fe80::1",
		"0.0.0.0", "100.64.0.1", "169.254.169.254", "192.0.2.1",
		"198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1",
		"2001:db8::1", "ff02::1",
	}
	for _, s := range nonPublic {
		if publicIP(net.ParseIP(s)) {
			t.Fatalf("%s must not be public", s)
		}
	}
}

type stubResolver struct {
	addrs map[string][]net.IP
	err   map[string]error
}

func (s stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if err, ok := s.err[host]; ok {
		return nil, err
	}
	ips, ok := s.addrs[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	out := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.IPAddr{IP: ip})
	}
	return out, nil
}

func TestResolveCheckedRefusesMixedAndPrivate(t *testing.T) {
	ctx := context.Background()
	r := stubResolver{addrs: map[string][]net.IP{
		"public.example":  {net.ParseIP("93.184.215.14")},
		"private.example": {net.ParseIP("10.1.2.3")},
		"mixed.example":   {net.ParseIP("93.184.215.14"), net.ParseIP("10.1.2.3")},
		"loop.example":    {net.ParseIP("127.0.0.1")},
	}}
	if _, err := resolveChecked(ctx, r, "public.example", false); err != nil {
		t.Fatalf("public: %v", err)
	}
	for _, host := range []string{"private.example", "mixed.example", "loop.example"} {
		if _, err := resolveChecked(ctx, r, host, false); !errors.Is(err, errAddressNotPublic) {
			t.Fatalf("%s: want address refusal, got %v", host, err)
		}
	}
	// Test-only loopback permission admits loopback alone, never mixed/private.
	if _, err := resolveChecked(ctx, r, "loop.example", true); err != nil {
		t.Fatalf("loopback permitted: %v", err)
	}
	for _, host := range []string{"private.example", "mixed.example"} {
		if _, err := resolveChecked(ctx, r, host, true); !errors.Is(err, errAddressNotPublic) {
			t.Fatalf("%s with loopback: want refusal, got %v", host, err)
		}
	}
	if _, err := resolveChecked(ctx, r, "unknown.example", true); !errors.Is(err, errAddressNotPublic) {
		t.Fatalf("unresolvable: want refusal, got %v", err)
	}
}

func TestParseResearchURLShape(t *testing.T) {
	for _, raw := range []string{
		"", "notaurl", "ftp://example.com/", "file:///etc/passwd",
		"http://", "https://user:pass@example.com/", "http://example.com/a b",
		"javascript:alert(1)",
	} {
		if _, err := parseResearchURL(raw, true); !errors.Is(err, errDestinationForbidden) {
			t.Fatalf("%q: want forbidden, got %v", raw, err)
		}
	}
	u, err := parseResearchURL("https://Example.COM:8443/a?b=c", false)
	if err != nil {
		t.Fatalf("valid: %v", err)
	}
	if u.Hostname() != "Example.COM" { // no normalization at dispatch; fingerprint folds
		t.Fatalf("host = %q", u.Hostname())
	}
}
