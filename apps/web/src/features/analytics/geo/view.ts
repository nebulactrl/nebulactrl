import type { MapBounds } from "@nebulactrl/dither/backgrounds/geo";
import { MIN_LAT_SPAN } from "./model";
import { LAT_LIMITS, type Projection, type ViewTransform } from "./projection";

/**
 * Pan and zoom by hand, as pure functions. The map draws a committed
 * projection (a grid of dots over some bounds). While a hand is on it, the
 * grid is moved with a `View`, a translate and a uniform scale about the
 * grid's top left corner in CSS pixels, and nothing is redrawn; when the
 * hand rests, `viewBounds` says which part of the world the moved grid now
 * shows and the map redraws there, crisp, with the view back at rest.
 */

export interface Vec {
	x: number;
	y: number;
}

/** A translate (x, y) then a uniform scale k about the grid's top left corner. */
export interface View extends Vec {
	k: number;
}

export const REST_VIEW: View = { x: 0, y: 0, k: 1 };

export const clamp = (n: number, lo: number, hi: number) =>
	Math.min(Math.max(n, lo), hi);

// ---- the wheel ------------------------------------------------------------

/** What one line of a line-mode wheel (Firefox, some mice) is worth in pixels. */
const LINE_PX = 16;

/** Zoom per pixel of wheel travel: a 100px notch is about x1.25. */
const WHEEL_RATE = 0.0022;
/** A trackpad pinch arrives as many small deltas, so each pixel counts for more. */
const PINCH_RATE = 0.01;
/** One event never zooms by more than this many pixels of travel. */
export const WHEEL_CAP = 240;
/** A ctrl-wheel event under this many pixels is a pinch, not a notch. */
const PINCH_MAX_DELTA = 50;

/** A wheel's `deltaY` in pixels: lines and pages (`deltaMode` 1 and 2) are scaled up. */
export function wheelPixels(
	e: { deltaY: number; deltaMode: number },
	page: number,
): number {
	if (e.deltaMode === 1) return e.deltaY * LINE_PX;
	if (e.deltaMode === 2) return e.deltaY * page;
	return e.deltaY;
}

/**
 * Browsers send a trackpad pinch as a wheel event with `ctrlKey` set (and no
 * `metaKey`), in pixels, with small deltas. A ctrl-wheel on a mouse comes in
 * whole notches of 100 or more.
 */
export function isPinchWheel(e: {
	ctrlKey: boolean;
	metaKey: boolean;
	deltaMode: number;
	deltaY: number;
}): boolean {
	return (
		e.ctrlKey &&
		!e.metaKey &&
		e.deltaMode === 0 &&
		Math.abs(e.deltaY) < PINCH_MAX_DELTA
	);
}

/** The zoom of one wheel event: `exp(-pixels * rate)`, so scrolling up zooms in and the steps compose. */
export function wheelFactor(pixels: number, pinch = false): number {
	return Math.exp(
		-clamp(pixels, -WHEEL_CAP, WHEEL_CAP) * (pinch ? PINCH_RATE : WHEEL_RATE),
	);
}

// ---- zoom -----------------------------------------------------------------

/**
 * How far a view may scale from the committed bounds: in to
 * `MIN_LAT_SPAN` degrees of latitude (the zoom buttons' limit), out to the
 * widest the map ever shows, `LAT_LIMITS` tall and one turn of longitude
 * wide. The range always holds 1, so a window already past a limit can still move.
 */
export function zoomRange(bounds: MapBounds): { min: number; max: number } {
	const lat = bounds.north - bounds.south;
	const lon = bounds.east - bounds.west;
	const worldLat = LAT_LIMITS.north - LAT_LIMITS.south;
	return {
		min: Math.min(1, Math.max(lat / worldLat, lon / 360)),
		max: Math.max(1, lat / MIN_LAT_SPAN),
	};
}

