package tailnet

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

var ErrTargetDenied = errors.New("target is denied by deployment policy")

type TargetPolicy struct {
	rules    []TargetRule
	resolver targetResolver
}

type targetResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type PortRange struct {
	Start uint16
	End   uint16
}

type TargetRule struct {
	Prefix netip.Prefix
	Host   string
	Ports  []PortRange
}

func ParseTargetRules(raw string) ([]TargetRule, error) {
	var rules []TargetRule
	indices := make(map[string]int)
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		target, portClause, hasPorts := strings.Cut(item, "@")
		target = strings.TrimSpace(target)
		if target == "" {
			return nil, fmt.Errorf("parse allowed target %q: target is empty", item)
		}
		var ports []PortRange
		if hasPorts {
			portRange, err := parsePortRange(strings.TrimSpace(portClause))
			if err != nil {
				return nil, fmt.Errorf("parse allowed target %q: %w", item, err)
			}
			ports = []PortRange{portRange}
		}

		var rule TargetRule
		var key string
		if prefix, err := netip.ParsePrefix(target); err == nil {
			rule = TargetRule{Prefix: prefix.Masked(), Ports: ports}
			key = "prefix:" + rule.Prefix.String()
		} else {
			if !hasPorts {
				return nil, fmt.Errorf("parse allowed target %q: domains require a port or port range", item)
			}
			host, err := normalizeDomain(target)
			if err != nil {
				return nil, fmt.Errorf("parse allowed target %q: %w", item, err)
			}
			rule = TargetRule{Host: host, Ports: ports}
			key = "host:" + host
		}

		if index, ok := indices[key]; ok {
			existing := &rules[index]
			if existing.Ports == nil || rule.Ports == nil {
				existing.Ports = nil
			} else {
				existing.Ports = mergePortRanges(append(existing.Ports, rule.Ports...))
			}
			continue
		}
		indices[key] = len(rules)
		rules = append(rules, rule)
	}
	return rules, nil
}

func parsePortRange(raw string) (PortRange, error) {
	if raw == "" {
		return PortRange{}, errors.New("port is empty")
	}
	startRaw, endRaw, hasRange := strings.Cut(raw, "-")
	start, err := parsePort(startRaw)
	if err != nil {
		return PortRange{}, err
	}
	if !hasRange {
		return PortRange{Start: start, End: start}, nil
	}
	end, err := parsePort(endRaw)
	if err != nil {
		return PortRange{}, err
	}
	if end < start {
		return PortRange{}, errors.New("port range end is less than start")
	}
	return PortRange{Start: start, End: end}, nil
}

func parsePort(raw string) (uint16, error) {
	port, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("invalid port %q", raw)
	}
	return uint16(port), nil
}

func normalizeDomain(raw string) (string, error) {
	if strings.ContainsAny(raw, "*:/?#[]@") {
		return "", errors.New("domain contains wildcard, credentials, or URL syntax")
	}
	host, err := idna.Lookup.ToASCII(raw)
	if err != nil {
		return "", fmt.Errorf("normalize domain: %w", err)
	}
	host = strings.ToLower(host)
	if !validDomain(host) {
		return "", errors.New("domain is malformed")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "", errors.New("numeric targets require CIDR notation")
	}
	return host, nil
}

func validDomain(host string) bool {
	if host == "" || len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func mergePortRanges(ranges []PortRange) []PortRange {
	slices.SortFunc(ranges, func(a, b PortRange) int {
		if byStart := cmp.Compare(a.Start, b.Start); byStart != 0 {
			return byStart
		}
		return cmp.Compare(a.End, b.End)
	})
	merged := ranges[:0]
	for _, portRange := range ranges {
		if len(merged) == 0 || uint32(portRange.Start) > uint32(merged[len(merged)-1].End)+1 {
			merged = append(merged, portRange)
			continue
		}
		merged[len(merged)-1].End = max(merged[len(merged)-1].End, portRange.End)
	}
	return merged
}

func NewTargetPolicy(rules []TargetRule) *TargetPolicy {
	return &TargetPolicy{rules: rules, resolver: net.DefaultResolver}
}

func (p *TargetPolicy) AllowAddr(addr netip.Addr) bool {
	for _, rule := range p.rules {
		if rule.Ports == nil && rule.Prefix.IsValid() && (rule.Prefix.Contains(addr.Unmap()) || rule.Prefix.Contains(addr)) {
			return true
		}
	}
	return false
}

func (p *TargetPolicy) AllowAddrPort(addrPort netip.AddrPort) bool {
	for _, rule := range p.rules {
		if rule.Prefix.IsValid() &&
			(rule.Prefix.Contains(addrPort.Addr().Unmap()) || rule.Prefix.Contains(addrPort.Addr())) &&
			allowPort(rule.Ports, addrPort.Port()) {
			return true
		}
	}
	return false
}

func (p *TargetPolicy) Resolve(ctx context.Context, host string, port uint16) (string, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if !p.AllowAddrPort(netip.AddrPortFrom(addr, port)) {
			return "", ErrTargetDenied
		}
		return net.JoinHostPort(addr.Unmap().String(), strconv.Itoa(int(port))), nil
	}
	normalizedHost, err := normalizeDomain(host)
	if err != nil {
		return "", fmt.Errorf("%w: invalid target host", ErrTargetDenied)
	}
	addrs, err := p.resolver.LookupNetIP(ctx, "ip", normalizedHost)
	if err != nil {
		return "", fmt.Errorf("resolve target %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("resolve target %q: no addresses", host)
	}
	for _, addr := range addrs {
		if !p.allowHostAddrPort(normalizedHost, netip.AddrPortFrom(addr, port)) {
			return "", ErrTargetDenied
		}
	}
	// Pin the checked address so a second DNS lookup cannot rebind the target.
	return net.JoinHostPort(addrs[0].Unmap().String(), strconv.Itoa(int(port))), nil
}

func (p *TargetPolicy) allowHostAddrPort(host string, addrPort netip.AddrPort) bool {
	if p.AllowAddrPort(addrPort) {
		return true
	}
	for _, rule := range p.rules {
		if rule.Host == host && allowPort(rule.Ports, addrPort.Port()) {
			return true
		}
	}
	return false
}

func allowPort(ranges []PortRange, port uint16) bool {
	if ranges == nil {
		return true
	}
	for _, portRange := range ranges {
		if portRange.Start <= port && port <= portRange.End {
			return true
		}
	}
	return false
}
