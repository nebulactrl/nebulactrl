import type { UptimeStatus } from "../../lib/bars-math";

/**
 * Pure option helpers for `DitherUptimeBar`: how many days the row shows at a
 * container width, how a day's date is written, and the words for each
 * status. Covered by `uptime-options.test.ts`.
 */

/** Container widths (px) where the row grows from 30 to 60 days (28rem) and to 90 days (42rem). */
const SIXTY_AT = 448;
const NINETY_AT = 672;

/** Days the row shows at `width`: 30 on narrow containers, 60 from 28rem, 90 from 42rem. */
export function windowDays(width: number | null): number {
	if (width === null) return 30;
	if (width >= NINETY_AT) return 90;
	return width >= SIXTY_AT ? 60 : 30;
}

const ISO_DAY = /^\d{4}-\d{2}-\d{2}$/;
const formatters = new Map<string, Intl.DateTimeFormat>();

/**
 * `2026-10-09` as "Oct 9, 2026" in `locale`, always read and written in UTC
 * so the server and the browser agree. Any other text (a display string such
 * as "Mar 14") is returned as given.
 */
export function formatDay(date: string, locale = "en-US"): string {
	if (!ISO_DAY.test(date)) return date;
	let formatter = formatters.get(locale);
	if (!formatter) {
		formatter = new Intl.DateTimeFormat(locale, {
			month: "short",
			day: "numeric",
			year: "numeric",
			timeZone: "UTC",
		});
		formatters.set(locale, formatter);
	}
	const time = Date.parse(`${date}T00:00:00Z`);
	return Number.isNaN(time) ? date : formatter.format(time);
}

/** The words a status is called by. `partial` is a degraded day, `none` a day without data. */
export interface UptimeWords {
	up: string;
	down: string;
	partial?: string;
	none?: string;
}

export const DEFAULT_WORDS: Readonly<Record<UptimeStatus, string>> = {
	up: "Operational",
	degraded: "Degraded",
	down: "Down",
	unknown: "No data",
};

/** The word for each status: the defaults, overridden by `words`. */
export function statusWords(words?: UptimeWords): Record<UptimeStatus, string> {
	return {
		up: words?.up ?? DEFAULT_WORDS.up,
		degraded: words?.partial ?? DEFAULT_WORDS.degraded,
		down: words?.down ?? DEFAULT_WORDS.down,
		unknown: words?.none ?? DEFAULT_WORDS.unknown,
	};
}

/** Keys a `colors` override may use: our four statuses, and dithercn's `none` for "no data". */
export type UptimeColorKey = UptimeStatus | "none";

/** `colors` re-keyed by status, so `none` and `unknown` both set the no-data colour (`unknown` wins). */
export function colorOverrides<V>(
	colors: Partial<Record<UptimeColorKey, V>> | undefined,
): Partial<Record<UptimeStatus, V>> {
	if (!colors) return {};
	const out: Partial<Record<UptimeStatus, V>> = {};
	if (colors.up !== undefined) out.up = colors.up;
	if (colors.degraded !== undefined) out.degraded = colors.degraded;
	if (colors.down !== undefined) out.down = colors.down;
	const none = colors.unknown ?? colors.none;
	if (none !== undefined) out.unknown = none;
	return out;
}
