package agentprotocol

import (
	"fmt"
	"net/netip"
)

// IP access rules (allow + deny lists) shared by domains (Access), TCP routes
// (RouteExt.AllowCIDRs/DenyCIDRs) and the agent's dynamic-config renderer.
//
// Traefik has no deny middleware, only ipAllowList. Rules are therefore
// evaluated deny-first by subtraction: the effective allow list is
// (allow, or every address when allow is empty) minus deny, rendered as one
// ipAllowList. A client is let in exactly when it matches an allow entry and
// no deny entry, which is the same decision an ordered "deny, then allow, then
// default" evaluation would give.

// Limits of an access rule set.
const (
	// MaxAccessCIDRs bounds each of the allow and deny lists.
	MaxAccessCIDRs = 200
	// MaxEffectiveCIDRs bounds the rendered ipAllowList. Subtracting many small
	// ranges from a large one splits it into up to 32 (IPv4) or 128 (IPv6)
	// prefixes per deny entry.
	MaxEffectiveCIDRs = 4096
)

// parsePrefix accepts a CIDR or a single address and returns the masked prefix.
func parsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a CIDR or IP address", s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// IsOpenCIDR reports whether s is the match-everything range of a family
// (0.0.0.0/0 or ::/0).
func IsOpenCIDR(s string) bool {
	p, err := parsePrefix(s)
	return err == nil && p.Bits() == 0
}

// AnyOpenCIDR reports whether any entry matches every address of a family.
func AnyOpenCIDR(cidrs []string) bool {
	for _, c := range cidrs {
		if IsOpenCIDR(c) {
			return true
		}
	}
	return false
}

// splitPrefix returns the two halves of p (p.Bits() < p.Addr().BitLen()).
func splitPrefix(p netip.Prefix) (lo, hi netip.Prefix) {
	bits := p.Bits()
	lo = netip.PrefixFrom(p.Addr(), bits+1)
	var hiAddr netip.Addr
	if p.Addr().Is4() {
		b := p.Addr().As4()
		b[bits/8] |= 0x80 >> (bits % 8)
		hiAddr = netip.AddrFrom4(b)
	} else {
		b := p.Addr().As16()
		b[bits/8] |= 0x80 >> (bits % 8)
		hiAddr = netip.AddrFrom16(b)
	}
	return lo, netip.PrefixFrom(hiAddr, bits+1)
}

// subtractPrefix returns the prefixes covering p minus d, in address order of
// the removed side first-split (deterministic).
func subtractPrefix(p, d netip.Prefix) []netip.Prefix {
	if p.Addr().Is4() != d.Addr().Is4() || !p.Overlaps(d) {
		return []netip.Prefix{p}
	}
	if d.Bits() <= p.Bits() {
		return nil // d covers p entirely
	}
	var out []netip.Prefix
	for p.Bits() < d.Bits() {
		lo, hi := splitPrefix(p)
		if lo.Overlaps(d) {
			out, p = append(out, hi), lo
		} else {
			out, p = append(out, lo), hi
		}
	}
	return out // p == d is dropped
}

// EffectiveAllow returns the ipAllowList source ranges equivalent to "allow
// minus deny". An empty allow list means every address. With no deny entries
// the allow list is returned unchanged (as written), so rule sets without deny
// entries render exactly as before. An error means the rules admit nobody or
// expand beyond MaxEffectiveCIDRs.
func EffectiveAllow(allow, deny []string) ([]string, error) {
	if len(deny) == 0 {
		return append([]string(nil), allow...), nil
	}
	base := make([]netip.Prefix, 0, max(len(allow), 2))
	if len(allow) == 0 {
		base = append(base, netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0"))
	}
	for _, s := range allow {
		p, err := parsePrefix(s)
		if err != nil {
			return nil, err
		}
		base = append(base, p)
	}
	for _, s := range deny {
		d, err := parsePrefix(s)
		if err != nil {
			return nil, err
		}
		var next []netip.Prefix
		for _, p := range base {
			next = append(next, subtractPrefix(p, d)...)
			if len(next) > MaxEffectiveCIDRs {
				return nil, fmt.Errorf("the deny list splits the allowed ranges into more than %d networks; use fewer or broader deny entries", MaxEffectiveCIDRs)
			}
		}
		base = next
	}
	if len(base) == 0 {
		return nil, fmt.Errorf("the rules deny every address")
	}
	out := make([]string, len(base))
	for i, p := range base {
		out[i] = p.String()
	}
	return out, nil
}

// ValidateAccessLists checks an allow/deny pair: each list holds at most
// MaxAccessCIDRs valid CIDRs or addresses, and the pair admits at least one
// address. needOne requires at least one entry overall.
func ValidateAccessLists(allow, deny []string, needOne bool) error {
	if len(allow) > MaxAccessCIDRs || len(deny) > MaxAccessCIDRs {
		return fmt.Errorf("at most %d allowed and %d denied CIDRs", MaxAccessCIDRs, MaxAccessCIDRs)
	}
	if needOne && len(allow) == 0 && len(deny) == 0 {
		return fmt.Errorf("needs at least one allowed or denied CIDR")
	}
	for _, c := range allow {
		if err := validCIDROrIP(c); err != nil {
			return err
		}
	}
	for _, c := range deny {
		if err := validCIDROrIP(c); err != nil {
			return err
		}
	}
	if _, err := EffectiveAllow(allow, deny); err != nil {
		return err
	}
	return nil
}
