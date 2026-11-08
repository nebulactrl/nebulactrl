import { cn } from "cn";
import { type PointerEvent, useRef, useState } from "react";
import { STAMP_LOCALE } from "#/features/logs/history/model";
import type { VolumePoint } from "./server-stats";

/** A span of time the reader dragged across the chart; RFC 3339 instants. */
interface VolumeSpan {
	from: string;
	to: string;
}

export interface VolumeHistogramProps {
	series: readonly VolumePoint[];
	/** The range in words, after "in": "the last 15 minutes". */
	rangeText: string;
	/** Dragging across two or more buckets chose their span. */
	onSelectSpan(span: VolumeSpan): void;
	/**
	 * What the series is: `ready`, `loading` (the first counts have not come:
	 * a skeleton, not "no lines") or `unavailable` (the counts failed, with
	 * `reason` saying why).
	 */
	status?: "ready" | "loading" | "unavailable";
	reason?: string;
	className?: string;
}

function amount(n: number, word: string): string {
	return `${n.toLocaleString(STAMP_LOCALE)} ${word}${n === 1 ? "" : "s"}`;
}

function stampOf(ms: number): string {
	const date = new Date(ms);
	const day = date.toLocaleDateString(STAMP_LOCALE, {
		day: "numeric",
		month: "short",
	});
	return `${day} ${date.toLocaleTimeString(STAMP_LOCALE, { hour12: false })}`;
}

/** "10:02:11", the clock time of a bucket edge. */
function clockOf(ms: number): string {
	return new Date(ms).toLocaleTimeString(STAMP_LOCALE, { hour12: false });
}

/** The heights of the loading skeleton's bars, in percent. */
const SKELETON_BARS = [30, 45, 38, 60, 52, 70, 48, 64, 40, 55, 36, 62, 50, 44];

const NO_SERIES: readonly VolumePoint[] = [];

/** "1,240 lines, 12 errors in the last 15 minutes", for a screen reader and the empty chart. */
function summaryOf(series: readonly VolumePoint[], rangeText: string): string {
	let lines = 0;
	let errors = 0;
	for (const point of series) {
		lines += point.count;
		errors += point.errors;
	}
	return `${amount(lines, "line")}, ${amount(errors, "error")} in ${rangeText}`;
}

/**
 * How many lines matched over the range, one bar per bucket: the errors in the
 * failed colour at the foot of each bar, the rest muted. Hovering a bar says
 * its time and counts; dragging across several selects that span (the
 * explorer then shows it as a stored history). The numbers are in a summary
 * for assistive tech, and the range picker is the keyboard's way to a span.
 */
