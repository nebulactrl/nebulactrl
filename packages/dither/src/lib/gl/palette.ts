/**
 * Palette handling for DitherShader: CSS colour strings (token names, var(),
 * oklch(), color-mix(), "transparent") become an RGBA ramp, low tone -> high,
 * packed into the `u_pal` uniform array.
 */
import { type RefObject, useEffect, useState } from "react";
import { type DitherMatrix, matrixTones } from "../bayer";
import { DITHER_COLOR_FALLBACKS, ditherColorToken } from "../colors";

/** Linear-free sRGB, each channel 0..1. */
export type RGBA = readonly [number, number, number, number];

/** Longest ramp the shader holds; longer palettes are sampled evenly. */
export const MAX_PALETTE = 8;

export const DEFAULT_PALETTE: readonly string[] = [
	"--dither-paper",
	"--dither-ink",
];

/** Fallbacks used when a `--dither-*` token is not defined on the page. */
const TOKEN_FALLBACKS: Readonly<Record<string, string>> = {
	"--dither-paper": "var(--background, #121417)",
	"--dither-ink": "var(--color-accent-ink, #6d6bff)",
};

/**
 * `--chart-1` -> `var(--chart-1)`; a named colour (`blue`, `ink`, …) ->
 * `var(--dither-blue, <fallback>)`; anything else passes through.
 */
export function cssColorExpr(entry: string): string {
	const value = entry.trim();
	const named = ditherColorToken(value);
	if (named) {
		const fallback =
			TOKEN_FALLBACKS[named] ??
			DITHER_COLOR_FALLBACKS[value as keyof typeof DITHER_COLOR_FALLBACKS];
		return fallback ? `var(${named}, ${fallback})` : `var(${named})`;
	}
	if (!value.startsWith("--")) return value;
	const fallback = TOKEN_FALLBACKS[value];
	return fallback ? `var(${value}, ${fallback})` : `var(${value})`;
}

const clamp01 = (n: number) => Math.min(1, Math.max(0, n));

function parseChannel(raw: string): number {
	const text = raw.trim();
	if (text.endsWith("%")) return clamp01(Number.parseFloat(text) / 100);
	return clamp01(Number.parseFloat(text) / 255);
}

function parseAlpha(raw: string | undefined): number {
	if (raw === undefined) return 1;
	const text = raw.trim();
	if (text.endsWith("%")) return clamp01(Number.parseFloat(text) / 100);
	return clamp01(Number.parseFloat(text));
}

/**
 * Parse the formats a browser reports for a resolved colour: `#rgb[a]`,
 * `#rrggbb[aa]`, `rgb()/rgba()` (comma or space syntax) and
 * `color(srgb r g b / a)`. Returns null for anything else (oklch, lab,
 * color-mix...) so the caller can fall back to the canvas probe.
 */
export function parseCssColor(css: string): RGBA | null {
	const text = css.trim().toLowerCase();
	if (text === "transparent") return [0, 0, 0, 0];
	if (text.startsWith("#")) {
		let hex = text.slice(1);
		if (hex.length === 3 || hex.length === 4) {
			hex = [...hex].map((c) => c + c).join("");
		}
		if ((hex.length !== 6 && hex.length !== 8) || /[^0-9a-f]/.test(hex))
			return null;
		const byte = (i: number) => Number.parseInt(hex.slice(i, i + 2), 16) / 255;
		return [byte(0), byte(2), byte(4), hex.length === 8 ? byte(6) : 1];
	}
	// The arguments are cut with `[^)]*` alone and the leading space and `srgb`
	// are dropped after: a `\s*` in front of it would match a run of spaces two ways.
	const fn = /^(rgba?|color)\(([^)]*)\)$/.exec(text);
	if (!fn) return null;
	const isColorFn = fn[1] === "color";
	if (isColorFn && !/^color\(\s*srgb\s/.test(text)) return null;
	const args = (fn[2] ?? "").trimStart().replace(/^srgb\s+/, "");
	const [colour, alpha] = args.split("/");
	const parts = (colour ?? "").split(/[\s,]+/).filter(Boolean);
	if (alpha === undefined && parts.length === 4) {
		const [r, g, b, a] = parts;
		if (r === undefined || g === undefined || b === undefined) return null;
		return isColorFn
			? [
					clamp01(Number(r)),
					clamp01(Number(g)),
					clamp01(Number(b)),
					parseAlpha(a),
				]
			: [parseChannel(r), parseChannel(g), parseChannel(b), parseAlpha(a)];
	}
	if (parts.length !== 3) return null;
	const [r, g, b] = parts;
	if (r === undefined || g === undefined || b === undefined) return null;
	const a = parseAlpha(alpha);
	if (Number.isNaN(a)) return null;
	const rgb: [number, number, number] = isColorFn
		? [clamp01(Number(r)), clamp01(Number(g)), clamp01(Number(b))]
		: [parseChannel(r), parseChannel(g), parseChannel(b)];
	if (rgb.some(Number.isNaN)) return null;
	return [rgb[0], rgb[1], rgb[2], a];
}

