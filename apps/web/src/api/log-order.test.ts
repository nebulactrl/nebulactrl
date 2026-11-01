import { describe, expect, test } from "bun:test";
import { mergeByTime, type Ordered } from "./log-order";

interface Line extends Ordered {
	name: string;
}

let next = 0;
/** A line as the stream hands it over: the id counts arrivals. */
const arrive = (name: string, ts?: string): Line =>
	ts === undefined
		? { id: ++next, name }
		: { id: ++next, name, ts: `2026-10-30T${ts}Z` };
const names = (lines: readonly Line[]) => lines.map((l) => l.name);
const ascending = (lines: readonly Line[]) =>
	lines.every((l, i) => i === 0 || l.id > (lines[i - 1] as Line).id);

describe("mergeByTime", () => {
	test("a batch later than everything shown appends and says nothing moved", () => {
		next = 0;
		const first = mergeByTime<Line>(
			[],
			[arrive("a", "10:00:00"), arrive("b", "10:00:01")],
		);
		const second = mergeByTime(first.lines, [arrive("c", "10:00:02")]);
		expect(names(second.lines)).toEqual(["a", "b", "c"]);
		expect(second.reordered).toBe(false);
		expect(ascending(second.lines)).toBe(true);
	});

	test("one service's whole backlog after another's is interleaved by time", () => {
		next = 0;
		// What the environment tail does: api's lines, then web's.
		const api = [
			arrive("api 17:04:55", "17:04:55"),
			arrive("api 17:05:10", "17:05:10"),
			arrive("api 20:45:58", "20:45:58"),
		];
		const web = [
			arrive("web 17:05:00", "17:05:00"),
			arrive("web 20:00:00", "20:00:00"),
		];
		const shown = mergeByTime<Line>([], api);
		const merged = mergeByTime(shown.lines, web);
		expect(names(merged.lines)).toEqual([
			"api 17:04:55",
			"web 17:05:00",
			"api 17:05:10",
			"web 20:00:00",
			"api 20:45:58",
		]);
		expect(merged.reordered).toBe(true);
		expect(ascending(merged.lines)).toBe(true);
		expect(new Set(merged.lines.map((l) => l.id)).size).toBe(5);
	});

	test("a batch that arrives out of order is sorted inside itself", () => {
		next = 0;
		const merged = mergeByTime<Line>(
			[],
			[arrive("late", "10:00:09"), arrive("early", "10:00:01")],
		);
		expect(names(merged.lines)).toEqual(["early", "late"]);
		expect(merged.reordered).toBe(false);
		expect(ascending(merged.lines)).toBe(true);
	});

	test("equal times keep arrival order", () => {
		next = 0;
		const shown = mergeByTime<Line>([], [arrive("one", "10:00:00")]);
		const merged = mergeByTime(shown.lines, [
			arrive("two", "10:00:00"),
			arrive("three", "10:00:00"),
		]);
		expect(names(merged.lines)).toEqual(["one", "two", "three"]);
		expect(merged.reordered).toBe(false);
	});

	test("a line without a time stays behind the line before it", () => {
		next = 0;
		const shown = mergeByTime<Line>(
			[],
			[arrive("a", "10:00:00"), arrive("z", "10:00:30")],
		);
		const merged = mergeByTime(shown.lines, [
			arrive("b", "10:00:10"),
			arrive("b-continued"),
		]);
		expect(names(merged.lines)).toEqual(["a", "b", "b-continued", "z"]);
		expect(ascending(merged.lines)).toBe(true);
	});

	test("times in other offsets and precisions compare as instants", () => {
		next = 0;
		const shown = mergeByTime<Line>(
			[],
			[{ id: ++next, name: "utc", ts: "2026-10-30T10:00:00.500Z" }],
		);
		const merged = mergeByTime(shown.lines, [
			{ id: ++next, name: "earlier", ts: "2026-10-30T11:59:59+02:00" },
		]);
		expect(names(merged.lines)).toEqual(["earlier", "utc"]);
	});

	test("lines inserted into the same gap again and again stay ordered and distinct", () => {
		next = 0;
		let shown = mergeByTime<Line>(
			[],
			[arrive("first", "10:00:00"), arrive("last", "11:00:00")],
		).lines;
		for (let i = 1; i <= 60; i++) {
			// Each new line lands just before "last": the same gap, halved each time.
			const second = String(i % 60).padStart(2, "0");
			shown = mergeByTime(shown, [arrive(`n${i}`, `10:59:${second}`)]).lines;
		}
		expect(shown.length).toBe(62);
		expect(ascending(shown)).toBe(true);
		expect(new Set(shown.map((l) => l.id)).size).toBe(62);
		expect(shown[0]?.name).toBe("first");
		expect(shown.at(-1)?.name).toBe("last");
	});

	test("an empty batch changes nothing", () => {
		next = 0;
		const shown = mergeByTime<Line>([], [arrive("a", "10:00:00")]);
		const merged = mergeByTime(shown.lines, []);
		expect(names(merged.lines)).toEqual(["a"]);
		expect(merged.reordered).toBe(false);
	});
});
