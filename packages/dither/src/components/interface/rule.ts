import type { DitherMatrix } from "../../lib/bayer";
import { rampStops } from "./bands";
import { clamp01, type DitherFillVariant } from "./fill";

/**
 * What a divider's rule is made of: the density of each band along its
 * length, and the cell and thickness it is drawn with. Pure, so the fade can
 * be tested without a DOM.
 */

export type RuleVariant = "fade" | "solid" | "dots";

/** Which end of a segment fades: `both` for a lone rule, `start`/`end` for the halves around a label (they fade away from it). */
export type RuleSide = "both" | "start" | "end";

interface VariantDefaults {
	cell: number;
	/** Thickness in px. */
	thickness: number;
}

const VARIANT_DEFAULTS: Record<RuleVariant, VariantDefaults> = {
	fade: { cell: 2, thickness: 4 },
	solid: { cell: 2, thickness: 4 },
	dots: { cell: 3, thickness: 3 },
};

/** Bands in one half of a rule: "ten steps of dither". */
const FADE_STEPS = 10;

export interface RuleStopsOptions {
	/** Share of each half that fades out, 0..1. */
	fade: number;
	/** Densest tone, in the middle (or at the solid end). */
	strength: number;
	side?: RuleSide;
	/** Bands per half. Default 10. */
	steps?: number;
}

/**
 * One tone per band along a rule, rounded to three places. A lone rule
 * (`both`) is dense in the middle and fades towards each tip over `fade` of
 * each half. A half beside a label fades only at its outer end.
 */
export function ruleStops({
	fade,
	strength,
	side = "both",
	steps = FADE_STEPS,
}: RuleStopsOptions): number[] {
	const top = clamp01(strength);
	const share = clamp01(fade);
	const bands = side === "both" ? steps * 2 : steps;
	return Array.from({ length: bands }, (_, i) => {
		const t = (i + 0.5) / bands;
		// Distance from the fading tip, 0 at the tip, and the span it fades over.
		const [distance, span] =
			side === "both"
				? [Math.min(t, 1 - t), share / 2]
				: side === "start"
					? [t, share]
					: [1 - t, share];
		const ramp = span <= 0 ? 1 : clamp01(distance / span);
		return Math.round(top * ramp * 1000) / 1000;
	});
}

/** The stops of the default `fade` variant: dense in the middle, dissolving at both ends. */
function defaultFade(side: RuleSide): number[] {
	const rise = rampStops(0, 0.7, 6);
	const fall = rampStops(0.7, 0, 6);
	if (side === "start")
		return [...rise, ...Array.from({ length: 6 }, () => 0.7)];
	if (side === "end") return [...Array.from({ length: 6 }, () => 0.7), ...fall];
	return [...rise, ...fall.slice(1)];
}

export interface RuleOptions {
	variant: RuleVariant;
	side: RuleSide;
	fade?: number | undefined;
	strength?: number | undefined;
	/** Thickness in cells. */
	thickness?: number | undefined;
	cell?: number | undefined;
	fill?: DitherFillVariant | undefined;
	matrix?: DitherMatrix | undefined;
}

export interface RuleSpec {
	stops: number[];
	cell: number;
	/** Thickness in px. */
	thickness: number;
	texture: DitherFillVariant | undefined;
	matrix: DitherMatrix | undefined;
}

/**
 * The rule for a divider's props. With none of `fade`, `strength` or `fill`
 * the variant's own stops are used (`fade`, `solid`, `dots`); with any of them
 * the rule is the ten-step fade described by those numbers.
 */
export function resolveRule(options: RuleOptions): RuleSpec {
	const { variant, side } = options;
	const base = VARIANT_DEFAULTS[variant];
	const custom =
		options.fade !== undefined ||
		options.strength !== undefined ||
		options.fill !== undefined;
	const cell = Math.max(1, options.cell ?? base.cell);
	let stops: number[];
	if (custom) {
		stops = ruleStops({
			fade: options.fade ?? (variant === "solid" ? 0 : 0.6),
			strength: options.strength ?? 0.6,
			side,
		});
	} else if (variant === "fade") {
		stops = defaultFade(side);
	} else {
		stops = [variant === "solid" ? 0.75 : 0.3];
	}
	return {
		stops,
		cell,
		thickness:
			options.thickness !== undefined
				? Math.max(1, options.thickness) * cell
				: base.thickness,
		texture: options.fill,
		matrix: options.matrix,
	};
}
