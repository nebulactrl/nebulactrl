import { cn } from "cn";
import {
	type CSSProperties,
	type ReactNode,
	useEffect,
	useMemo,
	useState,
} from "react";
import { ditherMaskStyle } from "../../lib/pattern";
import {
	MIN_DITHER_CELL,
	useForcedColors,
	useReducedMotionPref,
} from "../../lib/prefs";
import {
	bandLayers,
	barState,
	cometLayers,
	gradientLayers,
	partWidths,
	percentOf,
	segmentFills,
	segmentPartSpans,
} from "../../lib/progress-math";
import {
	bloomOpacity,
	bloomRadius,
	type ChartBloom,
	type ChartColor,
	ChartLegend,
	type DitherMatrix,
	type FillVariant,
	matrixProp,
	resolveColor,
	trackTint,
} from "./shared";

export interface ProgressPart {
	label: string;
	value: number;
	color?: ChartColor;
	/** Legend figure. Default the raw value. */
	text?: string;
	/** This part's texture. It wins over the bar's `fill`. */
	fill?: FillVariant;
}

export interface DitherProgressBarProps {
	/** What is in progress. Shown above the bar and used as its accessible name. */
	label: string;
	/** 0…`max`. Leave out (with no `parts`) for an indeterminate comet. */
	value?: number | null;
	/** Stacked parts, left to right, instead of `value`. */
	parts?: readonly ProgressPart[];
	/** The value that means full, or the capacity for parts. Default 100. */
	max?: number;
	/** With `parts`: shown after each value in the legend, such as " GB". */
	unit?: string;
	/** Text for the readout and screen readers. Default the percentage. */
	valueText?: string;
	/** Hide the label row visually (screen readers still get the name and value). */
	hideLabel?: boolean;
	/** With `parts`: leave the legend out, for a host that draws its own. */
	hideLegend?: boolean;
	size?: "sm" | "md" | "lg";
	/** Texture: `gradient` thins out behind the solid leading edge. */
	fill?: FillVariant;
	color?: ChartColor;
	/** Percent at and above which the bar turns amber, and a state word joins the readout. */
	warnAt?: number;
	/** Percent at and above which the bar turns red. */
	badAt?: number;
	/** Cut the bar into this many step blocks. With `parts` too, each block is painted by the part that covers it (a part's `value` counts out of `max`: 2 of 4 paints two of four blocks). */
	segments?: number;
	/** Ticks over the track: a threshold, a committed-limits marker. */
	markers?: readonly { percent: number; title: string; warn?: boolean }[];
	/** Dim the bar (a stale reading). */
	stale?: boolean;
	/** Render spans instead of divs, for a bar inside a button or a paragraph. */
	inline?: boolean;
	/** Dither cell in px. Default 2 (3 for `lg`). */
	cell?: number;
	/** Soft glow in the fill's colour behind it. Default `off`. */
	bloom?: ChartBloom;
	/**
	 * Fill in once when the bar first mounts (it grows from empty to its value)
	 * and tween between values after that. Timing comes from
	 * `--dither-fill-duration` and `--dither-fill-ease`. Off under reduced
	 * motion. With `false` the bar is at its value on first paint (a statically
	 * rendered page), later changes still tween, and a host may reveal it by
	 * driving `--dither-grow` (0 to 1) and `--dither-fill-delay` on the bar.
	 * Default true.
	 */
	animate?: boolean;
	/** Threshold matrix of the dither: Bayer 2/4/8 or blue noise. Default `bayer4`. */
	matrix?: DitherMatrix;
	/**
	 * What the bar shows while work is in flight (a single `value` bar).
	 * `band` sends a denser stretch of dither travelling through the fill,
	 * left to right, clipped to the filled width; it is one CSS keyframe and is
	 * off under reduced motion. Default `none`, so a calm bar stays calm.
	 */
	activity?: "none" | "band";
	/**
	 * Opt in to a resolved state for a single `value` bar: when true the fill
	 * resolves to full density in the bar's colour over 300ms (instantly under
	 * reduced motion) and the band stops. Leave it undefined for the ordinary
	 * bar; pass `false` while the work runs so the change can fade in.
	 */
	complete?: boolean;
	className?: string;
}

