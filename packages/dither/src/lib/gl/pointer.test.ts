import { describe, expect, test } from "bun:test";
import { clampPointer, easePointer, POINTER_SETTLE } from "./pointer";

describe("easePointer", () => {
	test("moves towards the target without overshooting", () => {
		const now = { x: 0, y: 1 };
		const target = { x: 1, y: 0 };
		expect(easePointer(now, target, 1 / 24)).toBe(true);
		expect(now.x).toBeGreaterThan(0);
		expect(now.x).toBeLessThan(1);
		expect(now.y).toBeLessThan(1);
		expect(now.y).toBeGreaterThan(0);
	});

	test("settles exactly on the rest point, then does no more work", () => {
		const now = { x: 0.9, y: 0.1 };
		const rest = { x: 0.5, y: 0.5 };
		let frames = 0;
		while (easePointer(now, rest, 1 / 24)) {
			frames++;
			if (frames > 600) throw new Error("never settled");
		}
		expect(now).toEqual(rest);
		expect(frames).toBeGreaterThan(1);
		// Settled: a further frame moves nothing.
		expect(easePointer(now, rest, 1 / 24)).toBe(false);
		expect(now).toEqual(rest);
	});

	test("a target inside the settle distance is snapped to, not chased", () => {
		const now = { x: 0.5, y: 0.5 };
		const target = { x: 0.5 + POINTER_SETTLE / 2, y: 0.5 };
		expect(easePointer(now, target, 1 / 24)).toBe(true);
		expect(now.x).toBe(target.x);
	});

	test("a new target after settling starts moving again", () => {
		const now = { x: 0.5, y: 0.5 };
		const rest = { x: 0.5, y: 0.5 };
		expect(easePointer(now, rest, 1 / 24)).toBe(false);
		expect(easePointer(now, { x: 0.2, y: 0.5 }, 1 / 24)).toBe(true);
		expect(now.x).toBeLessThan(0.5);
	});
});

describe("clampPointer", () => {
	test("keeps a point inside the 0..1 box", () => {
		expect(clampPointer(-0.2, 1.4)).toEqual([0, 1]);
		expect(clampPointer(0.25, 0.75)).toEqual([0.25, 0.75]);
	});
});
