import type { LampTone } from "#/components/ui/lamp";

/** Backup run states, as the API reports them. */
export type BackupStatus =
	| "queued"
	| "running"
	| "succeeded"
	| "failed"
	| "skipped"
	| "expired";

export type RestoreStatus =
	| "provisioning"
	| "pre_backup"
	| "awaiting_upload"
	| "uploading"
	| "restoring"
	| "succeeded"
	| "failed";

interface Presentation {
	tone: LampTone;
	pulse: boolean;
	label: string;
}

const BACKUP_STATUS: Record<BackupStatus, Presentation> = {
	queued: { tone: "building", pulse: true, label: "Queued" },
	running: { tone: "deploying", pulse: true, label: "Running" },
	succeeded: { tone: "ok", pulse: false, label: "Succeeded" },
	failed: { tone: "bad", pulse: false, label: "Failed" },
	skipped: { tone: "warn", pulse: false, label: "Skipped" },
	expired: { tone: "offline", pulse: false, label: "Not delivered" },
};

const RESTORE_STATUS: Record<RestoreStatus, Presentation> = {
	provisioning: {
		tone: "building",
		pulse: true,
		label: "Starting new database",
	},
	pre_backup: { tone: "deploying", pulse: true, label: "Safety backup" },
	awaiting_upload: {
		tone: "staged",
		pulse: false,
		label: "Waiting for the file",
	},
	uploading: { tone: "deploying", pulse: true, label: "Uploading" },
	restoring: { tone: "deploying", pulse: true, label: "Restoring" },
	succeeded: { tone: "ok", pulse: false, label: "Restored" },
	failed: { tone: "bad", pulse: false, label: "Failed" },
};

export function backupPresentation(status: string): Presentation {
	return (
		BACKUP_STATUS[status as BackupStatus] ?? {
			tone: "off",
			pulse: false,
			label: status,
		}
	);
}

export function restorePresentation(status: string): Presentation {
	return (
		RESTORE_STATUS[status as RestoreStatus] ?? {
			tone: "off",
			pulse: false,
			label: status,
		}
	);
}

/** True while a backup or restore is still in flight. */
export function inFlight(status: string): boolean {
	return (
		status === "queued" ||
		status === "running" ||
		status === "provisioning" ||
		status === "pre_backup" ||
		status === "awaiting_upload" ||
		status === "uploading" ||
		status === "restoring"
	);
}

export const TRIGGER_LABEL: Record<string, string> = {
	manual: "Manual",
	schedule: "Scheduled",
	pre_restore: "Before restore",
};

export interface SchedulePreset {
	id: "hourly" | "daily" | "weekly" | "custom";
	label: string;
	cron: string;
}

export const SCHEDULE_PRESETS: readonly SchedulePreset[] = [
	{ id: "hourly", label: "Hourly", cron: "@hourly" },
	{ id: "daily", label: "Daily 03:00", cron: "0 3 * * *" },
	{ id: "weekly", label: "Weekly Sun 03:00", cron: "0 3 * * 0" },
	{ id: "custom", label: "Custom", cron: "" },
];

/** The preset a stored expression matches, else "custom". */
export function presetFor(cron: string): SchedulePreset["id"] {
	const hit = SCHEDULE_PRESETS.find(
		(p) => p.id !== "custom" && p.cron === cron,
	);
	return hit?.id ?? "custom";
}

const MACROS: Record<string, string> = {
	"@hourly": "Every hour",
	"@daily": "Every day at 00:00",
	"@midnight": "Every day at 00:00",
	"@weekly": "Every Sunday at 00:00",
	"@monthly": "On the 1st of every month at 00:00",
	"@yearly": "Every year on 1 January at 00:00",
	"@annually": "Every year on 1 January at 00:00",
};

const WEEKDAYS = [
	"Sunday",
	"Monday",
	"Tuesday",
	"Wednesday",
	"Thursday",
	"Friday",
	"Saturday",
];

const pad = (n: string) => n.padStart(2, "0");

/**
 * A plain-words reading of the common cron shapes. Anything else comes back as
 * the expression itself, never a wrong sentence.
 */
export function describeCron(expr: string): string {
	const trimmed = expr.trim();
	const macro = MACROS[trimmed.toLowerCase()];
	if (macro) return macro;
	const f = trimmed.split(/\s+/);
	if (f.length !== 5) return trimmed;
	const [min = "", hour = "", dom = "", mon = "", dow = ""] = f;
	const num = /^\d+$/;
	const step = /^\*\/(\d+)$/;
	if (hour === "*" && dom === "*" && mon === "*" && dow === "*") {
		const m = step.exec(min);
		if (m) return `Every ${m[1]} minutes`;
		if (num.test(min)) return `Every hour at minute ${min}`;
	}
	if (num.test(min) && num.test(hour) && mon === "*") {
		const at = `${pad(hour)}:${pad(min)}`;
		if (dom === "*" && dow === "*") return `Every day at ${at}`;
		if (dom === "*" && num.test(dow)) {
			const day = WEEKDAYS[Number(dow) % 7];
			if (day) return `Every ${day} at ${at}`;
		}
		if (num.test(dom) && dow === "*")
			return `On day ${dom} of every month at ${at}`;
	}
	return trimmed;
}

