import { cn } from "cn";
import {
	type KeyboardEvent,
	type PointerEvent,
	useCallback,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { rectPath } from "../../lib/bars-math";
import { clamp, levelOf } from "../../lib/chart-math";
import {
	type CalendarEntry,
	calendarLayout,
	type HeatValue,
	heatMax,
	levelTones,
	parseDay,
	weekdayLabels,
} from "../../lib/heat-math";
import { type DitherFill, useDitherFillUrls } from "../../lib/pattern";
import {
	MIN_DITHER_CELL,
	useElementWidth,
	useForcedColors,
} from "../../lib/prefs";
import { Bloom, ChartLive, useChartUid, useEntrance } from "./chart-kit";
import {
	calendarStartForWeeks,
	type HeatUnit,
	levelRangeText,
	unitPlural,
	unitWord,
	weeksThatFit,
} from "./heat-options";
import {
	AXIS_TEXT,
	type ChartBloom,
	type ChartColor,
	ChartReadout,
	type DitherMatrix,
	inkFill,
	resolveColor,
	type ValueFormat,
} from "./shared";

interface HeatmapCommon {
	/** A name (`"purple"`, `"orange"`, `ok`, `info`…), a CSS colour or a `var(--token)`. Default series 1. */
	color?: ChartColor;
	/** Dither steps above "empty": 2–8. Default 5. */
	levels?: number;
	/** Alias of `levels` (the dithercn name). `levels` wins when both are set. */
	steps?: number;
	/** The value that earns the top level; larger values draw solid too. Default: the data's largest. */
	max?: number;
	/** Cell edge in px, 6–40. Default 14; shrinks to fit a narrow container. */
	cellSize?: number;
	/** Space between cells in px. Default 2. */
	gap?: number;
	/** Dither cell in px. Default: a quarter of the cell, so one tile fills one square. */
	cell?: number;
	/** What a value counts, for the readout and the table: `"min"`, or `{ one: "deploy", other: "deploys" }`. Default "value". */
	unit?: HeatUnit;
	/** Readout and table number format. Default: full number, up to 2 decimals. */
	format?: ValueFormat | undefined;
	/** "Less ▢▢▢▢▢ More" scale under the grid; each step spotlights its cells. Default true. */
	legend?: boolean;
	/** Hover / focus readout with the exact value. Default true. */
	tooltip?: boolean;
	/** Readout, keyboard and the scale spotlight. `false` draws a static image. Default true. */
	interactive?: boolean;
	/** Squares ripple in from the top left the first time the grid is seen. Always off under reduced motion. Default true. */
	animate?: boolean;
	/** Soft glow behind the two densest levels. Default `off`. */
	bloom?: ChartBloom;
	/** Threshold matrix of the dither: Bayer 2/4/8 or blue noise. Default `bayer4`. */
	matrix?: DitherMatrix;
	/** Accessible name; a summary and the cells (in a hidden table) follow. Default "Heatmap" / "Activity". */
	label?: string;
	className?: string;
}

export interface DitherMatrixHeatmapProps extends HeatmapCommon {
	layout?: "matrix";
	mode?: never;
	/** One unique label per row, e.g. weekdays. */
	rows: readonly string[];
	/** One unique label per column, e.g. hours. */
	columns: readonly string[];
	/** `values[r][c]`; `null` or a short row means "no data". */
	values: readonly (readonly HeatValue[])[];
	data?: never;
	weekStart?: never;
	weeks?: never;
	from?: never;
	to?: never;
}

export interface DitherCalendarHeatmapProps extends HeatmapCommon {
	layout: "calendar";
	mode?: never;
	/** One entry per day; entries on one date are summed. */
	data: readonly CalendarEntry[];
	/** First weekday row: 0 Sunday (default) or 1 Monday. */
	weekStart?: 0 | 1;
	/** Week columns shown, ending with the latest day. Default: the data's own range (trimmed to what fits). */
	weeks?: number;
	/** Range to draw; default the first to the last entry. */
	from?: string | Date;
	to?: string | Date;
	rows?: never;
	columns?: never;
	values?: never;
}

/** dithercn's calendar form: `mode` (default `"calendar"`) and `data` of `{ date, value }`. */
export interface DitherCalendarModeHeatmapProps extends HeatmapCommon {
	mode?: "calendar";
	layout?: never;
	data: readonly CalendarEntry[];
	weekStart?: 0 | 1;
	weeks?: number;
	from?: string | Date;
	to?: string | Date;
	rows?: never;
	columns?: never;
	values?: never;
}

/** dithercn's matrix form: `mode="matrix"` with the grid in `data[row][column]`. */
export interface DitherMatrixModeHeatmapProps extends HeatmapCommon {
	mode: "matrix";
	layout?: never;
	rows: readonly string[];
	columns: readonly string[];
	data: readonly (readonly HeatValue[])[];
	values?: never;
	weekStart?: never;
	weeks?: never;
	from?: never;
	to?: never;
}

export type DitherHeatmapProps =
	| DitherMatrixHeatmapProps
	| DitherCalendarHeatmapProps
	| DitherCalendarModeHeatmapProps
	| DitherMatrixModeHeatmapProps;

interface HeatCell {
	col: number;
	row: number;
	value: number | null;
	level: number;
	title: string;
}

interface HeatModel {
	cols: number;
	rows: number;
	cells: HeatCell[];
	/** `index[row * cols + col]` is the cell at that spot, if the grid has one there. */
	index: (number | undefined)[];
	rowLabels: string[];
	colLabels: { col: number; label: string; centre: boolean }[];
	max: number;
}

const FULL_NUMBER = new Intl.NumberFormat("en", { maximumFractionDigits: 2 });
const DAY_TITLE = new Intl.DateTimeFormat("en", {
	weekday: "short",
	month: "short",
	day: "numeric",
	year: "numeric",
	timeZone: "UTC",
});

/** 6 px per mono 10px glyph. */
const GLYPH = 6;
const LABEL_ROW = 16;
/** Left gutter of a calendar: "Mon" and friends. */
const CALENDAR_LABEL_W = 3 * GLYPH + 10;
/** Smallest square a calendar shrinks to before it drops the oldest weeks. */
const MIN_FIT_SIZE = 6;

function indexOf(cells: readonly HeatCell[], cols: number, rows: number) {
	const index = new Array<number | undefined>(cols * rows).fill(undefined);
	cells.forEach((cell, i) => {
		index[cell.row * cols + cell.col] = i;
	});
	return index;
}

function matrixModel(
	rows: readonly string[],
	columns: readonly string[],
	values: readonly (readonly HeatValue[])[],
	levels: number,
	maxProp: number | undefined,
): HeatModel {
	const max = maxProp !== undefined && maxProp > 0 ? maxProp : heatMax(values);
	const cells: HeatCell[] = [];
	rows.forEach((rowLabel, row) => {
		columns.forEach((colLabel, col) => {
			const raw = values[row]?.[col];
			const value =
				typeof raw === "number" && Number.isFinite(raw) ? raw : null;
			cells.push({
				col,
				row,
				value,
				level: value === null ? 0 : levelOf(value, max, levels),
				title: `${rowLabel} ${colLabel}`,
			});
		});
	});
	return {
		cols: columns.length,
		rows: rows.length,
		cells,
		index: indexOf(cells, columns.length, rows.length),
		rowLabels: [...rows],
		colLabels: columns.map((label, col) => ({ col, label, centre: true })),
		max,
	};
}

function calendarModel(
	data: readonly CalendarEntry[],
	weekStart: 0 | 1,
	from: string | Date | undefined,
	to: string | Date | undefined,
	levels: number,
	maxProp: number | undefined,
): HeatModel {
	const layout = calendarLayout(data, {
		weekStart,
		...(from !== undefined ? { from } : {}),
		...(to !== undefined ? { to } : {}),
	});
	const max =
		maxProp !== undefined && maxProp > 0
			? maxProp
			: heatMax([layout.cells.map((c) => c.value)]);
	const cells: HeatCell[] = layout.cells.map((c) => ({
		col: c.week,
		row: c.day,
		value: c.value,
		level: c.value === null ? 0 : levelOf(c.value, max, levels),
		title: DAY_TITLE.format(new Date(`${c.date}T00:00:00Z`)),
	}));
	const names = weekdayLabels(weekStart);
	return {
		cols: layout.weeks,
		rows: 7,
		cells,
		index: indexOf(cells, layout.weeks, 7),
		rowLabels: names.map((name, row) => (row % 2 === 1 ? name : "")),
		colLabels: layout.months.map((m) => ({
			col: m.week,
			label: m.label,
			centre: false,
		})),
		max,
	};
}

/**
 * A grid of squares that step through dither levels: empty is an outline,
 * level 1 a few dots, the top level solid. Every level is one `<pattern>`
 * registered once for the page; cells only reference it. Two forms:
 * `matrix` (rows × columns, e.g. weekday × hour) and `calendar` (weeks as
 * columns, GitHub-style), each in our props (`layout`, `values`) or
 * dithercn's (`mode`, `data`). Labels and the readout are plain text. Under
 * forced colours, or a dither cell below 1.5 px, a level is the colour at
 * that tone.
 */
export function DitherHeatmap(props: DitherHeatmapProps) {
	const {
		weekStart = 0,
		from: fromProp,
		to,
		weeks,
		color,
		max: maxProp,
		gap: gapProp = 2,
		cell,
		unit,
		format,
		legend = true,
		tooltip = true,
		interactive = true,
		animate = true,
		bloom = "off",
		matrix,
		className,
	} = props;
	const kind: "matrix" | "calendar" =
		props.mode ??
		props.layout ??
		(props.rows === undefined && props.data !== undefined
			? "calendar"
			: "matrix");
	const rows = props.rows;
	const columns = props.columns;
	const values = (
		kind === "matrix" ? (props.values ?? props.data) : undefined
	) as readonly (readonly HeatValue[])[] | undefined;
	const entries = (kind === "calendar" ? props.data : undefined) as
		| readonly CalendarEntry[]
		| undefined;
	const label = props.label ?? (kind === "calendar" ? "Activity" : "Heatmap");
	const levels = clamp(Math.round(props.levels ?? props.steps ?? 5), 2, 8);
	const cellSize = clamp(props.cellSize ?? 14, 6, 40);
	const gap = clamp(gapProp, 0, 8);
	const tooltipOn = tooltip && interactive;

	const rootRef = useRef<HTMLDivElement>(null);
	const svgRef = useRef<SVGSVGElement>(null);
	const width = useElementWidth(rootRef);
	const forced = useForcedColors();
	const entrance = useEntrance(rootRef, animate);
	const uid = useChartUid();
	const [active, setActive] = useState<number | null>(null);
	const [litLevel, setLitLevel] = useState<number | null>(null);
	const [lockedLevel, setLockedLevel] = useState<number | null>(null);
	const focusLevel = interactive ? (litLevel ?? lockedLevel) : null;
	const css = resolveColor(color, 0);
	const fmt = useMemo(() => {
		if (!format) return (v: number) => FULL_NUMBER.format(v);
		if (typeof format === "function") return format;
		const nf = new Intl.NumberFormat("en", format);
		return (v: number) => nf.format(v);
	}, [format]);

	// A calendar starts at `from`; `weeks` (or a container too narrow for the
	// whole range) trims it to the latest weeks.
	const from = useMemo(() => {
		if (kind !== "calendar" || fromProp !== undefined || !entries)
			return fromProp;
		const dates = entries.map((e) => e.date);
		if (weeks !== undefined)
			return calendarStartForWeeks(dates, weeks, weekStart, to) ?? undefined;
		if (width === null) return undefined;
		const cap = weeksThatFit(width, CALENDAR_LABEL_W, gap, MIN_FIT_SIZE);
		const start = calendarStartForWeeks(dates, cap, weekStart, to);
		const days = dates
			.map((d) => parseDay(d))
			.filter((d): d is number => d !== null);
		const first = days.length > 0 ? Math.min(...days) : null;
		const startDay = start === null ? null : parseDay(start);
		return start !== null &&
			first !== null &&
			startDay !== null &&
			startDay > first
			? start
			: undefined;
	}, [kind, fromProp, entries, weeks, weekStart, to, width, gap]);

	const model = useMemo(
		() =>
			kind === "calendar"
				? calendarModel(entries ?? [], weekStart, from, to, levels, maxProp)
				: matrixModel(rows ?? [], columns ?? [], values ?? [], levels, maxProp),
		[
			kind,
			rows,
			columns,
			values,
			entries,
			weekStart,
			from,
			to,
			levels,
			maxProp,
		],
	);

	const labelW = model.rowLabels.some((l) => l !== "")
		? Math.max(...model.rowLabels.map((l) => l.length)) * GLYPH + 10
		: 0;
	const topH = model.colLabels.length > 0 ? LABEL_ROW : 0;
	const fit =
		width === null || model.cols === 0
			? cellSize
			: Math.floor((width - labelW + gap) / model.cols - gap);
	const size = clamp(Math.min(cellSize, fit), 4, 40);
	const pitch = size + gap;
	const ditherCell = cell ?? size / 4;
	const solid = forced || ditherCell < MIN_DITHER_CELL;
	const tones = useMemo(() => levelTones(levels), [levels]);

	// Levels 1…levels-1 are patterns; the top level is the plain colour.
	const fills = useMemo<DitherFill[]>(
		() =>
			solid
				? []
				: tones
						.slice(1, levels)
						.map((tone) => inkFill(tone, css, ditherCell, matrix)),
		[solid, tones, levels, css, ditherCell, matrix],
	);
	const urls = useDitherFillUrls(fills);

	const paintFor = useCallback(
		(level: number) => {
			if (level <= 0) return { fill: "none" as const, opacity: 1 };
			if (level >= levels) return { fill: css, opacity: 1 };
			if (solid) return { fill: css, opacity: tones[level] ?? 1 };
			return { fill: urls[level - 1] ?? css, opacity: 1 };
		},
		[levels, css, solid, tones, urls],
	);

	const gridW = model.cols * pitch - gap;
	const gridH = model.rows * pitch - gap;
	const svgW = labelW + gridW;
	const svgH = topH + gridH;

	const cellNodes = useMemo(
		() =>
			model.cells.map((c) => {
				const paint = paintFor(c.level);
				return (
					<rect
						key={`${c.col}:${c.row}`}
						data-ripple={c.col + c.row}
						transform={`translate(${labelW + c.col * pitch} ${topH + c.row * pitch})`}
						x={0.5}
						y={0.5}
						width={Math.max(1, size - 1)}
						height={Math.max(1, size - 1)}
						rx={Math.min(2, size / 5)}
						fill={paint.fill}
						fillOpacity={paint.opacity}
						stroke="var(--dither-track)"
						strokeWidth={1}
						style={{
							opacity: focusLevel === null || focusLevel === c.level ? 1 : 0.2,
							transition: "opacity 150ms ease-out",
						}}
					/>
				);
			}),
		[model, paintFor, labelW, topH, pitch, size, focusLevel],
	);

	// The two densest levels' cells as one path, for the glow behind them.
	const glow = useMemo(
		() =>
			bloom === "off"
				? ""
				: model.cells
						.filter((c) => c.level >= Math.max(1, levels - 1))
						.map((c) =>
							rectPath(
								labelW + c.col * pitch,
								topH + c.row * pitch,
								size,
								size,
							),
						)
						.join(""),
		[bloom, model, levels, labelW, topH, pitch, size],
	);

	const ready = width !== null && model.cells.length > 0;

	// The ripple: each square fades in with a delay that grows toward the bottom right.
	useLayoutEffect(() => {
		if (!entrance.go || !ready) return;
		const nodes = svgRef.current?.querySelectorAll<SVGElement>("[data-ripple]");
		if (!nodes) return;
		const animations = Array.from(nodes, (node) =>
			node.animate([{ opacity: 0 }, { opacity: 1 }], {
				duration: 260,
				delay: Math.min(900, Number(node.dataset.ripple ?? 0) * 14),
				easing: "ease-out",
				fill: "backwards",
			}),
		);
		return () => {
			for (const animation of animations) animation.cancel();
		};
	}, [entrance.go, ready]);

	const colStep = useMemo(() => {
		const longest = Math.max(1, ...model.colLabels.map((l) => l.label.length));
		return Math.max(1, Math.ceil((longest * GLYPH + 6) / pitch));
	}, [model.colLabels, pitch]);

	const locate = (e: PointerEvent<SVGSVGElement>) => {
		const box = svgRef.current?.getBoundingClientRect();
		if (!box) return;
		const col = Math.floor((e.clientX - box.left - labelW) / pitch);
		const row = Math.floor((e.clientY - box.top - topH) / pitch);
		const hit =
			col >= 0 && col < model.cols && row >= 0 && row < model.rows
				? model.index[row * model.cols + col]
				: undefined;
		setActive(hit ?? null);
	};

	const move = (dc: number, dr: number) => {
		const current = active === null ? undefined : model.cells[active];
		if (!current) {
			setActive(model.cells.length > 0 ? 0 : null);
			return;
		}
		let col = current.col + dc;
		let row = current.row + dr;
		while (col >= 0 && col < model.cols && row >= 0 && row < model.rows) {
			const hit = model.index[row * model.cols + col];
			if (hit !== undefined) {
				setActive(hit);
				return;
			}
			col += dc;
			row += dr;
		}
	};
	const onKey = (e: KeyboardEvent) => {
		const run = (): boolean => {
			switch (e.key) {
				case "ArrowLeft":
					move(-1, 0);
					return true;
				case "ArrowRight":
					move(1, 0);
					return true;
				case "ArrowUp":
					move(0, -1);
					return true;
				case "ArrowDown":
					move(0, 1);
					return true;
				case "Home":
					setActive(model.cells.length > 0 ? 0 : null);
					return true;
				case "End":
					setActive(model.cells.length > 0 ? model.cells.length - 1 : null);
					return true;
				case "Escape":
					setActive(null);
					setLockedLevel(null);
					return true;
				default:
					return false;
			}
		};
		if (run()) {
			e.preventDefault();
			e.stopPropagation();
		}
	};

	const activeCell = active === null ? undefined : model.cells[active];
	const valueText = (v: number | null) => (v === null ? "No data" : fmt(v));
	const unitFor = (v: number | null) => unitWord(unit, v);
	const readoutText = (c: HeatCell) =>
		`${c.title}: ${c.value === null ? "No data" : `${fmt(c.value)} ${unitFor(c.value)}`}`;

	// "Activity: Oct 12, 2025 to Oct 12, 2026, 4,210 min in total, busiest Tue, Mar 3, 2026 with 212 min."
	const summary = useMemo(() => {
		const withData = model.cells.filter((c) => c.value !== null);
		if (withData.length === 0) return `${label}: no data`;
		const total = withData.reduce((sum, c) => sum + (c.value ?? 0), 0);
		let busiest = withData[0];
		for (const c of withData)
			if ((c.value ?? 0) > (busiest?.value ?? 0)) busiest = c;
		const first = model.cells[0];
		const last = model.cells.at(-1);
		const range =
			kind === "calendar" && first && last
				? `${first.title} to ${last.title}, `
				: "";
		return `${label}: ${range}${fmt(total)} ${unitWord(unit, total)} in total, busiest ${busiest?.title ?? ""} with ${fmt(busiest?.value ?? 0)}`;
	}, [model.cells, label, kind, unit, fmt]);

	const plot = ready ? (
		<svg
			ref={svgRef}
			width={svgW}
			height={svgH}
			className="block overflow-visible"
			aria-hidden
			onPointerMove={tooltipOn ? locate : undefined}
			onPointerDown={tooltipOn ? locate : undefined}
			onPointerLeave={tooltipOn ? () => setActive(null) : undefined}
		>
			{model.rowLabels.map((name, row) =>
				name === "" ? null : (
					<text
						key={name}
						x={labelW - 6}
						y={topH + row * pitch + size / 2}
						dy="0.32em"
						textAnchor="end"
						className={AXIS_TEXT}
					>
						{name}
					</text>
				),
			)}
			{model.colLabels.map((l) =>
				l.centre && l.col % colStep !== 0 ? null : (
					<text
						key={`c${l.col}`}
						x={labelW + l.col * pitch + (l.centre ? size / 2 : 0)}
						y={topH - 5}
						textAnchor={l.centre ? "middle" : "start"}
						className={AXIS_TEXT}
					>
						{l.label}
					</text>
				),
			)}
			<Bloom bloom={bloom} box={{ width: svgW, height: svgH }}>
				<path d={glow} fill={css} />
			</Bloom>
			<g style={entrance.armed && !entrance.go ? { opacity: 0 } : undefined}>
				{cellNodes}
			</g>
			{activeCell ? (
				<rect
					x={labelW + activeCell.col * pitch - 1}
					y={topH + activeCell.row * pitch - 1}
					width={size + 2}
					height={size + 2}
					rx={Math.min(3, size / 4)}
					fill="none"
					strokeWidth={1.5}
					className="pointer-events-none stroke-ink-1"
				/>
			) : null}
		</svg>
	) : null;

	const rampStep = (level: number) => {
		const paint = paintFor(level);
		return (
			<svg
				width={10}
				height={10}
				aria-hidden
				className="block shrink-0 overflow-visible"
			>
				<rect
					x={0.5}
					y={0.5}
					width={9}
					height={9}
					rx={2}
					fill={paint.fill}
					fillOpacity={paint.opacity}
					stroke="var(--dither-track)"
					strokeWidth={1}
				/>
			</svg>
		);
	};

	// A literal-role props object per mode, spread onto the root: the
	// interactive chart is a group around a keyboard-driven button, the
	// static one a labelled image.
	const rootProps = interactive
		? { role: "group" as const, "aria-label": summary }
		: { role: "img" as const, "aria-label": summary };

	return (
		<div
			ref={rootRef}
			{...rootProps}
			className={cn(
				"relative flex min-w-0 flex-col gap-2.5 rounded-ctl has-[:focus-visible]:focus-ring",
				className,
			)}
		>
			<div className="relative" style={ready ? { height: svgH } : undefined}>
				{plot && tooltipOn ? (
					<button
						type="button"
						aria-label={`${label}. Use the arrow keys to inspect cells.`}
						aria-describedby={`${uid}-keys`}
						onKeyDown={onKey}
						onBlur={() => setActive(null)}
						className="block rounded-ctl outline-none"
					>
						{plot}
					</button>
				) : (
					plot
				)}
				{tooltipOn && ready && activeCell ? (
					<ChartReadout
						title={activeCell.title}
						rows={[
							{
								label: unitFor(activeCell.value),
								color: css,
								value: valueText(activeCell.value),
							},
						]}
						style={{
							left: clamp(
								labelW + activeCell.col * pitch + size / 2 - 56,
								0,
								Math.max(0, (width ?? 0) - 130),
							),
							top:
								topH + activeCell.row * pitch >= 56
									? topH + activeCell.row * pitch - 56
									: topH + activeCell.row * pitch + size + 6,
						}}
					/>
				) : null}
			</div>
			{legend ? (
				<div
					className="flex items-center gap-1.5 font-mono text-2xs text-ink-3 leading-none"
					style={{ paddingLeft: labelW }}
				>
					<span>Less</span>
					{interactive ? (
						<ul className="flex items-center gap-[2px]" aria-label="Scale">
							{Array.from({ length: levels + 1 }, (_, level) => (
								<li
									// biome-ignore lint/suspicious/noArrayIndexKey: a fixed-length legend ramp that is never reordered
									key={level}
								>
									<button
										type="button"
										aria-pressed={lockedLevel === level}
										aria-label={levelRangeText(level, model.max, levels, fmt)}
										title={levelRangeText(level, model.max, levels, fmt)}
										onPointerEnter={() => setLitLevel(level)}
										onPointerLeave={() => setLitLevel(null)}
										onFocus={() => setLitLevel(level)}
										onBlur={() => setLitLevel(null)}
										onClick={() =>
											setLockedLevel((cur) => (cur === level ? null : level))
										}
										onKeyDown={(e) => {
											if (e.key === "Escape") setLockedLevel(null);
										}}
										className={cn(
											"block cursor-pointer rounded-[3px] p-[1px] outline-none focus-visible:focus-ring",
											lockedLevel === level && "bg-line-strong",
										)}
									>
										{rampStep(level)}
									</button>
								</li>
							))}
						</ul>
					) : (
						<svg
							width={(levels + 1) * 12 - 2}
							height={10}
							aria-hidden
							className="block shrink-0"
						>
							{Array.from({ length: levels + 1 }, (_, level) => {
								const paint = paintFor(level);
								return (
									<rect
										// biome-ignore lint/suspicious/noArrayIndexKey: a fixed-length legend ramp that is never reordered
										key={level}
										transform={`translate(${level * 12} 0)`}
										x={0.5}
										y={0.5}
										width={9}
										height={9}
										rx={2}
										fill={paint.fill}
										fillOpacity={paint.opacity}
										stroke="var(--dither-track)"
										strokeWidth={1}
									/>
								);
							})}
						</svg>
					)}
					<span>More</span>
					{model.max > 0 ? (
						<span className="ml-1.5 text-ink-4 tabular-nums">
							max {fmt(model.max)}
						</span>
					) : null}
				</div>
			) : null}
			{interactive ? (
				<ChartLive
					id={`${uid}-keys`}
					text={activeCell ? readoutText(activeCell) : ""}
				/>
			) : null}
			<table className="sr-only">
				<caption>{label}</caption>
				{kind === "calendar" ? (
					<>
						<thead>
							<tr>
								<th scope="col">Date</th>
								<th scope="col">{unitPlural(unit)}</th>
							</tr>
						</thead>
						<tbody>
							{model.cells.map((c) => (
								<tr key={`${c.col}:${c.row}`}>
									<th scope="row">{c.title}</th>
									<td>{valueText(c.value)}</td>
								</tr>
							))}
						</tbody>
					</>
				) : (
					<>
						<thead>
							<tr>
								<td />
								{(columns ?? []).map((name) => (
									<th key={name} scope="col">
										{name}
									</th>
								))}
							</tr>
						</thead>
						<tbody>
							{(rows ?? []).map((name, r) => (
								<tr key={name}>
									<th scope="row">{name}</th>
									{(columns ?? []).map((colName, c) => {
										const cellModel = model.cells[r * model.cols + c];
										return (
											<td key={colName}>
												{valueText(cellModel?.value ?? null)}
											</td>
										);
									})}
								</tr>
							))}
						</tbody>
					</>
				)}
			</table>
		</div>
	);
}
