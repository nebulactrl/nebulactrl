import { describe, expect, test } from "bun:test";
import {
	colorOverrides,
	DEFAULT_WORDS,
	formatDay,
	statusWords,
	windowDays,
} from "./uptime-options";

describe("windowDays", () => {
	test("30 on narrow containers, 60 from 28rem, 90 from 42rem", () => {
		expect(windowDays(null)).toBe(30);
		expect(windowDays(320)).toBe(30);
		expect(windowDays(447)).toBe(30);
		expect(windowDays(448)).toBe(60);
		expect(windowDays(671)).toBe(60);
		expect(windowDays(672)).toBe(90);
		expect(windowDays(1200)).toBe(90);
	});
});

describe("formatDay", () => {
	test("ISO dates are written in the locale, in UTC", () => {
		expect(formatDay("2026-10-09", "en-US")).toBe("Oct 9, 2026");
	});
	test("other text is returned as given", () => {
		expect(formatDay("Mar 14")).toBe("Mar 14");
		expect(formatDay("2026-10-09T10:00")).toBe("2026-10-09T10:00");
	});
	test("an impossible date is returned as given", () => {
		expect(formatDay("2026-13-45")).toBe("2026-13-45");
	});
});

describe("statusWords", () => {
	test("defaults", () => {
		expect(statusWords()).toEqual(DEFAULT_WORDS);
	});
	test("overrides follow the dithercn names", () => {
		expect(
			statusWords({
				up: "Succeeded",
				down: "Failed",
				none: "No backup",
			}),
		).toEqual({
			up: "Succeeded",
			degraded: "Degraded",
			down: "Failed",
			unknown: "No backup",
		});
	});
	test("partial names the degraded status", () => {
		expect(
			statusWords({ up: "OK", down: "Bad", partial: "Slow" }).degraded,
		).toBe("Slow");
	});
});

describe("colorOverrides", () => {
	test("none maps to the no-data status", () => {
		expect(colorOverrides({ none: "grey", up: "blue" })).toEqual({
			up: "blue",
			unknown: "grey",
		});
	});
	test("unknown wins over none", () => {
		expect(colorOverrides({ none: "grey", unknown: "pink" }).unknown).toBe(
			"pink",
		);
	});
	test("nothing given", () => {
		expect(colorOverrides(undefined)).toEqual({});
	});
});
