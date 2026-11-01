import { describe, expect, test } from "bun:test";
import {
	autoGap,
	bandEdges,
	barSlots,
	bodyBands,
	bucketRanges,
	dayFraction,
	dayStatus,
	fitCells,
	formatUptime,
	groupLayout,
	markerIndices,
	markerStride,
	percentShares,
	rectPath,
	slotAt,
	splitRuns,
	stackPixels,
	uptimeFraction,
	worstStatus,
} from "./bars-math";

describe("groupLayout", () => {
	test("splits the plot into equal steps with centred bars", () => {
		const layout = groupLayout(4, 400, 10);
		expect(layout.step).toBe(100);
		expect(layout.width).toBe(90);
		expect(layout.x(0)).toBe(5);
		expect(layout.x(3)).toBe(305);
	});
	test("a derived gap never swallows the bar", () => {
		expect(autoGap(2)).toBe(1);
		expect(autoGap(200)).toBe(16);
		const tight = groupLayout(100, 100);
		expect(tight.width).toBeGreaterThanOrEqual(1);
		expect(groupLayout(0, 100).step).toBe(100);
	});
});

describe("barSlots", () => {
	test("grouped bars share the width with an inner gap", () => {
		const slots = barSlots(10, 62, 3, "grouped", 2);
		expect(slots).toEqual([
			{ x: 10, w: 19.3 },
			{ x: 31.3, w: 19.3 },
			{ x: 52.7, w: 19.3 },
		]);
	});
	test("stacked bars all take the full group width", () => {
		expect(barSlots(4, 20, 3, "stacked")).toEqual([
			{ x: 4, w: 20 },
			{ x: 4, w: 20 },
			{ x: 4, w: 20 },
		]);
		expect(barSlots(4, 20, 1, "grouped")).toEqual([{ x: 4, w: 20 }]);
	});
	test("drops the inner gap when bars would be hairlines", () => {
		const slots = barSlots(0, 6, 3, "grouped", 2);
		expect(slots.map((s) => s.w)).toEqual([2, 2, 2]);
		expect(slots.map((s) => s.x)).toEqual([0, 2, 4]);
	});
	test("no series, no slots", () => {
		expect(barSlots(0, 10, 0, "grouped")).toEqual([]);
	});
});

describe("slotAt", () => {
	test("finds the group under a pointer and clamps outside", () => {
		expect(slotAt(0, 50, 4)).toBe(0);
		expect(slotAt(99, 50, 4)).toBe(1);
		expect(slotAt(-20, 50, 4)).toBe(0);
		expect(slotAt(900, 50, 4)).toBe(3);
		expect(slotAt(10, 50, 0)).toBe(0);
	});
});

describe("percentShares", () => {
	test("normalises a column to 100 and ignores negatives", () => {
		expect(percentShares([1, 1, 2])).toEqual([25, 25, 50]);
		expect(percentShares([-4, 4])).toEqual([0, 100]);
		expect(percentShares([0, 0])).toEqual([0, 0]);
	});
});

describe("stackPixels", () => {
	test("stacks segments upward", () => {
		expect(stackPixels([10, 20, 30], 2)).toEqual([
			{ from: 0, to: 20 },
			{ from: 20, to: 60 },
			{ from: 60, to: 120 },
		]);
	});
	test("lifts thin segments to the minimum and keeps the column's total", () => {
		const spans = stackPixels([100, 1], 1, 2);
		expect(spans).toEqual([
			{ from: 0, to: 99 },
			{ from: 99, to: 101 },
		]);
	});
	test("zero and invalid values take no room", () => {
		const spans = stackPixels([0, Number.NaN, 5], 10);
		expect(spans[0]).toEqual({ from: 0, to: 0 });
		expect(spans[1]).toEqual({ from: 0, to: 0 });
		expect(spans[2]).toEqual({ from: 0, to: 50 });
	});
	test("a column of only thin segments keeps each at the minimum", () => {
		const spans = stackPixels([0.1, 0.2], 1, 2);
		expect(spans).toEqual([
			{ from: 0, to: 2 },
			{ from: 2, to: 4 },
		]);
	});
});

