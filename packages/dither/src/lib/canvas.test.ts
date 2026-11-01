import { describe, expect, test } from "bun:test";
import { bayerMatrix } from "./bayer";
import { rasterizeField, sampleTones } from "./canvas";

const INK: [number, number, number] = [255, 255, 255];
const PAPER: [number, number, number] = [0, 0, 0];

const inked = (r: { data: Uint8ClampedArray }) => {
	let n = 0;
	for (let i = 0; i < r.data.length; i += 4) if ((r.data[i] ?? 0) > 127) n += 1;
	return n;
};

describe("sampleTones", () => {
	test("u runs left to right and v bottom to top, row 0 on top", () => {
		const tones = sampleTones((u) => u, 4, 2);
		expect(tones[0]).toBeCloseTo(0.125, 6);
		expect(tones[3]).toBeCloseTo(0.875, 6);
		const vertical = sampleTones((_u, v) => v, 1, 4);
		expect(vertical[0]).toBeGreaterThan(vertical[3] ?? 1);
	});

	test("clamps and drops non-finite values", () => {
		const tones = sampleTones((u) => (u < 0.5 ? Number.NaN : 7), 2, 1);
		expect(Array.from(tones)).toEqual([0, 1]);
	});
});

describe("rasterizeField (ordered)", () => {
	test("matches the Bayer matrix cell for cell", () => {
		const n = 4;
		const m = bayerMatrix(n);
		// levels high enough that no tone snapping interferes with 0.5
		const r = rasterizeField(() => 0.5, {
			cols: 8,
			rows: 8,
			levels: 17,
			palette: [PAPER, INK],
			method: "bayer4",
		});
		for (let y = 0; y < 8; y += 1) {
			for (let x = 0; x < 8; x += 1) {
				const on = (r.data[(y * 8 + x) * 4] ?? 0) > 127;
				expect(on).toBe(0.5 > (m[(y % n) * n + (x % n)] ?? 0));
			}
		}
	});

	test("a tone of 0.25 inks a quarter of a bayer4 tile", () => {
		const r = rasterizeField(() => 0.25, {
			cols: 16,
			rows: 16,
			levels: 17,
			palette: [PAPER, INK],
		});
		expect(inked(r)).toBe(16 * 16 * 0.25);
	});

	test("denser tone inks a superset of a sparser tone", () => {
		const a = rasterizeField(() => 0.25, {
			cols: 8,
			rows: 8,
			levels: 17,
			palette: [PAPER, INK],
		});
		const b = rasterizeField(() => 0.5, {
			cols: 8,
			rows: 8,
			levels: 17,
			palette: [PAPER, INK],
		});
		for (let i = 0; i < a.data.length; i += 4) {
			if ((a.data[i] ?? 0) > 127) expect((b.data[i] ?? 0) > 127).toBe(true);
		}
	});

	test("blue noise inks the same share as Bayer", () => {
		const r = rasterizeField(() => 0.5, {
			cols: 64,
			rows: 64,
			levels: 257,
			palette: [PAPER, INK],
			method: "blue-noise",
		});
		expect(Math.abs(inked(r) / (64 * 64) - 0.5)).toBeLessThan(0.01);
	});

	test("palette ramps pick the neighbouring colours", () => {
		const r = rasterizeField(() => 1, {
			cols: 2,
			rows: 2,
			palette: [PAPER, [10, 20, 30], [200, 100, 50]],
		});
		expect(Array.from(r.data.slice(0, 4))).toEqual([200, 100, 50, 255]);
		const zero = rasterizeField(() => 0, {
			cols: 1,
			rows: 1,
			palette: [PAPER, INK],
		});
		expect(Array.from(zero.data)).toEqual([0, 0, 0, 255]);
	});

	test("sub-pixels per cell scale the output; dots leave the corners as paper", () => {
		const r = rasterizeField(() => 1, {
			cols: 2,
			rows: 2,
			sub: 6,
			shape: "dot",
			palette: [PAPER, INK],
		});
		expect(r.width).toBe(12);
		expect(r.data[0]).toBe(0); // corner of a cell: outside the circle
		expect(r.data[(3 * 12 + 3) * 4]).toBe(255); // centre: ink
	});
});

describe("rasterizeField (diffusion)", () => {
	test("Floyd–Steinberg keeps the mean tone", () => {
		const r = rasterizeField(() => 0.3, {
			cols: 40,
			rows: 40,
			method: "floyd-steinberg",
			levels: 2,
			palette: [PAPER, INK],
		});
		expect(Math.abs(inked(r) / 1600 - 0.3)).toBeLessThan(0.02);
	});
});
