import { clamp01 } from "./fill";

/**
 * Block digits: a string set in the page's own font, rasterised onto a grid of
 * cells and turned into letters with a lit face and an extruded side. Pure
 * maths on masks; the browser-only part (drawing the font to a canvas) lives
 * in `not-found.tsx` and hands a `GlyphMask` to these functions.
 */

/** 1 where a glyph covers the cell. Row-major. */
export interface GlyphMask {
	width: number;
	height: number;
	data: Uint8Array;
}

/** A coverage mask from RGBA pixels: a cell is in when its alpha is at least 128. */
export function maskFromRgba(
	rgba: ArrayLike<number>,
	width: number,
	height: number,
): GlyphMask {
	const data = new Uint8Array(width * height);
	for (let i = 0; i < data.length; i++) {
		data[i] = (rgba[i * 4 + 3] ?? 0) >= 128 ? 1 : 0;
	}
	return { width, height, data };
}

export interface BlockLetters {
	/** Output size: the mask's size plus the depth, so the side has room. */
	width: number;
	height: number;
	/** Rows of the face, from the first covered row to the last (for the top-to-baseline fade). */
	faceTop: number;
	faceBottom: number;
	/** 1 where the face is. */
	face: Uint8Array;
	/** 0 where empty, otherwise how many cells down and to the right of a face cell it is (1…depth). */
	side: Uint8Array;
	depth: number;
}

/**
 * The face is the mask itself; the side is the mask smeared `depth` cells down
 * and to the right, behind it. Depth 0 gives flat letters.
 */
export function extrudeMask(mask: GlyphMask, depth: number): BlockLetters {
	const d = Math.max(0, Math.floor(depth));
	const width = mask.width + d;
	const height = mask.height + d;
	const face = new Uint8Array(width * height);
	const side = new Uint8Array(width * height);
	let faceTop = height;
	let faceBottom = 0;
	for (let y = 0; y < mask.height; y++) {
		for (let x = 0; x < mask.width; x++) {
			if (mask.data[y * mask.width + x] !== 1) continue;
			face[y * width + x] = 1;
			faceTop = Math.min(faceTop, y);
			faceBottom = Math.max(faceBottom, y);
		}
	}
	for (let y = 0; y < height; y++) {
		for (let x = 0; x < width; x++) {
			if (face[y * width + x] === 1) continue;
			for (let k = 1; k <= d; k++) {
				const sx = x - k;
				const sy = y - k;
				if (sx < 0 || sy < 0 || sx >= mask.width || sy >= mask.height) continue;
				if (mask.data[sy * mask.width + sx] === 1) {
					side[y * width + x] = k;
					break;
				}
			}
		}
	}
	if (faceTop > faceBottom) {
		faceTop = 0;
		faceBottom = 0;
	}
	return { width, height, faceTop, faceBottom, face, side, depth: d };
}

const FACE_TOP_TONE = 0.95;
const FACE_BASE_TONE = 0.32;

/** Tone of a face cell at `row`: densest at the top of the letters, thinning towards the baseline. */
export function faceTone(row: number, top: number, bottom: number): number {
	if (bottom <= top) return FACE_TOP_TONE;
	const along = clamp01((row - top) / (bottom - top));
	return FACE_TOP_TONE + (FACE_BASE_TONE - FACE_TOP_TONE) * along;
}

/** Tone of a side cell `k` cells behind the face: sparse, and sparser further back. */
export function sideTone(k: number, depth: number): number {
	if (depth <= 1) return 0.3;
	return 0.34 - 0.2 * clamp01((k - 1) / (depth - 1));
}

/** Seconds the digits take to print in. */
export const PRINT_SECONDS = 1.1;
/** Levels the print-in steps through. */
export const PRINT_LEVELS = 5;

/**
 * How much of the tone is printed `elapsed` seconds after mount, 0..1, in
 * `PRINT_LEVELS` whole steps: the digits come up level by level, never smoothly.
 */
export function printGain(elapsed: number): number {
	const progress = clamp01(elapsed / PRINT_SECONDS);
	return Math.round(progress * PRINT_LEVELS) / PRINT_LEVELS;
}

/** Seconds between light sweeps, and how long one takes to cross. */
export const SWEEP_PERIOD = 5;
export const SWEEP_SECONDS = 1.3;
const SWEEP_WIDTH = 0.09;
const SWEEP_STRENGTH = 0.4;
const SWEEP_SLANT = 0.6;

/**
 * The extra tone a passing band of light adds at cell (x, y) of a `width` ×
 * `height` grid, `elapsed` seconds after the sweeps start. A slanted band
 * crosses left to right once every `SWEEP_PERIOD` seconds.
 */
export function sweepBoost(
	x: number,
	y: number,
	width: number,
	height: number,
	elapsed: number,
): number {
	if (elapsed < 0) return 0;
	const phase = (elapsed % SWEEP_PERIOD) / SWEEP_SECONDS;
	if (phase > 1) return 0;
	const centre = -SWEEP_WIDTH + (1 + 2 * SWEEP_WIDTH) * phase;
	const u = (x + y * SWEEP_SLANT) / (width + height * SWEEP_SLANT);
	return SWEEP_STRENGTH * clamp01(1 - Math.abs(u - centre) / SWEEP_WIDTH);
}
