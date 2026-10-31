import { describe, expect, test } from "bun:test";
import {
	coarseLadder,
	quantizeProgress,
	revealView,
	SHARPEN_SHARE,
	scrollProgress,
} from "./reveal";

describe("scroll reveal maths", () => {
	test("the ladder runs from the coarsest block down to one on a log scale", () => {
		const ladder = coarseLadder(24);
		expect(ladder[0]).toBe(24);
		expect(ladder.at(-1)).toBe(1);
		expect(ladder.length).toBeLessThanOrEqual(9);
		expect([...ladder].sort((a, b) => b - a)).toEqual(ladder);
		expect(new Set(ladder).size).toBe(ladder.length);
		expect(coarseLadder(16, 5)).toEqual([16, 8, 4, 2, 1]);
		expect(coarseLadder(2)).toEqual([2, 1]);
		expect(coarseLadder(1)).toEqual([1]);
		expect(coarseLadder(0)).toEqual([1]);
	});

	test("progress is kept to a few hundred steps", () => {
		expect(quantizeProgress(0.5)).toBe(0.5);
		expect(quantizeProgress(-1)).toBe(0);
		expect(quantizeProgress(4)).toBe(1);
		expect(quantizeProgress(0.5004)).toBe(0.5);
	});

	test("progress runs from the bottom edge to 30% down, clamped", () => {
		expect(scrollProgress(800, 800)).toBe(0);
		expect(scrollProgress(1200, 800)).toBe(0);
		expect(scrollProgress(240, 800)).toBe(1);
		expect(scrollProgress(-50, 800)).toBe(1);
		expect(scrollProgress(520, 800)).toBeCloseTo(0.5, 5);
		expect(scrollProgress(100, 1000, 1, 0.5)).toBe(1);
	});

	test("stages sharpen monotonically and finish at the last", () => {
		const stages = [0, 0.1, 0.3, 0.5, SHARPEN_SHARE].map(
			(p) => revealView(p, 5, "photo").stage,
		);
		expect([...stages].sort((a, b) => a - b)).toEqual(stages);
		expect(stages[0]).toBe(0);
		expect(stages.at(-1)).toBe(4);
	});

	test("the photo dissolves in only after the dither is sharp, and only when it is the end", () => {
		expect(revealView(0.5, 5, "photo").photo).toBe(0);
		expect(revealView(SHARPEN_SHARE, 5, "photo").photo).toBe(0);
		expect(revealView(1, 5, "photo")).toEqual({ stage: 4, photo: 16 });
		const mid = revealView((1 + SHARPEN_SHARE) / 2, 5, "photo").photo;
		expect(mid).toBeGreaterThan(0);
		expect(mid).toBeLessThan(16);
		expect(revealView(1, 5, "dither")).toEqual({ stage: 4, photo: 0 });
	});
});
