import { describe, expect, test } from "bun:test";
import { computeDelta, deltaTone, POLARITY } from "./delta";
import { heatmapMax, rankRows, statusClass, topServerErrors } from "./lists";
import { classShare, sparkPoints, toSeriesRows } from "./series";

describe("computeDelta", () => {
	test("more requests is an improvement", () => {
		const d = computeDelta(12_403, 11_030, POLARITY.requests);
		expect(d.direction).toBe("up");
		expect(d.tone).toBe("good");
		expect(d.text).toBe("▲ 12%");
	});

	test("polarity flips for latency: slower is a decline", () => {
		const slower = computeDelta(220, 200, POLARITY.p95Ms);
		expect(slower.direction).toBe("up");
		expect(slower.tone).toBe("bad");
		const faster = computeDelta(182, 201, POLARITY.p95Ms);
		expect(faster.direction).toBe("down");
		expect(faster.tone).toBe("good");
		expect(faster.text).toBe("▼ 9.5%");
	});

	test("a higher error rate is bad, a lower one good", () => {
		expect(computeDelta(0.0042, 0.002, POLARITY.errorRate).tone).toBe("bad");
		expect(computeDelta(0.001, 0.002, POLARITY.errorRate).tone).toBe("good");
	});

	test("visitors dropping 3% is a decline", () => {
		const d = computeDelta(1_203, 1_241, POLARITY.visitors);
		expect(d.tone).toBe("bad");
		expect(d.text).toBe("▼ 3.1%");
	});

	test("sub-half-percent moves read flat and neutral", () => {
		const d = computeDelta(1_002, 1_000, POLARITY.requests);
		expect(d.direction).toBe("flat");
		expect(d.tone).toBe("neutral");
	});

	test("a zero baseline reads new, not infinity", () => {
		const d = computeDelta(40, 0, POLARITY.requests);
		expect(d.direction).toBe("new");
		expect(d.ratio).toBeNull();
		expect(d.text).toBe("▲ new");
		// With no baseline there is nothing to be better or worse than.
		expect(d.tone).toBe("neutral");
		expect(computeDelta(40, 0, POLARITY.p95Ms).tone).toBe("neutral");
		expect(computeDelta(0, 0, POLARITY.requests).direction).toBe("flat");
	});

	test("no comparison yields a dash", () => {
		expect(computeDelta(5, undefined, POLARITY.requests).text).toBe("–");
		expect(computeDelta(5, null, POLARITY.requests).text).toBe("–");
	});

	test("deltaTone is symmetric", () => {
		expect(deltaTone("higher-better", 0.5)).toBe("good");
		expect(deltaTone("lower-better", 0.5)).toBe("bad");
		expect(deltaTone("lower-better", -0.5)).toBe("good");
	});
});

describe("rankRows", () => {
	test("bar width is relative to the biggest row with a 2% floor; pct is of the sum", () => {
		const rows = rankRows([
			{ k: "/pricing", n: 2_000 },
			{ k: "/", n: 4_000 },
			{ k: "/tiny", n: 1 },
		]);
		expect(rows.map((r) => r.k)).toEqual(["/", "/pricing", "/tiny"]);
		expect(rows[0]?.width).toBe(100);
		expect(rows[1]?.width).toBe(50);
		expect(rows[2]?.width).toBe(2);
		expect(rows[0]?.pct).toBeCloseTo((4_000 / 6_001) * 100, 5);
		const total = rows.reduce((s, r) => s + r.pct, 0);
		expect(total).toBeCloseTo(100, 5);
	});

	test("zero-count rows and missing lists give nothing", () => {
		expect(rankRows([{ k: "/x", n: 0 }])).toEqual([]);
		expect(rankRows(null)).toEqual([]);
		expect(rankRows(undefined)).toEqual([]);
	});
});

describe("status helpers", () => {
	test("statusClass", () => {
		expect(statusClass(204)).toBe("2xx");
		expect(statusClass(301)).toBe("3xx");
		expect(statusClass(404)).toBe("4xx");
		expect(statusClass(503)).toBe("5xx");
		expect(statusClass(0)).toBe("other");
	});

	test("topServerErrors orders by 5xx count and drops clean paths", () => {
		const out = topServerErrors([
			{ k: "/a", n: 100, s5xx: 1 },
			{ k: "/b", n: 10, s5xx: 5 },
			{ k: "/c", n: 999 },
		]);
		expect(out.map((i) => i.k)).toEqual(["/b", "/a"]);
	});
});

describe("heatmap", () => {
	test("max", () => {
		const grid = [
			[0, 5],
			[10, 2],
		];
		expect(heatmapMax(grid)).toBe(10);
	});
});

describe("toSeriesRows", () => {
	const point = (ts: string, requests: number) => ({
		ts,
		requests,
		s2xx: requests,
		s3xx: 0,
		s4xx: 0,
		s5xx: 0,
		p50Ms: 10,
		p95Ms: 20,
		p99Ms: 30,
		bytesOut: 0,
		visitors: 0,
	});
	const range = {
		from: "2026-10-25T10:00:00Z",
		to: "2026-10-25T10:10:00Z",
		stepSeconds: 60,
	};

	test("an idle stretch is bracketed with zero rows and null latency", () => {
		const rows = toSeriesRows({
			range,
			series: [
				point("2026-10-25T10:02:00Z", 5),
				point("2026-10-25T10:07:00Z", 3),
			],
		});
		const at = (iso: string) => rows.find((r) => r.t === Date.parse(iso));
		expect(at("2026-10-25T10:03:00Z")?.requests).toBe(0);
		expect(at("2026-10-25T10:03:00Z")?.p95Ms).toBeNull();
		expect(at("2026-10-25T10:06:00Z")?.requests).toBe(0);
		// Leading and trailing gaps reach the range edges.
		expect(rows[0]?.t).toBe(Date.parse(range.from));
		expect(rows.at(-1)?.t).toBe(Date.parse(range.to));
		const sorted = rows.every(
			(r, i) => i === 0 || r.t >= (rows[i - 1]?.t ?? 0),
		);
		expect(sorted).toBe(true);
	});

	test("contiguous buckets are untouched", () => {
		const rows = toSeriesRows({
			range: { ...range, to: "2026-10-25T10:02:00Z" },
			series: [
				point("2026-10-25T10:00:00Z", 1),
				point("2026-10-25T10:01:00Z", 2),
				point("2026-10-25T10:02:00Z", 3),
			],
		});
		expect(rows.map((r) => r.requests)).toEqual([1, 2, 3]);
	});

	test("an empty series stays empty", () => {
		expect(toSeriesRows({ range, series: [] })).toEqual([]);
	});

	test("sparkPoints thins long series and keeps short ones", () => {
		expect(sparkPoints([1, 2, 3]).length).toBe(3);
		expect(sparkPoints(Array.from({ length: 200 }, (_, i) => i)).length).toBe(
			40,
		);
		expect(sparkPoints(undefined)).toEqual([]);
	});

	test("classShare is the fraction of requests in a class", () => {
		const rows = toSeriesRows({
			range,
			series: [{ ...point("2026-10-25T10:02:00Z", 10), s2xx: 8, s5xx: 2 }],
		});
		expect(classShare(rows, "s5xx")).toBeCloseTo(0.2);
		expect(classShare([], "s5xx")).toBe(0);
	});
});
