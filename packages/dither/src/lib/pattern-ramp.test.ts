import { describe, expect, test } from "bun:test";
import { inkedCells } from "./bayer";
import { toCSS } from "./export";
import {
	type DitherRampOptions,
	ditherRamp,
	ditherRampBands,
	ditherRampStyle,
	type RampEdge,
} from "./pattern";

const PREFIX = "data:image/svg+xml,";
const svgOf = (options: DitherRampOptions) =>
	decodeURIComponent(ditherRamp(options).slice(PREFIX.length));

/** The inked cells of a pattern path (`M x y h w v1 h-w z` runs), as "x,y" keys. */
function cellsOf(d: string): Set<string> {
	const cells = new Set<string>();
	for (const m of d.matchAll(/M(\d+) (\d+)h(\d+)v1/g)) {
		const [x, y, w] = [Number(m[1]), Number(m[2]), Number(m[3])];
		for (let i = 0; i < w; i++) cells.add(`${x + i},${y}`);
	}
	return cells;
}

/** Each drawn rect of a ramp SVG with the cells it inks (null = every cell), top to bottom. */
function layersOf(svg: string, axis: "y" | "x") {
	const patterns = new Map<string, Set<string>>();
	for (const m of svg.matchAll(
		/<pattern id="(\w+)"[^>]*><path [^>]*d="([^"]*)"/g,
	))
		patterns.set(m[1] as string, cellsOf(m[2] as string));
	const layers: { at: number; cells: Set<string> | null }[] = [];
	for (const m of svg.matchAll(
		new RegExp(`<rect ${axis}="([\\d.]+)%?"[^>]*fill="([^"]*)"`, "g"),
	)) {
		const ref = /url\(#(\w+)\)/.exec(m[2] as string);
		layers.push({
			at: Number(m[1]),
			cells: ref ? (patterns.get(ref[1] as string) ?? null) : null,
		});
	}
	return layers;
}

const base = { length: 160, from: 1, to: 1 / 16, steps: 16, cell: 3 } as const;
const EDGES: RampEdge[] = ["top", "bottom", "left", "right"];

describe("ditherRampBands", () => {
	test("bands tile the whole length with no gap or overlap, at any edge", () => {
		for (const edge of EDGES) {
			const bands = ditherRampBands({ ...base, edge })
				.slice()
				.sort((a, b) => a.lo - b.lo);
			expect(bands[0]?.lo).toBe(0);
			expect(bands.at(-1)?.hi).toBe(160);
			for (let i = 1; i < bands.length; i++) {
				expect(bands[i]?.lo).toBe(bands[i - 1]?.hi as number);
			}
		}
	});

	test("density never rises away from the edge, and the two ends are the two tones", () => {
		for (const edge of EDGES) {
			const bands = ditherRampBands({ ...base, edge });
			for (let i = 1; i < bands.length; i++) {
				expect(bands[i]?.count).toBeLessThanOrEqual(
					bands[i - 1]?.count as number,
				);
			}
			expect(bands[0]?.count).toBe(inkedCells(1, 4));
			expect(bands.at(-1)?.count).toBe(inkedCells(1 / 16, 4));
		}
	});

	test("the edge-first band sits on its own side of the box", () => {
		const at = (edge: RampEdge) => ditherRampBands({ ...base, edge })[0];
		expect(at("top")?.lo).toBe(0);
		expect(at("left")?.lo).toBe(0);
		expect(at("bottom")?.hi).toBe(160);
		expect(at("right")?.hi).toBe(160);
	});

	test("a numeric length puts every inner boundary on a whole cell row", () => {
		for (const cell of [2, 3, 4]) {
			for (const edge of EDGES) {
				for (const band of ditherRampBands({ ...base, edge, cell })) {
					for (const at of [band.lo, band.hi]) {
						if (at === 0 || at === 160) continue;
						expect(at % cell).toBe(0);
					}
				}
			}
		}
	});

	test("a string length places bands at shares of the box", () => {
		const bands = ditherRampBands({ ...base, edge: "top", length: "30%" });
		expect(bands).toHaveLength(16);
		expect(bands[0]).toMatchObject({ lo: 0, hi: 6.25 });
		expect(bands.at(-1)?.hi).toBe(100);
	});

	test("more steps than cell rows drops the empty bands instead of overlapping", () => {
		const bands = ditherRampBands({
			...base,
			edge: "top",
			length: 9,
			cell: 3,
			steps: 16,
		});
		const sorted = bands.slice().sort((a, b) => a.lo - b.lo);
		expect(sorted.length).toBeLessThanOrEqual(3);
		for (let i = 1; i < sorted.length; i++) {
			expect(sorted[i]?.lo).toBeGreaterThanOrEqual(sorted[i - 1]?.hi as number);
		}
	});
});

