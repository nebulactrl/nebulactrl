import { useMemo } from "react";
import {
	bandEdges,
	bodyBands,
	markerIndices,
	markerStride,
	rectPath,
	splitRuns,
} from "../../lib/bars-math";
import {
	linePath,
	type Pt,
	regionPath,
	sampleCurve,
} from "../../lib/chart-math";
import {
	type DitherFill,
	ditherGradientStops,
	useDitherFillUrls,
} from "../../lib/pattern";
import { AreaFill } from "./area-fill";
import type { CartesianContext } from "./cartesian-chart";
import { barSlotsInBand } from "./cartesian-math";
import { Bloom, Entrance } from "./chart-kit";
import {
	type ChartBloom,
	type ChartColor,
	type FillVariant,
	flatFill,
	inkFill,
	resolveColor,
} from "./shared";

/**
 * Layers for `DitherCartesianChart`: each draws one series on the chart's
 * shared scales (`ctx`), so an area, bars and a line can sit in one plot.
 * They are plain SVG, dim while another series is spotlit and play the
 * chart's entrance once.
 */

/** The curve a layer draws; `smooth` is a monotone curve, an alias of `monotone`. */
export type LayerCurve = "smooth" | "monotone" | "linear";
const curveOf = (curve: LayerCurve | undefined): "monotone" | "linear" =>
	curve === "linear" ? "linear" : "monotone";

const dim = (ctx: CartesianContext, index: number) => ({
	opacity: ctx.opacityOf(index),
	transition: "opacity 150ms ease-out",
});

/* ---------- area ---------- */

export interface DitherAreaLayerProps {
	ctx: CartesianContext;
	/** Series index: its colour, dimming and entrance delay. */
	index: number;
	/** The line's values. Default: the series' own values. */
	values?: readonly number[];
	/** The lower edge (the layer beneath in a stack). Default: the baseline. */
	lower?: readonly number[];
	fill?: FillVariant;
	/** Tone just under the line, 0–1. Default 0.72. */
	level?: number;
	curve?: LayerCurve;
	/** Draw the solid line on top. Default true. */
	line?: boolean;
	/** Glow behind the fill. Default the chart's `bloom`. */
	bloom?: ChartBloom;
}

/** A line over a dithered fill that is densest at the line. */
export function DitherAreaLayer({
	ctx,
	index,
	values,
	lower,
	fill = "gradient",
	level = 0.72,
	curve,
	line = true,
	bloom,
}: DitherAreaLayerProps) {
	const color = ctx.colors[index] ?? resolveColor(undefined, index);
	const kind = curveOf(curve);
	const top = values ?? ctx.values[index] ?? [];
	const geometry = useMemo(() => {
		const at = (row: readonly number[]): Pt[] =>
			row.map((v, i) => ({ x: ctx.xAt(i), y: ctx.yAt(v) }));
		return {
			top: sampleCurve(at(top), kind, 3),
			bottom: lower ? sampleCurve(at(lower), kind, 3) : ctx.baseline,
		};
	}, [ctx, top, lower, kind]);
	if (geometry.top.length < 2) return null;
	return (
		<g aria-hidden style={dim(ctx, index)}>
			<Bloom bloom={bloom ?? ctx.bloom} box={ctx}>
				<path d={regionPath(geometry.top, geometry.bottom)} fill={color} />
			</Bloom>
			<Entrance plan={ctx.entrance} kind="wipe" delay={index * 90}>
				<Entrance plan={ctx.entrance} kind="fade" delay={120 + index * 90}>
					<AreaFill
						top={geometry.top}
						bottom={geometry.bottom}
						color={color}
						variant={fill}
						cell={ctx.cell}
						depth={Math.min(110, (ctx.baseline - ctx.plot.top) * 0.8)}
						floor={ctx.baseline}
						level={level}
						solid={ctx.solid}
						{...(ctx.matrix ? { matrix: ctx.matrix } : {})}
					/>
				</Entrance>
				{line ? (
					<path
						d={linePath(geometry.top)}
						fill="none"
						stroke={color}
						strokeWidth={1.5}
						strokeLinejoin="round"
						strokeLinecap="round"
					/>
				) : null}
			</Entrance>
		</g>
	);
}

/* ---------- line ---------- */

export interface DitherLineLayerProps {
	ctx: CartesianContext;
	index: number;
	/** The line's values; `null` / `undefined` lift the line. Default: the series' own values. */
	values?: readonly (number | null | undefined)[];
	curve?: LayerCurve;
	/** A dashed reference line (a previous period, a target). */
	dashed?: boolean;
	/** Dots: none, every point (thinned when crowded) or the last. Default `last`. */
	markers?: "none" | "all" | "last";
	/** A dithered band between two readings, such as p10…p90. */
	range?: {
		low: readonly (number | null | undefined)[];
		high: readonly (number | null | undefined)[];
		color?: ChartColor;
	};
	/** Texture of the range band. Default `gradient`: one even level. */
	fill?: FillVariant;
	/** Coverage of the range band, 0–1. Default 0.25. */
	level?: number;
	bloom?: ChartBloom;
}

