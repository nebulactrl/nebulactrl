/*
 * `ditherImage` follows the shape and the colour-distance weights of
 * dithercn's `ditherImage`.
 * Copyright (c) 2026 Leo
 * Copyright (c) 2026 Shadcn Labs (startercn, which this project is based on)
 * Licensed under the MIT License: https://github.com/opencoredev/dithercn/blob/main/LICENSE
 * (the full permission notice is reproduced in the repository NOTICE)
 * Portions adapted from https://github.com/opencoredev/dithercn
 */

/**
 * Pure pixel dithering for the image components: error diffusion
 * (Floyd–Steinberg, Atkinson, Sierra Lite), ordered Bayer and blue noise, and a
 * hard threshold, each mapped onto a palette. Nothing here touches the DOM,
 * so the same code runs on the main thread and inside `dither.worker.ts`.
 *
 * Two entry points:
 *  - `ditherImage(image, { method, palette })`: the general, dithercn-shaped
 *    call. Palette colours are 0..1 RGB, the result is a new `ImageData`.
 *  - `ditherRGBA(rgba, w, h, options)`: what the components use. Adds tone
 *    adjustments (contrast, brightness, sharpen, normalise, invert) and two
 *    palette modes:
 *      - `tone`: the image is reduced to luminance and the palette is a ramp
 *        from low to high tone (paper → accent → ink). Diffusion works on one
 *        channel.
 *      - `color`: every pixel snaps to the nearest palette colour in RGB and
 *        the error is diffused per channel. For palettes that are not a ramp.
 *
 * Fully transparent pixels (alpha < `VOID_ALPHA`) stay transparent and neither
 * take nor give error, so a logo's silhouette is never smeared.
 */

import type { BayerSize, DitherMatrix } from "../../lib/bayer";
import {
	DIFFUSION_TAPS,
	type DiffusionMethod,
	type DiffusionTap,
	isDiffusionMethod,
} from "../../lib/diffuse";
import { thresholdSampler } from "../../lib/threshold";
import type { RGB } from "../../lib/tokens";

export type DitherAlgorithm =
	| "floyd-steinberg"
	| "atkinson"
	| "sierra-lite"
	| "bayer"
	| "blue-noise"
	| "threshold";

export type PaletteMode = "tone" | "color";

/** The method names of `ditherImage`: ordered matrices and error diffusion. */
export type OrderedMethod = DitherMatrix;
export type { DiffusionMethod };
export type DitherMethod = OrderedMethod | DiffusionMethod;

export const DITHER_METHODS = [
	"bayer2",
	"bayer4",
	"bayer8",
	"blue-noise",
	"floyd-steinberg",
	"atkinson",
	"sierra-lite",
] as const satisfies readonly DitherMethod[];

/** `"bayer8"` → `{ algorithm: "bayer", bayerSize: 8 }`; diffusion and blue noise map to themselves. */
export function methodToAlgorithm(method: DitherMethod): {
	algorithm: DitherAlgorithm;
	bayerSize: BayerSize;
} {
	switch (method) {
		case "bayer2":
			return { algorithm: "bayer", bayerSize: 2 };
		case "bayer4":
			return { algorithm: "bayer", bayerSize: 4 };
		case "bayer8":
			return { algorithm: "bayer", bayerSize: 8 };
		default:
			return { algorithm: method, bayerSize: 4 };
	}
}

/** The ordered matrix an algorithm thresholds with, or `null` for diffusion and hard threshold. */
export function matrixOf(
	algorithm: DitherAlgorithm,
	bayerSize: BayerSize,
): DitherMatrix | null {
	if (algorithm === "blue-noise") return "blue-noise";
	if (algorithm !== "bayer") return null;
	return bayerSize === 2 ? "bayer2" : bayerSize === 8 ? "bayer8" : "bayer4";
}

