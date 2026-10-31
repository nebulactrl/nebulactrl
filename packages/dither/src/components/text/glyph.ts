import type { CellGrid } from "../images/grid";

export interface GlyphRaster {
	grid: CellGrid;
	/** Glyph coverage per cell, 0..255, `grid.cols × grid.rows`. */
	coverage: Uint8ClampedArray<ArrayBuffer>;
}

let scratch: HTMLCanvasElement | null = null;

/**
 * Draw `text` the way `el` lays it out (its computed font, size, letter
 * spacing, line height and alignment) onto a canvas one pixel per dither
 * cell, and return the coverage. The canvas is scaled by `1 / grid.cssCell`, so
 * the glyphs land on the same CSS-pixel positions the real text occupies.
 * Lines break only at `\n`: the wrapper is expected to be `white-space: pre`.
 */
export function rasterizeText(
	el: HTMLElement,
	text: string,
	grid: CellGrid,
): GlyphRaster | null {
	scratch ??= document.createElement("canvas");
	scratch.width = grid.cols;
	scratch.height = grid.rows;
	const ctx = scratch.getContext("2d", { willReadFrequently: true });
	if (!ctx) return null;
	ctx.clearRect(0, 0, grid.cols, grid.rows);

	const cs = getComputedStyle(el);
	const fontSize = Number.parseFloat(cs.fontSize) || 16;
	const parsedLine = Number.parseFloat(cs.lineHeight);
	const lineHeight = Number.isFinite(parsedLine) ? parsedLine : fontSize * 1.2;
	const spacing = Number.parseFloat(cs.letterSpacing);

	ctx.setTransform(1 / grid.cssCell, 0, 0, 1 / grid.cssCell, 0, 0);
	ctx.font = `${cs.fontStyle} ${cs.fontWeight} ${fontSize}px ${cs.fontFamily}`;
	ctx.letterSpacing = Number.isFinite(spacing) ? `${spacing}px` : "0px";
	ctx.textBaseline = "alphabetic";
	ctx.fillStyle = "#000";
	const align = cs.textAlign;
	const textAlign =
		align === "center"
			? "center"
			: align === "right" || align === "end"
				? "right"
				: "left";
	ctx.textAlign = textAlign;

	const metrics = ctx.measureText("Hg");
	const ascent = metrics.fontBoundingBoxAscent ?? fontSize * 0.8;
	const descent = metrics.fontBoundingBoxDescent ?? fontSize * 0.2;
	const width = grid.cssWidth;
	const x =
		textAlign === "center" ? width / 2 : textAlign === "right" ? width : 0;
	const lines = (
		cs.textTransform === "uppercase" ? text.toUpperCase() : text
	).split("\n");
	lines.forEach((line, i) => {
		const top = i * lineHeight;
		const baseline = top + (lineHeight - (ascent + descent)) / 2 + ascent;
		ctx.fillText(line, x, baseline);
	});

	const { data } = ctx.getImageData(0, 0, grid.cols, grid.rows);
	const coverage = new Uint8ClampedArray(grid.cols * grid.rows);
	for (let p = 0; p < coverage.length; p += 1)
		coverage[p] = data[p * 4 + 3] ?? 0;
	return { grid, coverage };
}