describe("bodyBands", () => {
	const tones = [0.8, 0.6, 0.4, 0.2];
	test("a tall bar holds its last tone down to the baseline", () => {
		const bands = bodyBands(100, 40, tones);
		expect(bands.map((b) => b.y)).toEqual([0, 10, 20, 30]);
		expect(bands.map((b) => b.h)).toEqual([10, 10, 10, 70]);
		expect(bands.map((b) => b.tone)).toEqual(tones);
	});
	test("a short bar only gets the dense end", () => {
		const bands = bodyBands(15, 40, tones);
		expect(bands).toEqual([
			{ i: 0, y: 0, h: 10, tone: 0.8 },
			{ i: 1, y: 10, h: 5, tone: 0.6 },
		]);
	});
	test("covers the whole height exactly, with no gaps", () => {
		for (const height of [3, 17, 40, 41, 250]) {
			const bands = bodyBands(height, 40, tones);
			const total = bands.reduce((sum, b) => sum + b.h, 0);
			expect(Math.abs(total - height)).toBeLessThan(0.11);
		}
	});
	test("nothing to draw for an empty bar", () => {
		expect(bodyBands(0, 40, tones)).toEqual([]);
		expect(bodyBands(10, 40, [])).toEqual([]);
	});
});

describe("rectPath", () => {
	test("is a closed rectangle", () => {
		expect(rectPath(1, 2, 3, 4)).toBe("M1 2h3v4h-3Z");
	});
});

describe("status and uptime", () => {
	test("worstStatus ranks down > degraded > up > unknown", () => {
		expect(worstStatus(["up", "down", "degraded"])).toBe("down");
		expect(worstStatus(["up", "degraded"])).toBe("degraded");
		expect(worstStatus(["unknown", "up"])).toBe("up");
		expect(worstStatus([])).toBe("unknown");
	});
	test("dayStatus prefers the stated status, else thresholds the fraction", () => {
		expect(dayStatus({ status: "down", uptime: 1 })).toBe("down");
		expect(dayStatus({ uptime: 1 })).toBe("up");
		expect(dayStatus({ uptime: 0.999 })).toBe("up");
		expect(dayStatus({ uptime: 0.98 })).toBe("degraded");
		expect(dayStatus({ uptime: 0.5 })).toBe("down");
		expect(dayStatus({})).toBe("unknown");
		expect(dayStatus({ uptime: 0.97 }, { up: 0.99, degraded: 0.98 })).toBe(
			"down",
		);
	});
	test("dayFraction uses the fraction, else the status's weight", () => {
		expect(dayFraction({ uptime: 0.42 })).toBe(0.42);
		expect(dayFraction({ uptime: 4 })).toBe(1);
		expect(dayFraction({ status: "up" })).toBe(1);
		expect(dayFraction({ status: "degraded" })).toBe(0.5);
		expect(dayFraction({ status: "down" })).toBe(0);
		expect(dayFraction({ status: "unknown" })).toBeNull();
	});
	test("uptimeFraction averages the days that have data", () => {
		expect(uptimeFraction([])).toBeNull();
		expect(uptimeFraction([{ status: "unknown" }])).toBeNull();
		expect(
			uptimeFraction([
				{ status: "up" },
				{ status: "up" },
				{ status: "unknown" },
				{ status: "down" },
				{ status: "degraded" },
			]),
		).toBe(0.625);
		expect(uptimeFraction([{ uptime: 0.9996 }, { uptime: 1 }])).toBeCloseTo(
			0.9998,
			6,
		);
	});
	test("formatUptime floors so downtime never reads 100%", () => {
		expect(formatUptime(1)).toBe("100%");
		expect(formatUptime(0.9998)).toBe("99.98%");
		expect(formatUptime(0.999999)).toBe("99.99%");
		expect(formatUptime(0.9997)).toBe("99.97%");
		expect(formatUptime(0.5)).toBe("50.00%");
		expect(formatUptime(0.99, 1)).toBe("99.0%");
		expect(formatUptime(-1)).toBe("0.00%");
	});
});

