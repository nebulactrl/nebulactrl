import { describe, expect, test } from "bun:test";
import {
	exceedsRetention,
	IDLE_REFETCH_MS,
	isLiveRange,
	LIVE_REFETCH_MS,
	METRIC_RANGES,
	rangeMs,
	refetchIntervalMs,
	TRAFFIC_RANGES,
} from "./ranges";

describe("rangeMs", () => {
	test.each([
		["5m", 300_000],
		["1h", 3_600_000],
		["12h", 43_200_000],
		["7d", 604_800_000],
		["30d", 2_592_000_000],
		["bogus", 0],
		["", 0],
		["10", 0],
	])("%s", (range, expected) => {
		expect(rangeMs(range)).toBe(expected);
	});

	test("every offered range parses", () => {
		for (const range of [...METRIC_RANGES, ...TRAFFIC_RANGES]) {
			expect(rangeMs(range)).toBeGreaterThan(0);
		}
	});
});

describe("refetchIntervalMs", () => {
	test("resource metrics poll every 5s up to 10m, 15s beyond", () => {
		for (const range of ["5m", "10m"]) {
			expect(isLiveRange("metrics", range)).toBe(true);
			expect(refetchIntervalMs("metrics", range)).toBe(LIVE_REFETCH_MS);
		}
		for (const range of ["30m", "1h", "6h", "12h", "24h", "7d", "30d"]) {
			expect(isLiveRange("metrics", range)).toBe(false);
			expect(refetchIntervalMs("metrics", range)).toBe(IDLE_REFETCH_MS);
		}
	});

	test("traffic is live up to an hour", () => {
		for (const range of ["15m", "1h"]) {
			expect(refetchIntervalMs("traffic", range)).toBe(LIVE_REFETCH_MS);
		}
		for (const range of ["6h", "24h", "7d", "30d"]) {
			expect(refetchIntervalMs("traffic", range)).toBe(IDLE_REFETCH_MS);
		}
	});

	test("an unparseable range is never live", () => {
		expect(isLiveRange("metrics", "nope")).toBe(false);
	});
});

describe("exceedsRetention", () => {
	test("30d against 7 days of history exceeds; 7d does not", () => {
		expect(exceedsRetention("30d", 7)).toBe(true);
		expect(exceedsRetention("7d", 7)).toBe(false);
		expect(exceedsRetention("24h", 7)).toBe(false);
	});

	test("unknown retention never warns", () => {
		expect(exceedsRetention("30d", 0)).toBe(false);
	});
});
