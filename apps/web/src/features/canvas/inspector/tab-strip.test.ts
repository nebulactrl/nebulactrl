import { describe, expect, test } from "bun:test";
import { edgeFade, fadeMask, scrollTarget } from "./tab-strip";

describe("edgeFade", () => {
	test("a strip that fits fades nowhere", () => {
		expect(edgeFade(0, 400, 400)).toEqual({ start: false, end: false });
	});
	test("fades the end while there is more to the right", () => {
		expect(edgeFade(0, 300, 500)).toEqual({ start: false, end: true });
	});
	test("fades both ends in the middle and only the start at the end", () => {
		expect(edgeFade(80, 300, 500)).toEqual({ start: true, end: true });
		expect(edgeFade(200, 300, 500)).toEqual({ start: true, end: false });
	});
});

describe("fadeMask", () => {
	test("no mask when nothing is hidden", () => {
		expect(fadeMask({ start: false, end: false })).toBeUndefined();
	});
	test("fades only the ends that hide content", () => {
		const end = fadeMask({ start: false, end: true });
		expect(end).toContain("#000 0");
		expect(end).toContain("transparent 100%");
		const start = fadeMask({ start: true, end: false });
		expect(start).toContain("transparent 0");
		expect(start).toContain("#000 100%");
	});
});

describe("scrollTarget", () => {
	const viewport = { left: 0, width: 300 };
	test("a tab already in view with air does not scroll", () => {
		expect(scrollTarget(0, viewport, { left: 100, width: 60 })).toBeNull();
	});
	test("a tab clipped at the right scrolls it in with air", () => {
		expect(scrollTarget(0, viewport, { left: 260, width: 70 })).toBe(62);
	});
	test("a tab clipped at the left scrolls back, never below zero", () => {
		expect(scrollTarget(100, viewport, { left: -10, width: 60 })).toBe(58);
		expect(scrollTarget(10, viewport, { left: -5, width: 60 })).toBe(0);
	});
});
