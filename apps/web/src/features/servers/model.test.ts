import { describe, expect, test } from "bun:test";
import { diskTotalBytes, parseRefreshDays, usageText } from "./model";

type Host = Parameters<typeof diskTotalBytes>[0];

/** A host whose newest heartbeat and connect-time facts carry these disk sizes. */
const host = (heartbeat: number | null, facts: number | null) =>
	({
		latest: heartbeat === null ? null : { diskTotal: heartbeat },
		facts: facts === null ? null : { diskTotal: facts },
	}) as unknown as Host;

describe("diskTotalBytes", () => {
	const GiB = 1024 ** 3;
	test("the newest heartbeat wins over the facts from connecting", () => {
		expect(diskTotalBytes(host(150.9 * GiB, 157.8 * GiB))).toBe(150.9 * GiB);
	});
	test("facts stand in before a first heartbeat", () => {
		expect(diskTotalBytes(host(null, 157.8 * GiB))).toBe(157.8 * GiB);
		expect(diskTotalBytes(host(0, 157.8 * GiB))).toBe(157.8 * GiB);
	});
	test("nothing reported is null, not zero", () => {
		expect(diskTotalBytes(host(null, null))).toBeNull();
		expect(diskTotalBytes(host(0, 0))).toBeNull();
	});
});

describe("parseRefreshDays", () => {
	test("accepts whole days from 1 to 90", () => {
		expect(parseRefreshDays("1")).toBe(1);
		expect(parseRefreshDays("7")).toBe(7);
		expect(parseRefreshDays("90")).toBe(90);
	});
	test("rejects empty, fractional and out-of-range text", () => {
		expect(parseRefreshDays("")).toBeNull();
		expect(parseRefreshDays("  ")).toBeNull();
		expect(parseRefreshDays("0")).toBeNull();
		expect(parseRefreshDays("91")).toBeNull();
		expect(parseRefreshDays("2.5")).toBeNull();
		expect(parseRefreshDays("abc")).toBeNull();
	});
});

describe("usageText", () => {
	const GiB = 1024 ** 3;
	test("writes the unit once when used and total share it", () => {
		expect(usageText(2.8 * GiB, 15.7 * GiB)).toBe("2.8 / 15.7 GiB");
		expect(usageText(60 * GiB, 180 * GiB)).toBe("60 / 180 GiB");
	});
	test("keeps both units when they differ", () => {
		expect(usageText(512 * 1024 ** 2, 2 * GiB)).toBe("512 MiB / 2 GiB");
	});
});
