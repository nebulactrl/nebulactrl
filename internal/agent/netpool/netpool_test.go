package netpool

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
)

// fakeDaemon is a Docker daemon that enforces what the real one does for
// networks: unique names and non-overlapping subnets.
type fakeDaemon struct {
	docker.Engine // unused methods panic

	mu       sync.Mutex
	nets     map[string]*fakeNet
	order    []string // creation order of names
	internal map[string]bool
	// refuse are subnets refused as overlapping a host route the daemon knows
	// about but the agent does not.
	refuse map[string]bool
	// listDelay widens the window between listing and creating.
	listDelay time.Duration
	creates   int
}

type fakeNet struct{ subnets []string }

func newDaemon() *fakeDaemon {
	return &fakeDaemon{nets: map[string]*fakeNet{}, internal: map[string]bool{}, refuse: map[string]bool{}}
}

func (d *fakeDaemon) add(name string, subnets ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nets[name] = &fakeNet{subnets: subnets}
}

func (d *fakeDaemon) remove(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.nets, name)
}

func (d *fakeDaemon) subnetOf(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := d.nets[name]; n != nil && len(n.subnets) > 0 {
		return n.subnets[0]
	}
	return ""
}

func (d *fakeDaemon) NetworkList(_ context.Context, _ string, _ map[string]string) ([]docker.NetworkInfo, error) {
	d.mu.Lock()
	var out []docker.NetworkInfo
	for name, n := range d.nets {
		out = append(out, docker.NetworkInfo{Name: name, Subnets: n.subnets})
	}
	d.mu.Unlock()
	time.Sleep(d.listDelay)
	return out, nil
}

func (d *fakeDaemon) EnsureNetwork(_ context.Context, name string, _ map[string]string, opts ...docker.NetworkOption) error {
	return d.create(name, false, docker.ResolveNetworkOptions(opts...))
}

func (d *fakeDaemon) EnsureInternalNetwork(_ context.Context, name string, _ map[string]string, opts ...docker.NetworkOption) error {
	return d.create(name, true, docker.ResolveNetworkOptions(opts...))
}

func (d *fakeDaemon) create(name string, internal bool, o docker.NetworkOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.creates++
	if _, ok := d.nets[name]; ok {
		return nil
	}
	if o.Subnet == "" {
		return errors.New("the test daemon needs an explicit subnet")
	}
	want := netip.MustParsePrefix(o.Subnet)
	if d.refuse[o.Subnet] {
		return errors.New("Error response from daemon: invalid pool request: Pool overlaps with other one on this address space")
	}
	for _, n := range d.nets {
		for _, s := range n.subnets {
			if p := netip.MustParsePrefix(s); p.Addr().Is4() && p.Overlaps(want) {
				return errors.New("Error response from daemon: invalid pool request: Pool overlaps with other one on this address space")
			}
		}
	}
	d.nets[name] = &fakeNet{subnets: []string{o.Subnet}}
	d.order = append(d.order, name)
	d.internal[name] = internal
	return nil
}

func newAllocator(t *testing.T, pool string, prefixLen int, host ...string) *Allocator {
	t.Helper()
	a, err := New(pool, prefixLen)
	require.NoError(t, err)
	var hp []netip.Prefix
	for _, h := range host {
		hp = append(hp, netip.MustParsePrefix(h))
	}
	a.hostPrefixes = func() []netip.Prefix { return hp }
	return a
}

func ensure(t *testing.T, a *Allocator, d *fakeDaemon, name string) string {
	t.Helper()
	require.NoError(t, a.Ensure(context.Background(), d, name, nil, false))
	return d.subnetOf(name)
}

func TestDefaultsAndCapacity(t *testing.T) {
	a, err := New("", 0)
	require.NoError(t, err)
	require.Equal(t, "10.213.0.0/16", a.Pool().String())
	require.Equal(t, 24, a.PrefixLen())
	require.Equal(t, 256, a.Capacity())
}

