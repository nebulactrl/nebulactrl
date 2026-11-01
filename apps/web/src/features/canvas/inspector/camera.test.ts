import { describe, expect, test } from "bun:test";
import {
	PAN_EASE,
	PAN_MS,
	panDuration,
	RESIZE_SETTLE_MS,
	revealTiming,
} from "./camera";

describe("panDuration", () => {
	test("is the panel's open duration", () => {
		expect(panDuration(false, false)).toBe(PAN_MS);
		expect(PAN_MS).toBe(250);
	});

	test("is none under reduced motion or after a key press", () => {
		expect(panDuration(true, false)).toBe(0);
		expect(panDuration(false, true)).toBe(0);
	});
});

describe("revealTiming", () => {
	test("a panel that just opened pans right after its first frame", () => {
		expect(revealTiming(0, 640)).toEqual({ kind: "next-frames" });
	});

	test("another service taking the open panel pans at once", () => {
		expect(revealTiming(640, 640)).toEqual({ kind: "next-frames" });
	});

	test("a width that changed under an open panel waits for it to rest", () => {
		expect(revealTiming(640, 700)).toEqual({
			kind: "after-rest",
			ms: RESIZE_SETTLE_MS,
		});
	});
});

describe("PAN_EASE", () => {
	test("runs from 0 to 1 and decelerates (an ease-out)", () => {
		expect(PAN_EASE(0)).toBe(0);
		expect(PAN_EASE(1)).toBe(1);
		expect(PAN_EASE(0.25)).toBeGreaterThan(0.25);
		expect(PAN_EASE(0.5)).toBeGreaterThan(0.5);
	});
});
