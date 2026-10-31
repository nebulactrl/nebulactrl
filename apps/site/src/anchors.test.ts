import { describe, expect, test } from "bun:test";
import { duration, ease, MAX_MS, MIN_MS } from "./anchors.ts";

describe("the anchor scroll's easing", () => {
	test("it starts at 0 and ends at 1, and holds outside the range", () => {
		expect(ease(0)).toBe(0);
		expect(ease(1)).toBe(1);
		expect(ease(-0.5)).toBe(0);
		expect(ease(2)).toBe(1);
	});

	test("it is monotonic: the page never scrolls back", () => {
		let last = 0;
		for (let i = 1; i <= 100; i++) {
			const y = ease(i / 100);
			expect(y).toBeGreaterThanOrEqual(last);
			last = y;
		}
	});

	test("it is an ease in out: slow at both ends, fast in the middle", () => {
		expect(ease(0.1)).toBeLessThan(0.05);
		expect(ease(0.9)).toBeGreaterThan(0.95);
		expect(ease(0.5)).toBeGreaterThan(0.3);
		expect(ease(0.5)).toBeLessThan(0.7);
		// The middle moves faster than the ends.
		expect(ease(0.55) - ease(0.45)).toBeGreaterThan(ease(0.1) - ease(0));
	});
});

describe("the anchor scroll's duration", () => {
	test("it is 300 ms plus 0.15 ms a pixel, held between 450 and 900 ms", () => {
		expect(MIN_MS).toBe(450);
		expect(MAX_MS).toBe(900);
		expect(duration(0)).toBe(450);
		expect(duration(500)).toBe(450);
		expect(duration(2000)).toBe(600);
		expect(duration(10000)).toBe(900);
	});

	test("it is the same up or down, and grows with the distance", () => {
		expect(duration(-2000)).toBe(duration(2000));
		expect(duration(3000)).toBeGreaterThan(duration(2000));
	});
});
