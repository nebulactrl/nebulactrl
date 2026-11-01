import { describe, expect, test } from "bun:test";
import {
	type BayerSize,
	bayerMatrix,
	bayerRanks,
	ditherOn,
	inkedCells,
	quantize,
} from "./bayer";

describe("bayerMatrix", () => {
	test("is a permutation of evenly spaced thresholds in [0, 1)", () => {
		for (const n of [2, 4, 8] as const) {
			const matrix = bayerMatrix(n);
			expect(matrix.length).toBe(n * n);
			const sorted = [...matrix].sort((a, b) => a - b);
			sorted.forEach((value, i) => {
				expect(value).toBeCloseTo((i + 0.5) / (n * n), 6);
			});
			expect(sorted[0]).toBeGreaterThan(0);
			expect(sorted.at(-1)).toBeLessThan(1);
		}
	});

	test("matches the classic 4×4 Bayer ranks", () => {
		expect([...bayerRanks(4)]).toEqual([
			0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5,
		]);
	});

	test("is cached", () => {
		expect(bayerMatrix(4)).toBe(bayerMatrix(4));
	});
});

describe("ditherOn", () => {
	test("tone 0 inks nothing and tone 1 inks everything", () => {
		for (const n of [2, 4, 8] as BayerSize[]) {
			for (let y = 0; y < n; y += 1) {
				for (let x = 0; x < n; x += 1) {
					expect(ditherOn(0, x, y, n)).toBe(false);
					expect(ditherOn(1, x, y, n)).toBe(true);
				}
			}
		}
	});

	test("a tone inks that share of the tile", () => {
		const count = (tone: number, n: BayerSize) => {
			let inked = 0;
			for (let y = 0; y < n; y += 1) {
				for (let x = 0; x < n; x += 1) if (ditherOn(tone, x, y, n)) inked += 1;
			}
			return inked;
		};
		expect(count(0.25, 4)).toBe(4);
		expect(count(0.5, 4)).toBe(8);
		expect(count(0.75, 8)).toBe(48);
	});

	test("tiles in both directions, negative coordinates included", () => {
		expect(ditherOn(0.4, 5, 6)).toBe(ditherOn(0.4, 1, 2));
		expect(ditherOn(0.4, -3, -2)).toBe(ditherOn(0.4, 1, 2));
	});

	test("is monotone: a denser tone never un-inks a cell", () => {
		for (let y = 0; y < 4; y += 1) {
			for (let x = 0; x < 4; x += 1) {
				let was = false;
				for (let t = 0; t <= 1; t += 0.05) {
					const on = ditherOn(t, x, y);
					if (was) expect(on).toBe(true);
					was = on;
				}
			}
		}
	});
});

describe("quantize", () => {
	test("snaps to the nearest step and clamps", () => {
		expect(quantize(0.4, 5)).toBe(0.5);
		expect(quantize(0.12, 5)).toBe(0);
		expect(quantize(-1, 4)).toBe(0);
		expect(quantize(2, 4)).toBe(1);
		expect(quantize(0.5, 2)).toBe(1);
	});
});

describe("inkedCells", () => {
	test("rounds to a cell count and clamps", () => {
		expect(inkedCells(0.5, 4)).toBe(8);
		expect(inkedCells(-1, 4)).toBe(0);
		expect(inkedCells(3, 2)).toBe(4);
	});
});
