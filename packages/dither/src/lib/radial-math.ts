import { clamp, niceTicks, type Pt, polar } from "./chart-math";

/**
 * Pure geometry for the round charts: donut slice angles with gaps, radar
 * polygons, and the segments of a block gauge. Angles are radians measured
 * clockwise from 12 o'clock (the convention of `polar` and `arcPath`).
 */

export const TAU = Math.PI * 2;

/* ---------- donut / pie ---------- */

export interface Slice {
	index: number;
	/** The input value, with negatives and NaN read as 0. */
	value: number;
	/** Share of the total, 0…1. */
	fraction: number;
	from: number;
	to: number;
	mid: number;
}

/**
 * Angles for slices proportional to `values`, with `gap` radians cut between
 * neighbours. A zero slice takes no angle and no gap. A full circle puts half
 * a gap at each end of the first slice, so the seam sits under 12 o'clock;
 * a partial sweep has no gap at its two ends. The gap shrinks when it would
 * eat more than half the sweep.
 */
export function sliceAngles(
	values: readonly number[],
	{
		gap = 0,
		start = 0,
		sweep = TAU,
	}: { gap?: number; start?: number; sweep?: number } = {},
): Slice[] {
	const clean = values.map((v) => (Number.isFinite(v) && v > 0 ? v : 0));
	const total = clean.reduce((sum, v) => sum + v, 0);
	const live = clean.filter((v) => v > 0).length;
	if (total <= 0) {
		return clean.map((value, index) => ({
			index,
			value,
			fraction: 0,
			from: start,
			to: start,
			mid: start,
		}));
	}
	const full = sweep >= TAU - 1e-6;
	const gaps = full ? (live > 1 ? live : 0) : live - 1;
	const g = gaps > 0 ? Math.min(Math.max(0, gap), (sweep * 0.5) / gaps) : 0;
	const available = sweep - g * gaps;
	let cursor = start + (full ? g / 2 : 0);
	return clean.map((value, index) => {
		const fraction = value / total;
		const from = cursor;
		const to = from + available * fraction;
		if (value > 0) cursor = to + g;
		return { index, value, fraction, from, to, mid: (from + to) / 2 };
	});
}

/** Index of the nearest non-zero slice `step` places from `from` (wraps); `from` itself when nothing else is live. */
export function stepSlice(
	slices: readonly Slice[],
	from: number | null,
	step: 1 | -1,
): number | null {
	const n = slices.length;
	if (n === 0) return null;
	const begin = from ?? (step === 1 ? -1 : n);
	for (let k = 1; k <= n; k += 1) {
		const i = (((begin + step * k) % n) + n) % n;
		if ((slices[i]?.value ?? 0) > 0) return i;
	}
	return from;
}

/* ---------- radar ---------- */

/** Angle of axis `i` of `count`: axis 0 points up, the rest follow clockwise. */
export function radarAngle(i: number, count: number): number {
	return (i * TAU) / Math.max(1, count);
}

/** The point at `radius` along axis `i`. */
export function radarVertex(
	cx: number,
	cy: number,
	radius: number,
	i: number,
	count: number,
): Pt {
	return polar(cx, cy, radius, radarAngle(i, count));
}

/** A regular polygon ring: one vertex per axis at `radius`. */
export function radarRing(
	count: number,
	cx: number,
	cy: number,
	radius: number,
): Pt[] {
	return Array.from({ length: count }, (_, i) =>
		radarVertex(cx, cy, radius, i, count),
	);
}

/** One series' polygon: `values[i] / max` of the radius along axis i, clamped to 0…1. */
export function radarPolygon(
	values: readonly number[],
	max: number,
	cx: number,
	cy: number,
	radius: number,
): Pt[] {
	const count = values.length;
	return values.map((v, i) => {
		const share = max > 0 && Number.isFinite(v) ? clamp(v / max, 0, 1) : 0;
		return radarVertex(cx, cy, radius * share, i, count);
	});
}

/** `M x y L x y … Z` for a closed polygon. */
export function polygonPath(pts: readonly Pt[]): string {
	if (pts.length === 0) return "";
	const at = (n: number) => Math.round(n * 10) / 10;
	return `${pts.map((p, i) => `${i === 0 ? "M" : "L"}${at(p.x)} ${at(p.y)}`).join("")}Z`;
}

/** A series' numbers in axis order, from an array (padded / cut to the axes) or a record keyed by axis name. */
export function radarValues(
	values: readonly number[] | Readonly<Record<string, number>>,
	axes: readonly string[],
): number[] {
	const num = (v: unknown) =>
		typeof v === "number" && Number.isFinite(v) ? v : 0;
	if (Array.isArray(values)) {
		return axes.map((_, i) => num((values as readonly number[])[i]));
	}
	const record = values as Readonly<Record<string, number>>;
	return axes.map((axis) => num(record[axis]));
}

/** The radar's scale top: `max` when given, else the data's largest value rounded up to a round number. */
export function radarMax(
	series: readonly (readonly number[])[],
	max?: number,
): number {
	if (max !== undefined && max > 0) return max;
	const top = Math.max(0, ...series.flat());
	return top > 0 ? (niceTicks(0, top, 4).at(-1) ?? top) : 1;
}

/** Horizontal text anchor for a label at `angle` around a centre: flush to the side it sits on. */
export function labelAnchor(angle: number): "start" | "middle" | "end" {
	const s = Math.sin(angle);
	return s > 0.2 ? "start" : s < -0.2 ? "end" : "middle";
}

/* ---------- gauge ---------- */

/** Where `value` sits between `min` and `max`, 0…1. */
export function gaugeFraction(value: number, min: number, max: number): number {
	if (!Number.isFinite(value) || !(max > min)) return 0;
	return clamp((value - min) / (max - min), 0, 1);
}

/**
 * The segment that holds the value (it is drawn solid), or -1 when the value
 * is at the minimum. Segments before it are the filled ones.
 */
export function gaugeActive(fraction: number, count: number): number {
	if (!(fraction > 0)) return -1;
	return Math.min(count - 1, Math.max(0, Math.ceil(fraction * count) - 1));
}

/**
 * Which of `bands` tone bands a filled segment takes: 0 is the densest, right
 * behind the value, and the band grows with the distance back to the start.
 */
export function gaugeBand(
	index: number,
	active: number,
	bands: number,
): number {
	if (active <= 0 || bands <= 1) return 0;
	const distance = (active - index) / active;
	return clamp(Math.floor(distance * bands), 0, bands - 1);
}

export type GaugeStatus = "ok" | "warn" | "bad";

/**
 * Threshold state of a value. With `warnAt < badAt` (higher is worse: CPU) the
 * value is warn from `warnAt` and bad from `badAt`. With `badAt < warnAt`
 * (lower is worse: health) it is warn at or below `warnAt` and bad at or
 * below `badAt`. A lone threshold counts as higher-is-worse.
 */
export function gaugeStatus(
	value: number,
	warnAt?: number,
	badAt?: number,
): GaugeStatus {
	const lowerIsWorse =
		warnAt !== undefined && badAt !== undefined && badAt < warnAt;
	if (lowerIsWorse) {
		if (value <= badAt) return "bad";
		if (value <= warnAt) return "warn";
		return "ok";
	}
	if (badAt !== undefined && value >= badAt) return "bad";
	if (warnAt !== undefined && value >= warnAt) return "warn";
	return "ok";
}
