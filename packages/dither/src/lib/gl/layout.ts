/**
 * Pixel-grid math for DitherShader: how many cells cover a box, how big each
 * is in whole device pixels, and the canvas buffer that holds them.
 */

export const DEFAULT_CELL = 3;
export const MAX_DPR = 2;
/** Cells per axis the engine will allocate; larger boxes are coarsened. */
export const MAX_CELLS_PER_AXIS = 4096;
/** Canvas pixels drawn per cell for the dot shape, at most. */
const MAX_DOT_SUBDIVISION = 6;

export type DitherShape = "square" | "dot";

export interface GridLayout {
	/** Cells across and down. */
	cols: number;
	rows: number;
	/** Canvas pixels per cell along each axis (1 for squares, >1 for dots). */
	sub: number;
	/** Canvas buffer size in pixels. */
	canvasW: number;
	canvasH: number;
	/** Canvas CSS size in px; may overshoot the box by under one cell. */
	cssW: number;
	cssH: number;
	/** Cell size in whole device pixels, and in CSS pixels. */
	cellDev: number;
	cellCss: number;
	dpr: number;
	/** Box aspect ratio, width / height. */
	aspect: number;
}

export function clampDpr(raw: number): number {
	if (!Number.isFinite(raw) || raw <= 0) return 1;
	return Math.min(MAX_DPR, raw);
}

/**
 * Parse a `--dither-cell` custom property: "3px", "3", "0.25rem". Falls back
 * to DEFAULT_CELL for anything unusable.
 */
export function parseCellValue(raw: string | null | undefined): number {
	if (!raw) return DEFAULT_CELL;
	const text = raw.trim();
	const n = Number.parseFloat(text);
	if (!Number.isFinite(n) || n <= 0) return DEFAULT_CELL;
	if (text.endsWith("rem") || text.endsWith("em")) return n * 16;
	return n;
}

export interface SnappedCell {
	/** Device-pixel ratio after clamping. */
	dpr: number;
	/** Cell edge in whole device pixels (at least 1). */
	cellDev: number;
	/** The same edge in CSS px: what the grid actually draws. */
	cellCss: number;
}

/**
 * Round a cell to whole device pixels so neighbouring cells never alternate
 * between two sizes: a 3px cell is 6 device pixels at 2x, and 4.5 -> 5 at
 * 1.5x (3.33 CSS px). The result stays crisp at any DPR.
 */
export function snapCell(cellCss: number, rawDpr: number): SnappedCell {
	const dpr = clampDpr(rawDpr);
	const cellDev = Math.max(1, Math.round(cellCss * dpr));
	return { dpr, cellDev, cellCss: cellDev / dpr };
}

/**
 * CSS-pixel cell sizes (in half-pixel steps up to `max`) that already land on
 * whole device pixels at `dpr`; the sizes `snapCell` leaves untouched.
 */
export function crispCellSizes(rawDpr: number, max = 12): number[] {
	const dpr = clampDpr(rawDpr);
	const sizes: number[] = [];
	for (let css = 1; css <= max; css += 0.5) {
		const dev = css * dpr;
		if (Math.abs(dev - Math.round(dev)) < 1e-9) sizes.push(css);
	}
	return sizes;
}

/**
 * Canvas pixels per cell. Squares are one pixel per cell. Dots need several
 * to draw a round shape, and must divide the cell's device pixels so every
 * canvas pixel maps to a whole number of device pixels.
 */
export function chooseSubdivision(cellDev: number, shape: DitherShape): number {
	if (shape !== "dot" || cellDev < 3) return 1;
	for (let s = Math.min(cellDev, MAX_DOT_SUBDIVISION); s >= 2; s--) {
		if (cellDev % s === 0) return s;
	}
	return 1;
}

export function computeLayout(
	boxW: number,
	boxH: number,
	cellCss: number,
	rawDpr: number,
	shape: DitherShape,
): GridLayout {
	const { dpr, cellDev: snapped } = snapCell(cellCss, rawDpr);
	const w = Math.max(1, boxW);
	const h = Math.max(1, boxH);
	let cellDev = snapped;
	const needed = Math.max(w, h) * dpr;
	// Coarsen rather than allocate an enormous grid.
	if (needed / cellDev > MAX_CELLS_PER_AXIS) {
		cellDev = Math.ceil(needed / MAX_CELLS_PER_AXIS);
	}
	const cols = Math.max(1, Math.ceil((w * dpr) / cellDev - 1e-6));
	const rows = Math.max(1, Math.ceil((h * dpr) / cellDev - 1e-6));
	const sub = chooseSubdivision(cellDev, shape);
	return {
		cols,
		rows,
		sub,
		canvasW: cols * sub,
		canvasH: rows * sub,
		cssW: (cols * cellDev) / dpr,
		cssH: (rows * cellDev) / dpr,
		cellDev,
		cellCss: cellDev / dpr,
		dpr,
		aspect: w / h,
	};
}

export function sameLayout(a: GridLayout | null, b: GridLayout): boolean {
	return (
		a !== null &&
		a.cols === b.cols &&
		a.rows === b.rows &&
		a.sub === b.sub &&
		a.cssW === b.cssW &&
		a.cssH === b.cssH &&
		a.aspect === b.aspect
	);
}
