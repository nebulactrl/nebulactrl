import { describe, expect, test } from "bun:test";
import { fadeDepth, runFloor, splitRuns } from "./recharts-math";

describe("splitRuns", () => {
	test("a constant baseline keeps one run", () => {
		const runs = splitRuns(
			[
				{ x: 0, y: 5 },
				{ x: 1, y: 6 },
			],
			20,
		);
		expect(runs).toHaveLength(1);
		expect(runs[0]?.bottom).toBe(20);
		expect(runs[0]?.top).toHaveLength(2);
	});

	test("null points split the series into runs", () => {
		const runs = splitRuns(
			[
				{ x: 0, y: 5 },
				{ x: 1, y: null },
				{ x: 2, y: 7 },
				{ x: 3, y: 8 },
			],
			20,
		);
		expect(runs.map((r) => r.top.length)).toEqual([1, 2]);
	});

	test("a stacked baseline pairs by index and breaks on its own nulls", () => {
		const runs = splitRuns(
			[
				{ x: 0, y: 5 },
				{ x: 1, y: 6 },
				{ x: 2, y: 7 },
			],
			[
				{ x: 0, y: 10 },
				{ x: 1, y: null },
				{ x: 2, y: 12 },
			],
		);
		expect(runs).toHaveLength(2);
		expect(runs[0]?.bottom).toEqual([{ x: 0, y: 10 }]);
		expect(runs[1]?.bottom).toEqual([{ x: 2, y: 12 }]);
	});

	test("nothing in, nothing out", () => {
		expect(splitRuns([], 5)).toEqual([]);
	});
});

describe("runFloor and fadeDepth", () => {
	const run = {
		top: [
			{ x: 0, y: 40 },
			{ x: 1, y: 60 },
		],
		bottom: 100,
	};
	test("floor is the lowest edge", () => {
		expect(runFloor(run)).toBe(100);
	});
	test("depth is 85% of the span, capped, never tiny", () => {
		expect(fadeDepth(run)).toBeCloseTo(51);
		expect(fadeDepth(run, 30)).toBe(30);
		expect(fadeDepth({ top: [{ x: 0, y: 99 }], bottom: 100 })).toBe(8);
	});
});