/** "Keep the newest 7, delete after 30 days" in words. */
export function describeRetention(count: number, days: number): string {
	const parts: string[] = [];
	if (count > 0) parts.push(`keep the newest ${count}`);
	if (days > 0) parts.push(`delete after ${days} day${days === 1 ? "" : "s"}`);
	if (parts.length === 0) return "Keep everything";
	const text = parts.join(", ");
	return text.charAt(0).toUpperCase() + text.slice(1);
}

/** Engines whose dumps NebulaCtrl can load back (Valkey and Redis dumps are RDB files). */
export function canRestore(engine: string): boolean {
	return ["postgres", "mysql", "mariadb", "mongodb"].includes(engine);
}

/** Where a destination writes, for the list. */
export function destinationWhere(d: {
	kind: string;
	bucket?: string | undefined;
	endpoint?: string | undefined;
	prefix?: string | undefined;
	localDir?: string | undefined;
}): string {
	if (d.kind === "local")
		return `Agent backup directory${d.localDir ? `/${d.localDir}` : ""}`;
	// The built-in storage has no address a team could use: say what it is.
	if (d.kind === "builtin") return "Built-in backup storage";
	const host = d.endpoint ? d.endpoint.replace(/^https?:\/\//, "") : "AWS S3";
	return `${host} · ${d.bucket ?? ""}${d.prefix ? `/${d.prefix}` : ""}`;
}

/** What the history strip says about one UTC day. */
export type BackupDayStatus = "up" | "degraded" | "down" | "unknown";

export interface BackupDay {
	/** UTC day, `YYYY-MM-DD`. */
	date: string;
	status: BackupDayStatus;
	/** "2 succeeded, 1 failed" for the readout. */
	detail?: string;
}

/** The words the strip uses for each status. */
export const BACKUP_DAY_WORDS = {
	up: "Succeeded",
	partial: "Skipped",
	down: "Failed",
	none: "No backup",
} as const;

interface RunLike {
	status: string;
	createdAt: string;
}

const dayKey = (iso: string): string =>
	new Date(iso).toISOString().slice(0, 10);

/**
 * One entry per UTC day for the last `days` days ending on the day of `now`,
 * oldest first. A day with any failed run is `down`; else one with a skipped
 * or undelivered (expired) run is `degraded`; else one with a succeeded run is
 * `up`; a day with no finished run is `unknown`. Queued and running backups
 * have no verdict yet. `truncated` says the run list was cut at its page
 * size: days older than its oldest run are then marked as not loaded rather
 * than reported as having no backup.
 */
export function backupDays(
	runs: readonly RunLike[],
	days: number,
	now: Date,
	truncated = false,
): BackupDay[] {
	const counts = new Map<
		string,
		{ ok: number; failed: number; skipped: number }
	>();
	let oldest: string | undefined;
	for (const r of runs) {
		const key = dayKey(r.createdAt);
		if (oldest === undefined || key < oldest) oldest = key;
		const c = counts.get(key) ?? { ok: 0, failed: 0, skipped: 0 };
		if (r.status === "succeeded") c.ok += 1;
		else if (r.status === "failed") c.failed += 1;
		else if (r.status === "skipped" || r.status === "expired") c.skipped += 1;
		counts.set(key, c);
	}
	const today = Date.UTC(
		now.getUTCFullYear(),
		now.getUTCMonth(),
		now.getUTCDate(),
	);
	const out: BackupDay[] = [];
	for (let i = days - 1; i >= 0; i--) {
		const date = new Date(today - i * 86_400_000).toISOString().slice(0, 10);
		const c = counts.get(date);
		if (!c || c.ok + c.failed + c.skipped === 0) {
			out.push(
				truncated && oldest !== undefined && date < oldest
					? { date, status: "unknown", detail: "Older runs are not loaded" }
					: { date, status: "unknown" },
			);
			continue;
		}
		const parts: string[] = [];
		if (c.ok) parts.push(`${c.ok} succeeded`);
		if (c.failed) parts.push(`${c.failed} failed`);
		if (c.skipped) parts.push(`${c.skipped} skipped`);
		out.push({
			date,
			status: c.failed ? "down" : c.skipped ? "degraded" : "up",
			detail: parts.join(", "),
		});
	}
	return out;
}

/**
 * The share of finished runs in the strip that succeeded, 0–1; undefined when
 * no day has a verdict.
 */
export function backupSuccessShare(
	runs: readonly RunLike[],
	days: readonly BackupDay[],
): number | undefined {
	const inWindow = new Set(days.map((d) => d.date));
	let ok = 0;
	let finished = 0;
	for (const r of runs) {
		if (!inWindow.has(dayKey(r.createdAt))) continue;
		if (r.status === "succeeded") ok += 1;
		else if (
			r.status === "failed" ||
			r.status === "skipped" ||
			r.status === "expired"
		)
			finished += 1;
	}
	finished += ok;
	return finished === 0 ? undefined : ok / finished;
}
