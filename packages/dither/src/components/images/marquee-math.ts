/** Pure maths for `LogoMarquee`. */

/** Seconds for the row to coast to a stop or back up to speed. */
export const EASE_SECONDS = 0.45;

/**
 * The playback rate one frame after `rate`, `delta` seconds later, easing
 * towards `target` (0 held, 1 running). Snaps to the target once it is within
 * 0.01, so the animation can stop asking for frames.
 */
export function nextRate(rate: number, target: number, delta: number): number {
	const next = rate + (target - rate) * Math.min(1, (delta / EASE_SECONDS) * 4);
	return Math.abs(next - target) < 0.01 ? target : next;
}

/** Copies of the row needed to fill `width` with rows of `rowWidth`, at least 2 (one to slide into). */
export function loopCopies(width: number, rowWidth: number): number {
	if (rowWidth <= 0 || width <= 0) return 2;
	return Math.max(2, Math.ceil(width / rowWidth) + 1);
}
