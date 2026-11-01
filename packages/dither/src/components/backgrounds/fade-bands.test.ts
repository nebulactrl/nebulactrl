import { describe, expect, test } from "bun:test";
import { fadeBandSize, fadeBandTones } from "./fade-bands";

describe("fadeBandTones", () => {
	test("one band per Bayer level, capped at 16", () => {
		expect(fadeBandTones(2)).toHaveLength(4);
		expect(fadeBandTones(4)).toHaveLength(16);
		expect(fadeBandTones(8)).toHaveLength(16);
	});

	test("ends solid and starts sparse", () => {
		const tones = fadeBandTones(4);
		expect(tones[tones.length - 1]).toBe(1);
		expect(tones[0]).toBeCloseTo(1 / 16, 9);
	});

	test("densifies monotonically towards the edge", () => {
		const tones = fadeBandTones();
		for (let i = 1; i < tones.length; i++) {
			expect(tones[i] as number).toBeGreaterThan(tones[i - 1] as number);
		}
	});
});

describe("fadeBandSize", () => {
	test("keeps a Bayer size and gives blue noise the full 16 bands", () => {
		expect(fadeBandSize("bayer2")).toBe(2);
		expect(fadeBandSize("bayer4")).toBe(4);
		expect(fadeBandSize("bayer8")).toBe(8);
		expect(fadeBandTones(fadeBandSize("blue-noise"))).toHaveLength(16);
	});
});
