import { describe, expect, test } from "bun:test";
import {
	ditherFillId,
	ditherGradientStops,
	ditherMaskDataUri,
	ditherPatternSvg,
	ditherTilePath,
} from "./pattern";

describe("ditherTilePath", () => {
	test("is empty at 0 and covers the whole tile at n²", () => {
		expect(ditherTilePath(0, 4)).toBe("");
		const full = ditherTilePath(16, 4);
		// Four rows, each one merged rectangle of width 4.
		expect(full.split("M").length - 1).toBe(4);
		expect(full).toContain("h4v1h-4z");
	});

	test("is cached", () => {
		expect(ditherTilePath(5, 4)).toBe(ditherTilePath(5, 4));
	});

	test("dots draw one circle per inked cell", () => {
		const path = ditherTilePath(3, 4, "dot");
		expect(path.split("M").length - 1).toBe(3);
	});
});

describe("ditherFillId", () => {
	test("is stable and snaps tone to the matrix steps", () => {
		const a = { tone: 0.5, color: "var(--dither-ink)" };
		expect(ditherFillId(a)).toBe(ditherFillId({ ...a }));
		// 0.5 and 0.52 both ink 8 of 16 cells.
		expect(ditherFillId({ ...a, tone: 0.52 })).toBe(ditherFillId(a));
		expect(ditherFillId({ ...a, tone: 0.7 })).not.toBe(ditherFillId(a));
		expect(ditherFillId({ ...a, color: "red" })).not.toBe(ditherFillId(a));
		expect(ditherFillId(a)).toMatch(/^nc-dither-[0-9a-z]+$/);
	});
});

describe("ditherPatternSvg", () => {
	test("is a pattern of n × cell px with the colour in a style", () => {
		const svg = ditherPatternSvg({
			tone: 0.5,
			color: "var(--dither-ink)",
			cell: 3,
		});
		expect(svg).toContain('width="12"');
		expect(svg).toContain('height="12"');
		expect(svg).toContain('patternUnits="userSpaceOnUse"');
		expect(svg).toContain('style="fill:var(--dither-ink)"');
		expect(svg).toContain('transform="scale(3)"');
	});

	test("tone 1 is a solid rect, tone 0 has no ink", () => {
		expect(ditherPatternSvg({ tone: 1, color: "red" })).toContain("<rect");
		const none = ditherPatternSvg({ tone: 0, color: "red" });
		expect(none).not.toContain("<path");
		expect(none).not.toContain("<rect");
	});

	test("escapes hostile colour strings", () => {
		const svg = ditherPatternSvg({ tone: 0.5, color: 'red"/><script>' });
		expect(svg).not.toContain("<script>");
	});
});

describe("ditherMaskDataUri", () => {
	test("is an svg data uri with the intrinsic tile size", () => {
		const uri = ditherMaskDataUri({ tone: 0.25, cell: 2 });
		expect(uri.startsWith("data:image/svg+xml,")).toBe(true);
		const svg = decodeURIComponent(uri.slice("data:image/svg+xml,".length));
		expect(svg).toContain('width="8"');
		expect(svg).toContain('viewBox="0 0 4 4"');
	});

	test("8×8 tiles are 8 cells wide", () => {
		const svg = decodeURIComponent(
			ditherMaskDataUri({ tone: 0.5, cell: 3, n: 8 }).slice(
				"data:image/svg+xml,".length,
			),
		);
		expect(svg).toContain('width="24"');
	});
});

describe("ditherGradientStops", () => {
	test("runs evenly from one tone to the other", () => {
		expect(ditherGradientStops(0.8, 0.2, 4).map((t) => +t.toFixed(2))).toEqual([
			0.8, 0.6, 0.4, 0.2,
		]);
		expect(ditherGradientStops(0.5, 0, 1)).toEqual([0.5]);
	});
});
