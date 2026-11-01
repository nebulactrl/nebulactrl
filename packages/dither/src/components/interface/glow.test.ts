import { describe, expect, test } from "bun:test";
import { glowOrigin, glowRings, hoverRings, rimRings } from "./glow";

describe("glowOrigin", () => {
	test("corners are circles, top and bottom are wide", () => {
		expect(glowOrigin("top-left")).toEqual({ cx: "0", cy: "0", wide: false });
		expect(glowOrigin("bottom-right")).toEqual({
			cx: "100%",
			cy: "100%",
			wide: false,
		});
		expect(glowOrigin("top").wide).toBe(true);
		expect(glowOrigin("bottom").cy).toBe("100%");
	});
});

describe("glowRings", () => {
	test("a gradient is outermost first, sparsest to densest, ending at strength", () => {
		const rings = glowRings({ reach: 240, strength: 0.6, fill: "gradient" });
		expect(rings).toHaveLength(6);
		expect(rings[0]?.r).toBe(240);
		expect(rings.at(-1)?.r).toBe(40);
		expect(rings.at(-1)?.tone).toBeCloseTo(0.6);
		for (let i = 1; i < rings.length; i++) {
			expect(rings[i]?.r).toBeLessThan(rings[i - 1]?.r ?? 0);
			expect(rings[i]?.tone).toBeGreaterThan(rings[i - 1]?.tone ?? 1);
		}
		expect(rings.every((ring) => ring.opacity === 1)).toBe(true);
	});

	test("textures keep one tone and fade by overlap", () => {
		for (const fill of ["dotted", "hatched", "solid"] as const) {
			const rings = glowRings({ reach: 100, strength: 0.5, fill });
			expect(new Set(rings.map((ring) => ring.tone)).size).toBe(1);
			expect(rings[0]?.opacity).toBeGreaterThan(0);
			expect(rings[0]?.opacity).toBeLessThan(1);
		}
	});

	test("strength and ring count are clamped", () => {
		expect(
			glowRings({ reach: 10, strength: 4, fill: "gradient" }).at(-1)?.tone,
		).toBe(1);
		expect(
			glowRings({ reach: 10, strength: 0.5, fill: "gradient", rings: 0 }),
		).toHaveLength(1);
	});

	test("the rim reaches further and the hover stack is larger than the glow", () => {
		const base = { reach: 200, strength: 0.5, fill: "gradient" } as const;
		expect(rimRings(base)[0]?.r).toBe(320);
		expect(rimRings(base).at(-1)?.tone).toBeCloseTo(0.8);
		expect(hoverRings(base)[0]?.r).toBe(300);
	});
});
