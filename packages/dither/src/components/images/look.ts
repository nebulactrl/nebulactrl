/*
 * The preset recipes (method, cell, contrast and the newsprint, gameboy and
 * riso colours) are adapted from dithercn.
 * Copyright (c) 2026 Leo
 * Copyright (c) 2026 Shadcn Labs (startercn, which this project is based on)
 * Licensed under the MIT License: https://github.com/opencoredev/dithercn/blob/main/LICENSE
 * (the full permission notice is reproduced in the repository NOTICE)
 * Portions adapted from https://github.com/opencoredev/dithercn
 */

import type { BayerSize } from "../../lib/bayer";
import {
	type DitherBaseName,
	type DitherColorName,
	type DitherPalette,
	ditherColorToken,
	ditherColorVar,
	type RGB,
	toRGB,
} from "../../lib/tokens";
import {
	DEFAULT_RASTER_OPTIONS,
	type DitherAlgorithm,
	type DitherMethod,
	luminance,
	methodToAlgorithm,
	type PaletteMode,
	type RasterOptions,
} from "./algorithms";
import { MIN_COLUMNS } from "./grid";

/** A palette name (`blue`, `ink`, …), a `--custom-property` or any CSS colour, `transparent` included. */
export type DitherColor = DitherColorName | DitherBaseName | (string & {});

/**
 * A palette is a named recipe over the theme tokens, or an explicit list of
 * two or more colours. Entries may be a palette name (`blue`, `ink`, `paper`),
 * `accent`, `muted`, a `--custom-property`, `transparent` or any CSS colour.
 * Lists are sorted dark to light unless `sortPalette` is `false`, so dark parts
 * of the image take the darkest colour in both themes.
 *
 * - `mono`: paper → ink
 * - `duotone`: paper → accent
 * - `tritone`: paper → accent → ink
 */
export type PaletteSpec = "mono" | "duotone" | "tritone" | readonly string[];

/** The looks `DitherImage` ships with. */
export const DITHER_IMAGE_PRESETS = [
	"ink",
	"duotone",
	"newsprint",
	"gameboy",
	"riso",
] as const;
export type DitherImagePreset = (typeof DITHER_IMAGE_PRESETS)[number];

/** What a preset decides. Every field can be overridden by a prop. */
export interface PresetRecipe {
	method: DitherMethod;
	/** Cell size in CSS px. */
	cell: number;
	contrast: number;
	/** The palette entries, given the `color` prop. */
	palette: (color: DitherColor) => readonly string[];
}

/** A share of `color` mixed into the page colour, for in-between tones. */
const tint = (color: DitherColor, share: number) =>
	`color-mix(in oklch, ${ditherColorVar(color)} ${share}%, var(--dither-paper))`;

const NEWSPRINT = ["oklch(0.22 0.012 70)", "oklch(0.93 0.022 88)"] as const;

const GAMEBOY = [
	"oklch(0.27 0.06 145)",
	"oklch(0.43 0.09 140)",
	"oklch(0.68 0.15 125)",
	"oklch(0.75 0.16 122)",
] as const;

const RISO = [
	"oklch(0.3 0.1 268)",
	"oklch(0.56 0.2 262)",
	"oklch(0.73 0.17 2)",
	"oklch(0.95 0.02 85)",
] as const;

export const IMAGE_PRESETS: Record<DitherImagePreset, PresetRecipe> = {
	// The page colour, a tint and one ink, in blue noise. Follows the theme.
	duotone: {
		method: "blue-noise",
		cell: 2,
		contrast: 1.1,
		palette: (color) => ["--dither-paper", tint(color, 42), color],
	},
	// Four greens on a 3px Bayer grid.
	gameboy: {
		method: "bayer4",
		cell: 3,
		contrast: 1.15,
		palette: () => GAMEBOY,
	},
	// Atkinson diffusion, one ink on the page colour. Follows the theme.
	ink: {
		method: "atkinson",
		cell: 2,
		contrast: 1,
		palette: (color) => ["--dither-paper", color],
	},
	// An ordered 8×8 screen, near black on warm paper, in both themes.
	newsprint: {
		method: "bayer8",
		cell: 2,
		contrast: 1.25,
		palette: () => NEWSPRINT,
	},
	// Navy, blue and pink on cream, like a two-drum print.
	riso: {
		method: "atkinson",
		cell: 2,
		contrast: 1.15,
		palette: () => RISO,
	},
};

