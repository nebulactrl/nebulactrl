import { cn } from "cn";
import {
	type CSSProperties,
	type KeyboardEvent,
	type PointerEvent,
	useId,
	useState,
} from "react";
import { formatMs } from "#/features/metrics/chart-format";
import {
	type Cell,
	type CellTone,
	cellError,
	cellTime,
	checksLine,
	emptyRunTime,
	emptyRunWords,
	formatAvailability,
	type Granularity,
	type OutageLike,
	rangeStartLabel,
	readoutRows,
	stripSummary,
	tonesPresent,
	toneWords,
	type UptimeRange,
} from "./model";
import { OPERATIONAL_COLOR } from "./tones";

/**
 * How each tone is drawn. Operational is a quiet tint; degraded and down are
 * full strength. Colour is never alone: a degraded cell is striped, a paused
 * one hatched and grey, and a cell with no data is a short grey stub, so the
 * strip reads in forced colours and for colour-blind readers too. The words
 * are in the legend and the tooltip.
 */
const TONE_CLASS: Record<CellTone, string> = {
	ok: "",
	warn: "bg-warn",
	bad: "bg-bad",
	info: "bg-info/80",
	paused: "bg-ink-4/20",
	none: "bg-ink-4/30",
};

const STRIPES: CSSProperties = {
	backgroundImage:
		"repeating-linear-gradient(135deg, rgb(2 4 10 / 0.38) 0 2px, transparent 2px 5px)",
};

const HATCH: CSSProperties = {
	backgroundImage:
		"repeating-linear-gradient(135deg, var(--color-ink-3) 0 1px, transparent 1px 4px)",
};

const TONE_STYLE: Partial<Record<CellTone, CSSProperties>> = {
	ok: { backgroundColor: OPERATIONAL_COLOR },
	warn: STRIPES,
	paused: HATCH,
};

/** A small square that shows how a tone is drawn, for the legend. */
function Swatch({ tone }: { tone: CellTone }) {
	return (
		<span
			aria-hidden
			style={TONE_STYLE[tone]}
			className={cn(
				"inline-block size-2.5 shrink-0 rounded-[2px]",
				TONE_CLASS[tone],
			)}
		/>
	);
}

/**
 * One legend for every bar that shares it: the tones present in `cells`,
 * worst first, so a state no cell has is not listed.
 */
export function ToneLegend({ cells }: { cells: readonly Cell[] }) {
	return (
		<ul
			aria-label="How the cells are drawn"
			className="flex flex-wrap gap-x-3 gap-y-1 text-2xs text-ink-3 leading-none"
		>
			{tonesPresent(cells).map((tone) => (
				<li key={tone} className="flex items-center gap-1.5">
					<Swatch tone={tone} />
					{toneWords(tone)}
				</li>
			))}
		</ul>
	);
}

export interface UptimeStripProps {
	cells: readonly Cell[];
	granularity: Granularity;
	range: UptimeRange;
	/** Names the strip for assistive tech: "Recent checks of api". */
	label: string;
	/** Outages that explain a cell's error. */
	outages: readonly OutageLike[];
	lastCheckAt: string | null;
	lastError: string;
}

/**
 * The recent-checks strip: one cell per bucket of the range (a minute for 1h,
 * an hour for 24h and 7d, a UTC day beyond), coloured by the worst state in
 * the bucket. Hover or the arrow keys show a bucket's time, checks, latency
 * and error. The whole strip is one tab stop; a hidden table lists every cell.
 */
