import { type DitherColor, INK, PAPER } from "./common";

/**
 * The six colours of a nebula, in the order `DitherNebula`'s `palette` takes
 * them: sky, thin gas, mid gas, the brightest knots, the rim light on some
 * filaments, and the stars.
 */
export type NebulaPalette = readonly [
	sky: DitherColor,
	deep: DitherColor,
	mid: DitherColor,
	bright: DitherColor,
	rim: DitherColor,
	star: DitherColor,
];

/** Deep indigo, the `#6d6bff` accent, magenta knots, a cyan rim and ink-coloured stars on the page's paper. */
export const NEBULA_PALETTE: NebulaPalette = [
	PAPER,
	"#3a36b0",
	"#6d6bff",
	"#ff5cc6",
	"#5fe3ff",
	INK,
];

/**
 * A full six-colour palette from whatever the caller passed: a missing or
 * blank entry falls back to the default at that position, extra entries are
 * dropped. A new tuple every call; its content is what the shader keys on.
 */
export function resolveNebulaPalette(
	palette?: readonly DitherColor[] | undefined,
): NebulaPalette {
	const pick = (index: number): DitherColor => {
		const entry = palette?.[index]?.trim();
		return entry ? entry : (NEBULA_PALETTE[index] as DitherColor);
	};
	return [pick(0), pick(1), pick(2), pick(3), pick(4), pick(5)];
}
