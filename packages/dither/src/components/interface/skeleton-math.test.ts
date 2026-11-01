import { describe, expect, test } from "bun:test";
import {
	lineWidths,
	PULSE_LEVELS,
	restMask,
	SWEEP_STOPS,
} from "./skeleton-math";

describe("lineWidths", () => {
	test("the last of several lines is shorter", () => {
		expect(lineWidths(3)).toEqual(["100%", "100%", "60%"]);
		expect(lineWidths(2)).toEqual(["100%", "60%"]);
	});

	test("one line is full width and the count is clamped", () => {
		expect(lineWidths(1)).toEqual(["100%"]);
		expect(lineWidths(0)).toEqual(["100%"]);
		expect(lineWidths(2.9)).toHaveLength(2);
	});
});

describe("restMask", () => {
	test("no fill keeps the flat house grain", () => {
		expect(restMask(undefined)).toEqual({ axis: "y", stops: [0.2] });
	});

	test("gradient thins out from the left, textures stay flat", () => {
		const ramp = restMask("gradient");
		expect(ramp.axis).toBe("x");
		expect(ramp.stops[0] ?? 0).toBeGreaterThan(ramp.stops.at(-1) ?? 1);
		expect(restMask("dotted").texture).toBe("dotted");
		expect(restMask("hatched").stops).toHaveLength(1);
		expect(restMask("solid").texture).toBe("solid");
	});
});

describe("pulse and sweep", () => {
	test("pulse levels are three increasing tones", () => {
		expect(PULSE_LEVELS).toHaveLength(3);
		expect(PULSE_LEVELS[0]).toBeLessThan(PULSE_LEVELS[1]);
		expect(PULSE_LEVELS[1]).toBeLessThan(PULSE_LEVELS[2]);
	});

	test("the sweep band peaks in the middle", () => {
		const peak = Math.max(...SWEEP_STOPS);
		expect(SWEEP_STOPS.indexOf(peak)).toBe(3);
		expect(SWEEP_STOPS[0]).toBeCloseTo(SWEEP_STOPS.at(-1) ?? 1);
	});
});
