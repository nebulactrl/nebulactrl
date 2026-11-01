import { describe, expect, test } from "bun:test";
import { bandMaskKey, bandMaskSvg, rampStops, tilePath } from "./bands";

// A threshold test that does not depend on the engine: inked when the tone
// beats a fixed diagonal threshold.
const diag = (tone: number, x: number, y: number, n: number) =>
	tone > ((x + y) % n) / n;

describe("rampStops", () => {
	test("runs from `from` to `to` inclusive and clamps", () => {
		expect(rampStops(0.6, 0, 4)).toEqual([0.6, 0.4, 0.2, 0]);
		expect(rampStops(2, -1, 2)).toEqual([1, 0]);
		expect(rampStops(0.5, 0.5, 1)).toEqual([0.5]);
	});
});

describe("tilePath", () => {
	test("is empty at tone 0 and denser at higher tones", () => {
		expect(tilePath(0, 2, 4, diag)).toBe("");
		const low = tilePath(0.3, 2, 4, diag).split("M").length;
		const high = tilePath(0.9, 2, 4, diag).split("M").length;
		expect(high).toBeGreaterThan(low);
	});
});

describe("bandMaskSvg", () => {
	test("skips empty bands and shares one pattern per tone", () => {
		const svg = bandMaskSvg(
			{ axis: "y", stops: [0.5, 0.5, 0, 0.25], cell: 2, n: 4 },
			diag,
		);
		expect(svg.match(/<pattern /g)?.length).toBe(2);
		expect(svg.match(/<rect /g)?.length).toBe(3);
		expect(svg).toContain('height="25%"');
	});

	test("an x-axis mask places bands left to right", () => {
		const svg = bandMaskSvg({ axis: "x", stops: [0.5, 0.5], cell: 2 }, diag);
		expect(svg).toContain('x="50%"');
		expect(svg).toContain('height="100%"');
	});
});

describe("bandMaskKey", () => {
	test("equal masks share a key; any difference changes it", () => {
		const a = { axis: "y", stops: [0.5, 0.2] } as const;
		expect(bandMaskKey(a)).toBe(bandMaskKey({ ...a, stops: [0.5, 0.2] }));
		expect(bandMaskKey(a)).not.toBe(bandMaskKey({ ...a, stops: [0.5, 0.3] }));
		expect(bandMaskKey(a)).not.toBe(bandMaskKey({ ...a, axis: "x" }));
	});
});

describe("textured band masks", () => {
	test("a texture or matrix switches to the textured builder", () => {
		const base = { axis: "y", stops: [0.5, 0.25], cell: 2 } as const;
		expect(bandMaskKey(base)).not.toBe(
			bandMaskKey({ ...base, texture: "dotted" }),
		);
		expect(bandMaskKey(base)).not.toBe(
			bandMaskKey({ ...base, matrix: "blue-noise" }),
		);
	});

	test("solid bands are one rect at the tone's opacity", () => {
		const svg = bandMaskSvg({
			axis: "x",
			stops: [0.5, 0.25],
			cell: 2,
			texture: "solid",
		});
		expect(svg).toContain('fill-opacity="0.5"');
		expect(svg).toContain('fill-opacity="0.25"');
	});

	test("blue noise tiles on 16 cells and dotted differs from gradient", () => {
		const blue = bandMaskSvg({
			axis: "y",
			stops: [0.5],
			cell: 2,
			matrix: "blue-noise",
		});
		expect(blue).toContain('width="32"');
		const gradient = bandMaskSvg({
			axis: "y",
			stops: [0.5],
			cell: 2,
			texture: "gradient",
		});
		const dotted = bandMaskSvg({
			axis: "y",
			stops: [0.5],
			cell: 2,
			texture: "dotted",
		});
		expect(dotted).not.toBe(gradient);
	});
});
