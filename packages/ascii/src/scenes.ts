/**
 * The closed set of scenes and the pure maths that picks and places them.
 * Four are glyph scenes on one engine (`core.ts`): a spec (this file) plus a
 * field shader (`*.glsl.ts`). The Eclipse is the fifth and is not a glyph
 * scene: `planet.ts` draws it as a dithered planet and reads only its `fps`,
 * `panel`, `quiet` and `staticTime` from here, plus its placement.
 *
 * Shared by every glyph scene: the palette (slate ink plus the accent, never a
 * second hue), the 7.5k-cell budget, the 24 fps ceiling, one still frame for
 * reduced motion, pause when hidden or off screen, context-loss recovery.
 * Glyphs are punctuation and symbols only: a scene never spells a word or a
 * number.
 */
import {
	type MaskKind,
	maskScale,
	NARROW_BELOW_CSS_PX,
	type OrbPlacement,
	orbPlacement,
	PANEL_MASK,
	QUIET_MASK,
	type RectLike,
} from "./layout";

export const SCENE_NAMES = [
	"eclipse",
	"dial-out",
	"lost-signal",
	"dome",
	"band",
] as const;
export type SceneName = (typeof SCENE_NAMES)[number];

export const DEFAULT_SCENE: SceneName = "eclipse";

/** No glyph scene draws faster than this (fps). */
export const MAX_FPS = 24;

export interface SceneSpec {
	readonly name: SceneName;
	/** Frame cap the scene runs at unless the caller asks for less. Glyph scenes: <= MAX_FPS. The Eclipse: 30. */
	readonly fps: number;
	/** Glyphs the ramp is picked from, by measured ink coverage in the loaded font. */
	readonly candidates: string;
	/** Tone levels, blank included. */
	readonly levels: number;
	/** Interchangeable glyphs per level (1 = one glyph per tone). */
	readonly variants: number;
	/** Fixed-tone symbols after the ramp, addressed by the field's glyph select (128 + index). */
	readonly extras: string;
	/** Their tone, on the same dim-to-ink curve as the ramp (0..1, the ramp's own curve is not applied). */
	readonly extraTone: number;
	/** Exponent of the dim-to-ink tone curve. */
	readonly gamma: number;
	/** Field luminance under which a cell stays blank. */
	readonly cutoff: number;
	/** Bayer dither strength between two adjacent levels. */
	readonly dither: number;
	/**
	 * How `maskRect` and `quietRects` dim the field. "level" lowers the cell's
	 * tone. "alpha" keeps the glyph and lowers its opacity,
	 * which is what holds text-safe contrast: tone steps never get that faint.
	 */
	readonly maskMode: "level" | "alpha";
	/** `maskRect`: the opaque panel over the field. */
	readonly panel: MaskKind;
	/** `quietRects`: text drawn straight over the field. */
	readonly quiet: MaskKind;
	/** Shader time (s) painted for reduced motion. */
	readonly staticTime: number;
}

/** Symbols only: no letters, no digits. */
const SYMBOLS = ".'`\":;,_-~^+=*<>|/\\()[]{}#";

/** The dome turns by this much a second (three degrees a minute), in radians. */
export const DOME_OMEGA = 0.00087;
/** Spacing of the dome's arcs, in rows. */
export const DOME_GAP_ROWS = 2.4;

/** The quiet region of the alpha scenes: glyphs stay but fall to 5% opacity. */
const QUIET_ALPHA: MaskKind = { min: 0.05, feather: 90, pad: 24 };

export const SCENES: Readonly<Record<SceneName, SceneSpec>> = {
	/** Drawn by planet.ts: no glyphs, so the glyph fields are inert and kept neutral. */
	eclipse: {
		name: "eclipse",
		fps: 30,
		candidates: "",
		levels: 1,
		variants: 1,
		extras: "",
		extraTone: 0,
		gamma: 1,
		cutoff: 0,
		dither: 0,
		maskMode: "level",
		panel: PANEL_MASK,
		quiet: QUIET_MASK,
		staticTime: 14,
	},
	"dial-out": {
		name: "dial-out",
		fps: 20,
		candidates: ".':-=+*",
		levels: 8,
		variants: 1,
		extras: "",
		extraTone: 0.45,
		gamma: 0.55,
		cutoff: 0.04,
		dither: 0.5,
		maskMode: "alpha",
		panel: PANEL_MASK,
		quiet: QUIET_ALPHA,
		staticTime: 32,
	},
	"lost-signal": {
		name: "lost-signal",
		fps: 12,
		candidates: SYMBOLS,
		levels: 7,
		variants: 3,
		extras: "",
		extraTone: 0.45,
		gamma: 0.8,
		cutoff: 0.04,
		dither: 0.4,
		maskMode: "alpha",
		panel: PANEL_MASK,
		quiet: QUIET_ALPHA,
		staticTime: 9,
	},
	dome: {
		name: "dome",
		fps: 5,
		candidates: ".':+*",
		levels: 6,
		variants: 1,
		extras: "_",
		extraTone: 0.5,
		gamma: 0.5,
		cutoff: 0.05,
		dither: 0.3,
		maskMode: "alpha",
		panel: PANEL_MASK,
		quiet: QUIET_ALPHA,
		staticTime: 40,
	},
	band: {
		name: "band",
		fps: 10,
		candidates: ".,':+*",
		levels: 7,
		variants: 1,
		extras: ".",
		extraTone: 0.95,
		gamma: 0.5,
		cutoff: 0.04,
		dither: 0.3,
		maskMode: "alpha",
		panel: PANEL_MASK,
		quiet: QUIET_ALPHA,
		staticTime: 20,
	},
};

