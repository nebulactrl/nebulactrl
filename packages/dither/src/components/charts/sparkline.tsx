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
	linear,
	linePath,
	type Pt,
	sampleCurve,
} from "../../lib/chart-math";
import {
	MIN_DITHER_CELL,
	useElementWidth,
	useForcedColors,
} from "../../lib/prefs";
import { AreaFill } from "./area-fill";
import { scrubKeyTarget } from "./cartesian-math";
import { ChartLive, Entrance, useChartUid, useEntrance } from "./chart-kit";
import {
	type ChartBloom,
	type ChartColor,
	type DitherMatrix,
	type FillVariant,
	makeFormat,
	resolveColor,
	type ValueFormat,
} from "./shared";

export interface DitherSparklineProps {
	/** Values in order, oldest first. */
	data: readonly number[];
	/** What the trend shows ("Requests, last 24 hours"). Names the image and starts its summary. */
	label: string;
	/** Height in px. Default 32. The width follows the element: size it with `className`. */
	height?: number;
	/** A name (`"green"`, `"blue"`, `"ink"`), a CSS colour or a `--token`. Default the first series colour. */
	color?: ChartColor;
	/** Dithered fill under the line. Default true. */
	area?: boolean;
	fill?: FillVariant;
	/** A dot on the latest value. Default true. */
	dot?: boolean;
	/**
	 * The value range the height covers: `[min, max]`, `"data"` (the data's own range) or `"zero"`
	 * (0 to the largest value, the default: a flat 3% line stays flat).
	 */
	domain?: readonly [number, number] | "data" | "zero";
	/** Dither cell in px. Default 2: a step finer than charts. */
	cell?: number;
	/** Number format for the summary and the scrub label: `Intl` options or a function. */
	format?: ValueFormat;
	/** Soft glow in the line's colour behind the fill. Default `off`. */
	bloom?: ChartBloom;
	/** Scrub with the pointer or the arrow keys to read single values in a small label. Default false. */
	interactive?: boolean;
	/** Entrance motion, once, when first seen: the line draws, the fill fades up, the dot lands last. Always off under reduced motion. Default true. */
	animate?: boolean;
	/** Tone just under the line, 0–1. Default 0.7. */
	level?: number;
	/** Threshold matrix of the fill: Bayer 2/4/8 or blue noise. Default `bayer4`. */
	matrix?: DitherMatrix;
	className?: string;
}

const PAD = 3;
const DEFAULT_FORMAT = (v: number) => String(Math.round(v * 100) / 100);

/**
 * A tiny inline trend: crisp line, a dithered fill that is densest at the
 * line, and a dot on the latest value. Decorative-sized but never mute: the
 * accessible summary names the range and the latest value. With
 * `interactive` it becomes a focusable group you can scrub.
 */
