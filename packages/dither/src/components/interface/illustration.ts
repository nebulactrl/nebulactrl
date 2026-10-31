import type { DitherMatrix } from "../../lib/bayer";
import { cellInk, clamp01 } from "./fill";

/**
 * Small lit objects drawn in dither cells, for empty states. Each object is a
 * stack of flat or curved parts; every surface takes a tone from a light up and
 * to the left, so faces turned to the light are dense and faces turned away
 * are sparse, and curved surfaces step through every level of the matrix. A
 * one-cell seam splits the parts, a clear margin outlines the whole, and a
 * faint halo and floor shadow sit behind it.
 *
 * Pure: the scene is a grid of (part, tone) built once per name; the paths
 * are cached per name and matrix.
 */

export type EmptyStateIllustration =
	| "box"
	| "search"
	| "folder"
	| "planet"
	| "rack"
	| "radar"
	| "key"
	| "network"
	| "bell";

export const ILLUSTRATIONS: readonly EmptyStateIllustration[] = [
	"box",
	"search",
	"folder",
	"planet",
	"rack",
	"radar",
	"key",
	"network",
	"bell",
];

/** The grid every illustration is drawn on, in cells. */
export const ILLUSTRATION_WIDTH = 112;
export const ILLUSTRATION_HEIGHT = 72;

/** Cells of clear space between the object and its halo. */
const MARGIN = 2;

interface Sample {
	/** 0 is empty; every other value is one part of the object. */
	part: number;
	tone: number;
}

const EMPTY: Sample = { part: 0, tone: 0 };

type Point = readonly [number, number];

// Light from the upper left, towards the viewer (unit length).
const LIGHT = [-0.5, -0.55, 0.67] as const;

function inPolygon(x: number, y: number, points: readonly Point[]): boolean {
	let inside = false;
	for (let i = 0, j = points.length - 1; i < points.length; j = i++) {
		const [xi, yi] = points[i] ?? [0, 0];
		const [xj, yj] = points[j] ?? [0, 0];
		if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) {
			inside = !inside;
		}
	}
	return inside;
}

const inRect = (
	x: number,
	y: number,
	x0: number,
	y0: number,
	x1: number,
	y1: number,
) => x >= x0 && x < x1 && y >= y0 && y < y1;

/* ---------- box ---------- */

const BOX_W: Point = [30, 26];
const BOX_N: Point = [56, 13];
const BOX_E: Point = [82, 26];
const BOX_S: Point = [56, 39];
const BOX_DEPTH = 22;

function boxSample(x: number, y: number): Sample {
	const [wx, wy] = BOX_W;
	const [nx, ny] = BOX_N;
	const [ex, ey] = BOX_E;
	const [sx, sy] = BOX_S;
	const down = (p: Point): Point => [p[0], p[1] + BOX_DEPTH];
	// Front flaps fold down and out; back flaps stand up and out.
	if (inPolygon(x, y, [BOX_W, BOX_S, [sx - 9, sy + 6], [wx - 9, wy + 6]])) {
		return { part: 1, tone: 0.55 };
	}
	if (inPolygon(x, y, [BOX_S, BOX_E, [ex + 9, ey + 6], [sx + 9, sy + 6]])) {
		return { part: 2, tone: 0.32 };
	}
	// Front faces: the left one is lit, the right one turned away.
	if (inPolygon(x, y, [BOX_W, BOX_S, down(BOX_S), down(BOX_W)])) {
		return { part: 3, tone: 0.72 - (0.2 * (y - wy)) / BOX_DEPTH };
	}
	if (inPolygon(x, y, [BOX_S, BOX_E, down(BOX_E), down(BOX_S)])) {
		return { part: 4, tone: 0.36 - (0.12 * (y - ey)) / BOX_DEPTH };
	}
	// The opening: dark inside, a little lighter towards the back wall.
	if (
		inPolygon(x, y, [
			[wx + 4, wy],
			[nx, ny + 3],
			[ex - 4, ey],
			[sx, sy - 3],
		])
	) {
		return { part: 5, tone: 0.1 + 0.22 * clamp01((sy - y) / 24) };
	}
	if (inPolygon(x, y, [BOX_W, BOX_N, [nx - 8, ny - 10], [wx - 8, wy - 10]])) {
		return { part: 6, tone: 0.8 };
	}
	if (inPolygon(x, y, [BOX_N, BOX_E, [ex + 8, ey - 10], [nx + 8, ny - 10]])) {
		return { part: 7, tone: 0.48 };
	}
	return EMPTY;
}

/* ---------- search ---------- */