export interface RasterOptions {
	algorithm: DitherAlgorithm;
	/** Tone mode: ramp from lowest to highest tone. Colour mode: any set. */
	palette: readonly RGB[];
	/**
	 * Opacity 0..1 of each palette entry (missing = opaque). A `transparent`
	 * entry lets the page show through where the image takes that tone.
	 */
	paletteAlpha?: readonly number[] | undefined;
	mode: PaletteMode;
	/** Matrix size for `algorithm: "bayer"`. */
	bayerSize: BayerSize;
	/** 1 = unchanged; >1 pushes tones apart around mid grey. */
	contrast: number;
	/** Added to every tone, -1..1. */
	brightness: number;
	invert: boolean;
	/** Stretch the 1st–99th percentile of tones to the full range first. */
	normalize: boolean;
	/** Edge sharpening before dithering (3×3 unsharp mask), 0..1. */
	sharpen: number;
	/** Alternate scan direction per row, which hides diffusion worms. */
	serpentine: boolean;
}

export const DEFAULT_RASTER_OPTIONS: RasterOptions = {
	algorithm: "floyd-steinberg",
	palette: [
		[0, 0, 0],
		[255, 255, 255],
	],
	mode: "tone",
	bayerSize: 4,
	contrast: 1,
	brightness: 0,
	invert: false,
	normalize: false,
	sharpen: 0,
	serpentine: true,
};

/** Below this alpha a pixel is treated as empty. */
export const VOID_ALPHA = 8;

export function resolveRasterOptions(
	partial: Partial<RasterOptions> = {},
): RasterOptions {
	return { ...DEFAULT_RASTER_OPTIONS, ...partial };
}

/** Rec. 709 luminance of 0..255 channels, as 0..1. */
export function luminance(r: number, g: number, b: number): number {
	return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
}

/** Index of the palette colour closest to `rgb` (squared Euclidean in RGB). */
export function nearestPaletteIndex(
	r: number,
	g: number,
	b: number,
	palette: readonly RGB[],
): number {
	let best = 0;
	let bestDistance = Number.POSITIVE_INFINITY;
	for (let i = 0; i < palette.length; i += 1) {
		const c = palette[i];
		if (!c) continue;
		const dr = r - c[0];
		const dg = g - c[1];
		const db = b - c[2];
		const distance = dr * dr + dg * dg + db * db;
		if (distance < bestDistance) {
			bestDistance = distance;
			best = i;
		}
	}
	return best;
}

/**
 * Snap every pixel to its nearest palette colour with no dithering at all.
 * Alpha is kept; returns a new RGBA buffer.
 */
export function quantizeToPalette(
	rgba: Uint8ClampedArray,
	palette: readonly RGB[],
): Uint8ClampedArray<ArrayBuffer> {
	const out = new Uint8ClampedArray(rgba.length);
	for (let i = 0; i < rgba.length; i += 4) {
		const c =
			palette[
				nearestPaletteIndex(
					rgba[i] ?? 0,
					rgba[i + 1] ?? 0,
					rgba[i + 2] ?? 0,
					palette,
				)
			];
		out[i] = c?.[0] ?? 0;
		out[i + 1] = c?.[1] ?? 0;
		out[i + 2] = c?.[2] ?? 0;
		out[i + 3] = rgba[i + 3] ?? 255;
	}
	return out;
}

/** The error-diffusion kernel of an algorithm, or `null` for ordered and threshold. */
export function tapsFor(algorithm: string): readonly DiffusionTap[] | null {
	return isDiffusionMethod(algorithm) ? DIFFUSION_TAPS[algorithm] : null;
}

const clamp01 = (value: number) => (value < 0 ? 0 : Math.min(value, 1));

/** The `p` percentile (0..1) of a 256-bin histogram of tones, as a tone. */
function percentile(histogram: Uint32Array, total: number, p: number): number {
	const target = total * p;
	let seen = 0;
	for (let bin = 0; bin < histogram.length; bin += 1) {
		seen += histogram[bin] ?? 0;
		if (seen >= target) return bin / (histogram.length - 1);
	}
	return 1;
}

/** A 3×3 box blur of a one-channel image. Edges clamp. */
function boxBlur(
	values: Float32Array,
	width: number,
	height: number,
): Float32Array {
	const out = new Float32Array(values.length);
	for (let y = 0; y < height; y += 1) {
		const up = Math.max(0, y - 1) * width;
		const row = y * width;
		const down = Math.min(height - 1, y + 1) * width;
		for (let x = 0; x < width; x += 1) {
			const left = Math.max(0, x - 1);
			const right = Math.min(width - 1, x + 1);
			out[row + x] =
				((values[up + left] ?? 0) +
					(values[up + x] ?? 0) +
					(values[up + right] ?? 0) +
					(values[row + left] ?? 0) +
					(values[row + x] ?? 0) +
					(values[row + right] ?? 0) +
					(values[down + left] ?? 0) +
					(values[down + x] ?? 0) +
					(values[down + right] ?? 0)) /
				9;
		}
	}
	return out;
}

