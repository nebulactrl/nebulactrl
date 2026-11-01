/**
 * Pure helpers for the scene engines: glyph grid sizing, the UI-avoidance
 * mask, the narrow-layout decision, ramp selection and the frame gate. No DOM, no GL, so
 * they run under `bun test` and bundle into the standalone pages build.
 */

export type RGB = readonly [number, number, number];

/** Upper bound for cols x rows. The font grows with the viewport to stay under it. */
export const MAX_CELLS = 7500;
/**
 * A cell budget for a canvas of the given CSS size: one cell per `cellArea`
 * CSS px, within [min, MAX_CELLS]. A framed preview can use it so its glyphs stay
 * the size of the full page's instead of the cap packing them smaller.
 */
export function cellsFor(
	widthCss: number,
	heightCss: number,
	cellArea: number,
	min = 200,
): number {
	return Math.min(
		MAX_CELLS,
		Math.max(min, Math.ceil((widthCss * heightCss) / cellArea)),
	);
}

/** Smallest font in CSS px, and the line box that pairs with it. */
export const MIN_FONT_PX = 13;
export const LINE_RATIO = 16 / 13;
/** A cell is about 0.6 em wide: used only to seed the font size guess. */
const CELL_ASPECT = 0.6;

export interface GridSpec {
	/** Canvas size in device px. */
	W: number;
	H: number;
	dpr: number;
	/** Font size in CSS px. */
	fontPx: number;
	/** Cell size in whole device px, so glyphs stay crisp. */
	cw: number;
	ch: number;
	cols: number;
	rows: number;
}

/** The device-pixel ratio actually used: the display's, floored at 1 and capped. */
export function effectiveDpr(deviceDpr: number, cap: number): number {
	return Math.min(deviceDpr > 0 ? deviceDpr : 1, cap);
}

/**
 * Cell grid for a viewport. `advance(fontDevicePx)` is the monospace advance
 * in device px (measured from a canvas in the engine). The font size starts at
 * the closed-form guess for `maxCells` and steps up until the rounded grid
 * really is at or under the cap.
 */
export function computeGrid(
	widthCss: number,
	heightCss: number,
	dpr: number,
	advance: (fontDevicePx: number) => number,
	maxCells = MAX_CELLS,
	minFont = MIN_FONT_PX,
	lineRatio = LINE_RATIO,
): GridSpec {
	const W = Math.max(1, Math.round(widthCss * dpr));
	const H = Math.max(1, Math.round(heightCss * dpr));
	let fontPx = Math.max(
		minFont,
		Math.ceil(
			Math.sqrt((widthCss * heightCss) / (maxCells * CELL_ASPECT * lineRatio)),
		),
	);
	for (;;) {
		const cw = Math.max(4, Math.round(advance(Math.round(fontPx * dpr))));
		const ch = Math.max(4, Math.round(fontPx * lineRatio * dpr));
		const cols = Math.ceil(W / cw);
		const rows = Math.ceil(H / ch);
		// The guard keeps a degenerate advance() from looping forever.
		if (cols * rows <= maxCells || fontPx >= 400) {
			return { W, H, dpr, fontPx, cw, ch, cols, rows };
		}
		fontPx += 1;
	}
}

/* ───────── mask ───────── */

/** A rect, in device px, that dims the field near it. */
export interface MaskRegion {
	x0: number;
	y0: number;
	x1: number;
	y1: number;
	/** Field multiplier inside the rect (after `pad`). 0..1. */
	min: number;
	/** Distance over which the multiplier eases back to 1. */
	feather: number;
	/** Extra distance kept at `min` around the rect. */
	pad: number;
}

/** How a rect dims the field: the multiplier inside, the ease-out distance and the kept margin (CSS px, before `maskScale`). */
export interface MaskKind {
	min: number;
	feather: number;
	pad: number;
}

/** The panel: opaque, so only a soft edge. */
export const PANEL_MASK = { min: 0.8, feather: 40, pad: 0 } as const;
/** Text drawn straight over the field (the wordmark): kept near-black behind it. */
export const QUIET_MASK = { min: 0.07, feather: 70, pad: 30 } as const;

export interface RectLike {
	left: number;
	top: number;
	right: number;
	bottom: number;
}

/**
 * Feather and pad shrink on small viewports, where the 70+30 px
 * around the wordmark would blank the free band the planet has to live in.
 * 1 at 1100 css px and wider.
 */