/** Step blocks fill left to right: each one starts a little after the one before. */
const blockDelay = (index: number, count: number) =>
	`calc(var(--dither-fill-delay, 0ms) + ${index * Math.min(24, Math.round(360 / Math.max(1, count)))}ms)`;

const HEIGHT = { sm: 8, md: 10, lg: 14 } as const;

/**
 * How far a fill has grown, 0 to 1, as a CSS variable on the bar. Every fill
 * edge is `percent * --dither-grow`, so one number reveals the whole bar: the
 * component drives it on mount (`animate`), and a host may drive it from the
 * outside (the marketing site holds it at 0 until a bar scrolls into view).
 */
const GROW = "var(--dither-grow, 1)";
const grown = (percent: number) => `calc(${percent}% * ${GROW})`;

/**
 * Every fill edge carries `data-dither-edge`; its timing lives once in the
 * tokens (`--dither-fill-duration`, 350ms in the app and stretched by the site
 * for its reveal; `--dither-fill-ease`; a `--dither-fill-delay` a host can
 * stagger per bar). The dither is anchored to the leading edge, so the dense
 * head and its thinning tail travel with the edge as it moves.
 */

function layerStyle(
	color: string,
	tone: number,
	cell: number,
	shape: "square" | "dot" | "hatch",
	width: number | string | null,
	matrix?: DitherMatrix,
): CSSProperties {
	return {
		position: "absolute",
		top: 0,
		right: 0,
		bottom: 0,
		...(width === null ? { left: 0 } : { width }),
		backgroundColor: color,
		...ditherMaskStyle({ tone, cell, shape, ...matrixProp(matrix) }),
		// Every layer anchors its tile at the leading edge, so the denser tones
		// nest inside the sparser ones instead of shifting phase.
		WebkitMaskPosition: "right top",
		maskPosition: "right top",
	};
}

/** The 2px solid cap on a fill's leading edge: the exact value stays readable. */
function Cap({ color, cell }: { color: string; cell: number }) {
	return (
		<span
			aria-hidden
			data-dither-fill
			className="absolute inset-y-0 right-0 rounded-full"
			style={{
				width: Math.max(2, cell),
				backgroundColor: color,
				boxShadow: `0 0 8px 0 color-mix(in oklab, ${color} 55%, transparent)`,
			}}
		/>
	);
}

/** The dithered body of a fill, anchored at its leading (right) edge. */
function FillBody({
	color,
	variant,
	cell,
	solid,
	scale,
	matrix,
}: {
	color: string;
	variant: FillVariant;
	cell: number;
	solid: boolean;
	scale: number;
	matrix?: DitherMatrix | undefined;
}) {
	const layers = useMemo<CSSProperties[]>(() => {
		if (solid || variant === "solid") return [];
		if (variant === "gradient") {
			return gradientLayers(scale).map((l) =>
				layerStyle(color, l.tone, cell, "square", l.reach, matrix),
			);
		}
		return [
			layerStyle(
				color,
				variant === "dotted" ? 0.34 : 0.4,
				cell,
				variant === "dotted" ? "dot" : "hatch",
				null,
				matrix,
			),
		];
	}, [color, variant, cell, solid, scale, matrix]);
	if (layers.length === 0) {
		return (
			<span
				aria-hidden
				data-dither-fill
				className="absolute inset-0"
				style={{ backgroundColor: color }}
			/>
		);
	}
	return (
		<>
			{layers.map((style, i) => (
				<span
					// biome-ignore lint/suspicious/noArrayIndexKey: static, ordered layers
					key={i}
					aria-hidden
					data-dither-fill
					style={style}
				/>
			))}
		</>
	);
}

