package servers

import (
	"fmt"
	"time"
)

// RawMetricsRetention is how long the raw (≈10 s) metric_samples tier is kept.
// Older history lives in the 1-minute and 15-minute tiers for the server's
// metrics_retention_days.
const RawMetricsRetention = 48 * time.Hour

// MaxMetricPoints bounds the number of points in one metrics response.
const MaxMetricPoints = 300

// DefaultMetricsRetentionDays is the per-server default.
const DefaultMetricsRetentionDays = 7

// MetricTier names the stored resolution a range is read from.
type MetricTier int

const (
	// TierRaw is metric_samples (heartbeat / stats cadence).
	TierRaw MetricTier = iota
	// Tier1m is metric_samples_1m.
	Tier1m
	// Tier15m is metric_samples_15m.
	Tier15m
)

// MetricRanges lists the accepted `range` values in display order.
var MetricRanges = []string{"5m", "10m", "30m", "1h", "6h", "12h", "24h", "7d", "30d"}

// RangePlan is how a metrics range is served.
type RangePlan struct {
	Window time.Duration
	// Step is the bucket width of the response (at most MaxMetricPoints points).
	Step time.Duration
	Tier MetricTier
}

var rangePlans = map[string]RangePlan{
	"5m":  {5 * time.Minute, 10 * time.Second, TierRaw},
	"10m": {10 * time.Minute, 10 * time.Second, TierRaw},
	"30m": {30 * time.Minute, 10 * time.Second, TierRaw},
	"1h":  {time.Hour, 30 * time.Second, TierRaw},
	"6h":  {6 * time.Hour, 2 * time.Minute, Tier1m},
	"12h": {12 * time.Hour, 5 * time.Minute, Tier1m},
	"24h": {24 * time.Hour, 5 * time.Minute, Tier1m},
	"7d":  {7 * 24 * time.Hour, time.Hour, Tier15m},
	"30d": {30 * 24 * time.Hour, 3 * time.Hour, Tier15m},
}

// PlanRange maps a `range` query value (default "1h") to its window, bucket
// step and storage tier. Shared by the server and service metrics endpoints.
func PlanRange(s string) (RangePlan, error) {
	if s == "" {
		s = "1h"
	}
	p, ok := rangePlans[s]
	if !ok {
		return RangePlan{}, fmt.Errorf("invalid range %q (use one of %v)", s, MetricRanges)
	}
	return p, nil
}
