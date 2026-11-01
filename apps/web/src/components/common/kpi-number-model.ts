/**
 * Pure helpers for `KpiNumber`: turn the text a formatter already produced
 * ("1.2k", "212 MiB", "12.3%", "1,284") into the pieces `DitherNumber` needs,
 * so every existing formatter keeps its rules and the digits can still
 * resolve and roll.
 */

export interface KpiParts {
	/** Text before the digits: "~", "<". */
	prefix: string;
	value: number;
	/** Digits after the decimal point, kept so "12.0" does not become "12". */
	decimals: number;
	/** The text had thousands separators. */
	grouping: boolean;
	/** Text after the digits: "k", " MiB", "%". */
	suffix: string;
}

// prefix, integer (optionally grouped), fraction, suffix. The prefix and the
// suffix hold no digit, so "3h 12m" and "2/2" are not one number.
const NUMBER = /^(\D*?)(\d{1,3}(?:,\d{3})+|\d+)(?:\.(\d+))?(\D*)$/;

/**
 * The parts of a simple "number plus unit" text, or `null` when the text is
 * not one number ("–", "3h 12m", "2/2", "n/a"). Those render as plain text.
 */
export function parseKpiText(text: string): KpiParts | null {
	const match = NUMBER.exec(text.trim());
	if (!match) return null;
	const [, prefix = "", whole = "", fraction = "", suffix = ""] = match;
	const grouping = whole.includes(",");
	const value = Number(
		`${whole.replaceAll(",", "")}${fraction ? `.${fraction}` : ""}`,
	);
	if (!Number.isFinite(value)) return null;
	return { prefix, value, decimals: fraction.length, grouping, suffix };
}

/** The `Intl.NumberFormat` options that print a number with the digits its text had. */
export function kpiFormat(
	parts: Pick<KpiParts, "decimals" | "grouping">,
): Intl.NumberFormatOptions {
	return {
		minimumFractionDigits: parts.decimals,
		maximumFractionDigits: parts.decimals,
		useGrouping: parts.grouping,
	};
}
