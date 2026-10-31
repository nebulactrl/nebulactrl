import { describe, expect, test } from "bun:test";
import {
	DITHER_METHODS,
	ditherImage,
	ditherRGBA,
	matrixOf,
	methodToAlgorithm,
	rasterCacheKey,
	resolveRasterOptions,
	toneField,
} from "./algorithms";

function grey(width: number, height: number, value: number) {
	const data = new Uint8ClampedArray(width * height * 4);
	for (let p = 0; p < width * height; p += 1) {
		data.set([value, value, value, 255], p * 4);
	}
	return { data, width, height };
}

const reds = (data: Uint8ClampedArray) => {
	const out: number[] = [];
	for (let i = 0; i < data.length; i += 4) out.push(data[i] ?? 0);
	return out;
};

const mean = (values: number[]) =>
	values.reduce((a, b) => a + b, 0) / values.length;

describe("ditherImage", () => {
	test("every method returns a new bitmap of the same size and leaves the input alone", () => {
		const input = grey(24, 24, 140);
		const before = Array.from(input.data);
		for (const method of DITHER_METHODS) {
			const out = ditherImage(input, { method });
			expect(out.width).toBe(24);
			expect(out.height).toBe(24);
			expect(out.data).not.toBe(input.data);
			expect(Array.from(input.data)).toEqual(before);
		}
	});

	test("only palette colours come out, and alpha is kept", () => {
		const input = grey(16, 16, 100);
		input.data[3] = 77;
		for (const method of DITHER_METHODS) {
			const out = ditherImage(input, {
				method,
				palette: [
					[0, 0, 0],
					[1, 0, 0],
					[1, 1, 1],
				],
			});
			for (let i = 0; i < out.data.length; i += 4) {
				const key = `${out.data[i]},${out.data[i + 1]},${out.data[i + 2]}`;
				expect(["0,0,0", "255,0,0", "255,255,255"]).toContain(key);
			}
			expect(out.data[3]).toBe(77);
			expect(out.data[7]).toBe(255);
		}
	});

	test("mean output tracks the input tone for every method", () => {
		for (const method of DITHER_METHODS) {
			const out = ditherImage(grey(64, 64, 128), { method });
			expect(mean(reds(out.data)) / 255).toBeCloseTo(128 / 255, 1);
		}
	});

	test("black and white inputs stay pure", () => {
		for (const method of DITHER_METHODS) {
			expect(
				new Set(reds(ditherImage(grey(8, 8, 0), { method }).data)),
			).toEqual(new Set([0]));
			expect(
				new Set(reds(ditherImage(grey(8, 8, 255), { method }).data)),
			).toEqual(new Set([255]));
		}
	});

	test("blue noise has no Bayer period", () => {
		const out = reds(
			ditherImage(grey(32, 32, 128), { method: "blue-noise" }).data,
		);
		const bayer = reds(
			ditherImage(grey(32, 32, 128), { method: "bayer4" }).data,
		);
		let repeats = true;
		for (let y = 0; y < 32 && repeats; y += 1)
			for (let x = 4; x < 32 && repeats; x += 1)
				if (out[y * 32 + x] !== out[y * 32 + x - 4]) repeats = false;
		expect(repeats).toBe(false);
		let bayerRepeats = true;
		for (let y = 0; y < 32; y += 1)
			for (let x = 4; x < 32; x += 1)
				if (bayer[y * 32 + x] !== bayer[y * 32 + x - 4]) bayerRepeats = false;
		expect(bayerRepeats).toBe(true);
	});

	test("a lower threshold pushes more pixels to the light colour", () => {
		const low = mean(
			reds(
				ditherImage(grey(32, 32, 128), { method: "bayer8", threshold: 0.3 })
					.data,
			),
		);
		const high = mean(
			reds(
				ditherImage(grey(32, 32, 128), { method: "bayer8", threshold: 0.7 })
					.data,
			),
		);
		expect(low).toBeGreaterThan(high);
	});

	test("is deterministic", () => {
		const a = ditherImage(grey(20, 20, 90), { method: "sierra-lite" });
		const b = ditherImage(grey(20, 20, 90), { method: "sierra-lite" });
		expect(Array.from(a.data)).toEqual(Array.from(b.data));
	});
});

describe("methods", () => {
	test("bayer sizes map to the bayer algorithm", () => {
		expect(methodToAlgorithm("bayer2")).toEqual({
			algorithm: "bayer",
			bayerSize: 2,
		});
		expect(methodToAlgorithm("bayer8")).toEqual({
			algorithm: "bayer",
			bayerSize: 8,
		});
		expect(methodToAlgorithm("sierra-lite").algorithm).toBe("sierra-lite");
		expect(methodToAlgorithm("blue-noise").algorithm).toBe("blue-noise");
	});

	test("matrixOf names the ordered matrix, and diffusion has none", () => {
		expect(matrixOf("bayer", 8)).toBe("bayer8");
		expect(matrixOf("blue-noise", 4)).toBe("blue-noise");
		expect(matrixOf("atkinson", 4)).toBeNull();
		expect(matrixOf("threshold", 4)).toBeNull();
	});
});

describe("component pipeline", () => {
	const palette: [number, number, number][] = [
		[0, 0, 0],
		[255, 255, 255],
	];

	test("sierra-lite and blue-noise work through ditherRGBA", () => {
		const input = grey(32, 32, 128);
		for (const algorithm of ["sierra-lite", "blue-noise"] as const) {
			const out = ditherRGBA(input.data, 32, 32, { algorithm, palette });
			const share = mean(reds(out)) / 255;
			expect(share).toBeGreaterThan(0.4);
			expect(share).toBeLessThan(0.6);
		}
	});

	test("a transparent palette entry gives transparent pixels", () => {
		const input = grey(16, 16, 0);
		const out = ditherRGBA(input.data, 16, 16, {
			algorithm: "threshold",
			palette,
			paletteAlpha: [0, 1],
		});
		expect(out[3]).toBe(0);
		const light = ditherRGBA(grey(16, 16, 255).data, 16, 16, {
			algorithm: "threshold",
			palette,
			paletteAlpha: [0, 1],
		});
		expect(light[3]).toBe(255);
	});

	test("sharpen pushes a pixel away from its neighbourhood", () => {
		const input = grey(5, 5, 100);
		input.data.set([200, 200, 200, 255], (2 * 5 + 2) * 4);
		const opts = {
			contrast: 1,
			brightness: 0,
			invert: false,
			normalize: false,
		};
		const flat = toneField(input.data, opts, { width: 5, height: 5 });
		const sharp = toneField(
			input.data,
			{ ...opts, sharpen: 1 },
			{ width: 5, height: 5 },
		);
		expect(sharp[12] as number).toBeGreaterThan(flat[12] as number);
		expect(sharp[0] as number).toBeCloseTo(flat[0] as number, 5);
		// no size, no sharpening
		expect(toneField(input.data, { ...opts, sharpen: 1 })[12]).toBe(flat[12]);
	});

	test("the cache key sees sharpen and palette alpha", () => {
		const base = resolveRasterOptions({});
		const key = rasterCacheKey("/a", 4, 4, "cover", base);
		expect(
			rasterCacheKey("/a", 4, 4, "cover", { ...base, sharpen: 0.3 }),
		).not.toBe(key);
		expect(
			rasterCacheKey("/a", 4, 4, "cover", { ...base, paletteAlpha: [0, 1] }),
		).not.toBe(key);
	});
});
