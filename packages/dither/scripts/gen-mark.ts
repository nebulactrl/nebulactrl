/**
 * Generates `src/components/images/nebula-mark-g.generated.ts`: the dither
 * layer of the NebulaCtrl mark, variant G (an embossed mark over a Bayer
 * dithered drop shadow).
 *
 * The petals are flattened and filled into a mask, the mask is reduced to a
 * window grid of cells, and the cells of an offset copy of the mask that stay
 * outside the mark itself are thresholded with a 4x4 Bayer matrix at falling
 * tone. Only the shadow cells are generated, as one path per display tier in
 * cell units; the bevel and the gradient are vector and live in the component.
 *
 * Run `bun run gen:mark` from packages/dither after changing the geometry.
 */

import { NEBULA_MARK_PETALS } from "../src/components/images/nebula-mark";
import type { MarkGTier } from "../src/components/images/nebula-mark-g.generated";

/** The drawing window in mark units: the mark plus room for the shadow on the right and bottom. */
export const WINDOW = { x: -14, y: -14, size: 346 } as const;
/** Where the petals are rasterised: the favicon's window on the mark. */
const SOURCE = { x: -8, y: -8, size: 324 } as const;
const PIXELS_PER_UNIT = 2;
const RASTER = SOURCE.size * PIXELS_PER_UNIT;

/** Display sizes the kit draws; the component picks the nearest. */
export const TIER_SIZES = [24, 28, 32, 40, 48, 64, 96, 128] as const;

/** From this display size the cells are 2 px, below it 1 px. */
export const COARSE_FROM = 96;

type Point = readonly [number, number];

/** Flatten an absolute `M … C … Z` path into one polygon per subpath. */
export function flatten(d: string, steps = 14): Point[][] {
	const tokens = d.match(/[MCLZ]|-?\d*\.?\d+/g) ?? [];
	const polygons: Point[][] = [];
	let current: Point[] = [];
	let at: Point = [0, 0];
	let command = "";
	let i = 0;
	const next = () => Number(tokens[i++]);
	while (i < tokens.length) {
		const token = tokens[i] ?? "";
		if (/[MCLZ]/.test(token)) {
			command = token;
			i += 1;
			if (command === "Z") {
				if (current.length > 0) polygons.push(current);
				current = [];
				continue;
			}
		}
		if (command === "M") {
			if (current.length > 0) polygons.push(current);
			at = [next(), next()];
			current = [at];
			command = "L";
		} else if (command === "L") {
			at = [next(), next()];
			current.push(at);
		} else if (command === "C") {
			const p1: Point = [next(), next()];
			const p2: Point = [next(), next()];
			const p3: Point = [next(), next()];
			for (let s = 1; s <= steps; s += 1) {
				const t = s / steps;
				const u = 1 - t;
				const w0 = u * u * u;
				const w1 = 3 * u * u * t;
				const w2 = 3 * u * t * t;
				const w3 = t * t * t;
				current.push([
					w0 * at[0] + w1 * p1[0] + w2 * p2[0] + w3 * p3[0],
					w0 * at[1] + w1 * p1[1] + w2 * p2[1] + w3 * p3[1],
				]);
			}
			at = p3;
		} else {
			i += 1;
		}
	}
	if (current.length > 0) polygons.push(current);
	return polygons;
}

/** Nonzero-winding scanline fill of every polygon into one mask (1 inside). */
export function fillMask(
	polygons: Point[][],
	side: number,
	origin: number,
	perUnit: number,
): Uint8Array {
	const mask = new Uint8Array(side * side);
	for (let row = 0; row < side; row += 1) {
		const y = origin + (row + 0.5) / perUnit;
		const crossings: [number, number][] = [];
		for (const polygon of polygons) {
			for (let k = 0; k < polygon.length; k += 1) {
				const a = polygon[k];
				const b = polygon[(k + 1) % polygon.length];
				if (!a || !b || a[1] <= y === b[1] <= y) continue;
				crossings.push([
					a[0] + ((y - a[1]) / (b[1] - a[1])) * (b[0] - a[0]),
					b[1] > a[1] ? 1 : -1,
				]);
			}
		}
		crossings.sort((p, q) => p[0] - q[0]);
		let winding = 0;
		for (let k = 0; k < crossings.length - 1; k += 1) {
			winding += crossings[k]?.[1] ?? 0;
			if (winding === 0) continue;
			const from = Math.max(
				0,
				Math.round(((crossings[k]?.[0] ?? 0) - origin) * perUnit),
			);
			const to = Math.min(
				side,
				Math.round(((crossings[k + 1]?.[0] ?? 0) - origin) * perUnit),
			);
			for (let x = from; x < to; x += 1) mask[row * side + x] = 1;
		}
	}
	return mask;
}

/** The petals of the mark as a mask over the source window. */
export function petalMask(): Uint8Array {
	const mask = new Uint8Array(RASTER * RASTER);
	for (const d of NEBULA_MARK_PETALS) {
		const one = fillMask(flatten(d), RASTER, SOURCE.x, PIXELS_PER_UNIT);
		for (let p = 0; p < one.length; p += 1) if (one[p]) mask[p] = 1;
	}
	return mask;
}

