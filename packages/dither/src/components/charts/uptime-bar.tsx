import { cn } from "cn";
import {
	type KeyboardEvent,
	type PointerEvent,
	type ReactNode,
	useMemo,
	useRef,
	useState,
} from "react";
import {
	bodyBands,
	bucketRanges,
	DEFAULT_THRESHOLDS,
	dayStatus,
	fitCells,
	formatUptime,
	rectPath,
	slotAt,
	type UptimeStatus,
	type UptimeThresholds,
	uptimeFraction,
	worstStatus,
} from "../../lib/bars-math";
import { clamp } from "../../lib/chart-math";
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
import { scrubKeyTarget } from "./cartesian-math";
import { Bloom, ChartLive, useChartUid } from "./chart-kit";
import {
	type ChartBloom,
	type ChartColor,
	type DitherMatrix,
	type FillVariant,
	flatFill,
	inkFill,
	resolveColor,
} from "./shared";
import {
	colorOverrides,
	formatDay,
	statusWords,
	type UptimeColorKey,
	type UptimeWords,
	windowDays,
} from "./uptime-options";

export type { UptimeStatus, UptimeWords };

export interface UptimeDay {
	/** An ISO date (`2026-10-09`, written in the locale, in UTC) or a display string ("Mar 14"). Unique within the bar: it keys the table row. */
	date: string;
	/** The day's status. Without one it is derived from `uptime` (≥ 99.9% up, ≥ 95% degraded, else down; see `thresholds`). */
	status?: UptimeStatus;
	/** Share of the day the service was up, 0–1; `null` for a day with no data. Feeds the headline figure and the status when none is given. */
	uptime?: number | null;
	/** One line under the status in the readout: "Elevated latency in eu-west". */
	detail?: string;
	/** Alias of `detail` (the dithercn name). `detail` wins when both are set. */
	note?: string;
}

export interface DitherUptimeBarProps {
	/** Oldest first. */
	days: readonly UptimeDay[];
	/** Height of the bar row in px. Default 36. */
	height?: number;
	/** Space between cells in px. Default 2. */
	gap?: number;
	/** Headline uptime, 0–1. Default: the mean over the days that have data. */
	uptime?: number;
	/** Legend row and uptime figure. Default true. */
	legend?: boolean;
	/** Dither cell in px. Default 2: the cells are narrow. */
	cell?: number;
	/** Accessible name; a hidden table lists every day. */
	label: string;
	/** Colours per status: names (`"blue"`) or CSS. Keys `up`, `degraded`, `down` and `unknown` (alias `none`). Default green, amber, red, grey. */
	colors?: Partial<Record<UptimeColorKey, ChartColor>>;
	/** Texture of the up days; degraded days are hatched, outages solid, days without data dotted. Default `gradient`. */
	fill?: FillVariant;
	/** Soft glow behind degraded and down days only. Default `off`. */
	bloom?: ChartBloom;
	/** Locale for the dates of ISO `date`s, in tooltips and the table. Default `en-US`. */
	locale?: string;
	/** Threshold matrix of the gradient fill: Bayer 2/4/8 or blue noise. Default `bayer4`. */
	matrix?: DitherMatrix;
	/** Where a derived status flips: `up` (default 0.999) and `degraded` (default 0.95). */
	thresholds?: Partial<UptimeThresholds>;
	/** Show only the latest 30 / 60 / 90 days at container widths under 28rem / from 28rem / from 42rem. Default false (all days, merged to fit). */
	responsive?: boolean;
	/** Words for each status in the legend, tooltips and screen-reader text: `{ up: "Succeeded", down: "Failed", none: "No backup" }`. Default Operational / Degraded / Down / No data. */
	words?: UptimeWords;
	/** The caption after the headline figure. Default "uptime"; `false` hides it. */
	caption?: ReactNode | false;
	className?: string;
}

const COLOR: Record<UptimeStatus, string> = {
	up: "var(--color-status-live)",
	degraded: "var(--color-warn)",
	down: "var(--color-bad)",
	unknown: "var(--color-ink-3)",
};

/** Which statuses end in a solid cap. */
const CAPPED: Record<UptimeStatus, boolean> = {
	up: true,
	degraded: false,
	down: true,
	unknown: false,
};

/** Solid fallback: statuses differ by height and tint too, since texture is lost. */
const SOLID_HEIGHT: Record<UptimeStatus, number> = {
	up: 0.8,
	degraded: 0.55,
	down: 1,
	unknown: 0.25,
};
const SOLID_MIX: Record<UptimeStatus, number> = {
	up: 34,
	degraded: 50,
	down: 85,
	unknown: 16,
};

