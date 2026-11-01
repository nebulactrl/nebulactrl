import { clamp, linear, niceTicks } from "../../lib/chart-math";

/**
 * Pure maths behind `DitherCartesianChart`: the value scale, where the plot
 * sits inside the chart, where datum `i` sits on x and which datum a pointer
 * is over, which category labels fit, how the readout is placed and what a
 * key press does. No DOM, no React; covered by `cartesian-math.test.ts`.
 */

/** How data sit on x: `point` on the plot edges and between, `band` in equal slots. */
export type CartesianLayout = "point" | "band";

export interface Scale {
	min: number;
	max: number;
	ticks: readonly number[];
}

export interface ScaleOptions {
	/** Always include zero. Default true (areas and bars need it). */
	zero?: boolean;
	/** Force the domain, `[min, max]`; ticks are still rounded outward. */
	domain?: readonly [number, number];
	/** Roughly this many intervals. Default 4. */
	count?: number;
}

/**
 * A value domain with round ticks that covers every finite value. Non-finite
 * values are ignored; no data gives 0…1.
 */
export function buildScale(
	values: readonly number[],
	{ zero = true, domain, count = 4 }: ScaleOptions = {},
): Scale {
	const finite = values.filter((v) => Number.isFinite(v));
	let lo = domain ? domain[0] : finite.length > 0 ? Math.min(...finite) : 0;
	let hi = domain ? domain[1] : finite.length > 0 ? Math.max(...finite) : 1;
	if (zero && !domain) {
		lo = Math.min(lo, 0);
		hi = Math.max(hi, 0);
	}
	const ticks = niceTicks(lo, hi, count);
	return { min: ticks[0] ?? 0, max: ticks.at(-1) ?? 1, ticks };
}

export interface Plot {
	left: number;
	right: number;
	top: number;
	bottom: number;
}

/** Width of one monospace 10px glyph, px. */
export const AXIS_GLYPH = 6.1;

export interface PlotInput {
	width: number;
	height: number;
	axes: boolean;
	/** The tick labels as drawn, to size the left gutter. */
	tickLabels: readonly string[];
	layout: CartesianLayout;
}

/** Where the plot sits: room for tick labels on the left and x labels below when `axes`. */
export function plotBox({
	width,
	height,
	axes,
	tickLabels,
	layout,
}: PlotInput): Plot {
	if (!axes) {
		return { left: 0, right: width, top: 3, bottom: Math.max(4, height - 1) };
	}
	const longest = Math.max(1, ...tickLabels.map((l) => l.length));
	const left = Math.max(32, Math.ceil(longest * AXIS_GLYPH) + 12);
	return {
		left,
		right: Math.max(left + 1, width - (layout === "point" ? 8 : 0)),
		top: 8,
		bottom: Math.max(14, height - 20),
	};
}

/** Distance between neighbouring data (point) or the width of one slot (band). */
export function bandWidth(
	count: number,
	plot: Plot,
	layout: CartesianLayout,
): number {
	const span = Math.max(1, plot.right - plot.left);
	const slots = layout === "band" ? count : count - 1;
	return span / Math.max(1, slots);
}

/** Centre of datum `index` on x. A single `point` sits in the middle. */
export function xAt(
	index: number,
	count: number,
	plot: Plot,
	layout: CartesianLayout,
): number {
	const band = bandWidth(count, plot, layout);
	if (layout === "band") return plot.left + band * (index + 0.5);
	return count <= 1 ? (plot.left + plot.right) / 2 : plot.left + band * index;
}

/** The value → y map for a plot and a scale (min at the bottom). */
export function yScaleFor(plot: Plot, scale: Scale): (value: number) => number {
	return linear([scale.min, scale.max], [plot.bottom, plot.top]);
}

/**
 * The datum under pixel `px` (relative to the chart's left edge): the nearest
 * point in `point` layouts, the slot in `band` layouts. -1 when there is no data.
 */
export function indexAtX(
	px: number,
	count: number,
	plot: Plot,
	layout: CartesianLayout,
): number {
	if (count <= 0) return -1;
	const band = bandWidth(count, plot, layout);
	const offset = (px - plot.left) / band;
	const raw = layout === "band" ? Math.floor(offset) : Math.round(offset);
	return clamp(raw, 0, count - 1);
}

/**
 * Which category labels to draw: every `step`th (about one per `minGap` px),
 * plus the last. The one before the last drops out when it would crowd it.
 */
