import { describe, expect, test } from "bun:test";
import { durations } from "@nebulactrl/tokens";
import {
	boxTransform,
	INDICATOR_SECONDS,
	PENDING_MS,
	sameBox,
} from "./nav-indicator";

describe("sameBox", () => {
	const box = { x: 12, y: 96, w: 196, h: 32 };

	test("a row that has not moved is the same box", () => {
		expect(sameBox(box, { ...box })).toBe(true);
		expect(sameBox(box, { ...box, y: 96.3 })).toBe(true);
	});

	test("a row that moved or changed size is not", () => {
		expect(sameBox(box, { ...box, y: 128 })).toBe(false);
		expect(sameBox(box, { ...box, w: 40, h: 40 })).toBe(false);
	});
});

describe("boxTransform", () => {
	test("is the whole transform string, so the browser can run it off the main thread", () => {
		expect(boxTransform({ x: 12, y: 96 })).toBe("translate(12px, 96px)");
	});
});

describe("timing", () => {
	test("the slide is the quick token, well under 200ms", () => {
		expect(INDICATOR_SECONDS).toBe(durations.quick / 1000);
		expect(INDICATOR_SECONDS).toBeLessThanOrEqual(0.2);
	});

	test("a press that never navigates gives the highlight back within two seconds", () => {
		expect(PENDING_MS).toBeLessThanOrEqual(2000);
	});
});
