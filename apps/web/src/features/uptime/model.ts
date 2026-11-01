import type { UptimeDay } from "@nebulactrl/dither/charts/uptime-bar";
import type { StatusKind } from "#/components/ui/status-icon";

/**
 * The pure side of uptime: what a state is called and drawn as, how a series
 * becomes cells, how availability and latency read. The components only lay
 * out what this decides.
 */

/** What the control plane judged a service to be (`ServiceReport.state`). */
export type UptimeState =
	| "up"
	| "degraded"
	| "down"
	| "maintenance"
	| "paused"
	| "unknown";

/** The windows `GET /services/{id}/uptime` serves. */
export type UptimeRange = "1h" | "24h" | "7d" | "30d" | "90d";

/** Width of one series bucket. */
export type Granularity = "minute" | "hour" | "day";

/** How a cell is drawn: colour, plus a texture or a height where colour is not enough. */
export type CellTone = "ok" | "warn" | "bad" | "info" | "paused" | "none";

/** A check interval of the agent: the probe runs every 30 s. */
export const CHECK_INTERVAL_SECONDS = 30;

/** Failed-check and passing-check thresholds, as the monitor bounds them. */
export const MIN_THRESHOLD = 1;
export const MAX_THRESHOLD = 20;
export const MAX_REMINDER_MINUTES = 1440;

/** The range a strip opens on, and the ones the toggle offers. */
export const STRIP_RANGES = ["1h", "24h", "7d", "90d"] as const;
export type StripRange = (typeof STRIP_RANGES)[number];
export const DEFAULT_RANGE: StripRange = "1h";

const RANGE_WORDS: Record<UptimeRange, string> = {
	"1h": "the last hour",
	"24h": "the last 24 hours",
	"7d": "the last 7 days",
	"30d": "the last 30 days",
	"90d": "the last 90 days",
};

/** "the last hour", for a label that names the strip's window. */
export function rangeWords(range: UptimeRange): string {
	return RANGE_WORDS[range];
}

/** "60m ago" at the strip's left edge. */
const RANGE_START: Record<UptimeRange, string> = {
	"1h": "60m ago",
	"24h": "24h ago",
	"7d": "7d ago",
	"30d": "30d ago",
	"90d": "90d ago",
};

export function rangeStartLabel(range: UptimeRange): string {
	return RANGE_START[range];
}

/** The width of one cell of a granularity, in milliseconds. */
export const CELL_MS: Record<Granularity, number> = {
	minute: 60_000,
	hour: 3_600_000,
	day: 86_400_000,
};

// --- state -----------------------------------------------------------------

export interface StateView {
	kind: StatusKind;
	label: string;
	/** How the state colours a cell or a number. */
	tone: CellTone;
}

const STATES: Record<UptimeState, StateView> = {
	up: { kind: "live", label: "Up", tone: "ok" },
	degraded: { kind: "degraded", label: "Degraded", tone: "warn" },
	down: { kind: "failed", label: "Down", tone: "bad" },
	maintenance: { kind: "warning", label: "In maintenance", tone: "info" },
	paused: { kind: "stopped", label: "Paused", tone: "paused" },
	unknown: { kind: "unknown", label: "No data", tone: "none" },
};

/** The icon, words and tone of a state; anything the server adds later reads as no data. */
export function stateView(state: string): StateView {
	return STATES[state as UptimeState] ?? STATES.unknown;
}

/**
 * Down and degraded first, then what has gone quiet (no data), then
 * maintenance, then healthy services, and paused ones last.
 */
const SEVERITY: Record<UptimeState, number> = {
	down: 0,
	degraded: 1,
	unknown: 2,
	maintenance: 3,
	up: 4,
	paused: 5,
};

/** The sort rank of a state; lower is worse. */
export function stateSeverity(state: string): number {
	return SEVERITY[state as UptimeState] ?? SEVERITY.unknown;
}

/**
 * "Failing 2/3": failed checks in a row that have not become an outage yet.
 * Only while the committed state is still up; null otherwise. Without a
 * threshold the count stands alone.
 */
export function failingHint(report: {
	state: string;
	consecutiveFailures?: number | undefined;
	failureThreshold?: number | undefined;
}): string | null {
	const failures = report.consecutiveFailures ?? 0;
	if (report.state !== "up" || failures <= 0) return null;
	const threshold = report.failureThreshold ?? 0;
	if (threshold <= 0) return `Failing ${failures}`;
	return `Failing ${Math.min(failures, threshold)}/${threshold}`;
}