/** The scene named, or the Eclipse for `undefined` or an unknown name. */
export function resolveScene(name: string | undefined): SceneSpec {
	return name !== undefined && Object.hasOwn(SCENES, name)
		? SCENES[name as SceneName]
		: SCENES[DEFAULT_SCENE];
}

/**
 * The frame cap in effect: the caller's (or the scene's), never above the
 * scene's own ceiling (24, or its rate when that is higher: the Eclipse's 30)
 * and never under 1.
 */
export function sceneFps(spec: SceneSpec, requested?: number): number {
	const fps = requested ?? spec.fps;
	const ceiling = Math.max(MAX_FPS, spec.fps);
	return Math.min(ceiling, Math.max(1, Number.isFinite(fps) ? fps : spec.fps));
}

/** Total atlas columns: every ramp glyph (levels x variants) then the extras. */
export function atlasColumns(spec: SceneSpec): number {
	return spec.levels * spec.variants + spec.extras.length;
}

/* ───────── placement ───────── */

/** Where the dial-out signals converge, as fractions of the canvas (a dome's hero star, too). */
export interface FocusPoint {
	x: number;
	y: number;
}

/**
 * The lost signal's gap drifts by up to this fraction of the canvas's shorter
 * side, and the gap is cut that much larger than the content it frames, so the
 * content never leaves it.
 */
export const GAP_DRIFT = 0.09;
/** The gap's soft edge, as a fraction of its own size, over which the noise thins to nothing. */
export const GAP_EDGE = 0.34;
/** Gap bounds as fractions of the canvas, so some noise always frames it. */
const GAP_MAX_X = 0.46;
const GAP_MAX_Y = 0.44;
const GAP_MIN_X = 0.12;
const GAP_MIN_Y = 0.16;

export interface ScenePlacement {
	/** The Eclipse's orb and its field strength; zeroes with `dim` 1 for every other scene. */
	orb: OrbPlacement;
	/**
	 * The scene's anchor, device px:
	 * dial-out: x, y of the point the signals call; lost-signal: gap centre x,
	 * y and half-extent x, y; dome: the pole's x, y and the accent star's x, y
	 * at time 0; band: the Milky Way's centre line x at the top and at the
	 * bottom edge, and its half-width; eclipse: zeroes.
	 */
	focus: readonly [number, number, number, number];
}

const clamp = (v: number, lo: number, hi: number) =>
	Math.min(hi, Math.max(lo, v));

export interface PlacementInput {
	/** Canvas size in device px, and the pixel ratio. */
	W: number;
	H: number;
	dpr: number;
	/** The `maskRect`, canvas-relative device px. */
	rect: RectLike | null;
	/** The `quietRects`, same space. */
	quiet?: readonly RectLike[];
	/** CSS width under which a canvas counts as narrow. */
	narrowBelow?: number | undefined;
	/** Dial-out: an explicit point, as fractions. */
	focus?: FocusPoint | undefined;
}

/** Gap between a quiet rect and anything a scene wants to keep clear of its dimming: the quiet pad and feather, device px. */
export function quietClearance(W: number, dpr: number): number {
	return (QUIET_ALPHA.pad + QUIET_ALPHA.feather) * dpr * maskScale(W / dpr);
}

/** The left and right extent of the panel and the quiet text, or `null` with none. */
function avoidSpan(
	rect: RectLike | null,
	quiet: readonly RectLike[],
): { left: number; right: number } | null {
	const all = rect ? [rect, ...quiet] : quiet;
	if (all.length === 0) return null;
	return {
		left: Math.min(...all.map((r) => r.left)),
		right: Math.max(...all.map((r) => r.right)),
	};
}

/** The dial-out point sits this far down the canvas. */
export const DIAL_OUT_DEPTH = 0.4;
/** The dome's accent star starts this far down the canvas. */
const DOME_HERO_Y = 0.3;
/** The Milky Way's half-width: a share of the height plus a share of the width. */
export function bandHalfWidth(W: number, H: number): number {
	return 0.15 * H + 0.025 * W;
}
/** The Milky Way rises to the right at 45 degrees: its centre line is this much further right at the top than at the bottom, as a multiple of the height. */
export const BAND_LEAN = 1;
/** With nothing to clear, the centre line is this far across at the top. */
const BAND_FREE_TOP = 0.8;

