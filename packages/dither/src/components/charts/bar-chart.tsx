import { cn } from "cn";
import {
	type KeyboardEvent,
	type PointerEvent,
	useMemo,
	useRef,
	useState,
} from "react";
import {
	barSlots,
	bodyBands,
	groupLayout,
	percentShares,
	rectPath,
	slotAt,
	stackPixels,
} from "../../lib/bars-math";
import { compact, linear, niceTicks } from "../../lib/chart-math";
import {
	type DitherFill,
	ditherGradientStops,
	useDitherFillUrls,
} from "../../lib/pattern";
import {
	MIN_DITHER_CELL,
	useElementWidth,
	useForcedColors,
} from "../../lib/prefs";
import { highlightIndex } from "./cartesian-layers";
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
	type ChartFeatureProps,
	ChartLegend,
	ChartReadout,
	type ChartSeries,
	type FillVariant,
	flatFill,
	inkFill,
	makeFormat,
	read,
	seriesCss,
	type ValueFormat,
} from "./shared";

export interface DitherBarChartProps<T> extends ChartFeatureProps {
	/** One entry per group along x, in order. */
	data: readonly T[];
	/** Each group's x label: a field or a function. */
	x: Accessor<T, string | number>;
	series: readonly [ChartSeries<T>, ...ChartSeries<T>[]];
	/** `grouped` puts the series side by side; `stacked` piles them up (first series at the bottom). Default `grouped`. */
	mode?: "grouped" | "stacked";
	/** Stack to 100% of each group. Only applies to `stacked`. */
	percent?: boolean;
	/** `grouped`, `stacked` or `percent` (stacked and scaled to 100%): one prop for `mode` + `percent`. Wins over them when set. */
	layout?: "grouped" | "stacked" | "percent";
	/**
	 * The group drawn solid with its value above it while nothing is scrubbed: an index, `"last"` or `"max"`.
	 * The scrubbed group fills in solid instead. `"none"` leaves every body dithered until scrubbed.
	 * Leave unset to keep every body dithered (no fill-in on scrub).
	 */
	highlight?: number | "last" | "max" | "none";
	/** Height in px, axes included. Default 220. */
	height?: number;
	/** Body texture. Default `gradient`: densest at the cap, thinning toward the base. */
	fill?: FillVariant;
	/** Space between groups in px. Default: grows with the group width. */
	gap?: number;
	/** Value and category axes. Default true. */
	axes?: boolean;
	/** Legend under the chart. Default: two or more series. */
	legend?: boolean;
	/** Hover / keyboard readout. Default true. */
	tooltip?: boolean;
	/** Axis, readout and table number format. Default compact (1.2K). */
	format?: ValueFormat;
	/** Dither cell in px. Default 3. */
	cell?: number;
	/** Tone at the cap, 0–1. Default 0.78. */
	level?: number;
	/** Accessible name; the summary and a hidden table follow it. */
	label: string;
	className?: string;
}

const MARGIN_AXES = { top: 8, right: 8, bottom: 20, left: 40 };
const MARGIN_BARE = { top: 3, right: 0, bottom: 0, left: 0 };
/** Tone bands in a gradient body. */
const BANDS = 6;
/** Tone at the far end of a body's fade. */
const FLOOR_TONE = 0.1;
/** Cap height. A bar or segment is never thinner than this. */
const CAP_H = 2;
/** Gap between side-by-side bars in a group. */
const INNER_GAP = 2;
/** Space one x label needs, px. */
const LABEL_W = 46;
const TOTAL_COLOR = "var(--color-ink-3)";

interface BarRect {
	/** Group index. */
	g: number;
	x: number;
	y: number;
	w: number;
	h: number;
}

/**
 * Vertical bars with a dithered body and a solid cap. The body is densest at
 * the cap and thins toward the base; in `stacked` mode every segment has its
 * own cap, which is what keeps four stacked series apart. Plain SVG: the
 * bodies of one series are a handful of paths (one per tone band) so a chart
 * of hundreds of bars stays a few DOM nodes. Text is solid; under forced
 * colours (or a cell below 1.5 px) bodies fall back to a plain tint.
 */
