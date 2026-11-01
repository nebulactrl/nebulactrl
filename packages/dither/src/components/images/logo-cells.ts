/**
 * Pure cell logic for `DitheredLogo`: a logo is rasterised onto the cell grid,
 * cut at half coverage so every edge lands on a cell boundary, outlined with
 * solid cells one wide, and filled inside with a dither.
 */

import type { BayerSize, DitherMatrix } from "../../lib/bayer";
import { inkedAt, toMatrix } from "../../lib/threshold";

export type LogoFill = "gradient" | "solid" | "dotted" | "hatched";
export type LogoEffect = "none" | "shimmer" | "scan";

export interface LogoStyle {
	fill: LogoFill;
	/** Gradient tone at the bottom of the logo, 0..1. */
	floor: number;
	/** Keep a solid one-cell outline. */
	outline: boolean;
	/** The threshold matrix of the gradient and the effects: a Bayer size or a matrix name. Default `bayer4`. */
	n: BayerSize | DitherMatrix;
	effect: LogoEffect;
	/** 0..1 position in the effect's loop. */
	phase: number;
}

export const DEFAULT_LOGO_STYLE: LogoStyle = {
	fill: "gradient",
	floor: 0.2,
	outline: true,
	n: 4,
	effect: "none",
	phase: 0,
};

/**
 * Which part of the file is the logo. `alpha`: opaque cells, for transparent
 * backgrounds. `dark`: dark cells, for a dark logo on an opaque light
 * background.
 */
export type LogoSource = "alpha" | "dark";

/** How much of a cell belongs to the logo, on the 0..255 scale of a byte. */
export function coverage(
	rgba: ArrayLike<number>,
	pixel: number,
	source: LogoSource,
): number {
	const i = pixel * 4;
	const alpha = rgba[i + 3] ?? 0;
	if (source === "alpha") return alpha;
	const light =
		(0.2126 * (rgba[i] ?? 0) +
			0.7152 * (rgba[i + 1] ?? 0) +
			0.0722 * (rgba[i + 2] ?? 0)) /
		255;
	return (1 - light) * alpha;
}

/**
 * Cells at least half covered are inside: by alpha, or for `source: "dark"`
 * by darkness. `threshold` is the cut-off on 0..255 (default 128).
 */
export function insideMask(
	rgba: ArrayLike<number>,
	threshold = 128,
	source: LogoSource = "alpha",
): Uint8Array {
	const count = Math.floor(rgba.length / 4);
	const inside = new Uint8Array(count);
	for (let p = 0; p < count; p += 1)
		inside[p] = coverage(rgba, p, source) >= threshold ? 1 : 0;
	return inside;
}

/** First and last row that contain an inside cell, or `null` for an empty logo. */
export function rowSpan(
	inside: Uint8Array,
	w: number,
	h: number,
): [number, number] | null {
	let first = -1;
	let last = -1;
	for (let y = 0; y < h; y += 1) {
		for (let x = 0; x < w; x += 1) {
			if (inside[y * w + x]) {
				if (first === -1) first = y;
				last = y;
				break;
			}
		}
	}
	return first === -1 ? null : [first, last];
}

/** True when an inside cell touches the outside (or the grid edge) in a 4-neighbourhood. */
export function isEdge(
	inside: Uint8Array,
	w: number,
	h: number,
	x: number,
	y: number,
): boolean {
	const at = (cx: number, cy: number) =>
		cx < 0 || cy < 0 || cx >= w || cy >= h ? 0 : (inside[cy * w + cx] ?? 0);
	return (
		at(x - 1, y) === 0 ||
		at(x + 1, y) === 0 ||
		at(x, y - 1) === 0 ||
		at(x, y + 1) === 0
	);
}

/** Summed RGB distance, 0 to 3, between two pixels that counts as a new part of the logo. */
export const COLOR_SEAM = 0.3;

/**
 * The outline cells: inside cells that touch the outside, and, with `seams`,
 * inside cells next to a cell of a clearly different colour, so the parts of
 * a multicolour mark stay apart when drawn in one ink.
 */
