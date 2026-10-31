// Package netpool allocates the IPv4 subnets of the networks the agent creates
// (environment, internal and team shared networks).
//
// Left to itself the Docker daemon carves new bridge networks out of its
// default address pools, which hold only a few dozen /16 and /20 subnets
// (172.17.0.0/12 and 192.168.0.0/16). A host with many environments ("all
// predefined address pools have been fully subnetted") runs out long before
// it runs out of containers. The agent therefore gives every network of its own
// an explicit subnet out of a dedicated pool, by default 10.213.0.0/16 split
// into /24s.
package netpool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
)

const (
	// DefaultPool is the address range the agent allocates network subnets from.
	DefaultPool = "10.213.0.0/16"
	// DefaultPrefixLen is the size of every allocated subnet (a /24 holds 253
	// containers, and the default pool 256 networks).
	DefaultPrefixLen = 24

	// maxPrefixLen is the longest prefix Docker can still use: it needs the
	// network and broadcast addresses and a gateway.
	maxPrefixLen = 29
	// maxCreateAttempts bounds the retries after "pool overlaps" refusals, so a
	// host whose routes cover the whole pool fails fast.
	maxCreateAttempts = 64
)

// ErrPoolExhausted is wrapped by the error Ensure returns when every subnet of
// the pool is taken.
var ErrPoolExhausted = errors.New("network address pool exhausted")

// Allocator hands out subnets of one pool. The zero value is not usable; call
// New. It is safe for concurrent use.
type Allocator struct {
	pool      netip.Prefix
	prefixLen int

	// mu serialises allocation: the list-pick-create sequence of two networks
	// must not pick the same subnet.
	mu sync.Mutex
	// hostPrefixes reports the subnets of the agent host's interfaces; tests
	// replace it.
	hostPrefixes func() []netip.Prefix
}

// New validates the pool (an IPv4 CIDR) and the subnet size and returns an
// Allocator. An empty pool or a zero prefixLen selects the default.
func New(pool string, prefixLen int) (*Allocator, error) {
	if pool == "" {
		pool = DefaultPool
	}
	if prefixLen == 0 {
		prefixLen = DefaultPrefixLen
	}
	p, err := netip.ParsePrefix(pool)
	if err != nil {
		return nil, fmt.Errorf("network pool %q (--network-pool): not a CIDR range: %w", pool, err)
	}
	p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked()
	if !p.Addr().Is4() {
		return nil, fmt.Errorf("network pool %q (--network-pool): only IPv4 pools are supported", pool)
	}
	if prefixLen > maxPrefixLen {
		return nil, fmt.Errorf("network prefix length /%d (--network-prefix-len) is too long: at most /%d", prefixLen, maxPrefixLen)
	}
	if prefixLen < p.Bits() {
		return nil, fmt.Errorf("network prefix length /%d (--network-prefix-len) is larger than the pool %s (--network-pool): it must be /%d or longer", prefixLen, p, p.Bits())
	}
	return &Allocator{pool: p, prefixLen: prefixLen, hostPrefixes: interfacePrefixes}, nil
}

// Pool is the address range subnets are allocated from.
func (a *Allocator) Pool() netip.Prefix { return a.pool }

// WithHostPrefixes replaces the source of host interface networks the
// allocator avoids (default: the agent host's interfaces). Tests pass a fixed
// list so allocation does not depend on the machine running them.
func (a *Allocator) WithHostPrefixes(f func() []netip.Prefix) *Allocator {
	a.hostPrefixes = f
	return a
}

// PrefixLen is the prefix length of every allocated subnet.
func (a *Allocator) PrefixLen() int { return a.prefixLen }

// Capacity is how many subnets the pool holds.
func (a *Allocator) Capacity() int { return 1 << (a.prefixLen - a.pool.Bits()) }

