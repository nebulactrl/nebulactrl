import {
	type DitherMatrix,
	matrixTileSide,
	type TileSide,
	tileRanks,
} from "../../lib/bayer";
import { ditherColorVar } from "../../lib/colors";

/**
 * The vocabulary every interface piece shares: how a tone becomes cells
 * (`fill`), which threshold pattern does it (`matrix`), what colour the ink is
 * and how much it glows. Pure: no DOM, so it runs in tests and on the server.
 */

/** Texture of a dithered area. `gradient` steps through ordered-dither levels; the rest keep a texture. */
export const FILL_VARIANTS = [
	"gradient",
	"dotted",
	"hatched",
	"solid",
] as const;
export type DitherFillVariant = (typeof FILL_VARIANTS)[number];

/** A soft glow in the piece's colour. */
export const BLOOM_LEVELS = ["off", "low", "high"] as const;
export type DitherBloom = (typeof BLOOM_LEVELS)[number];

/** A palette name (`"blue"`), a custom property name (`"--chart-1"`) or any CSS colour. */
export type DitherColor = string;

/** `color` as something CSS can paint with; `fallback` names the default ink. */
export function inkCss(
	color: DitherColor | undefined,
	fallback: string = "ink",
): string {
	return ditherColorVar(color ?? fallback);
}

/** `color-mix` of two CSS colours; `pct` is the share of `a`. */
export function mix(a: string, b: string, pct: number): string {
	return `color-mix(in oklab, ${a} ${Math.round(pct)}%, ${b})`;
}

/** The `box-shadow` of a glow in `color`, or `undefined` when it is off. */
export function bloomShadow(
	color: string,
	bloom: DitherBloom | undefined,
): string | undefined {
	if (bloom === "low") return `0 0 12px 0 ${mix(color, "transparent", 35)}`;
	if (bloom === "high") return `0 0 22px 2px ${mix(color, "transparent", 55)}`;
	return undefined;
}

/** The `drop-shadow` of a glow for elements whose box is not their shape (canvas). */
export function bloomFilter(
	color: string,
	bloom: DitherBloom | undefined,
): string | undefined {
	if (bloom === "low")
		return `drop-shadow(0 0 5px ${mix(color, "transparent", 45)})`;
	if (bloom === "high")
		return `drop-shadow(0 0 10px ${mix(color, "transparent", 70)})`;
	return undefined;
}

export const clamp01 = (value: number) => Math.min(1, Math.max(0, value));

/** Inked share of the dotted lattice (every other column, every other row, staggered). */
const DOT_DENSITY = 1 / 4;
/** Inked share of the hatch (two of every four diagonals). */
const HATCH_DENSITY = 1 / 2;

/** Is cell (x, y) on the staggered dot lattice? Period 4 in both axes. */
export function onDotLattice(x: number, y: number): boolean {
	const col = ((x % 4) + 4) % 4;
	const row = ((y % 4) + 4) % 4;
	return col % 2 === 0 && (row + (col >> 1)) % 2 === 0;
}

/** Is cell (x, y) on a rising diagonal stripe? Period 4. */
export function onHatch(x: number, y: number): boolean {
	return (((x + y) % 4) + 4) % 4 < 2;
}

/** Side of the tile a fill repeats on: the matrix's, widened to 4 so a lattice or hatch fits. */
export function fillTileSide(
	fill: DitherFillVariant,
	matrix: DitherMatrix,
): TileSide {
	const side = matrixTileSide(matrix);
	return (fill === "dotted" || fill === "hatched") && side < 4 ? 4 : side;
}

const wrap = (value: number, n: number) => ((value % n) + n) % n;

function ordered(
	tone: number,
	x: number,
	y: number,
	side: number,
	ranks: ArrayLike<number>,
): boolean {
	const count = Math.min(
		side * side,
		Math.max(0, Math.round(clamp01(tone) * side * side)),
	);
	const rank = ranks[wrap(y, side) * side + wrap(x, side)];
	return rank !== undefined && rank < count;
}

/**
 * How inked cell (x, y) is at `tone` for a fill and matrix: 0 or 1 for the
 * dithered fills, the tone itself for `solid` (a plain tint). Denser tones are
 * always a superset of sparser ones. `dotted` and `hatched` keep their texture
 * and let the tone decide how much of it shows.
 */
export function cellInk(
	fill: DitherFillVariant,
	tone: number,
	x: number,
	y: number,
	matrix: DitherMatrix = "bayer4",
): number {
	if (tone <= 0) return 0;
	if (fill === "solid") return clamp01(tone);
	const side = matrixTileSide(matrix);
	const ranks = tileRanks(side);
	if (fill === "gradient") return ordered(tone, x, y, side, ranks) ? 1 : 0;
	if (fill === "dotted") {
		if (!onDotLattice(x, y)) return 0;
		return ordered(tone / DOT_DENSITY, x, y, side, ranks) ? 1 : 0;
	}
	if (!onHatch(x, y)) return 0;
	return ordered(tone / HATCH_DENSITY, x, y, side, ranks) ? 1 : 0;
}

/** One inked tile: path of the inked cells in px, the tile edge in px, and the ink's opacity. */
export interface FillTile {
	d: string;
	/** Tile edge in px. */
	size: number;
	/** 1 for dithered fills; the tone for `solid`. */
	opacity: number;
}

const tiles = new Map<string, FillTile>();

/**
 * The tile that paints `tone` with a fill, as an SVG path in CSS px (rows of
 * inked cells merged into one rect each). Cached by fill, matrix, cell and the
 * tone's inked-cell count.
 */
export function fillTile(
	fill: DitherFillVariant,
	matrix: DitherMatrix,
	tone: number,
	cell: number,
): FillTile {
	const side = fillTileSide(fill, matrix);
	const px = Math.max(1, cell);
	const size = side * px;
	if (fill === "solid") {
		const opacity = Math.round(clamp01(tone) * 1000) / 1000;
		return {
			d: opacity > 0 ? `M0 0h${size}v${size}h-${size}z` : "",
			size,
			opacity,
		};
	}
	const key = `${fill}|${matrix}|${px}|${Math.round(clamp01(tone) * side * side * 4)}`;
	const hit = tiles.get(key);
	if (hit) return hit;
	let d = "";
	for (let y = 0; y < side; y++) {
		let start = -1;
		for (let x = 0; x <= side; x++) {
			const on = x < side && cellInk(fill, tone, x, y, matrix) > 0;
			if (on && start === -1) start = x;
			else if (!on && start !== -1) {
				d += `M${start * px} ${y * px}h${(x - start) * px}v${px}h-${(x - start) * px}z`;
				start = -1;
			}
		}
	}
	const tile = { d, size, opacity: 1 };
	tiles.set(key, tile);
	return tile;
}
