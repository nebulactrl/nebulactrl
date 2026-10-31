package build

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseTotal(t *testing.T) {
	cases := []struct {
		line string
		want int64
		ok   bool
	}{
		{"Total:\t1.234GB", 1_234_000_000, true},
		{"Total:	0B", 0, true},
		{"Total:  512.5MB", 512_500_000, true},
		{"Total:\t3kB", 3000, true},
		{"Total:\t2TB", 2_000_000_000_000, true},
		{"Deleted Build Cache:", 0, false},
		{"Total:\tlots", 0, false},
		{"Total: 5 parsecs", 0, false},
	}
	for _, c := range cases {
		got, ok := parseTotal(c.line)
		require.Equal(t, c.ok, ok, c.line)
		require.Equal(t, c.want, got, c.line)
	}
}
