import { describe, expect, test } from "bun:test";
import { syncByTimestamp, timeTicks } from "./chart-time";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

describe("syncByTimestamp", () => {
	const ticks = [{ value: 0 }, { value: 60_000 }, { value: 600_000 }];

	test("picks the sample nearest the hovered instant", () => {
		expect(syncByTimestamp(ticks, { activeLabel: 10_000 })).toBe(0);
		expect(syncByTimestamp(ticks, { activeLabel: 45_000 })).toBe(1);
		expect(syncByTimestamp(ticks, { activeLabel: "590000" })).toBe(2);
	});

	test("hides the cursor when there is nothing to show", () => {
		expect(syncByTimestamp([], { activeLabel: 1 })).toBe(-1);
		expect(syncByTimestamp(ticks, { activeLabel: undefined })).toBe(-1);
		expect(syncByTimestamp(ticks, { activeLabel: "soon" })).toBe(-1);
	});
});

describe("timeTicks", () => {
	// UTC clock, so the marks are the same wherever the test runs.
	const utc = 0;

	test("an hour is marked every 10 minutes, on the clock", () => {
		const from = Date.UTC(2026, 9, 1, 12, 7);
		const ticks = timeTicks(from, from + HOUR, 6, utc);
		expect(ticks.map((t) => new Date(t).toISOString().slice(11, 16))).toEqual([
			"12:10",
			"12:20",
			"12:30",
			"12:40",
			"12:50",
			"13:00",
		]);
	});

	test("marks follow the visitor's clock, not UTC", () => {
		const from = Date.UTC(2026, 9, 1, 12, 0);
		// +05:30: local 17:30 is the first half hour at or after the start.
		const ticks = timeTicks(from, from + 2 * HOUR, 6, 330);
		expect(ticks[0]).toBe(Date.UTC(2026, 9, 1, 12, 0));
		expect(ticks.every((t) => (t + 330 * MINUTE) % (30 * MINUTE) === 0)).toBe(
			true,
		);
	});

	test("never more marks than asked for, and a day-wide range steps by hours", () => {
		const from = Date.UTC(2026, 9, 1, 0, 0);
		const ticks = timeTicks(from, from + 24 * HOUR, 6, utc);
		expect(ticks.length).toBeLessThanOrEqual(7);
		expect((ticks[1] ?? 0) - (ticks[0] ?? 0)).toBe(6 * HOUR);
	});

	test("an empty or backwards range has no marks", () => {
		expect(timeTicks(5, 5)).toEqual([]);
		expect(timeTicks(10, 5)).toEqual([]);
	});
});
