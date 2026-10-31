import { cn } from "cn";
import type { CSSProperties, ReactNode } from "react";
import type { DitherMatrix } from "../../lib/bayer";
import {
	ditherColorVar,
	type DitherColorName as PaletteColorName,
} from "../../lib/colors";
import type { DitherFill } from "../../lib/pattern";

export type { DitherMatrix };

/**
 * Pieces every dither chart shares: colour aliases, fill variants and the
 * crisp (never dithered) text furniture: axis labels, legend, readout.
 */

/** Named colours a chart accepts, mapped to the dither tokens. Anything else is passed through as CSS. */
export const COLOR_ALIASES = {
	ink: "var(--dither-ink)",
	ok: "var(--color-status-live)",
	info: "var(--color-info)",
	warn: "var(--color-warn)",
	bad: "var(--color-bad)",
	stopped: "var(--color-status-stopped)",
	muted: "var(--color-ink-3)",
} as const;

export type DitherColorName = keyof typeof COLOR_ALIASES;
/**
 * A named colour (ours, or a dithercn palette name such as `"blue"`), or any
 * CSS colour / `var(--token)` / `--custom-property`.
 */
export type ChartColor = DitherColorName | PaletteColorName | (string & {});

/** The i-th series colour (wraps after 8). */
export const seriesColor = (index: number) =>
	`var(--dither-series-${(index % 8) + 1})`;

/**
 * Resolve a `ChartColor` (or the series default) to a CSS colour string.
 * Palette names (`blue`, `purple`, …) and `--tokens` go through
 * `ditherColorVar`.
 */
export function resolveColor(color: ChartColor | undefined, index = 0): string {
	if (color === undefined) return seriesColor(index);
	return color in COLOR_ALIASES
		? COLOR_ALIASES[color as DitherColorName]
		: ditherColorVar(color);
}

/**
 * A series' colour: its own `color`, then the chart's `colors` by position,
 * then the default series colour.
 */
export function seriesCss(
	color: ChartColor | undefined,
	colors: readonly ChartColor[] | undefined,
	index: number,
): string {
	return resolveColor(color ?? colors?.[index], index);
}

/** Texture of a dithered mark. `gradient` thins away from its dense edge. */
export type FillVariant = "gradient" | "dotted" | "hatched" | "solid";

/** Soft glow behind a fill. Its strength follows `--dither-bloom`. */
export type ChartBloom = "off" | "low" | "high";

/** Props the cartesian charts (area, bar, line) share beyond data and axes. */
export interface ChartFeatureProps {
	/** Colours by series position: names (`"green"`, `"blue"`) or any CSS colour. A series' own `color` wins. */
	colors?: readonly ChartColor[];
	/** Soft glow in each series' colour behind its fill. Default `off`. */
	bloom?: ChartBloom;
	/** Scrubbing, readout and legend spotlight. `false` makes a decorative chart: no focus stop, no readout, just an image with a summary. Default true. */
	interactive?: boolean;
	/** Entrance motion, once, the first time the chart is seen. Always off under reduced motion. Default true. */
	animate?: boolean;
	/** Threshold matrix of the dithered fills: Bayer 2/4/8 or blue noise. Default `bayer4`. */
	matrix?: DitherMatrix;
	/** What a legend click does: `spotlight` locks a series lit (hover lights it), `toggle` hides it. Default `spotlight`. */
	legendAction?: "spotlight" | "toggle";
}

/** `matrix` as a partial `DitherFill`: nothing when unset (exact optional props). */
export const matrixProp = (
	matrix: DitherMatrix | undefined,
): { matrix?: DitherMatrix } => (matrix ? { matrix } : {});

/** A fill of `tone` in `color`, with the chart's matrix when it sets one. */
export function inkFill(
	tone: number,
	color: string,
	cell: number,
	matrix?: DitherMatrix,
): DitherFill {
	return { tone, color, cell, ...matrixProp(matrix) };
}

/** How a flat (single-tone) fill variant is drawn as a `DitherFill`. */
export function flatFill(
	variant: Exclude<FillVariant, "gradient" | "solid">,
	color: string,
	cell: number,
	matrix?: DitherMatrix,
): DitherFill {
	return variant === "dotted"
		? { tone: 0.3, color, cell, shape: "dot", ...matrixProp(matrix) }
		: { tone: 0.4, color, cell, shape: "hatch", ...matrixProp(matrix) };
}

/** Bloom as a CSS opacity expression, scaled by `--dither-bloom` (0 turns it off). */
export function bloomOpacity(bloom: ChartBloom | undefined): string | null {
	if (bloom === "low") return "calc(var(--dither-bloom, 0.35) * 1.1)";
	if (bloom === "high") return "calc(var(--dither-bloom, 0.35) * 2.2)";
	return null;
}

/** Blur radius of the glow, in px, per bloom level. */
export const bloomRadius = (bloom: ChartBloom | undefined) =>
	bloom === "high" ? 9 : 5;

/** A soft track tint behind a fill: the colour at low strength over the well. */
export const trackTint = (color: string, percent = 16) =>
	`color-mix(in oklab, ${color} ${percent}%, var(--color-surface-well))`;

/** Mono 10px axis label. Text is never dithered. */
export const AXIS_TEXT = "fill-ink-4 font-mono text-[10px]";

