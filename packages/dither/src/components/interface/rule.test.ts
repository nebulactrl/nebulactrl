import { describe, expect, test } from "bun:test";
import { resolveRule, ruleStops } from "./rule";

describe("ruleStops", () => {
	test("a lone rule is symmetric, dense in the middle and sparse at the tips", () => {
		const stops = ruleStops({ fade: 0.6, strength: 0.6 });
		expect(stops).toHaveLength(20);
		expect(stops[0]).toBeLessThan(stops[5] ?? 0);
		expect(stops[9]).toBe(0.6);
		expect(stops[10]).toBe(0.6);
		for (let i = 0; i < 10; i++) expect(stops[i]).toBe(stops[19 - i]);
		expect(Math.max(...stops)).toBeCloseTo(0.6);
	});

	test("fade 0 is flat and fade 1 fades all the way to the middle", () => {
		expect(new Set(ruleStops({ fade: 0, strength: 0.5 }))).toEqual(
			new Set([0.5]),
		);
		const full = ruleStops({ fade: 1, strength: 1 });
		expect(full[9] ?? 0).toBeGreaterThan(0.9);
		expect(full[0] ?? 1).toBeLessThan(0.1);
	});

	test("the halves around a label fade only at their outer end", () => {
		const start = ruleStops({ fade: 0.5, strength: 0.8, side: "start" });
		const end = ruleStops({ fade: 0.5, strength: 0.8, side: "end" });
		expect(start).toHaveLength(10);
		expect(start[0] ?? 1).toBeLessThan(0.3);
		expect(start.at(-1)).toBe(0.8);
		expect(end).toEqual([...start].reverse());
	});

	test("strength is clamped", () => {
		expect(Math.max(...ruleStops({ fade: 0.6, strength: 3 }))).toBe(1);
		expect(Math.max(...ruleStops({ fade: 0.6, strength: -1 }))).toBe(0);
	});
});

describe("resolveRule", () => {
	test("without custom props the variant's own rule is used", () => {
		const fade = resolveRule({ variant: "fade", side: "both" });
		expect(fade.thickness).toBe(4);
		expect(fade.cell).toBe(2);
		expect(fade.stops).toHaveLength(11);
		expect(resolveRule({ variant: "solid", side: "both" }).stops).toEqual([
			0.75,
		]);
		const dots = resolveRule({ variant: "dots", side: "both" });
		expect(dots.stops).toEqual([0.3]);
		expect(dots.cell).toBe(3);
		expect(dots.texture).toBeUndefined();
	});

	test("a labelled default fade is solid next to the label", () => {
		const left = resolveRule({ variant: "fade", side: "start" });
		expect(left.stops.at(-1)).toBe(0.7);
		expect(left.stops[0]).toBe(0);
	});

	test("fade, strength or fill switch to the ten-step rule", () => {
		const custom = resolveRule({
			variant: "fade",
			side: "both",
			strength: 0.4,
		});
		expect(custom.stops).toHaveLength(20);
		expect(Math.max(...custom.stops)).toBeCloseTo(0.4);
		const flat = resolveRule({
			variant: "solid",
			side: "both",
			fill: "hatched",
		});
		expect(new Set(flat.stops).size).toBe(1);
		expect(flat.texture).toBe("hatched");
	});

	test("thickness is in cells", () => {
		expect(
			resolveRule({ variant: "fade", side: "both", thickness: 1, cell: 3 })
				.thickness,
		).toBe(3);
		expect(
			resolveRule({ variant: "fade", side: "both", thickness: 2 }).thickness,
		).toBe(4);
	});
});