/**
 * Per-pixel tone 0..1 after sharpening, brightness, contrast, invert and
 * (optionally) percentile normalisation. Void pixels read as 0 and are
 * skipped later. Sharpening needs the bitmap's `size`; without it, or with
 * `sharpen` 0, the tones are not sharpened.
 */
export function toneField(
	rgba: Uint8ClampedArray,
	opts: Pick<
		RasterOptions,
		"contrast" | "brightness" | "invert" | "normalize"
	> & {
		sharpen?: number | undefined;
	},
	size?: { width: number; height: number },
): Float32Array {
	const count = rgba.length / 4;
	const tone = new Float32Array(count);
	const histogram = new Uint32Array(256);
	let visible = 0;
	for (let p = 0; p < count; p += 1) {
		const i = p * 4;
		const lum = luminance(rgba[i] ?? 0, rgba[i + 1] ?? 0, rgba[i + 2] ?? 0);
		tone[p] = lum;
		if ((rgba[i + 3] ?? 0) >= VOID_ALPHA) {
			histogram[Math.round(lum * 255)] =
				(histogram[Math.round(lum * 255)] ?? 0) + 1;
			visible += 1;
		}
	}
	let low = 0;
	let span = 1;
	if (opts.normalize && visible > 0) {
		low = percentile(histogram, visible, 0.01);
		const high = percentile(histogram, visible, 0.99);
		span = Math.max(high - low, 1 / 255);
	}
	const sharpen = opts.sharpen ?? 0;
	const blur =
		sharpen > 0 && size ? boxBlur(tone, size.width, size.height) : null;
	// Sharpening pushes each pixel away from its neighbourhood average.
	const amount = sharpen * 1.5;
	for (let p = 0; p < count; p += 1) {
		const raw = tone[p] ?? 0;
		const sharp = blur ? raw + amount * (raw - (blur[p] ?? raw)) : raw;
		let value = (sharp - low) / span;
		value = (value - 0.5) * opts.contrast + 0.5 + opts.brightness;
		if (opts.invert) value = 1 - value;
		tone[p] = clamp01(value);
	}
	return tone;
}

/**
 * Dither an RGBA bitmap onto `opts.palette`. Returns a new RGBA buffer of the
 * same size; the input is not modified. Deterministic for a given input.
 */
export function ditherRGBA(
	rgba: Uint8ClampedArray,
	width: number,
	height: number,
	options: Partial<RasterOptions> = {},
): Uint8ClampedArray<ArrayBuffer> {
	const opts = resolveRasterOptions(options);
	const out = new Uint8ClampedArray(width * height * 4);
	const palette = opts.palette;
	if (palette.length === 0 || width <= 0 || height <= 0) return out;
	return opts.mode === "tone"
		? ditherTone(rgba, width, height, opts, out)
		: ditherColor(rgba, width, height, opts, out);
}

function writePixel(
	out: Uint8ClampedArray,
	p: number,
	c: RGB | undefined,
	alpha: number,
	opacity = 1,
) {
	const i = p * 4;
	out[i] = c?.[0] ?? 0;
	out[i + 1] = c?.[1] ?? 0;
	out[i + 2] = c?.[2] ?? 0;
	out[i + 3] = opacity === 1 ? alpha : Math.round(alpha * opacity);
}

