import { describe, expect, test } from "bun:test";
import { bayerMatrix } from "./bayer";
import {
	drawDitherCanvas,
	rasterizeField,
	resolveRasterPalette,
	sampleTones,
} from "./canvas";
import { DITHER_FRAGMENT } from "./gl/shader-source";
import { thresholdSampler } from "./threshold";

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

describe("rasterizeField (ordered, few levels)", () => {
	const grey = (tone: number, method: "bayer4" | "blue-noise", levels = 2) =>
		rasterizeField(() => tone, {
			cols: 64,
			rows: 64,
			levels,
			palette: [PAPER, INK],
			method,
		});
	const reds = (r: { data: Uint8ClampedArray }) =>
		Array.from({ length: r.data.length / 4 }, (_, i) => r.data[i * 4]);

	test("at two levels the matrix still shapes the output: bayer4 and blue noise differ", () => {
		const bayer = reds(grey(0.5, "bayer4"));
		const blue = reds(grey(0.5, "blue-noise"));
		expect(bayer).not.toEqual(blue);
	});

	test("at two levels a mid-grey field mixes both colours in the tone's share", () => {
		for (const method of ["bayer4", "blue-noise"] as const) {
			const r = grey(0.5, method);
			expect(new Set(reds(r))).toEqual(new Set([0, 255]));
			expect(Math.abs(inked(r) / (64 * 64) - 0.5)).toBeLessThan(0.02);
			const quarter = grey(0.25, method);
			expect(Math.abs(inked(quarter) / (64 * 64) - 0.25)).toBeLessThan(0.02);
		}
	});

	test("the extremes stay solid at any level count", () => {
		for (const method of ["bayer4", "blue-noise"] as const) {
			for (const levels of [2, 3, 6]) {
				expect(inked(grey(0, method, levels))).toBe(0);
				expect(inked(grey(1, method, levels))).toBe(64 * 64);
			}
		}
	});

	/** The GLSL quantise block of `DITHER_FRAGMENT`, run as JavaScript. */
	const shaderQuantise = () => {
		const block =
			/\/\/ quantise-begin\n([\s\S]*?)\/\/ quantise-end/.exec(DITHER_FRAGMENT)?.[1];
		expect(block).toBeDefined();
		const body = (block ?? "")
			.replace(/^\s*\/\/.*$/gm, "")
			.replace(/\b(float|bool)\s+/g, "let ")
			.replace(/s\.a/g, "a");
		return new Function(
			"floor",
			"min",
			"clamp",
			"a",
			"steps",
			"thr",
			`${body}\nreturn { q, v };`,
		) as (
			floor: (n: number) => number,
			min: (a: number, b: number) => number,
			clamp: (n: number, lo: number, hi: number) => number,
			a: number,
			steps: number,
			thr: number,
		) => { q: number; v: number };
	};
	const clamp = (n: number, lo: number, hi: number) =>
		Math.min(hi, Math.max(lo, n));
	const STOPS: [number, number, number][] = [
		[0, 0, 0],
		[85, 85, 85],
		[170, 170, 170],
		[255, 255, 255],
	];

	test("the GPU shader quantises exactly like the CPU, palette stop for stop", () => {
		const snap = shaderQuantise();
		for (const method of ["bayer4", "blue-noise"] as const) {
			const sampler = thresholdSampler(method);
			for (const levels of [2, 3, 6, 17]) {
				const steps = levels - 1;
				for (let step = 0; step <= 40; step += 1) {
					// The CPU path holds tones as float32.
					const tone = Math.fround(step / 40);
					const cpu = rasterizeField(() => tone, {
						cols: 8,
						rows: 8,
						levels,
						palette: STOPS,
						method,
					});
					for (let y = 0; y < 8; y += 1) {
						for (let x = 0; x < 8; x += 1) {
							const thr = sampler(x, y);
							const { q, v } = snap(Math.floor, Math.min, clamp, tone, steps, thr);
							const pos = q * 3;
							const i = Math.min(Math.floor(pos), 2);
							const stop = pos - i > v ? i + 1 : i;
							expect(cpu.data[(y * 8 + x) * 4]).toBe(STOPS[stop]?.[0] as number);
						}
					}
				}
			}
		}
	});

	test("the mean palette position follows the tone at every level count", () => {
		const palettes = [STOPS.filter((_, i) => i === 0 || i === 3), STOPS];
		const off: string[] = [];
		for (const palette of palettes) {
			for (const method of ["bayer4", "blue-noise"] as const) {
				for (const levels of [2, 3, 6, 17]) {
					for (let step = 0; step <= 40; step += 1) {
						const tone = step / 40;
						const r = rasterizeField(() => tone, {
							cols: 64,
							rows: 64,
							levels,
							palette,
							method,
						});
						let sum = 0;
						for (let i = 0; i < r.data.length; i += 4) sum += r.data[i] ?? 0;
						const mean = sum / (64 * 64) / 255;
						const miss = Math.abs(mean - tone);
						// Both extremes must be solid. Bayer4 has only 16 thresholds, so
						// its share moves in steps of 1/16 and two quantising stages add up
						// to a little more than the 0.03 of blue noise.
						const loose = method === "bayer4" ? 0.04 : 0.03;
						const limit = tone === 0 || tone === 1 ? 0 : loose;
						if (miss > limit) {
							off.push(
								`${palette.length} colours, ${method}, ${levels} levels, tone ${tone}: mean ${mean.toFixed(3)}`,
							);
						}
					}
				}
			}
		}
		expect(off).toEqual([]);
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

describe("resolveRasterPalette", () => {
	test("defaults to paper then ink, resolved through the host's tokens", () => {
		const host = document.createElement("div");
		host.style.setProperty("--dither-paper", "#102030");
		host.style.setProperty("--dither-ink", "#f0e0d0");
		document.body.appendChild(host);
		expect(resolveRasterPalette(host, undefined)).toEqual([
			[16, 32, 48],
			[240, 224, 208],
		]);
		expect(resolveRasterPalette(host, [])).toEqual([
			[16, 32, 48],
			[240, 224, 208],
		]);
	});

	test("a wrapper override wins and raw RGB entries pass through in place", () => {
		const host = document.createElement("div");
		host.style.setProperty("--dither-blue", "#0000ff");
		document.body.appendChild(host);
		const palette = resolveRasterPalette(host, ["blue", [1, 2, 3], "#00ff00"]);
		expect(palette[0]).toEqual([0, 0, 255]);
		expect(palette[1]).toEqual([1, 2, 3]);
		expect(palette[2]).toEqual([0, 255, 0]);
	});

	test("without a host it resolves hex and named fallbacks directly", () => {
		expect(resolveRasterPalette(null, ["#ff0000", "red", [9, 9, 9]])).toEqual([
			[255, 0, 0],
			// `red` resolves through var(--dither-red): not a hex, so the canvas
			// probe (absent in a test DOM) reads it as mid grey.
			[128, 128, 128],
			[9, 9, 9],
		]);
	});
});

describe("drawDitherCanvas", () => {
	function fakeCtx(width: number, height: number) {
		const puts: { image: ImageData; x: number; y: number }[] = [];
		const canvas = { width, height };
		const ctx = {
			canvas,
			putImageData(image: ImageData, x: number, y: number) {
				puts.push({ image, x, y });
			},
		} as unknown as CanvasRenderingContext2D;
		return { ctx, canvas, puts };
	}

	test("sizes the buffer to the cell grid and paints the dithered tones", () => {
		const { ctx, canvas, puts } = fakeCtx(0, 0);
		const layout = drawDitherCanvas(ctx, () => 1, {
			width: 12,
			height: 6,
			cell: 3,
			dpr: 1,
			palette: [
				[0, 0, 0],
				[255, 0, 0],
			],
		});
		expect(layout).toMatchObject({ cols: 4, rows: 2, sub: 1 });
		expect(canvas).toMatchObject({ width: 4, height: 2 });
		expect(puts).toHaveLength(1);
		const { image } = puts[0] ?? { image: null };
		expect(image?.width).toBe(4);
		expect(image?.height).toBe(2);
		// Full tone: every cell is the high colour.
		expect(Array.from(image?.data.slice(0, 4) ?? [])).toEqual([255, 0, 0, 255]);
		expect(Array.from(image?.data.slice(-4) ?? [])).toEqual([255, 0, 0, 255]);
	});

	test("takes the canvas size when no box is given", () => {
		const { ctx, canvas } = fakeCtx(30, 15);
		const layout = drawDitherCanvas(ctx, () => 0, { cell: 3, dpr: 1 });
		expect(layout).toMatchObject({ cols: 10, rows: 5 });
		expect(canvas).toMatchObject({ width: 10, height: 5 });
	});

	test("returns null and draws nothing for an empty box", () => {
		const { ctx, puts } = fakeCtx(0, 40);
		expect(drawDitherCanvas(ctx, () => 1)).toBeNull();
		expect(puts).toHaveLength(0);
		const sized = fakeCtx(40, 40);
		expect(drawDitherCanvas(sized.ctx, () => 1, { height: 0 })).toBeNull();
	});

	test("dots draw sub-pixels and the dpr is passed to the layout", () => {
		const { ctx, canvas } = fakeCtx(0, 0);
		const layout = drawDitherCanvas(ctx, () => 1, {
			width: 6,
			height: 6,
			cell: 3,
			dpr: 2,
			shape: "dot",
		});
		expect(layout?.dpr).toBe(2);
		expect(layout?.sub).toBeGreaterThan(1);
		expect(canvas.width).toBe((layout?.cols ?? 0) * (layout?.sub ?? 0));
	});

	test("forwards method, levels and time to the rasteriser", () => {
		const times: number[] = [];
		const { ctx, puts } = fakeCtx(0, 0);
		drawDitherCanvas(
			ctx,
			(_u, _v, t) => {
				times.push(t);
				return 0.5;
			},
			{
				width: 3,
				height: 3,
				cell: 3,
				dpr: 1,
				time: 2.5,
				method: "floyd-steinberg",
				levels: 2,
				palette: [
					[0, 0, 0],
					[255, 255, 255],
				],
			},
		);
		expect(times).toEqual([2.5]);
		const px = puts[0]?.image.data[0] ?? -1;
		expect([0, 255]).toContain(px);
	});

	test("on a real canvas: CSS size follows the layout and the cell comes from --dither-cell", () => {
		const parent = document.createElement("div");
		parent.style.setProperty("--dither-blue", "#0000ff");
		const canvas = document.createElement("canvas");
		canvas.style.setProperty("--dither-cell", "4px");
		parent.appendChild(canvas);
		document.body.appendChild(parent);
		const puts: ImageData[] = [];
		const ctx = {
			canvas,
			putImageData: (img: ImageData) => puts.push(img),
		} as unknown as CanvasRenderingContext2D;

		const layout = drawDitherCanvas(ctx, () => 1, {
			width: 20,
			height: 8,
			dpr: 1,
			palette: ["paper", "blue"],
		});
		expect(layout).toMatchObject({ cols: 5, rows: 2, cellCss: 4 });
		expect(canvas.style.width).toBe(`${layout?.cssW}px`);
		expect(canvas.style.height).toBe(`${layout?.cssH}px`);
		expect(canvas.style.imageRendering).toBe("pixelated");
		expect(canvas.width).toBe(5);
		expect(canvas.height).toBe(2);
		expect(Array.from(puts[0]?.data.slice(0, 4) ?? [])).toEqual([
			0, 0, 255, 255,
		]);
	});
});

describe("rasterizeField inputs", () => {
	test("takes precomputed tones as an array", () => {
		const tones = Float32Array.from([0, 1, 1, 0]);
		const r = rasterizeField(tones, {
			cols: 2,
			rows: 2,
			levels: 2,
			palette: [PAPER, INK],
			method: "bayer2",
		});
		const on = (i: number) => (r.data[i * 4] ?? 0) > 127;
		expect([on(0), on(1), on(2), on(3)]).toEqual([false, true, true, false]);
	});

	test("an empty palette falls back to black and white", () => {
		const r = rasterizeField(() => 1, {
			cols: 1,
			rows: 1,
			levels: 2,
			palette: [],
		});
		expect(Array.from(r.data)).toEqual([255, 255, 255, 255]);
	});

	test("a single-colour palette fills every cell with it", () => {
		const r = rasterizeField(() => 0.7, {
			cols: 2,
			rows: 1,
			palette: [[10, 20, 30]],
		});
		expect(Array.from(r.data)).toEqual([10, 20, 30, 255, 10, 20, 30, 255]);
	});
});

describe("drawDitherCanvas defaults", () => {
	test("without a cell or dpr it uses 3px cells at the page's device pixel ratio", () => {
		const canvas = { width: 30, height: 30 };
		const ctx = {
			canvas,
			putImageData() {},
		} as unknown as CanvasRenderingContext2D;
		const layout = drawDitherCanvas(ctx, () => 0);
		expect(layout?.dpr).toBe(devicePixelRatio);
		expect(layout?.cellCss).toBe(3);
		expect(layout?.cols).toBe(10);
		expect(canvas.width).toBe(layout?.cols as number);
	});
});
