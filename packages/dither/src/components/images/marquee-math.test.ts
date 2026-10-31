import { describe, expect, test } from "bun:test";
import { loopCopies, nextRate } from "./marquee-math";

describe("marquee", () => {
	test("the rate eases towards the target and then snaps to it", () => {
		let rate = 1;
		let frames = 0;
		while (rate !== 0 && frames < 200) {
			const next = nextRate(rate, 0, 1 / 60);
			expect(next).toBeLessThan(rate);
			rate = next;
			frames += 1;
		}
		expect(rate).toBe(0);
		// about half a second at 60 fps
		expect(frames).toBeGreaterThan(20);
		expect(frames).toBeLessThan(90);
	});

	test("it speeds back up the same way", () => {
		let rate = 0;
		for (let frames = 0; rate !== 1 && frames < 200; frames += 1)
			rate = nextRate(rate, 1, 1 / 60);
		expect(rate).toBe(1);
	});

	test("a long frame cannot overshoot", () => {
		expect(nextRate(1, 0, 10)).toBe(0);
		expect(nextRate(0, 1, 10)).toBe(1);
	});

	test("enough copies fill the width, with one spare", () => {
		expect(loopCopies(1000, 400)).toBe(4);
		expect(loopCopies(300, 400)).toBe(2);
		expect(loopCopies(800, 400)).toBe(3);
		expect(loopCopies(800, 0)).toBe(2);
	});
});
