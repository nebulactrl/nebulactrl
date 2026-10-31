import {
	type BayerSize,
	type DitherMatrix,
	matrixTones,
} from "../../lib/bayer";

/**
 * Every background takes `matrix` and `stillTime` next to `n` and
 * `staticTime`. These resolve the pair into the one value the shader reads,
 * so a background never passes both.
 */

/** `matrix` wins; otherwise the Bayer matrix of size `n`; otherwise `fallback`. */
export function pickMatrix(
	matrix: DitherMatrix | undefined,
	n: BayerSize | undefined,
	fallback: DitherMatrix,
): DitherMatrix {
	if (matrix) return matrix;
	if (n) return `bayer${n}`;
	return fallback;
}

export interface MatrixInput {
	matrix?: DitherMatrix | undefined;
	n?: BayerSize | undefined;
	levels?: number | "full" | "stops" | undefined;
	staticTime?: number | undefined;
	stillTime?: number | undefined;
}

export interface MatrixDefaults {
	/** Matrix when neither `matrix` nor `n` is given. */
	matrix: DitherMatrix;
	/** Moment drawn when motion is off. */
	staticTime: number;
	/**
	 * Tone levels when `levels` is not given: a number, `"full"` for every tone
	 * the matrix can draw, or left out for the engine's own default.
	 */
	levels?: number | "full" | undefined;
}

export interface ResolvedMatrix {
	matrix: DitherMatrix;
	levels: number | "full" | "stops" | undefined;
	staticTime: number;
}

/** Resolves `matrix`/`n`, `levels` and `stillTime`/`staticTime` against a background's defaults. */
export function resolveMatrix(
	input: MatrixInput,
	defaults: MatrixDefaults,
): ResolvedMatrix {
	const matrix = pickMatrix(input.matrix, input.n, defaults.matrix);
	let levels = input.levels;
	if (levels === undefined && defaults.levels !== undefined) {
		levels = defaults.levels === "full" ? matrixTones(matrix) : defaults.levels;
	}
	return {
		matrix,
		levels,
		staticTime: input.stillTime ?? input.staticTime ?? defaults.staticTime,
	};
}
