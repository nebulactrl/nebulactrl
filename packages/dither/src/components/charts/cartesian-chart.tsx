import { cn } from "cn";
import {
	type KeyboardEvent,
	type PointerEvent,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { compact } from "../../lib/chart-math";
import {
	MIN_DITHER_CELL,
	useElementWidth,
	useForcedColors,
	useReducedMotionPref,
} from "../../lib/prefs";
import {
	bandWidth,
	buildScale,
	type CartesianLayout,
	indexAtX,
	labelAnchor,
	labelIndices,
	opacityFor,
	type Plot,
	plotBox,
	readoutPosition,
	type Scale,
	scrubKeyTarget,
	snapCellToDevice,
	xAt as xAtMath,
	yScaleFor,
} from "./cartesian-math";
import {
	ChartLive,
	type EntrancePlan,
	useChartUid,
	useEntrance,
	useSpotlight,
} from "./chart-kit";
import {
	type Accessor,
	AXIS_TEXT,
	type ChartBloom,
	type ChartColor,
	ChartLegend,
	ChartReadout,
	type ChartSeries,
	type DitherMatrix,
	makeFormat,
	read,
	seriesCss,
	type ValueFormat,
} from "./shared";

/**
 * The shared frame behind area, bar and line charts: measuring, scales,
 * grid and axes, the scrub crosshair and readout, the legend (hover
 * spotlights a series, click locks it) and a hidden table. The marks come
 * from `children`: a render function of the chart's scales, so layers
 * (`DitherAreaLayer`, `DitherBarLayer`, `DitherLineLayer`, or your own SVG)
 * compose on one set of axes.
 */

export interface CartesianSeries<T> extends ChartSeries<T> {
	/** The legend swatch repeats the dash of a dashed line. */
	dashed?: boolean;
}

/** What a layer draws from. Stable while the pointer scrubs, so layers do not redraw. */
export interface CartesianContext {
	/** Prefix for ids. */
	uid: string;
	width: number;
	height: number;
	plot: Plot;
	scale: Scale;
	layout: CartesianLayout;
	/** Number of data. */
	count: number;
	/** Step between points (`point`) or the width of one slot (`band`). */
	band: number;
	/** y of the scale's floor. */
	baseline: number;
	/** Dither cell in CSS px, snapped to whole device pixels. */
	cell: number;
	/** Forced colours or a cell below 1.5 px: draw flat tints instead of dither. */
	solid: boolean;
	matrix: DitherMatrix | undefined;
	bloom: ChartBloom;
	entrance: EntrancePlan;
	/** Centre of datum `index` on x. */
	xAt: (index: number) => number;
	yAt: (value: number) => number;
	/** Each series' values, one number per datum (non-finite read as 0). */
	values: readonly (readonly number[])[];
	/** Each series' CSS colour. */
	colors: readonly string[];
	/** The spotlit series index, or null. */
	focus: number | null;
	/** 1, or dimmed while another series is spotlit. */
	opacityOf: (index: number) => number;
}

export interface CartesianReadoutRow {
	label: string;
	color: string;
	value: string;
}

/** Where a series sits at the scrubbed datum: its crosshair marker and the readout anchor. */
export interface CartesianPoint {
	seriesIndex: number;
	/** y in px. */
	y: number;
}

export interface DitherCartesianChartProps<T> {
	/** One entry per position along x, in order. */
	data: readonly T[];
	/** Each datum's x label: a field or a function. */
	x: Accessor<T, string | number>;
	/** The series the legend, readout and table list. Layers draw them. */
	series: readonly [CartesianSeries<T>, ...CartesianSeries<T>[]];
	/** Accessible name; a hidden table follows it. */
	label: string;
	/** The marks, drawn on the shared scales. Without it the chart is just frame and legend. */
	children?: (context: CartesianContext) => ReactNode;
	/** A ready-made value scale (stacks need their totals). Default: fitted to the series values. */
	scale?: Scale;
	/** Force the value domain `[min, max]` (ticks round outward). */
	domain?: readonly [number, number];
	/** Start the value axis at zero. Default true. */
	zero?: boolean;
	/** `point`: data on the plot edges and between (lines, areas). `band`: each datum owns a slot (bars). Default `point`. */
	layout?: CartesianLayout;
	/** Height in px, axes included. Default 220. */
	height?: number;
	/** Value and category axes. Default true; false lets marks run to the edges. */
	axes?: boolean;
	/** Horizontal grid lines at the ticks (needs `axes`). Default true. */
	grid?: boolean;
	/** Readout while scrubbing. Default true. */
	tooltip?: boolean;
	/** The scrub mark: a `line` with a marker per series, a soft `column` behind the datum, or `none`. Default `line` for point layouts, `column` for band layouts. */
	crosshair?: "line" | "column" | "none";
	/** Legend under the chart. Default: two or more series. */
	legend?: boolean;
	/** Scrubbing, readout and legend spotlight. false is a decorative image with a summary. Default true. */
	interactive?: boolean;
	/** Entrance motion, once, when first seen. Always off under reduced motion. Default true. */
	animate?: boolean;
	/** Glow in each series' colour behind its fill (read by layers). Default `off`. */
	bloom?: ChartBloom;
	/** Colours by series position: names (`"green"`) or CSS. A series' own `color` wins. */
	colors?: readonly ChartColor[];
	/** Dither cell in px, snapped to device pixels. Default `--dither-cell`. */
	cell?: number;
	/** Threshold matrix of the layers' fills. Default `bayer4`. */
	matrix?: DitherMatrix;
	/** Readout, table and (without `tickFormat`) axis number format. Default compact (1.2K). */
	format?: ValueFormat;
	/** Axis tick format when it differs from the readout's. */
	tickFormat?: ValueFormat;
	/** Readout rows for a datum. Default: every series' value. */
	readout?: (index: number) => readonly CartesianReadoutRow[];
	/** Where each series sits at a datum (markers and readout anchor). Default: its value. */
	pointsAt?: (
		index: number,
		context: CartesianContext,
	) => readonly CartesianPoint[];
	/** Extra marks for the scrubbed datum, such as a solid bar. */
	activeMarks?: (index: number, context: CartesianContext) => ReactNode;
	className?: string;
}

interface Scrub {
	index: number;
	/** Glide to the new position (pointer) or jump (keyboard, first touch). */
	glide: boolean;
	visible: boolean;
}

const hide = (current: Scrub | null): Scrub | null =>
	current === null ? null : { ...current, glide: false, visible: false };

const finite = (value: unknown) => {
	const n = typeof value === "number" ? value : Number(value);
	return Number.isFinite(n) ? n : 0;
};

/** The dither cell: the prop, else `--dither-cell`, snapped to whole device pixels. */
function useCellSize(ref: { current: Element | null }, cell?: number): number {
	const [base, setBase] = useState(3);
	const [dpr, setDpr] = useState(1);
	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const value = Number.parseFloat(
			getComputedStyle(el).getPropertyValue("--dither-cell"),
		);
		if (Number.isFinite(value) && value > 0) setBase(value);
		setDpr(window.devicePixelRatio || 1);
	}, [ref]);
	return snapCellToDevice(cell ?? base, dpr);
}

