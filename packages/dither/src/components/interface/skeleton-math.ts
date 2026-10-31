import { rampStops } from "./bands";
import type { DitherFillVariant } from "./fill";

/** The three nested Bayer levels a pulse steps through (and back). */
export const PULSE_LEVELS = [0.22, 0.4, 0.6] as const;

/** The band that travels in a sweep: density peaks in the middle and falls away both sides. */
export const SWEEP_STOPS = [
	...rampStops(0.05, 0.7, 4),
	...rampStops(0.7, 0.05, 4).slice(1),
];

export interface RestMask {
	axis: "x" | "y";
	stops: number[];
	texture?: DitherFillVariant;
}

/**
 * The resting layer of a skeleton. Without a `fill` it is the house grain, a
 * flat tone over the whole well. `gradient` thins out from the left edge;
 * `dotted` and `hatched` keep their texture; `solid` is a plain tint.
 */
export function restMask(fill: DitherFillVariant | undefined): RestMask {
	switch (fill) {
		case undefined:
			return { axis: "y", stops: [0.2] };
		case "gradient":
			return { axis: "x", stops: rampStops(0.5, 0.05, 8), texture: "gradient" };
		case "dotted":
			return { axis: "y", stops: [0.4], texture: "dotted" };
		case "hatched":
			return { axis: "y", stops: [0.4], texture: "hatched" };
		case "solid":
			return { axis: "y", stops: [0.14], texture: "solid" };
	}
}

/** Widths of the lines of a text skeleton: full width, the last one shorter. */
export function lineWidths(lines: number): string[] {
	const count = Math.max(1, Math.floor(lines));
	return Array.from({ length: count }, (_, i) =>
		count > 1 && i === count - 1 ? "60%" : "100%",
	);
}
