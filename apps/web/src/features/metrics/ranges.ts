// Time ranges and their refresh cadence, shared by the resource charts
// (Metrics) and the traffic charts (Analytics). Pure, so the rules are
// testable: which ranges are "live" and how often each refetches.

export const METRIC_RANGES = [
	"5m",
	"10m",
	"30m",
	"1h",
	"6h",
	"12h",
	"24h",
	"7d",
	"30d",
] as const;
export type MetricRange = (typeof METRIC_RANGES)[number];

export const TRAFFIC_RANGES = ["15m", "1h", "6h", "24h", "7d", "30d"] as const;
export type TrafficRange = (typeof TRAFFIC_RANGES)[number];

export type RangeKind = "metrics" | "traffic";

const UNIT_MS = { m: 60_000, h: 3_600_000, d: 86_400_000 } as const;

/** "30m" → 1_800_000. Unknown shapes are 0 so a bad value never looks live. */
export function rangeMs(range: string): number {
	const match = /^(\d+)([mhd])$/.exec(range);
	if (!match) return 0;
	return Number(match[1]) * UNIT_MS[match[2] as keyof typeof UNIT_MS];
}

// Resource charts are live up to 10 minutes, traffic up to an hour
// (the 1-minute tier is real granularity there).
const LIVE_LIMIT_MS: Record<RangeKind, number> = {
	metrics: 10 * UNIT_MS.m,
	traffic: UNIT_MS.h,
};

export const LIVE_REFETCH_MS = 5_000;
export const IDLE_REFETCH_MS = 15_000;

export function isLiveRange(kind: RangeKind, range: string): boolean {
	const ms = rangeMs(range);
	return ms > 0 && ms <= LIVE_LIMIT_MS[kind];
}

/** 5 s for live ranges, 15 s otherwise. TanStack pauses hidden tabs itself. */
export function refetchIntervalMs(kind: RangeKind, range: string): number {
	return isLiveRange(kind, range) ? LIVE_REFETCH_MS : IDLE_REFETCH_MS;
}

/** True when `range` reaches further back than `retentionDays` keeps. */
export function exceedsRetention(
	range: string,
	retentionDays: number,
): boolean {
	return retentionDays > 0 && rangeMs(range) > retentionDays * UNIT_MS.d;
}

export function isMetricRange(value: unknown): value is MetricRange {
	return (METRIC_RANGES as readonly unknown[]).includes(value);
}

export function isTrafficRange(value: unknown): value is TrafficRange {
	return (TRAFFIC_RANGES as readonly unknown[]).includes(value);
}
