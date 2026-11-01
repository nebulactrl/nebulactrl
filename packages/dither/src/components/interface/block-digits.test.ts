import { describe, expect, test } from "bun:test";
import {
	extrudeMask,
	faceTone,
	maskFromRgba,
	PRINT_LEVELS,
	PRINT_SECONDS,
	printGain,
	SWEEP_SECONDS,
	sideTone,
	sweepBoost,
} from "./block-digits";

describe("maskFromRgba", () => {
	test("a cell is covered at alpha 128 and up", () => {
		const rgba = [0, 0, 0, 255, 0, 0, 0, 127, 9, 9, 9, 128, 0, 0, 0, 0];
		expect([...maskFromRgba(rgba, 4, 1).data]).toEqual([1, 0, 1, 0]);
	});
});

describe("extrudeMask", () => {
	const one = { width: 2, height: 2, data: Uint8Array.from([1, 0, 0, 0]) };

	test("the side runs down and to the right, k cells behind the face", () => {
		const letters = extrudeMask(one, 2);
		expect(letters.width).toBe(4);
		expect(letters.height).toBe(4);
		expect(letters.face[0]).toBe(1);
		expect(letters.side[1 * 4 + 1]).toBe(1);
		expect(letters.side[2 * 4 + 2]).toBe(2);
		expect([...letters.side].filter((k) => k > 0)).toHaveLength(2);
		expect([...letters.face].filter((v) => v === 1)).toHaveLength(1);
	});

	test("depth 0 is flat and keeps the size", () => {
		const letters = extrudeMask(one, 0);
		expect(letters.width).toBe(2);
		expect([...letters.side].every((k) => k === 0)).toBe(true);
	});

	test("the side never covers the face", () => {
		const block = { width: 3, height: 3, data: new Uint8Array(9).fill(1) };
		const letters = extrudeMask(block, 2);
		for (let i = 0; i < letters.face.length; i++) {
			if (letters.face[i] === 1) expect(letters.side[i]).toBe(0);
		}
		expect(letters.faceTop).toBe(0);
		expect(letters.faceBottom).toBe(2);
	});

	test("an empty mask has no face rows", () => {
		const letters = extrudeMask(
			{ width: 2, height: 2, data: new Uint8Array(4) },
			1,
		);
		expect(letters.faceTop).toBe(0);
		expect(letters.faceBottom).toBe(0);
	});
});

describe("tones", () => {
	test("the face thins from the top to the baseline", () => {
		expect(faceTone(0, 0, 10)).toBeGreaterThan(faceTone(5, 0, 10));
		expect(faceTone(5, 0, 10)).toBeGreaterThan(faceTone(10, 0, 10));
		expect(faceTone(3, 3, 3)).toBeGreaterThan(0.9);
		expect(faceTone(-4, 0, 10)).toBe(faceTone(0, 0, 10));
	});

	test("the side is sparse and sparser further back", () => {
		expect(sideTone(1, 4)).toBeGreaterThan(sideTone(4, 4));
		expect(sideTone(1, 4)).toBeLessThan(faceTone(10, 0, 10) + 0.05);
		expect(sideTone(1, 1)).toBe(0.3);
	});
});

describe("printGain", () => {
	test("steps from 0 to 1 in whole levels", () => {
		expect(printGain(0)).toBe(0);
		expect(printGain(PRINT_SECONDS)).toBe(1);
		expect(printGain(PRINT_SECONDS * 5)).toBe(1);
		for (let t = 0; t <= PRINT_SECONDS; t += 0.05) {
			const level = printGain(t) * PRINT_LEVELS;
			expect(Math.abs(level - Math.round(level))).toBeLessThan(1e-9);
		}
	});
});

describe("sweepBoost", () => {
	test("a band of light crosses, then rests until the next sweep", () => {
		expect(sweepBoost(50, 0, 100, 0, -1)).toBe(0);
		const half = SWEEP_SECONDS / 2;
		expect(sweepBoost(50, 0, 100, 0, half)).toBeCloseTo(0.4);
		expect(sweepBoost(5, 0, 100, 0, half)).toBe(0);
		expect(sweepBoost(50, 0, 100, 0, SWEEP_SECONDS + 1)).toBe(0);
	});

	test("it repeats every period", () => {
		const half = SWEEP_SECONDS / 2;
		expect(sweepBoost(50, 0, 100, 0, half + 5)).toBeCloseTo(
			sweepBoost(50, 0, 100, 0, half),
		);
	});
});
