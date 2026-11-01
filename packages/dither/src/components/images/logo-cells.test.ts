import { describe, expect, test } from "bun:test";
import {
	countInk,
	insideMask,
	isEdge,
	logoInk,
	rimMask,
	rowSpan,
	scanLit,
	sweepAt,
} from "./logo-cells";

/** An RGBA bitmap with a filled `iw × ih` rectangle at (ox, oy) in a `w × h` grid. */
function rectangle(
	w: number,
	h: number,
	ox: number,
	oy: number,
	iw: number,
	ih: number,
): Uint8ClampedArray {
	const data = new Uint8ClampedArray(w * h * 4);
	for (let y = oy; y < oy + ih; y += 1)
		for (let x = ox; x < ox + iw; x += 1) data[(y * w + x) * 4 + 3] = 255;
	return data;
}

describe("logo cells", () => {
	const w = 12;
	const h = 10;
	const inside = insideMask(rectangle(w, h, 2, 1, 8, 8));

	test("insideMask cuts at half coverage", () => {
		const data = new Uint8ClampedArray([0, 0, 0, 127, 0, 0, 0, 128]);
		expect(Array.from(insideMask(data))).toEqual([0, 1]);
	});

	test("rowSpan and isEdge find the silhouette", () => {
		expect(rowSpan(inside, w, h)).toEqual([1, 8]);
		expect(rowSpan(new Uint8Array(w * h), w, h)).toBeNull();
		expect(isEdge(inside, w, h, 2, 1)).toBe(true);
		expect(isEdge(inside, w, h, 5, 4)).toBe(false);
		expect(isEdge(inside, w, h, 5, 1)).toBe(true);
	});

	test("never inks outside the logo; always inks the outline", () => {
		const ink = logoInk(inside, w, h, { fill: "dotted", outline: true });
		for (let p = 0; p < w * h; p += 1) {
			if (!inside[p]) expect(ink[p]).toBe(0);
		}
		for (let x = 2; x < 10; x += 1) {
			expect(ink[1 * w + x]).toBe(1);
			expect(ink[8 * w + x]).toBe(1);
		}
		for (let y = 1; y < 9; y += 1) {
			expect(ink[y * w + 2]).toBe(1);
			expect(ink[y * w + 9]).toBe(1);
		}
	});

	test("solid fills every inside cell", () => {
		const ink = logoInk(inside, w, h, { fill: "solid" });
		expect(countInk(ink)).toBe(8 * 8);
	});

	test("without an outline the interior follows the fill alone", () => {
		const withOutline = logoInk(inside, w, h, {
			fill: "dotted",
			outline: true,
		});
		const bare = logoInk(inside, w, h, { fill: "dotted", outline: false });
		expect(countInk(withOutline)).toBeGreaterThan(countInk(bare));
	});

	test("a gradient is denser at the top than the bottom of the interior", () => {
		const ink = logoInk(inside, w, h, {
			fill: "gradient",
			floor: 0.1,
			outline: false,
		});
		const rows = (y0: number, y1: number) => {
			let n = 0;
			for (let y = y0; y < y1; y += 1)
				for (let x = 2; x < 10; x += 1) n += ink[y * w + x] ?? 0;
			return n;
		};
		expect(rows(1, 4)).toBeGreaterThan(rows(6, 9));
	});

	test("hatched and dotted are fixed textures", () => {
		const dots = logoInk(inside, w, h, { fill: "dotted", outline: false });
		const hatch = logoInk(inside, w, h, { fill: "hatched", outline: false });
		expect(countInk(dots)).toBe(16);
		expect(countInk(hatch)).toBe(21);
	});

	test("output is deterministic and effects only add ink", () => {
		const base = logoInk(inside, w, h, { fill: "gradient", phase: 0.4 });
		expect(
			Array.from(logoInk(inside, w, h, { fill: "gradient", phase: 0.4 })),
		).toEqual(Array.from(base));
		const shimmer = logoInk(inside, w, h, {
			fill: "gradient",
			phase: 0.4,
			effect: "shimmer",
		});
		const scan = logoInk(inside, w, h, {
			fill: "gradient",
			phase: 0.4,
			effect: "scan",
		});
		expect(countInk(shimmer)).toBeGreaterThanOrEqual(countInk(base));
		expect(countInk(scan)).toBeGreaterThanOrEqual(countInk(base));
		for (let p = 0; p < w * h; p += 1) {
			if (base[p]) {
				expect(shimmer[p]).toBe(1);
				expect(scan[p]).toBe(1);
			}
		}
	});

	test("the scan line is a row pair that moves down with phase", () => {
		expect(scanLit(1, 1, 7, 0.5)).toBe(false);
		const row = 1 + 0.5 * (7 + 6) - 3; // 4.5
		expect(scanLit(Math.floor(row), 1, 7, 0.5)).toBe(true);
	});
});

