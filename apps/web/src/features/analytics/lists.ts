import type { TopItem } from "./types";

export interface RankedRow extends TopItem {
	/** Share of the list's total, 0–100. */
	pct: number;
	/** Bar width, 0–100: proportional to the largest row, never below 2. */
	width: number;
}

/**
 * Rows for a bar-as-background list: width is relative to the biggest row
 * (Tremor BarList: `max(value / max * 100, 2)`), the percentage is of the
 * sum. Sorted descending; zero-count rows are dropped.
 */
export function rankRows(
	items: readonly TopItem[] | null | undefined,
): RankedRow[] {
	const rows = (items ?? []).filter((item) => item.n > 0);
	rows.sort((a, b) => b.n - a.n);
	const total = rows.reduce((sum, row) => sum + row.n, 0);
	const max = rows[0]?.n ?? 0;
	return rows.map((row) => ({
		...row,
		pct: total > 0 ? (row.n / total) * 100 : 0,
		width: max > 0 ? Math.max((row.n / max) * 100, 2) : 0,
	}));
}

/** Status class of an HTTP code: 404 → "4xx"; anything else "other". */
export function statusClass(
	code: number,
): "2xx" | "3xx" | "4xx" | "5xx" | "other" {
	if (code >= 200 && code < 300) return "2xx";
	if (code >= 300 && code < 400) return "3xx";
	if (code >= 400 && code < 500) return "4xx";
	if (code >= 500 && code < 600) return "5xx";
	return "other";
}

/** Paths that failed: top 5xx offenders by error count, then by volume. */
export function topServerErrors(
	items: readonly TopItem[] | null | undefined,
): TopItem[] {
	return (items ?? [])
		.filter((item) => (item.s5xx ?? 0) > 0)
		.sort((a, b) => (b.s5xx ?? 0) - (a.s5xx ?? 0) || b.n - a.n);
}

/** Max value across a 7×24 grid. */
export function heatmapMax(
	grid: readonly (readonly number[] | null)[],
): number {
	let max = 0;
	for (const row of grid) for (const v of row ?? []) if (v > max) max = v;
	return max;
}