export function rimMask(
	inside: Uint8Array,
	rgba: ArrayLike<number>,
	w: number,
	h: number,
	seams = true,
): Uint8Array {
	const rim = new Uint8Array(w * h);
	const gap = (a: number, b: number) =>
		(Math.abs((rgba[a] ?? 0) - (rgba[b] ?? 0)) +
			Math.abs((rgba[a + 1] ?? 0) - (rgba[b + 1] ?? 0)) +
			Math.abs((rgba[a + 2] ?? 0) - (rgba[b + 2] ?? 0))) /
		255;
	const seam = (x: number, y: number, dx: number, dy: number) =>
		seams &&
		x + dx < w &&
		y + dy < h &&
		inside[(y + dy) * w + x + dx] === 1 &&
		gap((y * w + x) * 4, ((y + dy) * w + x + dx) * 4) > COLOR_SEAM;
	for (let y = 0; y < h; y += 1) {
		for (let x = 0; x < w; x += 1) {
			if (!inside[y * w + x]) continue;
			if (isEdge(inside, w, h, x, y) || seam(x, y, 1, 0) || seam(x, y, 0, 1))
				rim[y * w + x] = 1;
		}
	}
	return rim;
}

/**
 * Which cells are inked, as 0/1 per cell, for one frame of the logo. Outside
 * cells are never inked. Edge cells are always inked when `outline` is on.
 * `effect: "shimmer"` sweeps a diagonal band of extra density across; `"scan"`
 * runs a solid line down the logo and leaves solid ink behind it that decays.
 */
export function logoInk(
	inside: Uint8Array,
	w: number,
	h: number,
	style: Partial<LogoStyle> = {},
	/** Cells that count as edge for the outline; default: `isEdge` of the silhouette. */
	rim?: Uint8Array,
): Uint8Array {
	const s = { ...DEFAULT_LOGO_STYLE, ...style };
	const matrix = toMatrix(s.n);
	const ink = new Uint8Array(w * h);
	const span = rowSpan(inside, w, h);
	if (!span) return ink;
	const [top, bottom] = span;
	const height = Math.max(1, bottom - top);
	const scanRow = top + s.phase * (height + 6) - 3;

	for (let y = 0; y < h; y += 1) {
		for (let x = 0; x < w; x += 1) {
			const p = y * w + x;
			if (!inside[p]) continue;
			if (s.outline && (rim ? rim[p] === 1 : isEdge(inside, w, h, x, y))) {
				ink[p] = 1;
				continue;
			}
			let on = false;
			switch (s.fill) {
				case "solid":
					on = true;
					break;
				case "gradient": {
					const t = (y - top) / height;
					let tone = 1 - (1 - s.floor) * t;
					if (s.effect === "shimmer") {
						const band =
							Math.abs(x + y - s.phase * (w + h)) / Math.max(2, h * 0.35);
						tone += Math.max(0, 1 - band) * 0.55;
					}
					on = inkedAt(Math.min(1, tone), x, y, matrix);
					break;
				}
				case "dotted":
					on = x % 2 === 0 && y % 2 === 0;
					break;
				case "hatched":
					on = (x + y) % 3 === 0;
					break;
			}
			if (s.effect === "scan") {
				const behind = scanRow - y;
				if (behind >= 0 && behind <= 1) on = true;
				else if (behind > 1 && behind < 8)
					on = on || inkedAt(1 - behind / 8, x, y, matrix);
			} else if (s.effect === "shimmer" && s.fill !== "gradient") {
				on = on || Math.abs(x + y - s.phase * (w + h)) < 2;
			}
			ink[p] = on ? 1 : 0;
		}
	}
	return ink;
}

/** Cells lit by the scan line at `phase`: the leading row and the one behind it. */
export function scanLit(
	y: number,
	top: number,
	height: number,
	phase: number,
): boolean {
	const row = top + phase * (height + 6) - 3;
	const behind = row - y;
	return behind >= 0 && behind <= 1;
}

/**
 * Position 0..1 in the effect's loop at `time` seconds, for a logo that
 * starts `phase` (0..1) into it. `period` is seconds per loop, at least 0.5.
 */
export function sweepAt(time: number, period: number, phase: number): number {
	const loop = time / Math.max(0.5, period) + phase;
	return ((loop % 1) + 1) % 1;
}

/** Number of inked cells. */
export function countInk(ink: Uint8Array): number {
	let total = 0;
	for (const cell of ink) total += cell;
	return total;
}