/**
 * Reduce a ramp to at most `max` colours, sampled evenly so the first and
 * last colours always survive.
 */
export function limitPalette<T>(colors: readonly T[], max = MAX_PALETTE): T[] {
	if (colors.length <= max) return [...colors];
	const out: T[] = [];
	for (let i = 0; i < max; i++) {
		const index = Math.round((i * (colors.length - 1)) / (max - 1));
		out.push(colors[index] as T);
	}
	return out;
}

export interface PackedPalette {
	/** Number of real colours (2..MAX_PALETTE). */
	count: number;
	/** `MAX_PALETTE * 4` floats, rgba per colour, padded with the last colour. */
	data: Float32Array;
}

/** A one-colour ramp is widened to two so there is always a paper and an ink. */
export function packPalette(
	colors: readonly RGBA[],
	into?: Float32Array,
): PackedPalette {
	let list = limitPalette(colors);
	if (list.length === 0)
		list = [
			[0, 0, 0, 0],
			[1, 1, 1, 1],
		];
	if (list.length === 1) list = [list[0] as RGBA, list[0] as RGBA];
	const data = into ?? new Float32Array(MAX_PALETTE * 4);
	const last = list[list.length - 1] as RGBA;
	for (let i = 0; i < MAX_PALETTE; i++) {
		const c = list[i] ?? last;
		data[i * 4] = c[0];
		data[i * 4 + 1] = c[1];
		data[i * 4 + 2] = c[2];
		data[i * 4 + 3] = c[3];
	}
	return { count: list.length, data };
}

/** Tone steps `DitherShader` uses when `levels` is not given. */
export const DEFAULT_LEVELS = 6;

/** What `DitherShader`'s `levels` prop accepts. */
export type LevelsInput = number | "full" | "stops";

/**
 * Tone steps that land exactly on the palette's stops: `levels - 1` is a
 * multiple of `colours - 1`, so no stray neighbour-colour dots appear at a
 * stop. The smallest such value at or above the default of 6, capped at the
 * tones the matrix can draw (bayer2 draws 5, so 3 colours give 5). When the
 * palette has more stops than the matrix draws tones, the stop count itself
 * is returned. `colours` is clamped to the 2..MAX_PALETTE the shader holds.
 */
export function levelsForPalette(
	colours: number,
	matrix: DitherMatrix,
): number {
	const stops = Math.min(MAX_PALETTE, Math.max(2, Math.floor(colours))) - 1;
	const cap = matrixTones(matrix);
	const start = Math.min(DEFAULT_LEVELS, cap);
	const levels = Math.ceil((start - 1) / stops) * stops + 1;
	if (levels <= cap) return levels;
	const fit = Math.floor((cap - 1) / stops) * stops + 1;
	return fit > 1 ? fit : stops + 1;
}

/**
 * The numeric levels for a `levels` prop: a number as given (default 6),
 * `"full"` every tone the matrix draws, `"stops"` aligned to the palette.
 */
export function resolveLevels(
	levels: LevelsInput | undefined,
	colours: number,
	matrix: DitherMatrix,
): number {
	if (levels === "full") return matrixTones(matrix);
	if (levels === "stops") return levelsForPalette(colours, matrix);
	return levels ?? DEFAULT_LEVELS;
}

