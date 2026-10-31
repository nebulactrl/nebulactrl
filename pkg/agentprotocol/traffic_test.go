package agentprotocol

import (
	"math"
	"math/rand/v2"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func histogram(samples []float64) []int64 {
	h := make([]int64, LatencyBuckets)
	for _, ms := range samples {
		h[LatencyBucket(ms)]++
	}
	return h
}

func exactQuantile(sorted []float64, q float64) float64 {
	return sorted[min(len(sorted)-1, int(q*float64(len(sorted))))]
}

func TestLatencyBucketBounds(t *testing.T) {
	require.Equal(t, 0, LatencyBucket(0.2))
	require.Equal(t, 0, LatencyBucket(1), "bounds are upper-inclusive")
	require.Equal(t, 1, LatencyBucket(1.01))
	require.Equal(t, 26, LatencyBucket(60000))
	require.Equal(t, 27, LatencyBucket(60000.1), "overflow")
	require.Equal(t, 28, LatencyBuckets)
}

// TestLatencyQuantileAgainstExact checks the histogram estimate against exact
// quantiles. Two things are asserted:
//
//   - the estimate always falls in the same bucket as the exact quantile (the
//     real guarantee of a fixed-bucket histogram), and
//   - the relative error stays within a per-distribution tolerance. Smooth
//     distributions land within 15 %. A cluster much narrower than its bucket
//     is the worst case for log-space interpolation, which assumes mass is
//     spread log-uniformly across the bucket: the bimodal case puts its 800 ms
//     cluster (sigma 20 ms) in the lower part of the 750-1000 ms bucket (ratio
//     1.33), and p99 sits 90 % of the way through that cluster's rank, so the
//     estimate (~970 ms) overshoots the exact value (~826 ms) by ~18 %.
func TestLatencyQuantileAgainstExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	dists := []struct {
		name string
		tol  float64
		gen  func() float64
	}{
		{"lognormal median 40ms", 0.15, func() float64 { return math.Exp(math.Log(40) + 0.8*rng.NormFloat64()) }},
		{"lognormal median 300ms", 0.15, func() float64 { return math.Exp(math.Log(300) + 1.2*rng.NormFloat64()) }},
		{"bimodal", 0.25, func() float64 {
			if rng.Float64() < 0.9 {
				return 5 + rng.Float64()
			}
			return 800 + 20*rng.NormFloat64()
		}},
		{"exponential mean 120ms", 0.15, func() float64 { return rng.ExpFloat64() * 120 }},
	}
	for _, d := range dists {
		t.Run(d.name, func(t *testing.T) {
			samples := make([]float64, 50000)
			for i := range samples {
				samples[i] = d.gen()
			}
			h := histogram(samples)
			sort.Float64s(samples)
			for _, q := range []float64{0.5, 0.95, 0.99} {
				want, got := exactQuantile(samples, q), LatencyQuantile(h, q)
				require.Equal(t, LatencyBucket(want), LatencyBucket(got), "q=%v: estimate %v left the bucket of exact %v", q, got, want)
				require.InEpsilon(t, want, got, d.tol, "q=%v", q)
			}
		})
	}
}

func TestLatencyHistogramsMergeByAddition(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	var all, a, b []float64
	for i := 0; i < 20000; i++ {
		v := math.Exp(math.Log(60) + rng.NormFloat64())
		all = append(all, v)
		if i%3 == 0 {
			a = append(a, v)
		} else {
			b = append(b, v)
		}
	}
	ha, hb := histogram(a), histogram(b)
	merged := make([]int64, LatencyBuckets)
	for i := range merged {
		merged[i] = ha[i] + hb[i]
	}
	require.Equal(t, histogram(all), merged)
	require.Equal(t, LatencyQuantile(histogram(all), 0.95), LatencyQuantile(merged, 0.95))
}

func TestLatencyQuantileEdgeCases(t *testing.T) {
	require.Zero(t, LatencyQuantile(make([]int64, LatencyBuckets), 0.95), "empty histogram")
	over := make([]int64, LatencyBuckets)
	over[LatencyBuckets-1] = 5
	require.Equal(t, 60000.0, LatencyQuantile(over, 0.5), "overflow reports the last bound")
	one := make([]int64, LatencyBuckets)
	one[LatencyBucket(100)] = 1
	p := LatencyQuantile(one, 0.5)
	require.Greater(t, p, 75.0)
	require.LessOrEqual(t, p, 100.0)
}

func TestMaskIPHost(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.5", "::1", "[::1]", "2001:db8::1", "fe80::1%eth0", "::ffff:192.0.2.1"} {
		require.Equal(t, IPLiteralHost, MaskIPHost(ip), ip)
	}
	for _, h := range []string{"app.localhost", "1.example.com", "localhost", "(none)", "(other)", "999.1.1.1", "a.b.c.d", ""} {
		require.Equal(t, h, MaskIPHost(h), h)
	}
}