/** Dense at the cap, thinning only a little: an operational cell reads as full. */
const UP_TONES = ditherGradientStops(0.82, 0.4, 5);
const DEGRADED_TONE = 0.5;
const DOWN_TONE = 0.88;
const UNKNOWN_TONE = 0.24;
const CAP_H = 2;
const STATUSES: readonly UptimeStatus[] = ["up", "degraded", "down", "unknown"];
const READOUT_W = 192;

interface CellRect {
	x: number;
	y: number;
	w: number;
	h: number;
}

/**
 * A status-page row: one tall narrow cell per day, told apart by colour and
 * by texture. Up is a dense dither under a solid cap, degraded a hatch, down
 * a near-solid block with a cap, no-data sparse dots. Cells share the row's
 * width (days merge into one cell, the worst status winning, when they would
 * be narrower than 3 px). Hover or arrow keys read a cell out. Plain SVG;
 * under forced colours (or a cell below 1.5 px) textures become tints that
 * also differ in height.
 */
export function DitherUptimeBar({
	days: allDays,
	height = 36,
	gap = 2,
	uptime,
	legend = true,
	cell = 2,
	label,
	colors,
	fill = "gradient",
	bloom = "off",
	locale = "en-US",
	matrix,
	thresholds,
	responsive = false,
	words,
	caption = "uptime",
	className,
}: DitherUptimeBarProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const uid = useChartUid();
	const solid = forced || cell < MIN_DITHER_CELL;
	const [active, setActive] = useState<number | null>(null);

	const th = useMemo<UptimeThresholds>(
		() => ({
			up: thresholds?.up ?? DEFAULT_THRESHOLDS.up,
			degraded: thresholds?.degraded ?? DEFAULT_THRESHOLDS.degraded,
		}),
		[thresholds?.up, thresholds?.degraded],
	);
	const days = useMemo(() => {
		const shown = responsive ? allDays.slice(-windowDays(width)) : allDays;
		return shown.map((d) => ({
			date: d.date,
			text: formatDay(d.date, locale),
			status: d.status,
			uptime: d.uptime ?? undefined,
			detail: d.detail ?? d.note,
		}));
	}, [allDays, responsive, width, locale]);
	const word = useMemo(() => statusWords(words), [words]);
	const palette = useMemo<Record<UptimeStatus, string>>(() => {
		const overrides = colorOverrides(colors);
		const pick = (status: UptimeStatus) => {
			const custom = overrides[status];
			return custom === undefined ? COLOR[status] : resolveColor(custom);
		};
		return {
			up: pick("up"),
			degraded: pick("degraded"),
			down: pick("down"),
			unknown: pick("unknown"),
		};
	}, [colors]);
	const statusOf = (d: {
		status?: UptimeStatus | undefined;
		uptime?: number | undefined;
	}) => dayStatus(d, th);

	// Fills in registration order: the up texture, then degraded, down, no data.
	const upLayered = fill === "gradient";
	const upFills = useMemo<DitherFill[]>(() => {
		if (fill === "solid") return [];
		if (fill === "gradient") {
			return UP_TONES.map((tone) => inkFill(tone, palette.up, cell, matrix));
		}
		return [flatFill(fill, palette.up, cell, matrix)];
	}, [fill, palette.up, cell, matrix]);
	const fills = useMemo<DitherFill[]>(
		() =>
			solid
				? []
				: [
						...upFills,
						{
							...inkFill(DEGRADED_TONE, palette.degraded, cell, matrix),
							shape: "hatch" as const,
						},
						inkFill(DOWN_TONE, palette.down, cell, matrix),
						{
							...inkFill(UNKNOWN_TONE, palette.unknown, cell, matrix),
							shape: "dot" as const,
						},
					],
		[solid, upFills, palette, cell, matrix],
	);
	const urls = useDitherFillUrls(fills);
	/** The fill of a status' body (the densest band for a gradient `up`), or its tint when solid. */
	const bodyFill = (status: UptimeStatus) => {
		if (solid) {
			return `color-mix(in oklab, ${palette[status]} ${SOLID_MIX[status]}%, transparent)`;
		}
		if (status === "up" && fill === "solid") {
			return `color-mix(in oklab, ${palette.up} 70%, transparent)`;
		}
		const at = {
			up: 0,
			degraded: upFills.length,
			down: upFills.length + 1,
			unknown: upFills.length + 2,
		}[status];
		return urls[at];
	};

	const geometry = useMemo(() => {
		if (width === null || width < 8 || days.length === 0) return null;
		const fit = fitCells(width, days.length, { gap, minCell: 3, maxCell: 20 });
		const cells = bucketRanges(days.length, fit.cells).map(
			([start, end], k) => {
				const slice = days.slice(start, end);
				const statuses = slice.map((d) => dayStatus(d, th));
				const status = worstStatus(statuses);
				const first = slice[0];
				const last = slice.at(-1);
				return {
					k,
					start,
					end,
					status,
					x: k * fit.step,
					w: fit.cellW,
					first,
					last,
					worst: slice[statuses.indexOf(status)] ?? first,
					fraction: uptimeFraction(slice),
				};
			},
		);
		const rects: Record<UptimeStatus, CellRect[]> = {
			up: [],
			degraded: [],
			down: [],
			unknown: [],
		};
		for (const c of cells) {
			const h = solid ? height * SOLID_HEIGHT[c.status] : height;
			rects[c.status].push({ x: c.x, y: height - h, w: c.w, h });
		}
		const bodies = STATUSES.flatMap((status) => {
			const list = rects[status];
			if (status !== "up" || solid || !upLayered) {
				return [
					{
						status,
						id: "flat",
						i: 0,
						d: list.map((r) => rectPath(r.x, r.y, r.w, r.h)).join(""),
					},
				];
			}
			const buckets: string[][] = UP_TONES.map(() => []);
			for (const r of list) {
				for (const band of bodyBands(r.h, r.h, UP_TONES)) {
					buckets[band.i]?.push(rectPath(r.x, r.y + band.y, r.w, band.h));
				}
			}
			return buckets.map((paths, i) => ({
				status,
				id: `band-${i}`,
				i,
				d: paths.join(""),
			}));
		});
		return { fit, cells, rects, bodies };
	}, [width, days, gap, height, solid, th, upLayered]);

	const headline = uptime ?? uptimeFraction(days);
	const hasUnknown = days.some((d) => statusOf(d) === "unknown");
	const legendStatuses = STATUSES.filter((s) => s !== "unknown" || hasUnknown);

	const current =
		geometry && active !== null && active < geometry.cells.length
			? active
			: null;
	const cellAt = current !== null ? geometry?.cells[current] : undefined;

	const onPointer = (e: PointerEvent<SVGRectElement>) => {
		if (!geometry) return;
		const svg = e.currentTarget.ownerSVGElement ?? e.currentTarget;
		const box = svg.getBoundingClientRect();
		setActive(
			slotAt(e.clientX - box.left, geometry.fit.step, geometry.cells.length),
		);
	};
	const onKey = (e: KeyboardEvent) => {
		const count = geometry?.cells.length ?? 0;
		const target = scrubKeyTarget(e.key, current, count);
		if (target === undefined) return;
		if (target !== null) {
			e.preventDefault();
			e.stopPropagation();
		}
		setActive(target);
	};

	const readout = (() => {
		if (!cellAt?.first || !cellAt.last) return null;
		const title =
			cellAt.first.date === cellAt.last.date
				? cellAt.first.text
				: `${cellAt.first.text} – ${cellAt.last.text}`;
		return {
			title,
			status: cellAt.status,
			days: cellAt.end - cellAt.start,
			fraction: cellAt.fraction,
			detail: cellAt.worst?.detail,
		};
	})();
	const liveText = readout
		? `${readout.title}, ${word[readout.status]}${readout.fraction !== null ? `, ${formatUptime(readout.fraction)} uptime` : ""}${readout.detail ? `. ${readout.detail}` : ""}`
		: "";
	const captionText = typeof caption === "string" ? caption : "uptime";

	const glowStatuses = bloom === "off" ? [] : (["degraded", "down"] as const);

	return (
		// biome-ignore lint/a11y/useSemanticElements: a status row is an image with a keyboard readout
		<div
			ref={rootRef}
			role="group"
			aria-label={label}
			className={cn(
				"relative flex min-w-0 flex-col gap-2 rounded-ctl has-[:focus-visible]:focus-ring",
				className,
			)}
		>
			<div className="relative" style={{ height }}>
				{geometry && width !== null ? (
					<button
						type="button"
						aria-label={`${label}. ${days.length} days. Use the arrow keys to inspect days.`}
						aria-describedby={`${uid}-keys`}
						onKeyDown={onKey}
						onBlur={() => setActive(null)}
						className="block rounded-ctl outline-none"
					>
						<svg
							width={width}
							height={height}
							className="block overflow-visible"
							aria-hidden
						>
							{glowStatuses.map((status) => (
								<Bloom key={status} bloom={bloom} box={{ width, height }}>
									<path
										d={geometry.rects[status]
											.map((r) => rectPath(r.x, r.y, r.w, r.h))
											.join("")}
										fill={palette[status]}
									/>
								</Bloom>
							))}
							{geometry.bodies.map((body) =>
								body.d ? (
									<path
										key={`${body.status}-${body.id}`}
										d={body.d}
										fill={
											body.status === "up" && !solid && upLayered
												? urls[body.i]
												: bodyFill(body.status)
										}
									/>
								) : null,
							)}
							{geometry.cells.map((c) => {
								if (!CAPPED[c.status]) return null;
								const h = solid ? height * SOLID_HEIGHT[c.status] : height;
								return (
									<rect
										key={c.k}
										aria-hidden
										x={c.x}
										y={height - h}
										width={c.w}
										height={CAP_H}
										rx={Math.min(2, c.w / 2)}
										ry={CAP_H / 2}
										fill={palette[c.status]}
									/>
								);
							})}
							{cellAt ? (
								<rect
									aria-hidden
									x={cellAt.x - 2}
									y={-2}
									width={cellAt.w + 4}
									height={height + 4}
									rx={3}
									fill="none"
									className="stroke-line-strong"
								/>
							) : null}
							<rect
								width={width}
								height={height}
								fill="transparent"
								onPointerMove={onPointer}
								onPointerDown={onPointer}
								onPointerLeave={() => setActive(null)}
							/>
						</svg>
					</button>
				) : null}
				{readout && cellAt && width !== null ? (
					<div
						aria-hidden
						style={{
							left: clamp(
								cellAt.x + cellAt.w / 2 - READOUT_W / 2,
								0,
								Math.max(0, width - READOUT_W),
							),
							bottom: height + 8,
							width: READOUT_W,
						}}
						className="surface-pop pointer-events-none absolute z-10 flex flex-col gap-1 rounded-ctl px-2.5 py-2 font-mono text-2xs leading-none"
					>
						<span className="text-ink-3">{readout.title}</span>
						<span className="flex items-center gap-1.5">
							<span
								aria-hidden
								className="size-2 shrink-0 rounded-[2px]"
								style={{ background: palette[readout.status] }}
							/>
							<span className="min-w-0 flex-1 truncate text-ink-1">
								{word[readout.status]}
							</span>
							{readout.fraction !== null ? (
								<span className="text-ink-1 tabular-nums">
									{formatUptime(readout.fraction)}
								</span>
							) : null}
						</span>
						{readout.days > 1 ? (
							<span className="text-ink-3">
								{readout.days} days, worst shown
							</span>
						) : null}
						{readout.detail ? (
							<span className="text-ink-2 leading-snug">{readout.detail}</span>
						) : null}
					</div>
				) : null}
			</div>
			<div className="flex justify-between font-mono text-[10px] text-ink-4">
				<span>{days[0]?.text}</span>
				<span>{days.length > 1 ? days.at(-1)?.text : null}</span>
			</div>
			{legend ? (
				<div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
					<ul
						className="flex flex-wrap items-center gap-x-3 gap-y-1.5"
						aria-label="Legend"
					>
						{legendStatuses.map((status) => (
							<li
								key={status}
								className="flex items-center gap-1.5 font-mono text-2xs leading-none text-ink-2"
							>
								<svg width={10} height={14} aria-hidden className="shrink-0">
									<rect
										width={10}
										height={solid ? 14 * SOLID_HEIGHT[status] : 14}
										y={solid ? 14 - 14 * SOLID_HEIGHT[status] : 0}
										fill={bodyFill(status)}
									/>
									{CAPPED[status] ? (
										<rect
											width={10}
											height={CAP_H}
											y={solid ? 14 - 14 * SOLID_HEIGHT[status] : 0}
											rx={2}
											ry={1}
											fill={palette[status]}
										/>
									) : null}
								</svg>
								<span>{word[status]}</span>
							</li>
						))}
					</ul>
					{headline !== null ? (
						<span className="ml-auto font-mono text-2xs leading-none text-ink-1 tabular-nums">
							{formatUptime(headline)}
							{caption === false ? null : (
								<>
									{" "}
									<span className="text-ink-3">{caption}</span>
								</>
							)}
						</span>
					) : null}
				</div>
			) : null}
			<ChartLive id={`${uid}-keys`} text={liveText} />
			<table className="sr-only">
				<caption>
					{label}
					{headline !== null
						? `, ${formatUptime(headline)}${caption === false ? "" : ` ${captionText}`}`
						: ""}
				</caption>
				<thead>
					<tr>
						<th scope="col">Date</th>
						<th scope="col">Status</th>
						<th scope="col">Uptime</th>
						<th scope="col">Detail</th>
					</tr>
				</thead>
				<tbody>
					{days.map((d) => (
						<tr key={d.date}>
							<th scope="row">{d.text}</th>
							<td>{word[statusOf(d)]}</td>
							<td>
								{d.uptime !== undefined
									? formatUptime(clamp(d.uptime, 0, 1))
									: ""}
							</td>
							<td>{d.detail ?? ""}</td>
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
