import type { BayerSize, DitherMatrix } from "../../lib/bayer";

/** Most bands a still fade stacks. */
const MAX_BANDS = 16;

/**
 * Tones of the bands a still fade stacks, innermost first: sparse on the
 * content side, solid (1) at the edge. One band per Bayer level up to 16.
 */
export function fadeBandTones(n: BayerSize = 4): number[] {
	const bands = Math.min(n * n, MAX_BANDS);
	return Array.from({ length: bands }, (_, i) => (i + 1) / bands);
}

/**
 * The `n` that gives a matrix its band count: a Bayer matrix keeps its size,
 * blue noise (a 16×16 tile) gets the full 16 bands.
 */
export function fadeBandSize(matrix: DitherMatrix): BayerSize {
	switch (matrix) {
		case "bayer2":
			return 2;
		case "bayer4":
		case "blue-noise":
			return 4;
		case "bayer8":
			return 8;
	}
}
