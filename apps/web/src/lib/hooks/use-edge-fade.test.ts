import { describe, expect, test } from "bun:test";
import { edgeFade, fadeMask, NO_FADE, scrollTarget } from "./use-edge-fade";

describe("edgeFade", () => {
	test("a strip that fits fades nowhere", () => {
		expect(edgeFade(0, 400, 400)).toEqual(NO_FADE);
	});

	test("fades the end that has more behind it", () => {
		expect(edgeFade(0, 300, 500)).toEqual({ start: false, end: true });
		expect(edgeFade(100, 300, 500)).toEqual({ start: true, end: true });
		expect(edgeFade(200, 300, 500)).toEqual({ start: true, end: false });
	});

	test("a sub-pixel gap at an end is not a fade", () => {
		expect(edgeFade(0.5, 300, 300.5)).toEqual(NO_FADE);
	});
});

describe("fadeMask", () => {
	test("is undefined when nothing is hidden", () => {
		expect(fadeMask(NO_FADE)).toBeUndefined();
	});

	test("fades only the ends that continue", () => {
		expect(fadeMask({ start: false, end: true })).toContain("transparent 100%");
		expect(fadeMask({ start: false, end: true })).not.toContain(
			"transparent 0",
		);
		expect(fadeMask({ start: true, end: false })).toContain("transparent 0");
	});
});

describe("scrollTarget", () => {
	const viewport = { left: 0, width: 300 };

	test("is null for an item already in view with air around it", () => {
		expect(scrollTarget(0, viewport, { left: 100, width: 60 })).toBeNull();
	});

	test("brings a clipped item at the end into view", () => {
		expect(scrollTarget(0, viewport, { left: 270, width: 80 })).toBe(82);
	});

	test("brings an item behind the start into view, never below zero", () => {
		expect(scrollTarget(50, viewport, { left: -20, width: 60 })).toBe(0);
	});
});
