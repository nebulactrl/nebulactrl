/**
 * The canvas-2D way to draw a dither: a CPU rasteriser for fields written in
 * JavaScript. It matches the WebGL path cell for cell (same Bayer / blue-noise
 * thresholds, same level snapping, same palette ramp, same device-pixel cell
 * snapping), so a still frame is identical on either.
 *
 * Use it where a shader is overkill or unavailable: export buttons, thumbnails,
 * `OffscreenCanvas` in a worker, and fields that need arbitrary JS (data
 * lookups, the land mask). Use `DitherShader` for anything that animates
 * full-bleed.
 *
 *   `rasterizeField`   pure: tones in, RGBA pixels out (testable, runs in a worker)
 *   `drawDitherCanvas` browser: sizes the canvas to the pixel grid and paints it
 */

import type { DitherMatrix } from "./bayer";
import { ditherColorVar } from "./colors";
import {
	type DiffusionMethod,
	diffuseTones,
	isDiffusionMethod,
} from "./diffuse";
import { computeLayout, type GridLayout, parseCellValue } from "./gl/layout";
import { resolvePaletteColors } from "./gl/palette";
import { thresholdSampler } from "./threshold";
import { type RGB, toRGB } from "./tokens";

/** Tone for a cell: `u` runs left to right and `v` bottom to top, both 0..1 (as in a GLSL field). */
export type ToneField = (u: number, v: number, time: number) => number;

/** Ordered matrices animate calmly; diffusion is for stills. */
export type DrawMethod = DitherMatrix | DiffusionMethod;

export interface RasterizeOptions {
	/** Cells across and down. */
	cols: number;
	rows: number;
	/** Ordered matrix or error-diffusion kernel. Default `bayer4`. */
	method?: DrawMethod;
	/** Tone steps before dithering. Default 6. */
	levels?: number;
	/** Ramp from paper (tone 0) to ink (tone 1), 0..255 channels. Default black to white. */
	palette?: readonly RGB[];
	/** Seconds passed to the field. Default 0. */
	time?: number;
	/** Canvas pixels per cell edge. 1 draws squares; more lets `dot` round the cells. Default 1. */
	sub?: number;
	shape?: "square" | "dot";
}

export interface Raster {
	/** Pixels across: `cols * sub`. */
	width: number;
	height: number;
	/** RGBA, row 0 on top. */
	data: Uint8ClampedArray<ArrayBuffer>;
}

const DEFAULT_PALETTE: readonly RGB[] = [
	[0, 0, 0],
	[255, 255, 255],
];

/** The tone of every cell, row 0 on top, clamped to 0..1. */
export function sampleTones(
	field: ToneField,
	cols: number,
	rows: number,
	time = 0,
): Float32Array {
	const tones = new Float32Array(cols * rows);
	for (let y = 0; y < rows; y += 1) {
		const v = 1 - (y + 0.5) / rows;
		for (let x = 0; x < cols; x += 1) {
			const tone = field((x + 0.5) / cols, v, time);
			tones[y * cols + x] =
				tone < 0 ? 0 : tone > 1 ? 1 : Number.isFinite(tone) ? tone : 0;
		}
	}
	return tones;
}

/** Colour at ramp position `pos` (0..1) with linear blending between stops. */
function rampColor(palette: readonly RGB[], pos: number): RGB {
	const last = palette.length - 1;
	if (last <= 0) return palette[0] ?? [0, 0, 0];
	const at = Math.min(last, Math.max(0, pos)) * last;
	const i = Math.min(last - 1, Math.floor(at));
	const f = at - i;
	const a = palette[i] ?? [0, 0, 0];
	const b = palette[i + 1] ?? a;
	return [
		a[0] + (b[0] - a[0]) * f,
		a[1] + (b[1] - a[1]) * f,
		a[2] + (b[2] - a[2]) * f,
	];
}

/**
 * Dither a tone field (a function, or tones you already have, row-major with
 * `cols * rows` entries) into RGBA pixels. Pure; never touches the DOM.
 */