export function DitherSparkline({
	data,
	label,
	height = 32,
	color,
	area = true,
	fill = "gradient",
	dot = true,
	domain,
	cell = 2,
	format,
	bloom = "off",
	interactive = false,
	animate = true,
	level = 0.7,
	matrix,
	className,
}: DitherSparklineProps) {
	const ref = useRef<HTMLSpanElement>(null);
	const width = useElementWidth(ref);
	const forced = useForcedColors();
	const entrance = useEntrance(ref, animate);
	const uid = useChartUid();
	const [active, setActive] = useState<number | null>(null);
	const css = resolveColor(color, 0);
	const solid = forced || cell < MIN_DITHER_CELL;
	const fmt = useMemo(() => makeFormat(format, DEFAULT_FORMAT), [format]);

	const geometry = useMemo(() => {
		if (width === null || width < 8 || data.length < 2) return null;
		const range: readonly [number, number] = Array.isArray(domain)
			? (domain as readonly [number, number])
			: domain === "data"
				? [Math.min(...data), Math.max(...data)]
				: [0, Math.max(Number.EPSILON, ...data)];
		const [lo, hi] = range;
		const y = linear([lo, hi === lo ? lo + 1 : hi], [height - PAD, PAD]);
		const pts: Pt[] = data.map((v, i) => ({
			x: PAD + (i / (data.length - 1)) * (width - PAD * 2),
			y: y(clamp(v, lo, hi)),
		}));
		const line = sampleCurve(pts, "monotone", 2);
		return { line, pts, last: pts.at(-1) ?? null };
	}, [width, data, domain, height]);

	const latest = data.at(-1);
	const summary =
		latest === undefined
			? `${label}: no data`
			: `${label}: latest ${fmt(latest)}, low ${fmt(Math.min(...data))}, high ${fmt(Math.max(...data))}`;

	const point = geometry && active !== null ? geometry.pts[active] : undefined;
	const value = active !== null ? data[active] : undefined;
	const text = value === undefined ? "" : fmt(value);

	const onPointer = (e: PointerEvent<HTMLSpanElement>) => {
		if (!geometry || width === null) return;
		const box = e.currentTarget.getBoundingClientRect();
		const t = (e.clientX - box.left - PAD) / Math.max(1, width - PAD * 2);
		setActive(clamp(Math.round(t * (data.length - 1)), 0, data.length - 1));
	};
	const onKey = (e: KeyboardEvent) => {
		const target = scrubKeyTarget(e.key, active, data.length);
		if (target === undefined) return;
		if (target !== null) {
			e.preventDefault();
			e.stopPropagation();
		}
		setActive(target);
	};

	const labelWidth = text.length * 6.5 + 14;
	// One literal-role props object per mode, spread onto the root: the
	// interactive sparkline is a focusable group you can scrub, the static
	// one a labelled image.
	const rootProps = interactive
		? {
				role: "group" as const,
				"aria-label": label,
				"aria-describedby": `${uid}-keys`,
				tabIndex: data.length > 0 ? 0 : undefined,
				onPointerMove: onPointer,
				onPointerLeave: () => setActive(null),
				onKeyDown: onKey,
				onFocus: () => setActive((cur) => cur ?? data.length - 1),
				onBlur: () => setActive(null),
			}
		: { role: "img" as const, "aria-label": summary };
	return (
		<span
			ref={ref}
			{...rootProps}
			className={cn(
				"relative block min-w-0",
				interactive &&
					"rounded-ctl outline-none focus-visible:focus-ring touch-pan-y",
				className,
			)}
			style={{ height }}
		>
			{geometry && width !== null ? (
				<svg
					width={width}
					height={height}
					aria-hidden
					className="block overflow-visible"
				>
					<Entrance plan={entrance} kind="wipe">
						{area ? (
							<AreaFill
								top={geometry.line}
								bottom={height}
								color={css}
								variant={fill}
								cell={cell}
								depth={height * 0.85}
								floor={height}
								bands={5}
								level={level}
								floorTone={0.1}
								solid={solid}
								matrix={matrix}
								bloom={bloom}
							/>
						) : null}
						<path
							d={linePath(geometry.line)}
							fill="none"
							stroke={css}
							strokeWidth={1.5}
							strokeLinejoin="round"
							strokeLinecap="round"
						/>
					</Entrance>
					{dot && geometry.last && !point ? (
						<Entrance plan={entrance} kind="fade" delay={500} duration={300}>
							<circle
								cx={geometry.last.x}
								cy={geometry.last.y}
								r={2.5}
								fill={css}
								style={{ stroke: "var(--dither-paper)", strokeWidth: 1.5 }}
							/>
						</Entrance>
					) : null}
					{point ? (
						<circle
							cx={point.x}
							cy={point.y}
							r={3}
							fill={css}
							style={{ stroke: "var(--dither-paper)", strokeWidth: 1.5 }}
						/>
					) : null}
				</svg>
			) : null}
			{interactive && point && width !== null ? (
				<span
					aria-hidden
					className="surface-pop pointer-events-none absolute z-10 rounded-ctl px-1.5 py-1 font-mono text-2xs leading-none tabular-nums text-ink-1"
					style={{
						left: clamp(
							point.x - labelWidth / 2,
							0,
							Math.max(0, width - labelWidth),
						),
						top: Math.min(point.y, height) - 26,
					}}
				>
					{text}
				</span>
			) : null}
			{interactive ? (
				<ChartLive
					id={`${uid}-keys`}
					text={active === null ? "" : `${label}: ${text}`}
				/>
			) : null}
		</span>
	);
}