export function labelIndices(
	count: number,
	plotWidth: number,
	minGap = 72,
): number[] {
	if (count <= 0) return [];
	const fit = Math.max(1, Math.floor(plotWidth / minGap));
	const step = Math.max(1, Math.ceil(count / fit));
	const shown = new Set<number>();
	for (let i = 0; i < count; i += step) shown.add(i);
	const last = count - 1;
	if (!shown.has(last)) {
		const prev = last - (last % step);
		if (prev > 0 && last - prev < step * 0.7) shown.delete(prev);
		shown.add(last);
	}
	return [...shown].sort((a, b) => a - b);
}

/** Text anchor of a category label so the first and last never leave the chart. */
export function labelAnchor(
	layout: CartesianLayout,
	index: number,
	count: number,
): "start" | "middle" | "end" {
	if (layout === "band" || count <= 1) return "middle";
	if (index === 0) return "start";
	return index === count - 1 ? "end" : "middle";
}

/**
 * What a key does to the scrubbed datum. Returns the new index, `null` to
 * clear (Escape), or `undefined` when the key is not a scrub key. With nothing
 * scrubbed, ArrowLeft starts at the end and ArrowRight at the start.
 */
export function scrubKeyTarget(
	key: string,
	current: number | null,
	count: number,
): number | null | undefined {
	if (key === "Escape") return null;
	if (count <= 0) return undefined;
	const last = count - 1;
	const page = Math.max(1, Math.round(count / 10));
	switch (key) {
		case "ArrowLeft":
			return clamp((current ?? count) - 1, 0, last);
		case "ArrowRight":
			return clamp((current ?? -1) + 1, 0, last);
		case "PageUp":
			return clamp((current ?? last) - page, 0, last);
		case "PageDown":
			return clamp((current ?? 0) + page, 0, last);
		case "Home":
			return 0;
		case "End":
			return last;
		default:
			return undefined;
	}
}

export interface ReadoutAnchor {
	/** x of the crosshair / column centre. */
	x: number;
	/** y of the highest marker (or the plot bottom). */
	y: number;
	/** Chart width. */
	width: number;
	/** Top of the plot. */
	top: number;
}

/**
 * Where the readout card goes: centred above the highest marker, clamped
 * inside the chart. With no room above it sits beside the crosshair at the
 * top of the plot (right of it, or left when the right overflows) so it never
 * covers a marker.
 */
export function readoutPosition(
	anchor: ReadoutAnchor,
	box: { w: number; h: number },
	gap = 12,
): { left: number; top: number } {
	const above = anchor.y - box.h - gap;
	if (above >= 0) {
		return {
			left: clamp(anchor.x - box.w / 2, 0, Math.max(0, anchor.width - box.w)),
			top: above,
		};
	}
	const right = anchor.x + gap;
	return {
		left:
			right + box.w <= anchor.width
				? right
				: Math.max(0, anchor.x - gap - box.w),
		top: anchor.top,
	};
}

/** Opacity of series `index` while `focus` is spotlit. */
export const DIMMED_OPACITY = 0.18;
export function opacityFor(focus: number | null, index: number): number {
	return focus === null || focus === index ? 1 : DIMMED_OPACITY;
}

/**
 * Bar slots inside a band: `count` bars side by side (`grouped`) or one
 * full-width slot (`stacked`), each at most `maxWidth` wide and centred.
 */
export function barSlotsInBand(
	center: number,
	band: number,
	count: number,
	mode: "grouped" | "stacked",
	{ gap = 2, pad = 0.2, maxWidth = 36 } = {},
): { x: number; w: number }[] {
	const inner = Math.max(1, band * (1 - pad));
	const per = mode === "stacked" ? 1 : Math.max(1, count);
	const w = Math.max(1, Math.min(maxWidth, (inner - gap * (per - 1)) / per));
	const total = w * per + gap * (per - 1);
	const start = center - total / 2;
	return Array.from({ length: mode === "stacked" ? count : per }, (_, k) => ({
		x: Math.round(mode === "stacked" ? start : start + k * (w + gap)),
		w: Math.round(w),
	}));
}

/** Snap a CSS length to whole device pixels (at least one), so dither cells stay sharp. */
export function snapCellToDevice(px: number, dpr: number): number {
	const ratio = dpr > 0 ? dpr : 1;
	return Math.max(1, Math.round(px * ratio)) / ratio;
}
