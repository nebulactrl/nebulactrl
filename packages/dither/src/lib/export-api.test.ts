import { describe, expect, test } from "bun:test";
import { cappedScale, MAX_PNG_SIDE, svgDataUrl, toCSS, toSVG } from "./export";

describe("cappedScale", () => {
	test("whole numbers, at least 1", () => {
		expect(cappedScale(10, 10, 3.4)).toBe(3);
		expect(cappedScale(10, 10, 0)).toBe(1);
		expect(cappedScale(10, 10)).toBe(1);
	});

	test("reduces a scale that would exceed 16,384 px on the longest side", () => {
		expect(MAX_PNG_SIDE).toBe(16384);
		expect(cappedScale(1000, 500, 100)).toBe(16);
		expect(cappedScale(20000, 10, 4)).toBe(1);
	});
});

describe("toSVG", () => {
	const pixels = (colors: number[][]) => ({
		width: colors.length,
		height: 1,
		data: colors.flatMap(([r = 0, g = 0, b = 0]) => [r, g, b, 255]),
	});

	test("a pixel buffer becomes cell-unit paths with the common colour as background", () => {
		const svg = toSVG(
			pixels([
				[0, 0, 0],
				[0, 0, 0],
				[255, 255, 255],
			]),
			{ cell: 4 },
		);
		expect(svg).toContain('width="12"');
		expect(svg).toContain('viewBox="0 0 3 1"');
		expect(svg).toContain('<rect width="3" height="1" fill="#000000"/>');
		expect(svg).toContain('fill="#ffffff"');
	});

	test("background: false draws every colour", () => {
		const svg = toSVG(
			pixels([
				[0, 0, 0],
				[0, 0, 0],
				[255, 255, 255],
			]),
			{ background: false },
		);
		expect(svg).not.toContain("<rect");
		expect(svg.match(/<path/g)?.length).toBe(2);
	});

	test("an indexed bitmap is coloured by palette index; unknown indices stay transparent", () => {
		const svg = toSVG(
			{ width: 4, height: 1, indices: [0, 0, 1, 9] },
			{ cell: 3, palette: ["#0a0a0b", "#3d6bff", "#dfe7ff"] },
		);
		expect(svg).toContain('width="12"');
		expect(svg).toContain('fill="#3d6bff"');
		expect(svg).not.toContain("<rect");
		expect(svg).not.toContain("#dfe7ff");
	});

	test("a fully covered indexed bitmap folds the most used colour into one rect", () => {
		const svg = toSVG(
			{ width: 3, height: 1, indices: [0, 0, 1] },
			{ palette: ["#111111", "#eeeeee"] },
		);
		expect(svg).toContain('<rect width="3" height="1" fill="#111111"/>');
		expect(svg.match(/<path/g)?.length).toBe(1);
	});
});

describe("toCSS", () => {
	test("tile kind writes a repeating data-URI background at the tile size", () => {
		const css = toCSS({
			level: 0.25,
			cell: 4,
			ink: "#3b82f6",
			selector: ".hero",
		});
		expect(css.startsWith(".hero {")).toBe(true);
		expect(css).toContain('background-image: url("data:image/svg+xml,');
		expect(css).toContain("background-size: 16px 16px;");
		expect(css).toContain("background-repeat: repeat;");
		expect(css).not.toContain("background-color");
	});

	test("paper becomes the background colour; transparent does not", () => {
		expect(toCSS({ level: 0.5, paper: "#111" })).toContain(
			"background-color: #111;",
		);
		expect(toCSS({ level: 0.5, paper: "transparent" })).not.toContain(
			"background-color",
		);
	});

	test("background mode resolves a named ink to its literal colour", () => {
		expect(toCSS({ level: 0.5, ink: "blue" })).toContain("%2338c5ff");
	});

	test("mask mode paints the element and masks it, keeping CSS variables", () => {
		const css = toCSS({
			level: 0.25,
			cell: 4,
			ink: "--dither-ink",
			mode: "mask",
		});
		expect(css).toContain("background-color: var(--dither-ink);");
		expect(css).toContain("mask: url(");
		expect(css).toContain("-webkit-mask: url(");
		expect(css).toContain("0 0 / 16px 16px repeat");
		expect(css).toContain(".dither {");
	});

	test("mask mode with blue noise uses the 16-cell tile", () => {
		const css = toCSS({
			level: 0.5,
			cell: 2,
			mode: "mask",
			matrix: "blue-noise",
		});
		expect(css).toContain("0 0 / 32px 32px repeat");
	});

	test("svg image kind embeds a cover background", () => {
		const css = toCSS({
			kind: "image",
			format: "svg",
			selector: ".logo",
			image: { width: 1, height: 1, data: [0, 0, 0, 255] },
		});
		expect(css).toContain(".logo {");
		expect(css).toContain("background-size: cover;");
	});
});

describe("svgDataUrl", () => {
	test("wraps an svg in a CSS url()", () => {
		const url = svgDataUrl('<svg xmlns="http://www.w3.org/2000/svg"></svg>');
		expect(url.startsWith('url("data:image/svg+xml,')).toBe(true);
		expect(url.endsWith('")')).toBe(true);
		expect(url).not.toContain("<");
	});
});
