package fleet

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func host(name string, total, used, committed int64, eligible bool) *Host {
	return &Host{ID: uuid.New(), Name: name, Eligible: eligible, HasData: total > 0, MemTotal: total, MemUsed: used, Committed: committed}
}

func TestHeadroomUsesTheLargerOfUsageAndCommitment(t *testing.T) {
	const g = int64(1) << 30
	require.Equal(t, 6*g, host("a", 8*g, 2*g, 1*g, true).Headroom())
	require.Equal(t, 4*g, host("a", 8*g, 2*g, 4*g, true).Headroom())
	require.Less(t, (&Host{}).Headroom(), int64(0), "no data ranks last")
}

func TestPickTargetPrefersMostFreeAndReservesDemand(t *testing.T) {
	const g = int64(1) << 30
	big, small, off := host("big", 16*g, 1*g, 0, true), host("small", 8*g, 1*g, 0, true), host("off", 64*g, 0, 0, false)
	hosts := []*Host{small, off, big}

	require.Same(t, big, PickTarget(hosts, Demand{Bytes: 10 * g}))
	// big now has 5 GiB left, small has 7: the next service goes to small.
	require.Same(t, small, PickTarget(hosts, Demand{Bytes: 1 * g}))

	fits, reasons := Fit(hosts)
	require.True(t, fits)
	require.Empty(t, reasons)

	PickTarget(hosts, Demand{Bytes: 20 * g})
	fits, reasons = Fit(hosts)
	require.False(t, fits)
	require.Len(t, reasons, 1)
	require.Contains(t, reasons[0], "over capacity")

	require.Nil(t, PickTarget([]*Host{off}, Demand{}))
}

func TestPickTargetTieBreaksByName(t *testing.T) {
	const g = int64(1) << 30
	a, b := host("a", 8*g, 0, 0, true), host("b", 8*g, 0, 0, true)
	require.Same(t, a, PickTarget([]*Host{b, a}, Demand{}))
}

func TestOvercommitted(t *testing.T) {
	require.True(t, Overcommitted(5, 4))
	require.False(t, Overcommitted(4, 4))
	require.False(t, Overcommitted(5, 0), "unknown total is not overcommitted")
}

func TestFormatBytes(t *testing.T) {
	require.Equal(t, "512 MiB", FormatBytes(512<<20))
	require.Equal(t, "1.5 GiB", FormatBytes(3<<29))
	require.Equal(t, "2 GiB", FormatBytes(2<<30))
	require.Equal(t, "0 KiB", FormatBytes(-5))
}
