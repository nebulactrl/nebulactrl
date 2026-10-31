import { describe, expect, test } from "bun:test";
import fs from "node:fs";
import path from "node:path";

/**
 * WCAG contrast for the site's colour tokens, read straight from the `@theme` blocks of site.css.
 * Text pairs need 4.5:1 (AA, normal text), and the decorative or boundary colours 3:1 (AA, non-text).
 * A token edit that drops a pair below its floor fails here, so the site cannot lose AA silently.
 */

const css = fs.readFileSync(
	path.resolve(import.meta.dir, "styles/site.css"),
	"utf8",
);

/** Every opaque `--color-*: oklch(L C H)` declared in an `@theme` block. Translucent tokens (`/ alpha`) have no fixed contrast and are skipped. */
function themeColours(source: string): Map<string, [number, number, number]> {
	const colours = new Map<string, [number, number, number]>();
	for (const block of source.matchAll(/@theme[^{]*\{([^}]*)\}/g)) {
		const body = block[1] ?? "";
		for (const decl of body.matchAll(
			/--color-([a-z0-9-]+):\s*oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\)\s*;/g,
		)) {
			colours.set(decl[1] as string, [
				Number(decl[2]),
				Number(decl[3]),
				Number(decl[4]),
			]);
		}
	}
	return colours;
}

/** oklch to linear sRGB (Bjorn Ottosson's matrices), clipped to the gamut. */
function linearRgb([lightness, chroma, hue]: [number, number, number]): [
	number,
	number,
	number,
] {
	const a = chroma * Math.cos((hue * Math.PI) / 180);
	const b = chroma * Math.sin((hue * Math.PI) / 180);
	const l = (lightness + 0.3963377774 * a + 0.2158037573 * b) ** 3;
	const m = (lightness - 0.1055613458 * a - 0.0638541728 * b) ** 3;
	const s = (lightness - 0.0894841775 * a - 1.291485548 * b) ** 3;
	const clip = (x: number) => Math.min(1, Math.max(0, x));
	return [
		clip(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s),
		clip(-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s),
		clip(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s),
	];
}