const LENS_C: Point = [46, 31];
const LENS_OUTER = 22;
const LENS_INNER = 15;
const HANDLE_FROM: Point = [61.6, 46.6];
const HANDLE_TO: Point = [86, 66];
const HANDLE_HALF = 4.2;
const COLLAR = 6;

function searchSample(x: number, y: number): Sample {
	const dx = x - LENS_C[0];
	const dy = y - LENS_C[1];
	const r = Math.hypot(dx, dy);
	if (r <= LENS_OUTER && r >= LENS_INNER) {
		// A rim lit from the upper left: dense there, sparse on the far side.
		const facing = (dx / r) * -0.6 + (dy / r) * -0.8;
		return { part: 1, tone: 0.22 + 0.72 * (0.5 + 0.5 * facing) };
	}
	if (r < LENS_INNER) {
		const angle = Math.atan2(dy, dx);
		const glint = Math.abs(angle + (3 * Math.PI) / 4) < 0.34 && r > 8 && r < 13;
		if (glint) return { part: 2, tone: 0.92 };
		// Glass: nearly clear, a touch denser towards the lit edge.
		return { part: 3, tone: 0.06 + 0.1 * (1 - r / LENS_INNER) };
	}
	const hx = HANDLE_TO[0] - HANDLE_FROM[0];
	const hy = HANDLE_TO[1] - HANDLE_FROM[1];
	const length = Math.hypot(hx, hy);
	const px = x - HANDLE_FROM[0];
	const py = y - HANDLE_FROM[1];
	const along = (px * hx + py * hy) / length;
	const across = (px * hy - py * hx) / length;
	if (along >= 0 && along <= length && Math.abs(across) <= HANDLE_HALF) {
		if (along < COLLAR) return { part: 4, tone: 0.28 };
		return { part: 5, tone: 0.55 - 0.3 * (across / HANDLE_HALF) };
	}
	return EMPTY;
}

/* ---------- folder ---------- */

const FRONT: readonly Point[] = [
	[20, 32],
	[92, 32],
	[88, 64],
	[24, 64],
];

function folderSample(x: number, y: number): Sample {
	if (inPolygon(x, y, FRONT)) {
		// A bright lip along the top edge, then lit paper-board thinning down.
		if (y < 34) return { part: 1, tone: 0.95 };
		return { part: 1, tone: 0.66 - (0.26 * (y - 34)) / 30 };
	}
	// The sheet peeking out: pale, with a few faint rule lines.
	if (inRect(x, y, 30, 16, 82, 32)) {
		const rule = y > 21 && (y - 22) % 4 === 0 && x > 35 && x < 76;
		return { part: 2, tone: rule ? 0.5 : 0.88 };
	}
	// The back panel and its tab.
	if (inRect(x, y, 22, 20, 90, 62) || inRect(x, y, 22, 14, 50, 20)) {
		return { part: 3, tone: 0.3 - (0.1 * (y - 14)) / 48 };
	}
	return EMPTY;
}

/* ---------- planet ---------- */

const PLANET_C: Point = [56, 34];
const PLANET_R = 21;
const RING_TILT = -0.28;
const RING_OUTER: Point = [40, 11];
const RING_INNER: Point = [30, 7.5];

function ringSample(
	x: number,
	y: number,
): { front: boolean; tone: number } | null {
	const dx = x - PLANET_C[0];
	const dy = y - PLANET_C[1];
	const cos = Math.cos(RING_TILT);
	const sin = Math.sin(RING_TILT);
	const u = dx * cos + dy * sin;
	const v = -dx * sin + dy * cos;
	const outer = (u / RING_OUTER[0]) ** 2 + (v / RING_OUTER[1]) ** 2;
	const inner = (u / RING_INNER[0]) ** 2 + (v / RING_INNER[1]) ** 2;
	if (outer > 1 || inner < 1) return null;
	return {
		front: v > 0,
		tone: 0.52 - 0.22 * (u / RING_OUTER[0]) + 0.1 * (1 - outer),
	};
}

function planetSample(x: number, y: number): Sample {
	const ring = ringSample(x, y);
	if (ring?.front) return { part: 1, tone: ring.tone };
	const dx = (x - PLANET_C[0]) / PLANET_R;
	const dy = (y - PLANET_C[1]) / PLANET_R;
	const flat = dx * dx + dy * dy;
	if (flat <= 1) {
		const dz = Math.sqrt(1 - flat);
		const lambert = dx * LIGHT[0] + dy * LIGHT[1] + dz * LIGHT[2];
		return { part: 2, tone: 0.06 + 0.92 * Math.max(0, lambert) ** 1.15 };
	}
	if (ring) return { part: 3, tone: ring.tone * 0.7 };
	return EMPTY;
}

