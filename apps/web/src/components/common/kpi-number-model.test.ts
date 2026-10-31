import { describe, expect, test } from "bun:test";
import { formatNumber } from "@nebulactrl/dither/text/number-parts";
import { kpiFormat, parseKpiText } from "./kpi-number-model";

/** The text `DitherNumber` would print for what we parsed. */
function roundTrip(text: string): string | null {
	const parts = parseKpiText(text);
	if (!parts) return null;
	return formatNumber(parts.value, {
		locale: "en",
		format: kpiFormat(parts),
		prefix: parts.prefix,
		suffix: parts.suffix,
	});
}

describe("parseKpiText", () => {
	test("splits a number from its unit", () => {
		expect(parseKpiText("212 MiB")).toEqual({
			prefix: "",
			value: 212,
			decimals: 0,
			grouping: false,
			suffix: " MiB",
		});
		expect(parseKpiText("12.3%")).toMatchObject({
			value: 12.3,
			decimals: 1,
			suffix: "%",
		});
		expect(parseKpiText("1.2k")).toMatchObject({ value: 1.2, suffix: "k" });
	});

	test("reads grouped numbers and keeps the grouping", () => {
		expect(parseKpiText("1,284")).toMatchObject({
			value: 1284,
			grouping: true,
			decimals: 0,
		});
		expect(parseKpiText("12,345,678.5 req")).toMatchObject({
			value: 12345678.5,
			decimals: 1,
			suffix: " req",
		});
	});

	test("keeps a prefix", () => {
		expect(parseKpiText("<1 ms")).toMatchObject({
			prefix: "<",
			value: 1,
			suffix: " ms",
		});
	});

	test("is null for text that is not one number", () => {
		for (const text of [
			"–",
			"—",
			"",
			"n/a",
			"3h 12m",
			"2/2",
			"1.2.3",
			"10:30",
		]) {
			expect(parseKpiText(text)).toBeNull();
		}
	});

	test("prints back the text it was given", () => {
		for (const text of [
			"0",
			"7",
			"12.0%",
			"12.3%",
			"0.04%",
			"1.2k",
			"3.4M",
			"212 MiB",
			"1.5 GiB",
			"1,284",
			"99,999 req",
			"<1 ms",
			"21 ms",
			"4.5 req/s",
		]) {
			expect(roundTrip(text)).toBe(text);
		}
	});

	test("is null for what it cannot print back", () => {
		expect(roundTrip("3h 12m")).toBeNull();
	});
});