export function maskScale(widthCss: number): number {
	return Math.min(1, Math.max(0.4, widthCss / 1100));
}

export function regionFromRect(
	r: RectLike,
	dpr: number,
	kind: MaskKind,
	scale = 1,
): MaskRegion {
	return {
		x0: r.left * dpr,
		y0: r.top * dpr,
		x1: r.right * dpr,
		y1: r.bottom * dpr,
		min: kind.min,
		feather: kind.feather * dpr * scale,
		pad: kind.pad * dpr * scale,
	};
}

/** The multiplier at a point (device px): the minimum over every region. */
export function maskAt(
	px: number,
	py: number,
	regions: readonly MaskRegion[],
): number {
	let m = 1;
	for (let k = 0; k < regions.length; k++) {
		const r = regions[k] as MaskRegion;
		const dx = Math.max(r.x0 - px, 0, px - r.x1);
		const dy = Math.max(r.y0 - py, 0, py - r.y1);
		const d = Math.hypot(dx, dy) - r.pad;
		if (d < r.feather) {
			const s = Math.max(d, 0) / r.feather;
			m = Math.min(m, r.min + (1 - r.min) * (s * s * (3 - 2 * s)));
		}
	}
	return m;
}

/**
 * Fills `out` (cols*rows bytes, row-major, row 0 at the top) with the per-cell
 * mask, scaled by `base`.
 */
export function buildMask(
	grid: Pick<GridSpec, "cols" | "rows" | "cw" | "ch">,
	regions: readonly MaskRegion[],
	out: Uint8Array,
	base = 1,
): Uint8Array {
	const { cols, rows, cw, ch } = grid;
	for (let j = 0; j < rows; j++) {
		const py = (j + 0.5) * ch;
		for (let i = 0; i < cols; i++) {
			out[j * cols + i] = Math.round(
				maskAt((i + 0.5) * cw, py, regions) * base * 255,
			);
		}
	}
	return out;
}

/* ───────── orb ───────── */

export interface OrbPlacement {
	/** Centre and radius in device px. */
	cx: number;
	cy: number;
	r: number;
	/** Field multiplier over the whole screen, 0..1. Lower on phones. */
	dim: number;
}

/** Viewports narrower than this (CSS px) have no free side for the planet. */
export const NARROW_BELOW_CSS_PX = 720;

/** The narrow layout's nominal field strength. */
export const NARROW_DIM = 0.45;

/**
 * The Eclipse's layout: wide, or narrow. Wide screens: right of centre.
 * Portrait and phone widths have no free side, so the planet sits under the
 * panel. With no card it falls back to the wide placement. The renderer
 * (`planet.ts`, `planetOrb`) reads only which layout applies (`dim < 1` is
 * narrow) and sizes and places the pixel-dithered planet itself.
 *
 * All inputs are device px. `card` is the panel rect. `narrowBelow` is the
 * CSS width under which the narrow placement applies (default 720); a canvas
 * that is a framed preview rather than a viewport passes 0 so only its aspect
 * decides.
 */
export function orbPlacement(
	W: number,
	H: number,
	dpr: number,
	card: RectLike | null,
	narrowBelow = NARROW_BELOW_CSS_PX,
): OrbPlacement {
	const wide: OrbPlacement = {
		cx: W * 0.77,
		cy: H * 0.45,
		r: Math.min(W, H) * 0.37,
		dim: 1,
	};
	const narrow = W < H * 1.05 || W / dpr < narrowBelow;
	if (!narrow || !card) return wide;
	return {
		cx: W / 2,
		cy: (card.top + card.bottom) / 2,
		r: Math.min(W * 0.46, H * 0.3),
		dim: NARROW_DIM,
	};
}

/* ───────── ramp ───────── */

/**
 * Picks `levels` glyphs (blank first) whose measured ink coverage climbs
 * evenly, so the ramp reads as smooth tone steps. `coverage` is
 * [glyph, ink fraction] for every candidate.
 */
