import { describe, expect, test } from "bun:test";
import {
	DIFFUSION_METHODS,
	DIFFUSION_TAPS,
	diffuseTones,
	isDiffusionMethod,
} from "./diffuse";

/** Mean level of the cells as a share of the top level: what a diffusion should keep of the input tone. */
function inkShare(indices: ArrayLike<number>, levels = 2): number {
	const top = levels - 1;
	let sum = 0;
	for (let i = 0; i < indices.length; i += 1) sum += (indices[i] ?? 0) / top;
	return indices.length === 0 ? 0 : sum / indices.length;
}

const flat = (tone: number, size = 48) =>
	Float32Array.from({ length: size * size }, () => tone);

describe("kernels", () => {
	test("Floyd–Steinberg and Sierra Lite pass on all the error; Atkinson 3/4", () => {
		const total = (m: keyof typeof DIFFUSION_TAPS) =>
			DIFFUSION_TAPS[m].reduce((sum, tap) => sum + tap.weight, 0);
		expect(total("floyd-steinberg")).toBeCloseTo(1, 10);
		expect(total("sierra-lite")).toBeCloseTo(1, 10);
		expect(total("atkinson")).toBeCloseTo(0.75, 10);
	});

	test("taps only reach cells that are not visited yet", () => {
		for (const method of DIFFUSION_METHODS) {
			for (const tap of DIFFUSION_TAPS[method]) {
				expect(tap.dy > 0 || (tap.dy === 0 && tap.dx > 0)).toBe(true);
			}
		}
	});

	test("isDiffusionMethod", () => {
		expect(isDiffusionMethod("atkinson")).toBe(true);
		expect(isDiffusionMethod("bayer4")).toBe(false);
	});
});

describe("diffuseTones", () => {
	test("pure paper and pure ink survive", () => {
		expect(diffuseTones(flat(0), 48, 48).every((v) => v === 0)).toBe(true);
		expect(diffuseTones(flat(1), 48, 48).every((v) => v === 1)).toBe(true);
	});

	test("Floyd–Steinberg keeps the mean tone", () => {
		for (const tone of [0.1, 0.25, 0.5, 0.8]) {
			const out = diffuseTones(flat(tone), 48, 48);
			expect(Math.abs(inkShare(out) - tone)).toBeLessThan(0.02);
		}
	});

	test("Sierra Lite keeps the mean tone too", () => {
		const out = diffuseTones(flat(0.3), 48, 48, { method: "sierra-lite" });
		expect(Math.abs(inkShare(out) - 0.3)).toBeLessThan(0.03);
	});

	test("Atkinson drops a quarter of the error, so it clips extremes", () => {
		const dark = inkShare(
			diffuseTones(flat(0.05), 48, 48, { method: "atkinson" }),
		);
		expect(dark).toBeLessThan(0.05);
	});

	test("multi-level output stays in range and keeps the mean", () => {
		const out = diffuseTones(flat(0.4), 48, 48, { levels: 4 });
		expect(Math.max(...out)).toBeLessThanOrEqual(3);
		expect(Math.abs(inkShare(out, 4) - 0.4)).toBeLessThan(0.02);
	});

	test("is deterministic and leaves the input alone", () => {
		const input = flat(0.37, 16);
		const copy = Float32Array.from(input);
		const a = diffuseTones(input, 16, 16);
		const b = diffuseTones(input, 16, 16);
		expect(Array.from(a)).toEqual(Array.from(b));
		expect(Array.from(input)).toEqual(Array.from(copy));
	});

	test("serpentine and raster scans differ but both are valid", () => {
		// 0.5 is a degenerate input: Floyd–Steinberg settles into the same
		// checkerboard in either direction, so the scans only differ off-centre.
		const a = diffuseTones(flat(0.3, 16), 16, 16, { serpentine: true });
		const b = diffuseTones(flat(0.3, 16), 16, 16, { serpentine: false });
		expect(Array.from(a)).not.toEqual(Array.from(b));
		expect(a.every((v) => v === 0 || v === 1)).toBe(true);
	});

	test("tones outside 0..1 are clamped", () => {
		const out = diffuseTones([-3, 4, 0.5, 2], 2, 2);
		expect(out[0]).toBe(0);
		expect(out[1]).toBe(1);
	});
});
