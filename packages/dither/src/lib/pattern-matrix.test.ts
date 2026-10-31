import { describe, expect, test } from "bun:test";
import { matrixTileSide, tileRanks } from "./bayer";
import {
	ditherFillId,
	ditherMaskDataUri,
	ditherMaskStyle,
	ditherPatternSvg,
	ditherTilePath,
} from "./pattern";

describe("matrix helpers", () => {
	test("tile sides", () => {
		expect(matrixTileSide("bayer2")).toBe(2);
		expect(matrixTileSide("bayer4")).toBe(4);
		expect(matrixTileSide("bayer8")).toBe(8);
		expect(matrixTileSide("blue-noise")).toBe(16);
	});

	test("tileRanks has side² distinct ranks for every tile", () => {
		for (const side of [2, 4, 8, 16] as const) {
			expect(new Set(Array.from(tileRanks(side))).size).toBe(side * side);
		}
	});
});

describe("fills with a matrix", () => {
	test("blue noise uses a 16-cell tile and a different id from bayer", () => {
		const bayer = {
			tone: 0.5,
			color: "#fff",
			cell: 3,
			matrix: "bayer4",
		} as const;
		const blue = { ...bayer, matrix: "blue-noise" } as const;
		expect(ditherFillId(bayer)).not.toBe(ditherFillId(blue));
		expect(ditherPatternSvg(blue)).toContain('width="48"');
		expect(ditherPatternSvg(bayer)).toContain('width="12"');
	});

	test("`matrix` wins over `n`; `n` alone still works", () => {
		const viaN = { tone: 0.5, color: "#fff", n: 8 } as const;
		const viaMatrix = { tone: 0.5, color: "#fff", matrix: "bayer8" } as const;
		expect(ditherFillId(viaN)).toBe(ditherFillId(viaMatrix));
		expect(ditherFillId({ ...viaN, matrix: "bayer2" })).toBe(
			ditherFillId({ tone: 0.5, color: "#fff", matrix: "bayer2" }),
		);
	});

	test("blue noise path inks exactly the requested number of cells", () => {
		const path = ditherTilePath(64, 16);
		// Each inked cell contributes one `M…h…v…h…z` run; merged runs cover 64 cells.
		const area = [...path.matchAll(/h(\d+)v1/g)].reduce(
			(sum, m) => sum + Number(m[1]),
			0,
		);
		expect(area).toBe(64);
	});

	test("mask data URIs differ per matrix and are sized to the tile", () => {
		const a = ditherMaskDataUri({ tone: 0.5, cell: 2, matrix: "bayer4" });
		const b = ditherMaskDataUri({ tone: 0.5, cell: 2, matrix: "blue-noise" });
		expect(a).not.toBe(b);
		expect(decodeURIComponent(b)).toContain('width="32"');
	});
});

describe("ditherMaskStyle overload", () => {
	test("a bare tone equals the object form", () => {
		expect(ditherMaskStyle(0.25, { cell: 4 })).toEqual(
			ditherMaskStyle({ tone: 0.25, cell: 4 }),
		);
	});

	test("mask size is the tile times the cell", () => {
		expect(ditherMaskStyle(0.5, { cell: 4 }).maskSize).toBe("16px");
		expect(
			ditherMaskStyle(0.5, { cell: 2, matrix: "blue-noise" }).maskSize,
		).toBe("32px");
	});
});
