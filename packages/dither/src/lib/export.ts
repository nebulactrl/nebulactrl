/**
 * Turn a dither into a file: a PNG from a canvas, a compact SVG from a bitmap,
 * an ink mask or a pattern fill, and a CSS rule whose `background-image` is an
 * SVG data URI. The string builders are pure and run anywhere (tests, server);
 * `exportPNG`, `canvasToBitmap` and `downloadBlob` need a browser.
 */

import {
	type DitherMatrix,
	inkedCells,
	matrixTileSide,
	type TileSide,
} from "./bayer";
import {
	DITHER_COLOR_FALLBACKS,
	ditherColorVar,
	isDitherColorName,
} from "./colors";
import {
	DEFAULT_CELL,
	type DitherFill,
	type DitherRampOptions,
	ditherFillId,
	ditherMaskDataUri,
	ditherPatternSvg,
	ditherRampMask,
	ditherTilePath,
} from "./pattern";

/** RGBA pixels, four entries per pixel, row-major. */
export interface DitherBitmap {
	width: number;
	height: number;
	data: ArrayLike<number>;
}

/** One ink colour: any non-zero entry is inked. */
export interface InkMask {
	width: number;
	height: number;
	ink: ArrayLike<number>;
}

/** One palette index per cell, row-major; indices with no palette colour stay transparent. */
export interface IndexedBitmap {
	width: number;
	height: number;
	indices: ArrayLike<number>;
}

export type SvgSource =
	| { kind: "bitmap"; bitmap: DitherBitmap }
	| { kind: "indexed"; bitmap: IndexedBitmap; palette: readonly string[] }
	| { kind: "mask"; mask: InkMask; color: string }
	| { kind: "pattern"; fill: DitherFill; width: number; height: number };

export interface SvgOptions {
	/** Edge of one bitmap pixel in output px. Default 1. Ignored for patterns. */
	cell?: number;
	/**
	 * Paper colour painted as one rect underneath. `"auto"` (default for bitmaps)
	 * picks the most common opaque colour so only the other cells are drawn.
	 */
	background?: string | "auto" | null;
	/** Accessible name, written as `<title>` with `role="img"`. Omit for decoration. */
	title?: string;
}

