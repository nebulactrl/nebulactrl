import {
	type BayerSize,
	bayerMatrixName,
	type DitherMatrix,
	ditherOn,
} from "../../lib/bayer";
import { type DitherFillVariant, fillTile } from "./fill";

/**
 * Banded dither masks: the box is cut into equal bands along one axis and each
 * band is dithered at its own tone, so density can fall off across a surface
 * (a highlight thinning out from the top edge, a rule fading at its ends).
 *
 * A flat dither tile cannot do this: stacking a gradient on top of it fades
 * the dots' opacity, not their density, and the finish stops reading as
 * dither. The mask is an SVG that fills the box it is applied to (no viewBox,
 * so the pattern stays in CSS px and cells stay crisp at any size).
 */
export interface BandMask {
	/** `y`: bands stack top to bottom. `x`: bands run left to right. */
	axis: "x" | "y";
	/** One tone (0..1) per band; 0 leaves the band empty. */
	stops: readonly number[];
	/** Cell edge in CSS px. */
	cell?: number;
	n?: BayerSize;
	/** Texture of every band. Setting it (or `matrix`) switches the mask to the textured builder. */
	texture?: DitherFillVariant;
	/** Threshold pattern, wins over `n`. `blue-noise` tiles on a 16×16 cell tile. */
	matrix?: DitherMatrix;
}

export type DitherTest = (
	tone: number,
	x: number,
	y: number,
	n: BayerSize,
) => boolean;

const DEFAULT_CELL = 2;
const DEFAULT_N: BayerSize = 4;

const clamp01 = (value: number) => Math.min(1, Math.max(0, value));

/** `steps` tones easing linearly from `from` to `to` (inclusive). */
export function rampStops(from: number, to: number, steps: number): number[] {
	if (steps <= 1) return [clamp01(from)];
	return Array.from({ length: steps }, (_, i) =>
		clamp01(from + ((to - from) * i) / (steps - 1)),
	);
}

/** Path (px) of the inked cells of one n×n tile at `tone`. */
export function tilePath(
	tone: number,
	cell: number,
	n: BayerSize,
	test: DitherTest = ditherOn,
): string {
	let d = "";
	for (let y = 0; y < n; y++) {
		for (let x = 0; x < n; x++) {
			if (test(tone, x, y, n))
				d += `M${x * cell} ${y * cell}h${cell}v${cell}h-${cell}z`;
		}
	}
	return d;
}

const pct = (value: number) => `${Math.round(value * 10_000) / 100}%`;

/** The SVG source for a band mask (alpha is the only channel that matters). */
export function bandMaskSvg(
	mask: BandMask,
	test: DitherTest = ditherOn,
): string {
	const cell = Math.max(1, mask.cell ?? DEFAULT_CELL);
	const n = mask.n ?? DEFAULT_N;
	const bands = mask.stops.length;
	const textured = mask.texture !== undefined || mask.matrix !== undefined;
	const texture = mask.texture ?? "gradient";
	const matrix = mask.matrix ?? bayerMatrixName(n);
	const tileFor = (tone: number) => {
		if (textured) return fillTile(texture, matrix, tone, cell);
		return { d: tilePath(tone, cell, n, test), size: cell * n, opacity: 1 };
	};
	const tiles = new Map<number, string>();
	let rects = "";
	mask.stops.forEach((raw, index) => {
		const tone = Math.round(clamp01(raw) * 1000) / 1000;
		if (tone <= 0) return;
		if (tileFor(tone).d === "") return;
		let id = tiles.get(tone);
		if (id === undefined) {
			id = `p${tiles.size}`;
			tiles.set(tone, id);
		}
		const start = index / bands;
		const size = 1 / bands;
		rects +=
			mask.axis === "y"
				? `<rect x="0" y="${pct(start)}" width="100%" height="${pct(size)}" fill="url(#${id})"/>`
				: `<rect x="${pct(start)}" y="0" width="${pct(size)}" height="100%" fill="url(#${id})"/>`;
	});
	let defs = "";
	for (const [tone, id] of tiles) {
		const tile = tileFor(tone);
		const opacity = tile.opacity < 1 ? ` fill-opacity="${tile.opacity}"` : "";
		defs += `<pattern id="${id}" width="${tile.size}" height="${tile.size}" patternUnits="userSpaceOnUse"><path d="${tile.d}" fill="#000"${opacity}/></pattern>`;
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" width="100%" height="100%" shape-rendering="crispEdges"><defs>${defs}</defs>${rects}</svg>`;
}

/** A stable key for caching: equal masks share one CSS class. */
export function bandMaskKey(mask: BandMask): string {
	return `${mask.axis}|${mask.cell ?? DEFAULT_CELL}|${mask.n ?? DEFAULT_N}|${mask.texture ?? ""}|${mask.matrix ?? ""}|${mask.stops.map((s) => Math.round(s * 1000)).join(",")}`;
}

export function bandMaskDataUri(
	mask: BandMask,
	test: DitherTest = ditherOn,
): string {
	return `data:image/svg+xml,${encodeURIComponent(bandMaskSvg(mask, test))}`;
}

const classes = new Map<string, string>();
let sheet: HTMLStyleElement | null = null;

/**
 * The CSS class that applies a band mask. The rule is injected once into a
 * single `<style>` element, so fifty skeletons share one data URI instead of
 * each carrying it in an inline style.
 */
export function bandMaskClass(mask: BandMask): string {
	const key = bandMaskKey(mask);
	const hit = classes.get(key);
	if (hit) return hit;
	const name = `nc-dm-${classes.size}`;
	classes.set(key, name);
	if (typeof document !== "undefined") {
		if (!sheet) {
			sheet = document.createElement("style");
			sheet.setAttribute("data-nc-dither-masks", "");
			document.head.appendChild(sheet);
		}
		const url = `url("${bandMaskDataUri(mask)}")`;
		sheet.appendChild(
			document.createTextNode(
				`.${name}{-webkit-mask-image:${url};mask-image:${url};-webkit-mask-size:100% 100%;mask-size:100% 100%;-webkit-mask-repeat:no-repeat;mask-repeat:no-repeat}`,
			),
		);
	}
	return name;
}
