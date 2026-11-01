import { cn } from "cn";
import {
	type KeyboardEvent,
	type PointerEvent,
	useMemo,
	useRef,
	useState,
} from "react";
import {
	clamp,
	compact,
	linear,
	linePath,
	niceTicks,
	type Pt,
	sampleCurve,
	stack,
} from "../../lib/chart-math";
import {
	MIN_DITHER_CELL,
	useElementWidth,
	useForcedColors,
} from "../../lib/prefs";
import { AreaFill } from "./area-fill";
import { scrubKeyTarget } from "./cartesian-math";
import {
	ChartLive,
	dimStyle,
	Entrance,
	useChartUid,
	useEntrance,
	useLegendState,
} from "./chart-kit";
import {
	type Accessor,
	AXIS_TEXT,
	type ChartFeatureProps,
	ChartLegend,
	ChartReadout,
	type ChartSeries,
	type FillVariant,
	makeFormat,
	read,
	seriesCss,
	type ValueFormat,
} from "./shared";

export interface DitherAreaChartProps<T> extends ChartFeatureProps {
	/** One entry per point along x, in order. */
	data: readonly T[];
	/** Each point's x label: a field or a function. */
	x: Accessor<T, string | number>;
	series: readonly [ChartSeries<T>, ...ChartSeries<T>[]];
	/** `none` overlaps series from the baseline; `stacked` sits each on the last; `percent` stacks to 100%. */
	stack?: "none" | "stacked" | "percent";
	/** Height in px, axes included. Default 220. */
	height?: number;
	/** Texture under the line. Default `gradient`: densest at the line. */
	fill?: FillVariant;
	/** `smooth` (alias `monotone`) never overshoots a data point; `linear` joins them with straight segments. */
	curve?: "monotone" | "smooth" | "linear";
	/** Value and category axes. Default true; false lets the fill run to the edges. */
	axes?: boolean;
	/** Legend under the chart. Default: two or more series. */
	legend?: boolean;
	/** Scrub readout. Default true. */
	tooltip?: boolean;
	/** Axis, readout and table number format. Default compact (1.2K). */
	format?: ValueFormat;
	/** Dither cell in px. Default 3. */
	cell?: number;
	/** Tone just under the line, 0–1. Default 0.72. */
	level?: number;
	/** Accessible name; the summary and a hidden table follow it. */
	label: string;
	className?: string;
}

const MARGIN_AXES = { top: 8, right: 8, bottom: 20, left: 40 };
const MARGIN_BARE = { top: 3, right: 0, bottom: 0, left: 0 };

/**
 * A line over a dithered fill that is densest at the line. Plain SVG, no
 * chart library: scales, curve sampling and band polygons live in
 * `lib/dither/chart-math`. The text is solid, the line is crisp, and under
 * forced colours (or a cell below 1.5 px) the fill falls back to a plain tint.
 */
