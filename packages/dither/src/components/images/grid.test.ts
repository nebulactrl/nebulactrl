import { describe, expect, test } from "bun:test";
import {
	boxAspect,
	effectiveCell,
	gridFor,
	halvingSizes,
	readCellVar,
	snapCell,
} from "./grid";

describe("cell grid", () => {
	test("a cell is a whole number of device pixels", () => {
		expect(snapCell(3, 1)).toEqual({ cellDev: 3, cssCell: 3 });
		expect(snapCell(3, 1.25)).toEqual({ cellDev: 4, cssCell: 3.2 });
		expect(snapCell(0.2, 2).cellDev).toBe(1);
	});

	test("the grid covers the box and sizes the canvas in whole cells", () => {
		const grid = gridFor(100, 50, 4, 1);
		expect(grid).toMatchObject({
			cols: 25,
			rows: 13,
			cellDev: 4,
			cssWidth: 100,
			cssHeight: 52,
		});
	});

	test("at dpr 2 the canvas CSS size is a whole number of device cells", () => {
		const grid = gridFor(101, 40, 3, 2);
		expect(grid.cellDev).toBe(6);
		expect(grid.cols).toBe(Math.ceil(202 / 6));
		expect(grid.cssWidth * 2).toBe(grid.cols * 6);
	});

	test("a huge box grows the cell instead of the grid", () => {
		const grid = gridFor(8000, 400, 1, 1, 2048);
		expect(grid.cols).toBeLessThanOrEqual(2048);
		expect(grid.cellDev).toBeGreaterThan(1);
	});

	test("readCellVar falls back without a document", () => {
		expect(readCellVar()).toBe(3);
	});
});

describe("cell for a box", () => {
	test("an explicit cell is kept; a preset cell shrinks to keep enough columns", () => {
		expect(effectiveCell(3, 100, 0)).toBe(3);
		expect(effectiveCell(3, 1200, 200)).toBe(3);
		expect(effectiveCell(3, 400, 200)).toBe(2);
		expect(effectiveCell(3, 100, 200)).toBe(1);
		expect(effectiveCell(2, 0, 200)).toBe(2);
	});
});

describe("halving a large source", () => {
	test("halves until the next half would undershoot the target", () => {
		expect(halvingSizes(4000, 3000, 500, 400)).toEqual([
			{ width: 2000, height: 1500 },
			{ width: 1000, height: 750 },
		]);
		expect(halvingSizes(400, 300, 200, 150)).toEqual([]);
		expect(halvingSizes(100, 100, 200, 200)).toEqual([]);
	});
});

describe("box aspect", () => {
	const natural = { width: 400, height: 200 };
	test("the ratio prop, then an aspect class, then the image's own", () => {
		expect(boxAspect(1.5, natural, "aspect-video")).toBe(1.5);
		expect(
			boxAspect(undefined, natural, "w-full aspect-video"),
		).toBeUndefined();
		expect(boxAspect(undefined, natural, "sm:aspect-square")).toBeUndefined();
		expect(boxAspect(undefined, natural, "w-full")).toBe(2);
		expect(boxAspect(undefined, null, "w-full")).toBeUndefined();
	});
});
