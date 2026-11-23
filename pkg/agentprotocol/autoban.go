package agentprotocol

import (
	"fmt"
	"net/netip"
	"time"
)

// Auto-ban: an address that trips the standard protection or a block rule
// with a condition other than ip/country Threshold times within
// WindowSeconds is banned for DurationSeconds. The agent decides from its
// access-log tail, the edge guard enforces (allow list, then bans, then the
// firewall rules), and the control plane mirrors the bans over TypeBans.

// AutoBan policy limits.
const (
	MinAutoBanThreshold = 3
	MaxAutoBanThreshold = 1000
	MinAutoBanWindow    = 60
	MaxAutoBanWindow    = 86400
	MinAutoBanDuration  = 300
	MaxAutoBanDuration  = 604800
)

// Ban key widths: the guard and the agent track an IPv4 address as a /32 and
// an IPv6 address as its /64.
const (
	BanBitsV4 = 32
	BanBitsV6 = 64
)

// Ban end reasons (BanEvent.Reason of an "end" event) and event kinds.
const (
	BanEventBan = "ban"
	BanEventEnd = "end"

	BanEndExpired  = "expired"
	BanEndUnbanned = "unbanned"
	BanEndLifted   = "lifted"
)

// AutoBan is the per-service auto-ban policy (RouteExt.AutoBan). Like the
// firewall it is operational: it never bumps a deployment generation.
type AutoBan struct {
	Enabled         bool     `json:"enabled"`
	Threshold       int      `json:"threshold"`       // strikes that start a ban, 3..1000
	WindowSeconds   int      `json:"windowSeconds"`   // how far back strikes count, 60..86400
	DurationSeconds int      `json:"durationSeconds"` // how long a ban lasts, 300..604800
	Allow           []string `json:"allow,omitempty"` // never banned: CIDR or IP, at most MaxAccessCIDRs
}

// Validate checks the limits and the allow list. The limits apply to a
// disabled policy too, so a stored policy can be switched on as it is.
func (a AutoBan) Validate() error {
	if a.Threshold < MinAutoBanThreshold || a.Threshold > MaxAutoBanThreshold {
		return fmt.Errorf("threshold must be %d-%d", MinAutoBanThreshold, MaxAutoBanThreshold)
	}
	if a.WindowSeconds < MinAutoBanWindow || a.WindowSeconds > MaxAutoBanWindow {
		return fmt.Errorf("window must be %d-%d seconds", MinAutoBanWindow, MaxAutoBanWindow)
	}
	if a.DurationSeconds < MinAutoBanDuration || a.DurationSeconds > MaxAutoBanDuration {
		return fmt.Errorf("duration must be %d-%d seconds", MinAutoBanDuration, MaxAutoBanDuration)
	}
	if len(a.Allow) > MaxAccessCIDRs {
		return fmt.Errorf("at most %d allow entries", MaxAccessCIDRs)
	}
	for _, s := range a.Allow {
		if _, err := parsePrefix(s); err != nil {
			return fmt.Errorf("allow: %w", err)
		}
	}
	return nil
}

// Normalize returns the policy with the allow list in canonical form: every
// entry a masked prefix (a bare address becomes /32 or /128, IPv4-mapped IPv6
// becomes IPv4), duplicates removed, order kept. Entries that do not parse
// are kept as they are for Validate to report.
func (a AutoBan) Normalize() AutoBan {
	var out []string
	seen := map[string]bool{}
	for _, s := range a.Allow {
		if p, err := parsePrefix(s); err == nil {
			s = unmapPrefix(p).String()
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	a.Allow = out
	return a
}

// Allows reports whether addr is on the allow list. Entries that do not
// parse are ignored.
func (a AutoBan) Allows(addr netip.Addr) bool {
	addr = cleanAddr(addr)
	if !addr.IsValid() {
		return false
	}
	for _, s := range a.Allow {
		if p, err := parsePrefix(s); err == nil && unmapPrefix(p).Contains(addr) {
			return true
		}
	}
	return false
}

// unmapPrefix turns an IPv4-mapped IPv6 prefix (at least /96) into the IPv4
// prefix; other prefixes are returned unchanged.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
		return netip.PrefixFrom(a.Unmap(), p.Bits()-96)
	}
	return p
}

