import { describe, expect, test } from "bun:test";
import { STRIP_FADE_PX, stripFade, stripMask } from "./strip-fade";

describe("stripFade", () => {
	test("a strip that fits has no fade", () => {
		expect(stripFade(0, 360, 360)).toEqual({ start: false, end: false });
	});

	test("at the start only the end fades", () => {
		expect(stripFade(0, 360, 640)).toEqual({ start: false, end: true });
	});

	test("in the middle both ends fade", () => {
		expect(stripFade(120, 360, 640)).toEqual({ start: true, end: true });
	});

	test("at the end only the start fades, within a sub-pixel of it", () => {
		expect(stripFade(280, 360, 640)).toEqual({ start: true, end: false });
		expect(stripFade(279.5, 360, 640)).toEqual({ start: true, end: false });
	});
});

describe("stripMask", () => {
	test("no mask without a fade", () => {
		expect(stripMask({ start: false, end: false })).toBeUndefined();
	});

	test("fades only the ends that have more behind them", () => {
		expect(stripMask({ start: false, end: true })).toBe(
			`linear-gradient(to right, #000, #000, #000 calc(100% - ${STRIP_FADE_PX}px), transparent)`,
		);
		expect(stripMask({ start: true, end: false })).toBe(
			`linear-gradient(to right, transparent, #000 ${STRIP_FADE_PX}px, #000, #000)`,
		);
	});
});
