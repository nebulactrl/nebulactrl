import { MAX_PALETTE } from "../../lib/gl/palette";

/**
 * Markers and arcs on the globe and dot map may set their own colour. The
 * shader draws one palette ramp, so each distinct colour is appended to it and
 * an item points at its entry by ramp position (what GLSL `paletteColor`
 * takes): `[land colour, accent, ...custom]` spread evenly over 0..1.
 */
export interface InkPlan {
	/** `[background, color, accent, ...custom colours]`, at most the engine's ramp length. */
	palette: string[];
	/** Ramp position of each item's ink, 0..1, in input order. Uncoloured items use the accent. */
	positions: number[];
	/** True when any item sets its own colour, so the shader must pick a colour per cell. */
	coloured: boolean;
}

/**
 * Builds the ramp for a scene. `itemColors` holds one entry per marker and arc
 * in any order; `undefined` means "the accent". Colours equal to `color` or
 * `accent` reuse those entries, and items past the ramp's capacity fall back
 * to the accent.
 */
export function planInks(
	background: string,
	color: string,
	accent: string,
	itemColors: readonly (string | undefined)[],
): InkPlan {
	const inks = [color, accent];
	const slots = itemColors.map((item) => {
		if (item === undefined) return 1;
		const existing = inks.indexOf(item);
		if (existing >= 0) return existing;
		// One palette slot belongs to the background.
		if (inks.length >= MAX_PALETTE - 1) return 1;
		inks.push(item);
		return inks.length - 1;
	});
	const last = inks.length - 1;
	return {
		palette: [background, ...inks],
		positions: slots.map((slot) => slot / last),
		coloured: itemColors.some((item) => item !== undefined),
	};
}