export function DitherBarChart<T>({
	data,
	x,
	series,
	mode: modeProp = "grouped",
	percent: percentProp = false,
	layout,
	highlight,
	height = 220,
	fill = "gradient",
	gap,
	axes = true,
	legend,
	tooltip = true,
	format,
	cell = 3,
	level = 0.78,
	colors,
	bloom = "off",
	interactive = true,
	animate = true,
	matrix,
	legendAction = "spotlight",
	label,
	className,
}: DitherBarChartProps<T>) {
	const rootRef = useRef<HTMLDivElement>(null);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const solid = forced || cell < MIN_DITHER_CELL;
	const legendState = useLegendState(series.length, legendAction);
	const hidden = legendState.hidden;
	const entrance = useEntrance(rootRef, animate);
	const uid = useChartUid();
	const live = interactive && tooltip;
	const [active, setActive] = useState<number | null>(null);
	const fmt = makeFormat(format, compact);
	const m = axes ? MARGIN_AXES : MARGIN_BARE;
	const showLegend = legend ?? series.length > 1;
	const mode =
		layout === undefined
			? modeProp
			: layout === "grouped"
				? "grouped"
				: "stacked";
	const percent = layout === undefined ? percentProp : layout === "percent";
	const asPercent = percent && mode === "stacked";

	const variant: FillVariant = solid ? "solid" : fill;
	const flat = variant === "dotted" || variant === "hatched" ? variant : null;
	const layered = variant === "gradient";
	const tones = useMemo(
		() => ditherGradientStops(level, FLOOR_TONE, BANDS),
		[level],
	);

	const shown = useMemo(
		() =>
			series
				.map((s, i) => ({ ...s, index: i, css: seriesCss(s.color, colors, i) }))
				.filter((s) => !hidden.has(s.label)),
		[series, hidden, colors],
	);

	const model = useMemo(() => {
		const n = data.length;
		// Negative or missing values are drawn as 0: bars grow from the baseline only.
		const raw = shown.map((s) =>
			data.map((d) => Math.max(0, Number(read(d, s.value)) || 0)),
		);
		const columns = Array.from({ length: n }, (_, g) =>
			raw.map((row) => row[g] ?? 0),
		);
		const totals = columns.map((col) => col.reduce((sum, v) => sum + v, 0));
		const values = asPercent
			? shown.map((_, k) => columns.map((col) => percentShares(col)[k] ?? 0))
			: raw;
		const max = asPercent
			? 100
			: mode === "stacked"
				? Math.max(0, ...totals)
				: Math.max(0, ...raw.flat());
		const ticks = niceTicks(0, max || 1, 4);
		const peaks = Array.from({ length: n }, (_, g) =>
			Math.max(0, ...raw.map((row) => row[g] ?? 0)),
		);
		return { n, raw, values, totals, peaks, ticks, top: ticks.at(-1) ?? 1 };
	}, [data, shown, mode, asPercent]);

	const pinned =
		highlight === undefined || highlight === "none"
			? null
			: highlightIndex(
					highlight,
					mode === "stacked" ? model.totals : model.peaks,
				);
	const solidGroup =
		highlight === undefined
			? null
			: active !== null && active < data.length
				? active
				: pinned;
	const geometry = useMemo(() => {
		if (width === null || width < 24) return null;
		const plotW = Math.max(1, width - m.left - m.right);
		const plotH = Math.max(1, height - m.top - m.bottom);
		const baseline = m.top + plotH;
		const layout = groupLayout(model.n, plotW, gap);
		const yScale = linear([0, model.top], [baseline, m.top]);
		const pxPerUnit = plotH / model.top;
		const count = model.values.length;
		const rects: BarRect[][] = model.values.map(() => []);
		for (let g = 0; g < model.n; g += 1) {
			const slots = barSlots(
				m.left + layout.x(g),
				layout.width,
				count,
				mode,
				INNER_GAP,
			);
			if (mode === "stacked") {
				const spans = stackPixels(
					model.values.map((row) => row[g] ?? 0),
					pxPerUnit,
					CAP_H,
				);
				spans.forEach((span, k) => {
					const slot = slots[k];
					const h = span.to - span.from;
					if (!slot || h <= 0) return;
					rects[k]?.push({
						g,
						x: slot.x,
						w: slot.w,
						y: baseline - span.to,
						h,
					});
				});
			} else {
				model.values.forEach((row, k) => {
					const v = row[g] ?? 0;
					const slot = slots[k];
					if (!slot || v <= 0) return;
					const h = Math.max(CAP_H, v * pxPerUnit);
					rects[k]?.push({ g, x: slot.x, w: slot.w, y: baseline - h, h });
				});
			}
		}
		const depth = Math.min(110, plotH * 0.8);
		const bodies = rects.map((list) => {
			const body = list.filter((r) => r.g !== solidGroup);
			if (!layered) {
				return [
					{
						id: "flat",
						d: body.map((r) => rectPath(r.x, r.y, r.w, r.h)).join(""),
					},
				];
			}
			const buckets: string[][] = tones.map(() => []);
			for (const r of body) {
				for (const band of bodyBands(r.h, depth, tones)) {
					buckets[band.i]?.push(rectPath(r.x, r.y + band.y, r.w, band.h));
				}
			}
			return buckets.map((paths, i) => ({
				id: `band-${i}`,
				d: paths.join(""),
			}));
		});
		const solids = rects.map((list) =>
			list
				.filter((r) => r.g === solidGroup)
				.map((r) => rectPath(r.x, r.y, r.w, r.h))
				.join(""),
		);
		const glows = rects.map((list) =>
			list.map((r) => rectPath(r.x, r.y, r.w, r.h)).join(""),
		);
		return {
			plotW,
			plotH,
			baseline,
			layout,
			yScale,
			rects,
			bodies,
			solids,
			glows,
		};
	}, [width, height, m, model, gap, mode, layered, tones, solidGroup]);

	const fills = useMemo<DitherFill[]>(() => {
		if (variant === "solid") return [];
		return shown.flatMap((s) =>
			flat === null
				? tones.map((tone) => inkFill(tone, s.css, cell, matrix))
				: [flatFill(flat, s.css, cell, matrix)],
		);
	}, [variant, flat, shown, tones, cell, matrix]);
	const urls = useDitherFillUrls(fills);
	const perSeries = flat === null ? tones.length : 1;
	const bodyFill = (k: number, i: number, css: string) =>
		variant === "solid"
			? `color-mix(in oklab, ${css} 30%, transparent)`
			: urls[k * perSeries + i];

	const labelStride = geometry
		? Math.max(1, Math.ceil(LABEL_W / geometry.layout.step))
		: 1;

	const xLabel = (i: number) => {
		const d = data[i];
		return d === undefined ? "" : String(read(d, x));
	};
	const current = active !== null && active < data.length ? active : null;

	const setFromX = (clientX: number, el: Element) => {
		if (!geometry || data.length === 0) return;
		const box = el.getBoundingClientRect();
		setActive(
			slotAt(clientX - box.left - m.left, geometry.layout.step, data.length),
		);
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

	const groupLeft =
		geometry && current !== null ? m.left + geometry.layout.step * current : 0;
	const readoutRows = () => {
		if (current === null) return [];
		const rows = shown.map((s, k) => {
			const value = model.raw[k]?.[current] ?? 0;
			const share = model.values[k]?.[current] ?? 0;
			return {
				label: s.label,
				color: s.css,
				value: asPercent ? `${fmt(value)} · ${Math.round(share)}%` : fmt(value),
			};
		});
		if (mode !== "stacked") return rows;
		// Top segment first: the readout reads like the stack looks.
		rows.reverse();
		if (shown.length > 1) {
			rows.push({
				label: "Total",
				color: TOTAL_COLOR,
				value: fmt(model.totals[current] ?? 0),
			});
		}
		return rows;
	};

	const liveText =
		current === null
			? ""
			: `${xLabel(current)}: ${readoutRows()
					.map((r) => `${r.label} ${r.value}`)
					.join(", ")}`;
	// Value above the pinned (highlighted) group while nothing is scrubbed.
	const pinnedLabels: { key: string; x: number; y: number; text: string }[] =
		geometry && pinned !== null && current === null
			? mode === "stacked"
				? (() => {
						const parts = geometry.rects.flatMap((list) =>
							list.filter((r) => r.g === pinned),
						);
						const first = parts[0];
						if (!first) return [];
						return [
							{
								key: "total",
								x: first.x + first.w / 2,
								y: Math.min(...parts.map((r) => r.y)),
								text: fmt(model.totals[pinned] ?? 0),
							},
						];
					})()
				: shown.flatMap((s, k) => {
						const r = geometry.rects[k]?.find((rect) => rect.g === pinned);
						return r
							? [
									{
										key: s.label,
										x: r.x + r.w / 2,
										y: r.y,
										text: fmt(model.raw[k]?.[pinned] ?? 0),
									},
								]
							: [];
					})
			: [];
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
												{asPercent ? `${tick}%` : fmt(tick)}
											</text>
										</g>
									);
								})
							: null}
						{axes
							? Array.from({ length: data.length }, (_, i) => i)
									.filter((i) => i % labelStride === 0)
									.map((i) => {
										const cx =
											m.left +
											geometry.layout.step * i +
											geometry.layout.step / 2;
										return (
											<text
												key={i}
												x={cx}
												y={height - 4}
												textAnchor={
													cx + LABEL_W / 2 > width
														? "end"
														: cx - LABEL_W / 2 < 0
															? "start"
															: "middle"
												}
												className={AXIS_TEXT}
											>
												{xLabel(i)}
											</text>
										);
									})
							: null}
						{live && current !== null ? (
							<rect
								aria-hidden
								x={groupLeft}
								y={m.top}
								width={geometry.layout.step}
								height={geometry.plotH}
								rx={2}
								style={{ fill: "var(--dither-track)" }}
							/>
						) : null}
						{shown.map((s, k) => (
							<g
								key={s.label}
								aria-hidden
								style={dimStyle(legendState.opacityOf(s.label))}
							>
								<Bloom bloom={bloom} box={{ width, height }}>
									<path d={geometry.glows[k] ?? ""} fill={s.css} />
								</Bloom>
								<Entrance
									plan={entrance}
									kind="rise"
									originY={geometry.baseline}
									delay={k * 80}
								>
									{geometry.bodies[k]?.map((body, i) =>
										body.d ? (
											<path
												key={body.id}
												d={body.d}
												fill={bodyFill(k, i, s.css)}
											/>
										) : null,
									)}
									{geometry.solids[k] ? (
										<path d={geometry.solids[k]} fill={s.css} />
									) : null}
									{geometry.rects[k]?.map((r) => {
										const capH = Math.min(CAP_H, r.h);
										return (
											<rect
												key={r.g}
												x={r.x}
												y={r.y}
												width={r.w}
												height={capH}
												rx={Math.min(2, r.w / 2)}
												ry={capH / 2}
												fill={s.css}
											/>
										);
									})}
								</Entrance>
							</g>
						))}
						{pinnedLabels.map((l) => (
							<text
								key={l.key}
								x={l.x}
								y={l.y - 6}
								textAnchor="middle"
								aria-hidden
								className="fill-ink-1 font-mono text-[10px] tabular-nums"
							>
								{l.text}
							</text>
						))}
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
				{live && geometry && current !== null && width !== null ? (
					<ChartReadout
						title={xLabel(current)}
						rows={readoutRows()}
						style={
							groupLeft + geometry.layout.step + 8 + 150 > width
								? { right: width - groupLeft + 8, top: m.top }
								: { left: groupLeft + geometry.layout.step + 8, top: m.top }
						}
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
								<td key={s.label}>
									{fmt(Math.max(0, Number(read(d, s.value)) || 0))}
								</td>
							))}
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
