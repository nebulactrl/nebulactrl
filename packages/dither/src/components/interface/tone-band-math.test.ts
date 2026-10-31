import { describe, expect, test } from "bun:test";
import {
	TONE_BAND_CELL,
	TONE_BAND_TONES,
	TONE_BAND_WIDTH,
	toneBandMask,
	toneBandMaskCss,
	toneBandProps,
	toneBandStops,
} from "./tone-band-math";

describe("toneBandStops", () => {
	test("one stop per cell column across the band", () => {
		expect(toneBandStops()).toHaveLength(TONE_BAND_WIDTH / TONE_BAND_CELL);
		expect(toneBandStops(20, 4)).toHaveLength(5);
		expect(toneBandStops(1, 4)).toHaveLength(1);
	});

	test("starts solid at the edge and never gets denser inward", () => {
		const stops = toneBandStops();
		expect(stops[0]).toBe(1);
		for (let i = 1; i < stops.length; i++) {
			expect(stops[i]).toBeLessThanOrEqual(stops[i - 1] ?? 0);
		}
		expect(stops.every((tone) => tone >= 0 && tone <= 1)).toBe(true);
		// The inner column is faint so the band blends into the wash.
		expect(stops.at(-1)).toBeLessThan(0.25);
	});
});

describe("toneBandMask", () => {
	test("runs left to right so density falls off inward", () => {
		const mask = toneBandMask();
		expect(mask.axis).toBe("x");
		expect(mask.stops).toEqual(toneBandStops());
	});

	test("is a CSS url of an SVG data URI", () => {
		expect(toneBandMaskCss()).toMatch(/^url\("data:image\/svg\+xml,/);
	});
});

describe("toneBandProps", () => {
	test("carries the class and the tone for every tone", () => {
		for (const tone of TONE_BAND_TONES) {
			expect(toneBandProps(tone)).toEqual({
				className: "tone-band",
				"data-tone": tone,
			});
		}
	});
});
