import { afterEach, describe, expect, test } from "bun:test";
import type { CellGrid } from "../images/grid";
import { installCanvasEnv } from "./canvas-env";
import { rasterizeText } from "./glyph";

const env = installCanvasEnv();

const grid = (over: Partial<CellGrid> = {}): CellGrid => ({
	cellDev: 2,
	cssCell: 2,
	cols: 6,
	rows: 3,
	cssWidth: 12,
	cssHeight: 6,
	...over,
});

function host(style: Record<string, string> = {}) {
	const el = document.createElement("div");
	Object.assign(el.style, {
		fontSize: "20px",
		fontFamily: "Inter",
		fontWeight: "700",
		fontStyle: "italic",
		lineHeight: "24px",
		letterSpacing: "1px",
		textAlign: "left",
		...style,
	});
	document.body.append(el);
	return el;
}

afterEach(() => {
	document.body.innerHTML = "";
});

const ctxOf = () => {
	const ctx =
		env.contexts.find((c) => c.callsOf("fillText").length > 0) ??
		env.contexts.at(-1);
	if (!ctx) throw new Error("no context");
	return ctx;
};

describe("rasterizeText", () => {
	test("sizes the scratch canvas to one pixel per cell and returns its coverage", () => {
		const g = grid();
		const raster = rasterizeText(host(), "Hi", g);
		expect(raster?.grid).toBe(g);
		expect(raster?.coverage).toHaveLength(18);
		expect(raster?.coverage.every((v) => v === 255)).toBe(true);
		expect(ctxOf().canvas.width).toBe(6);
		expect(ctxOf().canvas.height).toBe(3);
	});

	test("reads the alpha of every cell back, in row-major order", () => {
		env.coverage = (i) => (i % 2 === 0 ? 200 : 0);
		const raster = rasterizeText(host(), "Hi", grid());
		expect([...(raster?.coverage ?? [])].slice(0, 4)).toEqual([200, 0, 200, 0]);
	});

	test("scales the canvas by 1 / cssCell so glyphs land on the CSS pixel grid", () => {
		rasterizeText(host(), "Hi", grid({ cssCell: 4, cellDev: 4 }));
		expect(ctxOf().callsOf("setTransform")[0]).toEqual([
			0.25, 0, 0, 0.25, 0, 0,
		]);
		expect(ctxOf().callsOf("clearRect")[0]).toEqual([0, 0, 6, 3]);
	});

	test("takes font, size and letter spacing from the element's computed style", () => {
		rasterizeText(host(), "Hi", grid());
		expect(ctxOf().font).toContain("700");
		expect(ctxOf().font).toContain("20px");
		expect(ctxOf().font).toContain("Inter");
		expect(ctxOf().letterSpacing).toBe("1px");
		expect(ctxOf().textBaseline).toBe("alphabetic");
		expect(ctxOf().fillStyle).toBe("#000");
	});

	test("letter spacing defaults to 0px when the style has none", () => {
		rasterizeText(host({ letterSpacing: "normal" }), "Hi", grid());
		expect(ctxOf().letterSpacing).toBe("0px");
	});

	test("draws each line at its own baseline, separated by the line height", () => {
		rasterizeText(host(), "ab\ncd\nef", grid());
		const draws = ctxOf().callsOf("fillText");
		expect(draws.map((d) => d[0])).toEqual(["ab", "cd", "ef"]);
		// ascent 8 + descent 2 = 10 centred in a 24px line: baseline = (24-10)/2 + 8 = 15.
		expect(draws.map((d) => d[2])).toEqual([15, 39, 63]);
	});

	test("falls back to 0.8 / 0.2 of the font size when the browser has no font metrics", () => {
		env.metrics = {};
		rasterizeText(host(), "ab", grid());
		// 20px font: ascent 16, descent 4 -> baseline (24 - 20) / 2 + 16 = 18.
		expect(ctxOf().callsOf("fillText")[0]?.[2]).toBe(18);
	});

	test("an unparseable line height falls back to 1.2 x the font size", () => {
		env.metrics = {};
		rasterizeText(host({ lineHeight: "normal" }), "a\nb", grid());
		// line height 24 (20 * 1.2): the same baselines as an explicit 24px.
		expect(
			ctxOf()
				.callsOf("fillText")
				.map((d) => d[2]),
		).toEqual([18, 42]);
	});

	test("alignment sets the x origin: left at 0, centre at half the width, right at the full width", () => {
		const xOf = (textAlign: string) => {
			rasterizeText(host({ textAlign }), "ab", grid({ cssWidth: 100 }));
			const ctx = ctxOf();
			const x = ctx.callsOf("fillText").at(-1)?.[1];
			ctx.calls.length = 0;
			return [ctx.textAlign, x];
		};
		expect(xOf("left")).toEqual(["left", 0]);
		expect(xOf("center")).toEqual(["center", 50]);
		expect(xOf("right")).toEqual(["right", 100]);
		expect(xOf("end")).toEqual(["right", 100]);
		expect(xOf("start")).toEqual(["left", 0]);
	});

	test("text-transform: uppercase is drawn in capitals", () => {
		rasterizeText(host({ textTransform: "uppercase" }), "ab\ncd", grid());
		expect(
			ctxOf()
				.callsOf("fillText")
				.map((d) => d[0]),
		).toEqual(["AB", "CD"]);
	});

	test("returns null when the browser gives no 2D context", () => {
		env.available = false;
		expect(rasterizeText(host(), "Hi", grid())).toBeNull();
	});
});
