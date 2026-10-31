// Period-over-period deltas. The API returns raw numbers for the previous
// period; whether a rise is good or bad is a UI decision (Umami's
// convention: green = improvement, red = decline), so each metric carries a
// polarity.

export type Polarity = "higher-better" | "lower-better";
export type DeltaTone = "good" | "bad" | "neutral";

export const POLARITY = {
	requests: "higher-better",
	visitors: "higher-better",
	bytesOut: "higher-better",
	p95Ms: "lower-better",
	errorRate: "lower-better",
} as const satisfies Record<string, Polarity>;

export interface Delta {
	/** Signed fraction: +0.12 is twelve percent up. null when undefined. */
	ratio: number | null;
	direction: "up" | "down" | "flat" | "new";
	tone: DeltaTone;
	/** "▲ 12%", "▼ 3%", "0%", "▲ new", or "–". Text never relies on colour. */
	text: string;
}

// Changes below half a percent are noise: shown flat, never coloured.
const FLAT_BELOW = 0.005;

export function deltaTone(polarity: Polarity, ratio: number): DeltaTone {
	if (Math.abs(ratio) < FLAT_BELOW) return "neutral";
	const rising = ratio > 0;
	const good = polarity === "higher-better" ? rising : !rising;
	return good ? "good" : "bad";
}

export function computeDelta(
	current: number,
	previous: number | undefined | null,
	polarity: Polarity,
): Delta {
	if (
		previous === undefined ||
		previous === null ||
		!Number.isFinite(previous)
	) {
		return { ratio: null, direction: "flat", tone: "neutral", text: "–" };
	}
	if (previous === 0) {
		if (current === 0) {
			return { ratio: 0, direction: "flat", tone: "neutral", text: "0%" };
		}
		// No base to divide by: say "new" rather than an infinite percentage.
		return {
			ratio: null,
			direction: "new",
			// No previous value to be better or worse than: never coloured.
			tone: "neutral",
			text: "▲ new",
		};
	}
	const ratio = (current - previous) / previous;
	if (Math.abs(ratio) < FLAT_BELOW) {
		return { ratio, direction: "flat", tone: "neutral", text: "0%" };
	}
	const pct = Math.abs(ratio) * 100;
	const label = pct >= 10 ? pct.toFixed(0) : pct.toFixed(1);
	return {
		ratio,
		direction: ratio > 0 ? "up" : "down",
		tone: deltaTone(polarity, ratio),
		text: `${ratio > 0 ? "▲" : "▼"} ${label}%`,
	};
}
