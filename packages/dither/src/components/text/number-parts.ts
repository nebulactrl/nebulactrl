/**
 * Pure helpers for `DitherNumber`: split a formatted number into digit and
 * non-digit parts, and work out which digits changed between two values and
 * in what order they animate.
 */

export interface NumberPart {
	type: "digit" | "literal";
	/** One character for a digit; any string for a literal (sign, separator, symbol). */
	value: string;
	/** Stable React key: counts digits and literals from the right edge. */
	key: string;
}

export interface NumberFormatInput {
	locale?: Intl.LocalesArgument | undefined;
	format?: Intl.NumberFormatOptions | undefined;
	prefix?: string | undefined;
	suffix?: string | undefined;
}

/** `Intl` output for `value`, with prefix and suffix, as one flat string. */
export function formatNumber(
	value: number,
	input: NumberFormatInput = {},
): string {
	const text = new Intl.NumberFormat(input.locale, input.format).format(value);
	return `${input.prefix ?? ""}${text}${input.suffix ?? ""}`;
}

/**
 * Parts of the formatted number. Digits come from `formatToParts` integer and
 * fraction runs and only plain ASCII `0`–`9` animate; everything else (group
 * and decimal separators, currency, sign, compact suffix, prefix, suffix and
 * digits of other numbering systems) is a literal that never animates.
 * Keys count from the right, so appending a leading digit does not remount
 * the digits that were already there.
 */
export function numberParts(
	value: number,
	input: NumberFormatInput = {},
): NumberPart[] {
	const formatter = new Intl.NumberFormat(input.locale, input.format);
	const raw: { type: "digit" | "literal"; value: string }[] = [];
	if (input.prefix) raw.push({ type: "literal", value: input.prefix });
	for (const part of formatter.formatToParts(value)) {
		if (part.type === "integer" || part.type === "fraction") {
			for (const char of part.value)
				raw.push({
					type: /^[0-9]$/.test(char) ? "digit" : "literal",
					value: char,
				});
		} else {
			raw.push({ type: "literal", value: part.value });
		}
	}
	if (input.suffix) raw.push({ type: "literal", value: input.suffix });
	let digits = 0;
	let literals = 0;
	const parts: NumberPart[] = [];
	for (let i = raw.length - 1; i >= 0; i -= 1) {
		const part = raw[i] as { type: "digit" | "literal"; value: string };
		const key = part.type === "digit" ? `d${digits++}` : `l${literals++}`;
		parts.unshift({ ...part, key });
	}
	return parts;
}

/**
 * For each digit of `next` (left to right), its animation rank among the
 * digits that changed (0 = first), or -1 when it did not change. Digits are
 * matched from the right edge, so `99 → 100` changes all three.
 */
export function changedRanks(
	previous: readonly string[],
	next: readonly string[],
): number[] {
	const ranks = new Array<number>(next.length).fill(-1);
	let rank = 0;
	for (let i = 0; i < next.length; i += 1) {
		const before = previous[previous.length - (next.length - i)];
		if (before !== next[i]) {
			ranks[i] = rank;
			rank += 1;
		}
	}
	return ranks;
}

/** `1` when the digit grows, `-1` when it shrinks, `0` when it is not a plain 0–9 change. */
export function rollDirection(
	from: string | undefined,
	to: string,
): 1 | -1 | 0 {
	const a = from === undefined ? Number.NaN : Number(from);
	const b = Number(to);
	if (Number.isNaN(a) || Number.isNaN(b) || a === b) return 0;
	return b > a ? 1 : -1;
}

/** Most digits a roll spins past in one move, an extra turn included. */
export const MAX_ROLL_STEPS = 19;

const toBig = (digits: readonly string[]): bigint => {
	const text = digits.join("");
	return text === "" ? 0n : BigInt(text);
};

/**
 * How far the digit at `index` of `next` spins on its strip, signed: positive
 * rolls up (the value grew), negative rolls down (it shrank). Digits are
 * aligned from the right edge; a place that did not exist before counts as 0.
 * The digit travels the short way round the 0–9 strip in the direction of the
 * whole value, and a place whose own value moved by ten or more spins one full
 * extra turn, like a mechanical counter. Never 0 for a digit that changed.
 */
export function rollSteps(
	previous: readonly string[],
	next: readonly string[],
	index: number,
): number {
	const place = next.length - 1 - index;
	const after = Number(next[index]);
	const beforeRaw = previous[previous.length - 1 - place];
	const before = beforeRaw === undefined ? 0 : Number(beforeRaw);
	const from = toBig(previous);
	const to = toBig(next);
	const direction = to >= from ? 1 : -1;
	let steps =
		direction > 0 ? (after - before + 10) % 10 : (before - after + 10) % 10;
	const scale = 10n ** BigInt(place);
	const moved = to / scale - from / scale;
	if ((moved < 0n ? -moved : moved) >= 10n) steps += 10;
	return direction * Math.min(MAX_ROLL_STEPS, Math.max(1, steps));
}
