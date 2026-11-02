import { describe, expect, test } from "bun:test";
import { bayerMatrix } from "./bayer";
import { BLUE_NOISE_SIDE, blueNoiseThresholds } from "./blue-noise";
import { inkedAt, thresholdAt, thresholdSampler, toMatrix } from "./threshold";

describe("toMatrix", () => {
	test("maps Bayer sizes and passes names through; undefined is bayer4", () => {
		expect(toMatrix(undefined)).toBe("bayer4");
		expect(toMatrix(2)).toBe("bayer2");
		expect(toMatrix(4)).toBe("bayer4");
		expect(toMatrix(8)).toBe("bayer8");
		expect(toMatrix("blue-noise")).toBe("blue-noise");
		expect(toMatrix("bayer2")).toBe("bayer2");
	});
});

describe("thresholdSampler", () => {
	test("reads the Bayer tile cell for cell and repeats it", () => {
		const tile = bayerMatrix(4);
		const sample = thresholdSampler("bayer4");
		for (let y = 0; y < 4; y++) {
			for (let x = 0; x < 4; x++) {
				expect(sample(x, y)).toBe(tile[y * 4 + x] as number);
				expect(sample(x + 8, y + 12)).toBe(tile[y * 4 + x] as number);
			}
		}
	});

	test("negative coordinates tile the same way as positive ones", () => {
		const tile = bayerMatrix(4);
		const sample = thresholdSampler("bayer4");
		expect(sample(-1, -1)).toBe(tile[3 * 4 + 3] as number);
		expect(sample(-4, 0)).toBe(sample(0, 0));
		const noise = thresholdSampler("blue-noise");
		expect(noise(-1, -1)).toBe(
			blueNoiseThresholds()[
				(BLUE_NOISE_SIDE - 1) * BLUE_NOISE_SIDE + (BLUE_NOISE_SIDE - 1)
			] as number,
		);
		expect(noise(BLUE_NOISE_SIDE, 0)).toBe(noise(0, 0));
	});

	test("returns the same sampler for the same matrix", () => {
		expect(thresholdSampler("bayer8")).toBe(thresholdSampler("bayer8"));
		expect(thresholdSampler("bayer8")).not.toBe(thresholdSampler("bayer2"));
	});
});

describe("thresholdAt / inkedAt", () => {
	test("thresholdAt is one sample", () => {
		expect(thresholdAt("bayer2", 1, 1)).toBe(bayerMatrix(2)[3] as number);
	});

	test("a cell is inked only when its tone is above the threshold", () => {
		const t = thresholdAt("bayer4", 2, 3);
		expect(inkedAt(t + 0.001, 2, 3, "bayer4")).toBe(true);
		expect(inkedAt(t, 2, 3, "bayer4")).toBe(false);
		expect(inkedAt(0, 2, 3, "bayer4")).toBe(false);
	});
});