function ditherTone(
	rgba: Uint8ClampedArray,
	width: number,
	height: number,
	opts: RasterOptions,
	out: Uint8ClampedArray<ArrayBuffer>,
): Uint8ClampedArray<ArrayBuffer> {
	const palette = opts.palette;
	const last = palette.length - 1;
	const tone = toneField(rgba, opts, { width, height });
	const kind = matrixOf(opts.algorithm, opts.bayerSize);
	const threshold = kind ? thresholdSampler(kind) : null;
	const taps = tapsFor(opts.algorithm);

	for (let y = 0; y < height; y += 1) {
		const reverse = opts.serpentine && taps !== null && y % 2 === 1;
		for (let step = 0; step < width; step += 1) {
			const x = reverse ? width - 1 - step : step;
			const p = y * width + x;
			const alpha = rgba[p * 4 + 3] ?? 0;
			if (alpha < VOID_ALPHA) {
				writePixel(out, p, undefined, 0);
				continue;
			}
			const value = tone[p] ?? 0;
			let index: number;
			if (last === 0) {
				index = 0;
			} else if (threshold) {
				const scaled = clamp01(value) * last;
				const base = Math.min(last - 1, Math.floor(scaled));
				index = base + (scaled - base > threshold(x, y) ? 1 : 0);
			} else {
				index = Math.min(last, Math.max(0, Math.round(clamp01(value) * last)));
			}
			writePixel(
				out,
				p,
				palette[index],
				alpha,
				opts.paletteAlpha?.[index] ?? 1,
			);
			if (taps && last > 0) {
				const error = value - index / last;
				for (const tap of taps) {
					const nx = x + (reverse ? -tap.dx : tap.dx);
					const ny = y + tap.dy;
					if (nx < 0 || nx >= width || ny >= height) continue;
					const q = ny * width + nx;
					if ((rgba[q * 4 + 3] ?? 0) < VOID_ALPHA) continue;
					tone[q] = (tone[q] ?? 0) + error * tap.weight;
				}
			}
		}
	}
	return out;
}

function ditherColor(
	rgba: Uint8ClampedArray,
	width: number,
	height: number,
	opts: RasterOptions,
	out: Uint8ClampedArray<ArrayBuffer>,
): Uint8ClampedArray<ArrayBuffer> {
	const palette = opts.palette;
	const count = width * height;
	// Contrast, brightness and invert apply per channel here; normalisation is
	// a tone-mode concept.
	const chan = [
		new Float32Array(count),
		new Float32Array(count),
		new Float32Array(count),
	] as const;
	for (let p = 0; p < count; p += 1) {
		for (let c = 0; c < 3; c += 1) {
			let v = (rgba[p * 4 + c] ?? 0) / 255;
			v = (v - 0.5) * opts.contrast + 0.5 + opts.brightness;
			if (opts.invert) v = 1 - v;
			(chan[c] as Float32Array)[p] = clamp01(v) * 255;
		}
	}
	const kind = matrixOf(opts.algorithm, opts.bayerSize);
	const threshold = kind ? thresholdSampler(kind) : null;
	// Ordered colour dither perturbs each channel by up to half the spacing of a
	// palette spread evenly through the RGB cube.
	const spread = 255 / Math.max(2, Math.cbrt(palette.length));
	const taps = tapsFor(opts.algorithm);
	const [rCh, gCh, bCh] = chan;

	for (let y = 0; y < height; y += 1) {
		const reverse = opts.serpentine && taps !== null && y % 2 === 1;
		for (let step = 0; step < width; step += 1) {
			const x = reverse ? width - 1 - step : step;
			const p = y * width + x;
			const alpha = rgba[p * 4 + 3] ?? 0;
			if (alpha < VOID_ALPHA) {
				writePixel(out, p, undefined, 0);
				continue;
			}
			let r = rCh[p] ?? 0;
			let g = gCh[p] ?? 0;
			let b = bCh[p] ?? 0;
			if (threshold) {
				const offset = (threshold(x, y) - 0.5) * spread;
				r += offset;
				g += offset;
				b += offset;
			}
			const index = nearestPaletteIndex(r, g, b, palette);
			const chosen = palette[index];
			writePixel(out, p, chosen, alpha, opts.paletteAlpha?.[index] ?? 1);
			if (taps && chosen) {
				const er = r - chosen[0];
				const eg = g - chosen[1];
				const eb = b - chosen[2];
				for (const tap of taps) {
					const nx = x + (reverse ? -tap.dx : tap.dx);
					const ny = y + tap.dy;
					if (nx < 0 || nx >= width || ny >= height) continue;
					const q = ny * width + nx;
					if ((rgba[q * 4 + 3] ?? 0) < VOID_ALPHA) continue;
					rCh[q] = (rCh[q] ?? 0) + er * tap.weight;
					gCh[q] = (gCh[q] ?? 0) + eg * tap.weight;
					bCh[q] = (bCh[q] ?? 0) + eb * tap.weight;
				}
			}
		}
	}
	return out;
}

// -- ditherImage -------------------------------------------------------------

