import { clamp } from "./chart-math";

/**
 * Pure maths for the bar family: group positions, stacked segment heights,
 * the tone bands of a bar body, and the status-page maths (uptime fraction,
 * how many day cells fit, which days share a cell). No DOM, no React;
 * covered by `bars-math.test.ts`.
 */

const r1 = (value: number) => Math.round(value * 10) / 10;

/* ---------- groups along x ---------- */

export interface GroupLayout {
	/** Distance between the left edges of neighbouring groups. */
	step: number;
	/** Width of a group's bars (the step less the gap between groups). */
	width: number;
	/** Empty space between neighbouring groups. */
	gap: number;
	/** Left edge of group `i`, relative to the plot's left edge. */
	x: (index: number) => number;
}

/** A gap that grows with the step but stays a hairline-to-gutter size. */
export function autoGap(step: number): number {
	const gap = clamp(Math.round(step * 0.28), 2, 16);
	return Math.max(0, Math.min(gap, step - 1));
}

/**
 * `count` equal groups across `plotW`. Each group's bars are centred in its
 * step, so the plot has half a gap of air at both ends and a pointer's
 * `floor(x / step)` finds the group.
 */
export function groupLayout(
	count: number,
	plotW: number,
	gap?: number,
): GroupLayout {
	const step = count > 0 ? plotW / count : plotW;
	const g = clamp(gap ?? autoGap(step), 0, Math.max(0, step - 1));
	return {
		step,
		width: Math.max(1, step - g),
		gap: g,
		x: (index) => r1(index * step + g / 2),
	};
}

export interface Slot {
	x: number;
	w: number;
}

/**
 * Where each series' bar sits in a group. `grouped` places them side by
 * side with `innerGap` between; `stacked` puts every series on the group's
 * full width (the segments are separated vertically).
 */
export function barSlots(
	groupX: number,
	groupW: number,
	seriesCount: number,
	mode: "grouped" | "stacked",
	innerGap = 2,
): Slot[] {
	if (seriesCount <= 0) return [];
	if (mode === "stacked" || seriesCount === 1) {
		return Array.from({ length: seriesCount }, () => ({
			x: groupX,
			w: groupW,
		}));
	}
	let gap = innerGap;
	let w = (groupW - gap * (seriesCount - 1)) / seriesCount;
	if (w < 2) {
		gap = 0;
		w = groupW / seriesCount;
	}
	return Array.from({ length: seriesCount }, (_, s) => ({
		x: r1(groupX + s * (w + gap)),
		w: Math.max(1, r1(w)),
	}));
}

/** The group under a pointer, `px` measured from the plot's left edge. */
export function slotAt(px: number, step: number, count: number): number {
	if (count <= 0 || step <= 0) return 0;
	return clamp(Math.floor(px / step), 0, count - 1);
}

/* ---------- stacking ---------- */

/** Each value as its share of the column total, 0–100. An empty column is all zeros. */
export function percentShares(values: readonly number[]): number[] {
	const clean = values.map((v) => (Number.isFinite(v) ? Math.max(0, v) : 0));
	const total = clean.reduce((sum, v) => sum + v, 0);
	return clean.map((v) => (total > 0 ? (v / total) * 100 : 0));
}

export interface PxSpan {
	/** Offset above the baseline where the segment starts, px. */
	from: number;
	/** Offset above the baseline where it ends, px. */
	to: number;
}

/**
 * Segment spans for one stacked column, in px above the baseline. A value
 * above zero is never thinner than `minPx` (so a handful of 5xx among
 * millions still shows its cap); the room that takes comes out of the
 * larger segments, so the column keeps its true total height.
 */
export function stackPixels(
	values: readonly number[],
	pxPerUnit: number,
	minPx = 2,
): PxSpan[] {
	const heights = values.map((v) =>
		Number.isFinite(v) && v > 0 ? v * pxPerUnit : 0,
	);
	let lifted = 0;
	let free = 0;
	for (const h of heights) {
		if (h > 0 && h < minPx) lifted += minPx - h;
		else free += h;
	}
	const scale =
		lifted > 0 && free > 0 ? Math.max(0, (free - lifted) / free) : 1;
	let offset = 0;
	return heights.map((h) => {
		const px = h <= 0 ? 0 : h < minPx ? minPx : Math.max(minPx, h * scale);
		const span = { from: r1(offset), to: r1(offset + px) };
		offset += px;
		return span;
	});
}

