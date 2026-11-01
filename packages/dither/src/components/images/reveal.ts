/**
 * Pure maths for `ScrollRevealImage`: the cell multipliers of the coarse-to-fine
 * steps, the scroll progress of a box, and what each progress value shows.
 */

/** Number of coarseness steps between the first blocks and full detail. */
const LADDER_STEPS = 9;

/**
 * Cell multipliers from `coarsest` down to 1 in even steps on a log scale, so
 * every step looks like the same amount of sharpening. Repeats collapse, so a
 * small `coarsest` gives fewer steps. At least `[1]`.
 */
export function coarseLadder(coarsest: number, steps = LADDER_STEPS): number[] {
	const top = Math.max(1, coarsest);
	const count = Math.max(2, steps);
	const ladder = Array.from({ length: count }, (_, index) =>
		Math.max(1, Math.round(top ** (1 - index / (count - 1)))),
	);
	return [...new Set(ladder)];
}

/** Progress kept to `precision` steps, so a scroll pass makes few renders. */
export function quantizeProgress(progress: number, precision = 240): number {
	return Math.round(Math.min(1, Math.max(0, progress)) * precision) / precision;
}

/**
 * Progress 0..1 of an element scrolling in. 0 when its top edge is at
 * `start × viewportHeight` from the top of the viewport (1 = just entering at the
 * bottom); 1 when it is at `finish × viewportHeight` (0.3 = 30% down).
 */
export function scrollProgress(
	top: number,
	viewportHeight: number,
	start = 1,
	finish = 0.3,
): number {
	const from = start * viewportHeight;
	const to = finish * viewportHeight;
	if (from === to) return top <= to ? 1 : 0;
	return Math.min(1, Math.max(0, (from - top) / (from - to)));
}

export interface RevealView {
	/** Index into the coarse steps to draw (coarsest = 0). */
	stage: number;
	/** Photo mask step 0..16 when the photo dissolves in; 0 when it is not shown yet; 16 = fully shown. */
	photo: number;
}

/** Share of the progress spent sharpening the dither; the rest dissolves the photo in. */
export const SHARPEN_SHARE = 0.72;

/**
 * What to draw at `progress`. The dither sharpens through `steps` stages over
 * the first `SHARPEN_SHARE` of the progress; then, when the reveal ends on the
 * photo, the photo dissolves in over a 16-step Bayer mask.
 */
export function revealView(
	progress: number,
	steps: number,
	end: "photo" | "dither",
): RevealView {
	const p = Math.min(1, Math.max(0, progress));
	const sharpen = end === "photo" ? SHARPEN_SHARE : 1;
	const stage = Math.min(
		steps - 1,
		Math.floor((Math.min(p, sharpen) / sharpen) * steps),
	);
	if (end === "dither" || p <= SHARPEN_SHARE) return { stage, photo: 0 };
	return {
		stage: steps - 1,
		photo: Math.round(((p - SHARPEN_SHARE) / (1 - SHARPEN_SHARE)) * 16),
	};
}