export interface LegendItem {
	label: string;
	color: string;
	/** Optional text after the label, such as a total. */
	value?: string;
	/** Fill variant swatch: dither swatches repeat the chart's texture. */
	hatch?: boolean;
	/** Dashed series: the swatch repeats the dash so colour is never the only cue. */
	dashed?: boolean;
}

/**
 * Legend chips: a swatch in the series colour, the name in text, an optional
 * figure. Interactive when `onToggle` is passed (then the chips are buttons
 * with `aria-pressed`). With `onLock` the chips are spotlight buttons:
 * hovering or focusing one calls `onHover`, clicking it locks the
 * spotlight (`locked`), Escape lets go; chips other than `focus` dim.
 */
export function ChartLegend({
	items,
	hidden,
	onToggle,
	focus,
	locked,
	onHover,
	onLock,
	className,
}: {
	items: readonly LegendItem[];
	hidden?: ReadonlySet<string>;
	onToggle?: (label: string) => void;
	/** The spotlit label (hovered, else locked). */
	focus?: string | null;
	locked?: string | null;
	onHover?: (label: string | null) => void;
	onLock?: (label: string | null) => void;
	className?: string;
}) {
	return (
		<ul
			className={cn("flex flex-wrap items-center gap-x-3 gap-y-1.5", className)}
			aria-label="Legend"
		>
			{items.map((item) => {
				const off = hidden?.has(item.label) ?? false;
				const dim = focus != null && focus !== item.label;
				const body: ReactNode = (
					<>
						<span
							aria-hidden
							className="size-2 shrink-0 rounded-[2px]"
							style={
								item.dashed
									? {
											background: `repeating-linear-gradient(90deg, ${item.color} 0 2px, transparent 2px 4px)`,
											boxShadow: `inset 0 0 0 1px ${item.color}`,
										}
									: {
											background: off ? "transparent" : item.color,
											boxShadow: `inset 0 0 0 1px ${item.color}`,
										}
							}
						/>
						<span className="font-medium">{item.label}</span>
						{item.value ? (
							<span className="text-ink-3 tabular-nums">{item.value}</span>
						) : null}
					</>
				);
				const cls =
					"flex items-center gap-1.5 font-mono text-2xs leading-none text-ink-2 transition-opacity duration-150";
				const button =
					"cursor-pointer rounded-ctl outline-none focus-visible:focus-ring";
				return (
					<li key={item.label}>
						{onLock ? (
							<button
								type="button"
								aria-pressed={locked === item.label}
								aria-label={`Spotlight ${item.label}`}
								onPointerEnter={() => onHover?.(item.label)}
								onPointerLeave={() => onHover?.(null)}
								onFocus={() => onHover?.(item.label)}
								onBlur={() => onHover?.(null)}
								onClick={() =>
									onLock(locked === item.label ? null : item.label)
								}
								onKeyDown={(e) => {
									if (e.key === "Escape") onLock(null);
								}}
								className={cn(
									cls,
									button,
									dim && "opacity-45",
									locked === item.label && "text-ink-1",
								)}
							>
								{body}
							</button>
						) : onToggle ? (
							<button
								type="button"
								aria-pressed={!off}
								onClick={() => onToggle(item.label)}
								className={cn(cls, button, off && "opacity-55")}
							>
								{body}
							</button>
						) : (
							<span className={cn(cls, dim && "opacity-45")}>{body}</span>
						)}
					</li>
				);
			})}
		</ul>
	);
}

/**
 * The scrub readout: a pop-surface card with the x label and one row per
 * series. Positioned by the chart (absolute), never dithered.
 */
export function ChartReadout({
	title,
	rows,
	style,
	className,
}: {
	title: string;
	rows: readonly { label: string; color: string; value: string }[];
	style?: CSSProperties;
	className?: string;
}) {
	return (
		<div
			role="status"
			style={style}
			className={cn(
				"surface-pop pointer-events-none absolute z-10 flex min-w-28 flex-col gap-1 rounded-ctl px-2.5 py-2 font-mono text-2xs leading-none",
				className,
			)}
		>
			<span className="text-ink-3">{title}</span>
			{rows.map((row) => (
				<span key={row.label} className="flex items-center gap-1.5">
					<span
						aria-hidden
						className="size-2 shrink-0 rounded-[2px]"
						style={{ background: row.color }}
					/>
					<span className="min-w-0 flex-1 truncate text-ink-2">
						{row.label}
					</span>
					<span className="text-ink-1 tabular-nums">{row.value}</span>
				</span>
			))}
		</div>
	);
}

/** Read a datum's x or y through a key or an accessor. */
export type Accessor<T, V> = keyof T | ((datum: T) => V);

export function read<T, V>(datum: T, accessor: Accessor<T, V>): V {
	return typeof accessor === "function"
		? (accessor as (d: T) => V)(datum)
		: (datum[accessor as keyof T] as unknown as V);
}

/** A chart value series: a label and where to read its number. */
export interface ChartSeries<T> {
	label: string;
	value: Accessor<T, number>;
	color?: ChartColor;
}

/** An `Intl` options object or a function; the default is compact (1.2K). */
export type ValueFormat =
	| Intl.NumberFormatOptions
	| ((value: number) => string);

export function makeFormat(
	format: ValueFormat | undefined,
	fallback: (value: number) => string,
): (value: number) => string {
	if (!format) return fallback;
	if (typeof format === "function") return format;
	const nf = new Intl.NumberFormat("en", format);
	return (value) => nf.format(value);
}