/* ---------- bar bodies ---------- */

export interface BodyBand {
	/** Index into the tone list. */
	i: number;
	/** Distance below the bar's top, px. */
	y: number;
	/** Band height, px. */
	h: number;
	tone: number;
}

/**
 * Horizontal tone bands for a bar body that is densest at its top. The
 * tones are spread over `depth` px: a bar shorter than that only gets the
 * dense end (short bars read darker), a taller one holds its last, sparsest
 * tone down to the baseline.
 */
export function bodyBands(
	height: number,
	depth: number,
	tones: readonly number[],
): BodyBand[] {
	if (height <= 0 || tones.length === 0) return [];
	const thickness = Math.max(1, depth) / tones.length;
	const out: BodyBand[] = [];
	for (let i = 0; i < tones.length; i += 1) {
		const y = i * thickness;
		if (y >= height) break;
		const last = i === tones.length - 1;
		out.push({
			i,
			y: r1(y),
			h: r1(last ? height - y : Math.min(thickness, height - y)),
			tone: tones[i] ?? 0,
		});
	}
	return out;
}

/** An axis-aligned rectangle as path data, `M x y h w v h h -w Z`. */
export function rectPath(x: number, y: number, w: number, h: number): string {
	return `M${r1(x)} ${r1(y)}h${r1(w)}v${r1(h)}h${r1(-w)}Z`;
}

/* ---------- status pages ---------- */

export type UptimeStatus = "up" | "degraded" | "down" | "unknown";

/** How bad a status is when several days share one cell. */
const SEVERITY: Record<UptimeStatus, number> = {
	unknown: 0,
	up: 1,
	degraded: 2,
	down: 3,
};

/** The worst status of the group; an empty group is `unknown`. */
export function worstStatus(statuses: readonly UptimeStatus[]): UptimeStatus {
	let worst: UptimeStatus = "unknown";
	for (const status of statuses) {
		if (SEVERITY[status] > SEVERITY[worst]) worst = status;
	}
	return worst;
}

export interface UptimeThresholds {
	/** Uptime at or above this is `up`. Default 0.999. */
	up: number;
	/** Uptime at or above this (and below `up`) is `degraded`; lower is `down`. Default 0.95. */
	degraded: number;
}

export const DEFAULT_THRESHOLDS: UptimeThresholds = {
	up: 0.999,
	degraded: 0.95,
};

export interface DayInput {
	status?: UptimeStatus | undefined;
	/** Share of the period the service was up, 0–1. */
	uptime?: number | undefined;
}

/** A day's status: the one it states, else derived from its uptime fraction, else `unknown`. */
export function dayStatus(
	day: DayInput,
	thresholds: UptimeThresholds = DEFAULT_THRESHOLDS,
): UptimeStatus {
	if (day.status) return day.status;
	const f = day.uptime;
	if (f === undefined || !Number.isFinite(f)) return "unknown";
	if (f >= thresholds.up) return "up";
	if (f >= thresholds.degraded) return "degraded";
	return "down";
}

/** What a day with a stated status but no fraction counts for in the headline figure. */
const STATUS_WEIGHT: Record<UptimeStatus, number | null> = {
	up: 1,
	degraded: 0.5,
	down: 0,
	unknown: null,
};

/** A day's share of uptime, or `null` when nothing is known about it. */
export function dayFraction(day: DayInput): number | null {
	if (day.uptime !== undefined && Number.isFinite(day.uptime)) {
		return clamp(day.uptime, 0, 1);
	}
	return STATUS_WEIGHT[dayStatus(day)];
}

/**
 * The headline uptime over a run of days: the mean of the days that have
 * data. Days with no data neither help nor hurt. `null` when none do.
 */
export function uptimeFraction(days: readonly DayInput[]): number | null {
	let sum = 0;
	let known = 0;
	for (const day of days) {
		const f = dayFraction(day);
		if (f === null) continue;
		sum += f;
		known += 1;
	}
	return known === 0 ? null : sum / known;
}

/**
 * `99.98%`. The figure is floored, never rounded up: a period with any
 * downtime must not read `100%`. Exactly 1 prints `100%`.
 */