describe("logo source, seams and matrices", () => {
	test("source dark takes dark cells on an opaque light ground", () => {
		// white ground, one black pixel, one mid grey pixel, one transparent black pixel
		const data = new Uint8ClampedArray([
			255, 255, 255, 255, 0, 0, 0, 255, 100, 100, 100, 255, 0, 0, 0, 0,
		]);
		expect(Array.from(insideMask(data, 128, "dark"))).toEqual([0, 1, 1, 0]);
		expect(Array.from(insideMask(data, 128, "alpha"))).toEqual([1, 1, 1, 0]);
	});

	test("the rim adds the seam between two colours of one shape", () => {
		const w = 6;
		const h = 5;
		const data = new Uint8ClampedArray(w * h * 4);
		for (let y = 0; y < h; y += 1)
			for (let x = 0; x < w; x += 1)
				data.set(x < 3 ? [255, 0, 0, 255] : [0, 0, 255, 255], (y * w + x) * 4);
		const inside = insideMask(data);
		const plain = rimMask(inside, data, w, h, false);
		const seamed = rimMask(inside, data, w, h, true);
		// an interior cell of the left part, beside the seam
		expect(plain[2 * w + 2]).toBe(0);
		expect(seamed[2 * w + 2]).toBe(1);
		// far from both: still inside, not rim
		expect(seamed[2 * w + 4]).toBe(0);
		const ink = logoInk(
			inside,
			w,
			h,
			{ fill: "dotted", outline: true },
			seamed,
		);
		expect(ink[2 * w + 2]).toBe(1);
	});

	test("every matrix name draws, and blue noise differs from bayer", () => {
		const w = 24;
		const h = 24;
		const full = new Uint8Array(w * h).fill(1);
		const base = { fill: "gradient" as const, floor: 0.5, outline: false };
		const counts = (["bayer2", "bayer4", "bayer8", "blue-noise"] as const).map(
			(n) => countInk(logoInk(full, w, h, { ...base, n })),
		);
		for (const count of counts) {
			expect(count).toBeGreaterThan(w * h * 0.4);
			expect(count).toBeLessThan(w * h * 0.9);
		}
		const bayer = logoInk(full, w, h, { ...base, n: 4 });
		expect(Array.from(logoInk(full, w, h, { ...base, n: "bayer4" }))).toEqual(
			Array.from(bayer),
		);
		expect(
			Array.from(logoInk(full, w, h, { ...base, n: "blue-noise" })),
		).not.toEqual(Array.from(bayer));
	});
});

describe("sweep", () => {
	test("loops once per period and starts at the phase", () => {
		expect(sweepAt(0, 6, 0)).toBe(0);
		expect(sweepAt(3, 6, 0)).toBeCloseTo(0.5, 6);
		expect(sweepAt(6, 6, 0)).toBeCloseTo(0, 6);
		expect(sweepAt(0, 6, 0.25)).toBeCloseTo(0.25, 6);
		expect(sweepAt(3, 6, 0.75)).toBeCloseTo(0.25, 6);
	});

	test("the reduced-motion still frame sits at the start of the sweep for any phase", () => {
		for (const phase of [0, 0.2, 0.5, 0.9]) {
			const at = sweepAt(-phase * 6, 6, phase);
			expect(Math.min(at, 1 - at)).toBeLessThan(1e-9);
		}
	});

	test("a time before zero still lands in 0..1", () => {
		const at = sweepAt(-7, 6, 0);
		expect(at).toBeGreaterThanOrEqual(0);
		expect(at).toBeLessThan(1);
	});
});