/** The sweeping comet of an indeterminate bar: dense head, dithered tail. */
function Comet({
	color,
	cell,
	solid,
	matrix,
}: {
	color: string;
	cell: number;
	solid: boolean;
	matrix?: DitherMatrix | undefined;
}) {
	const layers = useMemo(
		() =>
			cometLayers().map((l) => ({
				share: l.share,
				style: layerStyle(
					color,
					l.tone,
					cell,
					"square",
					`${l.share * 100}%`,
					matrix,
				),
			})),
		[color, cell, matrix],
	);
	return (
		<span
			aria-hidden
			data-dither-comet
			className="absolute inset-y-0 left-0 w-1/4"
			style={{
				animation: "nc-dither-comet 1.5s cubic-bezier(0.4, 0, 0.6, 1) infinite",
			}}
		>
			{solid ? (
				<span
					data-dither-fill
					className="absolute inset-0"
					style={{ backgroundColor: color, opacity: 0.6 }}
				/>
			) : (
				layers.map((l) => (
					<span key={l.share} data-dither-fill style={l.style} />
				))
			)}
			<Cap color={color} cell={cell} />
		</span>
	);
}

/**
 * The working band of a running bar: a denser stretch of dither that travels
 * left to right inside the fill, clipped to the filled width. Dense head,
 * thinning tail; one transform keyframe.
 */
function Band({
	color,
	cell,
	matrix,
}: {
	color: string;
	cell: number;
	matrix?: DitherMatrix | undefined;
}) {
	const layers = useMemo(
		() =>
			bandLayers().map((l) => ({
				share: l.share,
				style: layerStyle(
					color,
					l.tone,
					cell,
					"square",
					`${l.share * 100}%`,
					matrix,
				),
			})),
		[color, cell, matrix],
	);
	return (
		<span aria-hidden className="absolute inset-0 overflow-hidden">
			<span
				data-dither-band
				className="absolute inset-y-0 left-0 w-[30%]"
				style={{ animation: "nc-dither-band 1.4s linear infinite" }}
			>
				{layers.map((l) => (
					<span key={l.share} data-dither-fill style={l.style} />
				))}
			</span>
		</span>
	);
}

const STATE_WORD = { normal: "", warn: "high", bad: "critical" } as const;
const STATE_COLOR = {
	normal: null,
	warn: "var(--color-warn)",
	bad: "var(--color-bad)",
} as const;

/**
 * A progress or capacity bar built from dither cells: a texture with a solid
 * leading edge, stacked parts, step segments, thresholds and an indeterminate
 * comet. HTML and CSS masks only: no SVG, no canvas, no animation loop (the
 * comet and the working band are each one transform keyframe, off under
 * reduced motion).
 */
