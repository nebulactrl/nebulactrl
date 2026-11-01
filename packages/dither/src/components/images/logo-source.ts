import { toRGB } from "../../lib/tokens";
import { nebulaMarkSvg } from "./nebula-mark";
import { type CrossOrigin, loadImage } from "./use-dithered";

/** Where a logo comes from: any image URL, a brand of the sprite, or our own mark. */
export type LogoSource =
	| { kind: "url"; src: string }
	| { kind: "brand"; slug: string }
	| { kind: "mark" };

/** The host's brand logos: an SVG sprite with one `<symbol id="brand-<slug>">` per brand, and each brand's own colour. */
export interface BrandCatalog {
	/** URL of the sprite. */
	spriteUrl: string;
	/** The brand's colour as a CSS hex, e.g. `#4169e1`. */
	hex(slug: string): string;
}

let catalog: BrandCatalog | null = null;
let spriteText: Promise<string> | null = null;

/**
 * Register the host's brand logos once at startup, before any `<DitheredLogo
 * brand>` or `<LogoMarquee>` brand mounts. `apps/web` does it in
 * `src/lib/dither-brands.ts`. Without a catalog only `src` URLs and our own
 * mark can be drawn.
 */
export function setBrandCatalog(next: BrandCatalog): void {
	catalog = next;
	spriteText = null;
}

function brands(): BrandCatalog {
	if (!catalog)
		throw new Error(
			"No brand catalog registered: call setBrandCatalog() first",
		);
	return catalog;
}

function sprite(): Promise<string> {
	const { spriteUrl } = brands();
	spriteText ??= fetch(spriteUrl).then((response) => {
		if (!response.ok)
			throw new Error(`Could not load the brand sprite (${response.status})`);
		return response.text();
	});
	spriteText.catch(() => {
		spriteText = null;
	});
	return spriteText;
}

/** Raster size (px) of the longer side an SVG logo is loaded at, before it is fitted to the grid. */
const SOURCE_SIZE = 384;

/** One brand of the sprite as a standalone SVG string, painted in `fill`. */
export async function brandSvg(slug: string, fill: string): Promise<string> {
	const markup = await sprite();
	const escaped = slug.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
	const match = new RegExp(
		`<symbol id="brand-${escaped}" viewBox="([^"]+)">([\\s\\S]*?)</symbol>`,
	).exec(markup);
	if (!match) throw new Error(`Brand ${slug} is not in the sprite`);
	const viewBox = match[1] ?? "0 0 24 24";
	const inner = (match[2] ?? "").replace(/<title>[\s\S]*?<\/title>/, "");
	const [, , vw = 24, vh = 24] = viewBox.split(/\s+/).map(Number);
	const scale = SOURCE_SIZE / Math.max(vw, vh);
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${viewBox}" width="${Math.round(vw * scale)}" height="${Math.round(vh * scale)}" fill="${fill}">${inner}</svg>`;
}

const rgbString = (css: string) => {
	const [r, g, b] = toRGB(css);
	return `rgb(${r},${g},${b})`;
};

/** Our mark, painted with the brand-mark tokens of the current theme. */
export function nebulaMarkSource(): string {
	const style = getComputedStyle(document.documentElement);
	const read = (name: string, fallback: string) =>
		rgbString(style.getPropertyValue(name).trim() || fallback);
	return nebulaMarkSvg(
		{
			from: read("--brand-mark-from", "#9a6bff"),
			mid: read("--brand-mark-mid", "#6d6bff"),
			to: read("--brand-mark-to", "#4fa8ff"),
			star: read("--brand-mark-star", "#eef0ff"),
		},
		SOURCE_SIZE,
	);
}

const svgUrl = (svg: string) =>
	`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;

/**
 * Load a logo as an image. Brand marks are painted in their own brand colour;
 * the mark in the theme's gradient. The caller turns alpha into the cell mask
 * and, for `tone="original"`, reads the colours from the pixels.
 */
export function loadLogo(
	source: LogoSource,
	crossOrigin?: CrossOrigin,
): Promise<HTMLImageElement> {
	switch (source.kind) {
		case "url":
			return loadImage(source.src, crossOrigin);
		case "mark":
			return loadImage(svgUrl(nebulaMarkSource()));
		case "brand":
			return brandSvg(source.slug, brands().hex(source.slug)).then((svg) =>
				loadImage(svgUrl(svg)),
			);
	}
}

/** A stable key for a source, for effect dependencies. */
export function logoSourceKey(source: LogoSource): string {
	return source.kind === "url"
		? `url:${source.src}`
		: source.kind === "brand"
			? `brand:${source.slug}`
			: "mark";
}
