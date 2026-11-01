import { useEffect, useState } from "react";
import {
	DITHER_COLOR_FALLBACKS,
	DITHER_COLOR_NAMES,
	type DitherColorName,
} from "./colors";

export {
	DITHER_COLOR_NAMES,
	type DitherBaseName,
	type DitherColorName,
	ditherColorToken,
	ditherColorVar,
	isDitherColorName,
} from "./colors";

/**
 * Theme access for dither drawing that cannot use `var(--x)` directly:
 * canvas, WebGL uniforms and the pure pattern builders. SVG and CSS
 * consumers should keep passing `var(--dither-…)` strings; the browser
 * resolves them for free.
 */

export type RGB = [number, number, number];

export interface DitherPalette {
	ink: string;
	paper: string;
	accent: string;
	muted: string;
	status: {
		live: string;
		building: string;
		deploying: string;
		failed: string;
		warning: string;
		info: string;
		stopped: string;
	};
	/** At least six chart series colours, from the `--dither-series-*` tokens. */
	series: string[];
	/** The named colours (`blue … grey`) resolved to `#rrggbb`. */
	named: Record<DitherColorName, string>;
}

/** Where each palette entry comes from, with a last-resort default. */
const SOURCES = {
	ink: ["--dither-ink", "#9aa4ff"],
	paper: ["--dither-paper", "#1a1d20"],
	accent: ["--color-accent-brand", "#6d6bff"],
	muted: ["--color-ink-4", "#959ca7"],
	live: ["--color-status-live", "#41d686"],
	building: ["--color-status-building", "#38c5ff"],
	deploying: ["--color-status-deploying", "#9aa4ff"],
	failed: ["--color-status-failed", "#ff7d7c"],
	warning: ["--color-warn", "#e6ad00"],
	info: ["--color-info", "#38c5ff"],
	stopped: ["--color-status-stopped", "#7a818d"],
} as const;

const SERIES = [
	["--dither-series-1", "#9aa4ff"],
	["--dither-series-2", "#38c5ff"],
	["--dither-series-3", "#41d686"],
	["--dither-series-4", "#e6ad00"],
	["--dither-series-5", "#ff7d7c"],
	["--dither-series-6", "#c58bff"],
	["--dither-series-7", "#ff9f6b"],
	["--dither-series-8", "#7a818d"],
] as const;

const rgbCache = new Map<string, RGB>();
let probe: CanvasRenderingContext2D | null | undefined;

function probeContext(): CanvasRenderingContext2D | null {
	if (probe !== undefined) return probe;
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
	return probe;
}

const HEX = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i;

/**
 * A CSS colour as 0–255 sRGB channels. Hex parses directly; everything else
 * (oklch, color-mix, named colours) is painted once into a 1×1 canvas and
 * read back. Cached per input string. Unknown input reads as mid grey.
 */
export function toRGB(css: string): RGB {
	const cached = rgbCache.get(css);
	if (cached) return cached;
	let rgb: RGB = [128, 128, 128];
	const hex = HEX.exec(css.trim());
	if (hex?.[1]) {
		const h = hex[1];
		const full = h.length === 3 ? [...h].map((c) => c + c).join("") : h;
		rgb = [
			Number.parseInt(full.slice(0, 2), 16),
			Number.parseInt(full.slice(2, 4), 16),
			Number.parseInt(full.slice(4, 6), 16),
		];
	} else {
		const ctx = probeContext();
		if (ctx) {
			ctx.clearRect(0, 0, 1, 1);
			// An invalid fillStyle is ignored, which leaves the previous colour:
			// reset to a known one so a bad input reads as the grey default.
			ctx.fillStyle = "#808080";
			ctx.fillStyle = css;
			ctx.fillRect(0, 0, 1, 1);
			const data = ctx.getImageData(0, 0, 1, 1).data;
			rgb = [data[0] ?? 128, data[1] ?? 128, data[2] ?? 128];
		}
	}
	rgbCache.set(css, rgb);
	return rgb;
}

const toHex = ([r, g, b]: RGB) =>
	`#${[r, g, b].map((c) => c.toString(16).padStart(2, "0")).join("")}`;

/**
 * Resolve a custom property on `el` to a concrete `#rrggbb`. The browser
 * computes `var()` and `color-mix()` on a throwaway child; the result goes
 * through `toRGB` so oklch survives.
 */
function resolveVar(el: Element, name: string, fallback: string): string {
	if (typeof document === "undefined") return fallback;
	const raw = getComputedStyle(el).getPropertyValue(name).trim();
	if (raw === "") return fallback;
	const span = document.createElement("span");
	span.style.color = raw;
	span.style.display = "none";
	el.appendChild(span);
	const computed = getComputedStyle(span).color;
	span.remove();
	return toHex(toRGB(computed || raw));
}

function namedColors(
	pick: (source: readonly [string, string]) => string,
): Record<DitherColorName, string> {
	return Object.fromEntries(
		DITHER_COLOR_NAMES.map((name) => [
			name,
			pick([`--dither-${name}`, DITHER_COLOR_FALLBACKS[name]]),
		]),
	) as Record<DitherColorName, string>;
}

/** Read the dither palette from the theme tokens in effect at `el` (default: `<html>`). */
export function readDitherPalette(
	el: Element | undefined = typeof document === "undefined"
		? undefined
		: document.documentElement,
): DitherPalette {
	if (!el) {
		return {
			ink: SOURCES.ink[1],
			paper: SOURCES.paper[1],
			accent: SOURCES.accent[1],
			muted: SOURCES.muted[1],
			status: {
				live: SOURCES.live[1],
				building: SOURCES.building[1],
				deploying: SOURCES.deploying[1],
				failed: SOURCES.failed[1],
				warning: SOURCES.warning[1],
				info: SOURCES.info[1],
				stopped: SOURCES.stopped[1],
			},
			series: SERIES.map(([, fallback]) => fallback),
			named: namedColors(([, fallback]) => fallback),
		};
	}
	const pick = ([name, fallback]: readonly [string, string]) =>
		resolveVar(el, name, fallback);
	return {
		ink: pick(SOURCES.ink),
		paper: pick(SOURCES.paper),
		accent: pick(SOURCES.accent),
		muted: pick(SOURCES.muted),
		status: {
			live: pick(SOURCES.live),
			building: pick(SOURCES.building),
			deploying: pick(SOURCES.deploying),
			failed: pick(SOURCES.failed),
			warning: pick(SOURCES.warning),
			info: pick(SOURCES.info),
			stopped: pick(SOURCES.stopped),
		},
		series: SERIES.map(pick),
		named: namedColors(pick),
	};
}

const sameColors = (a: DitherPalette, b: DitherPalette) =>
	JSON.stringify(a) === JSON.stringify(b);

/**
 * The palette, read on mount and again whenever `<html>`'s class or inline
 * style changes (a theme switch or a token override). Components redraw only
 * when a colour actually changed.
 */
export function useDitherPalette(): DitherPalette {
	const [palette, setPalette] = useState(() => readDitherPalette());
	useEffect(() => {
		const root = document.documentElement;
		const refresh = () => {
			const next = readDitherPalette(root);
			setPalette((current) => (sameColors(current, next) ? current : next));
		};
		refresh();
		const observer = new MutationObserver(refresh);
		observer.observe(root, {
			attributes: true,
			attributeFilter: ["class", "style", "data-theme"],
		});
		return () => observer.disconnect();
	}, []);
	return palette;
}
