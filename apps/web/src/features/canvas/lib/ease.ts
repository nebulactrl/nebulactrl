/** How long Arrange takes to move the cards to their new places. */
export const ARRANGE_MS = 300;

/** `ease-in-out` with punch: for a thing that is already on screen and moves. */
export const ARRANGE_EASE = [0.77, 0, 0.175, 1] as const;

/**
 * The CSS `cubic-bezier(x1, y1, x2, y2)` timing function as a function of
 * time (0 to 1), so a frame loop can follow the curve a transition would.
 */
export function cubicBezier(
	x1: number,
	y1: number,
	x2: number,
	y2: number,
): (t: number) => number {
	const at = (u: number, a: number, b: number) =>
		3 * (1 - u) * (1 - u) * u * a + 3 * (1 - u) * u * u * b + u * u * u;
	const slope = (u: number, a: number, b: number) =>
		3 * (1 - u) * (1 - u) * a + 6 * (1 - u) * u * (b - a) + 3 * u * u * (1 - b);
	return (t) => {
		if (t <= 0) return 0;
		if (t >= 1) return 1;
		// Newton from the straight guess, then bisection if it stalls.
		let u = t;
		for (let i = 0; i < 8; i++) {
			const error = at(u, x1, x2) - t;
			if (Math.abs(error) < 1e-6) return at(u, y1, y2);
			const d = slope(u, x1, x2);
			if (Math.abs(d) < 1e-6) break;
			u -= error / d;
		}
		let low = 0;
		let high = 1;
		u = t;
		for (let i = 0; i < 24; i++) {
			if (at(u, x1, x2) < t) low = u;
			else high = u;
			u = (low + high) / 2;
		}
		return at(u, y1, y2);
	};
}

export const arrangeEase = cubicBezier(...ARRANGE_EASE);
