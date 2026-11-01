import { describe, expect, test } from "bun:test";
import type { DitherMatrix } from "../../lib/bayer";
import {
	bloomFilter,
	bloomShadow,
	cellInk,
	FILL_VARIANTS,
	fillTile,
	fillTileSide,
	inkCss,
	onDotLattice,
	onHatch,
} from "./fill";

const MATRICES: DitherMatrix[] = ["bayer2", "bayer4", "bayer8", "blue-noise"];

function density(
	fill: (typeof FILL_VARIANTS)[number],
	tone: number,
	matrix: DitherMatrix,
	size = 16,
) {
	let total = 0;
	for (let y = 0; y < size; y++)
		for (let x = 0; x < size; x++) total += cellInk(fill, tone, x, y, matrix);
	return total / (size * size);
}

describe("cellInk", () => {
	test("is empty at tone 0 and a tint for solid", () => {
		for (const fill of FILL_VARIANTS) expect(cellInk(fill, 0, 3, 3)).toBe(0);
		expect(cellInk("solid", 0.4, 3, 3)).toBeCloseTo(0.4);
		expect(cellInk("solid", 3, 3, 3)).toBe(1);
	});

	test("gradient inks about its tone in every matrix", () => {
		for (const matrix of MATRICES) {
			expect(density("gradient", 0.5, matrix)).toBeCloseTo(0.5, 1);
			expect(density("gradient", 0.25, matrix)).toBeCloseTo(0.25, 1);
		}
	});

	test("a denser tone inks a superset of a sparser one", () => {
		for (const fill of ["gradient", "dotted", "hatched"] as const) {
			for (const matrix of MATRICES) {
				for (let y = 0; y < 16; y++) {
					for (let x = 0; x < 16; x++) {
						if (cellInk(fill, 0.3, x, y, matrix) > 0) {
							expect(cellInk(fill, 0.6, x, y, matrix)).toBeGreaterThan(0);
						}
					}
				}
			}
		}
	});

	test("dotted and hatched only ink their own lattice", () => {
		for (let y = 0; y < 8; y++) {
			for (let x = 0; x < 8; x++) {
				if (cellInk("dotted", 1, x, y) > 0)
					expect(onDotLattice(x, y)).toBe(true);
				if (cellInk("hatched", 1, x, y) > 0) expect(onHatch(x, y)).toBe(true);
			}
		}
		expect(density("dotted", 1, "bayer4")).toBeCloseTo(0.25, 5);
		expect(density("hatched", 1, "bayer4")).toBeCloseTo(0.5, 5);
	});

	test("the lattices wrap for negative cells", () => {
		expect(onDotLattice(-4, -4)).toBe(onDotLattice(0, 0));
		expect(onHatch(-3, 1)).toBe(onHatch(1, 1));
	});
});

describe("fillTile", () => {
	test("tile edge follows the matrix and widens for textures", () => {
		expect(fillTile("gradient", "bayer4", 0.5, 2).size).toBe(8);
		expect(fillTile("gradient", "blue-noise", 0.5, 2).size).toBe(32);
		expect(fillTileSide("dotted", "bayer2")).toBe(4);
		expect(fillTileSide("gradient", "bayer2")).toBe(2);
	});

	test("is empty at tone 0 and a flat rect at opacity = tone for solid", () => {
		expect(fillTile("gradient", "bayer4", 0, 2).d).toBe("");
		const solid = fillTile("solid", "bayer4", 0.4, 2);
		expect(solid.opacity).toBeCloseTo(0.4);
		expect(solid.d).toBe("M0 0h8v8h-8z");
	});

	test("denser tones draw more path", () => {
		const low = fillTile("gradient", "bayer4", 0.2, 2).d.length;
		const high = fillTile("gradient", "bayer4", 0.9, 2).d.length;
		expect(high).toBeGreaterThan(low);
	});
});

describe("colour and bloom", () => {
	test("names, custom properties and css colours resolve", () => {
		expect(inkCss("blue")).toBe("var(--dither-blue)");
		expect(inkCss(undefined)).toBe("var(--dither-ink)");
		expect(inkCss("--chart-1")).toBe("var(--chart-1)");
		expect(inkCss("#fff")).toBe("#fff");
	});

	test("bloom is off by default and stronger at high", () => {
		expect(bloomShadow("red", undefined)).toBeUndefined();
		expect(bloomShadow("red", "off")).toBeUndefined();
		expect(bloomShadow("red", "high")).not.toBe(bloomShadow("red", "low"));
		expect(bloomFilter("red", "low")).toContain("drop-shadow");
		expect(bloomFilter("red", "off")).toBeUndefined();
	});
});
