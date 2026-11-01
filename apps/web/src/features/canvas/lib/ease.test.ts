import { describe, expect, test } from "bun:test";
import { arrangeEase, cubicBezier } from "./ease";

describe("cubicBezier", () => {
	test("starts at 0 and ends at 1", () => {
		expect(arrangeEase(0)).toBe(0);
		expect(arrangeEase(1)).toBe(1);
	});

	test("the middle of the time is a little past the middle of the way", () => {
		// Its second handle (0.175) is nearer the end than its first (0.77) is to the start.
		expect(arrangeEase(0.5)).toBeGreaterThan(0.5);
		expect(arrangeEase(0.5)).toBeLessThan(0.7);
	});

	test("never goes backwards", () => {
		let last = 0;
		for (let i = 1; i <= 100; i++) {
			const value = arrangeEase(i / 100);
			expect(value).toBeGreaterThanOrEqual(last);
			last = value;
		}
	});

	test("matches the CSS keyword for a known curve", () => {
		// `ease` is cubic-bezier(0.25, 0.1, 0.25, 1): 0.5 of the time is about 0.8 of the way.
		expect(cubicBezier(0.25, 0.1, 0.25, 1)(0.5)).toBeCloseTo(0.8024, 3);
	});

	test("starts slow and finishes slow", () => {
		expect(arrangeEase(0.1)).toBeLessThan(0.05);
		expect(arrangeEase(0.9)).toBeGreaterThan(0.95);
	});
});