describe("ditherRamp", () => {
	test("is one image: one svg, one pattern per distinct level", () => {
		const svg = svgOf({ ...base, edge: "bottom" });
		expect(svg.match(/<svg /g)).toHaveLength(1);
		const levels = new Set(
			ditherRampBands({ ...base, edge: "bottom" }).map((b) => b.count),
		);
		// The solid band needs no pattern, and the rects fill with it directly.
		expect(svg.match(/<pattern /g)).toHaveLength(levels.size - 1);
		expect(svg).toContain('fill="#000"');
	});

	test("each level inks a subset of the next denser one", () => {
		for (const matrix of [
			"bayer2",
			"bayer4",
			"bayer8",
			"blue-noise",
		] as const) {
			const svg = svgOf({
				...base,
				edge: "top",
				matrix,
				steps: 12,
				from: 0.9,
				to: 0.1,
			});
			const layers = layersOf(svg, "y").filter((l) => l.cells);
			expect(layers.length).toBeGreaterThan(2);
			// Top edge: denser first.
			for (let i = 1; i < layers.length; i++) {
				const dense = layers[i - 1]?.cells as Set<string>;
				const sparse = layers[i]?.cells as Set<string>;
				for (const cell of sparse) expect(dense.has(cell)).toBe(true);
				expect(sparse.size).toBeLessThan(dense.size);
			}
		}
	});

	test("the image is as long as the ramp and one tile across", () => {
		expect(svgOf({ ...base, edge: "bottom" })).toContain(
			'width="12" height="160"',
		);
		expect(svgOf({ ...base, edge: "left" })).toContain(
			'width="160" height="12"',
		);
		expect(svgOf({ ...base, edge: "top", length: "20%" })).toContain(
			'width="12" height="100%"',
		);
	});

	test("equal options return the same cached image", () => {
		expect(ditherRamp({ ...base, edge: "top" })).toBe(
			ditherRamp({ ...base, edge: "top" }),
		);
		expect(ditherRamp({ ...base, edge: "top" })).not.toBe(
			ditherRamp({ ...base, edge: "bottom" }),
		);
	});
});

describe("ditherRampStyle", () => {
	test("repeats across the ramp and holds the edge it starts from", () => {
		const bottom = ditherRampStyle({ ...base, edge: "bottom" });
		expect(bottom.maskSize).toBe("12px 160px");
		expect(bottom.maskRepeat).toBe("repeat-x");
		expect(bottom.maskPosition).toBe("0 100%");
		const left = ditherRampStyle({ ...base, edge: "left" });
		expect(left.maskSize).toBe("160px 12px");
		expect(left.maskRepeat).toBe("repeat-y");
		expect(left.maskPosition).toBe("0 0");
		const right = ditherRampStyle({ ...base, edge: "right", length: "40%" });
		expect(right.maskSize).toBe("100% 12px");
		expect(right.maskPosition).toBe("100% 0");
	});

	test("blue noise repeats on its 16-cell tile", () => {
		expect(
			ditherRampStyle({ ...base, edge: "top", matrix: "blue-noise", cell: 2 })
				.maskSize,
		).toBe("32px 160px");
	});
});

describe("toCSS ramp", () => {
	test("writes the box length and one mask", () => {
		const css = toCSS({
			kind: "ramp",
			edge: "bottom",
			length: 120,
			from: 1,
			to: 0.1,
			cell: 2,
			selector: ".fade",
		});
		expect(css.startsWith(".fade {")).toBe(true);
		expect(css).toContain("height: 120px;");
		expect(css).toContain("background-color: var(--dither-ink);");
		expect(css.match(/\n\tmask:/g)).toHaveLength(1);
		expect(css).toContain("8px 120px repeat-x");
	});

	test("a ramp along x sizes the width", () => {
		const css = toCSS({
			kind: "ramp",
			edge: "left",
			length: "50%",
			from: 0.5,
			to: 0,
		});
		expect(css).toContain("width: 50%;");
	});
});
