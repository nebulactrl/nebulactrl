/** A point of a service's metrics series (`cpu` is null in a gap row). */
interface CpuPoint {
	cpu: number | null;
}

/** Fewest samples that draw a trend; fewer say "collecting" via no sparkline. */
const MIN_TREND_SAMPLES = 2;

/**
 * The CPU series of a card's sparkline: the sampled points only (gap rows
 * carry no CPU), and a scale from 0 to 100% of a core, or to the series' own
 * peak when a multi-replica service goes higher, so the same height means
 * the same load on every card. `null` while there are too few samples.
 */
export function cpuTrend(
	points: readonly CpuPoint[],
): { values: number[]; domain: readonly [number, number] } | null {
	const values = points.flatMap((p) => (p.cpu === null ? [] : [p.cpu]));
	if (values.length < MIN_TREND_SAMPLES) return null;
	return { values, domain: [0, Math.max(100, ...values)] };
}