/** An RGB colour, each channel in [0, 1]. A fourth channel (alpha) is ignored. */
export type PaletteColor =
	| readonly [number, number, number]
	| readonly [number, number, number, number];

/** At least two colours, darkest first. Pixels map to the nearest one. */
export type ImagePalette = readonly [
	PaletteColor,
	PaletteColor,
	...PaletteColor[],
];

/** Anything shaped like `ImageData`: RGBA bytes, row by row. */
export interface PixelBuffer {
	readonly data: Uint8ClampedArray;
	readonly width: number;
	readonly height: number;
}

interface SharedImageOptions {
	/** Output colours. Default black and white. */
	palette?: ImagePalette | undefined;
	/**
	 * Brightness cut-off in [0, 1]. Lower values push more pixels towards the
	 * lighter colours. Default 0.5.
	 */
	threshold?: number | undefined;
}

/** Ordered dithering: stable between frames, a visible (or, for blue noise, no) pattern. */
export interface OrderedImageOptions extends SharedImageOptions {
	method: OrderedMethod;
}

/** Error diffusion: finer detail on photos and logos. */
export interface DiffusionImageOptions extends SharedImageOptions {
	method: DiffusionMethod;
	/** Alternate the scan direction each row. Default `true`. */
	serpentine?: boolean | undefined;
}

export type DitherImageOptions = OrderedImageOptions | DiffusionImageOptions;

const BLACK_AND_WHITE: ImagePalette = [
	[0, 0, 0],
	[1, 1, 1],
];

/** Weighted towards green, as the eye is. Index of the nearest of `colors` (r, g, b triples). */
function nearestFlat(
	colors: Float32Array,
	r: number,
	g: number,
	b: number,
): number {
	let best = 0;
	let bestDistance = Number.POSITIVE_INFINITY;
	for (let index = 0; index < colors.length; index += 3) {
		const dr = r - (colors[index] ?? 0);
		const dg = g - (colors[index + 1] ?? 0);
		const db = b - (colors[index + 2] ?? 0);
		const distance = 0.3 * dr * dr + 0.59 * dg * dg + 0.11 * db * db;
		if (distance < bestDistance) {
			bestDistance = distance;
			best = index / 3;
		}
	}
	return best;
}

/** An `ImageData` where one exists (browsers, workers), else a plain object of the same shape. */
function makeImageData(
	data: Uint8ClampedArray<ArrayBuffer>,
	width: number,
	height: number,
): ImageData {
	if (typeof ImageData !== "undefined")
		return new ImageData(data, width, height);
	return { data, width, height, colorSpace: "srgb" } as ImageData;
}

/**
 * Dither an image to a palette: `bayer2`/`bayer4`/`bayer8`/`blue-noise`
 * (ordered) or `floyd-steinberg`/`atkinson`/`sierra-lite` (error diffusion).
 * Returns a new `ImageData` of the same size and never changes `image`.
 * Default: Floyd–Steinberg in black and white. Alpha is kept as is.
 *
 * Error diffusion depends on every earlier pixel, so a small change to the
 * input can move dots everywhere: keep it for still images.
 */
