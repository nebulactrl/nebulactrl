import type { RunView } from "#/api/gen/types.gen";
import type { LampTone } from "#/components/ui/lamp";
import { quoteArg } from "#/features/service-settings/draft";
import { formatDuration } from "#/lib/format";

export { splitCommand } from "#/features/service-settings/draft";

/** Task run states, as the API reports them. */
type RunStatus = RunView["status"];

interface Presentation {
	tone: LampTone;
	pulse: boolean;
	label: string;
}

const RUN_STATUS: Record<RunStatus, Presentation> = {
	queued: { tone: "building", pulse: true, label: "Queued" },
	running: { tone: "deploying", pulse: true, label: "Running" },
	succeeded: { tone: "ok", pulse: false, label: "Succeeded" },
	failed: { tone: "bad", pulse: false, label: "Failed" },
	timed_out: { tone: "bad", pulse: false, label: "Timed out" },
	skipped: { tone: "warn", pulse: false, label: "Skipped" },
};

/** Tone, pulse and word of a run status; an unknown status shows as written. */
export function runPresentation(status: string): Presentation {
	return (
		RUN_STATUS[status as RunStatus] ?? {
			tone: "off",
			pulse: false,
			label: status,
		}
	);
}

/** True while a run has not finished: its output is still arriving. */
export function isActive(status: string): boolean {
	return status === "queued" || status === "running";
}

/** An argv as one editable line: `sh -c "rake db:migrate"`. */
export function commandText(
	argv: readonly string[] | null | undefined,
): string {
	return (argv ?? []).map(quoteArg).join(" ");
}

const SOURCE_LABEL: Record<string, string> = {
	cron: "Scheduled",
	manual: "Run now",
	one_off: "Command",
	preview_seed: "Preview seed",
};

/** Where a run came from, in words. */
export function sourceLabel(source: string): string {
	return SOURCE_LABEL[source] ?? source;
}

/**
 * How long a run took: start to finish, or start to `now` while it runs.
 * Null for a run that never started (queued, or skipped).
 */
export function runDurationMs(
	run: Pick<RunView, "startedAt" | "finishedAt">,
	now: number = Date.now(),
): number | null {
	if (!run.startedAt) return null;
	const started = Date.parse(run.startedAt);
	if (Number.isNaN(started)) return null;
	const end = run.finishedAt ? Date.parse(run.finishedAt) : now;
	if (Number.isNaN(end)) return null;
	return Math.max(0, end - started);
}

/** "10.6s", "5m 02s"; "–" when the run never started. */
export function formatRunDuration(
	run: Pick<RunView, "startedAt" | "finishedAt">,
	now: number = Date.now(),
): string {
	const ms = runDurationMs(run, now);
	return ms === null ? "–" : formatDuration(ms);
}

/**
 * How a run can be started again: a task's run through the task, a one-off
 * through its command; nothing else (a preview seed belongs to its preview).
 */
export function rerunKind(
	run: Pick<RunView, "taskId" | "source" | "command">,
): "task" | "command" | null {
	if (run.taskId) return "task";
	if (run.source === "one_off" && (run.command ?? []).length > 0) {
		return "command";
	}
	return null;
}

function dayAndTime(date: Date, timeZone: string | undefined) {
	const parts = new Intl.DateTimeFormat("en-US", {
		timeZone,
		weekday: "short",
		day: "numeric",
		month: "short",
		hour: "2-digit",
		minute: "2-digit",
		hourCycle: "h23",
	}).formatToParts(date);
	const get = (type: string) =>
		parts.find((part) => part.type === type)?.value ?? "";
	return {
		day: `${get("weekday")} ${get("day")} ${get("month")}`,
		time: `${get("hour")}:${get("minute")}`,
	};
}

/** The reader's own IANA zone ("Europe/Berlin"), "UTC" when the browser hides it. */
export function browserTimezone(): string {
	return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

/** Whether the runtime knows `zone` as an IANA time zone. "" counts as UTC. */
export function isValidTimezone(zone: string): boolean {
	if (zone.trim() === "") return true;
	try {
		new Intl.DateTimeFormat("en-US", { timeZone: zone.trim() });
		return true;
	} catch {
		return false;
	}
}

/** Zone names for the picker: every zone the runtime knows, UTC first. */
export function timezoneOptions(): string[] {
	const all =
		typeof Intl.supportedValuesOf === "function"
			? Intl.supportedValuesOf("timeZone")
			: [];
	return ["UTC", ...all.filter((zone) => zone !== "UTC")];
}

export interface RunTimes {
	/** The instant in the schedule's zone: "Tue 27 Oct 09:00". */
	zoned: string;
	/** The same instant in UTC (date included when it differs). */
	utc: string;
	/** The same instant in the reader's zone; null when it equals the schedule's zone or UTC. */
	local: string | null;
}

/**
 * An ISO instant as a task's schedule sees it: in the schedule's own IANA
 * zone, in UTC, and in the reader's zone when that is a third one. `zone` ""
 * means UTC; `reader` pins the reader's zone for tests.
 */
export function formatRunTimes(
	iso: string,
	zone: string,
	reader: string = browserTimezone(),
): RunTimes {
	const date = new Date(iso);
	if (Number.isNaN(date.getTime())) return { zoned: "", utc: "", local: null };
	const scheduleZone = zone.trim() === "" ? "UTC" : zone.trim();
	const zoned = dayAndTime(date, scheduleZone);
	const utc = dayAndTime(date, "UTC");
	const local = dayAndTime(date, reader);
	const sameAsZone = reader === scheduleZone || reader === "UTC";
	return {
		zoned: `${zoned.day} ${zoned.time}`,
		utc:
			utc.day === zoned.day ? `${utc.time} UTC` : `${utc.day} ${utc.time} UTC`,
		local: sameAsZone ? null : `${local.day} ${local.time}`,
	};
}

export interface SchedulePreset {
	id: "every15" | "hourly" | "daily" | "weekly" | "custom";
	label: string;
	/** Null for custom. */
	cron: string | null;
}

export const SCHEDULE_PRESETS: readonly SchedulePreset[] = [
	{ id: "every15", label: "Every 15 min", cron: "*/15 * * * *" },
	{ id: "hourly", label: "Hourly", cron: "@hourly" },
	{ id: "daily", label: "Daily 03:00", cron: "0 3 * * *" },
	{ id: "weekly", label: "Weekly", cron: "0 3 * * 0" },
	{ id: "custom", label: "Custom", cron: null },
];

/** The preset a stored expression matches, else "custom". */
export function presetFor(cron: string): SchedulePreset["id"] {
	return SCHEDULE_PRESETS.find((p) => p.cron === cron)?.id ?? "custom";
}

export const DEFAULT_TIMEOUT_SECONDS = 1800;

/** A whole number of seconds of at least 1, or null for anything else. */
export function parseTimeoutSeconds(text: string): number | null {
	const trimmed = text.trim();
	if (!/^\d+$/.test(trimmed)) return null;
	const value = Number.parseInt(trimmed, 10);
	return value >= 1 ? value : null;
}
