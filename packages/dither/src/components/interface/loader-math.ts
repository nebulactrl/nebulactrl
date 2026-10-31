/**
 * Tone fields for the dithered loaders. Each shape is a field over a small
 * grid of cells: the shape's outline is hard (a cell is in or out), and only
 * the tone inside it changes, so the motion happens in the dither. Pure, in
 * units of the loader's height, so it is the same at every size.
 */

export type LoaderShape = "ring" | "dots" | "pulse" | "bar";
export type LoaderSize = "sm" | "md" | "lg";

export const LOADER_SHAPES: readonly LoaderShape[] = [
	"ring",
	"dots",
	"pulse",
	"bar",
];

/** Heights in px of `sm`, `md` and `lg`. */
export const LOADER_HEIGHT: Record<LoaderSize, number> = {
	sm: 24,
	md: 40,
	lg: 64,
};

/** Width as a multiple of height. */
export const LOADER_ASPECT: Record<LoaderShape, number> = {
	ring: 1,
	pulse: 1,
	dots: 2,
	bar: 3,
};

/** The moment drawn when motion is off: each shape reads as a loader on one still frame. */
export const LOADER_STILL: Record<LoaderShape, number> = {
	ring: 0.6,
	dots: 0.5,
	pulse: 0.35,
	bar: 0.9,
};

/** Tone of the faint track behind a comet or a bar. */
export const TRACK_TONE = 0.14;

export function loaderHeight(size: LoaderSize | number): number {
	return typeof size === "number" ? size : LOADER_HEIGHT[size];
}

/** Cell edge in px: 1 at `sm`, 2 above it, unless the caller says. */
export function loaderCell(
	size: LoaderSize | number,
	cell: number | undefined,
): number {
	if (cell !== undefined) return Math.max(1, cell);
	return loaderHeight(size) <= LOADER_HEIGHT.sm ? 1 : 2;
}

export interface LoaderGrid {
	cols: number;
	rows: number;
}

export function loaderGrid(
	shape: LoaderShape,
	height: number,
	cell: number,
): LoaderGrid {
	const rows = Math.max(1, Math.round(height / cell));
	return { cols: Math.max(1, Math.round(rows * LOADER_ASPECT[shape])), rows };
}

const TAU = Math.PI * 2;
const fract = (value: number) => value - Math.floor(value);
const clamp01 = (value: number) => Math.min(1, Math.max(0, value));

/** Radius of the ring and half its thickness, in heights. */
export const RING_RADIUS = 0.38;
export const RING_HALF_THICKNESS = 0.1;
/** The comet's head sits `HEAD` radians ahead of its tail; inside it the ring is solid. */
const HEAD = 0.18;

/** Angle of the comet's head: a steady turn with a slow breath on top. */
export function cometAngle(t: number): number {
	return TAU * t * 0.7 + 0.6 * Math.sin(TAU * t * 0.35);
}

/** Length of the comet's tail in radians: it stretches and shrinks as it goes round. */
export function cometTail(t: number): number {
	return Math.PI * (1.1 + 0.45 * Math.sin(TAU * t * 0.7 - 1));
}

function ringTone(px: number, py: number, t: number): number {
	const dx = px - 0.5;
	const dy = py - 0.5;
	if (Math.abs(Math.hypot(dx, dy) - RING_RADIUS) > RING_HALF_THICKNESS)
		return -1;
	const behind = (((cometAngle(t) - Math.atan2(dy, dx)) % TAU) + TAU) % TAU;
	const tail = cometTail(t);
	if (behind < HEAD) return 1;
	if (behind >= tail) return TRACK_TONE;
	const along = (behind - HEAD) / (tail - HEAD);
	return Math.max(TRACK_TONE, 0.95 * (1 - along) ** 1.2);
}

function pulseTone(px: number, py: number, t: number): number {
	const d = Math.hypot(px - 0.5, py - 0.5);
	const beat = 0.5 + 0.5 * Math.sin(TAU * t * 1.2);
	if (d <= 0.1 + 0.035 * beat) return 1;
	let tone = -1;
	for (let k = 0; k < 3; k++) {
		const phase = fract(t * 0.55 + k / 3);
		if (Math.abs(d - (0.16 + phase * 0.32)) <= 0.035) {
			tone = Math.max(tone, 0.85 * (1 - phase));
		}
	}
	return tone;
}

/** Radius of one of the three discs, and where they sit across the width of 2 heights. */
export const DOT_RADIUS = 0.26;
export const DOT_CENTRES = [2 / 6, 1, 2 - 2 / 6] as const;

function dotsTone(px: number, py: number, t: number): number {
	for (let k = 0; k < DOT_CENTRES.length; k++) {
		const cx = DOT_CENTRES[k] ?? 1;
		if (Math.hypot(px - cx, py - 0.5) > DOT_RADIUS) continue;
		const phase = fract(t * 0.9 - k * 0.16);
		const rise = phase < 0.5 ? phase / 0.5 : (1 - phase) / 0.5;
		return clamp01(0.12 + 0.88 * rise * rise * (3 - 2 * rise));
	}
	return -1;
}

/** Share of the bar's height it fills, centred. */
export const BAR_THICKNESS = 0.4;

function barTone(px: number, py: number, t: number): number {
	if (Math.abs(py - 0.5) > BAR_THICKNESS / 2) return -1;
	const u = px / LOADER_ASPECT.bar;
	const phase = fract(t * 0.55);
	const eased = phase < 0.5 ? 2 * phase * phase : 1 - (-2 * phase + 2) ** 2 / 2;
	const head = -0.2 + 1.4 * eased;
	const behind = head - u;
	if (behind >= -0.015 && behind < 0.03) return 1;
	if (behind >= 0.03 && behind < 0.35) {
		return Math.max(TRACK_TONE, 0.95 * (1 - (behind - 0.03) / 0.32));
	}
	return TRACK_TONE;
}

/**
 * The tone at (`px`, `py`) in units of the loader's height (y down), or -1
 * outside the shape. `t` is seconds on the shared clock, already scaled by
 * `speed`.
 */
export function loaderTone(
	shape: LoaderShape,
	px: number,
	py: number,
	t: number,
): number {
	switch (shape) {
		case "ring":
			return ringTone(px, py, t);
		case "pulse":
			return pulseTone(px, py, t);
		case "dots":
			return dotsTone(px, py, t);
		case "bar":
			return barTone(px, py, t);
	}
}
