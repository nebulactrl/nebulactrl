import { clamp } from "../../lib/chart-math";

/**
 * Gauge geometry for all three shapes: the 270° `dial`, the 180° `half` and
 * the full `ring` that starts at the top. Pure; covered by
 * `gauge-math.test.ts`.
 */

export type GaugeShape = "dial" | "half" | "ring";

/** Where the arc starts and how far it sweeps, in radians clockwise from 3 o'clock. */
export function shapeSpan(shape: GaugeShape): { start: number; sweep: number } {
	if (shape === "dial") {
		return { start: (-3 * Math.PI) / 4, sweep: (3 * Math.PI) / 2 };
	}
	if (shape === "half") return { start: -Math.PI / 2, sweep: Math.PI };
	return { start: 0, sweep: Math.PI * 2 };
}

/** Blocks around the arc when the caller does not say: 24 for the arcs, 48 for the ring. */
export function defaultSegments(shape: GaugeShape): number {
	return shape === "ring" ? 48 : 24;
}

export interface ShapeSegment {
	from: number;
	to: number;
	mid: number;
}

/**
 * `count` equal blocks around the arc. `gapShare` (0…0.9) is the part of each
 * block's pitch left empty, split evenly either side of it.
 */
export function shapeSegments(
	count: number,
	shape: GaugeShape,
	gapShare = 0.2,
): ShapeSegment[] {
	const { start, sweep } = shapeSpan(shape);
	const n = Math.max(1, Math.floor(count));
	const pitch = sweep / n;
	const span = pitch * (1 - clamp(gapShare, 0, 0.9));
	return Array.from({ length: n }, (_, i) => {
		const mid = start + pitch * (i + 0.5);
		return { from: mid - span / 2, to: mid + span / 2, mid };
	});
}

/** The angle of a 0…1 fraction along the arc. */
export function shapeAngle(fraction: number, shape: GaugeShape): number {
	const { start, sweep } = shapeSpan(shape);
	return start + clamp(fraction, 0, 1) * sweep;
}

export interface ShapeBox {
	width: number;
	height: number;
	cx: number;
	cy: number;
	radius: number;
}

/**
 * Drawing box for a gauge `size` px wide with `pad` px of room around the
 * arc. The dial stops at the bottom of its arc ends, the half gauge at its
 * baseline and the ring is square.
 */
export function shapeBox(
	shape: GaugeShape,
	size: number,
	pad: number,
): ShapeBox {
	const radius = Math.max(1, size / 2 - pad);
	const cx = size / 2;
	const cy = size / 2;
	const below =
		shape === "dial" ? radius * Math.SQRT1_2 : shape === "ring" ? radius : 0;
	return {
		width: size,
		height: Math.ceil(cy + below + pad),
		cx,
		cy,
		radius,
	};
}

/**
 * Index range `[from, to]` of the filled blocks that are new since the last
 * drawn value, so only they animate in. `previous` -1 means nothing was filled.
 */
export function newBlocks(
	previous: number,
	active: number,
): readonly [number, number] | null {
	return active > previous ? [previous + 1, active] : null;
}
