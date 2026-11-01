// Pure parts of the stored-log history view: the time range and how it is
// written in the address, merging the server's pages into one chronological
// list, mapping entries to the lines `LogView` draws, and the sentences for
// the states a history can be in. No React and no network, so it is tested.

import type {
	LogHistoryEntry,
	LogHistoryPage,
	LogHistoryUnavailable,
} from "#/api/gen/types.gen";
import type { LogLine } from "#/api/sse";

// --- Time range ----------------------------------------------------------------

export const RANGE_PRESETS = [
	{ id: "15m", label: "Last 15 minutes", ms: 15 * 60_000 },
	{ id: "1h", label: "Last hour", ms: 60 * 60_000 },
	{ id: "6h", label: "Last 6 hours", ms: 6 * 60 * 60_000 },
	{ id: "24h", label: "Last 24 hours", ms: 24 * 60 * 60_000 },
	{ id: "7d", label: "Last 7 days", ms: 7 * 24 * 60 * 60_000 },
] as const;

type RangePresetId = (typeof RANGE_PRESETS)[number]["id"];

const DEFAULT_PRESET: RangePresetId = "1h";

/** A preset counted back from now, or two instants (either may be open: no `to` is "now"). */
export type RangeSelection =
	| { kind: "preset"; id: RangePresetId }
	| { kind: "custom"; from: string | undefined; to: string | undefined };

/** The range as the address spells it: `?range=1h`, or `?from=&to=` (ISO instants). */
export type RangeParams = {
	range?: string | undefined;
	from?: string | undefined;
	to?: string | undefined;
};

function presetOf(id: unknown): (typeof RANGE_PRESETS)[number] | undefined {
	return RANGE_PRESETS.find((preset) => preset.id === id);
}

function instant(value: unknown): string | undefined {
	if (typeof value !== "string" || value === "") return undefined;
	const time = Date.parse(value);
	return Number.isNaN(time) ? undefined : new Date(time).toISOString();
}

/**
 * The range the address names. `?from=` or `?to=` make a custom range and win
 * over `?range=`; an unknown preset or an unreadable date is dropped, and an
 * empty address is the last hour. A `to` before the `from` is dropped too.
 */
export function parseRange(params: Record<string, unknown>): RangeSelection {
	const from = instant(params.from);
	let to = instant(params.to);
	if (
		from !== undefined &&
		to !== undefined &&
		Date.parse(to) <= Date.parse(from)
	) {
		to = undefined;
	}
	if (from !== undefined || to !== undefined) {
		return { kind: "custom", from, to };
	}
	const preset = presetOf(params.range);
	return { kind: "preset", id: preset?.id ?? DEFAULT_PRESET };
}

/** What to write into the address for a range; absent keys are removed. */
export function rangeParams(selection: RangeSelection): RangeParams {
	if (selection.kind === "preset") {
		return { range: selection.id, from: undefined, to: undefined };
	}
	return { range: undefined, from: selection.from, to: selection.to };
}

export interface ResolvedRange {
	/** RFC 3339; absent: from the oldest line the server keeps. */
	from: string | undefined;
	/** RFC 3339. Always set, so pages of one search stay consistent while lines arrive. */
	to: string;
}

/**
 * The instants a range means at `now` (ms). A range that reaches "now" is
 * pinned to the moment it was asked for; Refresh asks again with a new `now`.
 */
export function resolveRange(
	selection: RangeSelection,
	now: number,
): ResolvedRange {
	if (selection.kind === "preset") {
		const ms = presetOf(selection.id)?.ms ?? 60 * 60_000;
		return {
			from: new Date(now - ms).toISOString(),
			to: new Date(now).toISOString(),
		};
	}
	return {
		from: selection.from,
		to: selection.to ?? new Date(now).toISOString(),
	};
}

/** Whether Refresh has anything new to fetch without the range itself changing. */
export function reachesNow(selection: RangeSelection): boolean {
	return selection.kind === "preset" || selection.to === undefined;
}

/**
 * The product's interface is English, so its dates are too: the browser's
 * locale would put "27. Okt." in an English footer. Day first, 24-hour time.
 */
export const STAMP_LOCALE = "en-GB";

export interface StampOptions {
	/** Overrides the fixed interface locale (tests). */
	locale?: string;
	timeZone?: string;
}

/** "20 Oct, 14:02" in the reader's locale and zone. */
export function formatStamp(iso: string, options: StampOptions = {}): string {
	const date = new Date(iso);
	if (Number.isNaN(date.getTime())) return iso;
	const zone = options.timeZone ? { timeZone: options.timeZone } : {};
	// Date and time are formatted apart: a combined format puts " at " between them in some locales.
	const day = date.toLocaleDateString(options.locale ?? STAMP_LOCALE, {
		day: "numeric",
		month: "short",
		...zone,
	});
	const time = date.toLocaleTimeString(options.locale ?? STAMP_LOCALE, {
		hour: "2-digit",
		minute: "2-digit",
		hour12: false,
		...zone,
	});
	return `${day}, ${time}`;
}

