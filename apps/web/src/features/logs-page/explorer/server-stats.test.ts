import { describe, expect, test } from "bun:test";
import type { LogFacetsPage } from "#/api/gen/types.gen";
import { FACET_KEYS } from "#/features/logs/search/query";
import { histogramSeries, toSearchStats } from "./server-stats";

function page(overrides: Partial<LogFacetsPage> = {}): LogFacetsPage {
	return {
		matched: 0,
		scanned: 0,
		partial: false,
		facets: [],
		attrs: [],
		histogram: [],
		capturing: true,
		requestsCapturing: true,
		...overrides,
	};
}

describe("toSearchStats", () => {
	test("no page is every built-in facet empty and no attributes", () => {
		const stats = toSearchStats(undefined);
		expect(Object.keys(stats.facets).sort()).toEqual([...FACET_KEYS].sort());
		for (const key of FACET_KEYS) expect(stats.facets[key].size).toBe(0);
		expect(stats.attrs.size).toBe(0);
	});

	test("built-in facets become value counts; keys the server left out are empty", () => {
		const stats = toSearchStats(
			page({
				facets: [
					{
						key: "level",
						count: 30,
						values: [
							{ value: "error", count: 10 },
							{ value: "info", count: 20 },
						],
					},
					{
						key: "service",
						count: 30,
						values: [{ value: "api", count: 30 }],
					},
				],
			}),
		);
		expect([...stats.facets.level]).toEqual([
			["error", 10],
			["info", 20],
		]);
		expect([...stats.facets.service]).toEqual([["api", 30]]);
		expect(stats.facets.status.size).toBe(0);
		expect(stats.facets.replica.size).toBe(0);
	});

	test("a facet key that is not a built-in one is ignored", () => {
		const stats = toSearchStats(
			page({
				facets: [
					{ key: "mystery", count: 1, values: [{ value: "x", count: 1 }] },
				],
			}),
		);
		expect(Object.keys(stats.facets).sort()).toEqual([...FACET_KEYS].sort());
	});

	test("attributes lose the @, keep their count and values, and numeric ones count as numeric", () => {
		const stats = toSearchStats(
			page({
				attrs: [
					{
						key: "@method",
						count: 40,
						values: [
							{ value: "GET", count: 30 },
							{ value: "POST", count: 10 },
						],
					},
					{
						key: "@duration_ms",
						count: 40,
						numeric: true,
						min: 1,
						max: 900,
						values: [{ value: "12", count: 2 }],
					},
				],
			}),
		);
		expect([...stats.attrs.keys()]).toEqual(["method", "duration_ms"]);
		expect(stats.attrs.get("method")).toEqual({
			count: 40,
			numeric: 0,
			values: new Map([
				["GET", 30],
				["POST", 10],
			]),
		});
		expect(stats.attrs.get("duration_ms")).toEqual({
			count: 40,
			numeric: 40,
			values: new Map([["12", 2]]),
		});
	});

	test("a field without values is an empty map", () => {
		const stats = toSearchStats(
			page({
				attrs: [{ key: "@path", count: 5, values: null as never }],
			}),
		);
		expect(stats.attrs.get("path")?.values.size).toBe(0);
	});
});

describe("histogramSeries", () => {
	test("no page is no series", () => {
		expect(histogramSeries(undefined)).toEqual([]);
	});

	test("buckets become millisecond starts with their counts, in order", () => {
		const series = histogramSeries(
			page({
				histogram: [
					{ start: "2026-10-20T14:00:00.000Z", count: 5, errors: 1 },
					{ start: "2026-10-20T14:01:00.000Z", count: 0, errors: 0 },
				],
			}),
		);
		expect(series).toEqual([
			{ start: Date.parse("2026-10-20T14:00:00.000Z"), count: 5, errors: 1 },
			{ start: Date.parse("2026-10-20T14:01:00.000Z"), count: 0, errors: 0 },
		]);
	});

	test("a bucket with an unreadable start is left out", () => {
		const series = histogramSeries(
			page({
				histogram: [
					{ start: "not a time", count: 5, errors: 1 },
					{ start: "2026-10-20T14:01:00.000Z", count: 2, errors: 0 },
				],
			}),
		);
		expect(series).toHaveLength(1);
		expect(series[0]?.count).toBe(2);
	});
});