// cleanAddr drops the IPv6 zone and unmaps an IPv4-mapped address.
func cleanAddr(a netip.Addr) netip.Addr {
	return a.WithZone("").Unmap()
}

// BanPrefix is the key an address is banned under: IPv4 as /32, IPv6 as its
// /64, an IPv4-mapped IPv6 address as the IPv4 /32. An invalid address gives
// the zero Prefix.
func BanPrefix(a netip.Addr) netip.Prefix {
	a = cleanAddr(a)
	switch {
	case !a.IsValid():
		return netip.Prefix{}
	case a.Is4():
		return netip.PrefixFrom(a, BanBitsV4)
	default:
		return netip.PrefixFrom(a, BanBitsV6).Masked()
	}
}

// ExemptAddr reports whether an address is never banned and never counted:
// loopback, private (10/8, 172.16/12, 192.168/16, fc00::/7), CGNAT
// (100.64/10), link-local, unspecified, multicast, and an invalid address.
func ExemptAddr(a netip.Addr) bool {
	a = cleanAddr(a)
	return !a.IsValid() || a.IsLoopback() || IsPrivateAddr(a) ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast()
}

// Ban is an active or ended ban of one address prefix on one route.
type Ban struct {
	ID        string         `json:"id"`        // uuid minted by the agent
	ServiceID string         `json:"serviceId"` // owner (stack or service)
	RouteID   string         `json:"routeId"`   // guard route id
	Prefix    string         `json:"prefix"`    // "203.0.113.7/32" | "2001:db8:1:2::/64"
	StartedAt time.Time      `json:"startedAt"`
	ExpiresAt time.Time      `json:"expiresAt"`
	Strikes   int            `json:"strikes"`
	Rules     map[string]int `json:"rules,omitempty"` // strikes per firewall rule id
}

// BanEvent is one ban lifecycle change, delivered at least once.
type BanEvent struct {
	EventID string    `json:"eventId"` // uuid; the control plane dedupes on it
	Kind    string    `json:"kind"`    // BanEventBan | BanEventEnd
	At      time.Time `json:"at"`
	Reason  string    `json:"reason,omitempty"` // end: BanEndExpired | BanEndUnbanned | BanEndLifted
	Ban     Ban       `json:"ban"`
}

// BanFrame (agent → control plane, TypeBans) carries ban events. A Snapshot
// frame lists the full active set of the Services: the control plane ends
// every other active ban of those services.
type BanFrame struct {
	Seq      uint64     `json:"seq"`
	Events   []BanEvent `json:"events"`
	Snapshot bool       `json:"snapshot,omitempty"`
	Services []string   `json:"services,omitempty"`
}

// BanAck (control plane → agent, TypeBansAck) confirms a BanFrame was stored.
type BanAck struct {
	Seq uint64 `json:"seq"`
}

// AutoBanUnbanPayload is the payload of a KindAutoBanUnban command.
type AutoBanUnbanPayload struct {
	ServiceID string `json:"serviceId"`
	BanID     string `json:"banId"`
}

// GuardBans is the content of the guard's bans file (agent → guard). It is
// separate from the rules file so a ban does not rewrite the rules.
type GuardBans struct {
	// Services maps a route id to its allow list and bans.
	Services map[string]GuardRouteBans `json:"services"`
}

// GuardRouteBans is the auto-ban state of one guard route.
type GuardRouteBans struct {
	Allow []string   `json:"allow,omitempty"` // CIDR or IP, never banned
	Bans  []GuardBan `json:"bans,omitempty"`
}

// GuardBan is one active ban: Prefix is a BanPrefix key.
type GuardBan struct {
	Prefix    string    `json:"prefix"`
	ExpiresAt time.Time `json:"expiresAt"`
}
