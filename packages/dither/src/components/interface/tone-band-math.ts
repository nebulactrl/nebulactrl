import { type BandMask, bandMaskDataUri } from "./bands";

/**
 * The alert marker: a static ordered-dither band painted inside an element's
 * left edge in a tone colour. Dense at the edge, fading inward over
 * `TONE_BAND_WIDTH` px. It marks alerts, banners, callouts, inline errors,
 * toasts and error states; a lamp is for live resource status only.
 *
 * The pattern comes from the band-mask engine: one alpha mask, applied by the
 * `tone-band` utility to a `::before`, so a row costs no extra DOM. This file
 * is pure (no React); the mask is installed once as `--tone-band-mask` on the
 * root by `installToneBandMask`.
 */

export const TONE_BAND_TONES = [
	"danger",
	"warning",
	"info",
	"success",
] as const;
export type ToneBandTone = (typeof TONE_BAND_TONES)[number];

/** How far the band reaches into the element, in CSS px: three 2px cells,
 * inside the smallest host padding (8px) so it never meets the icon. */
export const TONE_BAND_WIDTH = 6;
/** Edge of one dither cell in CSS px. */
export const TONE_BAND_CELL = 2;
/** Fall-off exponent: above 1 keeps the edge dense and thins quickly inward. */
const FALLOFF = 1.5;

/** One tone (0..1) per cell column, from the edge (1) inward (towards 0). */
export function toneBandStops(
	width: number = TONE_BAND_WIDTH,
	cell: number = TONE_BAND_CELL,
): number[] {
	const columns = Math.max(1, Math.round(width / cell));
	return Array.from({ length: columns }, (_, index) => {
		const tone = (1 - index / columns) ** FALLOFF;
		return Math.round(tone * 1000) / 1000;
	});
}

/** The band mask of the marker: columns running left to right. */
export function toneBandMask(
	width: number = TONE_BAND_WIDTH,
	cell: number = TONE_BAND_CELL,
): BandMask {
	return { axis: "x", stops: toneBandStops(width, cell), cell, n: 4 };
}

/** The mask as a CSS `url(...)` value. */
export function toneBandMaskCss(): string {
	return `url("${bandMaskDataUri(toneBandMask())}")`;
}

let installed = false;

/**
 * Defines `--tone-band-mask` on the root, once. Until it exists the
 * `tone-band` utility falls back to a smooth gradient, so nothing paints a
 * solid block.
 */
export function installToneBandMask(): void {
	if (installed || typeof document === "undefined") return;
	installed = true;
	document.documentElement.style.setProperty(
		"--tone-band-mask",
		toneBandMaskCss(),
	);
}

/** The attributes that turn any element into a banded one. */
export function toneBandProps(tone: ToneBandTone): {
	className: string;
	"data-tone": ToneBandTone;
} {
	installToneBandMask();
	return { className: "tone-band", "data-tone": tone };
}
