import { describe, expect, test } from "bun:test";
import { latencyText } from "./latency-text";

describe("latencyText", () => {
	test("is the empty mark when there were no requests", () => {
		expect(latencyText(0, 0)).toBe("–");
	});

	test("formats the percentile once there were requests", () => {
		expect(latencyText(12, 0.4)).toBe("<1 ms");
		expect(latencyText(12, 84)).toBe("84 ms");
		expect(latencyText(3, 1500)).toBe("1.5 s");
	});
});