// --- availability ----------------------------------------------------------

/**
 * `99.98%`, `100%`, `0%`. Floored to two decimals, never rounded up: a
 * window with any downtime must not read `100%`. Null (nothing to judge)
 * reads "No data", which is not the same as 0%.
 */
export function formatAvailability(percent: number | null | undefined): string {
	if (percent === null || percent === undefined || Number.isNaN(percent)) {
		return "No data";
	}
	const clamped = Math.min(100, Math.max(0, percent));
	const floored = Math.floor(clamped * 100 + 1e-9) / 100;
	return `${Number(floored.toFixed(2))}%`;
}

/** The four windows the card lists, in order. */
export const AVAILABILITY_WINDOWS = ["24h", "7d", "30d", "90d"] as const;

export interface PointLike {
	at: string;
	checks: number;
	up: number;
	degraded: number;
	down: number;
	maintenance: number;
	avgLatencyMs: number;
	maxLatencyMs: number;
}

/** Passing checks over judged checks (maintenance left out), in percent; null with nothing judged. */
export function pointAvailability(point: PointLike): number | null {
	const judged = point.checks - point.maintenance;
	if (judged <= 0) return null;
	return ((point.up + point.degraded) / judged) * 100;
}

// --- cells -----------------------------------------------------------------

/** A day cell reads green from 99.5%, amber from 95%, red below. */
export const DAY_OK_AT = 99.5;
export const DAY_WARN_AT = 95;

/**
 * The colour of a bucket. Minutes and hours show the worst thing that
 * happened in them (any down check is red, else any degraded one is amber);
 * a UTC day is judged by its availability, so one blip in 2,880 checks does
 * not turn a whole day red. A bucket with nothing but maintenance is blue; a
 * bucket without checks is grey (hatched when the service is paused).
 */
export function cellTone(
	point: PointLike,
	granularity: Granularity,
	paused = false,
): CellTone {
	if (point.checks <= 0) return paused ? "paused" : "none";
	const judged = point.checks - point.maintenance;
	if (judged <= 0) return "info";
	if (granularity === "day") {
		const availability = pointAvailability(point) ?? 0;
		if (availability >= DAY_OK_AT) return "ok";
		return availability >= DAY_WARN_AT ? "warn" : "bad";
	}
	if (point.down > 0) return "bad";
	if (point.degraded > 0) return "warn";
	return "ok";
}

const TONE_WORDS: Record<CellTone, string> = {
	ok: "Operational",
	warn: "Degraded",
	bad: "Down",
	info: "Maintenance",
	paused: "Paused",
	none: "No data",
};

/** What a cell's tone is called in a tooltip and for a screen reader. */
export function toneWords(tone: CellTone): string {
	return TONE_WORDS[tone];
}

export interface Cell {
	/** Start of the bucket, ISO (UTC). */
	at: string;
	/** Start of the bucket, epoch ms. */
	start: number;
	/** End of the bucket, epoch ms (exclusive). */
	end: number;
	tone: CellTone;
	checks: number;
	up: number;
	degraded: number;
	down: number;
	maintenance: number;
	avgLatencyMs: number;
	maxLatencyMs: number;
	availability: number | null;
}

/**
 * One cell per bucket of the series, oldest first. `pausedSince` is when a
 * paused service entered that state: its empty buckets from then on are
 * "paused" rather than "no data", because nothing was probed on purpose.
 */
export function buildCells(
	points: readonly PointLike[] | null | undefined,
	granularity: Granularity,
	pausedSince?: string | null,
): Cell[] {
	const width = CELL_MS[granularity];
	const since = pausedSince ? Date.parse(pausedSince) : Number.NaN;
	return (points ?? []).map((point) => {
		const start = Date.parse(point.at);
		const end = start + width;
		const paused = !Number.isNaN(since) && end > since;
		return {
			at: point.at,
			start,
			end,
			tone: cellTone(point, granularity, paused),
			checks: point.checks,
			up: point.up,
			degraded: point.degraded,
			down: point.down,
			maintenance: point.maintenance,
			avgLatencyMs: point.avgLatencyMs,
			maxLatencyMs: point.maxLatencyMs,
			availability: pointAvailability(point),
		};
	});
}

