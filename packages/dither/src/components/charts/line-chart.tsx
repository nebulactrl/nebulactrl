import { cn } from "cn";
import {
	Fragment,
	type KeyboardEvent,
	type PointerEvent,
	useMemo,
	useRef,
	useState,
} from "react";
import {
	bandEdges,
	markerIndices,
	markerStride,
	splitRuns,
} from "../../lib/bars-math";
import {
	clamp,
	compact,
	linear,
	linePath,
	niceTicks,
	type Pt,
	regionPath,
	sampleCurve,
} from "../../lib/chart-math";
import { type DitherFill, useDitherFillUrls } from "../../lib/pattern";
import {
	MIN_DITHER_CELL,
	useElementWidth,
	useForcedColors,
} from "../../lib/prefs";
import { scrubKeyTarget } from "./cartesian-math";
import {
	Bloom,
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
	type ChartColor,
	type ChartFeatureProps,
	ChartLegend,
	ChartReadout,
	type FillVariant,
	flatFill,
	inkFill,
	makeFormat,
	read,
	resolveColor,
	seriesCss,
	type ValueFormat,
} from "./shared";

/** A reading: `null` / `undefined` mean "no data here" and break the line. */
export type LineValue = number | null | undefined;

export interface DitherLineBand<T> {
	low: Accessor<T, LineValue>;
	high: Accessor<T, LineValue>;
	/** Colour of the band. Default the series' colour. */
	color?: ChartColor;
}

export interface DitherLineSeries<T> {
	label: string;
	value: Accessor<T, LineValue>;
	color?: ChartColor;
	/** A dithered region between two readings, such as p50…p99. */
	band?: DitherLineBand<T>;
	/** Alias of `band` (the dithercn name): the region between `low` and `high`, with an optional `color`. */
	range?: DitherLineBand<T>;
	/** A dashed line reads as a reference (a previous period, a target); the legend repeats the dash. */
	dashed?: boolean;
}

export interface DitherLineReference {
	value: number;
	/** Solid text at the line's right end. */
	label: string;
}

export interface DitherLineChartProps<T> extends ChartFeatureProps {
	/** One entry per point along x, in order. */
	data: readonly T[];
	/** Each point's x label: a field or a function. */
	x: Accessor<T, string | number>;
	/** One to four series work best. */
	series: readonly [DitherLineSeries<T>, ...DitherLineSeries<T>[]];
	curve?: "monotone" | "linear";
	/** Circles with a paper ring: none, every point (thinned when crowded) or the last one. Default `none`. */
	markers?: "none" | "all" | "last";
	/** Dashed horizontal lines with a text label. */
	references?: readonly DitherLineReference[];
	/** Start the value axis at zero. Default true; false fits the data. */
	zero?: boolean;
	/** Height in px, axes included. Default 220. */
	height?: number;
	/** Value and category axes. Default true. */
	axes?: boolean;
	/** Legend under the chart. Default: two or more series. */
	legend?: boolean;
	/** Scrub readout and crosshair. Default true. */
	tooltip?: boolean;
	/** Axis, readout and table number format. Default compact (1.2K). */
	format?: ValueFormat;
	/** Dither cell of the range bands, in px. Default 3. */
	cell?: number;
	/** Tone of the range bands, 0–1. Default 0.3. */
	bandLevel?: number;
	/** Alias of `bandLevel` (the dithercn name). `bandLevel` wins when both are set. */
	level?: number;
	/** Texture of the range bands: `gradient` is one even level, `solid` a translucent tint. Default `gradient`. */
	fill?: FillVariant;
	/** Accessible name; the summary and a hidden table follow it. */
	label: string;
	className?: string;
}

const MARGIN_AXES = { top: 10, right: 12, bottom: 20, left: 40 };
const MARGIN_BARE = { top: 6, right: 6, bottom: 6, left: 6 };
const MARKER_RADIUS = 3;
const PAPER_RING = { stroke: "var(--dither-paper)", strokeWidth: 2 } as const;

const finite = (value: LineValue): number | null =>
	typeof value === "number" && Number.isFinite(value) ? value : null;

/**
 * Crisp 1.5 px lines for a few series, with optional dithered range bands,
 * point markers and dashed reference lines. Missing readings (`null`) lift
 * the line instead of joining across. Plain SVG; scales and curve sampling
 * live in `lib/dither/chart-math`, gap and band helpers in `bars-math`.
 * Text is solid; under forced colours (or a cell below 1.5 px) bands fall
 * back to a plain tint.
 */
