/**
 * Named dither colours: the `blue … grey` vocabulary every component and the
 * engine accept wherever a colour goes. Each name is a CSS custom property in
 * `packages/tokens/src/dither.css` (`--dither-blue`, …) that points at one of
 * our own status or accent tokens, so a theme or a wrapper override retints
 * everything.
 *
 * Pure: no DOM access, safe in tests and on the server.
 */

export const DITHER_COLOR_NAMES = [
	"blue",
	"purple",
	"green",
	"pink",
	"orange",
	"red",
	"grey",
] as const;

export type DitherColorName = (typeof DITHER_COLOR_NAMES)[number];

/** `ink` and `paper` are the two base tokens; the rest are the named hues. */
export type DitherBaseName = "ink" | "paper";

/** Last-resort hex per name, used when a page does not define the token. */
export const DITHER_COLOR_FALLBACKS: Readonly<
	Record<DitherColorName | DitherBaseName, string>
> = {
	ink: "#9aa4ff",
	paper: "#1a1d20",
	blue: "#38c5ff",
	purple: "#9aa4ff",
	green: "#41d686",
	pink: "#c58bff",
	orange: "#ff9f6b",
	red: "#ff7d7c",
	grey: "#7a818d",
};

const NAMES = new Set<string>([...DITHER_COLOR_NAMES, "ink", "paper"]);

export function isDitherColorName(
	value: string,
): value is DitherColorName | DitherBaseName {
	return NAMES.has(value);
}

/**
 * Turn any colour entry into something CSS can use directly:
 *
 * - `"blue"` / `"ink"` -> `var(--dither-blue)` / `var(--dither-ink)`
 * - `"--chart-1"`      -> `var(--chart-1)`
 * - everything else (`#hex`, `oklch()`, `var()`, `transparent`) is unchanged.
 */
export function ditherColorVar(entry: string): string {
	const value = entry.trim();
	if (isDitherColorName(value)) return `var(--dither-${value})`;
	if (value.startsWith("--")) return `var(${value})`;
	return value;
}

/** The custom property behind a named colour (`blue` -> `--dither-blue`), or null. */
export function ditherColorToken(entry: string): string | null {
	const value = entry.trim();
	return isDitherColorName(value) ? `--dither-${value}` : null;
}
