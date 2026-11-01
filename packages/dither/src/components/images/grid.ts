/**
 * Cell-grid maths shared by the text and image components. A dither cell is a
 * whole number of device pixels, so blocks stay crisp at fractional device
 * pixel ratios (1.25, 1.5) instead of alternating between 3 and 4 px wide.
 */

/** The `--dither-cell` fallback, in CSS px. */
export const DEFAULT_CELL_PX = 3;

/** Largest grid either side; beyond it the cell grows rather than the canvas. */
export const MAX_GRID = 2048;

export interface CellGrid {
	/** Device px per cell, a whole number ≥ 1. */
	cellDev: number;
	/** The same cell in CSS px (`cellDev / dpr`), used to size the canvas. */
	cssCell: number;
	cols: number;
	rows: number;
	/** CSS px the canvas occupies: a whole number of cells, may overhang the box by < 1 cell. */
	cssWidth: number;
	cssHeight: number;
}

/** Round a CSS cell size to whole device pixels (never below 1). */
export function snapCell(
	cell: number,
	dpr: number,
): { cellDev: number; cssCell: number } {
	const ratio = dpr > 0 ? dpr : 1;
	const cellDev = Math.max(1, Math.round(cell * ratio));
	return { cellDev, cssCell: cellDev / ratio };
}

/** The grid that covers a `width × height` CSS px box with cells of `cell` CSS px. */
export function gridFor(
	width: number,
	height: number,
	cell: number,
	dpr: number,
	max = MAX_GRID,
): CellGrid {
	const ratio = dpr > 0 ? dpr : 1;
	let { cellDev } = snapCell(cell, ratio);
	const needed = Math.max(width, height) * ratio;
	// Grow the cell until the grid fits `max`, so a huge box never allocates a huge canvas.
	if (needed / cellDev > max) cellDev = Math.ceil(needed / max);
	const cols = Math.max(1, Math.ceil((width * ratio) / cellDev));
	const rows = Math.max(1, Math.ceil((height * ratio) / cellDev));
	return {
		cellDev,
		cssCell: cellDev / ratio,
		cols,
		rows,
		cssWidth: (cols * cellDev) / ratio,
		cssHeight: (rows * cellDev) / ratio,
	};
}

/** `--dither-cell` as a number of CSS px, or the default when it is unset or not a length. */
export function readCellVar(el?: Element): number {
	if (typeof document === "undefined") return DEFAULT_CELL_PX;
	const raw = getComputedStyle(el ?? document.documentElement)
		.getPropertyValue("--dither-cell")
		.trim();
	const value = Number.parseFloat(raw);
	return Number.isFinite(value) && value >= 1 ? value : DEFAULT_CELL_PX;
}

/** A preset's cell shrinks, down to 1 px, to keep at least this many columns. */
export const MIN_COLUMNS = 200;

/**
 * The cell for a box `width` CSS px wide: `base`, or smaller (never below 1)
 * so the grid keeps at least `minColumns` columns. `minColumns` 0 keeps
 * `base`, which is what an explicit `cell` prop asks for.
 */
export function effectiveCell(
	base: number,
	width: number,
	minColumns: number,
): number {
	if (minColumns <= 0 || width <= 0) return base;
	return Math.min(base, Math.max(1, width / minColumns));
}

/**
 * The sizes to halve a `srcW × srcH` source through on its way down to
 * `dstW × dstH`, largest first, ending above the target. Halving in steps
 * keeps fine detail from aliasing into noise when the downscale is large.
 * Empty when one `drawImage` is enough.
 */
export function halvingSizes(
	srcW: number,
	srcH: number,
	dstW: number,
	dstH: number,
): Array<{ width: number; height: number }> {
	const steps: Array<{ width: number; height: number }> = [];
	let width = srcW;
	let height = srcH;
	while (width / 2 > dstW && height / 2 > dstH) {
		width = Math.ceil(width / 2);
		height = Math.ceil(height / 2);
		steps.push({ width, height });
	}
	return steps;
}

/**
 * The aspect ratio to reserve for an image box: the `ratio` prop, else the
 * image's own once it has loaded. A Tailwind `aspect-*` class on the box wins
 * over the image's own ratio, so it is left alone.
 */
export function boxAspect(
	ratio: number | undefined,
	natural: { width: number; height: number } | null,
	className: string | undefined,
): number | undefined {
	if (ratio) return ratio;
	if (className && /(?:^|\s)(?:[\w-]+:)*aspect-/.test(className))
		return undefined;
	return natural && natural.height > 0
		? natural.width / natural.height
		: undefined;
}