const escapeAttr = (value: string) =>
	value.replace(/[&"<>]/g, (c) => `&#${c.charCodeAt(0)};`);

const hex2 = (value: number) => Math.round(value).toString(16).padStart(2, "0");

/** `#rrggbb` for 0..255 channels. */
export function rgbHex(r: number, g: number, b: number): string {
	return `#${hex2(r)}${hex2(g)}${hex2(b)}`;
}

/**
 * Percent-encode an SVG for a `data:` URI. Only characters that break CSS
 * `url("…")` or URI parsing are escaped, so the result stays readable and
 * smaller than `encodeURIComponent`. Double quotes become single quotes.
 */
export function svgToDataUri(svg: string): string {
	const body = svg
		.replace(/\s+/g, " ")
		.replace(/> </g, "><")
		.trim()
		.replace(/"/g, "'")
		.replace(/%/g, "%25")
		.replace(/#/g, "%23")
		.replace(/</g, "%3C")
		.replace(/>/g, "%3E");
	return `data:image/svg+xml,${body}`;
}

function svgOpen(
	width: number,
	height: number,
	vbW: number,
	vbH: number,
	title?: string,
): string {
	const a11y = title === undefined ? ' aria-hidden="true"' : ' role="img"';
	return `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${vbW} ${vbH}" shape-rendering="crispEdges"${a11y}>${
		title === undefined ? "" : `<title>${escapeAttr(title)}</title>`
	}`;
}

/** Horizontal runs of one ink as a single path in bitmap-pixel units. */
function runsPath(
	width: number,
	height: number,
	isInk: (p: number) => boolean,
): string {
	let path = "";
	for (let y = 0; y < height; y += 1) {
		let start = -1;
		for (let x = 0; x <= width; x += 1) {
			const inked = x < width && isInk(y * width + x);
			if (inked && start === -1) start = x;
			else if (!inked && start !== -1) {
				path += `M${start} ${y}h${x - start}v1h${start - x}z`;
				start = -1;
			}
		}
	}
	return path;
}

interface ColorGroup {
	hex: string;
	alpha: number;
	count: number;
	pixels: number[];
}

function groupByColor(bitmap: DitherBitmap): ColorGroup[] {
	const groups = new Map<string, ColorGroup>();
	const total = bitmap.width * bitmap.height;
	for (let p = 0; p < total; p += 1) {
		const alpha = bitmap.data[p * 4 + 3] ?? 0;
		if (alpha === 0) continue;
		const hex = rgbHex(
			bitmap.data[p * 4] ?? 0,
			bitmap.data[p * 4 + 1] ?? 0,
			bitmap.data[p * 4 + 2] ?? 0,
		);
		const key = `${hex}/${alpha}`;
		let group = groups.get(key);
		if (!group) {
			group = { hex, alpha, count: 0, pixels: [] };
			groups.set(key, group);
		}
		group.count += 1;
		group.pixels.push(p);
	}
	return [...groups.values()];
}

function bitmapSvg(bitmap: DitherBitmap, options: SvgOptions): string {
	const cell = Math.max(1, options.cell ?? 1);
	const { width, height } = bitmap;
	const groups = groupByColor(bitmap);
	const opaque = groups.filter((g) => g.alpha === 255);
	const fullyOpaque =
		opaque.length === groups.length &&
		opaque.reduce((n, g) => n + g.count, 0) === width * height;

	let background: string | null = null;
	if (options.background === "auto" || options.background === undefined) {
		if (fullyOpaque && groups.length > 1) {
			background =
				[...groups].sort((a, b) => b.count - a.count)[0]?.hex ?? null;
		}
	} else {
		background = options.background;
	}

	let body = background
		? `<rect width="${width}" height="${height}" fill="${escapeAttr(background)}"/>`
		: "";
	for (const group of groups) {
		if (background !== null && group.hex === background && group.alpha === 255)
			continue;
		const set = new Set(group.pixels);
		const opacity =
			group.alpha === 255
				? ""
				: ` fill-opacity="${(group.alpha / 255).toFixed(3).replace(/0+$/, "")}"`;
		body += `<path fill="${group.hex}"${opacity} d="${runsPath(width, height, (p) => set.has(p))}"/>`;
	}
	return `${svgOpen(width * cell, height * cell, width, height, options.title)}${body}</svg>`;
}

/** An indexed bitmap: the most used colour is one background rect, the others one path each. */
function indexedSvg(
	bitmap: IndexedBitmap,
	palette: readonly string[],
	options: SvgOptions,
): string {
	const cell = Math.max(1, options.cell ?? 1);
	const { width, height, indices } = bitmap;
	const counts = new Map<number, number>();
	let transparent = 0;
	for (let p = 0; p < width * height; p += 1) {
		const index = indices[p] ?? -1;
		if (palette[index] === undefined) transparent += 1;
		else counts.set(index, (counts.get(index) ?? 0) + 1);
	}
	let background: number | null = null;
	if (options.background === undefined || options.background === "auto") {
		if (transparent === 0 && counts.size > 1) {
			background =
				[...counts.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? null;
		}
	}
	let body =
		background !== null
			? `<rect width="${width}" height="${height}" fill="${escapeAttr(palette[background] ?? "")}"/>`
			: options.background && options.background !== "auto"
				? `<rect width="${width}" height="${height}" fill="${escapeAttr(options.background)}"/>`
				: "";
	for (const index of [...counts.keys()].sort((a, b) => a - b)) {
		if (index === background) continue;
		const path = runsPath(width, height, (p) => indices[p] === index);
		body += `<path fill="${escapeAttr(palette[index] ?? "")}" d="${path}"/>`;
	}
	return `${svgOpen(width * cell, height * cell, width, height, options.title)}${body}</svg>`;
}

function maskSvg(mask: InkMask, color: string, options: SvgOptions): string {
	const cell = Math.max(1, options.cell ?? 1);
	const background =
		options.background && options.background !== "auto"
			? `<rect width="${mask.width}" height="${mask.height}" fill="${escapeAttr(options.background)}"/>`
			: "";
	const path = runsPath(
		mask.width,
		mask.height,
		(p) => (mask.ink[p] ?? 0) !== 0,
	);
	return `${svgOpen(mask.width * cell, mask.height * cell, mask.width, mask.height, options.title)}${background}<path fill="${escapeAttr(color)}" d="${path}"/></svg>`;
}

function patternSvg(
	fill: DitherFill,
	width: number,
	height: number,
	options: SvgOptions,
): string {
	const id = ditherFillId(fill);
	const background =
		options.background && options.background !== "auto"
			? `<rect width="${width}" height="${height}" fill="${escapeAttr(options.background)}"/>`
			: "";
	return `${svgOpen(width, height, width, height, options.title)}<defs>${ditherPatternSvg(fill)}</defs>${background}<rect width="${width}" height="${height}" fill="url(#${id})"/></svg>`;
}

/**
 * A standalone SVG for a bitmap (one path per colour, horizontal runs merged),
 * an ink mask (one path) or a pattern fill (one `<pattern>` and a rect).
 */
export function exportSVG(source: SvgSource, options: SvgOptions = {}): string {
	switch (source.kind) {
		case "bitmap":
			return bitmapSvg(source.bitmap, options);
		case "indexed":
			return indexedSvg(source.bitmap, source.palette, options);
		case "mask":
			return maskSvg(source.mask, source.color, options);
		case "pattern":
			return patternSvg(source.fill, source.width, source.height, options);
	}
}

/** One Bayer tile at `tone` as an SVG: `n × cell` px square, ink cells in `color`. */
export function tileSvg(fill: DitherFill): string {
	const n: TileSide = fill.matrix ? matrixTileSide(fill.matrix) : (fill.n ?? 4);
	const cell = Math.max(1, fill.cell ?? DEFAULT_CELL);
	const tile = n * cell;
	const count = inkedCells(fill.tone, n);
	const path =
		count > 0
			? `<path fill="${escapeAttr(fill.color)}" d="${ditherTilePath(count, n, fill.shape ?? "square")}"/>`
			: "";
	return `<svg xmlns="http://www.w3.org/2000/svg" width="${tile}" height="${tile}" viewBox="0 0 ${n} ${n}" shape-rendering="${fill.shape === "dot" ? "geometricPrecision" : "crispEdges"}">${path}</svg>`;
}

export type CssSource =
	| { kind: "tile"; fill: DitherFill }
	| { kind: "svg"; svg: string; width: number; height: number };

export interface CssOptions {
	/** Selector the rule is written for. Default `.dither`. */
	selector?: string;
	/** Also set `background-color` under the image. */
	background?: string;
	/** Use `cover` (default for `svg`) or the natural size. */
	size?: "cover" | "natural";
}

/**
 * A CSS rule whose `background-image` is the dither as an SVG data URI.
 * `tile` repeats one Bayer tile; `svg` paints a whole exported SVG.
 */
export function exportCSS(source: CssSource, options: CssOptions = {}): string {
	const selector = options.selector ?? ".dither";
	const lines: string[] = [];
	if (options.background)
		lines.push(`background-color: ${options.background};`);
	if (source.kind === "tile") {
		const n: TileSide = source.fill.matrix
			? matrixTileSide(source.fill.matrix)
			: (source.fill.n ?? 4);
		const tile = n * Math.max(1, source.fill.cell ?? DEFAULT_CELL);
		lines.push(
			`background-image: url("${svgToDataUri(tileSvg(source.fill))}");`,
		);
		lines.push(`background-size: ${tile}px ${tile}px;`);
		lines.push("background-repeat: repeat;");
	} else {
		lines.push(`background-image: url("${svgToDataUri(source.svg)}");`);
		lines.push(
			options.size === "natural"
				? `background-size: ${source.width}px ${source.height}px;`
				: "background-size: cover;",
		);
		lines.push("background-repeat: no-repeat;");
	}
	lines.push("image-rendering: pixelated;");
	return `${selector} {\n${lines.map((line) => `\t${line}`).join("\n")}\n}`;
}

type AnyCanvas = HTMLCanvasElement | OffscreenCanvas;

export interface PngOptions {
	/** Integer upscale, nearest-neighbour, so cells stay crisp. Default 1. */
	scale?: number;
	/** When set, the PNG is also downloaded under this name. */
	filename?: string;
}

/** The canvas as a PNG blob, optionally upscaled by whole pixels and downloaded. */
export async function exportPNG(
	canvas: AnyCanvas,
	options: PngOptions = {},
): Promise<Blob> {
	const scale = Math.max(1, Math.round(options.scale ?? 1));
	let source: AnyCanvas = canvas;
	if (scale > 1) {
		const scaled = document.createElement("canvas");
		scaled.width = canvas.width * scale;
		scaled.height = canvas.height * scale;
		const ctx = scaled.getContext("2d");
		if (!ctx) throw new Error("2D canvas is unavailable");
		ctx.imageSmoothingEnabled = false;
		ctx.drawImage(canvas, 0, 0, scaled.width, scaled.height);
		source = scaled;
	}
	const target = source;
	const blob =
		target instanceof HTMLCanvasElement
			? await new Promise<Blob | null>((resolve) =>
					target.toBlob(resolve, "image/png"),
				)
			: await target.convertToBlob({ type: "image/png" });
	if (!blob) throw new Error("Canvas could not be encoded as PNG");
	if (options.filename) downloadBlob(blob, options.filename);
	return blob;
}

/** Read a canvas back as a bitmap, for `exportSVG`. */
export function canvasToBitmap(canvas: AnyCanvas): DitherBitmap {
	const ctx = canvas.getContext("2d") as
		| CanvasRenderingContext2D
		| OffscreenCanvasRenderingContext2D
		| null;
	if (!ctx) throw new Error("2D canvas is unavailable");
	const image = ctx.getImageData(0, 0, canvas.width, canvas.height);
	return { width: image.width, height: image.height, data: image.data };
}

/** Save a blob through a temporary link. */
export function downloadBlob(blob: Blob, filename: string) {
	const url = URL.createObjectURL(blob);
	const link = document.createElement("a");
	link.href = url;
	link.download = filename;
	document.body.appendChild(link);
	link.click();
	link.remove();
	setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Save text (an SVG or a CSS rule) as a file. */
export function downloadText(
	text: string,
	filename: string,
	type = "text/plain",
) {
	downloadBlob(new Blob([text], { type }), filename);
}

// -- the documented export surface -------------------------------------------
//
// `toPNG`, `toSVG`, `toCSS`, `download` and `svgDataUrl` take the same inputs
// as the engine's other pieces: a canvas, the pixels of an image dither (one
// pixel per cell), or a grid of palette indices. They sit on top of the
// `export*` functions above.

/** Pixels of a dither, one per cell. Same shape as `ImageData`. */
export type PixelBuffer = DitherBitmap;

/** Longest side a PNG may have: the most every current browser can draw. */
export const MAX_PNG_SIDE = 16384;

/** `scale` as a whole number, reduced until the longest side fits `MAX_PNG_SIDE`. */
export function cappedScale(width: number, height: number, scale = 1): number {
	const whole = Math.max(1, Math.round(scale));
	const longest = Math.max(1, width, height);
	return Math.max(1, Math.min(whole, Math.floor(MAX_PNG_SIDE / longest)));
}

/** Draw a pixel buffer onto a new canvas, one canvas pixel per buffer pixel. */
export function bitmapToCanvas(bitmap: PixelBuffer): HTMLCanvasElement {
	const canvas = document.createElement("canvas");
	canvas.width = bitmap.width;
	canvas.height = bitmap.height;
	const ctx = canvas.getContext("2d");
	if (!ctx) throw new Error("2D canvas is unavailable");
	ctx.putImageData(
		new ImageData(
			Uint8ClampedArray.from(bitmap.data),
			bitmap.width,
			bitmap.height,
		),
		0,
		0,
	);
	return canvas;
}

const isCanvas = (source: AnyCanvas | PixelBuffer): source is AnyCanvas =>
	"getContext" in source;

/**
 * A canvas or a pixel buffer as a PNG. `scale` is a whole-number upscale with
 * no smoothing (pass the cell size to turn one pixel per cell into a
 * full-size image); it is reduced to keep the longest side within
 * `MAX_PNG_SIDE`. Needs a browser.
 */
export function toPNG(
	source: AnyCanvas | PixelBuffer,
	options: { scale?: number } = {},
): Promise<Blob> {
	const canvas = isCanvas(source) ? source : bitmapToCanvas(source);
	return exportPNG(canvas, {
		scale: cappedScale(canvas.width, canvas.height, options.scale),
	});
}

export interface ToSvgOptions {
	/** Output px per cell. Default 1. */
	cell?: number;
	/** Colours for an indexed bitmap, by index; indices without one stay transparent. */
	palette?: readonly string[];
	/** `false` draws every colour; default folds the most used one into one background rect. */
	background?: boolean;
	title?: string;
}

/**
 * A compact SVG in cell units: rows of same-coloured cells become short
 * strokes, the most used colour is one background rect. Runs anywhere.
 */
export function toSVG(
	source: PixelBuffer | IndexedBitmap,
	options: ToSvgOptions = {},
): string {
	const svgOptions: SvgOptions = {
		...(options.cell !== undefined ? { cell: options.cell } : {}),
		background: options.background === false ? null : "auto",
		...(options.title !== undefined ? { title: options.title } : {}),
	};
	if ("indices" in source) {
		return exportSVG(
			{ kind: "indexed", bitmap: source, palette: options.palette ?? [] },
			svgOptions,
		);
	}
	return exportSVG({ kind: "bitmap", bitmap: source }, svgOptions);
}

export interface ToCssTile {
	kind?: "tile";
	/** Share of cells inked, 0..1. */
	level: number;
	matrix?: DitherMatrix;
	/** Cell in CSS px. Default 3. */
	cell?: number;
	/** Ink: a literal colour in `background` mode; any colour, token or name in `mask` mode. */
	ink?: string;
	/** Colour behind the ink in `background` mode. Default transparent. */
	paper?: string;
	/** Bake the ink into the tile, or paint the element and mask it. Default `background`. */
	mode?: "background" | "mask";
	selector?: string;
}

export interface ToCssImage {
	kind: "image";
	image: PixelBuffer;
	/** `png` needs a browser; `svg` is a pure string. Default `png`. */
	format?: "png" | "svg";
	selector?: string;
}

/**
 * A whole gradient as one rule: the element is painted in `ink` and masked by
 * one ramp image, so a fade is one box with one mask, not a stack of bands.
 * The rule sets the box's size along the ramp to `length`.
 */
export interface ToCssRamp extends DitherRampOptions {
	kind: "ramp";
	/** Any colour, token or name. Default `--dither-ink`. */
	ink?: string;
	selector?: string;
}

export type ToCssOptions = ToCssTile | ToCssImage | ToCssRamp;

/**
 * One CSS rule. The `tile` kind repeats a single matrix tile at a coverage
 * `level`; the `image` kind embeds a whole dither as a covering background;
 * the `ramp` kind fades from one level to another along an axis.
 */
export function toCSS(options: ToCssOptions): string {
	const selector = options.selector ?? ".dither";
	if (options.kind === "ramp") {
		const { kind: _kind, ink, selector: _selector, ...ramp } = options;
		const mask = ditherRampMask(ramp);
		const axis = options.edge === "top" || options.edge === "bottom";
		const length =
			typeof options.length === "number"
				? `${options.length}px`
				: options.length;
		return `${selector} {\n\t${axis ? "height" : "width"}: ${length};\n\tbackground-color: ${ditherColorVar(ink ?? "--dither-ink")};\n\t-webkit-mask: ${mask.image} ${mask.position} / ${mask.size} ${mask.repeat};\n\tmask: ${mask.image} ${mask.position} / ${mask.size} ${mask.repeat};\n}`;
	}
	if (options.kind === "image") {
		if (options.format === "svg") {
			return exportCSS(
				{
					kind: "svg",
					svg: toSVG(options.image),
					width: options.image.width,
					height: options.image.height,
				},
				{ selector },
			);
		}
		const url = bitmapToCanvas(options.image).toDataURL("image/png");
		return `${selector} {\n\tbackground-image: url("${url}");\n\tbackground-size: cover;\n\tbackground-repeat: no-repeat;\n\timage-rendering: pixelated;\n}`;
	}
	const cell = Math.max(1, options.cell ?? 3);
	const mode = options.mode ?? "background";
	const fill = {
		tone: options.level,
		cell,
		...(options.matrix ? { matrix: options.matrix } : {}),
	};
	if (mode === "mask") {
		const tile = (options.matrix ? matrixTileSide(options.matrix) : 4) * cell;
		const mask = `url("${ditherMaskDataUri(fill)}") 0 0 / ${tile}px ${tile}px repeat`;
		return `${selector} {\n\tbackground-color: ${ditherColorVar(options.ink ?? "--dither-ink")};\n\t-webkit-mask: ${mask};\n\tmask: ${mask};\n}`;
	}
	// A data URI cannot read CSS variables, so names resolve to their fallback hex.
	const named =
		options.ink && isDitherColorName(options.ink)
			? DITHER_COLOR_FALLBACKS[options.ink]
			: undefined;
	const ink = named ?? options.ink ?? DITHER_COLOR_FALLBACKS.ink;
	const paper =
		options.paper && options.paper !== "transparent"
			? options.paper
			: undefined;
	return exportCSS(
		{ kind: "tile", fill: { ...fill, color: ink } },
		{ selector, ...(paper ? { background: paper } : {}) },
	);
}

/** Wrap an SVG string as a CSS `url("data:…")`. */
export function svgDataUrl(svg: string): string {
	return `url("${svgToDataUri(svg)}")`;
}

/** Save a Blob or a string through the browser's download flow. */
export function download(options: {
	data: Blob | string;
	filename: string;
	type?: string;
}): void {
	if (typeof options.data === "string") {
		downloadText(options.data, options.filename, options.type);
	} else {
		downloadBlob(options.data, options.filename);
	}
}