/**
 * `view` scaled by `factor` (clamped to `range`) so the point under
 * `anchor`, in the grid's own pixel space (origin at its top left corner),
 * stays under it.
 */
export function zoomAround(
	view: View,
	factor: number,
	anchor: Vec,
	range: { min: number; max: number },
): View {
	const k = clamp(view.k * factor, range.min, range.max);
	const f = k / view.k;
	return {
		x: anchor.x - (anchor.x - view.x) * f,
		y: anchor.y - (anchor.y - view.y) * f,
		k,
	};
}

/**
 * `bounds` zoomed by `factor` (below 1 zooms in) about its centre, keeping
 * its own shape. For a window the hand has moved: `zoomBounds` makes a
 * region of the tabs' shape instead.
 */
export function zoomWindow(bounds: MapBounds, factor: number): MapBounds {
	const lat = bounds.north - bounds.south;
	const lon = bounds.east - bounds.west;
	const { min, max } = zoomRange(bounds);
	const k = clamp(1 / factor, min, max);
	const latSpan = lat / k;
	const lonSpan = lon / k;
	const midLon = (bounds.east + bounds.west) / 2;
	const south = clamp(
		(bounds.north + bounds.south) / 2 - latSpan / 2,
		LAT_LIMITS.south,
		LAT_LIMITS.north - latSpan,
	);
	return normaliseLon({
		north: south + latSpan,
		south,
		west: midLon - lonSpan / 2,
		east: midLon + lonSpan / 2,
	});
}

// ---- pan ------------------------------------------------------------------

/** The offsets a view may take along one axis. */
export interface Range {
	lo: number;
	hi: number;
}

/**
 * The offsets at which content from `start` to `end` (unscaled grid pixels,
 * scaled by `k`) still covers a viewport of `viewport` pixels. Content
 * smaller than the viewport cannot cover it, so it is held centred: a
 * single offset.
 */
export function axisRange(
	start: number,
	end: number,
	k: number,
	viewport: number,
): Range {
	if ((end - start) * k <= viewport) {
		const centred = (viewport - (start + end) * k) / 2;
		return { lo: centred, hi: centred };
	}
	return { lo: viewport - end * k, hi: -start * k };
}

/**
 * The share of the viewport the grid keeps covering when it is dragged
 * sideways. The map redraws only when a hand rests, so what a drag uncovers
 * is blank until then; this keeps at least half of the panel drawn.
 */
export const MIN_COVER = 0.5;

/**
 * The offsets at which content of `size` unscaled pixels, at scale `k`, still
 * covers `cover` of a viewport of `viewport` pixels (all of itself when it is
 * smaller than that share).
 */
export function coverRange(
	size: number,
	k: number,
	viewport: number,
	cover = MIN_COVER,
): Range {
	const need = Math.min(cover * viewport, size * k);
	return { lo: need - size * k, hi: viewport - need };
}

/**
 * Where a view at scale `k` may sit. Latitude is bounded by `LAT_LIMITS`, so
 * the map's edge never comes into the box. Longitude wraps, so the redraw
 * after a drag always finds more world; but until it comes the grid's edge is
 * on screen, so a drag may uncover at most `MIN_COVER` of the box sideways.
 */
export function panLimits(
	p: Pick<Projection, "bounds" | "width" | "height">,
	k: number,
): { x: Range; y: Range } {
	const latSpan = p.bounds.north - p.bounds.south;
	const top = ((p.bounds.north - LAT_LIMITS.north) / latSpan) * p.height;
	const bottom = ((p.bounds.north - LAT_LIMITS.south) / latSpan) * p.height;
	return {
		x: coverRange(p.width, k, p.width),
		y: axisRange(top, bottom, k, p.height),
	};
}

/** `offset` held inside `range`. */
function clampOffset(offset: number, range: Range): number {
	return clamp(offset, range.lo, range.hi);
}

/** Slope at the edge: the first pixel past it moves the map this share of a pixel. */
const RUBBER_STIFFNESS = 0.55;

