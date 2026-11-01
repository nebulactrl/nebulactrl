/**
 * Ordered-dither threshold matrices. A cell is inked when the tone at that
 * cell is above its threshold, so a tone of 0.25 inks a quarter of the cells,
 * spread evenly across the tile. Bayer ranks are generic maths (bit
 * interleaving of x xor y and y); thresholds sit halfway between steps,
 * `(rank + 0.5) / (n * n)`, so no tone ever lands exactly on one.
 */

import { BLUE_NOISE_PATTERN_SIDE, blueNoiseRanks } from "./blue-noise";

export type BayerSize = 2 | 4 | 8;

/** Every threshold matrix the engine draws with. */
export type DitherMatrix = "bayer2" | "bayer4" | "bayer8" | "blue-noise";

export const DITHER_MATRICES: readonly DitherMatrix[] = [
	"bayer2",
	"bayer4",
	"bayer8",
	"blue-noise",
];

/** Side of a static tile: a Bayer size, or the compact blue-noise pattern tile. */
export type TileSide = BayerSize | 16;

/** `"bayer8"` -> 8; `"blue-noise"` -> 16 (the compact tile used by SVG and CSS fills). */
export function matrixTileSide(matrix: DitherMatrix): TileSide {
	if (matrix === "bayer2") return 2;
	if (matrix === "bayer4") return 4;
	if (matrix === "bayer8") return 8;
	return BLUE_NOISE_PATTERN_SIDE as TileSide;
}

/** The matrix for a Bayer size (the `n` props). */
export function bayerMatrixName(n: BayerSize): DitherMatrix {
	return n === 2 ? "bayer2" : n === 8 ? "bayer8" : "bayer4";
}

/** Ranks of a static tile, row-major, 0 = inked first. Bayer or the 16×16 blue-noise tile. */
export function tileRanks(side: TileSide): ArrayLike<number> {
	return side === 16 ? blueNoiseRanks(16) : ranksFor(side);
}

const cache = new Map<BayerSize, Float32Array>();
const rankCache = new Map<BayerSize, Uint8Array>();

/** Bayer ranks, 0 … n²−1, row-major: interleave the bits of (x xor y) and y. */
function ranksFor(n: BayerSize): Uint8Array {
	const cached = rankCache.get(n);
	if (cached) return cached;
	const ranks = new Uint8Array(n * n);
	for (let y = 0; y < n; y += 1) {
		for (let x = 0; x < n; x += 1) {
			let rank = 0;
			for (let bit = 1; bit < n; bit *= 2) {
				const xBit = Math.floor(x / bit) % 2;
				const yBit = Math.floor(y / bit) % 2;
				rank = rank * 4 + 2 * ((xBit + yBit) % 2) + yBit;
			}
			ranks[y * n + x] = rank;
		}
	}
	rankCache.set(n, ranks);
	return ranks;
}

/** Integer ranks of the matrix, row-major (0 = inked first). Cached. */
export function bayerRanks(n: BayerSize): Uint8Array {
	return ranksFor(n);
}

/** Thresholds in [0, 1), row-major. Cached: the same array is returned every call. */
export function bayerMatrix(n: BayerSize): Float32Array {
	const cached = cache.get(n);
	if (cached) return cached;
	const cells = n * n;
	const matrix = Float32Array.from(ranksFor(n), (rank) => (rank + 0.5) / cells);
	cache.set(n, matrix);
	return matrix;
}

/** Positive modulo, so negative cell coordinates tile too. */
const wrap = (value: number, n: number) => ((value % n) + n) % n;

/** Ordered-dither test: is cell (x, y) inked at `tone` (0..1)? */
export function ditherOn(
	tone: number,
	x: number,
	y: number,
	n: BayerSize = 4,
): boolean {
	const matrix = bayerMatrix(n);
	const threshold = matrix[wrap(Math.floor(y), n) * n + wrap(Math.floor(x), n)];
	return threshold !== undefined && tone > threshold;
}

/** Snap a tone to the nearest of `levels` evenly spaced steps in [0, 1]. */
export function quantize(tone: number, levels: number): number {
	const clamped = Math.min(1, Math.max(0, tone));
	if (levels <= 1) return clamped > 0.5 ? 1 : 0;
	const steps = levels - 1;
	return Math.round(clamped * steps) / steps;
}

/** How many of the side² cells of a tile are inked at `tone`. */
export function inkedCells(tone: number, n: TileSide = 4): number {
	const cells = n * n;
	return Math.min(cells, Math.max(0, Math.round(tone * cells)));
}

/** The value of the shader's `u_n`: 2, 4 or 8 for Bayer, 64 for the blue-noise tile. */
export function matrixShaderCode(matrix: DitherMatrix): 2 | 4 | 8 | 64 {
	if (matrix === "bayer2") return 2;
	if (matrix === "bayer4") return 4;
	if (matrix === "bayer8") return 8;
	return 64;
}

/** Distinct thresholds a blue-noise tile draws: 256 levels of 16 cells. */
const BLUE_NOISE_TONES = 256;

/** Tones a matrix can draw from none to all: its cell count plus one. */
export function matrixTones(matrix: DitherMatrix): number {
	switch (matrix) {
		case "bayer2":
			return 5;
		case "bayer4":
			return 17;
		case "bayer8":
			return 65;
		case "blue-noise":
			return BLUE_NOISE_TONES;
	}
}