/**
 * The shared chart frame. See the module comment: scales, axes, crosshair,
 * readout, legend and a hidden table around marks you compose.
 */
export function DitherCartesianChart<T>({
	data,
	x,
	series,
	label,
	children,
	scale: scaleProp,
	domain,
	zero = true,
	layout = "point",
	height = 220,
	axes = true,
	grid = true,
	tooltip = true,
	crosshair,
	legend,
	interactive = true,
	animate = true,
	bloom = "off",
	colors,
	cell: cellProp,
	matrix,
	format,
	tickFormat,
	readout,
	pointsAt,
	activeMarks,
	className,
}: DitherCartesianChartProps<T>) {
	const uid = useChartUid();
	const rootRef = useRef<HTMLDivElement>(null);
	const stageRef = useRef<HTMLDivElement>(null);
	const readoutRef = useRef<HTMLDivElement>(null);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const reduced = useReducedMotionPref();
	const entrance = useEntrance(rootRef, animate);
	const cell = useCellSize(rootRef, cellProp);
	const spot = useSpotlight();
	const [scrub, setScrub] = useState<Scrub | null>(null);
	const pointerX = useRef<number | null>(null);
	const frame = useRef(0);
	const fmt = useMemo(() => makeFormat(format, compact), [format]);
	const tickFmt = useMemo(
		() => (tickFormat ? makeFormat(tickFormat, compact) : fmt),
		[tickFormat, fmt],
	);

	const count = data.length;
	const css = useMemo(
		() => series.map((s, i) => seriesCss(s.color, colors, i)),
		[series, colors],
	);
	const values = useMemo(
		() => series.map((s) => data.map((d) => finite(read(d, s.value)))),
		[series, data],
	);
	const scale = useMemo(
		() =>
			scaleProp ??
			buildScale(values.flat(), { zero, ...(domain ? { domain } : {}) }),
		[scaleProp, values, zero, domain],
	);
	const showLegend = legend ?? series.length > 1;
	const scrubKind = interactive
		? (crosshair ?? (layout === "band" ? "column" : "line"))
		: "none";

	const focus =
		interactive && spot.focus !== null
			? series.findIndex((s) => s.label === spot.focus)
			: -1;
	const focusIndex = focus >= 0 ? focus : null;

	const ctx = useMemo<CartesianContext | null>(() => {
		if (width === null || width < 24) return null;
		const plot = plotBox({
			width,
			height,
			axes,
			tickLabels: scale.ticks.map(tickFmt),
			layout,
		});
		return {
			uid,
			width,
			height,
			plot,
			scale,
			layout,
			count,
			band: bandWidth(count, plot, layout),
			baseline: plot.bottom,
			cell,
			solid: forced || cell < MIN_DITHER_CELL,
			matrix,
			bloom,
			entrance,
			xAt: (i) => xAtMath(i, count, plot, layout),
			yAt: yScaleFor(plot, scale),
			values,
			colors: css,
			focus: focusIndex,
			opacityOf: (i) => opacityFor(focusIndex, i),
		};
	}, [
		width,
		height,
		axes,
		scale,
		tickFmt,
		layout,
		uid,
		count,
		cell,
		forced,
		matrix,
		bloom,
		entrance,
		values,
		css,
		focusIndex,
	]);

	const marks = useMemo(
		() => (ctx && children ? children(ctx) : null),
		[ctx, children],
	);

	const xLabel = (i: number) => {
		const d = data[i];
		return d === undefined ? "" : String(read(d, x));
	};
	const rowsAt = (i: number): readonly CartesianReadoutRow[] =>
		readout?.(i) ??
		series.map((s, k) => ({
			label: s.label,
			color: css[k] ?? "",
			value: fmt(values[k]?.[i] ?? 0),
		}));

	const index = scrub && scrub.index < count ? scrub.index : null;
	const showing = interactive && scrub?.visible === true && index !== null;
	const activeX = ctx && index !== null ? ctx.xAt(index) : 0;
	const points: readonly CartesianPoint[] =
		ctx && index !== null
			? (pointsAt?.(index, ctx) ??
				values.map((row, k) => ({
					seriesIndex: k,
					y: ctx.yAt(row[index] ?? 0),
				})))
			: [];
	const anchorY = ctx
		? Math.min(ctx.plot.bottom, ...points.map((p) => p.y))
		: 0;
	const activeRows = index === null ? [] : rowsAt(index);
	const live =
		showing && index !== null
			? `${xLabel(index)}: ${activeRows.map((r) => `${r.label} ${r.value}`).join(", ")}`
			: "";

	// The readout card follows the crosshair by transform, measured after layout.
	useLayoutEffect(() => {
		const el = readoutRef.current;
		if (!el || !ctx || !showing) return;
		const pos = readoutPosition(
			{ x: activeX, y: anchorY, width: ctx.width, top: ctx.plot.top },
			{ w: el.offsetWidth, h: el.offsetHeight },
		);
		el.style.transform = `translate3d(${Math.round(pos.left)}px, ${Math.round(pos.top)}px, 0)`;
	});

	useEffect(() => () => cancelAnimationFrame(frame.current), []);

	const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
		if (!interactive || !ctx || count === 0) return;
		const box = e.currentTarget.getBoundingClientRect();
		pointerX.current = e.clientX - box.left;
		if (frame.current !== 0) return;
		frame.current = requestAnimationFrame(() => {
			frame.current = 0;
			if (pointerX.current === null) return;
			const next = indexAtX(pointerX.current, count, ctx.plot, layout);
			if (next < 0) return;
			setScrub((cur) =>
				cur?.index === next && cur.visible
					? cur
					: { index: next, glide: cur?.visible === true, visible: true },
			);
		});
	};
	const onPointerLeave = () => {
		pointerX.current = null;
		cancelAnimationFrame(frame.current);
		frame.current = 0;
		setScrub(hide);
	};
	const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
		const target = scrubKeyTarget(e.key, index, count);
		if (target === undefined) return;
		if (target === null) {
			setScrub(hide);
			spot.clear();
			return;
		}
		e.preventDefault();
		// The page may bind the arrows too; the chart owns them while focused.
		e.stopPropagation();
		setScrub({ index: target, glide: false, visible: true });
	};
	const onFocus = () =>
		setScrub((cur) =>
			cur?.visible === true
				? cur
				: { index: cur?.index ?? count - 1, glide: false, visible: true },
		);

	const stageProps = interactive
		? {
				role: "group" as const,
				"aria-label": label,
				"aria-roledescription": "chart",
				"aria-describedby": `${uid}-keys`,
				tabIndex: count > 0 ? 0 : -1,
				onPointerMove,
				onPointerLeave,
				onKeyDown,
				onFocus,
				onBlur: () => setScrub(hide),
			}
		: {
				role: "img" as const,
				"aria-label": `${label}. ${series.map((s) => s.label).join(", ")}.`,
			};

	// Row keys come from the x label; repeats get an occurrence suffix so the
	// key stays stable without leaning on the array index.
	const seenLabels = new Map<string, number>();
	const tableRows = data.map((_, i) => {
		const label = xLabel(i);
		const seen = seenLabels.get(label) ?? 0;
		seenLabels.set(label, seen + 1);
		return {
			key: seen === 0 ? label : `${label}#${seen}`,
			label,
			cells: rowsAt(i),
		};
	});

	const glide = !reduced && scrub?.glide === true;
	const move = (extra: string) =>
		({
			transition: glide
				? `transform 200ms cubic-bezier(0.22, 1, 0.36, 1), opacity 150ms ${extra}`
				: `opacity 150ms ${extra}`,
		}) as const;

	return (
		<div
			ref={rootRef}
			className={cn("relative flex min-w-0 flex-col gap-2.5", className)}
		>
			<div
				ref={stageRef}
				{...stageProps}
				className="relative touch-pan-y select-none rounded-ctl outline-none focus-visible:focus-ring"
				style={{ height }}
			>
				{ctx ? (
					<svg
						aria-hidden
						width={ctx.width}
						height={ctx.height}
						className="block overflow-visible"
					>
						{axes
							? ctx.scale.ticks.map((tick) => {
									const y = Math.round(ctx.yAt(tick)) + 0.5;
									return (
										<g key={tick}>
											{grid ? (
												<line
													x1={ctx.plot.left}
													x2={ctx.plot.right}
													y1={y}
													y2={y}
													className="stroke-line-faint"
													strokeDasharray={
														tick === ctx.scale.min ? undefined : "3 3"
													}
													shapeRendering="crispEdges"
												/>
											) : null}
											<text
												x={ctx.plot.left - 8}
												y={ctx.yAt(tick)}
												dy="0.32em"
												textAnchor="end"
												className={cn(AXIS_TEXT, "tabular-nums")}
											>
												{tickFmt(tick)}
											</text>
										</g>
									);
								})
							: null}
						{axes
							? labelIndices(count, ctx.plot.right - ctx.plot.left).map((i) => (
									<text
										key={i}
										x={ctx.xAt(i)}
										y={ctx.height - 4}
										textAnchor={labelAnchor(layout, i, count)}
										className={AXIS_TEXT}
									>
										{xLabel(i)}
									</text>
								))
							: null}
						{scrubKind === "column" ? (
							<rect
								aria-hidden
								x={-ctx.band / 2}
								y={ctx.plot.top}
								width={ctx.band}
								height={ctx.plot.bottom - ctx.plot.top}
								rx={2}
								style={{
									fill: "var(--dither-track)",
									opacity: showing ? 1 : 0,
									transform: `translateX(${activeX}px)`,
									...move("ease-out"),
								}}
							/>
						) : null}
						{marks}
						{showing && index !== null && ctx
							? activeMarks?.(index, ctx)
							: null}
						{scrubKind === "line" ? (
							<g aria-hidden>
								<line
									x1={0.5}
									x2={0.5}
									y1={ctx.plot.top}
									y2={ctx.plot.bottom}
									className="stroke-line-strong"
									strokeDasharray="2 3"
									style={{
										opacity: showing ? 1 : 0,
										transform: `translateX(${Math.round(activeX)}px)`,
										...move("ease-out"),
									}}
								/>
								{series.map((s, k) => {
									const point = points.find((p) => p.seriesIndex === k);
									return (
										<circle
											key={s.label}
											r={3.5}
											fill={css[k]}
											style={{
												stroke: "var(--dither-paper)",
												strokeWidth: 2,
												opacity:
													showing && point ? opacityFor(focusIndex, k) : 0,
												transform: `translate(${activeX}px, ${point?.y ?? anchorY}px)`,
												...move("ease-out"),
											}}
										/>
									);
								})}
							</g>
						) : null}
					</svg>
				) : null}
				{interactive && tooltip && ctx ? (
					<div
						ref={readoutRef}
						aria-hidden
						className="pointer-events-none absolute top-0 left-0 z-10 [&>*]:static"
						style={{
							opacity: showing ? 1 : 0,
							...move("ease-out"),
						}}
					>
						{index !== null ? (
							<ChartReadout title={xLabel(index)} rows={activeRows} />
						) : null}
					</div>
				) : null}
			</div>
			{showLegend ? (
				<ChartLegend
					items={series.map((s, i) => ({
						label: s.label,
						color: css[i] ?? "",
						...(s.dashed ? { dashed: true } : {}),
					}))}
					{...(interactive
						? {
								focus: spot.focus,
								locked: spot.locked,
								onHover: spot.hover,
								onLock: spot.lock,
							}
						: {})}
				/>
			) : null}
			{interactive ? <ChartLive id={`${uid}-keys`} text={live} /> : null}
			<table className="sr-only">
				<caption>{label}</caption>
				<thead>
					<tr>
						<th scope="col">x</th>
						{(count > 0 ? rowsAt(0) : []).map((row) => (
							<th key={row.label} scope="col">
								{row.label}
							</th>
						))}
					</tr>
				</thead>
				<tbody>
					{tableRows.map((row) => (
						<tr key={row.key}>
							<th scope="row">{row.label}</th>
							{row.cells.map((cell) => (
								<td key={cell.label}>{cell.value}</td>
							))}
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
