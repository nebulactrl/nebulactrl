import type {
	UptimeDay,
	UptimeWords,
} from "@nebulactrl/dither/charts/uptime-bar";

/** Days the outcome strip covers; the API's default window. */
export const OUTCOME_DAYS = 30;

/** One UTC day of the aggregate: `failed` counts failed and rollback_failed, `other` canceled, rolled back and in flight. */
export interface DayOutcome {
	date: string;
	succeeded: number;
	failed: number;
	other: number;
}

/** The words the strip uses for each day status. */
export const OUTCOME_WORDS: UptimeWords = {
	up: "Succeeded",
	partial: "Canceled or rolled back",
	down: "Failed",
	none: "No deployments",
};

function plural(count: number, word: string): string {
	return `${count} ${word}`;
}

/**
 * The strip's days: a day with any failed deployment is `down`; else one with
 * a canceled, rolled back or still running deployment is `degraded`; else one
 * with a success is `up`; a day with no deployment has no data. The readout
 * line counts each kind.
 */
export function outcomeDays(days: readonly DayOutcome[]): UptimeDay[] {
	return days.map((d) => {
		const total = d.succeeded + d.failed + d.other;
		if (total === 0) return { date: d.date, status: "unknown" as const };
		const parts: string[] = [];
		if (d.succeeded) parts.push(plural(d.succeeded, "succeeded"));
		if (d.failed) parts.push(plural(d.failed, "failed"));
		if (d.other) parts.push(plural(d.other, "other"));
		return {
			date: d.date,
			status: d.failed
				? ("down" as const)
				: d.other
					? ("degraded" as const)
					: ("up" as const),
			detail: parts.join(", "),
		};
	});
}

/**
 * Succeeded deployments over all of them in the window, 0–1; undefined when
 * there were none (so the strip shows no figure instead of a made-up one).
 */
export function outcomeSuccessShare(
	days: readonly DayOutcome[],
): number | undefined {
	let ok = 0;
	let all = 0;
	for (const d of days) {
		ok += d.succeeded;
		all += d.succeeded + d.failed + d.other;
	}
	return all === 0 ? undefined : ok / all;
}