export function DitherProgressBar({
	label,
	value,
	parts,
	max = 100,
	valueText,
	hideLabel = false,
	hideLegend = false,
	size = "md",
	fill = "gradient",
	color,
	warnAt,
	badAt,
	segments,
	markers,
	stale = false,
	inline = false,
	cell,
	unit = "",
	bloom = "off",
	animate = true,
	matrix,
	className,
	activity = "none",
	complete,
}: DitherProgressBarProps) {
	const Tag = inline ? "span" : "div";
	const forced = useForcedColors();
	const reduced = useReducedMotionPref();
	// The first reveal: a bar mounts empty and grows to its value once. Later
	// value changes tween on their own (the widths transition), a refetch that
	// leaves the value alone moves nothing, and reduced motion paints the final
	// state at once.
	const [entered, setEntered] = useState(!animate || reduced);
	useEffect(() => {
		if (!animate || reduced) {
			setEntered(true);
			return;
		}
		// Two frames: the empty state must be painted before it can transition.
		let second = 0;
		const first = requestAnimationFrame(() => {
			second = requestAnimationFrame(() => setEntered(true));
		});
		return () => {
			cancelAnimationFrame(first);
			cancelAnimationFrame(second);
		};
	}, [animate, reduced]);
	// With `animate` the component owns `--dither-grow` (0 until the reveal). With
	// `animate={false}` it leaves the variable alone, so a host can drive it.
	const growVar = animate
		? ({ "--dither-grow": entered ? 1 : 0 } as CSSProperties)
		: undefined;
	const px = HEIGHT[size];
	const dot = cell ?? (size === "lg" ? 3 : 2);
	const solid = forced || dot < MIN_DITHER_CELL;
	const indeterminate =
		parts === undefined && (value === undefined || value === null);
	const total = parts
		? parts.reduce((sum, p) => sum + Math.max(0, p.value), 0)
		: (value ?? 0);
	const percent = indeterminate ? null : percentOf(total, max);
	const state = barState(percent, warnAt, badAt);
	const base = resolveColor(color, 0);
	const tone = STATE_COLOR[state] ?? base;
	const text =
		valueText ?? (percent === null ? "Working" : `${Math.round(percent)}%`);
	const spoken = STATE_WORD[state] ? `${text}, ${STATE_WORD[state]}` : text;
	const scale = px / 10;

	const widths = useMemo(
		() => (parts ? partWidths(parts, max) : []),
		[parts, max],
	);
	const blocks = useMemo(
		() => (segments ? segmentFills(total, max, segments) : []),
		[segments, total, max],
	);
	// With `parts` and `segments`, each block is painted by the parts that cover it.
	const partBlocks = useMemo(
		() =>
			segments && parts
				? segmentPartSpans(
						parts.map((p) => p.value),
						max,
						segments,
					)
				: null,
		[segments, parts, max],
	);

	let body: ReactNode;
	// Where the glow sits: under each part, or under the whole fill.
	const glowSpans: { left: number; width: number; color: string }[] = [];
	if (indeterminate) {
		body = <Comet color={tone} cell={dot} solid={solid} matrix={matrix} />;
	} else if (parts) {
		let left = 0;
		body = parts.map((part, i) => {
			const w = widths[i] ?? 0;
			const start = left;
			left += w;
			const partColor = resolveColor(part.color, i);
			glowSpans.push({ left: start, width: w, color: partColor });
			return (
				<span
					data-dither-edge
					key={part.label}
					className="absolute inset-y-0 transition-[left,width,visibility] motion-reduce:transition-none"
					style={{
						left: grown(start),
						width: grown(w),
						// A part with no share stays mounted and hidden, so it can grow in
						// when its data arrives instead of appearing at full width.
						...(w > 0 ? {} : { visibility: "hidden" }),
					}}
				>
					<FillBody
						color={partColor}
						variant={part.fill ?? (fill === "gradient" ? "dotted" : fill)}
						cell={dot}
						solid={solid}
						scale={scale}
						matrix={matrix}
					/>
					<Cap color={partColor} cell={dot} />
				</span>
			);
		});
	} else if (segments) {
		body = null;
		glowSpans.push({ left: 0, width: percent ?? 0, color: tone });
	} else {
		glowSpans.push({ left: 0, width: percent ?? 0, color: tone });
		body = (
			<span
				data-dither-edge
				className="absolute inset-y-0 left-0 transition-[width,background-color] motion-reduce:transition-none"
				style={{ width: grown(percent ?? 0) }}
			>
				{(percent ?? 0) > 0 ? (
					<>
						<FillBody
							color={tone}
							variant={fill}
							cell={dot}
							solid={solid}
							scale={scale}
							matrix={matrix}
						/>
						{activity === "band" && !complete && !solid ? (
							<Band color={tone} cell={dot} matrix={matrix} />
						) : null}
						{complete !== undefined ? (
							<span
								aria-hidden
								data-dither-resolve
								data-dither-complete={complete ? "" : undefined}
								className="absolute inset-0"
								style={{
									backgroundColor: tone,
									opacity: complete ? 1 : 0,
									boxShadow:
										"inset 0 1px 0 0 color-mix(in oklab, white 28%, transparent)",
									transition: "opacity 300ms cubic-bezier(0.23, 1, 0.32, 1)",
								}}
							/>
						) : null}
						<Cap color={tone} cell={dot} />
					</>
				) : null}
			</span>
		);
	}
	const glowOpacity = bloomOpacity(bloom);

	return (
		<Tag
			className={cn(
				"flex min-w-0 flex-col gap-1.5",
				stale && "opacity-50",
				className,
			)}
		>
			{hideLabel ? null : (
				<Tag className="flex items-baseline justify-between gap-2 text-xs leading-none">
					<span className="type-label">{label}</span>
					<span
						className={cn(
							"font-mono tabular-nums",
							state === "normal" ? "text-ink-1" : "text-warning-text",
						)}
					>
						{text}
						{STATE_WORD[state] ? (
							<span className="ml-1.5 text-ink-3">{STATE_WORD[state]}</span>
						) : null}
					</span>
				</Tag>
			)}
			<Tag
				role="progressbar"
				aria-label={label}
				aria-valuemin={0}
				aria-valuemax={max}
				{...(percent === null ? {} : { "aria-valuenow": Math.min(total, max) })}
				aria-valuetext={spoken}
				className="relative block"
				style={{ height: px, ...growVar }}
			>
				{glowOpacity
					? glowSpans.map((g, i) => (
							<span
								data-dither-edge
								// biome-ignore lint/suspicious/noArrayIndexKey: one glow per part, in order
								key={i}
								aria-hidden
								className="pointer-events-none absolute inset-y-0 rounded-full transition-[left,width] forced-colors:hidden motion-reduce:transition-none"
								style={{
									left: grown(g.left),
									width: grown(g.width),
									background: g.color,
									filter: `blur(${bloomRadius(bloom) * 0.6}px)`,
									opacity: glowOpacity,
								}}
							/>
						))
					: null}
				{segments ? (
					<Tag className="absolute inset-0 flex" style={{ gap: dot }}>
						{blocks.map((fraction, i) => (
							<span
								// biome-ignore lint/suspicious/noArrayIndexKey: fixed-length block row
								key={i}
								data-dither-track
								className="relative flex-1 overflow-hidden rounded-[3px]"
								style={{ background: "var(--dither-track)" }}
							>
								{partBlocks ? (
									(partBlocks[i] ?? []).map((span) => {
										const part = parts?.[span.part];
										if (!part) return null;
										const partColor = resolveColor(part.color, span.part);
										return (
											<span
												data-dither-edge
												key={span.part}
												className="absolute inset-y-0 transition-[width] motion-reduce:transition-none"
												style={{
													left: `${span.left * 100}%`,
													width: grown(span.width * 100),
													transitionDelay: blockDelay(i, blocks.length),
												}}
											>
												<FillBody
													color={partColor}
													variant={
														part.fill ?? (fill === "gradient" ? "dotted" : fill)
													}
													cell={dot}
													solid={solid}
													scale={scale}
													matrix={matrix}
												/>
												<Cap color={partColor} cell={dot} />
											</span>
										);
									})
								) : (
									<span
										data-dither-edge
										className="absolute inset-y-0 left-0 transition-[width,visibility] motion-reduce:transition-none"
										style={{
											width: grown(fraction * 100),
											transitionDelay: blockDelay(i, blocks.length),
											...(fraction > 0 ? {} : { visibility: "hidden" }),
										}}
									>
										<FillBody
											color={tone}
											variant={fill === "gradient" ? "dotted" : fill}
											cell={dot}
											solid={solid}
											scale={scale}
											matrix={matrix}
										/>
										<Cap color={tone} cell={dot} />
									</span>
								)}
							</span>
						))}
					</Tag>
				) : (
					<Tag
						data-dither-track
						className="track-well absolute inset-0 overflow-hidden rounded-full"
						style={{ background: trackTint(tone, 10) }}
					>
						{body}
					</Tag>
				)}
				{markers?.map((marker) => (
					<span
						key={`${marker.percent}-${marker.title}`}
						title={marker.title}
						aria-hidden
						className={cn(
							"absolute -top-[3px] -bottom-[3px] w-0.5 rounded-full",
							marker.warn ? "bg-warn" : "bg-ink-1/80",
						)}
						style={{
							left: `calc(${Math.max(0, Math.min(100, marker.percent))}% - 1px)`,
						}}
					/>
				))}
			</Tag>
			{parts && !hideLegend ? (
				<ChartLegend
					items={parts.map((p, i) => ({
						label: p.label,
						color: resolveColor(p.color, i),
						value: p.text ?? `${p.value}${unit}`,
					}))}
				/>
			) : null}
		</Tag>
	);
}