func TestNewValidates(t *testing.T) {
	for name, tc := range map[string]struct {
		pool string
		bits int
		want string
	}{
		"not a cidr":        {"nonsense", 24, "--network-pool"},
		"ipv6 pool":         {"fd00:213::/48", 64, "IPv4"},
		"prefix too long":   {"10.213.0.0/16", 31, "--network-prefix-len"},
		"prefix below pool": {"10.213.0.0/16", 8, "--network-prefix-len"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(tc.pool, tc.bits)
			require.ErrorContains(t, err, tc.want)
		})
	}
	a, err := New("10.213.7.9/16", 24) // host bits are masked off
	require.NoError(t, err)
	require.Equal(t, "10.213.0.0/16", a.Pool().String())
}

func TestAllocatesTheFirstFreeSubnetInOrder(t *testing.T) {
	d := newDaemon()
	a := newAllocator(t, DefaultPool, 24)
	require.Equal(t, "10.213.0.0/24", ensure(t, a, d, "nebulactrl-env-a"))
	require.Equal(t, "10.213.1.0/24", ensure(t, a, d, "nebulactrl-env-b"))
	require.Equal(t, "10.213.2.0/24", ensure(t, a, d, "nebulactrl-shared-c"))
}

func TestInternalNetworksUseTheInternalCall(t *testing.T) {
	d := newDaemon()
	a := newAllocator(t, DefaultPool, 24)
	require.NoError(t, a.Ensure(context.Background(), d, "nebulactrl-env-a-int", nil, true))
	require.NoError(t, a.Ensure(context.Background(), d, "nebulactrl-env-a", nil, false))
	require.True(t, d.internal["nebulactrl-env-a-int"])
	require.False(t, d.internal["nebulactrl-env-a"])
}

func TestSkipsSubnetsOfExistingDockerNetworks(t *testing.T) {
	d := newDaemon()
	d.add("someone-elses", "10.213.0.0/23") // covers .0 and .1
	d.add("bridge", "172.17.0.0/16")        // elsewhere: irrelevant
	d.add("v6", "fd00:213::/64")            // IPv6 never blocks an IPv4 pool
	d.add("partial", "10.213.2.128/25")     // a part of .2 is enough to skip it
	a := newAllocator(t, DefaultPool, 24)
	require.Equal(t, "10.213.3.0/24", ensure(t, a, d, "nebulactrl-env-a"))
}

func TestSkipsSubnetsOfHostInterfaces(t *testing.T) {
	d := newDaemon()
	a := newAllocator(t, DefaultPool, 24, "10.213.0.17/24", "192.168.1.5/24")
	require.Equal(t, "10.213.1.0/24", ensure(t, a, d, "nebulactrl-env-a"))
}

func TestHostInterfacePrefixesAreIPv4Only(t *testing.T) {
	for _, p := range interfacePrefixes() {
		require.True(t, p.Addr().Is4(), "%s", p)
		require.Equal(t, p.Masked(), p, "host prefixes are masked")
	}
}

func TestIPv6AddressesNeverBlock(t *testing.T) {
	require.Empty(t, parseSubnets([]string{"fd00::/8", "not a subnet"}))
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}, parseSubnets([]string{"10.0.0.5/24", "fd00::/8"}))
}

func TestReusesAnExistingNetworkWhateverItsSubnet(t *testing.T) {
	d := newDaemon()
	d.add("nebulactrl-env-a", "172.20.0.0/16") // created before the pool existed
	a := newAllocator(t, DefaultPool, 24)
	require.NoError(t, a.Ensure(context.Background(), d, "nebulactrl-env-a", nil, false))
	require.Zero(t, d.creates, "no create call for an existing network")
	require.Equal(t, "172.20.0.0/16", d.subnetOf("nebulactrl-env-a"))
}

