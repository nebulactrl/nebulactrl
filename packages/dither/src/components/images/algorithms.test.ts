import { describe, expect, test } from "bun:test";
import {
	ditherRGBA,
	fitRect,
	luminance,
	nearestPaletteIndex,
	quantizeToPalette,
	type RasterOptions,
	rasterCacheKey,
	resolveRasterOptions,
	toneField,
} from "./algorithms";

const BLACK: [number, number, number] = [0, 0, 0];
const WHITE: [number, number, number] = [255, 255, 255];

/** A grey RGBA bitmap, opaque. */
function grey(width: number, height: number, value: number): Uint8ClampedArray {
	const data = new Uint8ClampedArray(width * height * 4);
	for (let p = 0; p < width * height; p += 1) {
		data.set([value, value, value, 255], p * 4);
	}
	return data;
}

/** The red channel of each pixel as 0 (black) or 1 (white). */
function bits(rgba: Uint8ClampedArray): number[] {
	const out: number[] = [];
	for (let i = 0; i < rgba.length; i += 4)
		out.push((rgba[i] ?? 0) > 127 ? 1 : 0);
	return out;
}

const MONO: Partial<RasterOptions> = {
	palette: [BLACK, WHITE],
	serpentine: false,
};

describe("error diffusion on a one-row fixture", () => {
	test("Floyd–Steinberg of mid grey alternates", () => {
		const out = ditherRGBA(grey(4, 1, 128), 4, 1, {
			...MONO,
			algorithm: "floyd-steinberg",
		});
		expect(bits(out)).toEqual([1, 0, 1, 0]);
	});

	test("Atkinson of mid grey leaves a gap after the first dot", () => {
		const out = ditherRGBA(grey(4, 1, 128), 4, 1, {
			...MONO,
			algorithm: "atkinson",
		});
		expect(bits(out)).toEqual([1, 0, 0, 1]);
	});

	test("is deterministic: the same input twice gives identical bytes", () => {
		const input = grey(9, 7, 90);
		const a = ditherRGBA(input, 9, 7, {
			...MONO,
			algorithm: "floyd-steinberg",
		});
		const b = ditherRGBA(input, 9, 7, {
			...MONO,
			algorithm: "floyd-steinberg",
		});
		expect(Array.from(a)).toEqual(Array.from(b));
	});

	test("does not modify its input", () => {
		const input = grey(4, 4, 100);
		const copy = Array.from(input);
		ditherRGBA(input, 4, 4, MONO);
		expect(Array.from(input)).toEqual(copy);
	});
});

describe("tone preservation", () => {
	for (const algorithm of ["floyd-steinberg", "atkinson", "bayer"] as const) {
		test(`${algorithm}: mean output tracks the input tone`, () => {
			const size = 32;
			const out = ditherRGBA(grey(size, size, 64), size, size, {
				...MONO,
				algorithm,
			});
			const inked = bits(out).reduce<number>((sum, bit) => sum + bit, 0);
			const share = inked / (size * size);
			// 64/255 ≈ 0.25. Atkinson drops a quarter of the error, so it drifts more.
			const slack = algorithm === "atkinson" ? 0.13 : 0.05;
			expect(Math.abs(share - 64 / 255)).toBeLessThan(slack);
		});
	}

	test("pure black and pure white stay pure under every algorithm", () => {
		for (const algorithm of [
			"floyd-steinberg",
			"atkinson",
			"bayer",
			"threshold",
		] as const) {
			const black = bits(
				ditherRGBA(grey(8, 8, 0), 8, 8, { ...MONO, algorithm }),
			);
			const white = bits(
				ditherRGBA(grey(8, 8, 255), 8, 8, { ...MONO, algorithm }),
			);
			expect(black.every((bit) => bit === 0)).toBe(true);
			expect(white.every((bit) => bit === 1)).toBe(true);
		}
	});
});

describe("ordered and threshold", () => {
	test("threshold splits at mid grey", () => {
		const dark = bits(
			ditherRGBA(grey(3, 3, 100), 3, 3, { ...MONO, algorithm: "threshold" }),
		);
		const light = bits(
			ditherRGBA(grey(3, 3, 200), 3, 3, { ...MONO, algorithm: "threshold" }),
		);
		expect(dark.every((bit) => bit === 0)).toBe(true);
		expect(light.every((bit) => bit === 1)).toBe(true);
	});

	test("bayer 4×4 at tone 0.5 inks exactly half the cells of a tile", () => {
		const out = ditherRGBA(grey(4, 4, 128), 4, 4, {
			...MONO,
			algorithm: "bayer",
			bayerSize: 4,
		});
		const inked = bits(out).reduce<number>((sum, bit) => sum + bit, 0);
		expect(inked).toBe(8);
	});

	test("bayer tiles: output repeats with the matrix period", () => {
		const out = bits(
			ditherRGBA(grey(8, 8, 90), 8, 8, {
				...MONO,
				algorithm: "bayer",
				bayerSize: 4,
			}),
		);
		for (let y = 0; y < 4; y += 1) {
			for (let x = 0; x < 4; x += 1) {
				expect(out[y * 8 + x]).toBe(out[y * 8 + x + 4] as number);
				expect(out[y * 8 + x]).toBe(out[(y + 4) * 8 + x] as number);
			}
		}
	});

	test("a three-colour ramp uses the middle colour for mid tones", () => {
		const ramp = [BLACK, [255, 0, 0] as [number, number, number], WHITE];
		const out = ditherRGBA(grey(2, 2, 128), 2, 2, {
			palette: ramp,
			algorithm: "threshold",
		});
		expect(Array.from(out.slice(0, 4))).toEqual([255, 0, 0, 255]);
	});
});

