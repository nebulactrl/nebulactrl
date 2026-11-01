import { describe, expect, test } from "bun:test";
import {
	barState,
	cometLayers,
	gradientLayers,
	partWidths,
	percentOf,
	segmentFills,
	segmentPartSpans,
} from "./progress-math";

describe("barState", () => {
	test("bad wins over warn, null is normal", () => {
		expect(barState(50, 80, 95)).toBe("normal");
		expect(barState(80, 80, 95)).toBe("warn");
		expect(barState(96, 80, 95)).toBe("bad");
		expect(barState(null, 80, 95)).toBe("normal");
		expect(barState(99)).toBe("normal");
	});
});

describe("percentOf", () => {
	test("clamps and guards a zero max", () => {
		expect(percentOf(5, 10)).toBe(50);
		expect(percentOf(20, 10)).toBe(100);
		expect(percentOf(-3, 10)).toBe(0);
		expect(percentOf(5, 0)).toBe(0);
	});
});

describe("partWidths", () => {
	test("splits by share and never exceeds 100", () => {
		expect(partWidths([{ value: 2 }, { value: 3 }], 10)).toEqual([20, 30]);
		const over = partWidths([{ value: 8 }, { value: 8 }, { value: 8 }], 10);
		expect(over).toEqual([80, 20, 0]);
		expect(partWidths([{ value: -4 }], 10)).toEqual([0]);
	});
});

describe("segmentFills", () => {
	test("fills whole blocks then a fraction", () => {
		expect(segmentFills(3, 5, 5)).toEqual([1, 1, 1, 0, 0]);
		const part = segmentFills(50, 100, 4);
		expect(part).toEqual([1, 1, 0, 0]);
		const frac = segmentFills(30, 100, 4);
		expect(frac[0]).toBe(1);
		expect(frac[1]).toBeCloseTo(0.2);
		expect(frac[2]).toBe(0);
	});
});

describe("layers", () => {
	test("gradient layers get denser and reach less far", () => {
		const layers = gradientLayers();
		for (let i = 1; i < layers.length; i += 1) {
			const prev = layers[i - 1];
			const cur = layers[i];
			if (!prev || !cur) continue;
			expect(cur.tone).toBeGreaterThan(prev.tone);
			expect(cur.reach ?? 0).toBeLessThan(
				prev.reach ?? Number.POSITIVE_INFINITY,
			);
		}
		expect(gradientLayers(2)[1]?.reach).toBe(176);
	});
	test("comet layers nest: denser ones are shorter", () => {
		const layers = cometLayers();
		expect(layers[0]?.share).toBe(1);
		for (let i = 1; i < layers.length; i += 1) {
			expect(layers[i]?.share ?? 0).toBeLessThan(layers[i - 1]?.share ?? 0);
		}
	});
});

describe("segmentPartSpans", () => {
	test("whole blocks follow the part that covers them", () => {
		const blocks = segmentPartSpans([2, 1], 4, 4);
		expect(blocks[0]).toEqual([{ part: 0, left: 0, width: 1 }]);
		expect(blocks[1]).toEqual([{ part: 0, left: 0, width: 1 }]);
		expect(blocks[2]).toEqual([{ part: 1, left: 0, width: 1 }]);
		expect(blocks[3]).toEqual([]);
	});

	test("a block shared by two parts is split between them", () => {
		const blocks = segmentPartSpans([1.5, 1], 4, 4);
		expect(blocks[1]).toEqual([
			{ part: 0, left: 0, width: 0.5 },
			{ part: 1, left: 0.5, width: 0.5 },
		]);
		expect(blocks[2]).toEqual([{ part: 1, left: 0, width: 0.5 }]);
	});

	test("negative values count as 0 and the total is cut at max", () => {
		const blocks = segmentPartSpans([-3, 9], 2, 2);
		expect(blocks.map((b) => b.map((s) => s.part))).toEqual([[1], [1]]);
	});
});
