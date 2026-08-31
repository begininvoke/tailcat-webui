package tailnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"

	"tailscale.com/tailcfg"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" {
		return nil, errors.New("unexpected lookup network")
	}
	addrs, ok := r[host]
	if !ok {
		return nil, errors.New("unexpected lookup host")
	}
	return addrs, nil
}

func TestParseTargetRules(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []TargetRule
	}{
		{name: "legacy CIDR allows every port", raw: "127.0.0.0/8", want: []TargetRule{{Prefix: netip.MustParsePrefix("127.0.0.0/8")}}},
		{
			name: "CIDR ports are merged and prefix is masked",
			raw:  "192.0.2.9/24@443,192.0.2.0/24@80-81,192.0.2.0/24@82",
			want: []TargetRule{{
				Prefix: netip.MustParsePrefix("192.0.2.0/24"),
				Ports:  []PortRange{{Start: 80, End: 82}, {Start: 443, End: 443}},
			}},
		},
		{
			name: "IPv6 CIDR uses unambiguous port separator",
			raw:  "2001:db8::/32@443",
			want: []TargetRule{{
				Prefix: netip.MustParsePrefix("2001:db8::/32"),
				Ports:  []PortRange{{Start: 443, End: 443}},
			}},
		},
		{
			name: "exact domain is normalized with IDNA",
			raw:  "BÜCHER.example@8443-8444",
			want: []TargetRule{{
				Host:  "xn--bcher-kva.example",
				Ports: []PortRange{{Start: 8443, End: 8444}},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTargetRules(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseTargetRules(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseTargetRulesRejectsMalformedRules(t *testing.T) {
	for _, raw := range []string{
		"*.example.com@443",
		"example..com@443",
		"user:pass@example.com@443",
		"example.com",
		"example.com@0",
		"example.com@65536",
		"example.com@9000-8000",
		"example.com@80@443",
		"192.0.2.1@443",
		"@443",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseTargetRules(raw); err == nil {
				t.Fatalf("ParseTargetRules(%q) succeeded", raw)
			}
		})
	}
}

func TestTargetPolicyAllowAddrPort(t *testing.T) {
	rules, err := ParseTargetRules("127.0.0.0/8@80-81,::1/128@443")
	if err != nil {
		t.Fatal(err)
	}
	policy := NewTargetPolicy(rules)
	if !policy.AllowAddrPort(netip.MustParseAddrPort("127.0.0.1:80")) {
		t.Fatal("allowed address and port denied")
	}
	if policy.AllowAddrPort(netip.MustParseAddrPort("127.0.0.1:82")) {
		t.Fatal("address with disallowed port allowed")
	}
	if policy.AllowAddrPort(netip.MustParseAddrPort("169.254.169.254:80")) {
		t.Fatal("metadata target allowed")
	}
	if policy.AllowAddrPort(netip.MustParseAddrPort("[::1]:80")) {
		t.Fatal("IPv6 target with disallowed port allowed")
	}
	if policy.AllowAddr(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("port-unaware check allowed a port-constrained rule")
	}
	legacy, err := ParseTargetRules("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	if !NewTargetPolicy(legacy).AllowAddr(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("port-unaware check denied a legacy all-port rule")
	}
}

func TestDERPRegionRejectsExplicitPrivateAddress(t *testing.T) {
	manager := &Manager{allowedDERPHosts: map[string]struct{}{"tailcat.dev": {}}}
	region := &tailcfg.DERPRegion{RegionID: 1, Nodes: []*tailcfg.DERPNode{{Name: "official-name", RegionID: 1, HostName: "tc302a.ipn.dev", IPv6: "::ffff:127.0.0.1"}}}
	if err := manager.validateDERPRegion(region); !errors.Is(err, ErrTargetDenied) {
		t.Fatalf("private explicit DERP address error = %v", err)
	}
}

func TestTargetPolicyResolve(t *testing.T) {
	tests := []struct {
		name    string
		rules   string
		host    string
		lookup  string
		port    uint16
		answers []netip.Addr
		want    string
		wantErr error
	}{
		{name: "numeric address is checked with its port", rules: "192.0.2.0/24@443", host: "192.0.2.9", port: 443, want: "192.0.2.9:443"},
		{name: "numeric address on denied port", rules: "192.0.2.0/24@443", host: "192.0.2.9", port: 80, wantErr: ErrTargetDenied},
		{
			name:    "mixed DNS answers fail closed",
			rules:   "192.0.2.0/24@443",
			host:    "mixed.example",
			port:    443,
			answers: []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("198.51.100.10")},
			wantErr: ErrTargetDenied,
		},
		{
			name:    "mixed allowed DNS answers pin one checked address",
			rules:   "192.0.2.0/24@443,2001:db8::/32@443",
			host:    "dual-stack.example",
			port:    443,
			answers: []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8::10")},
			want:    "192.0.2.10:443",
		},
		{
			name:    "DNS answers must satisfy requested port",
			rules:   "192.0.2.0/24@443",
			host:    "port.example",
			port:    80,
			answers: []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			wantErr: ErrTargetDenied,
		},
		{
			name:    "exact IDNA domain pins checked numeric answer",
			rules:   "bücher.example@8443",
			host:    "BÜCHER.example",
			lookup:  "xn--bcher-kva.example",
			port:    8443,
			answers: []netip.Addr{netip.MustParseAddr("2001:db8::10")},
			want:    "[2001:db8::10]:8443",
		},
		{
			name:    "exact domain does not authorize another name",
			rules:   "allowed.example@443",
			host:    "other.example",
			port:    443,
			answers: []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			wantErr: ErrTargetDenied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, err := ParseTargetRules(tt.rules)
			if err != nil {
				t.Fatal(err)
			}
			policy := NewTargetPolicy(rules)
			if tt.answers != nil {
				lookup := tt.lookup
				if lookup == "" {
					lookup = tt.host
				}
				policy.resolver = staticResolver{lookup: tt.answers}
			}
			got, err := policy.Resolve(t.Context(), tt.host, tt.port)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Resolve() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("Resolve() = %q, want %q", got, tt.want)
			}
			if got != "" {
				host, _, err := net.SplitHostPort(got)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := netip.ParseAddr(host); err != nil {
					t.Fatalf("target was not pinned to an IP: %q", got)
				}
			}
		})
	}
}
