import { describe, expect, test } from "bun:test";
import {
	calendarStartForWeeks,
	levelRangeText,
	unitPlural,
	unitWord,
	weeksThatFit,
} from "./heat-options";

describe("unitWord", () => {
	test("a string is the unit", () => {
		expect(unitWord("min", 5)).toBe("min");
		expect(unitWord(undefined, 5)).toBe("value");
	});
	test("one/other follows the value", () => {
		const unit = { one: "deploy", other: "deploys" };
		expect(unitWord(unit, 1)).toBe("deploy");
		expect(unitWord(unit, 0)).toBe("deploys");
		expect(unitWord(unit, 12)).toBe("deploys");
		expect(unitPlural(unit)).toBe("deploys");
	});
});

describe("calendarStartForWeeks", () => {
	// 2026-10-12 is a Monday.
	test("Monday start: the last column begins on the latest Monday", () => {
		expect(calendarStartForWeeks(["2026-10-12"], 2, 1)).toBe("2026-10-05");
		expect(calendarStartForWeeks(["2026-10-12"], 1, 1)).toBe("2026-10-12");
	});
	test("Sunday start: the last column begins on the Sunday before", () => {
		expect(calendarStartForWeeks(["2026-10-12"], 2, 0)).toBe("2026-10-04");
		expect(calendarStartForWeeks(["2026-10-12"], 1, 0)).toBe("2026-10-11");
	});
	test("uses the latest of several dates, in any order", () => {
		const dates = ["2026-10-01", new Date(Date.UTC(2026, 9, 12)), "2026-09-20"];
		expect(calendarStartForWeeks(dates, 1, 1)).toBe("2026-10-12");
	});
	test("`to` overrides the latest entry", () => {
		expect(calendarStartForWeeks(["2026-10-12"], 1, 1, "2026-10-20")).toBe(
			"2026-10-19",
		);
	});
	test("no dates, no start", () => {
		expect(calendarStartForWeeks([], 4, 0)).toBeNull();
		expect(calendarStartForWeeks(["nope"], 4, 0)).toBeNull();
	});
});

describe("weeksThatFit", () => {
	test("counts columns of size + gap", () => {
		expect(weeksThatFit(300, 28, 2, 6)).toBe(Math.floor(274 / 8));
	});
	test("always at least one", () => {
		expect(weeksThatFit(10, 28, 2, 6)).toBe(1);
	});
});

describe("levelRangeText", () => {
	const fmt = (v: number) => String(v);
	test("empty and unset", () => {
		expect(levelRangeText(0, 100, 4, fmt)).toBe("No data or zero");
		expect(levelRangeText(1, 0, 4, fmt)).toBe("No values yet");
	});
	test("bands follow levelOf", () => {
		expect(levelRangeText(1, 100, 4, fmt)).toBe("0 to 25");
		expect(levelRangeText(3, 100, 4, fmt)).toBe("50 to 75");
		expect(levelRangeText(4, 100, 4, fmt)).toBe("75 and up");
	});
});
