package tailnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

var ErrTargetDenied = errors.New("target is denied by deployment policy")

type TargetPolicy struct {
	prefixes []netip.Prefix
	resolver *net.Resolver
}

func NewTargetPolicy(prefixes []netip.Prefix) *TargetPolicy {
	return &TargetPolicy{prefixes: prefixes, resolver: net.DefaultResolver}
}

func (p *TargetPolicy) AllowAddr(addr netip.Addr) bool {
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr.Unmap()) || prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (p *TargetPolicy) Resolve(ctx context.Context, host string, port uint16) (string, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if !p.AllowAddr(addr) {
			return "", ErrTargetDenied
		}
		return net.JoinHostPort(addr.String(), strconv.Itoa(int(port))), nil
	}
	addrs, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("resolve target %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("resolve target %q: no addresses", host)
	}
	for _, addr := range addrs {
		if !p.AllowAddr(addr) {
			return "", ErrTargetDenied
		}
	}
	// Pin the checked address so a second DNS lookup cannot rebind the target.
	return net.JoinHostPort(addrs[0].String(), strconv.Itoa(int(port))), nil
}