const PAPER_RING = { stroke: "var(--dither-paper)", strokeWidth: 2 } as const;

/** A crisp 2 px line with optional dithered range band, dash and markers. */
export function DitherLineLayer({
	ctx,
	index,
	values,
	curve,
	dashed = false,
	markers = "last",
	range,
	fill = "gradient",
	level = 0.25,
	bloom,
}: DitherLineLayerProps) {
	const color = ctx.colors[index] ?? resolveColor(undefined, index);
	const bandColor = range?.color ? resolveColor(range.color) : color;
	const kind = curveOf(curve);
	const row = values ?? ctx.values[index] ?? [];
	const geometry = useMemo(() => {
		const pointAt = (i: number, v: number): Pt => ({
			x: ctx.xAt(i),
			y: ctx.yAt(v),
		});
		const runs = splitRuns(row);
		const lines = runs
			.filter((run) => run.values.length > 1)
			.map((run) =>
				sampleCurve(
					run.values.map((v, k) => pointAt(run.start + k, v)),
					kind,
					3,
				),
			);
		const dots = runs
			.filter((run) => run.values.length === 1)
			.map((run) => pointAt(run.start, run.values[0] ?? 0));
		const edges = range ? bandEdges(range.low, range.high) : null;
		const bands = edges
			? splitRuns(edges.high)
					.filter((run) => run.values.length > 1)
					.map((run) => {
						const upper = run.values.map((v, k) => pointAt(run.start + k, v));
						const lowerPts = run.values.map((_, k) =>
							pointAt(run.start + k, edges.low[run.start + k] ?? 0),
						);
						return regionPath(
							sampleCurve(upper, kind, 3),
							sampleCurve(lowerPts, kind, 3),
						);
					})
			: [];
		const step =
			row.length > 1 ? (ctx.plot.right - ctx.plot.left) / (row.length - 1) : 0;
		const marks = markerIndices(row, markers, markerStride(step)).map((i) =>
			pointAt(i, row[i] ?? 0),
		);
		return { lines, dots, bands, marks };
	}, [ctx, row, kind, range, markers]);

	const bandFills = useMemo<DitherFill[]>(() => {
		if (ctx.solid || fill === "solid") return [];
		if (fill === "gradient")
			return [inkFill(level, bandColor, ctx.cell, ctx.matrix)];
		return [flatFill(fill, bandColor, ctx.cell, ctx.matrix)];
	}, [ctx.solid, ctx.cell, ctx.matrix, fill, level, bandColor]);
	const bandUrl = useDitherFillUrls(bandFills)[0];

	return (
		<g aria-hidden style={dim(ctx, index)}>
			{geometry.bands.length > 0 ? (
				<>
					<Bloom bloom={bloom ?? ctx.bloom} box={ctx}>
						{geometry.bands.map((d) => (
							<path key={d} d={d} fill={bandColor} />
						))}
					</Bloom>
					<Entrance plan={ctx.entrance} kind="fade" delay={200 + index * 90}>
						{geometry.bands.map((d) => (
							<path
								key={d}
								d={d}
								fill={
									bandUrl ??
									`color-mix(in oklab, ${bandColor} 22%, transparent)`
								}
							/>
						))}
					</Entrance>
				</>
			) : null}
			<Entrance
				plan={ctx.entrance}
				kind={dashed ? "fade" : "wipe"}
				delay={index * 90}
			>
				{geometry.lines.map((pts) => (
					<path
						key={pts[0]?.x}
						d={linePath(pts)}
						fill="none"
						stroke={color}
						strokeWidth={dashed ? 1.5 : 2}
						strokeLinejoin="round"
						strokeLinecap={dashed ? "butt" : "round"}
						strokeDasharray={dashed ? "5 4" : undefined}
					/>
				))}
				{geometry.dots.map((p) => (
					<circle key={p.x} cx={p.x} cy={p.y} r={2} fill={color} />
				))}
				{geometry.marks.map((p) => (
					<circle
						key={p.x}
						cx={p.x}
						cy={p.y}
						r={3}
						fill={color}
						style={PAPER_RING}
					/>
				))}
			</Entrance>
		</g>
	);
}

/* ---------- bars ---------- */

export interface DitherBarLayerProps {
	ctx: CartesianContext;
	index: number;
	/** Bar tops. Default: the series' own values (negatives draw as 0). */
	values?: readonly number[];
	/** Bar bottoms, for a stack. Default: the scale's floor. */
	lower?: readonly number[];
	/** Which of `count` side-by-side bars this series is. Default `{ index: 0, count: 1 }`: a full-width bar (use for stacks). */
	slot?: { index: number; count: number };
	fill?: FillVariant;
	/** Tone at the cap, 0–1. Default 0.75. */
	level?: number;
	/** The bar drawn solid (an index), `"last"` or `"max"`; `"none"` (default) leaves every body dithered. */
	highlight?: number | "last" | "max" | "none";
	/** Text above the highlighted bar. */
	highlightLabel?: (datumIndex: number) => string;
	/** Bar width cap in px. Default 36. */
	maxWidth?: number;
	bloom?: ChartBloom;
}

const CAP_H = 2;
const FLOOR_TONE = 0.1;
const BANDS = 6;