/**
 * The Milky Way's centre line: its x at the top edge and at the bottom edge.
 * It leans right going up, so it holds the free side of text that is short at
 * the bottom. At every text rect's rows (`quiet` and `rect`) it stays a quiet
 * clearance to the right, so the dense core never sits in the dimmed zone; it
 * may start up to half a half-width past the right edge when that takes.
 */
function bandLine(
	W: number,
	H: number,
	dpr: number,
	rects: readonly RectLike[],
): { top: number; bottom: number } {
	const half = bandHalfWidth(W, H);
	const lean = BAND_LEAN * H;
	const clear = quietClearance(W, dpr);
	let top = BAND_FREE_TOP * W;
	for (const r of rects) {
		if (r.bottom < 0 || r.top > H) continue;
		// The line is furthest left at the rect's lowest row.
		const lowest = Math.min(r.bottom, H);
		top = Math.max(top, r.right + clear + lean * (lowest / H));
	}
	top = Math.min(top, W + 0.5 * half);
	return { top, bottom: top - lean };
}

/**
 * Where a scene puts its one anchored thing. The Eclipse keeps clear of
 * `rect`; dial-out calls a point on the free side of the panel and the quiet
 * text, a clear pad away from it, and the dome puts its accent star there too;
 * the band runs its Milky Way past the text (`bandLine`); lost-signal opens
 * its gap around `rect`.
 */
export function scenePlacement(
	name: SceneName,
	{
		W,
		H,
		dpr,
		rect,
		quiet = [],
		narrowBelow = NARROW_BELOW_CSS_PX,
		focus,
	}: PlacementInput,
): ScenePlacement {
	const none: OrbPlacement = { cx: 0, cy: 0, r: 0, dim: 1 };
	const span = avoidSpan(rect, quiet);
	const clear = quietClearance(W, dpr);
	switch (name) {
		case "eclipse":
			return {
				orb: orbPlacement(W, H, dpr, rect, narrowBelow),
				focus: [0, 0, 0, 0],
			};
		case "dial-out": {
			// A narrow canvas has no free side: the point stays on the right and the
			// signals rise behind the text, at its quiet opacity.
			const narrow = W / dpr < narrowBelow;
			let fx = 0.74;
			if (!narrow && span) {
				// A pad past the dimmed zone and the halo, on the side the text is not.
				const halo = 7 * 16 * dpr;
				fx =
					(span.left + span.right) / 2 < W / 2
						? Math.max(0.74, (span.right + clear + halo) / W)
						: Math.min(0.26, (span.left - clear - halo) / W);
			}
			const f = focus ?? { x: fx, y: DIAL_OUT_DEPTH };
			return {
				orb: none,
				focus: [clamp(f.x, 0.08, 0.92) * W, clamp(f.y, 0.08, 0.8) * H, 0, 0],
			};
		}
		case "dome": {
			// The pole sits above the top right edge; the accent star on the free side.
			let hx = 0.76;
			if (span && (span.left + span.right) / 2 < W / 2) {
				hx = Math.max(hx, (span.right + clear) / W);
			}
			const f = focus ?? { x: hx, y: DOME_HERO_Y };
			return {
				orb: none,
				focus: [
					1.02 * W,
					-0.02 * H,
					clamp(f.x, 0.1, 0.94) * W,
					clamp(f.y, 0.1, 0.8) * H,
				],
			};
		}
		case "band": {
			const line = bandLine(W, H, dpr, rect ? [rect, ...quiet] : quiet);
			return {
				orb: none,
				focus: [line.top, line.bottom, bandHalfWidth(W, H), 0],
			};
		}
		case "lost-signal": {
			const drift = GAP_DRIFT * Math.min(W, H);
			let cx = W / 2;
			let cy = H / 2;
			let rx = W * 0.28;
			let ry = H * 0.3;
			if (rect) {
				cx = (rect.left + rect.right) / 2;
				cy = (rect.top + rect.bottom) / 2;
				rx = (rect.right - rect.left) / 2 + drift + W * 0.03;
				ry = (rect.bottom - rect.top) / 2 + drift + H * 0.05;
			}
			rx = clamp(rx, W * GAP_MIN_X, W * GAP_MAX_X);
			ry = clamp(ry, H * GAP_MIN_Y, H * GAP_MAX_Y);
			// Keep the whole gap, drift included, inside the canvas.
			cx = clamp(cx, rx, Math.max(rx, W - rx));
			cy = clamp(cy, ry, Math.max(ry, H - ry));
			return { orb: none, focus: [cx, cy, rx, ry] };
		}
		default:
			return { orb: none, focus: [0, 0, 0, 0] };
	}
}