/* ---------- rack ---------- */

const RACK_X0 = 26;
const RACK_X1 = 84;
const RACK_H = 13;
const RACK_TOPS = [13, 31, 49] as const;
const RACK_SKEW = 5;

function rackSample(x: number, y: number): Sample {
	for (const top of RACK_TOPS) {
		const bottom = top + RACK_H;
		const cy = top + 6.5;
		// A lit status light, then a dimmer one: parts 1 and 2 sit inside the face.
		if (Math.hypot(x - 34, y - cy) <= 2.4) return { part: 1, tone: 0.95 };
		if (Math.hypot(x - 42, y - cy) <= 1.6) return { part: 2, tone: 0.5 };
		// Two vent slots on the right of the face.
		if (
			inRect(x, y, 56, top + 4, 78, top + 6) ||
			inRect(x, y, 56, top + 8, 78, top + 10)
		) {
			return { part: 2, tone: 0.5 };
		}
		if (inRect(x, y, RACK_X0, top, RACK_X1, bottom)) {
			if (y < top + 1.5) return { part: 3, tone: 0.92 };
			return {
				part: 3,
				tone: 0.64 - (0.2 * (y - top)) / RACK_H - (0.14 * (x - RACK_X0)) / 58,
			};
		}
		// The right side, turned away from the light.
		if (
			inPolygon(x, y, [
				[RACK_X1, top],
				[RACK_X1 + RACK_SKEW, top - 3],
				[RACK_X1 + RACK_SKEW, bottom - 3],
				[RACK_X1, bottom],
			])
		) {
			return { part: 4, tone: 0.24 };
		}
		// The top, facing the light.
		if (
			inPolygon(x, y, [
				[RACK_X0, top],
				[RACK_X0 + RACK_SKEW, top - 3],
				[RACK_X1 + RACK_SKEW, top - 3],
				[RACK_X1, top],
			])
		) {
			return { part: 5, tone: 0.84 };
		}
	}
	return EMPTY;
}

/* ---------- radar ---------- */

const RADAR_C: Point = [56, 36];
const RADAR_R = 26;
/** The sweep's leading edge, radians; it trails behind by `SWEEP`. */
const SWEEP_LEAD = -1.05;
const SWEEP = 1.15;

function radarSample(x: number, y: number): Sample {
	const dx = x - RADAR_C[0];
	const dy = y - RADAR_C[1];
	const r = Math.hypot(dx, dy);
	if (r > RADAR_R) return EMPTY;
	// Rings, drawn with the lowest part ids so the disc leaves a seam round them.
	if (r >= RADAR_R - 2.2) {
		const facing = (dx / r) * -0.6 + (dy / r) * -0.8;
		return { part: 1, tone: 0.3 + 0.65 * (0.5 + 0.5 * facing) };
	}
	if (Math.abs(r - 16.5) <= 0.9) return { part: 2, tone: 0.5 };
	if (Math.abs(r - 8) <= 0.9) return { part: 2, tone: 0.55 };
	// Two contacts.
	if (Math.hypot(x - 69, y - 24) <= 2.3) return { part: 3, tone: 0.95 };
	if (Math.hypot(x - 43, y - 46) <= 1.7) return { part: 3, tone: 0.65 };
	if (r <= 1.6) return { part: 3, tone: 0.9 };
	// The sweep: bright on its leading edge, thinning behind it.
	const angle = Math.atan2(dy, dx);
	let behind = SWEEP_LEAD - angle;
	if (behind < 0) behind += Math.PI * 2;
	if (behind <= SWEEP && r > 2) {
		return { part: 4, tone: 0.9 * (1 - behind / SWEEP) ** 1.4 + 0.04 };
	}
	return { part: 5, tone: 0.1 + 0.08 * (1 - r / RADAR_R) };
}

/* ---------- key ---------- */

const KEY_C: Point = [54, 34];
const KEY_TILT = 0.38;
const BOW_C = -22;

