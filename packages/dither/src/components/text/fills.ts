/**
 * Pure fill logic for `DitherText`: which cells of a glyph's pixel grid are
 * inked. Cells are in grid units (one cell = one dither pixel), so the same
 * functions serve any cell size. Nothing here reads the DOM.
 */

import {
	type BayerSize,
	bayerMatrix,
	bayerMatrixName,
	type DitherMatrix,
} from "../../lib/bayer";
import { blueNoiseThreshold } from "../../lib/blue-noise";

export type TextFill = "gradient" | "noise" | "dotted" | "hatched" | "solid";

/**
 * The way a fill thins out or drifts. `"down"` is dense at the top. A number
 * is an angle in degrees as in CSS: 0 points up, 90 right, 180 down.
 */
export type FillDirection = "up" | "down" | "left" | "right" | number;

/** One colour of a text ramp: channels 0..255, alpha 0..255. */
export type TextRgba = readonly [number, number, number, number];

export interface FillSpec {
	fill: TextFill;
	direction: FillDirection;
	/** Tone where the fill starts (dense side), 0..1. */
	from: number;
	/** Tone where the fill ends (sparse side), 0..1. */
	to: number;
	/** Noise frequency: blobs across the box height. Higher is smaller blobs. */
	scale: number;
	/** Bayer size; ignored when `matrix` is set. */
	n: BayerSize;
	/** Threshold matrix for gradient and noise. Wins over `n`. */
	matrix?: DitherMatrix | undefined;
}

export const DEFAULT_FILL_SPEC: FillSpec = {
	fill: "gradient",
	direction: "down",
	from: 1,
	to: 0.4,
	scale: 3,
	n: 4,
};

/** Opacity of the ink between dots and strokes when `ghost` is not set. */
export function defaultGhost(fill: TextFill): number {
	return fill === "dotted" || fill === "hatched" ? 0.2 : 0;
}

const clamp01 = (value: number) => Math.min(1, Math.max(0, value));

/** Deterministic 0..1 hash of an integer lattice point. */
export function hash2(x: number, y: number): number {
	let h = Math.imul(x | 0, 374761393) + Math.imul(y | 0, 668265263);
	h = Math.imul(h ^ (h >>> 13), 1274126177);
	h ^= h >>> 16;
	return (h >>> 0) / 4294967296;
}

const smooth = (t: number) => t * t * (3 - 2 * t);

/** Smooth value noise in 0..1. */
export function valueNoise(x: number, y: number): number {
	const x0 = Math.floor(x);
	const y0 = Math.floor(y);
	const fx = smooth(x - x0);
	const fy = smooth(y - y0);
	const a = hash2(x0, y0);
	const b = hash2(x0 + 1, y0);
	const c = hash2(x0, y0 + 1);
	const d = hash2(x0 + 1, y0 + 1);
	return a + (b - a) * fx + (c - a) * fy + (a - b - c + d) * fx * fy;
}

/** Four octaves of value noise, 0..1, centred near 0.5. */
export function fbm(x: number, y: number): number {
	return (
		valueNoise(x, y) * 0.5333 +
		valueNoise(x * 2.03 + 17.1, y * 2.03 + 9.2) * 0.2667 +
		valueNoise(x * 4.01 + 3.7, y * 4.01 + 31.4) * 0.1333 +
		valueNoise(x * 8.05 + 41.3, y * 8.05 + 5.9) * 0.0667
	);
}

const AXES = {
	down: { dx: 0, dy: 1 },
	up: { dx: 0, dy: -1 },
	right: { dx: 1, dy: 0 },
	left: { dx: -1, dy: 0 },
} as const;

/**
 * Unit vector of a direction with y pointing down the page. A number is a CSS
 * angle (0 up, 90 right, 180 down). The four sides are exact.
 */
export function directionVector(direction: FillDirection): {
	dx: number;
	dy: number;
} {
	if (typeof direction === "string") return AXES[direction];
	const radians = (direction * Math.PI) / 180;
	return { dx: Math.sin(radians), dy: -Math.cos(radians) };
}

/**
 * Tone (0..1) of a gradient or noise fill at cell `(x, y)` of a `w × h` grid
 * at time `t` seconds. Dotted, hatched and solid fills are patterns, not tones,
 * and return 1.
 */
export function fillTone(
	spec: FillSpec,
	x: number,
	y: number,
	w: number,
	h: number,
	t: number,
): number {
	const u = (x + 0.5) / Math.max(1, w);
	const v = (y + 0.5) / Math.max(1, h);
	const { dx, dy } = directionVector(spec.direction);
	if (spec.fill === "gradient") {
		// A slow wobble bends the bands so the gradient reads as alive, not ruled.
		const bend = 0.05 * Math.sin((u * dy - v * dx) * 7 + t * 0.9);
		const aspect = w / Math.max(1, h);
		const px = (u - 0.5) * aspect;
		const py = v - 0.5;
		const extent = Math.abs(dx) * aspect * 0.5 + Math.abs(dy) * 0.5;
		const s = clamp01((px * dx + py * dy) / (2 * extent) + 0.5 + bend);
		return spec.from + (spec.to - spec.from) * s;
	}
	if (spec.fill === "noise") {
		const aspect = w / Math.max(1, h);
		const scale = Math.max(0.1, spec.scale);
		const px = (u - 0.5) * aspect * scale - t * 0.09 * dx;
		const py = (v - 0.5) * scale - t * 0.09 * dy;
		const value = clamp01((fbm(px, py) - 0.22) / 0.56);
		return spec.from + (spec.to - spec.from) * value;
	}
	return 1;
}

