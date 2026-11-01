import { cn } from "cn";
import {
	type KeyboardEvent,
	type PointerEvent,
	useId,
	useMemo,
	useRef,
	useState,
} from "react";
import { clamp, compact } from "../../lib/chart-math";
import { type DitherFill, useDitherFillUrls } from "../../lib/pattern";
import {
	MIN_DITHER_CELL,
	useElementWidth,
	useForcedColors,
	useReducedMotionPref,
} from "../../lib/prefs";
import {
	labelAnchor,
	polygonPath,
	radarAngle,
	radarMax,
	radarPolygon,
	radarRing,
	radarValues,
	radarVertex,
} from "../../lib/radial-math";
import {
	Bloom,
	ChartLive,
	Grow,
	useChartUid,
	useEntrance,
	useLegendState,
} from "./chart-kit";
import { axisAtOffset, circlePath, ringBandTones } from "./radar-options";
import {
	AXIS_TEXT,
	type ChartBloom,
	type ChartColor,
	ChartLegend,
	ChartReadout,
	type DitherMatrix,
	type FillVariant,
	flatFill,
	inkFill,
	makeFormat,
	seriesCss,
	type ValueFormat,
} from "./shared";

export interface RadarSeries {
	label: string;
	/** One number per axis, in axis order; or an object keyed by axis name. */
	values: readonly number[] | Readonly<Record<string, number>>;
	/** Name (`"green"`), CSS colour or `var(--token)`. Default: `colors`, then the series colours in order. */
	color?: ChartColor;
	/** This series' texture. It wins over the chart's `fill`. */
	fill?: FillVariant;
}

export interface DitherRadarChartProps {
	/** Axis names, unique, 3–10 (more are ignored). Axis 0 points up, the rest follow clockwise. */
	axes: readonly string[];
	/** One to three series work best. */
	series: readonly [RadarSeries, ...RadarSeries[]];
	/** Value at the outer ring. Default: the data's largest, rounded up to a round number. */
	max?: number;
	/** Grid rings, 3–5. They also set the steps of the `gradient` fill. Default 4. */
	rings?: number;
	/**
	 * `polygon` (alias `line`): hairline polygon rings. `circle`: hairline round rings.
	 * `dither`: polygon rings also filled, denser toward the centre. Default `line`.
	 */
	grid?: "line" | "dither" | "polygon" | "circle";
	/**
	 * Texture of every series: `gradient` (density steps up ring by ring), `dotted`, `hatched` or `solid`.
	 * Leave unset for the flat dither. A series' own `fill` wins.
	 */
	fill?: FillVariant;
	/** Soft glow in each series' colour behind it. Default `off`. */
	bloom?: ChartBloom;
	/** Colours by series position: names or CSS colours. A series' own `color` wins. */
	colors?: readonly ChartColor[];
	/** Diameter of the box in px, labels included; shrinks to fit. Default 280. */
	size?: number;
	/** Dither cell in px. Default 2. */
	cell?: number;
	/** Value format for the scale, readout and table. Default compact. */
	format?: ValueFormat;
	/** Legend under the chart. Default: two or more series. */
	legend?: boolean;
	/** The axis scrub, keyboard and legend spotlight. `false` draws a static image. Default true. */
	interactive?: boolean;
	/** Grow out from the centre the first time the chart is seen. Always off under reduced motion. Default true. */
	animate?: boolean;
	/** Threshold matrix of the fills: Bayer 2/4/8 or blue noise. Default `bayer4`. */
	matrix?: DitherMatrix;
	/** What a legend click does: `spotlight` keeps a series lit, `toggle` hides it. Default `spotlight`. */
	legendAction?: "spotlight" | "toggle";
	/** Accessible name; a hidden table follows it. */
	label: string;
	className?: string;
}

const MAX_AXES = 10;
const SERIES_TONE = 0.35;
/** Grid fill tone at the outermost ring; each ring inward adds `GRID_STEP`. */
const GRID_BASE = 0.05;
const GRID_STEP = 0.06;

/** How a series is painted: its own pattern(s), per ring band, or a flat tint. */
interface SeriesPlan {
	kind: "flat" | "bands" | "solid";
	fills: DitherFill[];
}

/**
 * A spider chart: a polygon per series over regular rings. Each polygon is a
 * flat dither (tone 0.35, or a texture) with a crisp 1.5 px outline and a dot
 * per vertex; axis names and the ring scale are plain text. Scrub across the
 * axes (pointer or Left/Right) to read every series on one axis; hover a
 * polygon, press Up/Down or use the legend to pick a series and dim the
 * others. Under forced colours, or a cell below 1.5 px, the dither fill is a
 * plain low-opacity tint.
 */
