/**
 * Pure helpers for the stat card: the headline text and the delta badge
 * (text, tone, a spoken form). Covered by `stat-math.test.ts`.
 */

type StatDeltaTone = "good" | "bad" | "neutral";

const COUNT = new Intl.NumberFormat("en");
const PERCENT = new Intl.NumberFormat("en", { maximumFractionDigits: 1 });

/** Numbers get thousands separators; strings show as given. */
export function statValueText(value: number | string): string {
	return typeof value === "number" ? COUNT.format(value) : value;
}

export type DeltaDirection = "up" | "down" | "flat";

/**
 * Splits the leading glyph off a delta's text (`▲ 8.1%`, `▼ 3%`, `● 0%`) so the
 * card can draw it as an arrow icon. Text with no glyph keeps its words and has
 * no direction; the words always carry the change, the icon only repeats it.
 */
export function splitDeltaGlyph(text: string): {
	direction: DeltaDirection | null;
	text: string;
} {
	const first = text.charAt(0);
	const rest = text.slice(1).trimStart();
	if (first === "▲") return { direction: "up", text: rest };
	if (first === "▼") return { direction: "down", text: rest };
	if (first === "●") return { direction: "flat", text: rest };
	return { direction: null, text };
}

export interface StatDelta {
	/** `▲ 8.1%`: the sign is the arrow, so colour is never the only cue. */
	text: string;
	tone: StatDeltaTone;
	/** `Up 8.1% vs last week`, for screen readers. */
	spoken: string;
}

/**
 * A change in percent as badge text. `good` names the direction that counts
 * as an improvement (`down` for latency and error rates). A change that
 * rounds to 0 is neutral with no arrow.
 */
export function statDelta(
	delta: number,
	good: "up" | "down" = "up",
	deltaLabel?: string,
): StatDelta {
	const abs = Math.abs(delta);
	const shown = PERCENT.format(abs);
	const suffix = deltaLabel ? ` ${deltaLabel}` : "";
	if (!Number.isFinite(delta) || Number(shown) === 0) {
		return { text: "0%", tone: "neutral", spoken: `No change${suffix}` };
	}
	const up = delta > 0;
	const improved = up === (good === "up");
	return {
		text: `${up ? "▲" : "▼"} ${shown}%`,
		tone: improved ? "good" : "bad",
		spoken: `${up ? "Up" : "Down"} ${shown}%${suffix}`,
	};
}
