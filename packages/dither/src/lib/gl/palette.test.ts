import { describe, expect, test } from "bun:test";
import {
	chooseSubdivision,
	computeLayout,
	parseCellValue,
	sameLayout,
} from "./layout";
import {
	cssColorExpr,
	DEFAULT_LEVELS,
	levelsForPalette,
	limitPalette,
	MAX_PALETTE,
	packPalette,
	paletteToVec3Array,
	parseCssColor,
	type RGBA,
	resolveLevels,
} from "./palette";

describe("parseCssColor", () => {
	test("hex forms", () => {
		expect(parseCssColor("#fff")).toEqual([1, 1, 1, 1]);
		expect(parseCssColor("#000000")).toEqual([0, 0, 0, 1]);
		const [r, g, b, a] = parseCssColor("#ff000080") as RGBA;
		expect([r, g, b]).toEqual([1, 0, 0]);
		expect(a).toBeCloseTo(0.502, 2);
	});

	test("rgb, rgba and space syntax", () => {
		expect(parseCssColor("rgb(255, 0, 0)")).toEqual([1, 0, 0, 1]);
		expect(parseCssColor("rgba(0, 255, 0, 0.5)")).toEqual([0, 1, 0, 0.5]);
		expect(parseCssColor("rgb(0 0 255 / 25%)")).toEqual([0, 0, 1, 0.25]);
	});

	test("color(srgb ...) and transparent", () => {
		expect(parseCssColor("color(srgb 1 0.5 0)")).toEqual([1, 0.5, 0, 1]);
		expect(parseCssColor("color(srgb 0 0 0 / 0.4)")).toEqual([0, 0, 0, 0.4]);
		expect(parseCssColor("transparent")).toEqual([0, 0, 0, 0]);
	});

	test("leaves modern spaces for the canvas probe", () => {
		expect(parseCssColor("oklch(0.7 0.15 270)")).toBeNull();
		expect(parseCssColor("color(display-p3 1 0 0)")).toBeNull();
		expect(parseCssColor("not a colour")).toBeNull();
	});

	test("leading space and srgb are dropped from the arguments", () => {
		expect(parseCssColor("color( srgb  1 0 0)")).toEqual([1, 0, 0, 1]);
		expect(parseCssColor("rgb(  255 0 0  )")).toEqual([1, 0, 0, 1]);
	});

	test("a long run of spaces is rejected in linear time", () => {
		const started = performance.now();
		expect(parseCssColor(`rgb(${" ".repeat(200_000)}`)).toBeNull();
		expect(parseCssColor(`color(${" ".repeat(200_000)}x)`)).toBeNull();
		expect(performance.now() - started).toBeLessThan(1000);
	});
});

describe("cssColorExpr", () => {
	test("wraps token names, passes colours through", () => {
		expect(cssColorExpr("--chart-1")).toBe("var(--chart-1)");
		expect(cssColorExpr("#fff")).toBe("#fff");
		expect(cssColorExpr("oklch(0.6 0.2 300)")).toBe("oklch(0.6 0.2 300)");
		expect(cssColorExpr("--dither-ink")).toContain("var(--dither-ink, ");
	});
});

describe("palette ramp packing", () => {
	const red: RGBA = [1, 0, 0, 1];
	const green: RGBA = [0, 1, 0, 1];
	const blue: RGBA = [0, 0, 1, 0.5];

	test("packs rgba and pads with the last colour", () => {
		const { count, data } = packPalette([red, green, blue]);
		expect(count).toBe(3);
		expect(data.length).toBe(MAX_PALETTE * 4);
		expect(Array.from(data.slice(0, 12))).toEqual([
			1, 0, 0, 1, 0, 1, 0, 1, 0, 0, 1, 0.5,
		]);
		expect(Array.from(data.slice(12, 16))).toEqual([0, 0, 1, 0.5]);
		expect(Array.from(data.slice(28, 32))).toEqual([0, 0, 1, 0.5]);
	});

	test("widens one colour to a ramp of two and empty to paper/ink", () => {
		expect(packPalette([red]).count).toBe(2);
		expect(packPalette([]).count).toBe(2);
	});

	test("reuses a scratch buffer", () => {
		const scratch = new Float32Array(MAX_PALETTE * 4);
		expect(packPalette([red, green], scratch).data).toBe(scratch);
	});

	test("long ramps keep both ends", () => {
		const colors = Array.from(
			{ length: 20 },
			(_, i): RGBA => [i / 19, 0, 0, 1],
		);
		const limited = limitPalette(colors);
		expect(limited.length).toBe(MAX_PALETTE);
		expect(limited[0]).toEqual(colors[0]);
		expect(limited[MAX_PALETTE - 1]).toEqual(colors[19]);
	});

	test("vec3 array flattens rgb", () => {
		expect(paletteToVec3Array([red, blue])).toEqual([1, 0, 0, 0, 0, 1]);
	});
});

