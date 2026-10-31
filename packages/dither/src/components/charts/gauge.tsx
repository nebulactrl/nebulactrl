import { cn } from "cn";
import { useLayoutEffect, useMemo, useRef } from "react";
import {
	arcPath,
	clamp,
	compact,
	niceTicks,
	polar,
} from "../../lib/chart-math";
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
import {
	type GaugeStatus,
	gaugeActive,
	gaugeBand,
	gaugeFraction,
	gaugeStatus,
	labelAnchor,
} from "../../lib/radial-math";
import { Bloom, useEntrance } from "./chart-kit";
import {
	defaultSegments,
	type GaugeShape,
	newBlocks,
	shapeAngle,
	shapeBox,
	shapeSegments,
	shapeSpan,
} from "./gauge-math";
import {
	AXIS_TEXT,
	type ChartBloom,
	type ChartColor,
	type DitherMatrix,
	type FillVariant,
	flatFill,
	inkFill,
	makeFormat,
	resolveColor,
	type ValueFormat,
} from "./shared";

export interface DitherGaugeProps {
	value: number;
	/** Value at the start of the arc. Default 0. */
	min?: number;
	/** Value at the end of the arc. Default 100. */
	max?: number;
	/** `dial` is a 270° arc opening downward; `half` is a 180° arc; `ring` is a full circle from the top (it hides the min and max labels). Default `dial`. */
	variant?: GaugeShape;
	/** Blocks around the arc. Default 24 (48 for `ring`). */
	segments?: number;
	/** Text after the value in the centre: "%", " ms". */
	unit?: string;
	/** Name of what is measured; shown under the value and used as the accessible name. */
	label: string;
	/** From here the gauge turns the warn colour and says so. `badAt` below `warnAt` makes lower worse. */
	warnAt?: number;
	/** From here the gauge turns the bad colour and says so. */
	badAt?: number;
	/** Colour while OK: name (`"green"`), CSS colour or `var(--token)`. Default series 1. */
	color?: ChartColor;
	/** Colour of the empty track, drawn faint. Default the neutral track tone. */
	trackColor?: ChartColor;
	/** Texture of the filled blocks (the block at the value is always solid). Default `gradient`: densest right behind the value. */
	fill?: FillVariant;
	/** Soft glow in the fill's colour behind the filled blocks. Default `off`. */
	bloom?: ChartBloom;
	/** Fill the blocks in one by one on mount and when the value rises. Always off under reduced motion. Default true. */
	animate?: boolean;
	/** Threshold matrix of the gradient: Bayer 2/4/8 or blue noise. Default `bayer4`. */
	matrix?: DitherMatrix;
	/** Min and max under the ends of the arc. Default false (never drawn for `ring`). */
	showRange?: boolean;
	/** Tick marks with labels around the arc: `true` for round values, or the values to mark. */
	ticks?: boolean | readonly number[];
	/** Width in px; shrinks to fit. Default 200. */
	size?: number;
	/** Dither cell in px. Default 2. */
	cell?: number;
	/** Number format for the value and ticks. Default compact. */
	format?: ValueFormat;
	className?: string;
}

/** Tone of the filled blocks right behind the value, and at the start of the arc. */
const TONE_NEAR = 0.6;
const TONE_FAR = 0.24;
const BANDS = 4;
/** Share of each block's pitch left empty. */
const GAP_SHARE = 0.22;

const STATUS_COLOR: Record<Exclude<GaugeStatus, "ok">, string> = {
	warn: "var(--color-warn)",
	bad: "var(--color-bad)",
};
const STATUS_WORD: Record<Exclude<GaugeStatus, "ok">, string> = {
	warn: "Warning",
	bad: "Critical",
};

/**
 * A radial gauge of dithered blocks. Blocks behind the value are dithered
 * (densest right behind it), the block that holds the value is solid, the
 * rest are faint track blocks. Crossing `warnAt` / `badAt` recolours the fill
 * and says so in text, with a notch on the arc where the threshold sits. The
 * value, unit and label are plain text; the element is a `meter`. Blocks fill
 * in one by one when it first appears and when the value rises. Under forced
 * colours, or a cell below 1.5 px, the bands are opacity steps.
 */