export function UptimeStrip({
	cells,
	granularity,
	range,
	label,
	outages,
	lastCheckAt,
	lastError,
}: UptimeStripProps) {
	const id = useId();
	const [active, setActive] = useState<number | null>(null);
	const count = cells.length;
	const current = active === null ? undefined : cells[active];

	const point = (event: PointerEvent<HTMLDivElement>) => {
		const box = event.currentTarget.getBoundingClientRect();
		if (box.width <= 0 || count === 0) return;
		const slot = Math.floor(((event.clientX - box.left) / box.width) * count);
		setActive(Math.min(count - 1, Math.max(0, slot)));
	};

	const key = (event: KeyboardEvent<HTMLDivElement>) => {
		if (count === 0) return;
		const at = active ?? count - 1;
		const next =
			event.key === "ArrowLeft"
				? at - 1
				: event.key === "ArrowRight"
					? at + 1
					: event.key === "Home"
						? 0
						: event.key === "End"
							? count - 1
							: null;
		if (next === null) {
			if (event.key === "Escape") setActive(null);
			return;
		}
		event.preventDefault();
		setActive(Math.min(count - 1, Math.max(0, next)));
	};

	if (count === 0) return null;

	const edge = active === null ? 0.5 : (active + 0.5) / count;
	const toStart = active !== null && active < 6;
	const toEnd = active !== null && active > count - 7;
	const error = current
		? cellError(current, outages, { at: lastCheckAt, error: lastError })
		: "";

	return (
		<div className="flex flex-col gap-1.5">
			<div className="relative">
				{/* biome-ignore lint/a11y/useSemanticElements: a group of cells with one tab stop and arrow keys; the data is in the hidden table */}
				<div
					role="group"
					aria-label={`${label}: ${stripSummary(cells, range)}`}
					aria-describedby={current ? `${id}-tip` : undefined}
					// biome-ignore lint/a11y/noNoninteractiveTabindex: the strip is read with the arrow keys
					tabIndex={0}
					onPointerMove={point}
					onPointerDown={point}
					onPointerLeave={() => setActive(null)}
					onKeyDown={key}
					onFocus={(event) => {
						if (
							active === null &&
							event.currentTarget.matches(":focus-visible")
						) {
							setActive(count - 1);
						}
					}}
					onBlur={() => setActive(null)}
					className="grid h-7 cursor-default touch-pan-y items-end gap-[2px] rounded-[3px] outline-none focus-visible:focus-ring"
					style={{ gridTemplateColumns: `repeat(${count}, minmax(0, 1fr))` }}
				>
					{cells.map((cell, index) => (
						<span
							key={cell.at}
							style={TONE_STYLE[cell.tone]}
							className={cn(
								"block rounded-[2px]",
								cell.tone === "none" ? "h-2" : "h-full",
								TONE_CLASS[cell.tone],
								index === active && "ring-2 ring-ink-1",
							)}
						/>
					))}
				</div>
				{current ? (
					<div
						id={`${id}-tip`}
						role="tooltip"
						style={{ left: `${edge * 100}%` }}
						className={cn(
							"surface-pop pointer-events-none absolute bottom-full z-20 mb-2 flex w-max max-w-[16rem] flex-col gap-1 rounded-tip px-2.5 py-2 text-xs leading-[1.35]",
							toStart ? "" : toEnd ? "-translate-x-full" : "-translate-x-1/2",
						)}
					>
						<span className="font-mono text-ink-3 text-[10.5px]">
							{cellTime(current.at, granularity)}
						</span>
						<span className="flex items-center gap-1.5 font-semibold text-ink-1">
							<Swatch tone={current.tone} />
							{toneWords(current.tone)}
						</span>
						<span className="text-ink-2">{checksLine(current)}</span>
						{granularity === "day" && current.availability !== null ? (
							<span className="text-ink-2">
								{formatAvailability(current.availability)} available
							</span>
						) : null}
						{current.avgLatencyMs > 0 ? (
							<span className="text-ink-2 tabular-nums">
								{formatMs(current.avgLatencyMs)} average ·{" "}
								{formatMs(current.maxLatencyMs)} slowest
							</span>
						) : null}
						{error ? (
							<span className="break-words text-status-failed-on">{error}</span>
						) : null}
					</div>
				) : null}
			</div>
			<div className="flex items-center justify-between font-mono text-[10px] text-ink-4 leading-none">
				<span>{rangeStartLabel(range)}</span>
				<span>now</span>
			</div>
			<table className="sr-only">
				<caption>{label}</caption>
				<thead>
					<tr>
						<th scope="col">Time</th>
						<th scope="col">State</th>
						<th scope="col">Checks</th>
					</tr>
				</thead>
				<tbody>
					{readoutRows(cells).map((row) =>
						"kind" in row ? (
							<tr key={row.from.at}>
								<th scope="row">{emptyRunTime(row, granularity)}</th>
								<td>{toneWords("none")}</td>
								<td>{emptyRunWords(row, granularity)}</td>
							</tr>
						) : (
							<tr key={row.at}>
								<th scope="row">{cellTime(row.at, granularity)}</th>
								<td>{toneWords(row.tone)}</td>
								<td>{checksLine(row)}</td>
							</tr>
						),
					)}
				</tbody>
			</table>
		</div>
	);
}
