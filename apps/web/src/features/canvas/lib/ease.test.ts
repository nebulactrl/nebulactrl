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

describe("cubicBezier where Newton's method stalls", () => {
	/** An independent reference: bisect x(u) = t, then read y(u). */
	function reference(
		x1: number,
		y1: number,
		x2: number,
		y2: number,
		t: number,
	) {
		const bez = (u: number, a: number, b: number) =>
			3 * (1 - u) ** 2 * u * a + 3 * (1 - u) * u ** 2 * b + u ** 3;
		let low = 0;
		let high = 1;
		for (let i = 0; i < 60; i++) {
			const mid = (low + high) / 2;
			if (bez(mid, x1, x2) < t) low = mid;
			else high = mid;
		}
		return bez((low + high) / 2, y1, y2);
	}

	test("a flat start (zero slope at 0) still follows the curve", () => {
		const ease = cubicBezier(0, 0, 0, 1);
		for (const t of [0.0001, 0.001, 0.01, 0.5]) {
			expect(ease(t)).toBeCloseTo(reference(0, 0, 0, 1, t), 3);
		}
	});

	test("a flat end (zero slope at 1) still follows the curve", () => {
		const ease = cubicBezier(1, 0, 1, 1);
		for (const t of [0.5, 0.99, 0.9999, 0.999999]) {
			expect(ease(t)).toBeCloseTo(reference(1, 0, 1, 1, t), 3);
		}
	});

	test("values outside 0..1 time are clamped to the ends", () => {
		const ease = cubicBezier(0.4, 0, 0.2, 1);
		expect(ease(-1)).toBe(0);
		expect(ease(2)).toBe(1);
	});
});