describe("fitCells and bucketRanges", () => {
	test("one cell per day while they fit", () => {
		const fit = fitCells(300, 30, { gap: 2, minCell: 3 });
		expect(fit.cells).toBe(30);
		expect(fit.cellW).toBeCloseTo((300 - 58) / 30, 6);
		expect(fit.step).toBeCloseTo(fit.cellW + 2, 6);
	});
	test("days share cells when the row is too narrow", () => {
		const fit = fitCells(100, 90, { gap: 2, minCell: 3 });
		expect(fit.cells).toBe(Math.floor(102 / 5));
		expect(fit.cellW).toBeGreaterThanOrEqual(3);
		expect(fit.cells * fit.step - 2).toBeLessThanOrEqual(100.0001);
	});
	test("few days stop growing at the maximum", () => {
		expect(fitCells(600, 7, { maxCell: 20 }).cellW).toBe(20);
	});
	test("degenerate input draws nothing", () => {
		expect(fitCells(0, 10).cells).toBe(0);
		expect(fitCells(100, 0).cells).toBe(0);
		expect(fitCells(2, 5).cells).toBe(1);
	});
	test("bucketRanges partitions every day once, in order", () => {
		const ranges = bucketRanges(90, 20);
		expect(ranges).toHaveLength(20);
		expect(ranges[0]?.[0]).toBe(0);
		expect(ranges.at(-1)?.[1]).toBe(90);
		for (let k = 1; k < ranges.length; k += 1) {
			expect(ranges[k]?.[0]).toBe(ranges[k - 1]?.[1] ?? -1);
		}
		const sizes = ranges.map(([a, b]) => b - a);
		expect(Math.max(...sizes) - Math.min(...sizes)).toBeLessThanOrEqual(1);
		expect(bucketRanges(3, 10)).toEqual([
			[0, 1],
			[1, 2],
			[2, 3],
		]);
		expect(bucketRanges(0, 5)).toEqual([]);
	});
});

describe("splitRuns", () => {
	test("lifts the line across gaps", () => {
		expect(splitRuns([1, 2, null, 4, undefined, 6, 7, 8])).toEqual([
			{ start: 0, values: [1, 2] },
			{ start: 3, values: [4] },
			{ start: 5, values: [6, 7, 8] },
		]);
	});
	test("treats NaN and Infinity as gaps; empty has no runs", () => {
		expect(splitRuns([Number.NaN, 1, Number.POSITIVE_INFINITY])).toEqual([
			{ start: 1, values: [1] },
		]);
		expect(splitRuns([])).toEqual([]);
		expect(splitRuns([null, null])).toEqual([]);
	});
});

describe("bandEdges", () => {
	test("orders the edges and blanks points missing either one", () => {
		const { low, high } = bandEdges([5, 1, null, 3], [2, 4, 9, undefined]);
		expect(low).toEqual([2, 1, null, null]);
		expect(high).toEqual([5, 4, null, null]);
	});
});

describe("markers", () => {
	test("stride keeps markers apart", () => {
		expect(markerStride(20)).toBe(1);
		expect(markerStride(4)).toBe(3);
		expect(markerStride(0)).toBe(1);
	});
	test("none, last and all", () => {
		const values = [1, null, 3, 4, null];
		expect(markerIndices(values, "none")).toEqual([]);
		expect(markerIndices(values, "last")).toEqual([3]);
		expect(markerIndices(values, "all")).toEqual([0, 2, 3]);
		expect(markerIndices(values, "all", 2)).toEqual([0, 2]);
		expect(markerIndices([null], "last")).toEqual([]);
	});
});