/** The trigger's text: the preset's name, or the two ends of a custom range. */
export function rangeLabel(
	selection: RangeSelection,
	options: StampOptions = {},
): string {
	if (selection.kind === "preset") {
		return presetOf(selection.id)?.label ?? "Last hour";
	}
	const { from, to } = selection;
	if (from && to) {
		return `${formatStamp(from, options)} to ${formatStamp(to, options)}`;
	}
	if (from) return `Since ${formatStamp(from, options)}`;
	if (to) return `Until ${formatStamp(to, options)}`;
	return "Custom range";
}

const pad = (n: number) => String(n).padStart(2, "0");

/** An instant as a `datetime-local` value in the reader's zone: "2026-10-20T14:02". */
export function toLocalInput(iso: string | undefined): string {
	if (iso === undefined) return "";
	const date = new Date(iso);
	if (Number.isNaN(date.getTime())) return "";
	return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** A `datetime-local` value (the reader's zone) as an ISO instant; empty or unreadable is undefined. */
export function fromLocalInput(value: string): string | undefined {
	if (value === "") return undefined;
	const time = new Date(value).getTime();
	return Number.isNaN(time) ? undefined : new Date(time).toISOString();
}

/** What is wrong with a custom range typed into the two fields, or null. */
export function customRangeProblem(
	from: string | undefined,
	to: string | undefined,
): string | null {
	if (from === undefined && to === undefined) {
		return "Pick a start or an end.";
	}
	if (
		from !== undefined &&
		to !== undefined &&
		Date.parse(to) <= Date.parse(from)
	) {
		return "The end must be after the start.";
	}
	return null;
}

// --- Pages and lines -----------------------------------------------------------

/** Lines of one history the view keeps; past this, narrow the range or the search. */
export const HISTORY_MAX_LINES = 50_000;

/** Each result gets an id range of its own, above every earlier result's, so a new search never reuses an id. */
const ID_SPAN = 2 ** 32;

const epochs = new WeakMap<LogHistoryPage, number>();
let lastEpoch = 0;

/**
 * Which result a list of pages belongs to, by its newest page: older pages
 * added behind it keep the number, a new search or a refresh (a different
 * newest page) gets a higher one.
 */
export function epochOf(newest: LogHistoryPage): number {
	let epoch = epochs.get(newest);
	if (epoch === undefined) {
		lastEpoch += 1;
		epoch = lastEpoch;
		epochs.set(newest, epoch);
	}
	return epoch;
}

/** A stored entry as the line `LogView` and the matcher work on. */
export function toLogLine(entry: LogHistoryEntry, id: number): LogLine {
	const line: LogLine = {
		id,
		ts: entry.ts,
		line: entry.line,
		serviceId: entry.serviceId,
		replica: entry.replica,
	};
	if (entry.stream) line.stream = entry.stream;
	if (entry.serviceName) line.serviceName = entry.serviceName;
	if (entry.deploymentId) line.deploymentId = entry.deploymentId;
	return line;
}

/**
 * The server's pages (newest page first, each newest line first) as one list,
 * oldest line first. A line's id depends only on `epoch` and on how many
 * lines are newer, so it stays the same when an older page is added and the
 * list can be keyed and scroll-anchored by it, while a different result never
 * shares an id with this one.
 */
export function mergePages(
	pages: readonly LogHistoryPage[],
	epoch = 1,
): LogLine[] {
	const chunks: LogLine[][] = [];
	let top = epoch * ID_SPAN;
	for (const page of pages) {
		const items = page.items ?? [];
		const chunk: LogLine[] = new Array(items.length);
		items.forEach((entry, i) => {
			chunk[items.length - 1 - i] = toLogLine(entry, top - i);
		});
		top -= items.length;
		chunks.push(chunk);
	}
	return chunks.reverse().flat();
}

/** The first line of `lines` (ascending ids) whose id is at least `id`, or `lines.length`. */
export function lowerBound(
	lines: readonly { id: number }[],
	id: number,
): number {
	let lo = 0;
	let hi = lines.length;
	while (lo < hi) {
		const mid = (lo + hi) >>> 1;
		const at = lines[mid];
		if (at && at.id < id) lo = mid + 1;
		else hi = mid;
	}
	return lo;
}

/** What the oldest page loaded so far says about continuing. */
export type Continuation =
	| { kind: "more"; cursor: string }
	/** The server stopped scanning early: the cursor continues the same search. */
	| { kind: "partial"; cursor: string }
	| { kind: "end" }
	/** The view holds as many lines as it keeps. */
	| { kind: "full" };

export function continuation(
	pages: readonly LogHistoryPage[],
	lineCount: number,
): Continuation {
	const last = pages[pages.length - 1];
	if (!last?.nextCursor) return { kind: "end" };
	if (last.partial) return { kind: "partial", cursor: last.nextCursor };
	if (lineCount >= HISTORY_MAX_LINES) return { kind: "full" };
	return { kind: "more", cursor: last.nextCursor };
}

// --- States --------------------------------------------------------------------

export interface Failure {
	kind: "server_offline" | "agent_outdated" | "error";
	message: string;
}

function fieldOf(value: unknown, key: string): unknown {
	return typeof value === "object" && value !== null
		? Reflect.get(value, key)
		: undefined;
}

/**
 * What a failed history request means. The single-service endpoint answers
 * 503 `server_offline` or 409 `agent_outdated` instead of an `unavailable`
 * list; anything else is an error with the server's own message.
 */
export function failureOf(error: unknown): Failure {
	const code = fieldOf(error, "code");
	const status = fieldOf(error, "status");
	const text = fieldOf(error, "message");
	const message =
		typeof text === "string" && text !== ""
			? text
			: "The history could not be read.";
	if (code === "server_offline" || status === 503) {
		return { kind: "server_offline", message };
	}
	if (code === "agent_outdated" || status === 409) {
		return { kind: "agent_outdated", message };
	}
	return { kind: "error", message };
}

/** "hetzner-01", "a and b", "a, b and c". */
export function joinNames(names: readonly string[]): string {
	if (names.length <= 1) return names[0] ?? "";
	return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** The sentence for a service whose server is offline; `serverName` may be unknown. */
export function offlineSentence(serverNames: readonly string[]): string {
	if (serverNames.length === 0) {
		return "History is stored on the service's server and is unavailable while it is offline.";
	}
	return serverNames.length === 1
		? `History is stored on ${serverNames[0]} and is unavailable while it is offline.`
		: `History is stored on ${joinNames(serverNames)} and is unavailable while they are offline.`;
}

export interface UnavailableSummary {
	/** The heading sentence. */
	title: string;
	/** One row per service whose history could not be read. */
	items: {
		serviceId: string;
		serviceName: string;
		serverName: string;
		reason: LogHistoryUnavailable["reason"];
		message: string;
	}[];
}

function distinct(values: readonly string[]): string[] {
	return [...new Set(values)];
}

/** The callout over an environment's history when some services' servers could not answer. */
export function summarizeUnavailable(
	list: readonly LogHistoryUnavailable[] | null | undefined,
): UnavailableSummary | null {
	if (!list || list.length === 0) return null;
	const items = list.map((entry) => ({
		serviceId: entry.serviceId,
		serviceName: entry.serviceName,
		serverName: entry.serverName,
		reason: entry.reason,
		message: entry.message,
	}));
	const reasons = distinct(list.map((entry) => entry.reason));
	const servers = distinct(list.map((entry) => entry.serverName));
	let title: string;
	if (reasons.length === 1 && reasons[0] === "server_offline") {
		title = offlineSentence(servers);
	} else if (reasons.length === 1 && reasons[0] === "agent_outdated") {
		title = `The agent on ${joinNames(servers)} is too old to read history. Update it to see these lines.`;
	} else {
		title = `History could not be read for ${list.length === 1 ? "1 service" : `${list.length} services`}.`;
	}
	return { title, items };
}

export type EmptyKind = "none-in-range" | "no-match";

/** Which empty sentence applies: the range holds no lines, or the search matched none. */
export function emptyKind(query: string): EmptyKind {
	return query.trim() === "" ? "none-in-range" : "no-match";
}

/** "Showing 500 lines. This server keeps logs back to 20 Oct, 14:02." */
export function footerText(options: {
	lines: number;
	oldest: string | undefined;
	scope: "service" | "environment";
	stamp?: StampOptions;
	continuation: Continuation["kind"];
}): string {
	const n = options.lines.toLocaleString();
	let text = `Showing ${n} ${options.lines === 1 ? "line" : "lines"}.`;
	if (options.oldest) {
		const who =
			options.scope === "service" ? "This server keeps" : "The servers keep";
		text += ` ${who} logs back to ${formatStamp(options.oldest, options.stamp)}.`;
	}
	if (options.continuation === "full") {
		text += ` That is as many as this view keeps (${HISTORY_MAX_LINES.toLocaleString()}). Narrow the range or the search to see more.`;
	}
	return text;
}