// Ensure creates the bridge network name with the first free subnet of the
// pool. A network that already exists is reused as it is, whatever its subnet.
// internal selects an internal network (no route out). A subnet is free when it
// overlaps neither the IPAM subnet of any Docker network nor an address range
// of a host interface. When the daemon still refuses the pick as overlapping
// (another process took it, or a route the interfaces do not show), the next
// free subnet is tried.
func (a *Allocator) Ensure(ctx context.Context, eng docker.Engine, name string, labels map[string]string, internal bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	nets, err := eng.NetworkList(ctx, "", nil)
	if err != nil {
		return fmt.Errorf("allocate subnet for network %s: %w", name, err)
	}
	var taken []netip.Prefix
	for _, n := range nets {
		if n.Name == name {
			return nil
		}
		taken = append(taken, parseSubnets(n.Subnets)...)
	}
	taken = append(taken, a.hostPrefixes()...)

	var lastErr error
	for range maxCreateAttempts {
		sub, ok := a.firstFree(taken)
		if !ok {
			return a.exhausted(name, lastErr)
		}
		if internal {
			err = eng.EnsureInternalNetwork(ctx, name, labels, docker.WithSubnet(sub.String()))
		} else {
			err = eng.EnsureNetwork(ctx, name, labels, docker.WithSubnet(sub.String()))
		}
		if err == nil {
			return nil
		}
		if !docker.IsPoolOverlap(err) {
			return err
		}
		lastErr = err
		taken = append(taken, sub)
	}
	return fmt.Errorf("create network %s: the daemon refused %d subnets of pool %s as overlapping (a host route covers the pool?); choose another range with --network-pool (NEBULACTRL_NETWORK_POOL): %w",
		name, maxCreateAttempts, a.pool, lastErr)
}

// exhausted is the error for a pool without a free subnet.
func (a *Allocator) exhausted(name string, cause error) error {
	err := fmt.Errorf("%w: no free /%d subnet left in %s for network %s (%d subnets, the rest overlap existing Docker networks or host addresses); widen the pool with --network-pool (NEBULACTRL_NETWORK_POOL) or use a longer --network-prefix-len (NEBULACTRL_NETWORK_PREFIX_LEN), or remove unused networks",
		ErrPoolExhausted, a.prefixLen, a.pool, name, a.Capacity())
	if cause != nil {
		err = fmt.Errorf("%w (last daemon refusal: %v)", err, cause)
	}
	return err
}

// firstFree returns the lowest subnet of the pool that overlaps none of taken.
func (a *Allocator) firstFree(taken []netip.Prefix) (netip.Prefix, bool) {
	size := uint64(1) << (32 - a.prefixLen)
	start := addrToUint(a.pool.Addr())
	end := start + uint64(1)<<(32-a.pool.Bits()) // exclusive
	for cur := start; cur < end; cur += size {
		cand := netip.PrefixFrom(uintToAddr(cur), a.prefixLen)
		if !overlapsAny(cand, taken) {
			return cand, true
		}
	}
	return netip.Prefix{}, false
}

func overlapsAny(p netip.Prefix, others []netip.Prefix) bool {
	for _, o := range others {
		if p.Overlaps(o) {
			return true
		}
	}
	return false
}

// parseSubnets keeps the IPv4 prefixes of cidrs; IPv6 never conflicts with a
// pool of IPv4 addresses, and unparsable entries are ignored.
func parseSubnets(cidrs []string) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		if p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()); p.Addr().Is4() {
			out = append(out, p.Masked())
		}
	}
	return out
}

// interfacePrefixes lists the IPv4 networks of the host's interfaces. Inside a
// container these are the container's own networks; the daemon's networks are
// covered by NetworkList.
func interfacePrefixes() []netip.Prefix {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ones, bits := ipn.Mask.Size()
		if bits == 128 && ip.Unmap().Is4() { // IPv4 in a 16-byte form
			ones -= 96
		}
		if ones < 0 {
			continue
		}
		if p := netip.PrefixFrom(ip.Unmap(), ones); p.Addr().Is4() && p.IsValid() {
			out = append(out, p.Masked())
		}
	}
	return out
}

func addrToUint(a netip.Addr) uint64 {
	b := a.As4()
	return uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
}

func uintToAddr(v uint64) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