export function DitherRadarChart({
	axes: axesProp,
	series,
	max: maxProp,
	rings: ringsProp = 4,
	grid = "line",
	fill,
	bloom = "off",
	colors,
	size = 280,
	cell = 2,
	format,
	legend,
	interactive = true,
	animate = true,
	matrix,
	legendAction = "spotlight",
	label,
	className,
}: DitherRadarChartProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const svgRef = useRef<SVGSVGElement>(null);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const reduced = useReducedMotionPref();
	const entrance = useEntrance(rootRef, animate);
	const uid = useChartUid();
	const clipBase = `nc-radar-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
	const solid = forced || cell < MIN_DITHER_CELL;
	const fmt = makeFormat(format, compact);
	const legendState = useLegendState(series.length, legendAction);
	const hidden = legendState.hidden;
	const [hovered, setHovered] = useState<number | null>(null);
	const [axis, setAxis] = useState<number | null>(null);
	const showLegend = legend ?? series.length > 1;
	const rings = clamp(Math.round(ringsProp), 3, 5);
	const round = grid === "circle";
	const gridFilled = grid === "dither";

	const axes = useMemo(() => axesProp.slice(0, MAX_AXES), [axesProp]);
	const rows = useMemo(
		() =>
			series.map((s, index) => ({
				label: s.label,
				index,
				css: seriesCss(s.color, colors, index),
				fill: s.fill ?? fill,
				values: radarValues(s.values, axes),
			})),
		[series, axes, colors, fill],
	);
	const top = useMemo(
		() =>
			radarMax(
				rows.map((r) => r.values),
				maxProp,
			),
		[rows, maxProp],
	);
	const shown = useMemo(
		() => rows.filter((r) => !hidden.has(r.label)),
		[rows, hidden],
	);

	const px = Math.max(160, Math.min(size, width ?? size));
	const pad = clamp(px * 0.15, 30, 44);
	const cx = px / 2;
	const radius = px / 2 - pad;

	const geometry = useMemo(
		() => ({
			rings: Array.from({ length: rings }, (_, k) => {
				const r = (radius * (k + 1)) / rings;
				return {
					r,
					path: round
						? circlePath(cx, cx, r)
						: polygonPath(radarRing(axes.length, cx, cx, r)),
				};
			}),
			polygons: rows.map((row) => {
				const pts = radarPolygon(row.values, top, cx, cx, radius);
				return { pts, path: polygonPath(pts) };
			}),
		}),
		[rings, radius, axes.length, cx, rows, top, round],
	);

	const bandTones = useMemo(() => ringBandTones(rings), [rings]);
	const plans = useMemo<SeriesPlan[]>(
		() =>
			rows.map((row) => {
				if (row.fill === undefined) {
					return {
						kind: "flat",
						fills: [inkFill(SERIES_TONE, row.css, cell, matrix)],
					};
				}
				if (row.fill === "solid") return { kind: "solid", fills: [] };
				if (row.fill === "gradient") {
					return {
						kind: "bands",
						fills: bandTones.map((tone) =>
							inkFill(tone, row.css, cell, matrix),
						),
					};
				}
				return {
					kind: "flat",
					fills: [flatFill(row.fill, row.css, cell, matrix)],
				};
			}),
		[rows, bandTones, cell, matrix],
	);
	const offsets = useMemo(() => {
		let next = 0;
		return plans.map((plan) => {
			const start = next;
			next += plan.fills.length;
			return start;
		});
	}, [plans]);
	const fills = useMemo<DitherFill[]>(() => {
		if (solid) return [];
		const seriesFills = plans.flatMap((plan) => plan.fills);
		const gridFills: DitherFill[] = gridFilled
			? Array.from({ length: rings }, (_, k) =>
					inkFill(
						GRID_BASE + GRID_STEP * (rings - 1 - k),
						"var(--color-ink-4)",
						cell,
						matrix,
					),
				)
			: [];
		return [...seriesFills, ...gridFills];
	}, [solid, plans, gridFilled, rings, cell, matrix]);
	const urls = useDitherFillUrls(fills);
	const gridOffset = plans.reduce((sum, plan) => sum + plan.fills.length, 0);

	// Annulus k = the ring between ring k-1 and ring k, for a gradient's bands.
	const annuli = geometry.rings.map((ring, k) => {
		const inner = geometry.rings[k - 1];
		return `${ring.path}${inner ? inner.path : ""}`;
	});

	const focusIndex = legendState.focus
		? rows.findIndex((r) => r.label === legendState.focus)
		: -1;
	const active = interactive
		? (hovered ?? (focusIndex >= 0 ? focusIndex : null))
		: null;
	const activeRow = active === null ? undefined : rows[active];

	const onKey = (e: KeyboardEvent) => {
		if (shown.length === 0) return;
		const at = shown.findIndex((r) => r.index === active);
		const goSeries = (step: number) => {
			const next = at === -1 ? (step > 0 ? 0 : shown.length - 1) : at + step;
			setHovered(shown[(next + shown.length) % shown.length]?.index ?? null);
		};
		const goAxis = (step: number) => {
			const n = axes.length;
			const next =
				axis === null ? (step > 0 ? 0 : n - 1) : (axis + step + n) % n;
			setAxis(n > 0 ? next : null);
		};
		if (e.key === "ArrowRight") goAxis(1);
		else if (e.key === "ArrowLeft") goAxis(-1);
		else if (e.key === "ArrowDown") goSeries(1);
		else if (e.key === "ArrowUp") goSeries(-1);
		else if (e.key === "Home") setAxis(axes.length > 0 ? 0 : null);
		else if (e.key === "End") setAxis(axes.length > 0 ? axes.length - 1 : null);
		else if (e.key === "Escape") {
			setAxis(null);
			setHovered(null);
			legendState.clear();
		} else return;
		e.preventDefault();
		e.stopPropagation();
	};

	const onPointer = (e: PointerEvent<SVGSVGElement>) => {
		const box = svgRef.current?.getBoundingClientRect();
		if (!box) return;
		setAxis(
			axisAtOffset(
				e.clientX - box.left - cx,
				e.clientY - box.top - cx,
				axes.length,
			),
		);
	};

	const opacityOf = (index: number) =>
		active !== null && active !== index ? 0.3 : 1;
	// The highlighted series is drawn last so it sits on top.
	const order = [...shown].sort(
		(a, b) => Number(a.index === active) - Number(b.index === active),
	);

	const axisName = axis === null ? undefined : axes[axis];
	const readout =
		axis !== null && axisName !== undefined
			? {
					title: axisName,
					rows: shown.map((r) => ({
						label: r.label,
						color: r.css,
						value: fmt(r.values[axis] ?? 0),
					})),
				}
			: activeRow
				? {
						title: activeRow.label,
						rows: axes.map((name, i) => ({
							label: name,
							color: activeRow.css,
							value: fmt(activeRow.values[i] ?? 0),
						})),
					}
				: null;
	const liveText = readout
		? `${readout.title}: ${readout.rows.map((r) => `${r.label} ${r.value}`).join(", ")}`
		: "";

	const drawing = (
		<svg
			ref={svgRef}
			width={px}
			height={px}
			className="block overflow-visible"
			aria-hidden
			onPointerMove={interactive ? onPointer : undefined}
			onPointerLeave={
				interactive
					? () => {
							setAxis(null);
							setHovered(null);
						}
					: undefined
			}
		>
			{gridFilled && !solid
				? [...geometry.rings]
						.reverse()
						.map((ring, k) => (
							<path
								key={ring.r}
								d={ring.path}
								fill={urls[gridOffset + (rings - 1 - k)] ?? "none"}
							/>
						))
				: null}
			{geometry.rings.map((ring) => (
				<path
					key={ring.r}
					d={ring.path}
					fill="none"
					className="stroke-line-faint"
				/>
			))}
			{axes.map((name, i) => {
				const end = radarVertex(cx, cx, radius, i, axes.length);
				return (
					<line
						key={`spoke:${name}`}
						x1={cx}
						y1={cx}
						x2={end.x}
						y2={end.y}
						className={axis === i ? "stroke-line-strong" : "stroke-line-faint"}
					/>
				);
			})}
			{geometry.rings.map((ring, k) => (
				<text
					key={`scale:${ring.r}`}
					x={cx + 4}
					y={cx - ring.r + 9}
					className={AXIS_TEXT}
				>
					{fmt((top * (k + 1)) / rings)}
				</text>
			))}
			<defs>
				{annuli.map((d, k) => (
					<clipPath
						// biome-ignore lint/suspicious/noArrayIndexKey: ring annuli are an ordered, fixed-length list
						key={k}
						id={`${clipBase}-${k}`}
					>
						<path d={d} clipRule="evenodd" />
					</clipPath>
				))}
			</defs>
			<Bloom bloom={bloom} box={{ width: px, height: px }}>
				{order.map((row) => {
					const poly = geometry.polygons[row.index];
					return poly ? (
						<path key={row.label} d={poly.path} fill={row.css} />
					) : null;
				})}
			</Bloom>
			<Grow plan={entrance} cx={cx} cy={cx}>
				{order.map((row) => {
					const poly = geometry.polygons[row.index];
					const plan = plans[row.index];
					if (!poly || !plan) return null;
					const url = urls[offsets[row.index] ?? 0];
					const tint = solid || plan.kind === "solid";
					return (
						<g
							key={row.label}
							style={{ opacity: opacityOf(row.index) }}
							className={cn(
								!reduced && "transition-opacity duration-150 ease-out",
							)}
							onPointerEnter={
								interactive ? () => setHovered(row.index) : undefined
							}
							onPointerLeave={interactive ? () => setHovered(null) : undefined}
						>
							{plan.kind === "bands" && !solid ? (
								geometry.rings.map((ring, k) => (
									<path
										key={ring.r}
										d={poly.path}
										fill={urls[(offsets[row.index] ?? 0) + k] ?? row.css}
										clipPath={`url(#${clipBase}-${k})`}
									/>
								))
							) : (
								<path
									d={poly.path}
									fill={tint ? row.css : (url ?? row.css)}
									fillOpacity={tint ? (plan.kind === "solid" ? 0.3 : 0.2) : 1}
								/>
							)}
							<path
								d={poly.path}
								fill="none"
								stroke={row.css}
								strokeWidth={active === row.index ? 2 : 1.5}
								strokeLinejoin="round"
							/>
							{axes.map((name, i) => {
								const p = poly.pts[i];
								if (!p) return null;
								return (
									<circle
										key={name}
										cx={p.x}
										cy={p.y}
										r={axis === i ? 3.5 : 2.5}
										fill={row.css}
										style={{
											stroke: "var(--dither-paper)",
											strokeWidth: 1.5,
										}}
									/>
								);
							})}
						</g>
					);
				})}
			</Grow>
			{axes.map((name, i) => {
				const angle = radarAngle(i, axes.length);
				const at = radarVertex(cx, cx, radius + 10, i, axes.length);
				const up = Math.cos(angle);
				return (
					<text
						key={`axis:${name}`}
						x={at.x}
						y={at.y}
						dy={up > 0.3 ? "0" : up < -0.3 ? "0.8em" : "0.32em"}
						textAnchor={labelAnchor(angle)}
						className={cn(
							"font-mono text-[10px]",
							axis === i ? "fill-ink-1" : "fill-ink-3",
						)}
					>
						{name}
					</text>
				);
			})}
		</svg>
	);

	// A literal-role props object per mode, spread onto the root: the
	// interactive chart is a group around a keyboard-driven button, the
	// static one a labelled image.
	const rootProps = interactive
		? { role: "group" as const, "aria-label": label }
		: {
				role: "img" as const,
				"aria-label": `${label}. ${axes.join(", ")}. ${rows.map((r) => r.label).join(", ")}.`,
			};

	return (
		<div
			ref={rootRef}
			{...rootProps}
			className={cn(
				"relative flex min-w-0 flex-col gap-2.5 rounded-ctl has-[:focus-visible]:focus-ring",
				className,
			)}
		>
			<div className="relative mx-auto" style={{ width: px, height: px }}>
				{interactive ? (
					<button
						type="button"
						aria-label={`${label}. ${axes.join(", ")}. Use Left and Right to inspect axes, Up and Down to pick a series.`}
						aria-describedby={`${uid}-keys`}
						onKeyDown={onKey}
						onBlur={() => {
							setAxis(null);
							setHovered(null);
						}}
						className="block rounded-ctl outline-none"
					>
						{drawing}
					</button>
				) : (
					drawing
				)}
				{interactive && readout ? (
					<ChartReadout
						title={readout.title}
						rows={readout.rows}
						style={{ right: 0, top: 0 }}
					/>
				) : null}
			</div>
			{showLegend ? (
				<ChartLegend
					items={rows.map((r) => ({ label: r.label, color: r.css }))}
					{...(interactive ? legendState.legendProps : {})}
				/>
			) : null}
			{interactive ? <ChartLive id={`${uid}-keys`} text={liveText} /> : null}
			<table className="sr-only">
				<caption>{label}</caption>
				<thead>
					<tr>
						<th scope="col">Axis</th>
						{rows.map((r) => (
							<th key={r.label} scope="col">
								{r.label}
							</th>
						))}
					</tr>
				</thead>
				<tbody>
					{axes.map((name, i) => (
						<tr key={name}>
							<th scope="row">{name}</th>
							{rows.map((r) => (
								<td key={r.label}>{fmt(r.values[i] ?? 0)}</td>
							))}
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
