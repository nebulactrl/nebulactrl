import { describe, expect, test } from "bun:test";
import { axisAtOffset, circlePath, ringBandTones } from "./radar-options";

describe("axisAtOffset", () => {
	test("axis 0 is straight up, the rest follow clockwise", () => {
		expect(axisAtOffset(0, -10, 4)).toBe(0);
		expect(axisAtOffset(10, 0, 4)).toBe(1);
		expect(axisAtOffset(0, 10, 4)).toBe(2);
		expect(axisAtOffset(-10, 0, 4)).toBe(3);
	});
	test("just left of up still belongs to axis 0", () => {
		expect(axisAtOffset(-1, -10, 4)).toBe(0);
	});
	test("picks the nearest of five", () => {
		// 72° apart: 40° clockwise from up is nearer to axis 1 (72°) than to axis 0.
		const rad = (40 * Math.PI) / 180;
		expect(axisAtOffset(Math.sin(rad), -Math.cos(rad), 5)).toBe(1);
	});
	test("no axes", () => {
		expect(axisAtOffset(1, 1, 0)).toBe(-1);
	});
});

describe("circlePath", () => {
	test("is two half arcs around the centre", () => {
		expect(circlePath(50, 50, 10)).toBe(
			"M40 50A10 10 0 1 0 60 50A10 10 0 1 0 40 50Z",
		);
	});
});

describe("ringBandTones", () => {
	test("steps up from the centre outward", () => {
		const tones = ringBandTones(5);
		expect(tones).toHaveLength(5);
		expect(tones[0]).toBeCloseTo(0.14, 5);
		expect(tones[4]).toBeCloseTo(0.62, 5);
		for (let i = 1; i < tones.length; i += 1) {
			expect(tones[i] ?? 0).toBeGreaterThan(tones[i - 1] ?? 0);
		}
	});
	test("one ring is the top tone", () => {
		expect(ringBandTones(1)).toEqual([0.62]);
	});
});
