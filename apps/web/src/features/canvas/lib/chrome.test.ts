import { describe, expect, test } from "bun:test";
import {
	COMPACT_BELOW,
	chromeMode,
	chromeModeFor,
	freeWidth,
	SEARCH_FLOAT_FROM,
} from "./chrome";

describe("freeWidth", () => {
	test("is what the inspector leaves of the canvas, never negative", () => {
		expect(freeWidth(1208, 950)).toBe(258);
		expect(freeWidth(1208, 0)).toBe(1208);
		expect(freeWidth(600, 900)).toBe(0);
	});
});

describe("chromeMode", () => {
	test("a roomy canvas keeps labels and a floating search field", () => {
		expect(chromeMode(1208)).toEqual({ compact: false, searchInline: false });
	});

	test("collapses to icons under the compact threshold", () => {
		expect(chromeMode(COMPACT_BELOW).compact).toBe(false);
		expect(chromeMode(COMPACT_BELOW - 1).compact).toBe(true);
		// A narrow canvas: 258px beside a 950px sheet.
		expect(chromeMode(258)).toEqual({ compact: true, searchInline: true });
	});

	test("the search field leaves the top centre before the labels go", () => {
		expect(SEARCH_FLOAT_FROM).toBeGreaterThan(COMPACT_BELOW);
		expect(chromeMode(SEARCH_FLOAT_FROM - 1)).toEqual({
			compact: false,
			searchInline: true,
		});
		expect(chromeMode(SEARCH_FLOAT_FROM).searchInline).toBe(false);
	});
});

describe("chromeModeFor", () => {
	test("takes the inspector off the canvas before choosing", () => {
		// 1440 wide with a 232px sidebar: a 950px sheet leaves 258px.
		expect(chromeModeFor(1208, 950)).toEqual({
			compact: true,
			searchInline: true,
		});
		expect(chromeModeFor(1208, 0)).toEqual({
			compact: false,
			searchInline: false,
		});
	});

	test("even the capped wide sheet (420px left) shows icons", () => {
		// 788px sheet on a 1208px canvas: 420 free.
		expect(chromeModeFor(1208, 788)).toEqual({
			compact: true,
			searchInline: true,
		});
	});

	test("a canvas that is not measured yet is roomy", () => {
		expect(chromeModeFor(0, 0)).toEqual({
			compact: false,
			searchInline: false,
		});
	});

	test("a phone's canvas is its screen: icons, and the search field in the column", () => {
		expect(chromeModeFor(390, 0)).toEqual({
			compact: true,
			searchInline: true,
		});
	});
});
