import type { CSSProperties, ReactNode } from "react";
import type { BayerSize, DitherMatrix } from "../../lib/bayer";
import type { ClearBox } from "../../lib/gl/clear-box";
import { ditherColorVar } from "../../lib/tokens";
import { resolveMatrix } from "./matrix";

/** Theme tokens every background reads by default. */
export const PAPER = "--dither-paper";
export const INK = "--dither-ink";

/** A colour from the palette ramp: a name (`"blue"`), a `--token` or any CSS colour. */
export type DitherColor = string;

/** Props every part-2 background shares, forwarded to `DitherShader`. */
export interface BackgroundBaseProps {
	/** Colours from paper (tone 0) to ink (tone 1). Names, token names, `var()` or any CSS colour. */
	palette?: DitherColor[] | undefined;
	/** Threshold pattern. Wins over `n`. */
	matrix?: DitherMatrix | undefined;
	/** Bayer matrix size. `matrix` is the same choice with blue noise added. */
	n?: BayerSize | undefined;
	/** Tone levels before dithering: a number, `"full"` (every tone the matrix draws) or `"stops"` (fits the palette). */
	levels?: number | "full" | "stops" | undefined;
	/** Size of one dither cell in CSS pixels. Defaults to `--dither-cell`. */
	cell?: number | undefined;
	/** Cell shape. */
	shape?: "square" | "dot" | undefined;
	/** Frames per second, capped at 30. */
	fps?: number | undefined;
	/** `false` draws one still frame. */
	animate?: boolean | undefined;
	/** The moment, in seconds, drawn when motion is off or reduced. */
	stillTime?: number | undefined;
	/** How fast it moves. 1 is the designed pace, 0 holds the scene still. */
	speed?: number | undefined;
	className?: string | undefined;
	style?: CSSProperties | undefined;
	/** Shown when WebGL2 is unavailable. A palette gradient by default. */
	fallback?: ReactNode;
	/**
	 * Boxes (CSS px from the top-left of this background) where the field stays
	 * clear, usually from `useClearBox`. Only backgrounds that name it use it:
	 * `DitherNebula` clears its gas, not its stars. Default: none.
	 */
	clear?: readonly ClearBox[] | undefined;
}

/** What a background starts from when the caller leaves a shared prop out. */
export interface BaseDefaults {
	/** Moment drawn when motion is off or reduced. */
	staticTime: number;
	/** Threshold pattern when neither `matrix` nor `n` is given. Default "bayer4". */
	matrix?: DitherMatrix | undefined;
	/** Tone levels when `levels` is not given; left out, the engine's own. */
	levels?: number | "full" | undefined;
}

/** The `DitherShader` props common to every background. */
export function shaderBase(
	{
		matrix,
		n,
		levels,
		cell,
		shape,
		fps,
		animate,
		stillTime,
		speed,
		className,
		style,
		fallback,
	}: BackgroundBaseProps,
	defaults: BaseDefaults,
) {
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime },
		{
			matrix: defaults.matrix ?? "bayer4",
			staticTime: defaults.staticTime,
			levels: defaults.levels,
		},
	);
	return {
		...tune,
		cell,
		shape,
		fps,
		animate,
		speed,
		className,
		style,
		fallback,
		"aria-hidden": true as const,
	};
}

/** Names and `--token` names become `var(...)`; anything else is already CSS. */
export function cssColor(color: string): string {
	return ditherColorVar(color);
}

/** Tint `ink` towards `paper` by `percent` of ink, for the default ramps. */
export function inkMix(percent: number): string {
	return `color-mix(in oklch, var(${INK}) ${percent}%, var(${PAPER}))`;
}

export const clamp = (value: number, min: number, max: number) =>
	Math.min(max, Math.max(min, value));