export function ditherImage(
	image: PixelBuffer,
	options: DitherImageOptions = { method: "floyd-steinberg" },
): ImageData {
	const { data, width, height } = image;
	const palette = options.palette ?? BLACK_AND_WHITE;
	const bias = 0.5 - (options.threshold ?? 0.5);
	const colors = new Float32Array(palette.length * 3);
	palette.forEach((color, index) => {
		colors[index * 3] = color[0];
		colors[index * 3 + 1] = color[1];
		colors[index * 3 + 2] = color[2];
	});
	const out = new Uint8ClampedArray(data.length);
	const pick = (index: number, offset: number) => {
		out[offset] = (colors[index * 3] ?? 0) * 255;
		out[offset + 1] = (colors[index * 3 + 1] ?? 0) * 255;
		out[offset + 2] = (colors[index * 3 + 2] ?? 0) * 255;
		out[offset + 3] = data[offset + 3] ?? 255;
	};

	const taps = tapsFor(options.method);
	if (!taps) {
		const sample = thresholdSampler(options.method as OrderedMethod);
		// Spread the threshold over one step between palette colours.
		const spread = 1 / (palette.length - 1);
		for (let y = 0; y < height; y += 1) {
			for (let x = 0; x < width; x += 1) {
				const offset = (y * width + x) * 4;
				const shift = (0.5 - sample(x, y)) * spread + bias;
				const index = nearestFlat(
					colors,
					(data[offset] ?? 0) / 255 + shift,
					(data[offset + 1] ?? 0) / 255 + shift,
					(data[offset + 2] ?? 0) / 255 + shift,
				);
				pick(index, offset);
			}
		}
		return makeImageData(out, width, height);
	}

	const serpentine =
		"serpentine" in options ? (options.serpentine ?? true) : true;
	const work = new Float32Array(width * height * 3);
	for (let index = 0; index < width * height; index += 1) {
		for (let channel = 0; channel < 3; channel += 1) {
			work[index * 3 + channel] = (data[index * 4 + channel] ?? 0) / 255 + bias;
		}
	}
	for (let y = 0; y < height; y += 1) {
		const reverse = serpentine && y % 2 === 1;
		for (let step = 0; step < width; step += 1) {
			const x = reverse ? width - 1 - step : step;
			const p = y * width + x;
			const r = work[p * 3] ?? 0;
			const g = work[p * 3 + 1] ?? 0;
			const b = work[p * 3 + 2] ?? 0;
			const index = nearestFlat(colors, r, g, b);
			pick(index, p * 4);
			const er = r - (colors[index * 3] ?? 0);
			const eg = g - (colors[index * 3 + 1] ?? 0);
			const eb = b - (colors[index * 3 + 2] ?? 0);
			for (const tap of taps) {
				const nx = x + (reverse ? -tap.dx : tap.dx);
				const ny = y + tap.dy;
				if (nx < 0 || nx >= width || ny >= height) continue;
				const q = (ny * width + nx) * 3;
				work[q] = (work[q] ?? 0) + er * tap.weight;
				work[q + 1] = (work[q + 1] ?? 0) + eg * tap.weight;
				work[q + 2] = (work[q + 2] ?? 0) + eb * tap.weight;
			}
		}
	}
	return makeImageData(out, width, height);
}

/** Source rectangle of an image of `srcW × srcH` shown in a `dstW × dstH` box. */
export interface CoverRect {
	sx: number;
	sy: number;
	sw: number;
	sh: number;
	/** Destination rectangle, in the box's pixels. */
	dx: number;
	dy: number;
	dw: number;
	dh: number;
}

/**
 * `object-fit: cover` (crop the source to the box's aspect ratio) or
 * `contain` (letterbox the destination), as the rectangles for `drawImage`.
 */
export function fitRect(
	srcW: number,
	srcH: number,
	dstW: number,
	dstH: number,
	fit: "cover" | "contain",
): CoverRect {
	const srcRatio = srcW / srcH;
	const dstRatio = dstW / dstH;
	if (fit === "cover") {
		let sw = srcW;
		let sh = srcH;
		if (srcRatio > dstRatio) sw = srcH * dstRatio;
		else sh = srcW / dstRatio;
		return {
			sx: (srcW - sw) / 2,
			sy: (srcH - sh) / 2,
			sw,
			sh,
			dx: 0,
			dy: 0,
			dw: dstW,
			dh: dstH,
		};
	}
	let dw = dstW;
	let dh = dstH;
	if (srcRatio > dstRatio) dh = dstW / srcRatio;
	else dw = dstH * srcRatio;
	return {
		sx: 0,
		sy: 0,
		sw: srcW,
		sh: srcH,
		dx: (dstW - dw) / 2,
		dy: (dstH - dh) / 2,
		dw,
		dh,
	};
}

/** A stable string for caching: same src + same look + same grid ⇒ same result. */
export function rasterCacheKey(
	src: string,
	width: number,
	height: number,
	fit: string,
	opts: RasterOptions,
): string {
	const palette = opts.palette.map((c) => c.join(",")).join(";");
	return [
		src,
		`${width}x${height}`,
		fit,
		opts.algorithm,
		opts.mode,
		opts.bayerSize,
		opts.contrast,
		opts.brightness,
		opts.invert ? 1 : 0,
		opts.normalize ? 1 : 0,
		opts.sharpen,
		opts.serpentine ? 1 : 0,
		palette,
		opts.paletteAlpha?.join(",") ?? "",
	].join("|");
}
