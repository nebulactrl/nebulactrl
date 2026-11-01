import type { EventView } from "#/api/gen/types.gen";

/**
 * One raw event of a deployment with its clock arithmetic: where it sits on
 * the wall clock, how long after the deployment began, and how long after the
 * event before it. The folded Steps view hides most of these (every
 * `started`, every status transition); the debug panel lists them all.
 */
export interface TimelineRow {
	id: number;
	/** The agent's step id, or "status" for a status transition. */
	step: string;
	/** `started`, `succeeded`, `failed`, … or, for a status row, the new status. */
	status: string;
	message: string;
	/** The timestamp as the API sent it. */
	at: string;
	/** Epoch milliseconds of `at`; null when it does not parse. */
	atMs: number | null;
	/** Milliseconds since the origin (the deployment's start); null without both clocks. */
	sinceStart: number | null;
	/** Milliseconds since the previous row; null for the first row or without both clocks. */
	sinceLast: number | null;
}

export interface TimelineSummary {
	count: number;
	/** Origin to the last event; null when no event has a clock. */
	totalMs: number | null;
	/** The longest wait between two consecutive events. */
	slowest: { id: number; ms: number } | null;
}

function parse(iso: string | null | undefined): number | null {
	if (!iso) return null;
	const ms = new Date(iso).getTime();
	return Number.isNaN(ms) ? null : ms;
}

/**
 * The raw events in the order the control plane recorded them (event ids
 * rise with time), each with its offset from `originIso` (the deployment's
 * start; the first event when omitted) and from the previous event.
 */
export function buildTimeline(
	events: readonly EventView[],
	originIso?: string | null,
): TimelineRow[] {
	const ordered = [...events].sort((a, b) => a.id - b.id);
	const first = ordered[0];
	const origin = parse(originIso) ?? (first ? parse(first.createdAt) : null);
	let previous: number | null = null;
	return ordered.map((event) => {
		const atMs = parse(event.createdAt);
		const row: TimelineRow = {
			id: event.id,
			step: event.step,
			status: event.status,
			message: event.message,
			at: event.createdAt,
			atMs,
			sinceStart: atMs !== null && origin !== null ? atMs - origin : null,
			sinceLast: atMs !== null && previous !== null ? atMs - previous : null,
		};
		if (atMs !== null) previous = atMs;
		return row;
	});
}

/** The totals above the timeline: how many events, how long, and where the time went. */
export function summarizeTimeline(
	rows: readonly TimelineRow[],
): TimelineSummary {
	let slowest: TimelineSummary["slowest"] = null;
	let totalMs: number | null = null;
	for (const row of rows) {
		if (row.sinceStart !== null) totalMs = row.sinceStart;
		if (
			row.sinceLast !== null &&
			(slowest === null || row.sinceLast > slowest.ms)
		) {
			slowest = { id: row.id, ms: row.sinceLast };
		}
	}
	return { count: rows.length, totalMs, slowest };
}

/**
 * A signed offset with millisecond precision: "+84 ms", "+1.204 s",
 * "+1 m 04.200 s". A negative offset (clocks that disagree) is "−12 ms".
 * Empty for a missing one, so the cell stays blank rather than claiming zero.
 */
export function formatOffset(ms: number | null): string {
	if (ms === null) return "";
	const sign = ms < 0 ? "−" : "+";
	const value = Math.abs(Math.round(ms));
	if (value < 1_000) return `${sign}${value} ms`;
	if (value < 60_000) return `${sign}${(value / 1_000).toFixed(3)} s`;
	const minutes = Math.floor(value / 60_000);
	const seconds = ((value % 60_000) / 1_000).toFixed(3).padStart(6, "0");
	return `${sign}${minutes} m ${seconds} s`;
}

/** Local wall-clock time with milliseconds: "14:02:11.482"; empty when it does not parse. */
export function clockWithMs(iso: string | null | undefined): string {
	const ms = parse(iso);
	if (ms === null) return "";
	const date = new Date(ms);
	const pad = (n: number, width = 2) => String(n).padStart(width, "0");
	return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`;
}