export function selectRamp(
	coverage: readonly (readonly [string, number])[],
	levels: number,
	curve = 1,
): string[] {
	let max = 0;
	for (const [, c] of coverage) max = Math.max(max, c);
	const used = new Set<string>();
	const out: [string, number][] = [[" ", 0]];
	for (let k = 1; k < levels; k++) {
		const target = (k / (levels - 1)) ** curve * max;
		let best: readonly [string, number] | null = null;
		let bestDist = Number.POSITIVE_INFINITY;
		for (const entry of coverage) {
			if (used.has(entry[0])) continue;
			const d = Math.abs(entry[1] - target);
			if (d < bestDist) {
				bestDist = d;
				best = entry;
			}
		}
		if (!best) break;
		used.add(best[0]);
		out.push([best[0], best[1]]);
	}
	out.sort((a, b) => a[1] - b[1]);
	return out.map((e) => e[0]);
}

/**
 * Like `selectRamp`, but every level has `variants` interchangeable glyphs of
 * about the same ink coverage (the nearest unused candidates to the level's
 * target), so a field of one tone still reads as varied glyph noise. Returns
 * level-major, `levels * variants` long: the blank level first (repeated),
 * then each level's variants by ink. A candidate pool too small to fill a
 * level ends the ramp early; a short level repeats its closest glyph. With
 * `variants = 1` it equals `selectRamp`.
 */
export function selectRampVariants(
	coverage: readonly (readonly [string, number])[],
	levels: number,
	variants: number,
	curve = 1,
): string[] {
	let max = 0;
	for (const [, c] of coverage) max = Math.max(max, c);
	const used = new Set<string>();
	const groups: (readonly [string, number])[][] = [];
	for (let k = 1; k < levels; k++) {
		const target = (k / (levels - 1)) ** curve * max;
		const group: (readonly [string, number])[] = [];
		for (let v = 0; v < variants; v++) {
			let best: readonly [string, number] | null = null;
			let bestDist = Number.POSITIVE_INFINITY;
			for (const entry of coverage) {
				if (used.has(entry[0])) continue;
				const d = Math.abs(entry[1] - target);
				if (d < bestDist) {
					bestDist = d;
					best = entry;
				}
			}
			if (!best) break;
			used.add(best[0]);
			group.push(best);
		}
		if (group.length === 0) break;
		while (group.length < variants) group.push(group[0] as (typeof group)[0]);
		group.sort((a, b) => a[1] - b[1]);
		groups.push(group);
	}
	const mean = (g: readonly (readonly [string, number])[]) =>
		g.reduce((s, e) => s + e[1], 0) / g.length;
	groups.sort((a, b) => mean(a) - mean(b));
	const out: string[] = Array.from({ length: variants }, () => " ");
	for (const g of groups) for (const e of g) out.push(e[0]);
	return out;
}

/* ───────── frame gate ───────── */

/**
 * rAF time-gate: draw only once the cap interval has elapsed. 1.5 ms of slack
 * absorbs vsync jitter, which is why 24 fps lands on 20 fps at 60 Hz.
 */
export function dueForFrame(
	now: number,
	lastDraw: number,
	intervalMs: number,
): boolean {
	return now - lastDraw >= intervalMs - 1.5;
}

export const mixRgb = (a: RGB, b: RGB, t: number): RGB => [
	a[0] + (b[0] - a[0]) * t,
	a[1] + (b[1] - a[1]) * t,
	a[2] + (b[2] - a[2]) * t,
];

/* ───────── contrast ───────── */

/** Texture behind text stays under 8% contrast: a WCAG ratio of at most 1.08 against the flat ground. */
export const QUIET_MAX_RATIO = 1.08;

function linear(channel: number): number {
	const c = channel / 255;
	return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

/** WCAG relative luminance of an sRGB colour (0..255 channels). */
export function luminance(c: RGB): number {
	return 0.2126 * linear(c[0]) + 0.7152 * linear(c[1]) + 0.0722 * linear(c[2]);
}

/** WCAG contrast ratio, >= 1. */
export function contrastRatio(a: RGB, b: RGB): number {
	const la = luminance(a);
	const lb = luminance(b);
	return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/**
 * The largest opacity at which a full-coverage `ink` glyph over `ground` still
 * stays within `maxRatio` of the ground (blended in sRGB, as the canvas does).
 * Bisection: contrast grows monotonically with opacity.
 */
export function maxQuietAlpha(
	ink: RGB,
	ground: RGB,
	maxRatio = QUIET_MAX_RATIO,
): number {
	let lo = 0;
	let hi = 1;
	for (let i = 0; i < 24; i++) {
		const mid = (lo + hi) / 2;
		if (contrastRatio(mixRgb(ground, ink, mid), ground) <= maxRatio) lo = mid;
		else hi = mid;
	}
	return lo;
}
