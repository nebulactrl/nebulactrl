import type { DitherFillVariant } from "./fill";

/**
 * The geometry of a card glow: a stack of nested shapes around an origin on
 * the card's edge, each filled with a denser dither than the one below it. A
 * dither tone is a superset of every sparser tone, so stacking the shapes
 * needs no cut-outs: where they overlap the densest tile wins.
 */

export type DitherCardGlow =
	| "top-left"
	| "top-right"
	| "bottom-left"
	| "bottom-right"
	| "top"
	| "bottom";

export interface GlowOrigin {
	/** SVG `cx`: a length or a percentage of the card's width. */
	cx: string;
	/** SVG `cy`. */
	cy: string;
	/** `top` and `bottom` are wide ellipses along an edge; corners are circles. */
	wide: boolean;
}

export function glowOrigin(glow: DitherCardGlow): GlowOrigin {
	switch (glow) {
		case "top-left":
			return { cx: "0", cy: "0", wide: false };
		case "top-right":
			return { cx: "100%", cy: "0", wide: false };
		case "bottom-left":
			return { cx: "0", cy: "100%", wide: false };
		case "bottom-right":
			return { cx: "100%", cy: "100%", wide: false };
		case "top":
			return { cx: "50%", cy: "0", wide: true };
		case "bottom":
			return { cx: "50%", cy: "100%", wide: true };
	}
}

/** How much wider than tall the glow along an edge is. */
export const EDGE_STRETCH = 2.2;

export interface GlowRing {
	/** Vertical radius in px (the radius, for a corner glow). */
	r: number;
	/** Dither tone of the ring, 0..1. */
	tone: number;
	/** Opacity of the ring: 1 for gradients (the tone carries the fade), lower for textures (the overlap does). */
	opacity: number;
}

export interface GlowRingOptions {
	/** How far the outermost ring reaches, px. */
	reach: number;
	/** Tone of the innermost ring, 0..1. */
	strength: number;
	fill: DitherFillVariant;
	/** How many rings. Default 6. */
	rings?: number;
}

/** Share of `strength` that one textured ring contributes: overlaps stack up to roughly `strength × 0.35 × rings`. */
const TEXTURE_RING_OPACITY = 0.35;

/**
 * The rings of a glow, outermost first. A gradient steps the tone from
 * `strength / rings` at the outer edge to `strength` at the origin. Dotted,
 * hatched and solid fills keep one texture on every ring and fade it by
 * overlap, so the lines build up towards the edge.
 */
export function glowRings({
	reach,
	strength,
	fill,
	rings = 6,
}: GlowRingOptions): GlowRing[] {
	const count = Math.max(1, Math.floor(rings));
	const top = Math.min(1, Math.max(0, strength));
	return Array.from({ length: count }, (_, i) => {
		const r = (reach * (count - i)) / count;
		if (fill === "gradient") {
			return { r, tone: (top * (i + 1)) / count, opacity: 1 };
		}
		return {
			r,
			tone: fill === "solid" ? 1 : 0.9,
			opacity: Math.min(1, top * TEXTURE_RING_OPACITY),
		};
	});
}

/** The rim's rings: a wider, denser stack, so the border catches more light than the surface. */
export function rimRings(options: GlowRingOptions): GlowRing[] {
	return glowRings({
		...options,
		reach: options.reach * 1.6,
		strength: Math.min(1, options.strength * 1.6),
	});
}

/** The hover stack: a larger one that switches on ring by ring. */
export function hoverRings(options: GlowRingOptions): GlowRing[] {
	return glowRings({ ...options, reach: options.reach * 1.5 });
}