/** Everything about how an image is dithered, shared by every image component. */
export interface DitherLook {
	/** A starting look: method, cell, contrast and palette. Default `duotone`. */
	preset?: DitherImagePreset | undefined;
	/** The ink of the `ink` and `duotone` presets: a palette name or any CSS colour. Default `ink`. */
	color?: DitherColor | undefined;
	/** The dither algorithm. Wins over `algorithm` and the preset's method. */
	method?: DitherMethod | undefined;
	/** The same choice as `method`, by algorithm name; `bayer` reads `bayerSize`. */
	algorithm?: DitherAlgorithm | undefined;
	/** Matrix size for `algorithm: "bayer"`. */
	bayerSize?: BayerSize | undefined;
	/** Two or more colours (or a named recipe) that replace the preset's. */
	palette?: PaletteSpec | undefined;
	/** Sort the palette dark to light. Default true; `false` keeps your order. */
	sortPalette?: boolean | undefined;
	/** `tone` (default) treats the palette as a ramp; `color` snaps to the nearest colour. */
	mode?: PaletteMode | undefined;
	/**
	 * Cell edge in CSS px, rounded to whole device pixels. Default: the preset's
	 * (2 or 3), shrinking towards 1 to keep about 200 columns on a small box.
	 */
	cell?: number | undefined;
	/** 1 leaves contrast alone. Default: the preset's. */
	contrast?: number | undefined;
	/** Added to every tone, -1..1. Default 0. */
	brightness?: number | undefined;
	/** Swap light and dark. */
	invert?: boolean | undefined;
	/** Stretch tones to the full range before dithering. Default true. */
	normalize?: boolean | undefined;
	/** Edge sharpening before dithering, 0..1. Default 0.3. */
	sharpen?: number | undefined;
	/** How the image fills its box. Default `cover`. */
	fit?: "cover" | "contain" | undefined;
}

export interface ResolvedLook {
	raster: RasterOptions;
	/** CSS px. `undefined` only when a caller bypasses `resolveLook`. */
	cell: number | undefined;
	/** Fewest columns before the cell shrinks below `cell`; 0 when `cell` was set. */
	minColumns: number;
	fit: "cover" | "contain";
}

export type Rgba = readonly [number, number, number, number];

const BASE_KEYWORDS = ["accent", "muted"] as const;

const NAMED_FALLBACK = (palette: DitherPalette, name: string): string => {
	if (name === "ink") return palette.ink;
	if (name === "paper") return palette.paper;
	return (palette.named as Record<string, string> | undefined)?.[name] ?? "";
};

let probe: CanvasRenderingContext2D | null | undefined;

/** A CSS colour as RGBA through a 1×1 canvas, which understands oklch and color-mix. */
function probeRgba(css: string): Rgba {
	if (probe === undefined) {
		probe =
			typeof document === "undefined"
				? null
				: (document.createElement("canvas").getContext("2d", {
						willReadFrequently: true,
					}) ?? null);
		if (probe) {
			probe.canvas.width = 1;
			probe.canvas.height = 1;
		}
	}
	if (!probe) {
		const [r, g, b] = toRGB(css);
		return [r, g, b, 1];
	}
	probe.clearRect(0, 0, 1, 1);
	probe.fillStyle = "#808080";
	probe.fillStyle = css;
	probe.fillRect(0, 0, 1, 1);
	const data = probe.getImageData(0, 0, 1, 1).data;
	return [
		data[0] ?? 128,
		data[1] ?? 128,
		data[2] ?? 128,
		(data[3] ?? 255) / 255,
	];
}

const caches = new WeakMap<
	DitherPalette,
	WeakMap<Element, Map<string, Rgba>>
>();

function cacheFor(palette: DitherPalette, el: Element): Map<string, Rgba> {
	let byElement = caches.get(palette);
	if (!byElement) {
		byElement = new WeakMap();
		caches.set(palette, byElement);
	}
	let entries = byElement.get(el);
	if (!entries) {
		entries = new Map();
		byElement.set(el, entries);
	}
	return entries;
}

/** Resolve a CSS value (custom properties, color-mix, …) as computed on `el`. */
function computedRgba(css: string, el: Element): Rgba {
	const span = document.createElement("span");
	span.style.color = css;
	span.style.display = "none";
	el.appendChild(span);
	const computed = getComputedStyle(span).color;
	span.remove();
	return probeRgba(computed || css);
}