export function DitherAreaChart<T>({
	data,
	x,
	series,
	stack: stackMode = "none",
	height = 220,
	fill = "gradient",
	curve: curveProp = "monotone",
	axes = true,
	legend,
	tooltip = true,
	format,
	cell = 3,
	level = 0.72,
	colors,
	bloom = "off",
	interactive = true,
	animate = true,
	matrix,
	legendAction = "spotlight",
	label,
	className,
}: DitherAreaChartProps<T>) {
	const rootRef = useRef<HTMLDivElement>(null);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const solid = forced || cell < MIN_DITHER_CELL;
	const legendState = useLegendState(series.length, legendAction);
	const hidden = legendState.hidden;
	const [active, setActive] = useState<number | null>(null);
	const entrance = useEntrance(rootRef, animate);
	const uid = useChartUid();
	const curve = curveProp === "linear" ? "linear" : "monotone";
	const live = interactive && tooltip;
	const fmt = makeFormat(format, compact);
	const m = axes ? MARGIN_AXES : MARGIN_BARE;
	const showLegend = legend ?? series.length > 1;

	const shown = useMemo(
		() =>
			series
				.map((s, i) => ({ ...s, index: i, css: seriesCss(s.color, colors, i) }))
				.filter((s) => !hidden.has(s.label)),
		[series, hidden, colors],
	);

	const model = useMemo(() => {
		const n = data.length;
		const raw = shown.map((s) =>
			data.map((d) => Number(read(d, s.value)) || 0),
		);
		let upper: number[][];
		let lower: number[][] | null = null;
		if (stackMode === "none") {
			upper = raw;
		} else {
			const base =
				stackMode === "percent"
					? raw.map((row, s) =>
							row.map((_, i) => {
								const total = raw.reduce(
									(sum, r) => sum + Math.max(0, r[i] ?? 0),
									0,
								);
								return total > 0
									? (Math.max(0, raw[s]?.[i] ?? 0) / total) * 100
									: 0;
							}),
						)
					: raw;
			const stacked = stack(base);
			upper = stacked.upper;
			lower = stacked.lower;
		}
		const max = stackMode === "percent" ? 100 : Math.max(0, ...upper.flat());
		const ticks = niceTicks(0, max || 1, 4);
		return { n, raw, upper, lower, ticks, top: ticks.at(-1) ?? 1 };
	}, [data, shown, stackMode]);

	const geometry = useMemo(() => {
		if (width === null || width < 24) return null;
		const plotW = Math.max(1, width - m.left - m.right);
		const plotH = Math.max(1, height - m.top - m.bottom);
		const xAt = (i: number) =>
			m.left + (model.n <= 1 ? plotW / 2 : (i / (model.n - 1)) * plotW);
		const yScale = linear([0, model.top], [m.top + plotH, m.top]);
		const baseline = m.top + plotH;
		const lines = model.upper.map((row) => {
			const pts: Pt[] = row.map((v, i) => ({ x: xAt(i), y: yScale(v) }));
			return sampleCurve(pts, curve, 3);
		});
		const lowers = model.lower?.map((row) =>
			sampleCurve(
				row.map((v, i) => ({ x: xAt(i), y: yScale(v) })),
				curve,
				3,
			),
		);
		return { plotW, plotH, xAt, yScale, baseline, lines, lowers };
	}, [width, height, m.left, m.right, m.top, m.bottom, model, curve]);

	const tickCount = geometry ? Math.max(2, Math.floor(geometry.plotW / 78)) : 2;
	const xLabels = useMemo(() => {
		if (!geometry) return [];
		const n = data.length;
		if (n === 0) return [];
		const count = Math.min(n, tickCount);
		return Array.from({ length: count }, (_, k) =>
			count === 1 ? 0 : Math.round((k / (count - 1)) * (n - 1)),
		);
	}, [geometry, data.length, tickCount]);

	const setFromX = (clientX: number, el: Element) => {
		if (!geometry || data.length === 0) return;
		const box = el.getBoundingClientRect();
		const t = (clientX - box.left - m.left) / geometry.plotW;
		setActive(clamp(Math.round(t * (data.length - 1)), 0, data.length - 1));
	};
	const onPointer = (e: PointerEvent<SVGRectElement>) =>
		setFromX(e.clientX, e.currentTarget.ownerSVGElement ?? e.currentTarget);
	const onKey = (e: KeyboardEvent) => {
		const target = scrubKeyTarget(e.key, active, data.length);
		if (target === undefined) return;
		if (target === null) legendState.clear();
		else {
			e.preventDefault();
			e.stopPropagation();
		}
		setActive(target);
	};

	const xLabel = (i: number) => {
		const d = data[i];
		return d === undefined ? "" : String(read(d, x));
	};
	const activeX = geometry && active !== null ? geometry.xAt(active) : null;
	const liveText =
		active !== null && active < data.length
			? `${xLabel(active)}: ${shown
					.map((s, k) => `${s.label} ${fmt(model.raw[k]?.[active] ?? 0)}`)
					.join(", ")}`
			: "";

	// One literal-role props object per mode, spread onto the root: the
	// interactive chart is a focusable group with a keyboard readout, the
	// static one a labelled image.
	const rootProps = interactive
		? {
				role: "group" as const,
				"aria-label": label,
				"aria-describedby": live ? `${uid}-keys` : undefined,
				tabIndex: live ? 0 : -1,
				onKeyDown: live ? onKey : undefined,
				onBlur: () => setActive(null),
			}
		: {
				role: "img" as const,
				"aria-label": `${label}. ${series.map((s) => s.label).join(", ")}.`,
				tabIndex: -1,
				onBlur: () => setActive(null),
			};

	return (
		<div
			ref={rootRef}
			{...rootProps}
			className={cn(
				"relative flex min-w-0 flex-col gap-2.5 rounded-ctl outline-none focus-visible:focus-ring",
				className,
			)}
		>
			<div className="relative" style={{ height }}>
				{geometry && width !== null ? (
					<svg
						width={width}
						height={height}
						className="block overflow-visible"
						role="img"
						aria-label={`${label}. ${shown.map((s) => s.label).join(", ")}.`}
					>
						{axes
							? model.ticks.map((tick) => {
									const y = geometry.yScale(tick);
									return (
										<g key={tick}>
											<line
												x1={m.left}
												x2={m.left + geometry.plotW}
												y1={y}
												y2={y}
												className="stroke-line-faint"
												strokeDasharray={tick === 0 ? undefined : "3 3"}
											/>
											<text
												x={m.left - 8}
												y={y}
												dy="0.32em"
												textAnchor="end"
												className={AXIS_TEXT}
											>
												{stackMode === "percent" ? `${tick}%` : fmt(tick)}
											</text>
										</g>
									);
								})
							: null}
						{axes
							? xLabels.map((i, k) => (
									<text
										key={i}
										x={geometry.xAt(i)}
										y={height - 4}
										textAnchor={
											k === 0
												? "start"
												: k === xLabels.length - 1
													? "end"
													: "middle"
										}
										className={AXIS_TEXT}
									>
										{xLabel(i)}
									</text>
								))
							: null}
						{shown.map((s, k) => {
							const top = geometry.lines[k];
							if (!top) return null;
							return (
								<g
									key={s.label}
									style={dimStyle(legendState.opacityOf(s.label))}
								>
									<Entrance plan={entrance} kind="wipe" delay={k * 90}>
										<Entrance plan={entrance} kind="fade" delay={120 + k * 90}>
											<AreaFill
												top={top}
												bottom={geometry.lowers?.[k] ?? geometry.baseline}
												color={s.css}
												variant={fill}
												cell={cell}
												depth={Math.min(110, geometry.plotH * 0.8)}
												floor={geometry.baseline}
												level={level}
												solid={solid}
												matrix={matrix}
												bloom={bloom}
											/>
										</Entrance>
									</Entrance>
								</g>
							);
						})}
						{shown.map((s, k) => {
							const top = geometry.lines[k];
							return top ? (
								<g
									key={s.label}
									style={dimStyle(legendState.opacityOf(s.label))}
								>
									<Entrance plan={entrance} kind="wipe" delay={k * 90}>
										<path
											d={linePath(top)}
											fill="none"
											stroke={s.css}
											strokeWidth={1.5}
											strokeLinejoin="round"
											strokeLinecap="round"
										/>
									</Entrance>
								</g>
							) : null;
						})}
						{live && activeX !== null ? (
							<g aria-hidden>
								<line
									x1={activeX}
									x2={activeX}
									y1={m.top}
									y2={geometry.baseline}
									className="stroke-line-strong"
								/>
								{shown.map((s, k) => {
									const v = model.upper[k]?.[active ?? 0] ?? 0;
									return (
										<circle
											key={s.label}
											cx={activeX}
											cy={geometry.yScale(v)}
											r={3.5}
											fill={s.css}
											style={{ stroke: "var(--dither-paper)", strokeWidth: 2 }}
										/>
									);
								})}
							</g>
						) : null}
						{live ? (
							<rect
								x={m.left}
								y={m.top}
								width={geometry.plotW}
								height={geometry.plotH}
								fill="transparent"
								onPointerMove={onPointer}
								onPointerDown={onPointer}
								onPointerLeave={() => setActive(null)}
							/>
						) : null}
					</svg>
				) : null}
				{live && geometry && active !== null && activeX !== null ? (
					<ChartReadout
						title={xLabel(active)}
						rows={shown.map((s, k) => ({
							label: s.label,
							color: s.css,
							value: fmt(model.raw[k]?.[active] ?? 0),
						}))}
						style={{
							left: clamp(activeX + 12, 0, Math.max(0, (width ?? 0) - 130)),
							top: m.top,
						}}
					/>
				) : null}
			</div>
			{showLegend ? (
				<ChartLegend
					items={series.map((s, i) => ({
						label: s.label,
						color: seriesCss(s.color, colors, i),
					}))}
					{...legendState.legendProps}
				/>
			) : null}
			{live ? <ChartLive id={`${uid}-keys`} text={liveText} /> : null}
			<table className="sr-only">
				<caption>{label}</caption>
				<thead>
					<tr>
						<th scope="col">x</th>
						{series.map((s) => (
							<th key={s.label} scope="col">
								{s.label}
							</th>
						))}
					</tr>
				</thead>
				<tbody>
					{data.map((d, i) => (
						<tr key={xLabel(i) + String(i)}>
							<th scope="row">{String(read(d, x))}</th>
							{series.map((s) => (
								<td key={s.label}>{fmt(Number(read(d, s.value)) || 0)}</td>
							))}
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
