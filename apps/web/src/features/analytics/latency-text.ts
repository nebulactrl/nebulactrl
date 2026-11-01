import { formatMs } from "#/features/metrics/chart-format";

/**
 * A latency percentile for display. With no requests there is nothing to
 * take a percentile of: the report carries 0, which `formatMs` would print
 * as "<1 ms", a speed nobody measured. Show the empty mark instead.
 */
export function latencyText(requests: number, ms: number): string {
	return requests > 0 ? formatMs(ms) : "–";
}