function keySample(x: number, y: number): Sample {
	const dx = x - KEY_C[0];
	const dy = y - KEY_C[1];
	const cos = Math.cos(KEY_TILT);
	const sin = Math.sin(KEY_TILT);
	// Along the key and across it.
	const u = dx * cos + dy * sin;
	const v = -dx * sin + dy * cos;
	const bowR = Math.hypot(u - BOW_C, v);
	if (bowR <= 14 && bowR >= 6.5) {
		const facing = ((u - BOW_C) / bowR) * -0.55 + (v / bowR) * -0.83;
		return { part: 1, tone: 0.24 + 0.7 * (0.5 + 0.5 * facing) };
	}
	if (u >= -10 && u <= 38 && Math.abs(v) <= 3.8) {
		return { part: 2, tone: 0.58 - 0.34 * ((v + 3.8) / 7.6) };
	}
	if (
		(u >= 24 && u <= 29.5 && v > 3.8 && v <= 12) ||
		(u >= 33 && u <= 38 && v > 3.8 && v <= 8.5)
	) {
		return { part: 3, tone: 0.4 };
	}
	return EMPTY;
}

/* ---------- network ---------- */

const NODES: readonly Point[] = [
	[56, 17],
	[28, 49],
	[84, 49],
];
const NODE_R = 10;

function distanceToSegment(x: number, y: number, a: Point, b: Point): number {
	const abx = b[0] - a[0];
	const aby = b[1] - a[1];
	const t = clamp01(
		((x - a[0]) * abx + (y - a[1]) * aby) / (abx * abx + aby * aby),
	);
	return Math.hypot(x - (a[0] + t * abx), y - (a[1] + t * aby));
}

function networkSample(x: number, y: number): Sample {
	for (let i = 0; i < NODES.length; i++) {
		const [cx, cy] = NODES[i] ?? [0, 0];
		const dx = (x - cx) / NODE_R;
		const dy = (y - cy) / NODE_R;
		const flat = dx * dx + dy * dy;
		if (flat <= 1) {
			const dz = Math.sqrt(1 - flat);
			const lambert = dx * LIGHT[0] + dy * LIGHT[1] + dz * LIGHT[2];
			return { part: i + 1, tone: 0.1 + 0.88 * Math.max(0, lambert) ** 1.1 };
		}
	}
	// Links between the nodes; the discs claim their own cells first.
	for (let i = 0; i < NODES.length; i++) {
		const a = NODES[i] ?? [0, 0];
		const b = NODES[(i + 1) % NODES.length] ?? [0, 0];
		if (distanceToSegment(x, y, a, b) <= 1.3) return { part: 4, tone: 0.5 };
	}
	return EMPTY;
}

/* ---------- bell ---------- */

const BELL_X = 56;

function bellSample(x: number, y: number): Sample {
	const dx = x - BELL_X;
	if (Math.hypot(dx, y - 9.5) <= 3) return { part: 4, tone: 0.8 };
	if (Math.hypot(dx, y - 60) <= 4.6) {
		return { part: 3, tone: 0.3 + 0.5 * (0.5 - (0.5 * (dx + (y - 60))) / 6) };
	}
	// The lip flares out under the dome.
	if (y >= 48 && y <= 54) {
		const half = 26 + (3 * (y - 48)) / 6;
		if (Math.abs(dx) <= half) {
			return { part: 2, tone: 0.6 - (0.5 * (dx + half)) / (2 * half) };
		}
		return EMPTY;
	}
	if (y >= 13 && y < 48) {
		const half = 4 + 22 * Math.sqrt(clamp01((y - 13) / 35));
		if (Math.abs(dx) <= half) {
			// Lit from the left, with a bright shoulder.
			const across = (dx + half) / (2 * half);
			return { part: 1, tone: 0.9 - 0.72 * across ** 0.9 };
		}
	}
	return EMPTY;
}

const SCENES: Record<EmptyStateIllustration, (x: number, y: number) => Sample> =
	{
		box: boxSample,
		search: searchSample,
		folder: folderSample,
		planet: planetSample,
		rack: rackSample,
		radar: radarSample,
		key: keySample,
		network: networkSample,
		bell: bellSample,
	};

/** Centre of the halo and its radius in cells, per object. */
const HALO: Record<EmptyStateIllustration, { c: Point; r: number }> = {
	box: { c: [56, 34], r: 46 },
	search: { c: [56, 38], r: 48 },
	folder: { c: [56, 38], r: 46 },
	planet: { c: [56, 34], r: 50 },
	rack: { c: [56, 36], r: 48 },
	radar: { c: [56, 36], r: 48 },
	key: { c: [56, 36], r: 48 },
	network: { c: [56, 34], r: 48 },
	bell: { c: [56, 34], r: 46 },
};

export interface IllustrationScene {
	width: number;
	height: number;
	/** Part id per cell, 0 where empty. No two different parts touch. */
	parts: Uint8Array;
	/** Tone of the object, 0..1 per cell. */
	tones: Float32Array;
	/** Tone of the halo and the floor shadow, 0 inside the object's margin. */
	ambient: Float32Array;
}

