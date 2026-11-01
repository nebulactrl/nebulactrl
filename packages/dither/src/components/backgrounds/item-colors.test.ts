import { describe, expect, test } from "bun:test";
import { MAX_PALETTE } from "../../lib/gl/palette";
import { planInks } from "./item-colors";

describe("planInks", () => {
	test("no custom colours keeps the three-entry ramp and is not coloured", () => {
		const plan = planInks("paper", "ink", "orange", [undefined, undefined]);
		expect(plan.palette).toEqual(["paper", "ink", "orange"]);
		expect(plan.positions).toEqual([1, 1]);
		expect(plan.coloured).toBe(false);
	});

	test("a custom colour is appended and addressed by ramp position", () => {
		const plan = planInks("paper", "ink", "orange", ["blue", undefined]);
		expect(plan.palette).toEqual(["paper", "ink", "orange", "blue"]);
		// Three inks (ink, orange, blue) sit at 0, 0.5 and 1 on the ramp.
		expect(plan.positions).toEqual([1, 0.5]);
		expect(plan.coloured).toBe(true);
	});

	test("equal colours share one entry", () => {
		const plan = planInks("paper", "ink", "orange", [
			"blue",
			"blue",
			"ink",
			"orange",
		]);
		expect(plan.palette).toEqual(["paper", "ink", "orange", "blue"]);
		expect(plan.positions).toEqual([1, 1, 0, 0.5]);
	});

	test("never grows past the engine's ramp; extra colours use the accent", () => {
		const many = Array.from({ length: 12 }, (_, i) => `c${i}`);
		const plan = planInks("paper", "ink", "orange", many);
		expect(plan.palette).toHaveLength(MAX_PALETTE);
		expect(plan.positions).toHaveLength(12);
		const last = plan.positions.at(-1);
		expect(last).toBeCloseTo(1 / (MAX_PALETTE - 2), 9);
		for (const pos of plan.positions) {
			expect(pos).toBeGreaterThanOrEqual(0);
			expect(pos).toBeLessThanOrEqual(1);
		}
	});
});
