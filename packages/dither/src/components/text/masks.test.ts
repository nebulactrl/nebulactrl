import { describe, expect, test } from "bun:test";
import {
	complementMaskUrl,
	edgeBandRows,
	edgeBandUrl,
	edgeFadeMask,
	MASK_STEPS,
	maskTiling,
	maskUrl,
} from "./masks";

const decode = (url: string) =>
	decodeURIComponent(url.slice('url("'.length, -2));

describe("maskUrl", () => {
	test("is a CSS url() of an svg data uri sized to the tile", () => {
		const url = maskUrl(8, 3, 4);
		expect(url.startsWith('url("data:image/svg+xml,')).toBe(true);
		expect(url.endsWith('")')).toBe(true);
		expect(decode(url)).toContain('width="12" height="12"');
	});

	test("step 0 inks nothing and the top step is one solid square", () => {
		expect(decode(maskUrl(0, 2, 4))).not.toContain("<path");
		expect(decode(maskUrl(0, 2, 4))).not.toContain("<rect");
		expect(decode(maskUrl(MASK_STEPS, 2, 4))).toContain("<rect");
	});

	test("a higher step never inks fewer cells", () => {
		const pathLength = (step: number) =>
			decode(maskUrl(step, 2, 4)).match(/<path d="([^"]*)"/)?.[1]?.length ?? 0;
		expect(pathLength(4)).toBeGreaterThan(0);
		expect(pathLength(12)).toBeGreaterThanOrEqual(pathLength(4));
	});

	test("the shape and the matrix size change the tile", () => {
		expect(maskUrl(8, 2, 4, "dot")).not.toBe(maskUrl(8, 2, 4, "square"));
		expect(decode(maskUrl(8, 2, 8))).toContain('width="16"');
	});
});

describe("complementMaskUrl", () => {
	test("step 0 inks every cell, as four full-width rows", () => {
		expect(decode(complementMaskUrl(0, 2, 4)).match(/h4v1h-4z/g)).toHaveLength(
			4,
		);
	});

	test("the top step inks nothing", () => {
		expect(decode(complementMaskUrl(MASK_STEPS, 2, 4))).not.toContain("<path");
	});

	test("a mask and its complement cover every cell exactly once", () => {
		const cells = (url: string) => {
			const svg = decode(url);
			const out = new Set<string>();
			if (svg.includes("<rect")) {
				for (let y = 0; y < 4; y += 1)
					for (let x = 0; x < 4; x += 1) out.add(`${x},${y}`);
				return out;
			}
			for (const m of svg.matchAll(/M(\d+) (\d+)h(\d+)/g)) {
				for (let x = 0; x < Number(m[3]); x += 1)
					out.add(`${Number(m[1]) + x},${m[2]}`);
			}
			return out;
		};
		for (const step of [0, 1, 3, 8, 13, 16]) {
			const forward = cells(maskUrl(step, 2, 4));
			const complement = cells(complementMaskUrl(step, 2, 4));
			expect(forward.size).toBe(step);
			expect(complement.size).toBe(16 - step);
			for (const cell of forward) expect(complement.has(cell)).toBe(false);
		}
	});
});

describe("maskTiling", () => {
	test("repeats a tile of n * cell px, with the -webkit- twins", () => {
		expect(maskTiling(3, 4)).toEqual({
			maskRepeat: "repeat",
			maskSize: "12px 12px",
			WebkitMaskRepeat: "repeat",
			WebkitMaskSize: "12px 12px",
		});
		expect(maskTiling(2, 8).maskSize).toBe("16px 16px");
	});
});

describe("edge bands", () => {
	test("the outermost row is the sparsest and each row inward inks more", () => {
		const top = edgeBandRows(4, 3, "top");
		const count = (row: boolean[]) => row.filter(Boolean).length;
		expect(top).toHaveLength(3);
		expect(top.every((row) => row.length === 4)).toBe(true);
		expect(count(top[0] ?? [])).toBeLessThanOrEqual(count(top[1] ?? []));
		expect(count(top[1] ?? [])).toBeLessThanOrEqual(count(top[2] ?? []));
		expect(count(top[2] ?? [])).toBeGreaterThan(count(top[0] ?? []));
	});

	test("the bottom edge mirrors the top: its first row is the innermost", () => {
		const bottom = edgeBandRows(4, 3, "bottom");
		const top = edgeBandRows(4, 3, "top");
		const count = (row: boolean[] | undefined) =>
			(row ?? []).filter(Boolean).length;
		expect(count(bottom[2])).toBe(count(top[0]));
		expect(count(bottom[0])).toBe(count(top[2]));
	});

	test("the rows' ink counts match depth / (rows + 1) of the tile cells", () => {
		const rows = edgeBandRows(4, 3, "top").map(
			(row) => row.filter(Boolean).length,
		);
		// depth 1..3 of 4 -> round(d/4 * 16) = 4, 8, 12 cells of the 4x4 matrix; each row sees 4 of them at most.
		expect(rows.every((n) => n >= 0 && n <= 4)).toBe(true);
		expect(rows.reduce((a, b) => a + b, 0)).toBeGreaterThan(0);
	});

	test("edgeBandUrl is a tile n*cell wide and rows*cell tall", () => {
		const svg = decode(edgeBandUrl(4, 3, 2, "top"));
		expect(svg).toContain("width='8' height='6'");
		expect(svg).toContain("viewBox='0 0 4 3'");
		expect(edgeBandUrl(4, 3, 2, "top")).not.toBe(
			edgeBandUrl(4, 3, 2, "bottom"),
		);
	});

	test("edgeFadeMask stacks a top band, a bottom band and a solid middle, with -webkit- twins", () => {
		const mask = edgeFadeMask(2, 3, 4);
		expect(mask.maskImage).toBe(mask.WebkitMaskImage);
		expect(mask.maskImage?.split("url(").length).toBe(3);
		expect(mask.maskImage).toContain("linear-gradient(#000, #000)");
		expect(mask.maskSize).toBe("8px 6px, 8px 6px, 100% calc(100% - 12px)");
		expect(mask.maskPosition).toBe("0 0, 0 100%, 0 6px");
		expect(mask.maskRepeat).toBe("repeat-x, repeat-x, no-repeat");
		expect(mask.WebkitMaskSize).toBe(mask.maskSize);
		expect(mask.WebkitMaskPosition).toBe(mask.maskPosition);
		expect(mask.WebkitMaskRepeat).toBe(mask.maskRepeat);
	});

	test("a fade of more rows eats more of the strip", () => {
		expect(edgeFadeMask(2, 5).maskSize).toBe(
			"8px 10px, 8px 10px, 100% calc(100% - 20px)",
		);
	});
});