export function formatUptime(fraction: number, digits = 2): string {
	const f = clamp(fraction, 0, 1);
	if (f >= 1) return "100%";
	const scale = 10 ** digits;
	const floored = Math.floor(f * 100 * scale + 1e-6) / scale;
	return `${Math.min(floored, 100 - 1 / scale).toFixed(digits)}%`;
}

export interface CellFit {
	/** Number of cells drawn (≤ the day count). */
	cells: number;
	/** Width of one cell, px. */
	cellW: number;
	/** Distance between the left edges of neighbouring cells. */
	step: number;
}

/**
 * How many cells fit `width` with `gap` between. Every day gets its own
 * cell while each stays at least `minCell` wide; past that, days share
 * cells (`bucketRanges`). Few days stop growing at `maxCell`.
 */
export function fitCells(
	width: number,
	count: number,
	{ gap = 2, minCell = 3, maxCell = 20 } = {},
): CellFit {
	if (count <= 0 || width <= 0) return { cells: 0, cellW: 0, step: 0 };
	const fit = Math.max(1, Math.floor((width + gap) / (minCell + gap)));
	const cells = Math.min(count, fit);
	const cellW = Math.min(maxCell, (width - gap * (cells - 1)) / cells);
	return { cells, cellW, step: cellW + gap };
}

/**
 * Split `count` days into `cells` runs of near-equal length, as
 * `[start, end)` index pairs covering every day once, in order.
 */
export function bucketRanges(
	count: number,
	cells: number,
): [start: number, end: number][] {
	if (count <= 0 || cells <= 0) return [];
	const n = Math.min(cells, count);
	return Array.from({ length: n }, (_, k): [number, number] => [
		Math.floor((k * count) / n),
		Math.floor(((k + 1) * count) / n),
	]);
}

/* ---------- lines with gaps ---------- */

export interface Run {
	/** Index of the run's first point in the source series. */
	start: number;
	/** The run's values, all finite, at consecutive indices. */
	values: number[];
}

/**
 * Split a series at its gaps: each run is a stretch of consecutive finite
 * values. `null`, `undefined` and non-finite values are gaps, and the line
 * is lifted across them rather than joined.
 */
export function splitRuns(
	values: readonly (number | null | undefined)[],
): Run[] {
	const runs: Run[] = [];
	let current: Run | null = null;
	for (let i = 0; i < values.length; i += 1) {
		const v = values[i];
		if (typeof v === "number" && Number.isFinite(v)) {
			if (current) current.values.push(v);
			else {
				current = { start: i, values: [v] };
				runs.push(current);
			}
		} else {
			current = null;
		}
	}
	return runs;
}

/**
 * Where a range band exists, as ordered [low, high] per point; `null` where
 * either edge is missing. Edges given the wrong way round are swapped.
 */
export function bandEdges(
	low: readonly (number | null | undefined)[],
	high: readonly (number | null | undefined)[],
): { low: (number | null)[]; high: (number | null)[] } {
	const n = Math.max(low.length, high.length);
	const lo: (number | null)[] = [];
	const hi: (number | null)[] = [];
	for (let i = 0; i < n; i += 1) {
		const a = low[i];
		const b = high[i];
		if (
			typeof a === "number" &&
			typeof b === "number" &&
			Number.isFinite(a) &&
			Number.isFinite(b)
		) {
			lo.push(Math.min(a, b));
			hi.push(Math.max(a, b));
		} else {
			lo.push(null);
			hi.push(null);
		}
	}
	return { low: lo, high: hi };
}

/** Every how-many-th point can carry a marker so neighbours stay `minGap` px apart. */
export function markerStride(pointStep: number, minGap = 10): number {
	if (!(pointStep > 0)) return 1;
	return Math.max(1, Math.ceil(minGap / pointStep));
}

/**
 * Indices that get a marker: none, every `stride`-th point with a value, or
 * only the last point with a value.
 */
export function markerIndices(
	values: readonly (number | null | undefined)[],
	mode: "none" | "all" | "last",
	stride = 1,
): number[] {
	if (mode === "none") return [];
	const present: number[] = [];
	values.forEach((v, i) => {
		if (typeof v === "number" && Number.isFinite(v)) present.push(i);
	});
	if (mode === "last") return present.slice(-1);
	return present.filter((i) => i % Math.max(1, stride) === 0);
}