/** Flatten a ramp to an `[r, g, b, ...]` array of 0..1 floats (rgb only). */
export function paletteToVec3Array(colors: readonly RGBA[]): number[] {
	const out: number[] = [];
	for (const c of colors) out.push(c[0], c[1], c[2]);
	return out;
}

let probeCtx: CanvasRenderingContext2D | null | undefined;

/** Paint a CSS colour into a 1x1 canvas to read what the browser makes of it. */
function probeColor(css: string): RGBA {
	if (probeCtx === undefined) {
		const canvas = document.createElement("canvas");
		canvas.width = 1;
		canvas.height = 1;
		probeCtx = canvas.getContext("2d", { willReadFrequently: true });
	}
	const ctx = probeCtx;
	if (!ctx) return [0, 0, 0, 1];
	ctx.clearRect(0, 0, 1, 1);
	ctx.fillStyle = "#000";
	ctx.fillStyle = css;
	ctx.fillRect(0, 0, 1, 1);
	const px = ctx.getImageData(0, 0, 1, 1).data;
	return [
		(px[0] ?? 0) / 255,
		(px[1] ?? 0) / 255,
		(px[2] ?? 0) / 255,
		(px[3] ?? 255) / 255,
	];
}

/**
 * Resolve palette entries in the context of `host`, so a variable overridden on
 * a wrapper applies. Browser-only.
 */
export function resolvePaletteColors(
	host: Element,
	palette: readonly string[],
): RGBA[] {
	const probe = document.createElement("span");
	probe.style.cssText =
		"position:absolute;visibility:hidden;pointer-events:none;width:0;height:0";
	host.appendChild(probe);
	try {
		return palette.map((entry) => {
			probe.style.color = "";
			probe.style.color = cssColorExpr(entry);
			const computed = getComputedStyle(probe).color;
			return parseCssColor(computed) ?? probeColor(computed);
		});
	} finally {
		probe.remove();
	}
}

const PLACEHOLDER: readonly RGBA[] = [
	[0, 0, 0, 0],
	[1, 1, 1, 1],
];

/**
 * The palette as RGBA, re-resolved when the entries change or the theme does
 * (class/style/data-theme on <html>).
 */
export function useResolvedPalette(
	hostRef: RefObject<Element | null>,
	palette: readonly string[] | undefined,
): readonly RGBA[] {
	const entries = palette && palette.length > 0 ? palette : DEFAULT_PALETTE;
	const key = entries.join("\u0000");
	const [colors, setColors] = useState<readonly RGBA[]>(PLACEHOLDER);

	// biome-ignore lint/correctness/useExhaustiveDependencies: `key` is the content of `entries`.
	useEffect(() => {
		const host = hostRef.current;
		if (!host) return;
		const resolve = () => setColors(resolvePaletteColors(host, entries));
		resolve();
		const observer = new MutationObserver(resolve);
		observer.observe(document.documentElement, {
			attributes: true,
			attributeFilter: ["class", "style", "data-theme"],
		});
		return () => observer.disconnect();
	}, [hostRef, key]);

	return colors;
}

/**
 * Theme colours for your own canvas work, as `[r, g, b, a]` in 0..1. Default
 * `[ink, paper]`; pass names (`["--chart-1", "blue"]`) for others. `null`
 * before mount; re-resolved when the theme changes.
 */
export function useDitherColors(
	hostRef: RefObject<Element | null>,
	names: readonly string[] = DEFAULT_COLOR_NAMES,
): readonly RGBA[] | null {
	const key = names.join("\u0000");
	const [colors, setColors] = useState<readonly RGBA[] | null>(null);
	// biome-ignore lint/correctness/useExhaustiveDependencies: `key` is the content of `names`.
	useEffect(() => {
		const host = hostRef.current;
		if (!host) return;
		const resolve = () => setColors(resolvePaletteColors(host, names));
		resolve();
		const observer = new MutationObserver(resolve);
		observer.observe(document.documentElement, {
			attributes: true,
			attributeFilter: ["class", "style", "data-theme"],
		});
		return () => observer.disconnect();
	}, [hostRef, key]);
	return colors;
}

const DEFAULT_COLOR_NAMES: readonly string[] = ["ink", "paper"];
