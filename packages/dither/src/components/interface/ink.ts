import { useMemo } from "react";
import { useForcedColors } from "../../lib/prefs";
import { type RGB, toRGB, useDitherPalette } from "../../lib/tokens";
import { type DitherColor, inkCss } from "./fill";

/**
 * A CSS colour as 0–255 channels, resolved where it is used: `var()`,
 * `color-mix()` and system colours all go through the browser's own cascade
 * on a throwaway element. For canvas drawing, which cannot read `var()`.
 */
export function resolveInk(css: string): RGB {
	if (typeof document === "undefined") return [128, 128, 128];
	const probe = document.createElement("span");
	probe.style.color = css;
	probe.style.display = "none";
	document.documentElement.appendChild(probe);
	const computed = getComputedStyle(probe).color;
	probe.remove();
	return toRGB(computed || css);
}

/**
 * The ink colour of a canvas dither: `color` (default the dither ink), read
 * again when the theme changes, and the page's text colour under forced
 * colours so the drawing stays legible.
 */
export function useInkRgb(color: DitherColor | undefined): RGB {
	const palette = useDitherPalette();
	const forced = useForcedColors();
	// biome-ignore lint/correctness/useExhaustiveDependencies: `palette` changes when a theme token does, which is when the ink must be read again
	return useMemo(
		() => resolveInk(forced ? "CanvasText" : inkCss(color)),
		[color, forced, palette],
	);
}
