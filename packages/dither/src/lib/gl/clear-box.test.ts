import { describe, expect, test } from "bun:test";
import {
	type ClearBox,
	clearBoxFromRect,
	clearBoxUniforms,
	MAX_CLEAR_BOXES,
	sameClearBoxes,
} from "./clear-box";
import { computeLayout } from "./layout";

const box = (over: Partial<ClearBox> = {}): ClearBox => ({
	x: 30,
	y: 60,
	w: 90,
	h: 45,
	radius: 12,
	feather: 24,
	...over,
});

describe("clearBoxUniforms", () => {
	test("CSS px become cells and y stays from the top", () => {
		const layout = computeLayout(400, 300, 3, 1, "square");
		const u = clearBoxUniforms([box()], layout);
		expect(u.clearRect0).toEqual([10, 20, 30, 15]);
		expect(u.clearShape01).toEqual([4, 8, 0, 0]);
		expect(u.clearN).toBe(1);
	});

	test("the DPR reaches the boxes through the layout's snapped cell", () => {
		// 3 CSS px at 1.5x is 4.5 device px, snapped to 5: a cell is 3.333 CSS px.
		const layout = computeLayout(400, 300, 3, 1.5, "square");
		expect(layout.cellCss).toBeCloseTo(10 / 3, 9);
		const u = clearBoxUniforms([box({ x: 100, w: 200 })], layout);
		const rect = u.clearRect0 as readonly number[];
		expect(rect[0]).toBeCloseTo(30, 9);
		expect(rect[2]).toBeCloseTo(60, 9);
		// A 2x screen keeps the 3px cell, so the same box is the same cells.
		const x2 = clearBoxUniforms(
			[box()],
			computeLayout(400, 300, 3, 2, "square"),
		);
		expect(x2.clearRect0).toEqual([10, 20, 30, 15]);
	});

	test("a unit cell gives the boxes back in CSS px", () => {
		const u = clearBoxUniforms([box(), box({ x: 5, radius: 0, feather: 0 })], {
			cellCss: 1,
		});
		expect(u.clearRect0).toEqual([30, 60, 90, 45]);
		expect(u.clearRect1).toEqual([5, 60, 90, 45]);
		expect(u.clearShape01).toEqual([12, 24, 0, 0]);
		expect(u.clearN).toBe(2);
	});

	test("more than four boxes are cut to four", () => {
		const many = Array.from({ length: 7 }, (_, i) => box({ x: i * 10 }));
		const u = clearBoxUniforms(many, { cellCss: 1 });
		expect(MAX_CLEAR_BOXES).toBe(4);
		expect(u.clearN).toBe(4);
		expect(u.clearRect3).toEqual([30, 60, 90, 45]);
	});

	test("an empty list clears nothing and keeps every key", () => {
		const u = clearBoxUniforms([], { cellCss: 3 });
		const full = clearBoxUniforms([box(), box(), box(), box()], {
			cellCss: 3,
		});
		expect(u.clearN).toBe(0);
		expect(Object.keys(u).sort()).toEqual(Object.keys(full).sort());
		expect(u.clearRect0).toEqual([0, 0, 0, 0]);
		expect(u.clearShape23).toEqual([0, 0, 0, 0]);
	});

	test("slots past the count are zeroed", () => {
		const u = clearBoxUniforms([box()], { cellCss: 1 });
		expect(u.clearRect1).toEqual([0, 0, 0, 0]);
		expect(u.clearShape01).toEqual([12, 24, 0, 0]);
	});
});

describe("clearBoxFromRect", () => {
	const options = { pad: 24, radius: 16, feather: 32 };

	test("pads the element and measures from the host's top-left", () => {
		const host = { left: 100, top: 50, right: 1100, bottom: 650 };
		const el = { left: 140, top: 130, right: 540, bottom: 330 };
		expect(clearBoxFromRect(el, host, options)).toEqual({
			x: 16,
			y: 56,
			w: 448,
			h: 248,
			radius: 16,
			feather: 32,
		});
	});

	test("rounds outward to whole pixels, so the element is always covered", () => {
		const host = { left: 0, top: 0, right: 800, bottom: 600 };
		const el = { left: 100.4, top: 50.6, right: 300.2, bottom: 120.1 };
		const b = clearBoxFromRect(el, host, { pad: 0, radius: 0, feather: 0 });
		expect(b.x).toBe(100);
		expect(b.y).toBe(50);
		expect(b.x + b.w).toBe(301);
		expect(b.y + b.h).toBe(121);
	});

	test("a negative radius or feather is a hard, square box", () => {
		const host = { left: 0, top: 0, right: 10, bottom: 10 };
		const el = { left: 1, top: 1, right: 5, bottom: 5 };
		const b = clearBoxFromRect(el, host, { pad: 0, radius: -4, feather: -1 });
		expect([b.radius, b.feather]).toEqual([0, 0]);
	});
});

describe("sameClearBoxes", () => {
	test("equal values are the same, any difference or length is not", () => {
		expect(sameClearBoxes([box()], [box()])).toBe(true);
		expect(sameClearBoxes([], [])).toBe(true);
		expect(sameClearBoxes([box()], [box({ w: 91 })])).toBe(false);
		expect(sameClearBoxes([box()], [box(), box()])).toBe(false);
	});
});