export function DitherGauge({
	value,
	min = 0,
	max = 100,
	variant = "dial",
	segments,
	unit = "",
	label,
	warnAt,
	badAt,
	color,
	trackColor,
	fill = "gradient",
	bloom = "off",
	animate = true,
	matrix,
	showRange = false,
	ticks,
	size = 200,
	cell = 2,
	format,
	className,
}: DitherGaugeProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const svgRef = useRef<SVGSVGElement>(null);
	const previous = useRef(-1);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const entrance = useEntrance(rootRef, animate);
	const solid = forced || cell < MIN_DITHER_CELL;
	const fmt = makeFormat(format, compact);

	const count = clamp(Math.round(segments ?? defaultSegments(variant)), 6, 90);
	const px = Math.max(96, Math.min(size, width ?? size));
	const showTicks = ticks !== undefined && ticks !== false;
	const rangeOn = showRange && variant !== "ring";
	const box = shapeBox(variant, px, showTicks ? 26 : rangeOn ? 18 : 6);
	const thickness = clamp(box.radius * 0.22, 9, 28);
	const inner = box.radius - thickness;

	const status = gaugeStatus(value, warnAt, badAt);
	const okColor = resolveColor(color, 0);
	const css = status === "ok" ? okColor : STATUS_COLOR[status];
	const track = trackColor
		? `color-mix(in oklab, ${resolveColor(trackColor)} 20%, transparent)`
		: "var(--dither-track)";
	const fraction = gaugeFraction(value, min, max);
	const activeIndex = gaugeActive(fraction, count);

	const paths = useMemo(
		() =>
			shapeSegments(count, variant, GAP_SHARE).map((s) =>
				arcPath(box.cx, box.cy, inner, box.radius, s.from, s.to),
			),
		[count, variant, box.cx, box.cy, inner, box.radius],
	);

	const tones = useMemo(
		() => ditherGradientStops(TONE_NEAR, TONE_FAR, BANDS),
		[],
	);
	const fills = useMemo<DitherFill[]>(() => {
		if (solid || fill === "solid") return [];
		if (fill === "gradient") {
			return tones.map((tone) => inkFill(tone, css, cell, matrix));
		}
		return [flatFill(fill, css, cell, matrix)];
	}, [solid, fill, tones, css, cell, matrix]);
	const urls = useDitherFillUrls(fills);

	const paintFor = (index: number) => {
		if (index > activeIndex) return { fill: track, opacity: 1 };
		if (index === activeIndex) return { fill: css, opacity: 1 };
		if (fill === "solid") return { fill: css, opacity: 1 };
		const band = fill === "gradient" ? gaugeBand(index, activeIndex, BANDS) : 0;
		return solid
			? { fill: css, opacity: tones[band] ?? TONE_NEAR }
			: { fill: urls[band] ?? css, opacity: 1 };
	};

	// Blocks fill in one by one: all of them on first sight, only the new ones when the value rises.
	useLayoutEffect(() => {
		if (!entrance.go) return;
		const range = newBlocks(previous.current, activeIndex);
		previous.current = activeIndex;
		const svg = svgRef.current;
		if (!range || !svg) return;
		const [from, to] = range;
		const animations: Animation[] = [];
		for (const node of svg.querySelectorAll<SVGElement>("[data-block]")) {
			const index = Number(node.dataset.block);
			if (index < from || index > to) continue;
			animations.push(
				node.animate([{ opacity: 0 }, { opacity: 1 }], {
					duration: 220,
					delay: (index - from) * 18,
					easing: "ease-out",
					fill: "backwards",
				}),
			);
		}
		return () => {
			for (const animation of animations) animation.cancel();
		};
	}, [entrance.go, activeIndex]);
	const hideFilled = entrance.armed && !entrance.go;

	const tickValues = useMemo(() => {
		if (!showTicks) return [];
		const raw: readonly number[] =
			typeof ticks === "object"
				? ticks
				: ticks === true
					? niceTicks(min, max, 4)
					: [];
		return raw.filter((v) => v >= min && v <= max);
	}, [showTicks, ticks, min, max]);

	const thresholds: { at: number; status: "warn" | "bad" }[] = [];
	if (warnAt !== undefined) thresholds.push({ at: warnAt, status: "warn" });
	if (badAt !== undefined) thresholds.push({ at: badAt, status: "bad" });

	const lowerIsWorse =
		warnAt !== undefined && badAt !== undefined && badAt < warnAt;
	const crossed =
		status === "ok" ? undefined : status === "bad" ? badAt : warnAt;
	const statusText =
		status === "ok" || crossed === undefined
			? null
			: `${STATUS_WORD[status]} ${lowerIsWorse ? "≤" : "≥"} ${fmt(crossed)}${unit}`;

	const shown = clamp(value, min, max);
	const valueText = `${fmt(value)}${unit} of ${fmt(max)}${unit}${statusText ? `, ${statusText}` : ""}`;
	const textWidth = Math.max(40, inner * 1.5);
	const centred = variant !== "half";

	const span = shapeSpan(variant);
	const rangeLabels = rangeOn
		? [
				{ at: min, angle: span.start },
				{ at: max, angle: span.start + span.sweep },
			].map(({ at, angle }) => {
				const p = polar(box.cx, box.cy, (inner + box.radius) / 2, angle);
				return { text: fmt(at), x: p.x, y: p.y + thickness / 2 + 11 };
			})
		: [];

	const glow = useMemo(
		() => (bloom === "off" ? [] : paths.slice(0, activeIndex + 1)),
		[bloom, paths, activeIndex],
	);

	return (
		// biome-ignore lint/a11y/useSemanticElements: a segmented arc cannot be a native <meter>
		<div
			ref={rootRef}
			role="meter"
			aria-label={label}
			aria-valuemin={min}
			aria-valuemax={max}
			aria-valuenow={Number.isFinite(shown) ? shown : min}
			aria-valuetext={valueText}
			className={cn("relative", className)}
			style={{ width: size, maxWidth: "100%", height: box.height }}
		>
			<svg
				ref={svgRef}
				width={box.width}
				height={box.height}
				className="block overflow-visible"
				aria-hidden
			>
				<Bloom bloom={bloom} box={{ width: box.width, height: box.height }}>
					{glow.map((d, i) => (
						<path
							// biome-ignore lint/suspicious/noArrayIndexKey: segments are positional
							key={i}
							d={d}
							fill={css}
						/>
					))}
				</Bloom>
				{paths.map((d, i) => {
					const paint = paintFor(i);
					return (
						<path
							// biome-ignore lint/suspicious/noArrayIndexKey: segments are positional
							key={i}
							data-block={i <= activeIndex ? i : undefined}
							d={d}
							fill={paint.fill}
							fillOpacity={paint.opacity}
							style={
								hideFilled && i <= activeIndex ? { opacity: 0 } : undefined
							}
						/>
					);
				})}
				{thresholds.map((t) => {
					const angle = shapeAngle(gaugeFraction(t.at, min, max), variant);
					const a = polar(box.cx, box.cy, inner - 3, angle);
					const b = polar(box.cx, box.cy, box.radius + 3, angle);
					return (
						<line
							key={t.status}
							x1={a.x}
							y1={a.y}
							x2={b.x}
							y2={b.y}
							stroke={STATUS_COLOR[t.status]}
							strokeWidth={1.5}
							strokeLinecap="round"
						/>
					);
				})}
				{tickValues.map((v) => {
					const angle = shapeAngle(gaugeFraction(v, min, max), variant);
					const a = polar(box.cx, box.cy, box.radius + 4, angle);
					const b = polar(box.cx, box.cy, box.radius + 8, angle);
					const at = polar(box.cx, box.cy, box.radius + 11, angle);
					const up = Math.cos(angle);
					return (
						<g key={v}>
							<line
								x1={a.x}
								y1={a.y}
								x2={b.x}
								y2={b.y}
								className="stroke-line-strong"
							/>
							<text
								x={at.x}
								y={at.y}
								dy={up > 0.3 ? "0" : up < -0.3 ? "0.8em" : "0.32em"}
								textAnchor={labelAnchor(angle)}
								className={AXIS_TEXT}
							>
								{fmt(v)}
							</text>
						</g>
					);
				})}
				{rangeLabels.map((l, i) => (
					<text
						// biome-ignore lint/suspicious/noArrayIndexKey: exactly two labels, min then max
						key={i}
						x={l.x}
						y={l.y}
						textAnchor="middle"
						className={AXIS_TEXT}
					>
						{l.text}
					</text>
				))}
			</svg>
			<div
				className={cn(
					"pointer-events-none absolute flex -translate-x-1/2 flex-col items-center gap-1 text-center",
					centred ? "-translate-y-1/2" : "-translate-y-full",
				)}
				style={{
					left: box.cx,
					top: centred ? box.cy : box.cy - 2,
					width: textWidth,
				}}
			>
				<span className="type-metric max-w-full truncate text-ink-1 tabular-nums leading-none">
					{fmt(value)}
					{unit ? <span className="text-ink-3 text-sm">{unit}</span> : null}
				</span>
				<span className="type-micro max-w-full truncate leading-none">
					{label}
				</span>
				{statusText ? (
					<span
						className="max-w-full truncate font-mono text-2xs leading-none"
						style={{ color: css }}
					>
						{statusText}
					</span>
				) : null}
			</div>
		</div>
	);
}
