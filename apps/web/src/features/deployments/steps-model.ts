import type { EventView } from "#/api/gen/types.gen";

/**
 * Status transitions that the step rows already say: "queued" before the
 * first step, "releasing" while create/start run, "verifying" while the
 * health check runs. Shown as rows they doubled the timeline and landed out
 * of order (a transition is recorded when the control plane hears of it).
 * Outcomes (failed, rolled back, canceled, …) stay as rows.
 */
const PHASE_STATUSES: Record<string, true> = {
	created: true,
	queued: true,
	preparing: true,
	building: true,
	releasing: true,
	verifying: true,
	succeeded: true,
};

/** A step's result as the timeline shows it. */
type StepState = "started" | "succeeded" | "failed" | "warning" | "info";

/** One row of the timeline: a step folded from its start and its result. */
export interface StepRow {
	key: string;
	/** The agent's step id, or "status" for an outcome row. */
	step: string;
	/** The step's latest state; an outcome row carries its status here. */
	state: StepState | string;
	startedAt: string;
	/** Set once the step has a result. */
	endedAt?: string;
	/** The most telling message: a failure's reason, else the result's, else the start's. */
	message: string;
}

const TERMINAL: Record<string, true> = {
	succeeded: true,
	failed: true,
	warning: true,
};

/**
 * Folds the raw event list into one row per step run: `pull · started` and
 * `pull · succeeded` become one `pull` row with its duration. A step that
 * starts again after finishing (a retry, a rollback's route) opens a new
 * row. Phase transitions are dropped; outcome statuses stay as their own row.
 */
export function foldSteps(events: readonly EventView[]): StepRow[] {
	const rows: StepRow[] = [];
	const open = new Map<string, StepRow>();
	for (const event of events) {
		if (event.step === "status") {
			if (PHASE_STATUSES[event.status]) continue;
			rows.push({
				key: `status-${event.id}`,
				step: "status",
				state: event.status,
				startedAt: event.createdAt,
				message: event.message,
			});
			continue;
		}
		const current = open.get(event.step);
		if (current === undefined || event.status === "started") {
			if (current !== undefined && !TERMINAL[current.state]) {
				// A second start without a result: keep the first row, refresh it.
				current.message = event.message || current.message;
				continue;
			}
			const row: StepRow = {
				key: `${event.step}-${event.id}`,
				step: event.step,
				state: event.status,
				startedAt: event.createdAt,
				message: event.message,
			};
			if (TERMINAL[event.status]) row.endedAt = event.createdAt;
			rows.push(row);
			open.set(event.step, row);
			continue;
		}
		current.state = event.status;
		if (TERMINAL[event.status]) current.endedAt = event.createdAt;
		if (event.status === "failed" || event.status === "warning") {
			current.message = event.message || current.message;
		} else if (event.message) {
			current.message = event.message;
		}
	}
	return rows;
}

/** "0.4 s", "2.1 s", "1 min 4 s"; empty without both ends. */
export function stepDuration(row: StepRow): string {
	if (row.endedAt === undefined) return "";
	const ms = Date.parse(row.endedAt) - Date.parse(row.startedAt);
	if (!Number.isFinite(ms) || ms < 0) return "";
	if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
	const minutes = Math.floor(ms / 60_000);
	const seconds = Math.round((ms % 60_000) / 1000);
	return seconds === 0 ? `${minutes} min` : `${minutes} min ${seconds} s`;
}
