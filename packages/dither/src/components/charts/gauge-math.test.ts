import { describe, expect, test } from "bun:test";
import {
	defaultSegments,
	newBlocks,
	shapeAngle,
	shapeBox,
	shapeSegments,
	shapeSpan,
} from "./gauge-math";

describe("shapeSpan", () => {
	test("dial sweeps 270° from the bottom left", () => {
		const { start, sweep } = shapeSpan("dial");
		expect(sweep).toBeCloseTo((3 * Math.PI) / 2, 8);
		expect(start).toBeCloseTo((-3 * Math.PI) / 4, 8);
	});
	test("half sweeps the top 180°", () => {
		expect(shapeSpan("half")).toEqual({ start: -Math.PI / 2, sweep: Math.PI });
	});
	test("ring is a full turn from the top", () => {
		expect(shapeSpan("ring")).toEqual({ start: 0, sweep: Math.PI * 2 });
	});
});

describe("shapeSegments", () => {
	test("blocks tile the arc with a gap", () => {
		const segments = shapeSegments(4, "ring", 0.2);
		expect(segments).toHaveLength(4);
		const pitch = (Math.PI * 2) / 4;
		expect(segments[0]?.mid).toBeCloseTo(pitch / 2, 8);
		expect((segments[0]?.to ?? 0) - (segments[0]?.from ?? 0)).toBeCloseTo(
			pitch * 0.8,
			8,
		);
	});
	test("at least one block", () => {
		expect(shapeSegments(0, "half")).toHaveLength(1);
	});
});

describe("shapeAngle", () => {
	test("maps 0…1 across the sweep and clamps", () => {
		expect(shapeAngle(0, "ring")).toBe(0);
		expect(shapeAngle(0.5, "ring")).toBeCloseTo(Math.PI, 8);
		expect(shapeAngle(2, "half")).toBeCloseTo(Math.PI / 2, 8);
		expect(shapeAngle(-1, "half")).toBeCloseTo(-Math.PI / 2, 8);
	});
});

describe("shapeBox", () => {
	test("half stops at its baseline, ring is square", () => {
		expect(shapeBox("half", 200, 6).height).toBe(100 + 6);
		expect(shapeBox("ring", 200, 6).height).toBe(200);
	});
	test("dial hangs below the centre by the arc ends", () => {
		const box = shapeBox("dial", 200, 6);
		expect(box.height).toBe(Math.ceil(100 + 94 * Math.SQRT1_2 + 6));
	});
});

describe("defaults and new blocks", () => {
	test("segment defaults", () => {
		expect(defaultSegments("ring")).toBe(48);
		expect(defaultSegments("dial")).toBe(24);
	});
	test("only blocks above the last drawn one are new", () => {
		expect(newBlocks(-1, 3)).toEqual([0, 3]);
		expect(newBlocks(2, 5)).toEqual([3, 5]);
		expect(newBlocks(5, 5)).toBeNull();
		expect(newBlocks(5, 2)).toBeNull();
	});
});
