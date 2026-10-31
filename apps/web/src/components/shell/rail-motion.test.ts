import { describe, expect, test } from "bun:test";
import { durations } from "@nebulactrl/tokens";
import { clipFromRight, columnKeyframes, railDuration } from "./rail-motion";

describe("railDuration", () => {
	test("expanding takes the fast token, in seconds", () => {
		expect(railDuration(false, false)).toBe(durations.fast / 1000);
	});

	test("collapsing is quicker than expanding but stays over 150ms", () => {
		const collapse = railDuration(true, false);
		expect(collapse).toBeLessThan(railDuration(false, false));
		expect(collapse).toBeGreaterThan(durations.quick / 1000);
	});

	test("reduced motion is the short fade, either way", () => {
		expect(railDuration(true, true)).toBe(durations.quick / 1000);
		expect(railDuration(false, true)).toBe(durations.quick / 1000);
	});
});

describe("columnKeyframes", () => {
	test("a collapse starts the column where it was: the shrink to the right of rest", () => {
		// 220px sidebar to 64px: the column's left edge moves 220 -> 64.
		expect(columnKeyframes(0, 156).transform).toEqual([
			"translateX(156px)",
			"none",
		]);
	});

	test("an expand starts to the left of rest", () => {
		expect(columnKeyframes(0, -156).transform).toEqual([
			"translateX(-156px)",
			"none",
		]);
	});

	test("a toggle during a slide adds to where the column is right now", () => {
		// Expanding was 100px short of rest (-100); collapsing from there
		// starts 156 further along: the column does not jump.
		expect(columnKeyframes(-100, 156).transform[0]).toBe("translateX(56px)");
	});
});

describe("clipFromRight", () => {
	test("hides the given width from the right edge", () => {
		expect(clipFromRight(0)).toBe("inset(0px 0px 0px 0px)");
		expect(clipFromRight(156)).toBe("inset(0px 156px 0px 0px)");
	});
});
