import type { Pt } from "./chart-math";

/**
 * Adapter maths between Recharts' area geometry and the dither band
 * builders. Recharts hands a custom Area shape its points (nulls where the
 * data has gaps) and a baseline: a number, or one point per data point for a
 * stacked series.
 */

interface MaybePoint {
	x: number | null;
	y: number | null;
}

export interface Run {
	top: Pt[];
	/** A constant baseline y, or the lower edge sampled per point (stacked). */
	bottom: Pt[] | number;
}

/**
 * Split an area's points into unbroken runs. A point with a null x or y
 * (a gap in the data) ends the current run; a stacked series' lower edge is
 * paired by index, and a null there ends it too.
 */
export function splitRuns(
	points: readonly MaybePoint[],
	baseLine: number | readonly MaybePoint[] | undefined,
): Run[] {
	const runs: Run[] = [];
	let top: Pt[] = [];
	let bottom: Pt[] = [];
	const flush = () => {
		if (top.length > 0) {
			runs.push({
				top,
				bottom:
					typeof baseLine === "number" || baseLine === undefined
						? (baseLine ?? 0)
						: bottom,
			});
		}
		top = [];
		bottom = [];
	};
	points.forEach((p, i) => {
		const low = Array.isArray(baseLine)
			? (baseLine as readonly MaybePoint[])[i]
			: undefined;
		const broken =
			p.x === null ||
			p.y === null ||
			(Array.isArray(baseLine) && (low?.x == null || low?.y == null));
		if (broken) {
			flush();
			return;
		}
		top.push({ x: p.x as number, y: p.y as number });
		if (low && low.x !== null && low.y !== null)
			bottom.push({ x: low.x, y: low.y });
	});
	flush();
	return runs;
}

/** The lowest edge of a run (largest y): where the last tone band should end. */
export function runFloor(run: Run): number {
	const lows =
		typeof run.bottom === "number" ? [run.bottom] : run.bottom.map((p) => p.y);
	return Math.max(...lows, ...run.top.map((p) => p.y));
}

/**
 * A fade depth for a run: 85% of the distance from its highest point to its
 * floor, capped at `max`, so a short chart still gets the whole gradient.
 */
export function fadeDepth(run: Run, max = 110): number {
	const highest = Math.min(...run.top.map((p) => p.y));
	return Math.max(8, Math.min(max, (runFloor(run) - highest) * 0.85));
}
