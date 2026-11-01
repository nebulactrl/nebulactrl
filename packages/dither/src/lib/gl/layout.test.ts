import { describe, expect, test } from "bun:test";
import {
	chooseSubdivision,
	clampDpr,
	computeLayout,
	crispCellSizes,
	parseCellValue,
	snapCell,
} from "./layout";

describe("snapCell", () => {
	test("keeps whole device pixels at 1x and 2x", () => {
		expect(snapCell(3, 1)).toEqual({ dpr: 1, cellDev: 3, cellCss: 3 });
		expect(snapCell(3, 2)).toEqual({ dpr: 2, cellDev: 6, cellCss: 3 });
	});

	test("rounds 4.5 device pixels up at 1.5x, so the cell is 3.33 CSS px", () => {
		const cell = snapCell(3, 1.5);
		expect(cell.cellDev).toBe(5);
		expect(cell.cellCss).toBeCloseTo(10 / 3, 10);
	});

	test("never drops below one device pixel and clamps odd ratios", () => {
		expect(snapCell(0.1, 1).cellDev).toBe(1);
		expect(snapCell(3, 0).dpr).toBe(1);
		expect(snapCell(3, 5).dpr).toBe(2);
		expect(clampDpr(Number.NaN)).toBe(1);
	});

	test("the snapped size is always an integer number of device pixels", () => {
		for (const dpr of [1, 1.25, 1.5, 1.75, 2]) {
			for (let css = 1; css <= 12; css += 0.5) {
				const { cellDev } = snapCell(css, dpr);
				expect(Number.isInteger(cellDev)).toBe(true);
			}
		}
	});
});

describe("crispCellSizes", () => {
	test("every integer size is crisp at 1x and 2x", () => {
		expect(crispCellSizes(1, 4)).toEqual([1, 2, 3, 4]);
		expect(crispCellSizes(2, 3)).toEqual([1, 1.5, 2, 2.5, 3]);
	});

	test("at 1.5x only even sizes are crisp", () => {
		expect(crispCellSizes(1.5, 4)).toEqual([2, 4]);
	});
});

describe("computeLayout", () => {
	test("covers the box to the next whole cell", () => {
		const layout = computeLayout(100, 50, 3, 1, "square");
		expect(layout.cols).toBe(34);
		expect(layout.rows).toBe(17);
		expect(layout.cssW).toBe(102);
		expect(layout.aspect).toBe(2);
	});

	test("a 3px cell on a 2x screen is 6 device pixels and the same CSS size", () => {
		const layout = computeLayout(300, 150, 3, 2, "square");
		expect(layout.cellDev).toBe(6);
		expect(layout.cellCss).toBe(3);
		expect(layout.cols).toBe(100);
	});

	test("dots subdivide only into divisors of the device cell", () => {
		expect(chooseSubdivision(6, "dot")).toBe(6);
		expect(chooseSubdivision(5, "dot")).toBe(5);
		expect(chooseSubdivision(7, "dot")).toBe(1);
		expect(chooseSubdivision(2, "dot")).toBe(1);
		expect(chooseSubdivision(6, "square")).toBe(1);
	});

	test("huge boxes are coarsened", () => {
		const layout = computeLayout(100000, 100, 1, 1, "square");
		expect(layout.cols).toBeLessThanOrEqual(4096);
	});
});

describe("parseCellValue", () => {
	test("parses px, bare numbers and rem; falls back otherwise", () => {
		expect(parseCellValue("3px")).toBe(3);
		expect(parseCellValue("4")).toBe(4);
		expect(parseCellValue("0.25rem")).toBe(4);
		expect(parseCellValue("nope")).toBe(3);
		expect(parseCellValue(undefined)).toBe(3);
	});
});
