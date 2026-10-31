import type { AsciiPalette } from "./engine";

/**
 * The palette as shipped in styles.css (`--ascii-dim`, `--ascii-ink`,
 * `--color-accent-brand`, `--color-accent-ink`, `--brand-mark-star`). The
 * standalone maintenance pages have no CSS vars, so they use this directly.
 */
export const NEBULA_PALETTE: AsciiPalette = {
	dim: [0x3a, 0x40, 0x4c],
	ink: [0xb4, 0xbb, 0xc8],
	accent: [0x6d, 0x6b, 0xff],
	accentInk: [0x9a, 0xa4, 0xff],
	star: [0xee, 0xf0, 0xff],
};

const TOKENS = {
	dim: "--ascii-dim",
	ink: "--ascii-ink",
	accent: "--color-accent-brand",
	accentInk: "--color-accent-ink",
	star: "--brand-mark-star",
} as const satisfies Record<keyof AsciiPalette, string>;

/**
 * Reads the palette from CSS custom properties, once. The tokens are
 * `oklch()` or `color-mix()`, not hex, so each is resolved by the cascade on a
 * probe element and then painted into a 1x1 canvas to read sRGB bytes back.
 * Any token that does not resolve keeps its NEBULA_PALETTE value.
 */
export function readPalette(scope: HTMLElement): AsciiPalette {
	const ctx = document
		.createElement("canvas")
		.getContext("2d", { willReadFrequently: true });
	if (!ctx) return NEBULA_PALETTE;
	ctx.canvas.width = ctx.canvas.height = 1;
	const probe = document.createElement("span");
	probe.setAttribute("aria-hidden", "true");
	probe.style.cssText =
		"position:absolute;visibility:hidden;pointer-events:none";
	scope.appendChild(probe);
	const out = { ...NEBULA_PALETTE };
	for (const key of Object.keys(TOKENS) as (keyof AsciiPalette)[]) {
		const fallback = NEBULA_PALETTE[key];
		probe.style.color = `var(${TOKENS[key]})`;
		const resolved = getComputedStyle(probe).color;
		// An unparsable colour leaves fillStyle at the previous value, so seed it with a sentinel.
		ctx.fillStyle = "#010203";
		ctx.fillStyle = resolved;
		ctx.clearRect(0, 0, 1, 1);
		ctx.fillRect(0, 0, 1, 1);
		const d = ctx.getImageData(0, 0, 1, 1).data;
		const sentinel = d[0] === 1 && d[1] === 2 && d[2] === 3;
		out[key] =
			sentinel || d[3] === 0 ? fallback : [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0];
	}
	probe.remove();
	return out;
}
