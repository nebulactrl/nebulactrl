import { cn } from "cn";
import { type KeyboardEvent, useMemo, useRef, useState } from "react";
import { arcPath, clamp, compact, polar } from "../../lib/chart-math";
import { type DitherFill, useDitherFillUrls } from "../../lib/pattern";
import {
	MIN_DITHER_CELL,
	useElementWidth,
	useForcedColors,
	useReducedMotionPref,
} from "../../lib/prefs";
import { sliceAngles, stepSlice } from "../../lib/radial-math";
import { Bloom, ChartLive, Sweep, useChartUid, useEntrance } from "./chart-kit";
import {
	type ChartBloom,
	type ChartColor,
	ChartReadout,
	type DitherMatrix,
	type FillVariant,
	flatFill,
	inkFill,
	makeFormat,
	seriesCss,
	type ValueFormat,
} from "./shared";

export interface DonutSlice {
	/** Unique within the chart: it keys the slice, legend row and table row. */
	label: string;
	value: number;
	/** Name (`"blue"`, `"grey"`…), CSS colour or `var(--token)`. Default: `colors`, then the series colours in order. */
	color?: ChartColor;
	/** This slice's texture. It wins over the chart's `fill`. */
	fill?: FillVariant;
}

export interface DitherDonutChartProps {
	data: readonly DonutSlice[];
	/** `donut` has a hole with the total (or the hovered slice) in it; `pie` is whole. Default `donut`. */
	variant?: "donut" | "pie";
	/** Alias of `variant` (the dithercn name). Wins when both are set. */
	mode?: "donut" | "pie";
	/**
	 * Texture of every slice: `gradient` (dense at the rim, thinning inward), `dotted`, `hatched` or `solid`.
	 * Leave unset to cycle four textures by slice so slices differ without colour. A slice's own `fill` wins.
	 */
	fill?: FillVariant;
	/** Soft glow in each slice's colour behind it. Default `off`. */
	bloom?: ChartBloom;
	/** Colours by slice position: names or CSS colours. A slice's own `color` wins. */
	colors?: readonly ChartColor[];
	/** Diameter in px; shrinks to fit a narrow container. Default 180. */
	size?: number;
	/** Ring thickness as a share of the radius (donut only), 0.05–0.95. Default 0.38. */
	thickness?: number;
	/** Gap between slices, in dither cells. Default about 3 px. */
	gap?: number;
	/** Dither cell in px. Default 2. */
	cell?: number;
	/** Shown after every value, such as " GB". */
	unit?: string;
	/** Number format for values and the centre. Default compact (1.2K). */
	format?: ValueFormat;
	/** Caption under the total in the hole. Default "Total". */
	centerLabel?: string;
	/** Legend with values and percents beside / under the chart. When false it stays in the hidden table. Default true. */
	legend?: boolean;
	/** Hover, keyboard focus, Enter to keep a slice lit, and the legend spotlight. `false` draws a static image. Default true. */
	interactive?: boolean;
	/** Sweep the slices in clockwise the first time the chart is seen. Always off under reduced motion. Default true. */
	animate?: boolean;
	/** Threshold matrix of the textures: Bayer 2/4/8 or blue noise. Default `bayer4`. */
	matrix?: DitherMatrix;
	/** Accessible name; a hidden table follows it. */
	label: string;
	className?: string;
}

/**
 * Four textures cycled by slice, so slices stay apart without colour:
 * dense squares, dots, hatch, sparse squares. Tones are tuned so neighbours
 * differ in weight as well as in shape.
 */
const TEXTURES = [
	{ tone: 0.7, shape: "square" },
	{ tone: 0.42, shape: "dot" },
	{ tone: 0.5, shape: "hatch" },
	{ tone: 0.26, shape: "square" },
] as const;

/** Radial bands of a `gradient` slice, rim first. */
const GRADIENT_TONES = [0.85, 0.6, 0.38, 0.2] as const;

/** Space between slices, in px. */
const GAP_PX = 3;
/** The solid outer rim, in px. */
const RIM = 1.5;
const HOLE = 0.62;
/** Room around the ring, so a 1.5 px rim never touches the box edge. */
const MARGIN = 2;

const percentText = (fraction: number) =>
	fraction > 0 && fraction < 0.01
		? "<1%"
		: fraction < 0.1
			? `${(fraction * 100).toFixed(1)}%`
			: `${Math.round(fraction * 100)}%`;

/** How one slice is painted: one pattern, radial bands of patterns, or a flat colour. */
type SlicePlan =
	| { kind: "single"; fills: DitherFill[] }
	| { kind: "bands"; fills: DitherFill[] }
	| { kind: "solid"; fills: DitherFill[] };