/** WCAG relative luminance: the weighted sum of the linear channels. */
function luminance(colour: [number, number, number]): number {
	const [r, g, b] = linearRgb(colour);
	return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function ratio(
	a: [number, number, number],
	b: [number, number, number],
): number {
	const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [
		number,
		number,
	];
	return (hi + 0.05) / (lo + 0.05);
}

const colours = themeColours(css);
const WHITE: [number, number, number] = [1, 0, 0];

function token(name: string): [number, number, number] {
	const value = colours.get(name);
	if (!value) throw new Error(`no opaque --color-${name} in a @theme block`);
	return value;
}

/** The grounds text sits on: the page, an inset well, a raised plate, a float, and the pop a nav link hovers to. */
const GROUNDS = [
	"void",
	"surface-well",
	"surface-raised",
	"surface-float",
] as const;
const TEXT_GROUNDS = [...GROUNDS, "surface-pop"] as const;

const AA_TEXT = 4.5;
const AA_NON_TEXT = 3;

describe("colour contrast (WCAG AA)", () => {
	test("the tokens parse out of site.css", () => {
		for (const name of [
			"void",
			"surface-well",
			"surface-raised",
			"surface-float",
			"surface-pop",
			"ink-1",
			"ink-2",
			"ink-3",
			"ink-4",
			"accent-ink",
		]) {
			expect(colours.has(name)).toBe(true);
		}
	});

	// ink-3 serves text under 18px (sidebar labels, card descriptions, captions, meta) and ink-4 the
	// dimmest text left (n/a marks, "and N more", placeholders), so every ink is held to the text floor.
	for (const ink of ["ink-1", "ink-2", "ink-3", "ink-4"]) {
		for (const ground of TEXT_GROUNDS) {
			test(`${ink} on ${ground} is at least ${AA_TEXT}:1`, () => {
				expect(ratio(token(ink), token(ground))).toBeGreaterThanOrEqual(
					AA_TEXT,
				);
			});
		}
	}

	// Links on the docs prose, the FAQ and compare copy: the void, wells (inline code, code blocks) and plates.
	for (const ground of GROUNDS) {
		test(`accent-ink on ${ground} is at least ${AA_TEXT}:1`, () => {
			expect(ratio(token("accent-ink"), token(ground))).toBeGreaterThanOrEqual(
				AA_TEXT,
			);
		});
	}

	// Status colours print as text (marks, tags, pills) on the same grounds.
	for (const name of [
		"ok",
		"warn",
		"bad",
		"status-live-on",
		"status-building-on",
		"status-failed-on",
		"status-staged-on",
		"warning-text",
	]) {
		for (const ground of GROUNDS) {
			test(`${name} on ${ground} is at least ${AA_TEXT}:1`, () => {
				expect(ratio(token(name), token(ground))).toBeGreaterThanOrEqual(
					AA_TEXT,
				);
			});
		}
	}

	// btn-primary prints white on a gradient from fill-top to fill-bot.
	for (const stop of ["accent-fill-top", "accent-fill-bot"]) {
		test(`white on ${stop} is at least ${AA_TEXT}:1`, () => {
			expect(ratio(WHITE, token(stop))).toBeGreaterThanOrEqual(AA_TEXT);
		});
	}

	// Non-text: the brand mark and the control outline must be seen against the void and a well.
	for (const name of ["accent-brand", "line-control"]) {
		for (const ground of ["void", "surface-well"]) {
			test(`${name} on ${ground} is at least ${AA_NON_TEXT}:1`, () => {
				expect(ratio(token(name), token(ground))).toBeGreaterThanOrEqual(
					AA_NON_TEXT,
				);
			});
		}
	}
});

/**
 * The halftone (packages/tokens/src/halftone.css) under the site's text: the dawn at the top of the
 * docs, compare, FAQ and legal pages, and the arc under the hero's scene and the 404's rim.
 * They all draw the brightest dot as white through the `--ambient-ink` filter chain, screened onto
 * the void at `--ambient-alpha-page`. Texture behind text stays under 8% contrast, and `ink-3`
 * stays at least 7:1 on that dot.
 */
const halftoneCss = fs.readFileSync(
	path.resolve(import.meta.dir, "../../../packages/tokens/src/halftone.css"),
	"utf8",
);

type Rgb = [number, number, number];

/** The one-line filter chain of `--ambient-ink`: brightness, sepia, saturate and hue-rotate only. */
function ambientInk(source: string): string[] {
	const chain = /--ambient-ink:\s*([^;]+);/.exec(source)?.[1];
	if (!chain) throw new Error("no --ambient-ink in halftone.css");
	return [...chain.matchAll(/([a-z-]+)\(([-\d.]+)(?:deg)?\)/g)].map(
		(m) => `${m[1]} ${m[2]}`,
	);
}

/** CSS filter functions on an sRGB colour (0 to 1 per channel), as the Filter Effects spec writes them. */
function applyFilter(rgb: Rgb, step: string): Rgb {
	const [fn, raw] = step.split(" ") as [string, string];
	const x = Number(raw);
	const [r, g, b] = rgb;
	const clip = (v: number) => Math.min(1, Math.max(0, v));
	const matrix = (m: number[]): Rgb => [
		clip((m[0] ?? 0) * r + (m[1] ?? 0) * g + (m[2] ?? 0) * b),
		clip((m[3] ?? 0) * r + (m[4] ?? 0) * g + (m[5] ?? 0) * b),
		clip((m[6] ?? 0) * r + (m[7] ?? 0) * g + (m[8] ?? 0) * b),
	];
	if (fn === "brightness") return [clip(r * x), clip(g * x), clip(b * x)];
	if (fn === "sepia") {
		const k = 1 - x;
		return matrix([
			0.393 + 0.607 * k,
			0.769 - 0.769 * k,
			0.189 - 0.189 * k,
			0.349 - 0.349 * k,
			0.686 + 0.314 * k,
			0.168 - 0.168 * k,
			0.272 - 0.272 * k,
			0.534 - 0.534 * k,
			0.131 + 0.869 * k,
		]);
	}
	if (fn === "saturate") {
		return matrix([
			0.213 + 0.787 * x,
			0.715 - 0.715 * x,
			0.072 - 0.072 * x,
			0.213 - 0.213 * x,
			0.715 + 0.285 * x,
			0.072 - 0.072 * x,
			0.213 - 0.213 * x,
			0.715 - 0.715 * x,
			0.072 + 0.928 * x,
		]);
	}
	if (fn === "hue-rotate") {
		const c = Math.cos((x * Math.PI) / 180);
		const s = Math.sin((x * Math.PI) / 180);
		return matrix([
			0.213 + c * 0.787 - s * 0.213,
			0.715 - c * 0.715 - s * 0.715,
			0.072 - c * 0.072 + s * 0.928,
			0.213 - c * 0.213 + s * 0.143,
			0.715 + c * 0.285 + s * 0.14,
			0.072 - c * 0.072 - s * 0.283,
			0.213 - c * 0.213 - s * 0.787,
			0.715 - c * 0.715 + s * 0.715,
			0.072 + c * 0.928 + s * 0.072,
		]);
	}
	throw new Error(`unsupported filter ${fn}`);
}

const toSrgb = (linear: number) =>
	linear <= 0.0031308 ? 12.92 * linear : 1.055 * linear ** (1 / 2.4) - 0.055;
const toLinear = (srgb: number) =>
	srgb <= 0.04045 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4;
const luminanceOfSrgb = ([r, g, b]: Rgb) =>
	0.2126 * toLinear(r) + 0.7152 * toLinear(g) + 0.0722 * toLinear(b);

/** The brightest dot of a halftone host over `ground`: white through the ink filter, screened at `alpha` (in sRGB, as the browser blends). */
function brightestDot(ground: Rgb, alpha: number): Rgb {
	let dot: Rgb = [1, 1, 1];
	for (const step of ambientInk(halftoneCss)) dot = applyFilter(dot, step);
	return ground.map((g, i) => {
		const screened = 1 - (1 - g) * (1 - (dot[i] ?? 0));
		return g * (1 - alpha) + screened * alpha;
	}) as Rgb;
}

/** `--ambient-alpha-page` with the site's `--ambient-strength` (site.css overrides the app's 0.35). */
function pageAlpha(): number {
	const strength = Number(
		/--ambient-strength:\s*([\d.]+);/.exec(css)?.[1] ??
			/--ambient-strength:\s*([\d.]+);/.exec(halftoneCss)?.[1],
	);
	const factor = Number(
		/--ambient-alpha-page:\s*calc\(([\d.]+)\s*\*\s*var\(--ambient-strength\)\)/.exec(
			halftoneCss,
		)?.[1],
	);
	if (!(strength > 0) || !(factor > 0)) throw new Error("no ambient alpha");
	return strength * factor;
}

const sRGBOf = (name: string): Rgb => linearRgb(token(name)).map(toSrgb) as Rgb;

describe("halftone under text", () => {
	const alpha = pageAlpha();
	const dot = brightestDot(sRGBOf("void"), alpha);
	const dotLuminance = luminanceOfSrgb(dot);
	const voidLuminance = luminance(token("void"));

	test("the filter chain parses to the four steps of the nebula ink", () => {
		expect(ambientInk(halftoneCss).map((s) => s.split(" ")[0])).toEqual([
			"brightness",
			"sepia",
			"saturate",
			"hue-rotate",
		]);
	});

	// Texture contrast: the brightest dot against the void it sits on, as the difference over the sum of the
	// two luminances (each with the 0.05 flare of the WCAG ratio). Under 8% behind text.
	test("the brightest dot is under 8% contrast on the void", () => {
		const hi = dotLuminance + 0.05;
		const lo = voidLuminance + 0.05;
		expect((hi - lo) / (hi + lo)).toBeLessThan(0.08);
	});

	// The dot is brighter than the void, so a text colour paler than both has to clear it.
	for (const [ink, floor] of [
		["ink-3", 7],
		["ink-4", AA_TEXT],
	] as const) {
		test(`${ink} on the brightest dot is at least ${floor}:1`, () => {
			expect(
				(luminance(token(ink)) + 0.05) / (dotLuminance + 0.05),
			).toBeGreaterThanOrEqual(floor);
		});
	}

	test("forced colours hide every halftone layer, hosts and the shared hook alike", () => {
		const forced =
			/@media \(forced-colors: active\) \{\s*([^{]+)::before \{\s*display: none;/.exec(
				halftoneCss,
			)?.[1];
		expect(forced).toBeDefined();
		for (const host of [
			".canvas-floor",
			".bg-ambient",
			".ht-dawn",
			".ht-arc",
			".ht-foot",
			".ht-sphere",
			".ht-layer",
		])
			expect(forced).toContain(host);
	});
});