/**
 * How far the map follows a pull `over` pixels past its edge: it starts at
 * `RUBBER_STIFFNESS` of the pull and flattens, and never reaches
 * `viewport`, so the harder you pull the less it gives.
 */
export function rubberBand(over: number, viewport: number): number {
	if (over <= 0 || viewport <= 0) return 0;
	return (1 - 1 / ((over * RUBBER_STIFFNESS) / viewport + 1)) * viewport;
}

/** `offset` as drawn while a hand holds it: inside `range` as is, past it damped by `rubberBand`. */
export function resist(offset: number, range: Range, viewport: number): number {
	if (offset > range.hi)
		return range.hi + rubberBand(offset - range.hi, viewport);
	if (offset < range.lo)
		return range.lo - rubberBand(range.lo - offset, viewport);
	return offset;
}

/** The view with its offsets held inside what `panLimits` allows at its scale. */
export function clampView(
	v: View,
	p: Pick<Projection, "bounds" | "width" | "height">,
): View {
	const limits = panLimits(p, v.k);
	return {
		x: clampOffset(v.x, limits.x),
		y: clampOffset(v.y, limits.y),
		k: v.k,
	};
}

/** The view as drawn while a hand holds it at raw offsets `v`: the scale as is, the offsets rubber-banded. */
export function resistView(
	v: View,
	p: Pick<Projection, "bounds" | "width" | "height">,
): View {
	const limits = panLimits(p, v.k);
	return {
		x: resist(v.x, limits.x, p.width),
		y: resist(v.y, limits.y, p.height),
		k: v.k,
	};
}

// ---- momentum -------------------------------------------------------------

/** A frame at 60 Hz: the unit of the friction and the stop speed. */
export const FRAME_MS = 1000 / 60;
/** What is left of a release's speed after one frame. */
export const FRICTION = 0.92;
/** Under this many pixels a frame the glide is over. */
export const STOP_SPEED = 0.1;
/** No flick carries faster than this, in pixels a frame. */
const MAX_SPEED = 60;
/** The release speed is read from this much of the drag's end. */
const SAMPLE_WINDOW_MS = 100;
/** A pointer that has not moved for this long before release is at rest. */
const IDLE_MS = 80;

/**
 * One step of the glide after `dtMs`: how far it moved (the frames' moves
 * summed, so a slow frame covers what the fast ones would have) and the
 * velocity left, in pixels a frame. Zero once it falls under `STOP_SPEED`.
 */
export function momentumStep(
	velocity: Vec,
	dtMs: number,
): { delta: Vec; velocity: Vec } {
	const frames = dtMs / FRAME_MS;
	const decay = FRICTION ** frames;
	const gain = (1 - decay) / (1 - FRICTION);
	const next = { x: velocity.x * decay, y: velocity.y * decay };
	return {
		delta: { x: velocity.x * gain, y: velocity.y * gain },
		velocity: Math.hypot(next.x, next.y) < STOP_SPEED ? { x: 0, y: 0 } : next,
	};
}

export interface PointerSample extends Vec {
	/** `performance.now()` of the move. */
	t: number;
}

/**
 * The glide a release starts, in pixels a frame, from the moves of the last
 * 100ms. A pointer that stopped before it let go carries nothing.
 */
export function releaseVelocity(
	samples: readonly PointerSample[],
	now: number,
): Vec {
	const none = { x: 0, y: 0 };
	const recent = samples.filter((s) => now - s.t <= SAMPLE_WINDOW_MS);
	const first = recent[0];
	const last = recent.at(-1);
	if (!first || !last || first === last) return none;
	if (now - last.t > IDLE_MS || last.t <= first.t) return none;
	const dt = last.t - first.t;
	const vx = ((last.x - first.x) / dt) * FRAME_MS;
	const vy = ((last.y - first.y) / dt) * FRAME_MS;
	const speed = Math.hypot(vx, vy);
	if (speed <= MAX_SPEED) return { x: vx, y: vy };
	return { x: (vx / speed) * MAX_SPEED, y: (vy / speed) * MAX_SPEED };
}

