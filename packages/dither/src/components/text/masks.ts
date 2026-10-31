import { type BayerSize, bayerRanks } from "../../lib/bayer";
import { svgToDataUri } from "../../lib/export";
import { ditherMaskDataUri } from "../../lib/pattern";

/** Mask steps from empty (0) to solid (16). */
export const MASK_STEPS = 16;

/** CSS `url("…")` of the mask with `step / 16` of its cells inked: cells of rank < ⌈step·n²/16⌉. */
export function maskUrl(
	step: number,
	cell: number,
	n: BayerSize = 4,
	shape: "square" | "dot" | "hatch" = "square",
): string {
	return `url("${ditherMaskDataUri({ tone: step / MASK_STEPS, cell, n, shape })}")`;
}

/**
 * The exact complement of `maskUrl(step, …)` for square cells: the cells that
 * are *not* inked at that step. Two layers masked with a step and its
 * complement never overlap and never leave a gap.
 */
export function complementMaskUrl(
	step: number,
	cell: number,
	n: BayerSize = 4,
): string {
	const ranks = bayerRanks(n);
	const count = Math.min(
		n * n,
		Math.max(0, Math.round((step / MASK_STEPS) * n * n)),
	);
	let path = "";
	for (let y = 0; y < n; y += 1) {
		let start = -1;
		for (let x = 0; x <= n; x += 1) {
			const inked = x < n && (ranks[y * n + x] ?? 0) >= count;
			if (inked && start === -1) start = x;
			else if (!inked && start !== -1) {
				path += `M${start} ${y}h${x - start}v1h${start - x}z`;
				start = -1;
			}
		}
	}
	const tile = n * cell;
	const body = path === "" ? "" : `<path d="${path}"/>`;
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${tile}" height="${tile}" viewBox="0 0 ${n} ${n}" shape-rendering="crispEdges">${body}</svg>`;
	return `url("${svgToDataUri(svg)}")`;
}

/** Inline styles that make a mask tile at `n * cell` px. */
export function maskTiling(
	cell: number,
	n: BayerSize = 4,
): {
	maskRepeat: "repeat";
	maskSize: string;
	WebkitMaskRepeat: "repeat";
	WebkitMaskSize: string;
} {
	const size = `${n * cell}px ${n * cell}px`;
	return {
		maskRepeat: "repeat",
		maskSize: size,
		WebkitMaskRepeat: "repeat",
		WebkitMaskSize: size,
	};
}

/**
 * The cells of a fade band `rows` tall and `n` wide: the outermost row is the
 * sparsest and each row inward inks more, so a digit window thins out toward
 * its edge instead of being cut off. Row 0 of `"top"` is the outer edge; row 0
 * of `"bottom"` is the innermost. Uses the Bayer matrix of size `n`.
 */
export function edgeBandRows(
	n: BayerSize,
	rows: number,
	edge: "top" | "bottom",
): boolean[][] {
	const ranks = bayerRanks(n);
	const cells = n * n;
	return Array.from({ length: rows }, (_, row) => {
		const depth = edge === "top" ? row + 1 : rows - row;
		const count = Math.round((depth / (rows + 1)) * cells);
		return Array.from({ length: n }, (_, x) => {
			const rank = ranks[(row % n) * n + x] ?? 0;
			return rank < count;
		});
	});
}

/** CSS `url("…")` of one fade band, `n * cell` wide and `rows * cell` tall. */
export function edgeBandUrl(
	n: BayerSize,
	rows: number,
	cell: number,
	edge: "top" | "bottom",
): string {
	let path = "";
	edgeBandRows(n, rows, edge).forEach((row, y) => {
		let start = -1;
		for (let x = 0; x <= n; x += 1) {
			const inked = x < n && row[x] === true;
			if (inked && start === -1) start = x;
			else if (!inked && start !== -1) {
				path += `M${start} ${y}h${x - start}v1h${start - x}z`;
				start = -1;
			}
		}
	});
	const body = path === "" ? "" : `<path d="${path}"/>`;
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${n * cell}" height="${rows * cell}" viewBox="0 0 ${n} ${rows}" shape-rendering="crispEdges">${body}</svg>`;
	return `url("${svgToDataUri(svg)}")`;
}

/**
 * Mask styles that dither the top and bottom `rows` cell rows of an element
 * out: a sparse-to-dense band at each edge around a solid middle. Apply while
 * a digit strip moves, so digits fade in and out of the line.
 */
export function edgeFadeMask(
	cell: number,
	rows: number,
	n: BayerSize = 4,
): Record<string, string> {
	const tile = `${n * cell}px ${rows * cell}px`;
	const image = `${edgeBandUrl(n, rows, cell, "top")}, ${edgeBandUrl(n, rows, cell, "bottom")}, linear-gradient(#000, #000)`;
	const size = `${tile}, ${tile}, 100% calc(100% - ${2 * rows * cell}px)`;
	const position = `0 0, 0 100%, 0 ${rows * cell}px`;
	const repeat = "repeat-x, repeat-x, no-repeat";
	return {
		maskImage: image,
		WebkitMaskImage: image,
		maskSize: size,
		WebkitMaskSize: size,
		maskPosition: position,
		WebkitMaskPosition: position,
		maskRepeat: repeat,
		WebkitMaskRepeat: repeat,
	};
}