/** Fraction of each cell of an n x n grid over the drawing window that the mask covers. */
export function windowCover(mask: Uint8Array, n: number): Float32Array {
	const cell = WINDOW.size / n;
	const cover = new Float32Array(n * n);
	const weight = 1 / PIXELS_PER_UNIT / cell;
	for (let y = 0; y < RASTER; y += 1) {
		const cy = Math.floor(
			(SOURCE.y + (y + 0.5) / PIXELS_PER_UNIT - WINDOW.y) / cell,
		);
		if (cy < 0 || cy >= n) continue;
		for (let x = 0; x < RASTER; x += 1) {
			if (!mask[y * RASTER + x]) continue;
			const cx = Math.floor(
				(SOURCE.x + (x + 0.5) / PIXELS_PER_UNIT - WINDOW.x) / cell,
			);
			if (cx < 0 || cx >= n) continue;
			cover[cy * n + cx] = (cover[cy * n + cx] ?? 0) + weight * weight;
		}
	}
	return cover;
}

/** Bayer threshold matrix of side `size` (a power of two), values 0..size²-1. */
export function bayer(size: number): number[][] {
	if (size === 1) return [[0]];
	const half = size / 2;
	const quarter = bayer(half);
	const matrix = Array.from({ length: size }, () =>
		new Array<number>(size).fill(0),
	);
	for (let y = 0; y < half; y += 1) {
		for (let x = 0; x < half; x += 1) {
			const v = (quarter[y]?.[x] ?? 0) * 4;
			(matrix[y] as number[])[x] = v;
			(matrix[y] as number[])[x + half] = v + 2;
			(matrix[y + half] as number[])[x] = v + 3;
			(matrix[y + half] as number[])[x + half] = v + 1;
		}
	}
	return matrix;
}

const BAYER_4 = bayer(4);

/** A cell is inked when `level` (0..1) beats its Bayer threshold. */
export function inked(level: number, x: number, y: number): boolean {
	const threshold = ((BAYER_4[y % 4]?.[x % 4] ?? 0) + 0.5) / 16;
	return level > threshold;
}

/** Cell size in px at a display size. */
export const cellPx = (size: number): number => (size >= COARSE_FROM ? 2 : 1);

/** Shadow tone per step away from the mark, by cell size. */
export const shadowLevels = (cell: number): number[] =>
	cell === 2 ? [0.72, 0.5, 0.3] : [0.6, 0.32];

const round2 = (v: number) => Math.round(v * 100) / 100;

/** Cells of the shadow: outside the mark, one to three diagonal steps down-right of it. */
export function shadowCells(
	cover: Float32Array,
	n: number,
	levels: number[],
): boolean[] {
	const inside = Array.from(cover, (v) => v >= 0.5);
	return inside.map((isInside, k) => {
		if (isInside) return false;
		const x = k % n;
		const y = Math.floor(k / n);
		for (let step = 1; step <= levels.length; step += 1) {
			const sx = x - step;
			const sy = y - step;
			if (sx >= 0 && sy >= 0 && inside[sy * n + sx])
				return inked(levels[step - 1] ?? 0, x, y);
		}
		return false;
	});
}

/** Horizontal runs of set cells as `M x y h w v1 h-w z` subpaths in cell units. */
export function runsPath(on: boolean[], n: number): string {
	let d = "";
	for (let y = 0; y < n; y += 1) {
		let x = 0;
		while (x < n) {
			if (!on[y * n + x]) {
				x += 1;
				continue;
			}
			let end = x;
			while (end < n && on[y * n + end]) end += 1;
			d += `M${x} ${y}h${end - x}v1h${x - end}z`;
			x = end;
		}
	}
	return d;
}

export function buildTier(mask: Uint8Array, size: number): MarkGTier {
	const cell = cellPx(size);
	const cells = size / cell;
	const shadow = runsPath(
		shadowCells(windowCover(mask, cells), cells, shadowLevels(cell)),
		cells,
	);
	return { size, cells, bevel: round2(cell * (WINDOW.size / size)), shadow };
}

export function buildTiers(): MarkGTier[] {
	const mask = petalMask();
	return TIER_SIZES.map((size) => buildTier(mask, size));
}

/** The committed module for a list of tiers. */
export function moduleSource(tiers: MarkGTier[]): string {
	const rows = tiers
		.map(
			(t) =>
				`\t{\n\t\tsize: ${t.size},\n\t\tcells: ${t.cells},\n\t\tbevel: ${t.bevel},\n\t\tshadow: "${t.shadow}",\n\t},`,
		)
		.join("\n");
	return `// Generated by scripts/gen-mark.ts (\`bun run gen:mark\`). Do not edit.
// The dither layer of the NebulaCtrl mark, variant G: a 4x4 Bayer drop shadow per display size.

export interface MarkGTier {
	/** The display size this grid is built for, in px. */
	size: number;
	/** Cells across the window. */
	cells: number;
	/** Bevel band width in mark units. */
	bevel: number;
	/** Shadow cells as runs, in cell units. */
	shadow: string;
}

/** The drawing window in mark units: the mark plus room for the shadow on the right and bottom. */
export const MARK_G_WINDOW = { x: ${WINDOW.x}, y: ${WINDOW.y}, size: ${WINDOW.size} } as const;

export const MARK_G_TIERS: readonly MarkGTier[] = [
${rows}
];
`;
}

if (import.meta.main) {
	const out = new URL(
		"../src/components/images/nebula-mark-g.generated.ts",
		import.meta.url,
	);
	await Bun.write(out, moduleSource(buildTiers()));
	console.log(`wrote ${out.pathname}`);
}