const scenes = new Map<EmptyStateIllustration, IllustrationScene>();

/** The (part, tone) grid for an illustration: seams cut, margin cleared. Cached. */
export function illustrationScene(
	name: EmptyStateIllustration,
): IllustrationScene {
	const hit = scenes.get(name);
	if (hit) return hit;
	const width = ILLUSTRATION_WIDTH;
	const height = ILLUSTRATION_HEIGHT;
	const sample = SCENES[name];
	const parts = new Uint8Array(width * height);
	const tones = new Float32Array(width * height);
	for (let y = 0; y < height; y++) {
		for (let x = 0; x < width; x++) {
			const s = sample(x + 0.5, y + 0.5);
			parts[y * width + x] = s.part;
			tones[y * width + x] = clamp01(s.tone);
		}
	}

	// A seam: where two different parts touch, the cell of the later part is cleared.
	const seam: number[] = [];
	const at = (x: number, y: number) =>
		x < 0 || y < 0 || x >= width || y >= height
			? 0
			: (parts[y * width + x] ?? 0);
	for (let y = 0; y < height; y++) {
		for (let x = 0; x < width; x++) {
			const p = at(x, y);
			if (p === 0) continue;
			for (const q of [
				at(x - 1, y),
				at(x + 1, y),
				at(x, y - 1),
				at(x, y + 1),
			]) {
				if (q !== 0 && q !== p && p > q) {
					seam.push(y * width + x);
					break;
				}
			}
		}
	}
	for (const index of seam) parts[index] = 0;

	// Halo and floor shadow, then clear everything within MARGIN of the object.
	const ambient = new Float32Array(width * height);
	const halo = HALO[name];
	for (let y = 0; y < height; y++) {
		for (let x = 0; x < width; x++) {
			const d = Math.hypot(x + 0.5 - halo.c[0], y + 0.5 - halo.c[1]);
			const glow = 0.2 * clamp01(1 - d / halo.r) ** 1.4;
			const sx = (x + 0.5 - 56) / 30;
			const sy = (y + 0.5 - 67) / 3.5;
			const floor = 0.5 * clamp01(1 - (sx * sx + sy * sy));
			ambient[y * width + x] = Math.max(glow, floor);
		}
	}
	for (let y = 0; y < height; y++) {
		for (let x = 0; x < width; x++) {
			if ((parts[y * width + x] ?? 0) === 0) continue;
			for (let dy = -MARGIN; dy <= MARGIN; dy++) {
				for (let dx = -MARGIN; dx <= MARGIN; dx++) {
					const nx = x + dx;
					const ny = y + dy;
					if (nx >= 0 && ny >= 0 && nx < width && ny < height) {
						ambient[ny * width + nx] = 0;
					}
				}
			}
		}
	}
	const scene = { width, height, parts, tones, ambient };
	scenes.set(name, scene);
	return scene;
}

export interface IllustrationPaths {
	/** Inked cells of the object, in cell units. */
	object: string;
	/** Inked cells of the halo and floor shadow. */
	ambient: string;
}

const pathCache = new Map<string, IllustrationPaths>();

function rowRuns(
	width: number,
	height: number,
	inked: (x: number, y: number) => boolean,
): string {
	let d = "";
	for (let y = 0; y < height; y++) {
		let start = -1;
		for (let x = 0; x <= width; x++) {
			const on = x < width && inked(x, y);
			if (on && start === -1) start = x;
			else if (!on && start !== -1) {
				d += `M${start} ${y}h${x - start}v1h${start - x}z`;
				start = -1;
			}
		}
	}
	return d;
}

/** The two SVG paths (object, ambient) of an illustration for a matrix, in cell units. Cached. */
export function illustrationPaths(
	name: EmptyStateIllustration,
	matrix: DitherMatrix,
): IllustrationPaths {
	const key = `${name}|${matrix}`;
	const hit = pathCache.get(key);
	if (hit) return hit;
	const scene = illustrationScene(name);
	const { width, height } = scene;
	const paths = {
		object: rowRuns(
			width,
			height,
			(x, y) =>
				(scene.parts[y * width + x] ?? 0) > 0 &&
				cellInk("gradient", scene.tones[y * width + x] ?? 0, x, y, matrix) > 0,
		),
		ambient: rowRuns(
			width,
			height,
			(x, y) =>
				cellInk("gradient", scene.ambient[y * width + x] ?? 0, x, y, matrix) >
				0,
		),
	};
	pathCache.set(key, paths);
	return paths;
}