export function rasterizeField(
	field: ToneField | ArrayLike<number>,
	options: RasterizeOptions,
): Raster {
	const { cols, rows } = options;
	const method = options.method ?? "bayer4";
	const levels = Math.max(2, Math.floor(options.levels ?? 6));
	const palette =
		options.palette && options.palette.length > 0
			? options.palette
			: DEFAULT_PALETTE;
	const sub = Math.max(1, Math.floor(options.sub ?? 1));
	const round = options.shape === "dot" && sub > 1;
	const steps = levels - 1;

	const tones =
		typeof field === "function"
			? sampleTones(field, cols, rows, options.time ?? 0)
			: field;

	const width = cols * sub;
	const height = rows * sub;
	const data = new Uint8ClampedArray(width * height * 4);
	const paper = palette[0] ?? [0, 0, 0];
	const last = palette.length - 1;

	// Which ramp segment and which colour a cell takes.
	let diffused: Uint8Array | null = null;
	let threshold: ((x: number, y: number) => number) | null = null;
	if (isDiffusionMethod(method)) {
		diffused = diffuseTones(tones, cols, rows, { method, levels });
	} else {
		threshold = thresholdSampler(method);
	}

	const put = (px: number, py: number, rgb: RGB) => {
		const o = (py * width + px) * 4;
		data[o] = rgb[0];
		data[o + 1] = rgb[1];
		data[o + 2] = rgb[2];
		data[o + 3] = 255;
	};

	for (let y = 0; y < rows; y += 1) {
		for (let x = 0; x < cols; x += 1) {
			let colour: RGB;
			if (diffused) {
				colour = rampColor(palette, (diffused[y * cols + x] ?? 0) / steps);
			} else {
				const tone = tones[y * cols + x] ?? 0;
				const q = Math.floor(tone * steps + 0.5) / steps;
				const pos = q * last;
				const i = Math.min(Math.floor(pos), Math.max(last - 1, 0));
				const lowColour = palette[i] ?? paper;
				const highColour = palette[i + 1] ?? lowColour;
				colour = pos - i > (threshold?.(x, y) ?? 0.5) ? highColour : lowColour;
			}
			if (sub === 1) {
				put(x, y, colour);
				continue;
			}
			for (let sy = 0; sy < sub; sy += 1) {
				for (let sx = 0; sx < sub; sx += 1) {
					let inside = true;
					if (round) {
						const fx = (sx + 0.5) / sub - 0.5;
						const fy = (sy + 0.5) / sub - 0.5;
						inside = Math.hypot(fx, fy) < 0.5;
					}
					put(x * sub + sx, y * sub + sy, inside ? colour : paper);
				}
			}
		}
	}
	return { width, height, data };
}

export interface DrawDitherOptions {
	/** Box the grid covers, CSS px. Default: the canvas's CSS size. */
	width?: number;
	height?: number;
	/** Cell in CSS px. Default: `--dither-cell` on the canvas, then 3. */
	cell?: number;
	/** Device-pixel ratio. Default `devicePixelRatio`. */
	dpr?: number;
	method?: DrawMethod;
	levels?: number;
	shape?: "square" | "dot";
	/** Colours low tone to high: CSS colours, `--tokens` or names (`"blue"`). Default paper, ink. */
	palette?: readonly (string | RGB)[];
	time?: number;
}

/** Resolve a palette to 0..255 channels in the context of `host`. */
export function resolveRasterPalette(
	host: Element | null,
	palette: readonly (string | RGB)[] | undefined,
): RGB[] {
	const entries = palette && palette.length > 0 ? palette : ["paper", "ink"];
	const strings = entries.filter((e): e is string => typeof e === "string");
	const resolved = host
		? resolvePaletteColors(host, strings)
		: strings.map((s) => {
				const [r, g, b] = toRGB(ditherColorVar(s));
				return [r / 255, g / 255, b / 255, 1] as const;
			});
	let next = 0;
	return entries.map((entry) => {
		if (typeof entry !== "string") return entry;
		const c = resolved[next++] ?? [0, 0, 0, 1];
		return [c[0] * 255, c[1] * 255, c[2] * 255] as RGB;
	});
}

/**
 * Paint a dither onto a 2D canvas. The canvas is resized to the pixel grid
 * (`cols × rows` cells at whole device pixels, so every cell is the same size
 * at any DPR) and its CSS size set to cover the box; add
 * `image-rendering: pixelated` if you scale it further. Returns the layout it
 * used, or null when the context has no canvas size to work with.
 */
export function drawDitherCanvas(
	ctx: CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D,
	field: ToneField | ArrayLike<number>,
	options: DrawDitherOptions = {},
): GridLayout | null {
	const canvas = ctx.canvas;
	const html =
		typeof HTMLCanvasElement !== "undefined" &&
		canvas instanceof HTMLCanvasElement
			? canvas
			: null;
	const width = options.width ?? html?.clientWidth ?? canvas.width;
	const height = options.height ?? html?.clientHeight ?? canvas.height;
	if (width <= 0 || height <= 0) return null;
	const cell =
		options.cell ??
		(html
			? parseCellValue(getComputedStyle(html).getPropertyValue("--dither-cell"))
			: 3);
	const dpr =
		options.dpr ??
		(typeof devicePixelRatio === "number" ? devicePixelRatio : 1);
	const shape = options.shape ?? "square";
	const layout = computeLayout(width, height, cell, dpr, shape);

	const palette = resolveRasterPalette(
		html?.parentElement ?? html,
		options.palette,
	);
	const raster = rasterizeField(field, {
		cols: layout.cols,
		rows: layout.rows,
		sub: layout.sub,
		shape,
		palette,
		...(options.method ? { method: options.method } : {}),
		...(options.levels ? { levels: options.levels } : {}),
		...(options.time !== undefined ? { time: options.time } : {}),
	});

	canvas.width = raster.width;
	canvas.height = raster.height;
	if (html) {
		html.style.width = `${layout.cssW}px`;
		html.style.height = `${layout.cssH}px`;
		html.style.imageRendering = "pixelated";
	}
	ctx.putImageData(
		new ImageData(raster.data, raster.width, raster.height),
		0,
		0,
	);
	return layout;
}
