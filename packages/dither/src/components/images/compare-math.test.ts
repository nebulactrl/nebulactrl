import { describe, expect, test } from "bun:test";
import {
	clampPercent,
	keyTarget,
	labelHidden,
	pointerPosition,
	roundPercent,
} from "./compare-math";

describe("compare slider", () => {
	test("arrows move 1, shift and page keys 10, home and end go to the ends", () => {
		expect(keyTarget("ArrowRight", false, 50)).toBe(51);
		expect(keyTarget("ArrowLeft", false, 50)).toBe(49);
		expect(keyTarget("ArrowUp", true, 50)).toBe(60);
		expect(keyTarget("ArrowDown", true, 50)).toBe(40);
		expect(keyTarget("PageUp", false, 50)).toBe(60);
		expect(keyTarget("PageDown", false, 50)).toBe(40);
		expect(keyTarget("Home", false, 50)).toBe(0);
		expect(keyTarget("End", false, 50)).toBe(100);
		expect(keyTarget("a", false, 50)).toBeNull();
	});

	test("positions stay between 0 and 100", () => {
		expect(keyTarget("ArrowLeft", true, 4)).toBe(0);
		expect(keyTarget("PageUp", false, 98)).toBe(100);
		expect(clampPercent(-3)).toBe(0);
		expect(clampPercent(140)).toBe(100);
		expect(roundPercent(33.333)).toBe(33.3);
	});

	test("the pointer maps to a percentage of the box", () => {
		expect(pointerPosition(150, 100, 200)).toBe(25);
		expect(pointerPosition(50, 100, 200)).toBe(0);
		expect(pointerPosition(400, 100, 200)).toBe(100);
		expect(pointerPosition(100, 0, 0)).toBeNull();
	});

	test("corner labels hide when the divider covers them", () => {
		expect(labelHidden("left", 10)).toBe(true);
		expect(labelHidden("left", 50)).toBe(false);
		expect(labelHidden("right", 90)).toBe(true);
		expect(labelHidden("right", 50)).toBe(false);
	});
});