func TestReusesSubnetsFreedByRemoval(t *testing.T) {
	d := newDaemon()
	a := newAllocator(t, DefaultPool, 24)
	ensure(t, a, d, "nebulactrl-env-a")
	require.Equal(t, "10.213.1.0/24", ensure(t, a, d, "nebulactrl-env-b"))
	ensure(t, a, d, "nebulactrl-env-c")
	d.remove("nebulactrl-env-b")
	require.Equal(t, "10.213.1.0/24", ensure(t, a, d, "nebulactrl-env-d"), "the freed subnet is the first free one again")
	require.Equal(t, "10.213.3.0/24", ensure(t, a, d, "nebulactrl-env-e"))
}

func TestRetriesWithTheNextSubnetWhenTheDaemonSaysOverlap(t *testing.T) {
	d := newDaemon()
	d.refuse["10.213.0.0/24"] = true // a host route the interfaces do not show
	d.refuse["10.213.1.0/24"] = true
	a := newAllocator(t, DefaultPool, 24)
	require.Equal(t, "10.213.2.0/24", ensure(t, a, d, "nebulactrl-env-a"))
	require.Equal(t, 3, d.creates)
}

func TestOtherCreateErrorsAreNotRetried(t *testing.T) {
	d := newDaemon()
	a := newAllocator(t, DefaultPool, 24)
	err := a.Ensure(context.Background(), failing{d}, "nebulactrl-env-a", nil, false)
	require.ErrorContains(t, err, "boom")
	require.Zero(t, d.creates)
}

type failing struct{ *fakeDaemon }

func (failing) EnsureNetwork(context.Context, string, map[string]string, ...docker.NetworkOption) error {
	return errors.New("boom")
}

func TestGivesUpWhenEverySubnetIsRefused(t *testing.T) {
	d := newDaemon()
	for i := range 256 {
		d.refuse[fmt.Sprintf("10.213.%d.0/24", i)] = true
	}
	a := newAllocator(t, DefaultPool, 24)
	err := a.Ensure(context.Background(), d, "nebulactrl-env-a", nil, false)
	require.ErrorContains(t, err, "--network-pool")
	require.Equal(t, maxCreateAttempts, d.creates, "retries are bounded")
}

func TestExhaustedPoolNamesTheFlagsToChange(t *testing.T) {
	d := newDaemon()
	a := newAllocator(t, "10.213.0.0/24", 26) // four /26
	for i := range 4 {
		ensure(t, a, d, fmt.Sprintf("nebulactrl-env-%d", i))
	}
	err := a.Ensure(context.Background(), d, "nebulactrl-env-x", nil, false)
	require.ErrorIs(t, err, ErrPoolExhausted)
	msg := err.Error()
	require.Contains(t, msg, "10.213.0.0/24")
	require.Contains(t, msg, "nebulactrl-env-x")
	require.Contains(t, msg, "--network-pool")
	require.Contains(t, msg, "NEBULACTRL_NETWORK_POOL")
	require.Contains(t, msg, "--network-prefix-len")
	require.NotContains(t, d.nets, "nebulactrl-env-x")

	d.remove("nebulactrl-env-2")
	require.Equal(t, "10.213.0.128/26", ensure(t, a, d, "nebulactrl-env-x"), "removing a network makes room again")
}

func TestExhaustionByForeignNetworksAndHostAddresses(t *testing.T) {
	d := newDaemon()
	d.add("a", "10.213.0.0/25")
	a := newAllocator(t, "10.213.0.0/24", 25, "10.213.0.200/25")
	err := a.Ensure(context.Background(), d, "nebulactrl-env-x", nil, false)
	require.ErrorIs(t, err, ErrPoolExhausted)
}

func TestConcurrentAllocationsGetDistinctSubnets(t *testing.T) {
	d := newDaemon()
	d.listDelay = time.Millisecond // widens the list-then-create window
	a := newAllocator(t, DefaultPool, 24)

	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.Ensure(context.Background(), d, fmt.Sprintf("nebulactrl-env-%02d", i), nil, i%2 == 0)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	seen := map[string]bool{}
	for name := range d.nets {
		s := d.subnetOf(name)
		require.False(t, seen[s], "subnet %s allocated twice", s)
		seen[s] = true
	}
	require.Len(t, seen, n)
}
