import { describe, expect, test } from "bun:test";
import {
	capBytes,
	capField,
	draftOf,
	formatCap,
	GB,
	type LogSettingsDraft,
	MB,
	parseRetention,
	requestBody,
	shortId,
	topServices,
	usageLine,
	usagePercent,
	validate,
} from "./log-settings-model";

const defaults = {
	capture: true,
	retentionDays: 7,
	maxServiceBytes: 500 * MB,
	maxServerBytes: 5 * GB,
};

describe("size fields", () => {
	test("whole gigabytes are shown in GB, everything else in MB", () => {
		expect(capField(5 * GB)).toEqual({ amount: "5", unit: "GB" });
		expect(capField(500 * MB)).toEqual({ amount: "500", unit: "MB" });
		expect(capField(1536 * MB)).toEqual({ amount: "1536", unit: "MB" });
		expect(capField(32 * MB)).toEqual({ amount: "32", unit: "MB" });
	});

	test("a field means bytes, 1024-based", () => {
		expect(capBytes({ amount: "500", unit: "MB" })).toBe(500 * MB);
		expect(capBytes({ amount: "5", unit: "GB" })).toBe(5 * GB);
		expect(capBytes({ amount: "1.5", unit: "GB" })).toBe(1.5 * GB);
		expect(capBytes({ amount: "", unit: "MB" })).toBeNull();
		expect(capBytes({ amount: "abc", unit: "MB" })).toBeNull();
		expect(capBytes({ amount: "0", unit: "MB" })).toBeNull();
		expect(capBytes({ amount: "-5", unit: "GB" })).toBeNull();
	});

	test("the draft of the stored settings round-trips to the same bytes", () => {
		const draft = draftOf(defaults);
		expect(draft).toEqual({
			capture: true,
			retentionDays: "7",
			perService: { amount: "500", unit: "MB" },
			perServer: { amount: "5", unit: "GB" },
		});
		expect(requestBody(draft)).toEqual(defaults);
	});
});

describe("validation", () => {
	const base: LogSettingsDraft = draftOf(defaults);

	test("the defaults are valid", () => {
		expect(validate(base)).toEqual({});
	});

	test("retention is 1 to 90 whole days", () => {
		expect(parseRetention("1")).toBe(1);
		expect(parseRetention("90")).toBe(90);
		expect(parseRetention("0")).toBeNull();
		expect(parseRetention("91")).toBeNull();
		expect(parseRetention("7.5")).toBeNull();
		expect(parseRetention("")).toBeNull();
		expect(validate({ ...base, retentionDays: "0" }).retentionDays).toBe(
			"Keep 1–90 days.",
		);
	});

	test("a cap is at least 32 MB", () => {
		expect(
			validate({ ...base, perService: { amount: "31", unit: "MB" } })
				.perService,
		).toBe("At least 32 MB.");
		expect(
			validate({ ...base, perService: { amount: "32", unit: "MB" } })
				.perService,
		).toBeUndefined();
		expect(
			validate({ ...base, perServer: { amount: "", unit: "GB" } }).perServer,
		).toBe("Enter a size.");
	});

	test("the per-service cap cannot exceed the server cap", () => {
		const errors = validate({
			...base,
			perService: { amount: "6", unit: "GB" },
			perServer: { amount: "5", unit: "GB" },
		});
		expect(errors.perService).toBe(
			"The per-service limit cannot exceed the server limit.",
		);
		expect(errors.perServer).toBeUndefined();
		expect(
			validate({ ...base, perService: { amount: "5", unit: "GB" } }),
		).toEqual({});
	});

	test("no request body while a field is wrong", () => {
		expect(requestBody({ ...base, retentionDays: "x" })).toBeNull();
		expect(
			requestBody({ ...base, perServer: { amount: "1", unit: "MB" } }),
		).toBeNull();
		expect(
			requestBody({
				...base,
				capture: false,
				retentionDays: "30",
				perService: { amount: "1", unit: "GB" },
			}),
		).toEqual({
			capture: false,
			retentionDays: 30,
			maxServiceBytes: GB,
			maxServerBytes: 5 * GB,
		});
	});
});

describe("usage", () => {
	test("sizes read in MB and GB", () => {
		expect(formatCap(5 * GB)).toBe("5 GB");
		expect(formatCap(1.2 * GB)).toBe("1.2 GB");
		expect(formatCap(500 * MB)).toBe("500 MB");
		expect(formatCap(0)).toBe("0 MB");
		expect(formatCap(100)).toBe("<1 MB");
	});

	test("the usage sentence", () => {
		expect(usageLine(1.2 * GB, 5 * GB)).toBe(
			"Using 1.2 GB of 5 GB on this server",
		);
	});

	test("the share of the limit stays between 0 and 100", () => {
		expect(usagePercent(1 * GB, 4 * GB)).toBe(25);
		expect(usagePercent(5 * GB, 4 * GB)).toBe(100);
		expect(usagePercent(1, 0)).toBe(0);
	});

	test("the five biggest services, named, and unknown ones say so", () => {
		const usage = {
			services: Array.from({ length: 7 }, (_, i) => ({
				serviceId: `s${i}`,
				bytes: (i + 1) * MB,
				lines: i * 100,
			})),
		};
		const names = new Map([
			["s6", "api"],
			["s5", "worker"],
		]);
		const rows = topServices(usage, names);
		expect(rows.map((r) => r.serviceId)).toEqual([
			"s6",
			"s5",
			"s4",
			"s3",
			"s2",
		]);
		expect(rows[0]?.name).toBe("api");
		expect(rows[2]?.name).toBeUndefined();
		expect(topServices(undefined, names)).toEqual([]);
		expect(shortId("0b6f2c1e-7d3a-4f55-9a10-3c2d1e0f9a8b")).toBe("0b6f2c1e");
		expect(topServices({ services: null }, names)).toEqual([]);
	});
});
