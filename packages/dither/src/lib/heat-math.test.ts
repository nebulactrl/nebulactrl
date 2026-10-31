import { describe, expect, test } from "bun:test";
import {
	calendarLayout,
	dayToIso,
	heatMax,
	levelTones,
	parseDay,
	weekdayLabels,
	weekdayOf,
} from "./heat-math";

describe("heatMax", () => {
	test("is the largest finite value in the matrix", () => {
		expect(
			heatMax([
				[0, 1, 50],
				[null, 100, undefined],
			]),
		).toBe(100);
	});

	test("an all-empty matrix has max 0", () => {
		expect(heatMax([[null, 0], []])).toBe(0);
	});
});

describe("levelTones", () => {
	test("empty is 0, level 1 is the floor, the last is solid", () => {
		const tones = levelTones(5);
		expect(tones).toHaveLength(6);
		expect(tones[0]).toBe(0);
		expect(tones[1]).toBeCloseTo(0.12);
		expect(tones[5]).toBe(1);
		for (let i = 2; i < tones.length; i += 1) {
			expect(tones[i] ?? 0).toBeGreaterThan(tones[i - 1] ?? 0);
		}
	});
});

describe("dates", () => {
	test("parseDay reads ISO days in UTC and rejects roll-overs", () => {
		expect(dayToIso(parseDay("2026-03-01") ?? -1)).toBe("2026-03-01");
		expect(parseDay("2026-02-31")).toBeNull();
		expect(parseDay("not a date")).toBeNull();
		expect(parseDay(new Date(Date.UTC(2026, 0, 1, 23, 59)))).toBe(
			parseDay("2026-01-01"),
		);
	});

	test("weekday: 2026-01-01 is a Thursday", () => {
		expect(weekdayOf(parseDay("2026-01-01") ?? 0)).toBe(4);
		expect(weekdayLabels(1)).toEqual([
			"Mon",
			"Tue",
			"Wed",
			"Thu",
			"Fri",
			"Sat",
			"Sun",
		]);
	});
});

describe("calendarLayout", () => {
	test("empty input gives an empty layout", () => {
		expect(calendarLayout([])).toEqual({ cells: [], weeks: 0, months: [] });
	});

	test("fills every day, pads the first column, and sums duplicates", () => {
		// Thu 2026-01-01 … Mon 2026-01-12, weeks start on Sunday.
		const layout = calendarLayout([
			{ date: "2026-01-01", value: 2 },
			{ date: "2026-01-01", value: 3 },
			{ date: "2026-01-12", value: 7 },
		]);
		expect(layout.cells).toHaveLength(12);
		expect(layout.cells[0]).toEqual({
			date: "2026-01-01",
			week: 0,
			day: 4,
			value: 5,
		});
		// Sat 3 Jan ends week 0; Sun 4 Jan opens week 1.
		expect(layout.cells[2]).toMatchObject({ week: 0, day: 6, value: null });
		expect(layout.cells[3]).toMatchObject({
			date: "2026-01-04",
			week: 1,
			day: 0,
		});
		expect(layout.cells.at(-1)).toMatchObject({
			date: "2026-01-12",
			week: 2,
			day: 1,
			value: 7,
		});
		expect(layout.weeks).toBe(3);
	});

	test("weekStart 1 starts rows on Monday", () => {
		const layout = calendarLayout([{ date: "2026-01-05", value: 1 }], {
			weekStart: 1,
		});
		// 2026-01-05 is a Monday: row 0.
		expect(layout.cells[0]).toMatchObject({ week: 0, day: 0 });
	});

	test("explicit from/to extend the range", () => {
		const layout = calendarLayout([{ date: "2026-01-05", value: 1 }], {
			from: "2026-01-04",
			to: "2026-01-06",
		});
		expect(layout.cells.map((c) => c.date)).toEqual([
			"2026-01-04",
			"2026-01-05",
			"2026-01-06",
		]);
		expect(layout.cells.map((c) => c.value)).toEqual([null, 1, null]);
	});

	test("month labels sit on the month's first column; a partial first month yields", () => {
		// Jan 30 2026 (Fri) … Apr 2: the Jan sliver is 1 column from Feb.
		const layout = calendarLayout([
			{ date: "2026-01-30", value: 1 },
			{ date: "2026-04-02", value: 1 },
		]);
		expect(layout.months.map((m) => m.label)).toEqual(["Feb", "Mar", "Apr"]);
		for (const m of layout.months) {
			const cell = layout.cells.find((c) => c.week === m.week);
			expect(cell).toBeDefined();
		}
		// Each label is a column where a new month's first day (or the start) lands.
		const feb = layout.months[0];
		const firstOfFeb = layout.cells.find((c) => c.date === "2026-02-01");
		expect(feb?.week).toBe(firstOfFeb?.week ?? -1);
	});
});
