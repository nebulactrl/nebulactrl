import { describe, expect, test } from "bun:test";
import {
	decodeLandMask,
	getWorldLand,
	isLand,
	isLandAt,
	LAND_COLUMNS,
	LAND_ROWS,
	landCoverage,
} from "./land-mask";
import { WORLD_LAND_RLE } from "./world-land";

/** Encodes a tiny bitmap with the same varint RLE format as the generator. */
function encode(width: number, height: number, bits: number[]): string {
	const bytes: number[] = [];
	const varint = (n: number) => {
		let v = n;
		while (v >= 0x80) {
			bytes.push((v & 0x7f) | 0x80);
			v >>>= 7;
		}
		bytes.push(v);
	};
	varint(width);
	varint(height);
	let cur = 0;
	let run = 0;
	for (const b of bits) {
		if (b === cur) run++;
		else {
			varint(run);
			cur = b;
			run = 1;
		}
	}
	varint(run);
	return Buffer.from(bytes).toString("base64");
}

describe("decodeLandMask", () => {
	test("round-trips a small bitmap", () => {
		const bits = [0, 0, 1, 1, 1, 0, 1, 0];
		const mask = decodeLandMask(encode(4, 2, bits));
		expect(mask.width).toBe(4);
		expect(mask.height).toBe(2);
		expect(Array.from(mask.data)).toEqual(bits.map((b) => (b ? 255 : 0)));
	});

	test("handles a bitmap that starts with land and long runs", () => {
		const bits = [1, ...new Array<number>(300).fill(0), 1];
		const mask = decodeLandMask(encode(302, 1, bits));
		expect(mask.data[0]).toBe(255);
		expect(mask.data[150]).toBe(0);
		expect(mask.data[301]).toBe(255);
	});

	test("rejects truncated data", () => {
		const good = encode(4, 2, [0, 0, 1, 1, 1, 0, 1, 0]);
		const bytes = Buffer.from(good, "base64").subarray(0, 4);
		expect(() => decodeLandMask(bytes.toString("base64"))).toThrow();
	});
});

describe("bundled world mask", () => {
	const mask = getWorldLand();

	// 0.25° cells, so a zoomed-in region (the analytics map's expanded view)
	// still has coastlines; the mask ships with the app only, not the site.
	test("is small enough to bundle", () => {
		expect(WORLD_LAND_RLE.length).toBeLessThan(18 * 1024);
	});

	test("has the generated dimensions", () => {
		expect(mask.data.length).toBe(mask.width * mask.height);
		expect(mask.width).toBe(mask.height * 2);
	});

	test("marks known land as land", () => {
		expect(isLand(mask, 48.9, 2.3)).toBe(true); // Paris
		expect(isLand(mask, 35.7, 139.7)).toBe(true); // Tokyo
		expect(isLand(mask, 40.7, -100)).toBe(true); // US midwest
		expect(isLand(mask, 23, 12)).toBe(true); // Sahara
		expect(isLand(mask, -80, 0)).toBe(true); // Antarctica
	});

	test("marks open ocean as sea", () => {
		expect(isLand(mask, 0, -140)).toBe(false); // Pacific
		expect(isLand(mask, 30, -40)).toBe(false); // Atlantic
		expect(isLand(mask, -40, 80)).toBe(false); // Indian ocean
	});

	test("wraps longitude", () => {
		expect(isLand(mask, 48.9, 2.3 + 360)).toBe(isLand(mask, 48.9, 2.3));
		expect(isLand(mask, 0, -140 - 360)).toBe(false);
	});

	test("land share is plausible", () => {
		let land = 0;
		for (const v of mask.data) if (v === 255) land++;
		const share = land / mask.data.length;
		expect(share).toBeGreaterThan(0.25);
		expect(share).toBeLessThan(0.45);
	});
});

describe("world-land lookups", () => {
	test("exposes the mask size", () => {
		const mask = getWorldLand();
		expect(LAND_COLUMNS).toBe(mask.width);
		expect(LAND_ROWS).toBe(mask.height);
	});

	test("isLandAt reads the bundled mask", () => {
		expect(isLandAt(48.9, 2.3)).toBe(true);
		expect(isLandAt(0, -140)).toBe(false);
	});

	test("landCoverage is 1 deep inland, 0 at sea and stays in 0..1", () => {
		expect(landCoverage(23, 12)).toBe(1);
		expect(landCoverage(0, -140)).toBe(0);
		for (const [lat, lon] of [
			[51.5, -0.13],
			[35.7, 139.7],
			[-33.9, 151.2],
			[90, 180],
			[-90, -180],
		] as const) {
			const c = landCoverage(lat, lon);
			expect(c).toBeGreaterThanOrEqual(0);
			expect(c).toBeLessThanOrEqual(1);
		}
	});

	test("landCoverage blends across a tiny mask and wraps longitude", () => {
		const tiny = decodeLandMask(encode(2, 2, [1, 0, 1, 0]));
		expect(landCoverage(45, -90, tiny)).toBeCloseTo(1, 9);
		expect(landCoverage(45, 0, tiny)).toBeCloseTo(0.5, 9);
		expect(landCoverage(45, -90 + 360, tiny)).toBeCloseTo(1, 9);
	});
});
