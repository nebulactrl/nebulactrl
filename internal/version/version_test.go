package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	for _, s := range []string{"1.4.0", "v1.4.0", "0.2.10", "1.0.0-rc.1", "1.0.0-alpha", "1.0.0+build.5", " 2.0.0 "} {
		_, ok := Parse(s)
		require.True(t, ok, s)
	}
	for _, s := range []string{"", "dev", "latest", "1.4", "1.4.0.1", "01.4.0", "1.4.x", "1.4.0-", "v1.4.0-3-gabc1234", "1.4.0-12-gabc1234-dirty", "1.0.0-01", "-1.0.0", "1.0.0-a_b"} {
		_, ok := Parse(s)
		require.False(t, ok, s)
	}
}

func TestCompare(t *testing.T) {
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "1.10.0", "2.0.0"}
	for i := range order {
		for j := range order {
			a, _ := Parse(order[i])
			b, _ := Parse(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			require.Equal(t, want, Compare(a, b), "%s vs %s", order[i], order[j])
		}
	}
	a, _ := Parse("v1.2.3+a")
	b, _ := Parse("1.2.3+b")
	require.Equal(t, 0, Compare(a, b), "build metadata is ignored")
}

func TestComputeSkew(t *testing.T) {
	cases := []struct {
		agent, control string
		want           Skew
		auto           bool
	}{
		{"1.3.0", "1.4.0", SkewBehind, true},
		{"v1.3.0", "1.4.0", SkewBehind, true},
		{"1.4.0", "1.4.0", SkewCurrent, false},
		{"v1.4.0", "1.4.0", SkewCurrent, false},
		{"1.5.0", "1.4.0", SkewAhead, false},
		{"1.4.0-rc.1", "1.4.0", SkewBehind, true},
		{"dev", "dev", SkewCurrent, false},
		{"dev", "1.4.0", SkewUnknown, false},
		{"1.4.0", "dev", SkewUnknown, false},
		{"", "1.4.0", SkewUnknown, false},
		{"v1.3.0-4-gabc", "1.4.0", SkewUnknown, false},
	}
	for _, c := range cases {
		got := Compute(c.agent, c.control)
		require.Equal(t, c.want, got, "%q vs %q", c.agent, c.control)
		require.Equal(t, c.auto, got.AutoUpgradable(), "%q vs %q", c.agent, c.control)
	}
}

func TestCurrent(t *testing.T) {
	SetCurrent(" 1.2.3 ")
	require.Equal(t, "1.2.3", Current())
	SetCurrent("")
	require.Equal(t, "dev", Current())
}