describe("levelsForPalette", () => {
	test("lands on the stops: levels - 1 is a multiple of colours - 1", () => {
		for (const matrix of [
			"bayer2",
			"bayer4",
			"bayer8",
			"blue-noise",
		] as const) {
			for (const colours of [2, 3, 5]) {
				const levels = levelsForPalette(colours, matrix);
				expect((levels - 1) % (colours - 1)).toBe(0);
			}
		}
	});

	test("is the smallest fit at or above 6 where the matrix draws enough tones", () => {
		for (const matrix of ["bayer4", "bayer8", "blue-noise"] as const) {
			expect(levelsForPalette(2, matrix)).toBe(6);
			expect(levelsForPalette(3, matrix)).toBe(7);
			expect(levelsForPalette(5, matrix)).toBe(9);
		}
	});

	test("is capped by the tones bayer2 draws", () => {
		expect(levelsForPalette(2, "bayer2")).toBe(5);
		expect(levelsForPalette(3, "bayer2")).toBe(5);
		expect(levelsForPalette(5, "bayer2")).toBe(5);
		// 4 colours need a multiple of 3; 4 is the largest that fits.
		expect(levelsForPalette(4, "bayer2")).toBe(4);
	});

	test("more stops than tones returns the stop count", () => {
		expect(levelsForPalette(8, "bayer2")).toBe(8);
	});

	test("colour counts are clamped to what the shader holds", () => {
		expect(levelsForPalette(0, "bayer4")).toBe(levelsForPalette(2, "bayer4"));
		expect(levelsForPalette(1, "bayer4")).toBe(levelsForPalette(2, "bayer4"));
		expect(levelsForPalette(40, "bayer8")).toBe(
			levelsForPalette(MAX_PALETTE, "bayer8"),
		);
	});
});

describe("resolveLevels", () => {
	test("a number is kept and the default stays 6", () => {
		expect(resolveLevels(12, 3, "bayer4")).toBe(12);
		expect(resolveLevels(undefined, 3, "bayer4")).toBe(6);
		expect(DEFAULT_LEVELS).toBe(6);
	});

	test('"full" is every tone the matrix draws', () => {
		expect(resolveLevels("full", 3, "bayer2")).toBe(5);
		expect(resolveLevels("full", 3, "bayer4")).toBe(17);
		expect(resolveLevels("full", 3, "bayer8")).toBe(65);
		expect(resolveLevels("full", 3, "blue-noise")).toBe(256);
	});

	test('"stops" follows the palette size', () => {
		expect(resolveLevels("stops", 5, "bayer4")).toBe(9);
	});
});

describe("layout", () => {
	test("cells cover the box and the canvas is one pixel per cell", () => {
		const l = computeLayout(1440, 700, 3, 1, "square");
		expect(l.cols).toBe(480);
		expect(l.rows).toBe(234);
		expect(l.canvasW).toBe(480);
		expect(l.sub).toBe(1);
		expect(l.cssW).toBe(1440);
	});

	test("cell rounds to whole device pixels at fractional DPR", () => {
		const l = computeLayout(300, 300, 3, 1.5, "square");
		expect(l.cellDev).toBe(5); // 4.5 -> 5 device px
		expect(l.cellCss).toBeCloseTo(5 / 1.5, 6);
	});

	test("DPR is capped at 2", () => {
		expect(computeLayout(300, 300, 3, 3, "square").dpr).toBe(2);
	});

	test("dots subdivide only into whole device pixels", () => {
		expect(chooseSubdivision(6, "dot")).toBe(6);
		expect(chooseSubdivision(3, "dot")).toBe(3);
		expect(chooseSubdivision(8, "dot")).toBe(4);
		expect(chooseSubdivision(7, "dot")).toBe(1);
		expect(chooseSubdivision(2, "dot")).toBe(1);
		expect(chooseSubdivision(6, "square")).toBe(1);
		const l = computeLayout(300, 300, 3, 2, "dot");
		expect(l.canvasW).toBe(l.cols * 6);
	});

	test("huge boxes are coarsened", () => {
		const l = computeLayout(40_000, 100, 1, 1, "square");
		expect(l.cols).toBeLessThanOrEqual(4096);
	});

	test("cell custom property parsing", () => {
		expect(parseCellValue("3px")).toBe(3);
		expect(parseCellValue(" 4 ")).toBe(4);
		expect(parseCellValue("0.25rem")).toBe(4);
		expect(parseCellValue("")).toBe(3);
		expect(parseCellValue("junk")).toBe(3);
		expect(parseCellValue("-2px")).toBe(3);
	});

	test("sameLayout", () => {
		const a = computeLayout(300, 200, 3, 1, "square");
		expect(sameLayout(a, computeLayout(300, 200, 3, 1, "square"))).toBe(true);
		expect(sameLayout(a, computeLayout(301, 200, 3, 1, "square"))).toBe(false);
		expect(sameLayout(null, a)).toBe(false);
	});
});