describe("palette quantise", () => {
	const palette: [number, number, number][] = [
		[10, 10, 10],
		[240, 30, 30],
		[30, 200, 60],
	];

	test("nearestPaletteIndex picks the closest colour", () => {
		expect(nearestPaletteIndex(0, 0, 0, palette)).toBe(0);
		expect(nearestPaletteIndex(250, 40, 20, palette)).toBe(1);
		expect(nearestPaletteIndex(20, 180, 90, palette)).toBe(2);
	});

	test("quantizeToPalette maps every pixel onto the palette and keeps alpha", () => {
		const rgba = new Uint8ClampedArray([
			5,
			5,
			5,
			255, //
			230,
			60,
			10,
			128,
			0,
			255,
			0,
			255,
		]);
		const out = quantizeToPalette(rgba, palette);
		expect(Array.from(out)).toEqual([
			10, 10, 10, 255, 240, 30, 30, 128, 30, 200, 60, 255,
		]);
	});

	test("colour mode diffusion only ever emits palette colours", () => {
		const size = 6;
		const rgba = new Uint8ClampedArray(size * size * 4);
		for (let p = 0; p < size * size; p += 1) {
			rgba.set([(p * 37) % 256, (p * 91) % 256, (p * 53) % 256, 255], p * 4);
		}
		const out = ditherRGBA(rgba, size, size, {
			palette,
			mode: "color",
			algorithm: "atkinson",
		});
		for (let i = 0; i < out.length; i += 4) {
			const hit = palette.some(
				(c) => c[0] === out[i] && c[1] === out[i + 1] && c[2] === out[i + 2],
			);
			expect(hit).toBe(true);
		}
	});
});

describe("transparency", () => {
	test("void pixels stay transparent and never take error", () => {
		const rgba = grey(3, 1, 128);
		rgba[7] = 0; // alpha of the middle pixel
		const out = ditherRGBA(rgba, 3, 1, {
			...MONO,
			algorithm: "floyd-steinberg",
		});
		expect(out[7]).toBe(0);
		expect(out[3]).toBe(255);
		expect(out[11]).toBe(255);
	});
});

describe("tone adjustments", () => {
	test("luminance weights sum to one", () => {
		expect(luminance(255, 255, 255)).toBeCloseTo(1, 6);
		expect(luminance(0, 0, 0)).toBe(0);
		expect(luminance(0, 255, 0)).toBeCloseTo(0.7152, 4);
	});

	test("invert flips tones; brightness shifts them; contrast spreads them", () => {
		const input = grey(1, 1, 64);
		const base = {
			contrast: 1,
			brightness: 0,
			invert: false,
			normalize: false,
		};
		const plain = toneField(input, base)[0] as number;
		expect(toneField(input, { ...base, invert: true })[0]).toBeCloseTo(
			1 - plain,
			5,
		);
		expect(toneField(input, { ...base, brightness: 0.2 })[0]).toBeCloseTo(
			plain + 0.2,
			5,
		);
		expect(
			toneField(input, { ...base, contrast: 2 })[0] as number,
		).toBeLessThan(plain);
	});

	test("normalize stretches a narrow band to the full range", () => {
		const rgba = new Uint8ClampedArray(100 * 4);
		for (let p = 0; p < 100; p += 1) {
			const v = 100 + p; // 100..199
			rgba.set([v, v, v, 255], p * 4);
		}
		const tone = toneField(rgba, {
			contrast: 1,
			brightness: 0,
			invert: false,
			normalize: true,
		});
		expect(tone[0] as number).toBeLessThan(0.05);
		expect(tone[99] as number).toBeGreaterThan(0.95);
	});
});

describe("geometry and cache key", () => {
	test("cover crops the wider axis", () => {
		const r = fitRect(200, 100, 100, 100, "cover");
		expect(r.sw).toBe(100);
		expect(r.sh).toBe(100);
		expect(r.sx).toBe(50);
		expect(r.sy).toBe(0);
		expect([r.dx, r.dy, r.dw, r.dh]).toEqual([0, 0, 100, 100]);
	});

	test("contain letterboxes the shorter axis", () => {
		const r = fitRect(200, 100, 100, 100, "contain");
		expect([r.dx, r.dy, r.dw, r.dh]).toEqual([0, 25, 100, 50]);
		expect([r.sx, r.sy, r.sw, r.sh]).toEqual([0, 0, 200, 100]);
	});

	test("cache key changes with src, grid and options but not with object identity", () => {
		const opts = resolveRasterOptions({ algorithm: "atkinson" });
		const key = rasterCacheKey("/a.jpg", 10, 10, "cover", opts);
		expect(
			rasterCacheKey(
				"/a.jpg",
				10,
				10,
				"cover",
				resolveRasterOptions({ algorithm: "atkinson" }),
			),
		).toBe(key);
		expect(rasterCacheKey("/b.jpg", 10, 10, "cover", opts)).not.toBe(key);
		expect(rasterCacheKey("/a.jpg", 11, 10, "cover", opts)).not.toBe(key);
		expect(
			rasterCacheKey("/a.jpg", 10, 10, "cover", {
				...opts,
				algorithm: "bayer",
			}),
		).not.toBe(key);
		expect(
			rasterCacheKey("/a.jpg", 10, 10, "cover", {
				...opts,
				palette: [WHITE, BLACK],
			}),
		).not.toBe(key);
	});
});