/** Threshold of the fill's matrix at cell `(x, y)`, in (0, 1). */
export function thresholdAt(spec: FillSpec, x: number, y: number): number {
	const matrix = spec.matrix ?? bayerMatrixName(spec.n);
	if (matrix === "blue-noise") return blueNoiseThreshold(x, y);
	const n = matrix === "bayer2" ? 2 : matrix === "bayer8" ? 8 : 4;
	const wrap = (value: number) => ((Math.floor(value) % n) + n) % n;
	return bayerMatrix(n)[wrap(y) * n + wrap(x)] ?? 0.5;
}

/** Whole-cell step of a texture drift at time `t`: about two cells a second. */
function textureOffset(
	direction: FillDirection,
	t: number,
): { ox: number; oy: number } {
	const { dx, dy } = directionVector(direction);
	const step = Math.floor(t * 2);
	return { ox: Math.round(dx * step), oy: Math.round(dy * step) };
}

const mod = (value: number, base: number) => ((value % base) + base) % base;

/** Is the dotted lattice inked at `(x, y)`? 1 cell in 4, on a square grid. */
const dotted = (x: number, y: number) => mod(x, 2) === 0 && mod(y, 2) === 0;
/** Is the hatched lattice inked at `(x, y)`? Diagonal strokes, 1 cell in 3. */
const hatched = (x: number, y: number) => mod(x + y, 3) === 0;

/**
 * Is cell `(x, y)` inked? Gradient and noise go through the ordered dither;
 * dotted and hatched are fixed textures that step one cell at a time in the
 * drift direction; solid is always inked.
 */
export function fillInk(
	spec: FillSpec,
	x: number,
	y: number,
	w: number,
	h: number,
	t: number,
): boolean {
	switch (spec.fill) {
		case "solid":
			return true;
		case "gradient":
		case "noise":
			return fillTone(spec, x, y, w, h, t) > thresholdAt(spec, x, y);
		case "dotted": {
			const { ox, oy } = textureOffset(spec.direction, t);
			return dotted(x - ox, y - oy);
		}
		case "hatched": {
			const { ox, oy } = textureOffset(spec.direction, t);
			return hatched(x - ox, y - oy);
		}
	}
}

/**
 * The colour of cell `(x, y)` on a multi-tone ramp (lightest tone first).
 * Gradient and noise pick between the two nearest colours through the
 * matrix; textures use the first colour in the gaps and the last on the
 * strokes; solid is the last colour.
 */
export function rampColor(
	ramp: readonly TextRgba[],
	spec: FillSpec,
	x: number,
	y: number,
	w: number,
	h: number,
	t: number,
): TextRgba {
	const last = ramp[ramp.length - 1] ?? [0, 0, 0, 0];
	const first = ramp[0] ?? last;
	if (spec.fill === "solid") return last;
	if (spec.fill === "dotted" || spec.fill === "hatched")
		return fillInk(spec, x, y, w, h, t) ? last : first;
	const segments = ramp.length - 1;
	const scaled = clamp01(fillTone(spec, x, y, w, h, t)) * segments;
	const low = Math.min(segments - 1, Math.floor(scaled));
	const above = scaled - low > thresholdAt(spec, x, y);
	return (above ? ramp[low + 1] : ramp[low]) ?? last;
}

/**
 * Paint a `w × h` grid of RGBA: inked glyph cells in `ink`, the other glyph
 * cells in `ink` at `ghost` alpha (so letters keep their shape), everything
 * outside the glyphs transparent. `glyph` is coverage 0..255 per cell. With a
 * `palette` (lightest tone first, at least two colours) the ramp replaces
 * `ink` and `ghost`.
 */
export function paintTextGrid(
	glyph: Uint8ClampedArray,
	w: number,
	h: number,
	spec: FillSpec,
	ink: readonly [number, number, number],
	ghost: number,
	t: number,
	out: Uint8ClampedArray<ArrayBuffer> = new Uint8ClampedArray(w * h * 4),
	palette?: readonly TextRgba[],
): Uint8ClampedArray<ArrayBuffer> {
	const ghostAlpha = Math.round(clamp01(ghost) * 255);
	const ramp = palette && palette.length >= 2 ? palette : null;
	for (let y = 0; y < h; y += 1) {
		for (let x = 0; x < w; x += 1) {
			const p = y * w + x;
			const o = p * 4;
			const covered = (glyph[p] ?? 0) >= 128;
			if (!covered) {
				out[o] = ink[0];
				out[o + 1] = ink[1];
				out[o + 2] = ink[2];
				out[o + 3] = 0;
				continue;
			}
			if (ramp) {
				const color = rampColor(ramp, spec, x, y, w, h, t);
				out[o] = color[0];
				out[o + 1] = color[1];
				out[o + 2] = color[2];
				out[o + 3] = color[3];
				continue;
			}
			out[o] = ink[0];
			out[o + 1] = ink[1];
			out[o + 2] = ink[2];
			out[o + 3] = fillInk(spec, x, y, w, h, t) ? 255 : ghostAlpha;
		}
	}
	return out;
}
