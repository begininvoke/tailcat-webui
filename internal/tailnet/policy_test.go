package tailnet

import (
	"errors"
	"net"
	"net/netip"
	"testing"

	"tailscale.com/tailcfg"
)

func TestTargetPolicy(t *testing.T) {
	policy := NewTargetPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")})
	if !policy.AllowAddr(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("loopback target denied")
	}
	if policy.AllowAddr(netip.MustParseAddr("169.254.169.254")) {
		t.Fatal("metadata target allowed")
	}
	if policy.AllowAddr(netip.MustParseAddr("10.0.0.1")) {
		t.Fatal("private target outside allowlist allowed")
	}
}

func TestDERPRegionRejectsExplicitPrivateAddress(t *testing.T) {
	manager := &Manager{allowedDERPHosts: map[string]struct{}{"tailcat.dev": {}}}
	region := &tailcfg.DERPRegion{RegionID: 1, Nodes: []*tailcfg.DERPNode{{Name: "official-name", RegionID: 1, HostName: "tc302a.ipn.dev", IPv6: "::ffff:127.0.0.1"}}}
	if err := manager.validateDERPRegion(region); !errors.Is(err, ErrTargetDenied) {
		t.Fatalf("private explicit DERP address error = %v", err)
	}
}

func TestResolvePinsCheckedAddress(t *testing.T) {
	policy := NewTargetPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")})
	target, err := policy.Resolve(t.Context(), "localhost", 8080)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := netip.ParseAddr(host); err != nil {
		t.Fatalf("target was not pinned to an IP: %q", target)
	}
}