/**
 * One palette entry as RGBA (0..255 channels, alpha 0..1). Palette names,
 * `--custom-properties` and `var()`/`color-mix()` resolve against `el` (default
 * `<html>`), so a wrapper that overrides `--dither-paper` retints its own
 * images. Without a DOM, only names, `accent`, `muted`, `transparent` and hex
 * resolve; anything else reads as mid grey.
 */
export function resolveRgba(
	entry: string,
	palette: DitherPalette,
	el?: Element,
): Rgba {
	const key = entry.trim();
	if ((BASE_KEYWORDS as readonly string[]).includes(key)) {
		const [r, g, b] = toRGB(palette[key as (typeof BASE_KEYWORDS)[number]]);
		return [r, g, b, 1];
	}
	if (key === "transparent") return [0, 0, 0, 0];
	const token = ditherColorToken(key);
	if (typeof document === "undefined") {
		const fallback = token ? NAMED_FALLBACK(palette, key) : key;
		const [r, g, b] = toRGB(fallback);
		return [r, g, b, 1];
	}
	const scope = el ?? document.documentElement;
	const cache = cacheFor(palette, scope);
	const hit = cache.get(key);
	if (hit) return hit;
	let resolved: Rgba;
	if (token && !getComputedStyle(scope).getPropertyValue(token).trim()) {
		// The page does not define the token: use the engine's own value.
		const [r, g, b] = toRGB(NAMED_FALLBACK(palette, key));
		resolved = [r, g, b, 1];
	} else {
		resolved = computedRgba(ditherColorVar(key), scope);
	}
	cache.set(key, resolved);
	return resolved;
}

/** One palette entry as RGB (alpha dropped). `ink`, `paper`, `accent`, `muted`, names, `--tokens`, CSS. */
export function resolveColor(
	entry: string,
	palette: DitherPalette,
	el?: Element,
): RGB {
	const [r, g, b] = resolveRgba(entry, palette, el);
	return [r, g, b];
}

/** The entries of a palette spec, before they are resolved. */
export function paletteEntries(spec: PaletteSpec): readonly string[] {
	return spec === "mono"
		? ["paper", "ink"]
		: spec === "duotone"
			? ["paper", "accent"]
			: spec === "tritone"
				? ["paper", "accent", "ink"]
				: spec;
}

/** Colours sorted darkest first (stable), so dark image tones take the darkest. */
export function sortByLuminance<T extends readonly number[]>(
	colors: readonly T[],
): T[] {
	return colors
		.map((color, index) => ({ color, index }))
		.sort(
			(a, b) =>
				luminance(a.color[0] ?? 0, a.color[1] ?? 0, a.color[2] ?? 0) -
					luminance(b.color[0] ?? 0, b.color[1] ?? 0, b.color[2] ?? 0) ||
				a.index - b.index,
		)
		.map(({ color }) => color);
}

export function resolveLook(
	look: DitherLook,
	palette: DitherPalette,
	el?: Element,
): ResolvedLook {
	const recipe = IMAGE_PRESETS[look.preset ?? "duotone"];
	const entries =
		look.palette === undefined
			? recipe.palette(look.color ?? "ink")
			: paletteEntries(look.palette);
	const resolved = entries.map((entry) => resolveRgba(entry, palette, el));
	const colors =
		look.sortPalette === false ? resolved : sortByLuminance(resolved);
	const alphas = colors.map((color) => color[3]);
	const picked = look.method
		? methodToAlgorithm(look.method)
		: look.algorithm
			? { algorithm: look.algorithm, bayerSize: look.bayerSize ?? 4 }
			: methodToAlgorithm(recipe.method);
	return {
		raster: {
			algorithm: picked.algorithm,
			bayerSize: picked.bayerSize,
			palette: colors.map(([r, g, b]): RGB => [r, g, b]),
			paletteAlpha: alphas.some((alpha) => alpha < 1) ? alphas : undefined,
			mode: look.mode ?? "tone",
			contrast: look.contrast ?? recipe.contrast,
			brightness: look.brightness ?? DEFAULT_RASTER_OPTIONS.brightness,
			invert: look.invert ?? false,
			normalize: look.normalize ?? true,
			sharpen: look.sharpen ?? 0.3,
			serpentine: DEFAULT_RASTER_OPTIONS.serpentine,
		},
		cell: look.cell ?? recipe.cell,
		minColumns: look.cell === undefined ? MIN_COLUMNS : 0,
		fit: look.fit ?? "cover",
	};
}
