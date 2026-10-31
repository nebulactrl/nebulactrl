import { eases } from "@nebulactrl/tokens";

/** A `cubic-bezier(a, b, c, d)` token as the tuple `motion/react` takes. */
export function bezier(curve: string): [number, number, number, number] {
	const [a = 0, b = 0, c = 0, d = 0] = (curve.match(/-?\d*\.?\d+/g) ?? []).map(
		Number,
	);
	return [a, b, c, d];
}

/**
 * `--ease-smooth-out` for `motion/react` (the one curve for open, close,
 * position change and resize). Not a spring: the
 * spring below is for pressing.
 */
export const EASE_OUT = bezier(eases.smoothOut);

/** Press feedback on buttons and other tappable surfaces. */
export const SPRING_PRESS = {
	type: "spring",
	stiffness: 500,
	damping: 30,
	mass: 0.6,
} as const;
