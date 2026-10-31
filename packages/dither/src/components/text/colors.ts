import {
	DITHER_COLOR_NAMES,
	type DitherColorName,
	ditherColorVar,
} from "../../lib/colors";
import { type DitherPalette, toRGB } from "../../lib/tokens";
import type { TextRgba } from "./fills";

/**
 * A text colour: a palette name (`blue`, `purple`, `green`, `pink`, `orange`,
 * `red`, `grey`), a base token (`ink`, `paper`, `accent`, `muted`), a custom
 * property (`--foreground`) or any CSS colour.
 */
export type TextColor = string;

const KEYWORD_VARS: Record<string, string> = {
	ink: "var(--dither-ink)",
	paper: "var(--dither-paper)",
	accent: "var(--color-accent-brand)",
	muted: "var(--color-ink-4)",
};

/** A text colour as a CSS value, for `color`, `background` and animation keyframes. */
export function cssTextColor(color: TextColor): string {
	return KEYWORD_VARS[color.trim()] ?? ditherColorVar(color);
}

const isName = (value: string): value is DitherColorName =>
	(DITHER_COLOR_NAMES as readonly string[]).includes(value);

/** A CSS colour containing `var()` resolved through a throwaway element. */
function probeCss(css: string): TextRgba {
	if (typeof document === "undefined") return [128, 128, 128, 255];
	const span = document.createElement("span");
	span.style.color = css;
	span.style.display = "none";
	document.body.append(span);
	const computed = getComputedStyle(span).color;
	span.remove();
	const [r, g, b] = toRGB(computed || css);
	return [r, g, b, 255];
}

/**
 * A text colour as RGBA channels for canvas drawing. Names and base tokens
 * come from the resolved `palette`; `transparent` has alpha 0; anything with
 * `var()` is resolved in the document.
 */
export function resolveTextRgba(
	color: TextColor,
	palette: DitherPalette,
): TextRgba {
	const value = color.trim();
	if (value === "transparent") return [0, 0, 0, 0];
	const keyword = (hex: string): TextRgba => {
		const [r, g, b] = toRGB(hex);
		return [r, g, b, 255];
	};
	if (value === "ink") return keyword(palette.ink);
	if (value === "paper") return keyword(palette.paper);
	if (value === "accent") return keyword(palette.accent);
	if (value === "muted") return keyword(palette.muted);
	if (isName(value)) return keyword(palette.named[value]);
	if (value.startsWith("--") || value.includes("var(")) {
		return probeCss(ditherColorVar(value));
	}
	const [r, g, b] = toRGB(value);
	return [r, g, b, 255];
}