export function DitherLineChart<T>({
	data,
	x,
	series: seriesProp,
	curve: curveProp = "monotone",
	markers = "none",
	references,
	zero = true,
	height = 220,
	axes = true,
	legend,
	tooltip = true,
	format,
	cell = 3,
	bandLevel,
	level: levelProp,
	fill = "gradient",
	colors,
	bloom = "off",
	interactive = true,
	animate = true,
	matrix,
	legendAction = "spotlight",
	label,
	className,
}: DitherLineChartProps<T>) {
	const rootRef = useRef<HTMLDivElement>(null);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const solid = forced || cell < MIN_DITHER_CELL;
	const legendState = useLegendState(seriesProp.length, legendAction);
	const hidden = legendState.hidden;
	const entrance = useEntrance(rootRef, animate);
	const uid = useChartUid();
	const live = interactive && tooltip;
	const curve = curveProp === "linear" ? "linear" : "monotone";
	const bandTone = bandLevel ?? levelProp ?? 0.3;
	const [active, setActive] = useState<number | null>(null);
	const fmt = makeFormat(format, compact);
	const m = axes ? MARGIN_AXES : MARGIN_BARE;
	const showLegend = legend ?? seriesProp.length > 1;
	const series = useMemo(
		() =>
			seriesProp.map((s) =>
				s.band || !s.range ? s : { ...s, band: s.range },
			) as unknown as readonly [DitherLineSeries<T>, ...DitherLineSeries<T>[]],
		[seriesProp],
	);

	const shown = useMemo(
		() =>
			series
				.map((s, i) => {
					const css = seriesCss(s.color, colors, i);
					return {
						...s,
						index: i,
						css,
						bandCss: s.band?.color ? resolveColor(s.band.color) : css,
					};
				})
				.filter((s) => !hidden.has(s.label)),
		[series, hidden, colors],
	);

	const model = useMemo(() => {
		const cols = shown.map((s) => {
			const values = data.map((d) => finite(read(d, s.value)));
			const band = s.band;
			const edges = band
				? bandEdges(
						data.map((d) => read(d, band.low)),
						data.map((d) => read(d, band.high)),
					)
				: null;
			return { values, edges };
		});
		const all: number[] = [];
		for (const col of cols) {
			for (const v of col.values) if (v !== null) all.push(v);
			for (const v of col.edges?.low ?? []) if (v !== null) all.push(v);
			for (const v of col.edges?.high ?? []) if (v !== null) all.push(v);
		}
		for (const ref of references ?? []) {
			if (Number.isFinite(ref.value)) all.push(ref.value);
		}
		const lo = all.length > 0 ? Math.min(...all) : 0;
		const hi = all.length > 0 ? Math.max(...all) : 1;
		const ticks = niceTicks(zero ? Math.min(0, lo) : lo, hi, 4);
		return {
			n: data.length,
			cols,
			ticks,
			bottom: ticks[0] ?? 0,
			top: ticks.at(-1) ?? 1,
		};
	}, [data, shown, references, zero]);

	const geometry = useMemo(() => {
		if (width === null || width < 24) return null;
		const plotW = Math.max(1, width - m.left - m.right);
		const plotH = Math.max(1, height - m.top - m.bottom);
		const baseline = m.top + plotH;
		const xAt = (i: number) =>
			m.left + (model.n <= 1 ? plotW / 2 : (i / (model.n - 1)) * plotW);
		const yScale = linear([model.bottom, model.top], [baseline, m.top]);
		const stride = markerStride(model.n <= 1 ? plotW : plotW / (model.n - 1));
		const pointAt = (i: number, v: number): Pt => ({ x: xAt(i), y: yScale(v) });
		const perSeries = model.cols.map((col) => {
			const runs = splitRuns(col.values);
			const lines = runs
				.filter((run) => run.values.length > 1)
				.map((run) =>
					sampleCurve(
						run.values.map((v, k) => pointAt(run.start + k, v)),
						curve,
						3,
					),
				);
			const dots = runs
				.filter((run) => run.values.length === 1)
				.map((run) => pointAt(run.start, run.values[0] ?? 0));
			const bands = col.edges
				? splitRuns(col.edges.high)
						.filter((run) => run.values.length > 1)
						.map((run) => {
							const upper = run.values.map((v, k) => pointAt(run.start + k, v));
							const lower = run.values.map((_, k) =>
								pointAt(run.start + k, col.edges?.low[run.start + k] ?? 0),
							);
							return regionPath(
								sampleCurve(upper, curve, 3),
								sampleCurve(lower, curve, 3),
							);
						})
				: [];
			const marks = markerIndices(col.values, markers, stride).map((i) =>
				pointAt(i, col.values[i] ?? 0),
			);
			return {
				linePaths: lines.map((pts) => linePath(pts)),
				dots,
				bands,
				marks,
			};
		});
		const refs = (references ?? [])
			.filter((r) => Number.isFinite(r.value))
			.map((r) => ({ ...r, y: yScale(r.value) }));
		return { plotW, plotH, baseline, xAt, yScale, perSeries, refs };
	}, [width, height, m, model, curve, markers, references]);

	const fills = useMemo<DitherFill[]>(
		() =>
			solid || fill === "solid"
				? []
				: shown
						.filter((s) => s.band)
						.map((s) =>
							fill === "gradient"
								? inkFill(bandTone, s.bandCss, cell, matrix)
								: flatFill(fill, s.bandCss, cell, matrix),
						),
		[solid, shown, bandTone, cell, fill, matrix],
	);
	const urls = useDitherFillUrls(fills);
	const bandUrls = useMemo(() => {
		const map = new Map<string, string>();
		let next = 0;
		for (const s of shown) {
			if (!s.band) continue;
			const url = urls[next];
			next += 1;
			if (url) map.set(s.label, url);
		}
		return map;
	}, [shown, urls]);

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

	const current = active !== null && active < data.length ? active : null;
	const setFromX = (clientX: number, el: Element) => {
		if (!geometry || data.length === 0) return;
		const box = el.getBoundingClientRect();
		const t = (clientX - box.left - m.left) / geometry.plotW;
		setActive(clamp(Math.round(t * (data.length - 1)), 0, data.length - 1));
	};
	const onPointer = (e: PointerEvent<SVGRectElement>) =>
		setFromX(e.clientX, e.currentTarget.ownerSVGElement ?? e.currentTarget);
	const onKey = (e: KeyboardEvent) => {
		const target = scrubKeyTarget(e.key, current, data.length);
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
	const activeX = geometry && current !== null ? geometry.xAt(current) : null;

	const readoutRows = () => {
		if (current === null) return [];
		return shown.flatMap((s, k) => {
			const col = model.cols[k];
			const v = col?.values[current] ?? null;
			const rows = [
				{ label: s.label, color: s.css, value: v === null ? "—" : fmt(v) },
			];
			const lo = col?.edges?.low[current] ?? null;
			const hi = col?.edges?.high[current] ?? null;
			if (lo !== null && hi !== null) {
				rows.push({
					label: `${s.label} range`,
					color: s.css,
					value: `${fmt(lo)}–${fmt(hi)}`,
				});
			}
			return rows;
		});
	};
	const liveText =
		current === null
			? ""
			: `${xLabel(current)}: ${readoutRows()
					.map((r) => `${r.label} ${r.value}`)
					.join(", ")}`;

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
												strokeDasharray={
													tick === model.bottom ? undefined : "3 3"
												}
											/>
											<text
												x={m.left - 8}
												y={y}
												dy="0.32em"
												textAnchor="end"
												className={AXIS_TEXT}
											>
												{fmt(tick)}
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
						{geometry.refs.map((ref) => (
							<g key={`${ref.label}-${ref.value}`} aria-hidden>
								<line
									x1={m.left}
									x2={m.left + geometry.plotW}
									y1={ref.y}
									y2={ref.y}
									className="stroke-line-strong"
									strokeDasharray="5 4"
								/>
								<text
									x={m.left + geometry.plotW - 2}
									y={ref.y - 5}
									textAnchor="end"
									className="fill-ink-3 font-mono text-[10px]"
									style={{
										paintOrder: "stroke",
										stroke: "var(--dither-paper)",
										strokeWidth: 3,
										strokeLinejoin: "round",
									}}
								>
									{ref.label}
								</text>
							</g>
						))}
						{shown.map((s, k) => {
							const paths = geometry.perSeries[k]?.bands ?? [];
							if (paths.length === 0) return null;
							const url = bandUrls.get(s.label);
							return (
								<g
									key={s.label}
									aria-hidden
									style={dimStyle(legendState.opacityOf(s.label))}
								>
									<Bloom bloom={bloom} box={{ width, height }}>
										{paths.map((d) => (
											<path key={d} d={d} fill={s.bandCss} />
										))}
									</Bloom>
									<Entrance plan={entrance} kind="fade" delay={200 + k * 90}>
										{paths.map((d) => (
											<path
												key={d}
												d={d}
												fill={
													url ??
													`color-mix(in oklab, ${s.bandCss} 20%, transparent)`
												}
											/>
										))}
									</Entrance>
								</g>
							);
						})}
						{shown.map((s, k) => {
							const g = geometry.perSeries[k];
							if (!g) return null;
							return (
								<g
									key={s.label}
									aria-hidden
									style={dimStyle(legendState.opacityOf(s.label))}
								>
									<Bloom bloom={bloom} box={{ width, height }}>
										{g.linePaths.map((d) => (
											<path
												key={d}
												d={d}
												fill="none"
												stroke={s.css}
												strokeWidth={3}
											/>
										))}
									</Bloom>
									<Entrance
										plan={entrance}
										kind={s.dashed ? "fade" : "wipe"}
										delay={k * 90}
									>
										{g.linePaths.map((d) => (
											<path
												key={d}
												d={d}
												fill="none"
												stroke={s.css}
												strokeWidth={1.5}
												strokeLinejoin="round"
												strokeLinecap={s.dashed ? "butt" : "round"}
												strokeDasharray={s.dashed ? "5 4" : undefined}
											/>
										))}
										{g.dots.map((p) => (
											<circle key={p.x} cx={p.x} cy={p.y} r={2} fill={s.css} />
										))}
									</Entrance>
								</g>
							);
						})}
						{shown.map((s, k) => (
							<g
								key={s.label}
								aria-hidden
								style={dimStyle(legendState.opacityOf(s.label))}
							>
								<Entrance plan={entrance} kind="fade" delay={300 + k * 90}>
									{geometry.perSeries[k]?.marks.map((p) => (
										<circle
											key={p.x}
											cx={p.x}
											cy={p.y}
											r={MARKER_RADIUS}
											fill={s.css}
											style={PAPER_RING}
										/>
									))}
								</Entrance>
							</g>
						))}
						{live && activeX !== null && current !== null ? (
							<g aria-hidden>
								<line
									x1={activeX}
									x2={activeX}
									y1={m.top}
									y2={geometry.baseline}
									className="stroke-line-strong"
								/>
								{shown.map((s, k) => {
									const v = model.cols[k]?.values[current] ?? null;
									if (v === null) return null;
									return (
										<circle
											key={s.label}
											cx={activeX}
											cy={geometry.yScale(v)}
											r={3.5}
											fill={s.css}
											style={PAPER_RING}
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
				{live && geometry && current !== null && activeX !== null ? (
					<ChartReadout
						title={xLabel(current)}
						rows={readoutRows()}
						style={{
							left: clamp(activeX + 12, 0, Math.max(0, (width ?? 0) - 150)),
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
						...(s.dashed ? { dashed: true } : {}),
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
							<Fragment key={s.label}>
								<th scope="col">{s.label}</th>
								{s.band ? <th scope="col">{`${s.label} range`}</th> : null}
							</Fragment>
						))}
					</tr>
				</thead>
				<tbody>
					{data.map((d, i) => (
						<tr key={xLabel(i) + String(i)}>
							<th scope="row">{String(read(d, x))}</th>
							{series.map((s) => {
								const v = finite(read(d, s.value));
								const lo = s.band ? finite(read(d, s.band.low)) : null;
								const hi = s.band ? finite(read(d, s.band.high)) : null;
								return (
									<Fragment key={s.label}>
										<td>{v === null ? "no data" : fmt(v)}</td>
										{s.band ? (
											<td>
												{lo === null || hi === null
													? "no data"
													: `${fmt(Math.min(lo, hi))} to ${fmt(Math.max(lo, hi))}`}
											</td>
										) : null}
									</Fragment>
								);
							})}
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
