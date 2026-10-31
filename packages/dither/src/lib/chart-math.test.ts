import { describe, expect, test } from "bun:test";
import {
	arcPath,
	areaBands,
	compact,
	levelOf,
	levelTone,
	linear,
	linePath,
	niceStep,
	niceTicks,
	polar,
	regionPath,
	sampleCurve,
	stack,
} from "./chart-math";

describe("linear", () => {
	test("maps a domain onto a range, including inverted ranges", () => {
		expect(linear([0, 10], [0, 100])(5)).toBe(50);
		expect(linear([0, 10], [100, 0])(2.5)).toBe(75);
		expect(linear([3, 3], [0, 10])(3)).toBe(5);
	});
});

describe("niceTicks", () => {
	test("covers the data with round steps", () => {
		expect(niceTicks(0, 87, 4)).toEqual([0, 20, 40, 60, 80, 100]);
		const t = niceTicks(3, 47, 4);
		expect(t[0]).toBeLessThanOrEqual(3);
		expect(t.at(-1)).toBeGreaterThanOrEqual(47);
	});
	test("handles flat and inverted input", () => {
		expect(niceTicks(5, 5).length).toBeGreaterThan(1);
		expect(niceTicks(10, 0)[0]).toBe(0);
	});
	test("niceStep is 1/2/5 times a power of ten", () => {
		expect(niceStep(100, 5)).toBe(20);
		expect(niceStep(7, 4)).toBe(2);
	});
});

describe("compact", () => {
	test("abbreviates thousands", () => {
		expect(compact(1200)).toBe("1.2K");
	});
});

describe("sampleCurve", () => {
	const data = [
		{ x: 0, y: 10 },
		{ x: 30, y: 40 },
		{ x: 60, y: 40 },
		{ x: 90, y: 5 },
		{ x: 120, y: 60 },
	];
	test("passes through every data point", () => {
		const sampled = sampleCurve(data, "monotone", 3);
		for (const p of data) {
			expect(
				sampled.some((s) => s.x === p.x && Math.abs(s.y - p.y) < 1e-9),
			).toBe(true);
		}
	});
	test("never overshoots between points", () => {
		const sampled = sampleCurve(data, "monotone", 2);
		for (let i = 0; i < data.length - 1; i += 1) {
			const a = data[i];
			const b = data[i + 1];
			if (!a || !b) continue;
			const lo = Math.min(a.y, b.y) - 1e-6;
			const hi = Math.max(a.y, b.y) + 1e-6;
			for (const s of sampled) {
				if (s.x >= a.x && s.x <= b.x) {
					expect(s.y).toBeGreaterThanOrEqual(lo);
					expect(s.y).toBeLessThanOrEqual(hi);
				}
			}
		}
	});
	test("linear returns the points, and drops repeated x", () => {
		expect(sampleCurve(data, "linear")).toEqual(data);
		expect(
			sampleCurve(
				[
					{ x: 0, y: 1 },
					{ x: 0, y: 2 },
					{ x: 5, y: 3 },
				],
				"linear",
			),
		).toHaveLength(2);
	});
	test("samples roughly every `step` px", () => {
		const sampled = sampleCurve(data, "monotone", 3);
		expect(sampled.length).toBeGreaterThan(30);
		expect(sampled.length).toBeLessThan(60);
	});
});

describe("paths", () => {
	test("linePath is M then L", () => {
		expect(
			linePath([
				{ x: 0, y: 1 },
				{ x: 2.345, y: 3 },
			]),
		).toBe("M0 1L2.3 3");
		expect(linePath([])).toBe("");
	});
	test("regionPath closes against a baseline or a lower curve", () => {
		const top = [
			{ x: 0, y: 5 },
			{ x: 10, y: 6 },
		];
		expect(regionPath(top, 20)).toBe("M0 5L10 6L10 20L0 20Z");
		expect(
			regionPath(top, [
				{ x: 0, y: 9 },
				{ x: 10, y: 9 },
			]),
		).toBe("M0 5L10 6L10 9L0 9Z");
	});
});

describe("areaBands", () => {
	const top = [
		{ x: 0, y: 10 },
		{ x: 10, y: 20 },
	];
	test("returns one polygon per tone, densest first", () => {
		const bands = areaBands(top, {
			depth: 30,
			tones: [0.8, 0.4, 0.1],
			floor: 100,
		});
		expect(bands.map((b) => b.tone)).toEqual([0.8, 0.4, 0.1]);
		expect(bands[0]?.path).toBe("M0 10L10 20L10 30L0 20Z");
		expect(bands[1]?.path).toBe("M0 20L10 30L10 40L0 30Z");
	});
	test("the last band runs down to the floor", () => {
		const bands = areaBands(top, { depth: 30, tones: [1, 0.5], floor: 100 });
		expect(bands[1]?.path).toBe("M0 25L10 35L10 100L0 100Z");
	});
	test("empty input gives no bands", () => {
		expect(areaBands([], { depth: 10, tones: [1], floor: 0 })).toEqual([]);
	});
});

describe("stack", () => {
	test("accumulates and clamps negatives", () => {
		const { lower, upper } = stack([
			[1, 2, 3],
			[4, -1, 1],
		]);
		expect(lower).toEqual([
			[0, 0, 0],
			[1, 2, 3],
		]);
		expect(upper).toEqual([
			[1, 2, 3],
			[5, 2, 4],
		]);
	});
});

describe("polar and arcs", () => {
	test("angle 0 is 12 o'clock and π/2 is 3 o'clock", () => {
		const top = polar(50, 50, 10, 0);
		expect(top.x).toBeCloseTo(50);
		expect(top.y).toBeCloseTo(40);
		const right = polar(50, 50, 10, Math.PI / 2);
		expect(right.x).toBeCloseTo(60);
		expect(right.y).toBeCloseTo(50);
	});
	test("arcPath closes, flags large arcs, and is empty for no sweep", () => {
		expect(arcPath(0, 0, 5, 10, 0, 0)).toBe("");
		expect(arcPath(0, 0, 5, 10, 0, Math.PI / 2)).toContain(" 0 0 1 ");
		expect(arcPath(0, 0, 5, 10, 0, Math.PI * 1.5)).toContain(" 0 1 1 ");
		expect(arcPath(0, 0, 5, 10, 0, 1).endsWith("Z")).toBe(true);
		expect(arcPath(0, 0, 0, 10, 0, 1).startsWith("M0 0L")).toBe(true);
	});
});

describe("levels", () => {
	test("levelOf gives any positive value at least step 1", () => {
		expect(levelOf(0, 10, 5)).toBe(0);
		expect(levelOf(0.01, 10, 5)).toBe(1);
		expect(levelOf(10, 10, 5)).toBe(5);
		expect(levelOf(5, 10, 4)).toBe(2);
		expect(levelOf(5, 0, 4)).toBe(0);
	});
	test("levelTone runs from min to max", () => {
		expect(levelTone(0, 5)).toBe(0);
		expect(levelTone(1, 5, 0.1, 1)).toBeCloseTo(0.1);
		expect(levelTone(5, 5, 0.1, 1)).toBeCloseTo(1);
	});
});
