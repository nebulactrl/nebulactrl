import { levelTone } from "./chart-math";

/**
 * Pure layout for the dithered heatmap: level tones, and the GitHub-style
 * calendar (weeks as columns, weekdays as rows). Dates are read and written
 * as UTC days so a layout never shifts with the visitor's time zone.
 */

/** A matrix / calendar value. `null` and `undefined` mean "no data". */
export type HeatValue = number | null | undefined;

const DAY_MS = 86_400_000;

/** Largest finite positive value in a matrix, or 0 when there is none. */
export function heatMax(values: readonly (readonly HeatValue[])[]): number {
	let max = 0;
	for (const row of values) {
		for (const v of row) {
			if (typeof v === "number" && Number.isFinite(v) && v > max) max = v;
		}
	}
	return max;
}

/**
 * Tone of every level, index 0 (empty) first: `[0, 0.12, …, 1]`. Any value
 * above zero is at least `min`, so a quiet cell never reads as empty.
 */
export function levelTones(levels: number, min = 0.12): number[] {
	return Array.from({ length: levels + 1 }, (_, level) =>
		levelTone(level, levels, min),
	);
}

/* ---------- calendar ---------- */

export interface CalendarEntry {
	/** `YYYY-MM-DD`, any `Date.parse`-able string, or a `Date` (read in UTC). */
	date: string | Date;
	value: number;
}

export interface CalendarCell {
	/** `YYYY-MM-DD`. */
	date: string;
	/** Column, 0-based. */
	week: number;
	/** Row, 0-based: 0 is the week's first day (see `weekStart`). */
	day: number;
	/** The day's value (duplicate dates are summed); `null` when the day has no entry. */
	value: number | null;
}

export interface CalendarLayout {
	cells: CalendarCell[];
	/** Number of columns. */
	weeks: number;
	/** Month labels and the column they start over. */
	months: { week: number; label: string }[];
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

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;

/** Epoch day (UTC) of a date, or `null` when it does not parse. */
export function parseDay(date: string | Date): number | null {
	if (date instanceof Date) {
		const t = Date.UTC(
			date.getUTCFullYear(),
			date.getUTCMonth(),
			date.getUTCDate(),
		);
		return Number.isNaN(t) ? null : Math.floor(t / DAY_MS);
	}
	const iso = /^(\d{4})-(\d{2})-(\d{2})/.exec(date);
	if (iso) {
		const [, y, m, d] = iso;
		const t = Date.UTC(Number(y), Number(m) - 1, Number(d));
		const back = new Date(t);
		// Reject 2026-02-31 and friends instead of rolling over silently.
		return back.getUTCMonth() === Number(m) - 1 &&
			back.getUTCDate() === Number(d)
			? Math.floor(t / DAY_MS)
			: null;
	}
	const t = Date.parse(date);
	return Number.isNaN(t) ? null : parseDay(new Date(t));
}

/** `YYYY-MM-DD` of an epoch day. */
export function dayToIso(day: number): string {
	return new Date(day * DAY_MS).toISOString().slice(0, 10);
}

/** 0 = Sunday … 6 = Saturday. 1970-01-01 (day 0) was a Thursday. */
export function weekdayOf(day: number): number {
	return (((day + 4) % 7) + 7) % 7;
}

/** Short weekday names for the 7 rows, starting at `weekStart` (0 = Sunday, 1 = Monday). */
export function weekdayLabels(weekStart: number): string[] {
	return Array.from(
		{ length: 7 },
		(_, row) => WEEKDAYS[(row + weekStart) % 7] ?? "",
	);
}

/**
 * Lay dated values out as weeks × 7 weekdays. Every day from the first to
 * the last entry (or `from`…`to`) gets a cell, days without an entry carry
 * `null`. Entries on one date are summed; unparseable dates are skipped.
 * Month labels sit over the first column of each month; a label that would
 * land within `minMonthGap` columns of the previous one is dropped (the
 * partial first month yields to the next).
 */
export function calendarLayout(
	entries: readonly CalendarEntry[],
	{
		weekStart = 0,
		from,
		to,
		minMonthGap = 3,
	}: {
		weekStart?: number;
		from?: string | Date;
		to?: string | Date;
		minMonthGap?: number;
	} = {},
): CalendarLayout {
	const byDay = new Map<number, number>();
	for (const entry of entries) {
		const day = parseDay(entry.date);
		if (day === null || !Number.isFinite(entry.value)) continue;
		byDay.set(day, (byDay.get(day) ?? 0) + entry.value);
	}
	const days = [...byDay.keys()];
	const start = from !== undefined ? parseDay(from) : null;
	const end = to !== undefined ? parseDay(to) : null;
	const first = start ?? (days.length ? Math.min(...days) : null);
	const last = end ?? (days.length ? Math.max(...days) : null);
	if (first === null || last === null || last < first) {
		return { cells: [], weeks: 0, months: [] };
	}
	const startRow = (weekdayOf(first) - weekStart + 7) % 7;
	const cells: CalendarCell[] = [];
	for (let day = first; day <= last; day += 1) {
		const offset = day - first + startRow;
		cells.push({
			date: dayToIso(day),
			week: Math.floor(offset / 7),
			day: offset % 7,
			value: byDay.get(day) ?? null,
		});
	}

	const months: { week: number; label: string }[] = [];
	let prevMonth = -1;
	cells.forEach((cell, i) => {
		const month = new Date(first * DAY_MS + i * DAY_MS).getUTCMonth();
		if (month === prevMonth) return;
		prevMonth = month;
		const label = MONTHS[month] ?? "";
		const prev = months.at(-1);
		if (prev && cell.week - prev.week < minMonthGap) {
			if (months.length === 1) months[0] = { week: cell.week, label };
			return;
		}
		months.push({ week: cell.week, label });
	});

	return {
		cells,
		weeks: (cells.at(-1)?.week ?? -1) + 1,
		months,
	};
}