/** The totals over a run of cells, for the strip's one-line summary. */
export function cellTotals(cells: readonly Cell[]): {
	checks: number;
	up: number;
	degraded: number;
	down: number;
	maintenance: number;
} {
	const totals = { checks: 0, up: 0, degraded: 0, down: 0, maintenance: 0 };
	for (const cell of cells) {
		totals.checks += cell.checks;
		totals.up += cell.up;
		totals.degraded += cell.degraded;
		totals.down += cell.down;
		totals.maintenance += cell.maintenance;
	}
	return totals;
}

const count = (n: number, word: string): string =>
	`${n.toLocaleString("en-US")} ${word}`;

/** "2,880 checks: 2,877 up, 3 down", or "No checks" for a bucket without any. */
export function checksLine(
	cell: Pick<Cell, "checks" | "up" | "degraded" | "down" | "maintenance">,
): string {
	if (cell.checks <= 0) return "No checks";
	const parts: string[] = [];
	if (cell.up > 0) parts.push(count(cell.up, "up"));
	if (cell.degraded > 0) parts.push(count(cell.degraded, "degraded"));
	if (cell.down > 0) parts.push(count(cell.down, "down"));
	if (cell.maintenance > 0) {
		parts.push(count(cell.maintenance, "in maintenance"));
	}
	return `${count(cell.checks, cell.checks === 1 ? "check" : "checks")}: ${parts.join(", ")}`;
}

/** "2026-10-29 14:03 UTC" for a minute, hour or day bucket (a day is just the date). */
export function cellTime(iso: string, granularity: Granularity): string {
	const date = new Date(iso);
	if (Number.isNaN(date.getTime())) return "";
	const pad = (n: number) => n.toString().padStart(2, "0");
	const day = `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`;
	if (granularity === "day") return `${day} UTC`;
	return `${day} ${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())} UTC`;
}

/** The sentence a screen reader gets for the whole strip. */
export function stripSummary(
	cells: readonly Cell[],
	range: UptimeRange,
): string {
	const totals = cellTotals(cells);
	if (totals.checks === 0) return `No checks in ${rangeWords(range)}`;
	const parts = [count(totals.up, "up")];
	if (totals.degraded > 0) parts.push(count(totals.degraded, "degraded"));
	if (totals.down > 0) parts.push(count(totals.down, "down"));
	if (totals.maintenance > 0) {
		parts.push(count(totals.maintenance, "in maintenance"));
	}
	return `${count(totals.checks, "checks")} in ${rangeWords(range)}: ${parts.join(", ")}`;
}

/** A run of neighbouring cells that have no checks, read out once. */
export interface EmptyRun {
	kind: "empty";
	from: Cell;
	to: Cell;
	count: number;
}

/**
 * The strip's cells for a screen-reader table: every cell that has checks
 * on its own row, and each run of cells without any collapsed into one row
 * ("2026-10-29 14:00 UTC to 14:59 UTC: no checks, 60 buckets"), so a strip of
 * sixty buckets with no data does not read "No data" sixty times.
 */
export function readoutRows(cells: readonly Cell[]): (Cell | EmptyRun)[] {
	const rows: (Cell | EmptyRun)[] = [];
	for (const cell of cells) {
		const last = rows.at(-1);
		if (cell.checks > 0 || cell.tone !== "none") {
			rows.push(cell);
		} else if (last && "kind" in last) {
			last.to = cell;
			last.count += 1;
		} else {
			rows.push({ kind: "empty", from: cell, to: cell, count: 1 });
		}
	}
	return rows;
}

/** "2026-10-29 14:00 UTC", or "… UTC to …" for a run of several buckets. */
export function emptyRunTime(run: EmptyRun, granularity: Granularity): string {
	const from = cellTime(run.from.at, granularity);
	if (run.count === 1) return from;
	return `${from} to ${cellTime(run.to.at, granularity)}`;
}

const BUCKETS: Record<Granularity, string> = {
	minute: "minutes",
	hour: "hours",
	day: "days",
};

/** "No checks", or "No checks for 60 minutes" for a run of empty buckets. */
export function emptyRunWords(run: EmptyRun, granularity: Granularity): string {
	if (run.count === 1) return "No checks";
	return `No checks for ${run.count} ${BUCKETS[granularity]}`;
}

const MONTHS = [
	"Jan",
	"Feb",
	"Mar",
	"Apr",
	"May",
	"Jun",
	"Jul",
	"Aug",
	"Sep",
	"Oct",
	"Nov",
	"Dec",
] as const;

