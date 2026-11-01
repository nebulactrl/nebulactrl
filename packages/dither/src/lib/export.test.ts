import { describe, expect, test } from "bun:test";
import { exportCSS, exportSVG, rgbHex, svgToDataUri, tileSvg } from "./export";

/** A 4×2 RGBA bitmap: top row ink, bottom row paper, one transparent corner. */
function fixture() {
	const ink = [255, 255, 255, 255];
	const paper = [0, 0, 0, 255];
	const clear = [0, 0, 0, 0];
	return {
		width: 4,
		height: 2,
		data: [
			...ink,
			...ink,
			...paper,
			...ink,
			...paper,
			...paper,
			...paper,
			...clear,
		],
	};
}

describe("rgbHex", () => {
	test("pads and rounds", () => {
		expect(rgbHex(0, 15, 255)).toBe("#000fff");
		expect(rgbHex(1.4, 2.6, 0)).toBe("#010300");
	});
});

describe("svgToDataUri", () => {
	test("has the data: prefix, no raw angle brackets, hashes or double quotes", () => {
		const uri = svgToDataUri(
			'<svg xmlns="http://www.w3.org/2000/svg"><path fill="#fff" d="M0 0h1"/></svg>',
		);
		expect(uri.startsWith("data:image/svg+xml,")).toBe(true);
		const body = uri.slice("data:image/svg+xml,".length);
		expect(body).not.toMatch(/[<>"#]/);
		expect(decodeURIComponent(body)).toBe(
			"<svg xmlns='http://www.w3.org/2000/svg'><path fill='#fff' d='M0 0h1'/></svg>",
		);
	});

	test("collapses whitespace between tags", () => {
		const body = svgToDataUri("<svg>\n  <g>\n  </g>\n</svg>");
		expect(decodeURIComponent(body.slice("data:image/svg+xml,".length))).toBe(
			"<svg><g></g></svg>",
		);
	});
});

describe("exportSVG: bitmap", () => {
	test("emits a sized svg root with a viewBox in bitmap pixels", () => {
		const svg = exportSVG({ kind: "bitmap", bitmap: fixture() }, { cell: 3 });
		expect(
			svg.startsWith(
				'<svg xmlns="http://www.w3.org/2000/svg" width="12" height="6" viewBox="0 0 4 2"',
			),
		).toBe(true);
		expect(svg.endsWith("</svg>")).toBe(true);
		expect(svg).toContain('aria-hidden="true"');
	});

	test("merges horizontal runs into one path per colour", () => {
		const svg = exportSVG(
			{ kind: "bitmap", bitmap: fixture() },
			{ background: null },
		);
		// Ink: run of 2 at x=0 and a single at x=3 on row 0.
		expect(svg).toContain(
			'<path fill="#ffffff" d="M0 0h2v1h-2zM3 0h1v1h-1z"/>',
		);
		// Paper: (2,0), then (0..2, 1); the transparent corner is skipped.
		expect(svg).toContain(
			'<path fill="#000000" d="M2 0h1v1h-1zM0 1h3v1h-3z"/>',
		);
	});

	test("background auto paints the dominant colour as a rect only when fully opaque", () => {
		const svg = exportSVG({ kind: "bitmap", bitmap: fixture() });
		// A transparent pixel exists, so no automatic background.
		expect(svg).not.toContain("<rect");
		const opaque = {
			width: 3,
			height: 1,
			data: [9, 9, 9, 255, 9, 9, 9, 255, 200, 0, 0, 255],
		};
		const compact = exportSVG({ kind: "bitmap", bitmap: opaque });
		expect(compact).toContain('<rect width="3" height="1" fill="#090909"/>');
		expect(compact).toContain('<path fill="#c80000" d="M2 0h1v1h-1z"/>');
		expect(compact).not.toContain('fill="#090909" d=');
	});

	test("partial alpha becomes fill-opacity; title adds role img", () => {
		const svg = exportSVG(
			{
				kind: "bitmap",
				bitmap: { width: 1, height: 1, data: [255, 0, 0, 128] },
			},
			{ title: "A <dot>" },
		);
		expect(svg).toContain('fill-opacity="0.502"');
		expect(svg).toContain('role="img"');
		expect(svg).toContain("<title>A &#60;dot&#62;</title>");
	});
});

describe("exportSVG: mask and pattern", () => {
	test("mask draws a single ink path", () => {
		const svg = exportSVG({
			kind: "mask",
			color: "#6d6bff",
			mask: { width: 3, height: 2, ink: [1, 0, 1, 0, 1, 1] },
		});
		expect(svg).toContain('viewBox="0 0 3 2"');
		expect(svg).toContain(
			'<path fill="#6d6bff" d="M0 0h1v1h-1zM2 0h1v1h-1zM1 1h2v1h-2z"/>',
		);
	});

	test("pattern wraps a <pattern> and fills a rect with it", () => {
		const svg = exportSVG(
			{
				kind: "pattern",
				fill: { tone: 0.5, color: "#fff", cell: 2 },
				width: 40,
				height: 20,
			},
			{ background: "#000" },
		);
		expect(svg).toContain('width="40" height="20" viewBox="0 0 40 20"');
		expect(svg).toMatch(
			/<defs><pattern id="nc-dither-[a-z0-9]+" width="8" height="8"/,
		);
		expect(svg).toContain('<rect width="40" height="20" fill="#000"/>');
		expect(svg).toMatch(
			/<rect width="40" height="20" fill="url\(#nc-dither-[a-z0-9]+\)"\/>/,
		);
	});
});

describe("CSS snippet", () => {
	test("tile rule has a data-uri background-image and the tile size", () => {
		const css = exportCSS(
			{ kind: "tile", fill: { tone: 0.25, color: "#9aa4ff", cell: 3 } },
			{ selector: ".hero", background: "#1a1d20" },
		);
		expect(css.startsWith(".hero {\n")).toBe(true);
		expect(css).toContain("\tbackground-color: #1a1d20;");
		expect(css).toMatch(
			/\tbackground-image: url\("data:image\/svg\+xml,[^"]+"\);/,
		);
		expect(css).toContain("\tbackground-size: 12px 12px;");
		expect(css).toContain("\tbackground-repeat: repeat;");
		expect(css).toContain("\timage-rendering: pixelated;");
		expect(css.endsWith("}")).toBe(true);
	});

	test("svg rule covers the box by default and can use natural size", () => {
		const svg = exportSVG({
			kind: "mask",
			color: "#fff",
			mask: { width: 2, height: 2, ink: [1, 0, 0, 1] },
		});
		const cover = exportCSS({ kind: "svg", svg, width: 2, height: 2 });
		expect(cover.startsWith(".dither {")).toBe(true);
		expect(cover).toContain("background-size: cover;");
		const natural = exportCSS(
			{ kind: "svg", svg, width: 2, height: 2 },
			{ size: "natural" },
		);
		expect(natural).toContain("background-size: 2px 2px;");
	});

	test("tile svg is n × cell px and inks tone × n² cells", () => {
		const svg = tileSvg({ tone: 0.5, color: "#fff", cell: 2, n: 4 });
		expect(svg).toContain('width="8" height="8" viewBox="0 0 4 4"');
		expect(svg).toContain("<path");
		expect(tileSvg({ tone: 0, color: "#fff" })).not.toContain("<path");
	});
});
