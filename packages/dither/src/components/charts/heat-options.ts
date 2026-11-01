import { dayToIso, parseDay, weekdayOf } from "../../lib/heat-math";

/**
 * Pure option helpers for `DitherHeatmap`: which weeks a calendar shows, the
 * unit word for a value, and the name of each step in the scale. Covered by
 * `heat-options.test.ts`.
 */

/** What a value counts: one word, or a singular and a plural. */
export type HeatUnit = string | { one: string; other: string };

/** The unit for `value`: `{ one: "deploy", other: "deploys" }` reads "deploy" for 1. */
export function unitWord(
	unit: HeatUnit | undefined,
	value: number | null,
): string {
	if (unit === undefined) return "value";
	if (typeof unit === "string") return unit;
	return value === 1 ? unit.one : unit.other;
}

/** The unit in the table header and summary, where no single value applies. */
export function unitPlural(unit: HeatUnit | undefined): string {
	return unitWord(unit, null);
}

/**
 * The first day (`YYYY-MM-DD`) of a calendar that shows `weeks` week columns
 * ending with the week of the latest day (`to`, else the latest entry).
 * `null` when there is no date to anchor on.
 */
export function calendarStartForWeeks(
	dates: readonly (string | Date)[],
	weeks: number,
	weekStart: 0 | 1,
	to?: string | Date,
): string | null {
	const days = dates
		.map((d) => parseDay(d))
		.filter((d): d is number => d !== null);
	const end = to !== undefined ? parseDay(to) : null;
	const latest = end ?? (days.length > 0 ? Math.max(...days) : null);
	if (latest === null) return null;
	const row = (weekdayOf(latest) - weekStart + 7) % 7;
	const count = Math.max(1, Math.floor(weeks));
	return dayToIso(latest - row - (count - 1) * 7);
}

/** How many week columns of at least `minSize` px (plus `gap`) fit in `width` after a `labelW` gutter. */
export function weeksThatFit(
	width: number,
	labelW: number,
	gap: number,
	minSize: number,
): number {
	return Math.max(1, Math.floor((width - labelW + gap) / (minSize + gap)));
}

/**
 * The range a step of the scale covers, as text: `No data or zero`,
 * `20 to 40`, `80 and up`. Mirrors `levelOf`: step L covers
 * `(max·(L−1)/levels, max·L/levels]`, the top step everything above.
 */
export function levelRangeText(
	level: number,
	max: number,
	levels: number,
	fmt: (value: number) => string,
): string {
	if (level <= 0) return "No data or zero";
	if (!(max > 0)) return "No values yet";
	const low = (max * (level - 1)) / levels;
	if (level >= levels) return `${fmt(low)} and up`;
	return `${fmt(low)} to ${fmt((max * level) / levels)}`;
}
