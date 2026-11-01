package analytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestSanitizeMasksIPLiteralHostsAndReferrers(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 30, 15, 0, time.UTC)
	b := agentprotocol.TrafficBucket{
		Host:    "127.0.0.1",
		Bucket:  now.Add(-time.Minute),
		LatHist: make([]int64, agentprotocol.LatencyBuckets),
		Top: agentprotocol.TrafficTop{Referrers: []agentprotocol.TopEntry{
			{K: "192.168.1.10", N: 1}, {K: "news.example.org", N: 2},
		}},
	}
	p, ok := sanitize(b, now, "")
	require.True(t, ok)
	require.Equal(t, agentprotocol.IPLiteralHost, p.Host)
	require.NotContains(t, string(p.Top), "192.168.1.10")
	require.Contains(t, string(p.Top), "news.example.org")

	b.Host = "App.Example.com"
	p, ok = sanitize(b, now, "")
	require.True(t, ok)
	require.Equal(t, "app.example.com", p.Host)
}