/** Which datum a `highlight` setting points at (the tallest, the last, a fixed index, or none). */
export function highlightIndex(
	highlight: DitherBarLayerProps["highlight"],
	values: readonly number[],
): number | null {
	if (highlight === undefined || highlight === "none") return null;
	if (typeof highlight === "number") {
		return highlight >= 0 && highlight < values.length ? highlight : null;
	}
	if (values.length === 0) return null;
	if (highlight === "last") return values.length - 1;
	let best = 0;
	values.forEach((v, i) => {
		if (v > (values[best] ?? 0)) best = i;
	});
	return best;
}

/** Dithered bars under a solid 2 px cap, rising from the baseline (or a stack's lower edge). */
export function DitherBarLayer({
	ctx,
	index,
	values,
	lower,
	slot = { index: 0, count: 1 },
	fill = "gradient",
	level = 0.75,
	highlight = "none",
	highlightLabel,
	maxWidth = 36,
	bloom,
}: DitherBarLayerProps) {
	const color = ctx.colors[index] ?? resolveColor(undefined, index);
	const row = values ?? ctx.values[index] ?? [];
	const variant: FillVariant = ctx.solid ? "solid" : fill;
	const layered = variant === "gradient";
	const tones = useMemo(
		() => ditherGradientStops(level, FLOOR_TONE, BANDS),
		[level],
	);
	const marked = highlightIndex(highlight, row);

	const geometry = useMemo(() => {
		const rects = row.flatMap((raw, i) => {
			const top = Math.max(0, Number.isFinite(raw) ? raw : 0);
			const base = lower?.[i] ?? Math.max(0, ctx.scale.min);
			const y0 = ctx.yAt(base);
			const y1 = ctx.yAt(base + top);
			if (top <= 0) return [];
			const h = Math.max(CAP_H, y0 - y1);
			const slots = barSlotsInBand(
				ctx.xAt(i),
				ctx.band,
				slot.count,
				"grouped",
				{
					maxWidth,
				},
			);
			const s = slots[slot.index];
			return s ? [{ i, x: s.x, w: s.w, y: y0 - h, h }] : [];
		});
		const depth = Math.min(110, (ctx.baseline - ctx.plot.top) * 0.8);
		const body = layered
			? tones.map((_, t) =>
					rects
						.filter((r) => r.i !== marked)
						.flatMap((r) =>
							bodyBands(r.h, depth, tones)
								.filter((b) => b.i === t)
								.map((b) => rectPath(r.x, r.y + b.y, r.w, b.h)),
						)
						.join(""),
				)
			: [
					rects
						.filter((r) => r.i !== marked)
						.map((r) => rectPath(r.x, r.y, r.w, r.h))
						.join(""),
				];
		const solidBody = rects
			.filter((r) => r.i === marked)
			.map((r) => rectPath(r.x, r.y, r.w, r.h))
			.join("");
		const glow = rects.map((r) => rectPath(r.x, r.y, r.w, r.h)).join("");
		return { rects, body, solidBody, glow };
	}, [
		ctx,
		row,
		lower,
		slot.count,
		slot.index,
		maxWidth,
		layered,
		tones,
		marked,
	]);

	const fills = useMemo<DitherFill[]>(() => {
		if (variant === "solid") return [];
		if (variant === "gradient") {
			return tones.map((tone) => inkFill(tone, color, ctx.cell, ctx.matrix));
		}
		return [flatFill(variant, color, ctx.cell, ctx.matrix)];
	}, [variant, tones, color, ctx.cell, ctx.matrix]);
	const urls = useDitherFillUrls(fills);

	const markedRect = geometry.rects.find((r) => r.i === marked);
	return (
		<g aria-hidden style={dim(ctx, index)}>
			<Bloom bloom={bloom ?? ctx.bloom} box={ctx}>
				<path d={geometry.glow} fill={color} />
			</Bloom>
			<Entrance
				plan={ctx.entrance}
				kind="rise"
				originY={ctx.baseline}
				delay={index * 80}
			>
				{geometry.body.map((d, t) =>
					d ? (
						<path
							// biome-ignore lint/suspicious/noArrayIndexKey: tone bands are an ordered, fixed-length stack
							key={t}
							d={d}
							fill={
								variant === "solid"
									? `color-mix(in oklab, ${color} 30%, transparent)`
									: urls[t]
							}
						/>
					) : null,
				)}
				{geometry.solidBody ? (
					<path d={geometry.solidBody} fill={color} />
				) : null}
				{geometry.rects.map((r) => (
					<rect
						key={r.i}
						x={r.x}
						y={r.y}
						width={r.w}
						height={Math.min(CAP_H, r.h)}
						rx={Math.min(2, r.w / 2)}
						fill={color}
					/>
				))}
			</Entrance>
			{markedRect && highlightLabel ? (
				<text
					x={markedRect.x + markedRect.w / 2}
					y={markedRect.y - 6}
					textAnchor="middle"
					className="fill-ink-1 font-mono text-[10px] tabular-nums"
				>
					{highlightLabel(markedRect.i)}
				</text>
			) : null}
		</g>
	);
}
