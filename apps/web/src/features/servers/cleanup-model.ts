import type { CleanupRun, CleanupStep, CleanupView } from "#/api/gen/types.gen";
import { count, formatBytes } from "#/lib/format";

/** The name of a prune step as the page shows it. */
export const STEP_LABELS: Record<CleanupStep["name"], string> = {
	containers_unmanaged: "Stopped containers",
	images_dangling: "Dangling images",
	images_unused: "Unused images",
	build_cache: "Build cache",
	networks: "Networks",
	volumes: "Volumes",
};

/** Who started a run: the schedule, the disk alert, a person, or the volume action. */
export function triggerLabel(
	run: Pick<CleanupRun, "trigger" | "mode">,
): string {
	if (run.mode === "volumes") return "Unused volumes";
	switch (run.trigger) {
		case "scheduled":
			return "Scheduled";
		case "emergency":
			return "Disk alert";
		default:
			return "Run by hand";
	}
}

/** One line for a run: what it freed, or that it is still going or failed. */
export function runSummary(
	run: Pick<CleanupRun, "status" | "reclaimedBytes" | "items" | "error">,
): string {
	if (run.status === "pending") return "Running";
	const freed = `Freed ${formatBytes(run.reclaimedBytes)} · ${count(run.items, "item")} removed`;
	if (run.status === "failed") {
		return run.reclaimedBytes > 0 || run.items > 0
			? `Failed · ${freed}`
			: "Failed";
	}
	return freed;
}

/** One line for a step of a run. */
export function stepSummary(step: CleanupStep): string {
	if (step.skipped) return step.skipped;
	const parts: string[] = [];
	parts.push(`${count(step.items, "item")} removed`);
	if (step.reclaimedBytes > 0) parts.push(formatBytes(step.reclaimedBytes));
	if (step.protected > 0) parts.push(`${step.protected} kept for releases`);
	return parts.join(" · ");
}

// --- editable schedule and limits ---------------------------------------

/** The editable limits of a cleanup; the control plane enforces the same ranges. */
export const CLEANUP_LIMITS = {
	unusedImageHours: { min: 1, max: 8760, default: 72 },
	buildCacheDays: { min: 1, max: 365, default: 7 },
	diskTriggerPercent: { min: 50, max: 99 },
} as const;

export const DEFAULT_CLEANUP_CRON = "0 4 * * *";

/** Schedules offered as shortcuts; any 5-field cron expression (UTC) works. */
export const CLEANUP_SCHEDULE_PRESETS = [
	{ label: "Daily at 04:00", cron: DEFAULT_CLEANUP_CRON },
	{ label: "Every 6 hours", cron: "0 */6 * * *" },
	{ label: "Sundays at 03:00", cron: "0 3 * * 0" },
] as const;

/** What the reader typed; numbers stay text so a half-typed field is representable. */
export interface CleanupDraft {
	cron: string;
	unusedImageMinAgeHours: string;
	buildCacheMinAgeDays: string;
	/** Empty follows the server's disk alert threshold. */
	diskTriggerPercent: string;
}

export type CleanupDraftErrors = Partial<Record<keyof CleanupDraft, string>>;

export function toCleanupDraft(
	view: Pick<
		CleanupView,
		| "cron"
		| "unusedImageMinAgeHours"
		| "buildCacheMinAgeDays"
		| "diskTriggerPercent"
	>,
): CleanupDraft {
	return {
		cron: view.cron,
		unusedImageMinAgeHours: String(view.unusedImageMinAgeHours),
		buildCacheMinAgeDays: String(view.buildCacheMinAgeDays),
		diskTriggerPercent:
			view.diskTriggerPercent == null ? "" : String(view.diskTriggerPercent),
	};
}

const CRON_MACRO = /^@(yearly|annually|monthly|weekly|daily|midnight|hourly)$/i;
const CRON_FIELD = /^[A-Za-z0-9*/,\-?]+$/;

/** A cheap shape check; the control plane parses the expression for real (and refuses runs closer than 15 minutes). */
export function cronProblem(cron: string): string | undefined {
	const text = cron.trim();
	if (text === "") return "Enter a cron expression, like 0 4 * * *.";
	if (text.startsWith("@")) {
		return CRON_MACRO.test(text)
			? undefined
			: "Use @daily, @weekly, @monthly, @yearly or @hourly, or five fields.";
	}
	const fields = text.split(/\s+/);
	if (fields.length !== 5) {
		return "A schedule has five fields: minute hour day-of-month month day-of-week.";
	}
	if (!fields.every((f) => CRON_FIELD.test(f))) {
		return "A field holds numbers, names, *, /, , and -.";
	}
	return undefined;
}

function integerProblem(
	text: string,
	min: number,
	max: number,
	label: string,
): string | undefined {
	const value = text.trim();
	if (!/^\d+$/.test(value)) return `${label} must be a whole number.`;
	const n = Number(value);
	return n < min || n > max ? `${label} must be ${min} to ${max}.` : undefined;
}

export function validateCleanupDraft(d: CleanupDraft): CleanupDraftErrors {
	const errors: CleanupDraftErrors = {};
	const cron = cronProblem(d.cron);
	if (cron) errors.cron = cron;
	const hours = integerProblem(
		d.unusedImageMinAgeHours,
		CLEANUP_LIMITS.unusedImageHours.min,
		CLEANUP_LIMITS.unusedImageHours.max,
		"Image age",
	);
	if (hours) errors.unusedImageMinAgeHours = hours;
	const days = integerProblem(
		d.buildCacheMinAgeDays,
		CLEANUP_LIMITS.buildCacheDays.min,
		CLEANUP_LIMITS.buildCacheDays.max,
		"Build cache age",
	);
	if (days) errors.buildCacheMinAgeDays = days;
	if (d.diskTriggerPercent.trim() !== "") {
		const pct = integerProblem(
			d.diskTriggerPercent,
			CLEANUP_LIMITS.diskTriggerPercent.min,
			CLEANUP_LIMITS.diskTriggerPercent.max,
			"The disk trigger",
		);
		if (pct) errors.diskTriggerPercent = pct;
	}
	return errors;
}

/** The PUT body: every setting, because the endpoint replaces them all (0 or empty keeps a default). */
export interface CleanupBody {
	enabled: boolean;
	removeStoppedUnmanaged: boolean;
	cron: string;
	unusedImageMinAgeHours: number;
	buildCacheMinAgeDays: number;
	diskTriggerPercent: number;
}

export function cleanupBody(
	flags: Pick<CleanupBody, "enabled" | "removeStoppedUnmanaged">,
	d: CleanupDraft,
): CleanupBody {
	return {
		...flags,
		cron: d.cron.trim().replace(/\s+/g, " "),
		unusedImageMinAgeHours: Number(d.unusedImageMinAgeHours),
		buildCacheMinAgeDays: Number(d.buildCacheMinAgeDays),
		diskTriggerPercent:
			d.diskTriggerPercent.trim() === "" ? 0 : Number(d.diskTriggerPercent),
	};
}

export function cleanupDraftChanged(a: CleanupDraft, b: CleanupDraft): boolean {
	return (
		a.cron.trim().replace(/\s+/g, " ") !== b.cron.trim().replace(/\s+/g, " ") ||
		a.unusedImageMinAgeHours.trim() !== b.unusedImageMinAgeHours.trim() ||
		a.buildCacheMinAgeDays.trim() !== b.buildCacheMinAgeDays.trim() ||
		a.diskTriggerPercent.trim() !== b.diskTriggerPercent.trim()
	);
}
