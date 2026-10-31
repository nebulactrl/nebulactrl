import { describe, expect, test } from "bun:test";
import { isNarrowBar, NARROW_BAR_PX } from "./use-bar-narrow";

describe("isNarrowBar", () => {
	test("a bar under 760px has no room for the guarded key", () => {
		expect(isNarrowBar(NARROW_BAR_PX - 1)).toBe(true);
		expect(isNarrowBar(390)).toBe(true);
	});

	test("760px and wider keeps the guarded key", () => {
		expect(isNarrowBar(NARROW_BAR_PX)).toBe(false);
		expect(isNarrowBar(1440)).toBe(false);
	});
});
