import { describe, expect, test } from "bun:test";
import {
	bucketAxisLabel,
	bytesToMb,
	compactNumber,
	dualTime,
	formatByteCount,
	formatMs,
	formatRate,
} from "./chart-format";

// 2026-10-25T12:05:10Z
const T = Date.UTC(2026, 9, 25, 12, 5, 10);

describe("dualTime", () => {
	test("prints the same instant in a local zone and UTC", () => {
		const t = dualTime(T, { stepSeconds: 10, timeZone: "Europe/Berlin" });
		// Berlin is UTC+1 after the late-October change (25th is CET).
		expect(t.local).toBe("13:05:10");
		expect(t.utc).toBe("12:05:10");
	});

	test("drops seconds for minute-or-coarser steps", () => {
		const t = dualTime(T, { stepSeconds: 60, timeZone: "UTC" });
		expect(t.utc).toBe("12:05");
	});

	test("adds the date when the span crosses a day", () => {
		const t = dualTime(T, {
			stepSeconds: 300,
			spanMs: 7 * 86_400_000,
			timeZone: "UTC",
		});
		expect(t.utc).toContain("25 Oct");
	});
});

describe("bucketAxisLabel", () => {
	test("minute steps read as a clock", () => {
		expect(bucketAxisLabel(T, 60, "UTC")).toBe("12:05");
	});
	test("hour steps carry the date and hour", () => {
		expect(bucketAxisLabel(T, 3_600, "UTC")).toBe("25 Oct 12");
	});
	test("day steps are a date", () => {
		expect(bucketAxisLabel(T, 86_400, "UTC")).toBe("25 Oct");
	});
});

describe("compactNumber", () => {
	test.each([
		[0, "0"],
		[999, "999"],
		[1_000, "1k"],
		[12_403, "12.4k"],
		[124_030, "124k"],
		[1_200_000, "1.2M"],
		[3_400_000_000, "3.4B"],
	])("%d", (n, expected) => {
		expect(compactNumber(n)).toBe(expected);
	});
});

describe("rates, latency and sizes", () => {
	test("formatRate", () => {
		expect(formatRate(0)).toBe("0%");
		expect(formatRate(0.0042)).toBe("0.42%");
		expect(formatRate(0.031)).toBe("3.1%");
		expect(formatRate(0.00001)).toBe("<0.01%");
	});
	test("formatMs", () => {
		expect(formatMs(0.2)).toBe("<1 ms");
		expect(formatMs(182)).toBe("182 ms");
		expect(formatMs(1540)).toBe("1.54 s");
	});
	test("formatByteCount", () => {
		expect(formatByteCount(0)).toBe("0 B");
		expect(formatByteCount(84_300_112)).toBe("80.4 MiB");
		expect(formatByteCount(2 * 1024 ** 3)).toBe("2 GiB");
	});
	test("bytesToMb", () => {
		expect(bytesToMb(222_298_112)).toBe(212);
	});
});