export function VolumeHistogram({
	series: counted,
	rangeText,
	onSelectSpan,
	status = "ready",
	reason,
	className,
}: VolumeHistogramProps) {
	const chartRef = useRef<HTMLDivElement>(null);
	const [hover, setHover] = useState<number | null>(null);
	const [drag, setDrag] = useState<{ from: number; to: number } | null>(null);
	// Counts that are still coming, or failed, are not a chart of nothing.
	const series = status === "ready" ? counted : NO_SERIES;
	const summary =
		status === "loading"
			? "Loading counts"
			: status === "unavailable"
				? `Counts unavailable${reason ? `: ${reason}` : ""}`
				: summaryOf(series, rangeText);
	const peak = series.reduce((most, point) => Math.max(most, point.count), 0);
	const buckets = series.length;
	// The bucket edges the labels name: the first start, the middle, the end of the last bucket.
	const first = series[0];
	const last = series[buckets - 1];
	const bucketWidth = first && series[1] ? series[1].start - first.start : 0;
	const edges =
		first && last
			? { start: first.start, end: last.start + bucketWidth }
			: undefined;
	const edgeText = (ms: number) =>
		edges && edges.end - edges.start >= 86_400_000 ? stampOf(ms) : clockOf(ms);

	const bucketAt = (event: PointerEvent): number => {
		const rect = chartRef.current?.getBoundingClientRect();
		const width = rect?.width || 1;
		const x = (event.clientX - (rect?.left ?? 0)) / width;
		return Math.min(buckets - 1, Math.max(0, Math.floor(x * buckets)));
	};

	const finish = (event: PointerEvent) => {
		if (!drag) return;
		const at = bucketAt(event);
		const first = Math.min(drag.from, at);
		const last = Math.max(drag.from, at);
		setDrag(null);
		const start = series[first];
		const end = series[last];
		if (!start || !end || first === last) return;
		// The last bucket has no successor: it is as wide as the one before it.
		const next = series[last + 1];
		const before = series[last - 1];
		const width = next
			? next.start - end.start
			: end.start - (before?.start ?? end.start);
		onSelectSpan({
			from: new Date(start.start).toISOString(),
			to: new Date(end.start + width).toISOString(),
		});
	};

	const shown = hover === null ? undefined : series[hover];
	const lo = drag ? Math.min(drag.from, drag.to) : 0;
	const hi = drag ? Math.max(drag.from, drag.to) : 0;

	return (
		<div className={cn("relative", className)}>
			<p className="sr-only">{summary}</p>
			<div
				ref={chartRef}
				aria-hidden
				data-testid="volume-chart"
				className={cn(
					"surface-well relative flex h-14 touch-none select-none items-end gap-px overflow-hidden rounded-card px-1 pt-1",
					buckets > 1 && "cursor-crosshair",
				)}
				onPointerDown={(event) => {
					if (buckets < 2 || event.button !== 0) return;
					event.currentTarget.setPointerCapture?.(event.pointerId);
					const at = bucketAt(event);
					setDrag({ from: at, to: at });
				}}
				onPointerMove={(event) => {
					if (buckets === 0) return;
					const at = bucketAt(event);
					setHover(at);
					if (drag) setDrag({ from: drag.from, to: at });
				}}
				onPointerUp={finish}
				onPointerCancel={() => setDrag(null)}
				onLostPointerCapture={() => setDrag(null)}
				onPointerLeave={() => setHover(null)}
			>
				{series.map((point, index) => {
					// A non-empty bucket never drops under 2px, however small next to the peak.
					const height = point.count === 0 ? 0 : (point.count / peak) * 100;
					const share =
						point.count === 0 ? 0 : (point.errors / point.count) * 100;
					return (
						<div
							key={point.start}
							className={cn(
								"flex h-full min-w-0 flex-1 items-end rounded-t-[1px]",
								hover === index && "bg-white/5",
							)}
						>
							<div
								className="flex w-full flex-col justify-end overflow-hidden rounded-t-[1px] transition-[height] duration-quick ease-out motion-reduce:transition-none"
								style={{
									height: `${height}%`,
									minHeight: point.count === 0 ? 0 : 2,
								}}
							>
								<div
									className="bg-ink-4"
									style={{ height: `${100 - share}%` }}
								/>
								<div
									className="bg-status-failed"
									style={{ height: `${share}%` }}
								/>
							</div>
						</div>
					);
				})}
				{drag ? (
					<div
						className="pointer-events-none absolute inset-y-0 border-accent-ink border-x bg-accent-wash"
						style={{
							left: `${(lo / buckets) * 100}%`,
							width: `${((hi - lo + 1) / buckets) * 100}%`,
						}}
					/>
				) : null}
				{status === "loading" ? (
					<div className="absolute inset-0 flex items-end gap-px px-1 pt-1">
						{SKELETON_BARS.map((percent, index) => (
							<div
								// biome-ignore lint/suspicious/noArrayIndexKey: a fixed decoration
								key={index}
								className="min-w-0 flex-1 rounded-t-[1px] bg-ink-4/20 motion-safe:animate-pulse"
								style={{ height: `${percent}%` }}
							/>
						))}
						<p className="absolute inset-0 grid place-items-center text-ink-3 text-xs">
							Loading counts…
						</p>
					</div>
				) : status === "unavailable" ? (
					<p className="absolute inset-0 grid place-items-center px-3 text-center text-ink-3 text-xs">
						Counts unavailable{reason ? ` · ${reason}` : ""}
					</p>
				) : peak === 0 ? (
					<p className="absolute inset-0 grid place-items-center text-ink-4 text-xs">
						No matching lines in {rangeText}
					</p>
				) : null}
			</div>
			{edges ? (
				<div
					aria-hidden
					data-testid="volume-labels"
					className="mt-1 grid grid-cols-3 px-1 text-[11px] text-ink-4 tabular-nums"
				>
					<span>{edgeText(edges.start)}</span>
					<span className="text-center">
						{edgeText((edges.start + edges.end) / 2)}
					</span>
					<span className="text-right">{edgeText(edges.end)}</span>
				</div>
			) : null}
			{shown && hover !== null ? (
				<div
					className="surface-pop pointer-events-none absolute bottom-[calc(100%+4px)] z-10 -translate-x-1/2 whitespace-nowrap rounded-tip px-2 py-1.5 text-ink-1 text-xs tabular-nums"
					style={{
						left: `clamp(80px, ${((hover + 0.5) / buckets) * 100}%, calc(100% - 80px))`,
					}}
				>
					<span className="font-mono text-ink-3">{stampOf(shown.start)}</span>
					<span className="ml-2 font-medium">
						{amount(shown.count, "line")}
					</span>
					<span
						className={cn(
							"ml-2",
							shown.errors > 0 ? "text-status-failed-on" : "text-ink-4",
						)}
					>
						{amount(shown.errors, "error")}
					</span>
				</div>
			) : null}
		</div>
	);
}
