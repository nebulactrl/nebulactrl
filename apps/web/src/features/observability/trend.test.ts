import { describe, expect, test } from "bun:test";
import { cpuTrend } from "./trend";

describe("cpuTrend", () => {
	test("no trend until there are two samples", () => {
		expect(cpuTrend([])).toBeNull();
		expect(cpuTrend([{ cpu: 5 }])).toBeNull();
		expect(cpuTrend([{ cpu: 5 }, { cpu: null }])).toBeNull();
	});

	test("gap rows are dropped, the scale is one core", () => {
		expect(cpuTrend([{ cpu: 5 }, { cpu: null }, { cpu: 40 }])).toEqual({
			values: [5, 40],
			domain: [0, 100],
		});
	});

	test("a multi-replica peak above 100% stretches the scale", () => {
		expect(cpuTrend([{ cpu: 20 }, { cpu: 240 }])?.domain).toEqual([0, 240]);
	});
});
