import { describe, expect, test } from "bun:test";
import {
	BLUE_NOISE_PATTERN_SIDE,
	BLUE_NOISE_SIDE,
	blueNoiseBytes,
	blueNoiseRanks,
	blueNoiseThreshold,
	blueNoiseTile,
} from "./blue-noise";

const toroidal = (a: number, b: number, side: number) => {
	const d = Math.abs(a - b);
	return Math.min(d, side - d);
};

/** Smallest toroidal distance between any two of the first `count` ranked cells. */
function minSpacing(ranks: Uint16Array, side: number, count: number): number {
	const pts: [number, number][] = [];
	for (let i = 0; i < ranks.length; i += 1) {
		if ((ranks[i] ?? 0) < count) pts.push([i % side, Math.floor(i / side)]);
	}
	let min = Number.POSITIVE_INFINITY;
	for (let i = 0; i < pts.length; i += 1) {
		for (let j = i + 1; j < pts.length; j += 1) {
			const a = pts[i];
			const b = pts[j];
			if (!a || !b) continue;
			min = Math.min(
				min,
				Math.hypot(toroidal(a[0], b[0], side), toroidal(a[1], b[1], side)),
			);
		}
	}
	return min;
}

describe("blueNoiseRanks", () => {
	test("is a permutation of 0..side²-1", () => {
		const side = BLUE_NOISE_PATTERN_SIDE;
		const ranks = blueNoiseRanks(side);
		expect(ranks.length).toBe(side * side);
		const seen = new Set(ranks);
		expect(seen.size).toBe(side * side);
		expect(Math.min(...ranks)).toBe(0);
		expect(Math.max(...ranks)).toBe(side * side - 1);
	});

	test("is deterministic and cached", () => {
		expect(blueNoiseRanks(16)).toBe(blueNoiseRanks(16));
		expect(Array.from(blueNoiseRanks(8))).toEqual(
			Array.from(blueNoiseRanks(8)),
		);
	});

	test("spreads the first cells evenly", () => {
		// 16 cells on a 16×16 torus would sit 4 apart on a perfect lattice.
		expect(minSpacing(blueNoiseRanks(16), 16, 16)).toBeGreaterThanOrEqual(2.5);
	});

	test("rejects tiles that are too small", () => {
		expect(() => blueNoiseRanks(4)).toThrow(RangeError);
	});
});

describe("64×64 tile", () => {
	test("tile, thresholds and bytes agree", () => {
		const tile = blueNoiseTile();
		expect(tile.length).toBe(BLUE_NOISE_SIDE * BLUE_NOISE_SIDE);
		const bytes = blueNoiseBytes();
		expect(bytes.length).toBe(tile.length);
		const lowest = tile.indexOf(0);
		const highest = tile.indexOf(tile.length - 1);
		expect(bytes[lowest]).toBe(0);
		expect(bytes[highest]).toBe(255);
	});

	test("thresholds tile in both directions and stay below 1", () => {
		expect(blueNoiseThreshold(3, 5)).toBe(
			blueNoiseThreshold(3 + BLUE_NOISE_SIDE, 5 - BLUE_NOISE_SIDE),
		);
		for (let i = 0; i < 64; i += 1) {
			const t = blueNoiseThreshold(i * 7, i * 3);
			expect(t).toBeGreaterThan(0);
			expect(t).toBeLessThan(1);
		}
	});

	test("a tone of 0.25 inks a quarter of the tile", () => {
		let inked = 0;
		for (let y = 0; y < BLUE_NOISE_SIDE; y += 1) {
			for (let x = 0; x < BLUE_NOISE_SIDE; x += 1) {
				if (0.25 > blueNoiseThreshold(x, y)) inked += 1;
			}
		}
		expect(inked).toBe(BLUE_NOISE_SIDE * BLUE_NOISE_SIDE * 0.25);
	});
});
