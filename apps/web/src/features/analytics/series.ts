import type { TrafficPoint, TrafficReport } from "./types";

export interface SeriesRow {
	t: number;
	requests: number;
	s2xx: number;
	s3xx: number;
	s4xx: number;
	s5xx: number;
	/** Latency lines break where no request was served. */
	p50Ms: number | null;
	p95Ms: number | null;
	p99Ms: number | null;
}

function zeroRow(t: number): SeriesRow {
	return {
		t,
		requests: 0,
		s2xx: 0,
		s3xx: 0,
		s4xx: 0,
		s5xx: 0,
		p50Ms: null,
		p95Ms: null,
		p99Ms: null,
	};
}

function toRow(point: TrafficPoint): SeriesRow {
	const served = point.requests > 0;
	return {
		t: Date.parse(point.ts),
		requests: point.requests,
		s2xx: point.s2xx,
		s3xx: point.s3xx,
		s4xx: point.s4xx,
		s5xx: point.s5xx,
		p50Ms: served ? point.p50Ms : null,
		p95Ms: served ? point.p95Ms : null,
		p99Ms: served ? point.p99Ms : null,
	};
}

/**
 * Chart rows for a report: buckets are sorted and quiet stretches are filled
 * with explicit zero rows (one right after a busy bucket, one right before
 * the next), so a stacked area drops to the floor across an idle gap instead
 * of drawing a ramp over it. Works whatever the bucket alignment, since it
 * only compares neighbours.
 */
export function toSeriesRows(
	report: Pick<TrafficReport, "series"> & {
		range: Pick<TrafficReport["range"], "from" | "to" | "stepSeconds">;
	},
): SeriesRow[] {
	const stepMs = report.range.stepSeconds * 1000;
	const rows = (report.series ?? []).map(toRow).sort((a, b) => a.t - b.t);
	const from = Date.parse(report.range.from);
	const to = Date.parse(report.range.to);
	if (stepMs <= 0) return rows;

	const out: SeriesRow[] = [];
	const first = rows[0];
	if (first === undefined) return out;
	if (Number.isFinite(from) && first.t - from > stepMs * 1.5) {
		out.push(zeroRow(from), zeroRow(first.t - stepMs));
	}
	let prev: SeriesRow | undefined;
	for (const row of rows) {
		if (prev && row.t - prev.t > stepMs * 1.5) {
			out.push(zeroRow(prev.t + stepMs), zeroRow(row.t - stepMs));
		}
		out.push(row);
		prev = row;
	}
	if (prev && Number.isFinite(to) && to - prev.t > stepMs * 1.5) {
		out.push(zeroRow(prev.t + stepMs), zeroRow(to));
	}
	return out;
}

/** Share of requests that are 5xx / 4xx etc., or 0 when nothing was served. */
export function classShare(
	rows: readonly SeriesRow[],
	key: "s2xx" | "s3xx" | "s4xx" | "s5xx",
): number {
	let total = 0;
	let part = 0;
	for (const row of rows) {
		total += row.requests;
		part += row[key];
	}
	return total > 0 ? part / total : 0;
}

/** Sparkline points: the series as {i, v}, thinned to at most `max` points. */
export function sparkPoints(
	values: readonly number[] | null | undefined,
	max = 40,
): { i: number; v: number }[] {
	const list = values ?? [];
	if (list.length <= max) return list.map((v, i) => ({ i, v }));
	const stride = list.length / max;
	const out: { i: number; v: number }[] = [];
	for (let i = 0; i < max; i += 1) {
		out.push({ i, v: list[Math.floor(i * stride)] ?? 0 });
	}
	return out;
}