/**
 * "13:46 UTC" for a moment on the current UTC day, "29 Oct 13:46 UTC" for an
 * earlier one: a stop time that says when, not "1d ago".
 */
export function formatUtcStamp(iso: string, now: number = Date.now()): string {
	const date = new Date(iso);
	if (Number.isNaN(date.getTime())) return "";
	const pad = (n: number) => n.toString().padStart(2, "0");
	const time = `${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())} UTC`;
	const day = (value: Date) => value.toISOString().slice(0, 10);
	return day(date) === day(new Date(now))
		? time
		: `${date.getUTCDate()} ${MONTHS[date.getUTCMonth()]} ${time}`;
}

/**
 * When the checks of a monitored service stopped, or null while they are
 * arriving. A state of `unknown` with a last check on record means the
 * agent stopped reporting results: the service may be perfectly healthy, and
 * "No data" alone says neither that nor since when.
 */
export function checksStoppedAt(item: {
	state: string;
	monitored?: boolean;
	lastCheckAt: string | null;
}): string | null {
	if (item.monitored === false) return null;
	return item.state === "unknown" ? item.lastCheckAt : null;
}

/** The tones present in the cells, worst first, for the strip's legend. */
export function tonesPresent(cells: readonly Cell[]): CellTone[] {
	const order: CellTone[] = ["bad", "warn", "info", "ok", "paused", "none"];
	const seen = new Set(cells.map((cell) => cell.tone));
	return order.filter((tone) => seen.has(tone));
}

// --- outages ---------------------------------------------------------------

export interface OutageLike {
	id: string;
	startedAt: string;
	resolvedAt: string | null;
	durationSeconds: number;
	cause: string;
}

/**
 * What a cell can say about the error: the cause of an outage that overlaps
 * it, else the last error when the cell holds the last check and it failed.
 */
export function cellError(
	cell: Pick<Cell, "start" | "end" | "down">,
	outages: readonly OutageLike[],
	last: { at: string | null; error: string },
): string {
	for (const outage of outages) {
		const from = Date.parse(outage.startedAt);
		const to = outage.resolvedAt ? Date.parse(outage.resolvedAt) : Date.now();
		if (from < cell.end && to >= cell.start && outage.cause !== "") {
			return outage.cause;
		}
	}
	if (last.error !== "" && last.at !== null) {
		const at = Date.parse(last.at);
		if (at >= cell.start && at < cell.end) return last.error;
	}
	return "";
}