/**
 * Slices that differ in colour and in texture, each with a solid rim arc.
 * Plain SVG: the angles (with gaps) come from `lib/dither/radial-math`, the
 * arcs from `chart-math`. Hover, focus or arrow keys pick a slice and dim
 * the rest; the hole (donut) or a readout (pie) says what it is. Under forced
 * colours, or a cell below 1.5 px, textures become opacity steps.
 */
export function DitherDonutChart({
	data,
	variant: variantProp = "donut",
	mode,
	fill,
	bloom = "off",
	colors: colorsProp,
	size = 180,
	thickness,
	gap,
	cell = 2,
	unit = "",
	format,
	centerLabel = "Total",
	legend = true,
	interactive = true,
	animate = true,
	matrix,
	label,
	className,
}: DitherDonutChartProps) {
	const variant = mode ?? variantProp;
	const rootRef = useRef<HTMLDivElement>(null);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const reduced = useReducedMotionPref();
	const entrance = useEntrance(rootRef, animate);
	const uid = useChartUid();
	const solid = forced || cell < MIN_DITHER_CELL;
	const fmtNumber = makeFormat(format, compact);
	const fmt = (value: number) => `${fmtNumber(value)}${unit}`;
	const [hovered, setHovered] = useState<number | null>(null);
	const [locked, setLocked] = useState<number | null>(null);
	const active = interactive ? (hovered ?? locked) : null;

	const px = Math.max(48, Math.min(size, width ?? size));
	const cx = px / 2;
	const outer = px / 2 - MARGIN;
	const inner =
		variant === "pie"
			? 0
			: thickness === undefined
				? outer * HOLE
				: outer * (1 - clamp(thickness, 0.05, 0.95));

	const colors = useMemo(
		() => data.map((s, i) => seriesCss(s.color, colorsProp, i)),
		[data, colorsProp],
	);
	const slices = useMemo(() => {
		const midRadius = variant === "pie" ? outer * 0.55 : (inner + outer) / 2;
		const gapPx = gap === undefined ? GAP_PX : gap * cell;
		return sliceAngles(
			data.map((s) => s.value),
			{ gap: gapPx / Math.max(1, midRadius) },
		);
	}, [data, outer, inner, variant, gap, cell]);
	const total = slices.reduce((sum, s) => sum + s.value, 0);

	const body = outer - RIM;
	const paths = useMemo(
		() =>
			slices.map((s) => ({
				body: arcPath(cx, cx, inner, body, s.from, s.to),
				rim: arcPath(cx, cx, body, outer, s.from, s.to),
				// Radial bands for a `gradient` slice, rim first.
				bands: GRADIENT_TONES.map((_, k) => {
					const step = (body - inner) / GRADIENT_TONES.length;
					const r1 = body - k * step;
					return arcPath(cx, cx, r1 - step, r1, s.from, s.to);
				}),
			})),
		[slices, cx, inner, body, outer],
	);

	const plans = useMemo<SlicePlan[]>(
		() =>
			data.map((slice, i) => {
				const color = colors[i] ?? "var(--dither-ink)";
				const variantOf = slice.fill ?? fill;
				if (variantOf === undefined) {
					const t = TEXTURES[i % TEXTURES.length] ?? TEXTURES[0];
					return {
						kind: "single",
						fills: [
							{
								...inkFill(t.tone, color, cell, matrix),
								shape: t.shape,
							},
						],
					};
				}
				if (variantOf === "solid") return { kind: "solid", fills: [] };
				if (variantOf === "gradient") {
					return {
						kind: "bands",
						fills: GRADIENT_TONES.map((tone) =>
							inkFill(tone, color, cell, matrix),
						),
					};
				}
				return {
					kind: "single",
					fills: [flatFill(variantOf, color, cell, matrix)],
				};
			}),
		[data, colors, fill, cell, matrix],
	);
	const offsets = useMemo(() => {
		let next = 0;
		return plans.map((plan) => {
			const start = next;
			next += plan.fills.length;
			return start;
		});
	}, [plans]);
	const allFills = useMemo(
		() => (solid ? [] : plans.flatMap((plan) => plan.fills)),
		[solid, plans],
	);
	const urls = useDitherFillUrls(allFills);

	/** Paint of slice `i` (its densest band for a gradient): used by the legend swatch. */
	const paintFor = (i: number) => {
		const color = colors[i] ?? "var(--dither-ink)";
		const plan = plans[i];
		const tone = plan?.fills[0]?.tone ?? 0.7;
		if (solid || !plan || plan.kind === "solid") {
			return {
				fill: color,
				opacity: plan?.kind === "solid" ? 1 : Math.min(1, tone + 0.15),
			};
		}
		return { fill: urls[offsets[i] ?? 0] ?? "none", opacity: 1 };
	};
	const bandUrl = (i: number, k: number) => urls[(offsets[i] ?? 0) + k];

	const onKey = (e: KeyboardEvent) => {
		const current = active;
		if (e.key === "Enter" || e.key === " ") {
			e.preventDefault();
			setLocked((cur) =>
				cur === current || current === null ? null : current,
			);
			return;
		}
		const next: Record<string, number | null> = {
			ArrowRight: stepSlice(slices, current, 1),
			ArrowDown: stepSlice(slices, current, 1),
			ArrowLeft: stepSlice(slices, current, -1),
			ArrowUp: stepSlice(slices, current, -1),
			Home: stepSlice(slices, null, 1),
			End: stepSlice(slices, null, -1),
			Escape: null,
		};
		if (e.key in next) {
			e.preventDefault();
			e.stopPropagation();
			const target = next[e.key] ?? null;
			setHovered(target);
			if (e.key === "Escape") setLocked(null);
		}
	};

	const dim = (i: number) => (active !== null && active !== i ? 0.45 : 1);
	const activeSlice = active === null ? undefined : slices[active];
	const activeData = active === null ? undefined : data[active];
	const hole = inner * 1.6;
	const piePoint =
		variant === "pie" && activeSlice
			? polar(cx, cx, outer * 0.62, activeSlice.mid)
			: null;
	const summary = `${label}: ${data
		.map((s, i) => `${s.label} ${fmt(slices[i]?.value ?? 0)}`)
		.join(", ")}`;

	const drawing = (
		<svg
			width={px}
			height={px}
			className="block overflow-visible"
			aria-hidden
			onPointerLeave={interactive ? () => setHovered(null) : undefined}
		>
			{total <= 0 ? (
				<path
					d={arcPath(cx, cx, inner, outer, 0, Math.PI * 2)}
					fill="var(--dither-track)"
				/>
			) : null}
			<Bloom bloom={bloom} box={{ width: px, height: px }}>
				{data.map((d, i) => {
					const path = paths[i];
					const s = slices[i];
					return path && s && s.value > 0 ? (
						<path key={d.label} d={path.body} fill={colors[i]} />
					) : null;
				})}
			</Bloom>
			<Sweep plan={entrance} cx={cx} cy={cx} radius={outer}>
				{data.map((d, i) => {
					const s = slices[i];
					const path = paths[i];
					const plan = plans[i];
					if (!s || !path || !plan || s.value <= 0) return null;
					const color = colors[i] ?? "var(--dither-ink)";
					const paint = paintFor(i);
					return (
						// biome-ignore lint/a11y/noStaticElementInteractions: pointer hover and lock only; the same actions are keyboard-reachable through the button around this aria-hidden drawing
						<g
							key={d.label}
							style={{ opacity: dim(i) }}
							className={cn(
								!reduced && "transition-opacity duration-150 ease-out",
							)}
							onPointerEnter={interactive ? () => setHovered(i) : undefined}
							onPointerMove={interactive ? () => setHovered(i) : undefined}
							onClick={
								interactive
									? () => setLocked((cur) => (cur === i ? null : i))
									: undefined
							}
						>
							{plan.kind === "bands" && !solid ? (
								path.bands.map((band, k) => (
									<path
										// biome-ignore lint/suspicious/noArrayIndexKey: radial bands are an ordered, fixed-length stack
										key={k}
										d={band}
										fill={bandUrl(i, k) ?? color}
									/>
								))
							) : (
								<path
									d={path.body}
									fill={paint.fill}
									fillOpacity={paint.opacity}
								/>
							)}
							<path d={path.rim} fill={color} />
						</g>
					);
				})}
			</Sweep>
		</svg>
	);

	// A literal-role props object per mode, spread onto the root: the
	// interactive chart is a group around a keyboard-driven button, the
	// static one a labelled image.
	const rootProps = interactive
		? { role: "group" as const, "aria-label": label }
		: { role: "img" as const, "aria-label": summary };

	return (
		<div
			ref={rootRef}
			{...rootProps}
			className={cn(
				"relative flex min-w-0 flex-wrap items-center gap-x-6 gap-y-3 rounded-ctl has-[:focus-visible]:focus-ring",
				className,
			)}
		>
			<div className="relative shrink-0" style={{ width: px, height: px }}>
				{interactive ? (
					<button
						type="button"
						aria-label={`${label}. ${data.map((s) => s.label).join(", ")}. Use the arrow keys to inspect slices, Enter to keep one lit.`}
						aria-describedby={`${uid}-live`}
						onKeyDown={onKey}
						onBlur={() => {
							setHovered(null);
						}}
						className="block rounded-ctl outline-none"
					>
						{drawing}
					</button>
				) : (
					drawing
				)}
				{variant === "donut" ? (
					<div
						className="pointer-events-none absolute top-1/2 left-1/2 flex -translate-x-1/2 -translate-y-1/2 flex-col items-center gap-1 text-center"
						style={{ width: hole }}
					>
						<span className="type-metric max-w-full truncate text-ink-1 tabular-nums leading-none">
							{fmt(activeSlice ? activeSlice.value : total)}
						</span>
						<span className="type-micro max-w-full truncate leading-none">
							{activeSlice && activeData
								? `${percentText(activeSlice.fraction)} · ${activeData.label}`
								: centerLabel}
						</span>
					</div>
				) : null}
				{piePoint && activeSlice && activeData ? (
					<ChartReadout
						title={activeData.label}
						rows={[
							{
								label: percentText(activeSlice.fraction),
								color: colors[activeSlice.index] ?? "var(--dither-ink)",
								value: fmt(activeSlice.value),
							},
						]}
						style={{
							left: clamp(piePoint.x - 56, 0, Math.max(0, px - 112)),
							top: clamp(piePoint.y - 24, 0, Math.max(0, px - 48)),
							...(reduced
								? null
								: { transition: "left 150ms ease-out, top 150ms ease-out" }),
						}}
					/>
				) : null}
			</div>
			<ul
				aria-label="Legend"
				// Kept for screen readers when hidden visually: the table below repeats it.
				className={
					legend
						? "flex min-w-36 max-w-72 flex-1 flex-col gap-1.5 font-mono text-2xs leading-none"
						: "sr-only"
				}
			>
				{data.map((s, i) => {
					const slice = slices[i];
					const paint = paintFor(i);
					const color = colors[i] ?? "var(--dither-ink)";
					const row = (
						<>
							<svg
								width={12}
								height={12}
								aria-hidden
								className="block shrink-0"
							>
								<rect
									x={0.75}
									y={0.75}
									width={10.5}
									height={10.5}
									rx={2}
									fill={paint.fill}
									fillOpacity={paint.opacity}
									stroke={color}
									strokeWidth={1.5}
								/>
							</svg>
							<span
								className={cn(
									"min-w-0 flex-1 truncate text-left font-medium",
									active === i ? "text-ink-1" : "text-ink-2",
								)}
							>
								{s.label}
							</span>
							<span className="text-ink-1 tabular-nums">
								{fmt(slice?.value ?? 0)}
							</span>
							<span className="w-10 text-right text-ink-3 tabular-nums">
								{percentText(slice?.fraction ?? 0)}
							</span>
						</>
					);
					const rowClass = cn(
						"flex w-full items-center gap-2 transition-opacity duration-150",
						reduced && "transition-none",
						active !== null && active !== i && "opacity-55",
					);
					return (
						<li key={s.label}>
							{interactive && legend ? (
								<button
									type="button"
									aria-pressed={locked === i}
									onPointerEnter={() => setHovered(i)}
									onPointerLeave={() => setHovered(null)}
									onFocus={() => setHovered(i)}
									onBlur={() => setHovered(null)}
									onClick={() => setLocked((cur) => (cur === i ? null : i))}
									onKeyDown={(e) => {
										if (e.key === "Escape") setLocked(null);
									}}
									className={cn(
										rowClass,
										"cursor-pointer rounded-ctl outline-none focus-visible:focus-ring",
									)}
								>
									{row}
								</button>
							) : (
								<span className={rowClass}>{row}</span>
							)}
						</li>
					);
				})}
			</ul>
			{interactive ? (
				<ChartLive
					id={`${uid}-live`}
					text={
						activeSlice && activeData
							? `${activeData.label}: ${fmt(activeSlice.value)}, ${percentText(activeSlice.fraction)}`
							: ""
					}
				/>
			) : null}
			<table className="sr-only">
				<caption>{label}</caption>
				<thead>
					<tr>
						<th scope="col">Slice</th>
						<th scope="col">Value</th>
						<th scope="col">Share</th>
					</tr>
				</thead>
				<tbody>
					{data.map((s, i) => (
						<tr key={s.label}>
							<th scope="row">{s.label}</th>
							<td>{fmt(slices[i]?.value ?? 0)}</td>
							<td>{percentText(slices[i]?.fraction ?? 0)}</td>
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