// ---- back to the world ------------------------------------------------------

/** `bounds` with its longitudes shifted by whole turns so the west edge is in [-180, 180). */
export function normaliseLon(bounds: MapBounds): MapBounds {
	const turns = Math.floor((bounds.west + 180) / 360);
	if (turns === 0) return bounds;
	return {
		...bounds,
		west: bounds.west - turns * 360,
		east: bounds.east - turns * 360,
	};
}

/**
 * The part of the world a view shows: the projection's grid (its own pixel
 * rectangle) after the view moved it, as bounds. Scale 1 and no offset is
 * the projection's own bounds. A grid shown at scale k covers 1/k of the
 * span, so a view that was kept inside `panLimits` stays inside `LAT_LIMITS`.
 */
export function viewBounds(
	p: Pick<Projection, "bounds" | "width" | "height">,
	v: View,
): MapBounds {
	const lonSpan = p.bounds.east - p.bounds.west;
	const latSpan = p.bounds.north - p.bounds.south;
	const west = p.bounds.west - (v.x / v.k / p.width) * lonSpan;
	const north = p.bounds.north + (v.y / v.k / p.height) * latSpan;
	return normaliseLon({
		north,
		south: north - latSpan / v.k,
		west,
		east: west + lonSpan / v.k,
	});
}

/**
 * Where the layer that is about to fade in starts, when the layer it
 * replaces is shown with `view`: the transform of `viewTransform(from, to)`
 * with the view of `from` folded in, so a tab pressed while the map is
 * still gliding from a flick starts from what is on screen.
 */
export function enterFrom(
	view: View,
	t: ViewTransform,
	from: Pick<Projection, "x" | "y">,
	to: Pick<Projection, "x" | "y">,
): ViewTransform {
	return {
		x: from.x + view.x + view.k * (to.x + t.x - from.x) - to.x,
		y: from.y + view.y + view.k * (to.y + t.y - from.y) - to.y,
		scaleX: view.k * t.scaleX,
		scaleY: view.k * t.scaleY,
	};
}

/**
 * The key that holds the wheel zoom, by platform: ⌘ on Apple devices, Ctrl
 * elsewhere. `glyph` is what the hint draws, `name` what a screen reader says.
 */
export function zoomModifier(platform: string): {
	glyph: string;
	name: string;
} {
	return /Mac|iPhone|iPad|iPod/.test(platform)
		? { glyph: "⌘", name: "Command" }
		: { glyph: "Ctrl", name: "Control" };
}

// ---- keys -----------------------------------------------------------------

/** The share of the grid one arrow key press moves the map. */
export const KEY_PAN_SHARE = 0.2;
/** The zoom of one + or - key press. */
export const KEY_ZOOM = 1.5;

export type KeyAction =
	| { type: "pan"; x: -1 | 0 | 1; y: -1 | 0 | 1 }
	| { type: "zoom"; factor: number }
	| { type: "fit" };

/**
 * What a key does on the focused map. An arrow moves the camera that way, so
 * the map moves the other way: `x` and `y` are the sign of the map's offset.
 */
export function keyAction(key: string): KeyAction | null {
	switch (key) {
		case "ArrowLeft":
			return { type: "pan", x: 1, y: 0 };
		case "ArrowRight":
			return { type: "pan", x: -1, y: 0 };
		case "ArrowUp":
			return { type: "pan", x: 0, y: 1 };
		case "ArrowDown":
			return { type: "pan", x: 0, y: -1 };
		case "+":
		case "=":
			return { type: "zoom", factor: KEY_ZOOM };
		case "-":
		case "_":
			return { type: "zoom", factor: 1 / KEY_ZOOM };
		case "0":
			return { type: "fit" };
		default:
			return null;
	}
}