/** "42s", "12m", "1h 05m", "2d 3h": how long an outage lasted. */
export function formatSpan(seconds: number): string {
	const total = Math.max(0, Math.round(seconds));
	if (total < 60) return `${total}s`;
	const minutes = Math.floor(total / 60);
	if (minutes < 60) return `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) {
		return `${hours}h ${(minutes % 60).toString().padStart(2, "0")}m`;
	}
	return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

// --- latency ---------------------------------------------------------------

/**
 * Mean and maximum latency over a run of buckets. A bucket's mean covers its
 * passing checks that measured one (0 when none did), so the mean over
 * buckets is weighted by those checks and skips buckets without a figure.
 */
export function latencySummary(
	points: readonly PointLike[] | null | undefined,
): {
	avg: number | null;
	max: number | null;
} {
	let weighted = 0;
	let weight = 0;
	let max = 0;
	for (const point of points ?? []) {
		const passing = point.up + point.degraded;
		if (point.avgLatencyMs > 0 && passing > 0) {
			weighted += point.avgLatencyMs * passing;
			weight += passing;
		}
		max = Math.max(max, point.maxLatencyMs);
	}
	return {
		avg: weight > 0 ? weighted / weight : null,
		max: max > 0 ? max : null,
	};
}

// --- the 90-day bar --------------------------------------------------------

/** The words the dithered bars use for each day. */
export const DAY_WORDS = {
	up: "Operational",
	partial: "Degraded",
	down: "Down",
	none: "No data",
} as const;

/**
 * Daily points as the days of a `DitherUptimeBar`: the same colour bands as
 * a day cell, the availability as the day's fraction, and the check counts
 * as its readout line. A day without checks, or with only maintenance, has no
 * data.
 */
export function uptimeDays(
	points: readonly PointLike[] | null | undefined,
): UptimeDay[] {
	return (points ?? []).map((point) => {
		const date = point.at.slice(0, 10);
		const tone = cellTone(point, "day");
		const availability = pointAvailability(point);
		const status =
			tone === "ok"
				? ("up" as const)
				: tone === "warn"
					? ("degraded" as const)
					: tone === "bad"
						? ("down" as const)
						: ("unknown" as const);
		const detail =
			point.checks <= 0
				? undefined
				: tone === "info"
					? "Maintenance only"
					: checksLine(point);
		return {
			date,
			status,
			uptime: availability === null ? null : availability / 100,
			...(detail ? { detail } : {}),
		};
	});
}

// --- the environment overview ----------------------------------------------

export interface EnvServiceLike {
	serviceId: string;
	name: string;
	state: string;
	monitored: boolean;
}

/** Down and degraded first; paused, unmonitored and healthy last. Ties by name. */
export function sortServices<T extends EnvServiceLike>(
	items: readonly T[] | null | undefined,
): T[] {
	const rank = (item: T) =>
		item.monitored ? stateSeverity(item.state) : SEVERITY.paused;
	return [...(items ?? [])].sort(
		(a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name),
	);
}

/** How many services are down or degraded, for the section's note. */
export function attentionCount(
	items: readonly EnvServiceLike[] | null | undefined,
): number {
	return (items ?? []).filter(
		(item) =>
			item.monitored && (item.state === "down" || item.state === "degraded"),
	).length;
}

/** A service's state as the environment list shows it: switched-off monitoring reads as such, whatever the stored state. */
export function serviceStateView(
	item: Pick<EnvServiceLike, "state" | "monitored">,
): StateView {
	return item.monitored
		? stateView(item.state)
		: { ...stateView("paused"), label: "Monitoring off" };
}

/**
 * The line under a service in the environment list. A down or degraded
 * service says what its last failing check reported. An up service that is
 * failing right now (its last check failed, the outage has not opened) gets
 * the amber "Failing" hint instead; the list has no per-service count, the
 * Overview card has "Failing 2/3". Anything else says nothing.
 */
export function rowNote(
	item: Pick<EnvServiceLike, "state" | "monitored"> & { lastError: string },
): { kind: "error" | "hint"; text: string } | null {
	if (!item.monitored || item.lastError === "") return null;
	if (item.state === "down" || item.state === "degraded") {
		return { kind: "error", text: item.lastError };
	}
	return item.state === "up" ? { kind: "hint", text: "Failing" } : null;
}

/** Rows the environment list shows folded. */
export const FOLDED_ROWS = 5;

/** Down, degraded, gone quiet or failing right now: a row the fold never hides. */
function needsEyes(item: EnvServiceLike & { lastError: string }): boolean {
	return (
		item.monitored &&
		(stateSeverity(item.state) <= SEVERITY.unknown || item.lastError !== "")
	);
}

/**
 * The rows of a sorted list that stay in view folded, and the ones behind
 * "Show more". The first `limit` rows stay, plus any later row that needs
 * eyes, in their sorted order. One hidden row is not worth the button that
 * would take its place, so the list then does not fold at all.
 */
export function foldServices<T extends EnvServiceLike & { lastError: string }>(
	sorted: readonly T[],
	limit: number = FOLDED_ROWS,
): { shown: T[]; hidden: T[] } {
	const shown: T[] = [];
	const hidden: T[] = [];
	sorted.forEach((item, index) => {
		(index < limit || needsEyes(item) ? shown : hidden).push(item);
	});
	return hidden.length < 2
		? { shown: [...sorted], hidden: [] }
		: { shown, hidden };
}

/** "Show 4 more, all up", "Show 4 more: 3 up, 1 paused": what the fold hides. */
export function foldedLabel(
	hidden: readonly Pick<EnvServiceLike, "state" | "monitored">[],
): string {
	const counts = new Map<string, number>();
	for (const item of hidden) {
		const label = serviceStateView(item).label.toLowerCase();
		counts.set(label, (counts.get(label) ?? 0) + 1);
	}
	const head = `Show ${hidden.length} more`;
	if (counts.size === 1) return `${head}, all ${[...counts.keys()][0]}`;
	const parts = [...counts].map(([label, n]) => `${n} ${label}`);
	return `${head}: ${parts.join(", ")}`;
}

// --- monitor settings ------------------------------------------------------

/** What `PUT /services/{id}/uptime-monitor` takes and returns. */
export interface MonitorBody {
	enabled: boolean;
	alerts: boolean;
	failureThreshold: number;
	recoveryThreshold: number;
	reminderMinutes: number;
}

/** The presets of the reminder interval; anything else is "custom". */
export const REMINDER_PRESETS = [15, 30, 60, 120] as const;
export type ReminderChoice =
	| "off"
	| `${(typeof REMINDER_PRESETS)[number]}`
	| "custom";

export interface MonitorForm {
	enabled: boolean;
	alerts: boolean;
	failureThreshold: string;
	recoveryThreshold: string;
	reminder: ReminderChoice;
	/** Minutes, while `reminder` is custom. */
	reminderCustom: string;
}

export type MonitorErrors = Partial<
	Record<"failureThreshold" | "recoveryThreshold" | "reminderCustom", string>
>;

function choiceOf(minutes: number): ReminderChoice {
	if (minutes <= 0) return "off";
	const preset = REMINDER_PRESETS.find((value) => value === minutes);
	return preset === undefined ? "custom" : `${preset}`;
}

/** The form's starting values from the saved monitor. */
export function toMonitorForm(monitor: MonitorBody): MonitorForm {
	const reminder = choiceOf(monitor.reminderMinutes);
	return {
		enabled: monitor.enabled,
		alerts: monitor.alerts,
		failureThreshold: String(monitor.failureThreshold),
		recoveryThreshold: String(monitor.recoveryThreshold),
		reminder,
		reminderCustom:
			reminder === "custom" ? String(monitor.reminderMinutes) : "",
	};
}

const INTEGER = /^\d+$/;

function threshold(value: string, what: string): string | undefined {
	const text = value.trim();
	if (!INTEGER.test(text)) return `${what} must be a whole number.`;
	const n = Number(text);
	if (n < MIN_THRESHOLD || n > MAX_THRESHOLD) {
		return `${what} must be between ${MIN_THRESHOLD} and ${MAX_THRESHOLD}.`;
	}
	return undefined;
}

/** What is wrong with the form, keyed by field; empty when it can be saved. */
export function validateMonitor(form: MonitorForm): MonitorErrors {
	const errors: MonitorErrors = {};
	const failure = threshold(form.failureThreshold, "Failed checks");
	if (failure) errors.failureThreshold = failure;
	const recovery = threshold(form.recoveryThreshold, "Passing checks");
	if (recovery) errors.recoveryThreshold = recovery;
	if (form.reminder === "custom") {
		const text = form.reminderCustom.trim();
		const n = Number(text);
		if (!INTEGER.test(text) || n < 1 || n > MAX_REMINDER_MINUTES) {
			errors.reminderCustom = `Minutes must be a whole number from 1 to ${MAX_REMINDER_MINUTES}.`;
		}
	}
	return errors;
}

/** The body to save, or null while the form has a problem. */
export function buildMonitorBody(form: MonitorForm): MonitorBody | null {
	if (Object.keys(validateMonitor(form)).length > 0) return null;
	const reminderMinutes =
		form.reminder === "off"
			? 0
			: form.reminder === "custom"
				? Number(form.reminderCustom.trim())
				: Number(form.reminder);
	return {
		enabled: form.enabled,
		alerts: form.alerts,
		failureThreshold: Number(form.failureThreshold.trim()),
		recoveryThreshold: Number(form.recoveryThreshold.trim()),
		reminderMinutes,
	};
}

/** Whether the form differs from the saved monitor (a form with a problem counts as changed). */
export function monitorChanged(form: MonitorForm, saved: MonitorBody): boolean {
	const body = buildMonitorBody(form);
	if (body === null) return true;
	return (
		body.enabled !== saved.enabled ||
		body.alerts !== saved.alerts ||
		body.failureThreshold !== saved.failureThreshold ||
		body.recoveryThreshold !== saved.recoveryThreshold ||
		body.reminderMinutes !== saved.reminderMinutes
	);
}

/** "about 1.5 min": how long `checks` checks 30 s apart take. */
export function checksSpan(checks: number): string {
	const seconds = checks * CHECK_INTERVAL_SECONDS;
	if (seconds < 60) return `${seconds} s`;
	return `${Number((seconds / 60).toFixed(1))} min`;
}

/** The reminder interval as words: "Off", "Every 30 min", "Every 2 h". */
export function reminderText(minutes: number): string {
	if (minutes <= 0) return "Off";
	if (minutes % 60 === 0) return `Every ${minutes / 60} h`;
	return `Every ${minutes} min`;
}
