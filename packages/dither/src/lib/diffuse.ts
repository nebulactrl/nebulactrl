/**
 * Error diffusion as a general engine primitive.
 *
 * Ordered dithers decide each cell on its own, so they animate calmly.
 * Diffusion rounds each cell to the nearest level and pushes the rounding
 * error onto the cells not yet visited, so every cell depends on all the ones
 * before it: a small change anywhere can move dots everywhere. Use it for
 * still pictures, never for motion.
 *
 * This works on any tone field (a chart's density grid, a mask, a photo's
 * luminance), not only images. The image components build on the same
 * kernels.
 */

export type DiffusionMethod = "floyd-steinberg" | "atkinson" | "sierra-lite";

export interface DiffusionTap {
	/** Columns to the right (negative: left) of the cell being rounded. */
	dx: number;
	/** Rows below. Always 0 or more. */
	dy: number;
	/** Share of the error sent there. */
	weight: number;
}

/** Floyd–Steinberg: all of the error, 7/16 right and the rest below. */
const FLOYD_STEINBERG: readonly DiffusionTap[] = [
	{ dx: 1, dy: 0, weight: 7 / 16 },
	{ dx: -1, dy: 1, weight: 3 / 16 },
	{ dx: 0, dy: 1, weight: 5 / 16 },
	{ dx: 1, dy: 1, weight: 1 / 16 },
];

/** Atkinson spreads only 6/8 of the error, so highlights and shadows stay clean. */
const ATKINSON: readonly DiffusionTap[] = [
	{ dx: 1, dy: 0, weight: 1 / 8 },
	{ dx: 2, dy: 0, weight: 1 / 8 },
	{ dx: -1, dy: 1, weight: 1 / 8 },
	{ dx: 0, dy: 1, weight: 1 / 8 },
	{ dx: 1, dy: 1, weight: 1 / 8 },
	{ dx: 0, dy: 2, weight: 1 / 8 },
];

/** Sierra Lite: the cheapest kernel, half the error to the right, a quarter down. */
const SIERRA_LITE: readonly DiffusionTap[] = [
	{ dx: 1, dy: 0, weight: 2 / 4 },
	{ dx: -1, dy: 1, weight: 1 / 4 },
	{ dx: 0, dy: 1, weight: 1 / 4 },
];

export const DIFFUSION_TAPS: Readonly<
	Record<DiffusionMethod, readonly DiffusionTap[]>
> = {
	"floyd-steinberg": FLOYD_STEINBERG,
	atkinson: ATKINSON,
	"sierra-lite": SIERRA_LITE,
};

export const DIFFUSION_METHODS: readonly DiffusionMethod[] = [
	"floyd-steinberg",
	"atkinson",
	"sierra-lite",
];

export function isDiffusionMethod(value: string): value is DiffusionMethod {
	return value in DIFFUSION_TAPS;
}

export interface DiffuseOptions {
	/** Default `floyd-steinberg`. */
	method?: DiffusionMethod;
	/** Output levels, at least 2. Default 2 (ink or paper). */
	levels?: number;
	/** Alternate scan direction per row, which hides diffusion worms. Default true. */
	serpentine?: boolean;
}

/**
 * Round a `width × height` tone field (0..1, row-major, row 0 on top) to
 * `levels` evenly spaced steps with error diffusion. Returns the level index
 * (0 … levels−1) of every cell. The input is not modified. Deterministic.
 */
export function diffuseTones(
	tones: ArrayLike<number>,
	width: number,
	height: number,
	options: DiffuseOptions = {},
): Uint8Array {
	const taps = DIFFUSION_TAPS[options.method ?? "floyd-steinberg"];
	const levels = Math.max(2, Math.min(256, Math.floor(options.levels ?? 2)));
	const serpentine = options.serpentine ?? true;
	const steps = levels - 1;
	const work = Float32Array.from({ length: width * height }, (_, i) => {
		const tone = tones[i] ?? 0;
		return tone < 0 ? 0 : tone > 1 ? 1 : tone;
	});
	const out = new Uint8Array(width * height);
	for (let y = 0; y < height; y += 1) {
		const reverse = serpentine && y % 2 === 1;
		for (let step = 0; step < width; step += 1) {
			const x = reverse ? width - 1 - step : step;
			const i = y * width + x;
			const value = work[i] ?? 0;
			const index = Math.min(steps, Math.max(0, Math.round(value * steps)));
			out[i] = index;
			const error = value - index / steps;
			if (error === 0) continue;
			for (const tap of taps) {
				const nx = x + (reverse ? -tap.dx : tap.dx);
				const ny = y + tap.dy;
				if (nx < 0 || nx >= width || ny >= height) continue;
				work[ny * width + nx] =
					(work[ny * width + nx] ?? 0) + error * tap.weight;
			}
		}
	}
	return out;
}
