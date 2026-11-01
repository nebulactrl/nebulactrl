import { expect, test } from "bun:test";
import { MARK_G_TIERS } from "../src/components/images/nebula-mark-g.generated";
import {
	bayer,
	buildTiers,
	COARSE_FROM,
	cellPx,
	fillMask,
	flatten,
	inked,
	moduleSource,
	petalMask,
	runsPath,
	shadowCells,
	shadowLevels,
	TIER_SIZES,
	WINDOW,
	windowCover,
} from "./gen-mark";

test("bayer matrices hold every threshold once", () => {
	for (const n of [2, 4, 8]) {
		const flat = bayer(n)
			.flat()
			.sort((a, b) => a - b);
		expect(flat).toEqual(Array.from({ length: n * n }, (_, i) => i));
	}
});

test("a Bayer 4x4 inks half the cells at half tone", () => {
	let on = 0;
	for (let y = 0; y < 4; y += 1)
		for (let x = 0; x < 4; x += 1) if (inked(0.5, x, y)) on += 1;
	expect(on).toBe(8);
});

test("flatten and fill give a square its area", () => {
	const polygons = flatten(
		"M0 0 C 3 0 7 0 10 0 C 10 3 10 7 10 10 C 7 10 3 10 0 10 C 0 7 0 3 0 0 Z",
	);
	const mask = fillMask(polygons, 40, -5, 2);
	expect(mask.reduce((a, b) => a + b, 0) / 4).toBeCloseTo(100, 0);
});

test("the petal mask keeps its area on every grid", () => {
	const mask = petalMask();
	const unit = (n: number) =>
		(windowCover(mask, n).reduce((a, b) => a + b, 0) * WINDOW.size ** 2) /
		n ** 2;
	expect(unit(64)).toBeGreaterThan(30000);
	expect(unit(24) / unit(64)).toBeCloseTo(1, 1);
});

test("shadow cells sit outside the mark, down and to the right", () => {
	const n = 8;
	const cover = new Float32Array(n * n);
	for (let y = 2; y < 5; y += 1)
		for (let x = 2; x < 5; x += 1) cover[y * n + x] = 1;
	const on = shadowCells(cover, n, [1.1, 1.1]);
	expect(on.some(Boolean)).toBe(true);
	for (let k = 0; k < n * n; k += 1) {
		if (!on[k]) continue;
		const x = k % n;
		const y = Math.floor(k / n);
		expect(cover[k]).toBe(0);
		expect(x >= 3 && y >= 3).toBe(true);
	}
});

test("runsPath merges a run into one subpath", () => {
	expect(runsPath([true, true, false, true], 2)).toBe(
		"M0 0h2v1h-2zM1 1h1v1h-1z",
	);
});

test("cells are 1 px up to the coarse threshold and 2 px from it", () => {
	expect(cellPx(COARSE_FROM - 1)).toBe(1);
	expect(cellPx(COARSE_FROM)).toBe(2);
	expect(shadowLevels(2)).toHaveLength(3);
	expect(shadowLevels(1)).toHaveLength(2);
});

test("the committed module is what the generator writes", () => {
	const tiers = buildTiers();
	expect(tiers.map((t) => t.size)).toEqual([...TIER_SIZES]);
	expect(MARK_G_TIERS).toEqual(tiers);
	expect(moduleSource(tiers)).toContain("Do not edit");
});
