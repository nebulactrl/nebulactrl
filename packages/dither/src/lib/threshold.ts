/**
 * One threshold lookup for every ordered matrix the engine draws with:
 * Bayer 2/4/8 and the 64×64 blue-noise tile. Thresholds are in [0, 1); a cell
 * is inked when its tone is above the threshold. The GPU path in
 * `gl/shader-source.ts` reads the same values.
 */

import { type BayerSize, bayerMatrix, type DitherMatrix } from "./bayer";
import { BLUE_NOISE_SIDE, blueNoiseThresholds } from "./blue-noise";

export type { DitherMatrix } from "./bayer";

const BAYER_SIZE: Record<Exclude<DitherMatrix, "blue-noise">, BayerSize> = {
	bayer2: 2,
	bayer4: 4,
	bayer8: 8,
};

/** The matrix named by a matrix name or a Bayer size (the `n` props). */
export function toMatrix(
	value: DitherMatrix | BayerSize | undefined,
): DitherMatrix {
	if (value === undefined) return "bayer4";
	if (value === 2) return "bayer2";
	if (value === 4) return "bayer4";
	if (value === 8) return "bayer8";
	return value as DitherMatrix;
}

type Sampler = (x: number, y: number) => number;
const samplers = new Map<DitherMatrix, Sampler>();

function buildSampler(matrix: DitherMatrix): Sampler {
	if (matrix === "blue-noise") {
		const tile = blueNoiseThresholds();
		const side = BLUE_NOISE_SIDE;
		return (x, y) =>
			tile[
				(((y % side) + side) % side) * side + (((x % side) + side) % side)
			] ?? 0.5;
	}
	const n = BAYER_SIZE[matrix];
	const tile = bayerMatrix(n);
	return (x, y) => tile[(((y % n) + n) % n) * n + (((x % n) + n) % n)] ?? 0.5;
}

/**
 * A `(x, y) => threshold` function for `matrix`. Coordinates may be any
 * integers; negative ones tile too. Build it once per image, not per pixel.
 */
export function thresholdSampler(matrix: DitherMatrix): Sampler {
	const cached = samplers.get(matrix);
	if (cached) return cached;
	const made = buildSampler(matrix);
	samplers.set(matrix, made);
	return made;
}

/** Threshold of one cell. Slower than a sampler in a loop; fine for a few cells. */
export function thresholdAt(
	matrix: DitherMatrix,
	x: number,
	y: number,
): number {
	return thresholdSampler(matrix)(x, y);
}

/** Is cell (x, y) inked at `tone`? */
export function inkedAt(
	tone: number,
	x: number,
	y: number,
	matrix: DitherMatrix,
): boolean {
	return tone > thresholdAt(matrix, x, y);
}
