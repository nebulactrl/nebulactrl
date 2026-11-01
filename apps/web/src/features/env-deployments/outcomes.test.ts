import { describe, expect, test } from "bun:test";
import { outcomeDays, outcomeSuccessShare } from "./outcomes";

const day = (date: string, succeeded = 0, failed = 0, other = 0) => ({
	date,
	succeeded,
	failed,
	other,
});

describe("outcomeDays", () => {
	test("failed beats other beats succeeded; an empty day has no data", () => {
		const days = outcomeDays([
			day("2026-10-24"),
			day("2026-10-25", 2),
			day("2026-10-26", 3, 0, 1),
			day("2026-10-27", 1, 1, 2),
		]);
		expect(days.map((d) => d.status)).toEqual([
			"unknown",
			"up",
			"degraded",
			"down",
		]);
	});

	test("the readout counts each kind and skips zeros", () => {
		const days = outcomeDays([
			day("2026-10-27", 1, 1, 2),
			day("2026-10-26", 4),
		]);
		expect(days[0]?.detail).toBe("1 succeeded, 1 failed, 2 other");
		expect(days[1]?.detail).toBe("4 succeeded");
	});

	test("keeps the API's ISO dates, oldest first", () => {
		const days = outcomeDays([day("2026-10-24"), day("2026-10-25")]);
		expect(days.map((d) => d.date)).toEqual(["2026-10-24", "2026-10-25"]);
	});
});

describe("outcomeSuccessShare", () => {
	test("succeeded over every deployment in the window", () => {
		expect(
			outcomeSuccessShare([day("a", 3), day("b", 1, 2, 2), day("c")]),
		).toBeCloseTo(4 / 8);
	});
	test("undefined with no deployments", () => {
		expect(outcomeSuccessShare([day("a"), day("b")])).toBeUndefined();
		expect(outcomeSuccessShare([])).toBeUndefined();
	});
});
