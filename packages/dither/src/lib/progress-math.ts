/**
 * Pure maths for dithered progress and capacity bars: how a value splits into
 * stacked parts and step blocks, which tone state it is in, and the tone
 * layers that make a fill thin out behind its leading edge.
 */

import { clamp } from "./chart-math";

export type BarState = "normal" | "warn" | "bad";

/** `bad` at or above `badAt`, `warn` at or above `warnAt`. Null percent is `normal`. */
export function barState(
	percent: number | null,
	warnAt?: number,
	badAt?: number,
): BarState {
	if (percent === null) return "normal";
	if (badAt !== undefined && percent >= badAt) return "bad";
	if (warnAt !== undefined && percent >= warnAt) return "warn";
	return "normal";
}

/** A value as 0–100, clamped. A non-positive `max` reads as 0. */
export function percentOf(value: number, max: number): number {
	return max > 0 ? clamp((value / max) * 100, 0, 100) : 0;
}

export interface PartInput {
	value: number;
}

/**
 * Percent widths for stacked parts. Negative values count as 0 and the
 * running total is clamped at 100 so parts that exceed `max` never overflow
 * the track (the last visible part is cut short).
 */
export function partWidths(parts: readonly PartInput[], max: number): number[] {
	let used = 0;
	return parts.map((part) => {
		const share = percentOf(Math.max(0, part.value), max);
		const width = Math.min(share, 100 - used);
		used += width;
		return width;
	});
}

/**
 * Fill of each of `segments` equal step blocks for a value out of `max`:
 * 1 for a full block, a fraction for the partial one, 0 after it.
 */
export function segmentFills(
	value: number,
	max: number,
	segments: number,
): number[] {
	const count = Math.max(1, Math.floor(segments));
	const filled = (percentOf(value, max) / 100) * count;
	return Array.from({ length: count }, (_, i) => clamp(filled - i, 0, 1));
}

export interface FillLayer {
	/** Tone of the layer, 0..1. */
	tone: number;
	/** Layer width in px measured back from the leading edge; `null` = the whole fill. */
	reach: number | null;
}

/**
 * Layers of a gradient fill, sparsest first. Tone sets nest (a denser tile
 * contains every cell of a sparser one), so painting them right-anchored on
 * top of each other leaves the densest tone at the leading edge and thins it
 * behind. `scale` stretches the reaches for taller bars.
 */
export function gradientLayers(scale = 1): FillLayer[] {
	return [
		{ tone: 0.16, reach: null },
		{ tone: 0.28, reach: 88 * scale },
		{ tone: 0.42, reach: 58 * scale },
		{ tone: 0.58, reach: 34 * scale },
		{ tone: 0.78, reach: 16 * scale },
	];
}

/**
 * Layers of an indeterminate comet, sparsest first: a long faint tail and a
 * dense head. Widths are shares of the comet element, which is right
 * anchored.
 */
export function cometLayers(): { tone: number; share: number }[] {
	return [
		{ tone: 0.14, share: 1 },
		{ tone: 0.3, share: 0.7 },
		{ tone: 0.52, share: 0.42 },
		{ tone: 0.8, share: 0.2 },
	];
}

/**
 * Layers of the working band of a running bar, sparsest first: a faint tail and
 * a dense head, right anchored, denser than the fill it travels through. Widths
 * are shares of the band element.
 */
export function bandLayers(): { tone: number; share: number }[] {
	return [
		{ tone: 0.34, share: 1 },
		{ tone: 0.52, share: 0.62 },
		{ tone: 0.78, share: 0.3 },
	];
}

export interface SegmentSpan {
	/** Index of the part that paints this stretch. */
	part: number;
	/** Where the stretch starts inside its block, 0–1. */
	left: number;
	/** How much of the block it covers, 0–1. */
	width: number;
}

/**
 * Step blocks painted by stacked parts: for each of `segments` equal blocks,
 * the stretches each part covers, in order. Parts count out of `max` like
 * `partWidths`, so with `max` equal to `segments` a part of value 2 paints two
 * whole blocks. Negative values count as 0 and the total is cut at `max`.
 */
export function segmentPartSpans(
	values: readonly number[],
	max: number,
	segments: number,
): SegmentSpan[][] {
	const count = Math.max(1, Math.floor(segments));
	const blocks: SegmentSpan[][] = Array.from({ length: count }, () => []);
	let cumulative = 0;
	let before = new Array<number>(count).fill(0);
	values.forEach((value, part) => {
		cumulative += Math.max(0, value);
		const upTo = segmentFills(cumulative, max, count);
		for (let i = 0; i < count; i += 1) {
			const left = before[i] ?? 0;
			const width = (upTo[i] ?? 0) - left;
			if (width > 1e-9) blocks[i]?.push({ part, left, width });
		}
		before = upTo;
	});
	return blocks;
}
