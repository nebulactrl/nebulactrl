import { describe, expect, test } from "bun:test";
import {
	BAR_THICKNESS,
	cometAngle,
	cometTail,
	DOT_CENTRES,
	LOADER_SHAPES,
	LOADER_STILL,
	loaderCell,
	loaderGrid,
	loaderHeight,
	loaderTone,
	RING_RADIUS,
	TRACK_TONE,
} from "./loader-math";

describe("sizes", () => {
	test("named sizes are 24, 40 and 64 px; a number is a height", () => {
		expect(loaderHeight("sm")).toBe(24);
		expect(loaderHeight("md")).toBe(40);
		expect(loaderHeight("lg")).toBe(64);
		expect(loaderHeight(18)).toBe(18);
	});

	test("cells are 1 px at sm, 2 above, unless set", () => {
		expect(loaderCell("sm", undefined)).toBe(1);
		expect(loaderCell("md", undefined)).toBe(2);
		expect(loaderCell(16, undefined)).toBe(1);
		expect(loaderCell("sm", 3)).toBe(3);
		expect(loaderCell("md", 0)).toBe(1);
	});

	test("dots are twice as wide as tall and the bar three times", () => {
		expect(loaderGrid("ring", 40, 2)).toEqual({ cols: 20, rows: 20 });
		expect(loaderGrid("pulse", 24, 1)).toEqual({ cols: 24, rows: 24 });
		expect(loaderGrid("dots", 40, 2)).toEqual({ cols: 40, rows: 20 });
		expect(loaderGrid("bar", 24, 1)).toEqual({ cols: 72, rows: 24 });
	});
});

describe("ring", () => {
	test("is hollow: nothing in the middle or the corners", () => {
		expect(loaderTone("ring", 0.5, 0.5, 0.3)).toBeLessThan(0);
		expect(loaderTone("ring", 0.02, 0.02, 0.3)).toBeLessThan(0);
	});

	test("the comet's head is solid and its tail thins out behind it", () => {
		const t = 0.4;
		const head = cometAngle(t);
		const at = (angle: number) =>
			loaderTone(
				"ring",
				0.5 + RING_RADIUS * Math.cos(angle),
				0.5 + RING_RADIUS * Math.sin(angle),
				t,
			);
		expect(at(head - 0.01)).toBe(1);
		const tail = cometTail(t);
		expect(at(head - tail * 0.5)).toBeLessThan(at(head - 0.25));
		expect(at(head - tail * 0.5)).toBeGreaterThan(TRACK_TONE);
		expect(at(head - tail - 0.3)).toBe(TRACK_TONE);
	});
});

describe("dots", () => {
	test("three discs with gaps between them", () => {
		for (const cx of DOT_CENTRES) {
			expect(loaderTone("dots", cx, 0.5, 0)).toBeGreaterThanOrEqual(0);
		}
		expect(loaderTone("dots", 2 / 3 + 0.0, 0.5, 0)).toBeLessThan(0);
		expect(loaderTone("dots", 1, 0.02, 0)).toBeLessThan(0);
	});

	test("each disc builds up and dissolves again", () => {
		const tones = Array.from({ length: 40 }, (_, i) =>
			loaderTone("dots", DOT_CENTRES[1] ?? 1, 0.5, i * 0.03),
		);
		expect(Math.max(...tones)).toBeGreaterThan(0.9);
		expect(Math.min(...tones)).toBeLessThan(0.3);
	});
});

describe("pulse", () => {
	test("a dot in the middle with rippling rings around it", () => {
		expect(loaderTone("pulse", 0.5, 0.5, 0.2)).toBe(1);
		expect(loaderTone("pulse", 0.02, 0.02, 0.2)).toBeLessThan(0);
		const seen = new Set<boolean>();
		for (let x = 0; x < 100; x++) {
			seen.add(loaderTone("pulse", 0.5 + x / 250, 0.5, 0.2) >= 0);
		}
		expect(seen.has(false)).toBe(true);
	});
});

describe("bar", () => {
	test("a faint track of BAR_THICKNESS, none above or below", () => {
		expect(loaderTone("bar", 1.5, 0.5 + BAR_THICKNESS, 0)).toBeLessThan(0);
		expect(loaderTone("bar", 1.5, 0.5, 0)).toBeGreaterThanOrEqual(TRACK_TONE);
	});

	test("the head is solid with a tail thinning behind it", () => {
		// Find the head at some moment by scanning the length of the bar.
		const t = 0.9;
		const scan = Array.from({ length: 300 }, (_, i) =>
			loaderTone("bar", (i / 300) * 3, 0.5, t),
		);
		expect(Math.max(...scan)).toBe(1);
		expect(Math.min(...scan)).toBe(TRACK_TONE);
	});
});

describe("every shape", () => {
	test("tones stay in [-1, 1] and the still frame is not empty", () => {
		for (const shape of LOADER_SHAPES) {
			const { cols, rows } = loaderGrid(shape, 40, 2);
			let inked = 0;
			for (let y = 0; y < rows; y++) {
				for (let x = 0; x < cols; x++) {
					const tone = loaderTone(
						shape,
						(x + 0.5) / rows,
						(y + 0.5) / rows,
						LOADER_STILL[shape],
					);
					expect(tone).toBeLessThanOrEqual(1);
					expect(tone).toBeGreaterThanOrEqual(-1);
					if (tone > 0.5) inked++;
				}
			}
			expect(inked).toBeGreaterThan(0);
		}
	});
});
