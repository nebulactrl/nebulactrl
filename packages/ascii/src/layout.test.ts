import { describe, expect, test } from "bun:test";
import {
	buildMask,
	cellsFor,
	computeGrid,
	contrastRatio,
	dueForFrame,
	effectiveDpr,
	LINE_RATIO,
	luminance,
	MAX_CELLS,
	maskAt,
	maskScale,
	maxQuietAlpha,
	mixRgb,
	orbPlacement,
	PANEL_MASK,
	QUIET_MASK,
	QUIET_MAX_RATIO,
	regionFromRect,
	selectRamp,
	selectRampVariants,
} from "./layout";

/** Plex Mono advances 0.6 em; the engine measures it, tests model it. */
const advance = (fontDevicePx: number) => fontDevicePx * 0.6;

describe("computeGrid", () => {
	test("1440x900 @2 gives a 16px font and 152x47 cells", () => {
		const g = computeGrid(1440, 900, 2, advance);
		expect(g.fontPx).toBe(16);
		expect(g.W).toBe(2880);
		expect(g.cw).toBe(19);
		expect(g.ch).toBe(39);
		expect(g.cols).toBe(152);
		expect(g.rows).toBe(47);
	});

	test("never exceeds the cell cap, from phone to ultrawide", () => {
		for (const [w, h, dpr] of [
			[390, 844, 3],
			[768, 1024, 2],
			[1440, 900, 1],
			[1440, 900, 2],
			[2560, 1440, 2],
			[3440, 1440, 1],
			[5120, 2880, 2],
		] as const) {
			const g = computeGrid(w, h, effectiveDpr(dpr, 2), advance);
			expect(g.cols * g.rows).toBeLessThanOrEqual(MAX_CELLS);
			expect(g.fontPx).toBeGreaterThanOrEqual(13);
		}
	});

	test("the font steps up with the viewport, not with DPR", () => {
		const small = computeGrid(1440, 900, 1, advance);
		const big = computeGrid(2560, 1440, 1, advance);
		expect(big.fontPx).toBeGreaterThan(small.fontPx);
		expect(computeGrid(1440, 900, 2, advance).fontPx).toBe(small.fontPx);
	});

	test("a wide advance still lands under the cap", () => {
		const g = computeGrid(1440, 900, 1, (px) => px * 0.9, 2000);
		expect(g.cols * g.rows).toBeLessThanOrEqual(2000);
	});

	test("cell height keeps the line ratio in whole device px", () => {
		const g = computeGrid(1440, 900, 2, advance);
		expect(g.ch).toBe(Math.round(g.fontPx * LINE_RATIO * 2));
	});
});

describe("effectiveDpr", () => {
	test("caps at 2 and floors a bad value at 1", () => {
		expect(effectiveDpr(3, 2)).toBe(2);
		expect(effectiveDpr(1.5, 2)).toBe(1.5);
		expect(effectiveDpr(0, 2)).toBe(1);
	});
});

describe("mask", () => {
	const card = regionFromRect(
		{ left: 520, top: 320, right: 920, bottom: 660 },
		1,
		PANEL_MASK,
	);
	const quiet = regionFromRect(
		{ left: 600, top: 240, right: 790, bottom: 299 },
		1,
		QUIET_MASK,
	);

	test("inside the panel the field is at the panel floor", () => {
		expect(maskAt(700, 500, [card])).toBeCloseTo(PANEL_MASK.min, 5);
	});

	test("far away the field is untouched", () => {
		expect(maskAt(1300, 100, [card, quiet])).toBe(1);
	});

	test("the feather is monotonic from the edge outwards", () => {
		let prev = 0;
		for (let d = 0; d <= PANEL_MASK.feather; d += 4) {
			const m = maskAt(920 + d, 500, [card]);
			expect(m).toBeGreaterThanOrEqual(prev);
			prev = m;
		}
		expect(maskAt(920 + PANEL_MASK.feather, 500, [card])).toBe(1);
	});

	test("the wordmark region keeps its pad dark and its feather wide", () => {
		// Within pad px of the text rect: pinned at the quiet floor.
		expect(maskAt(695, 270 - 29, [quiet])).toBeCloseTo(QUIET_MASK.min, 5);
		// pad + half the feather out: well below 1 but above the floor.
		const mid = maskAt(695, 240 - QUIET_MASK.pad - QUIET_MASK.feather / 2, [
			quiet,
		]);
		expect(mid).toBeGreaterThan(QUIET_MASK.min);
		expect(mid).toBeLessThan(0.7);
		expect(
			maskAt(695, 240 - QUIET_MASK.pad - QUIET_MASK.feather, [quiet]),
		).toBe(1);
	});

	test("regions combine by minimum and scale with DPR", () => {
		const hi = regionFromRect({ left: 10, top: 10, right: 20, bottom: 20 }, 2, {
			min: 0.5,
			feather: 8,
			pad: 4,
		});
		expect(hi).toMatchObject({ x0: 20, y1: 40, feather: 16, pad: 8 });
		expect(maskAt(500, 500, [card, quiet])).toBeCloseTo(
			Math.min(maskAt(500, 500, [card]), maskAt(500, 500, [quiet])),
			10,
		);
	});

	test("buildMask samples cell centres, row 0 at the top, as bytes", () => {
		const grid = { cols: 4, rows: 2, cw: 10, ch: 10 };
		const region = regionFromRect(
			{ left: 0, top: 0, right: 20, bottom: 10 },
			1,
			{ min: 0, feather: 10, pad: 0 },
		);
		const out = buildMask(grid, [region], new Uint8Array(8));
		expect(Array.from(out.subarray(0, 2))).toEqual([0, 0]);
		// Cells below the rect and right of it start to recover.
		expect(out[2]).toBeGreaterThan(0);
		expect(out[4]).toBeGreaterThan(0);
		expect(out[7]).toBe(255);
	});

	test("buildMask scales every cell by the base strength", () => {
		const grid = { cols: 2, rows: 1, cw: 10, ch: 10 };
		const out = buildMask(grid, [], new Uint8Array(2), 0.5);
		expect(Array.from(out)).toEqual([128, 128]);
	});
});

describe("maskScale", () => {
	test("full strength from 1100 css px, never below 0.4", () => {
		expect(maskScale(1440)).toBe(1);
		expect(maskScale(1100)).toBe(1);
		expect(maskScale(550)).toBeCloseTo(0.5, 5);
		expect(maskScale(320)).toBe(0.4);
	});

	test("scales feather and pad, not the floor", () => {
		const r = regionFromRect(
			{ left: 0, top: 0, right: 10, bottom: 10 },
			2,
			QUIET_MASK,
			0.5,
		);
		expect(r.feather).toBe(QUIET_MASK.feather * 2 * 0.5);
		expect(r.pad).toBe(QUIET_MASK.pad * 2 * 0.5);
		expect(r.min).toBe(QUIET_MASK.min);
	});
});

describe("cellsFor", () => {
	test("one cell per cellArea css px, within the floor and the cap", () => {
		expect(cellsFor(800, 300, 150)).toBe(1600);
		expect(cellsFor(10, 10, 150)).toBe(200);
		expect(cellsFor(5000, 5000, 150)).toBe(MAX_CELLS);
	});

	test("a framed canvas keeps the page's glyph size instead of packing to the cap", () => {
		const g = computeGrid(800, 300, 2, advance, cellsFor(800, 300, 150));
		expect(g.fontPx).toBeLessThanOrEqual(15);
		expect(g.cols * g.rows).toBeLessThanOrEqual(1600);
	});
});

describe("orbPlacement", () => {
	const cardFor = (W: number, H: number, dpr: number) => {
		const w = 400 * dpr;
		const h = 340 * dpr;
		return {
			left: (W - w) / 2,
			right: (W + w) / 2,
			top: (H - h) / 2 + 40 * dpr,
			bottom: (H + h) / 2 + 40 * dpr,
		};
	};

	test("desktop: the card covers at most the left third of the orb", () => {
		for (const [W, H, dpr] of [
			[1440, 900, 1],
			[2880, 1800, 2],
			[2560, 1440, 1],
			[5120, 2880, 2],
		] as const) {
			const card = cardFor(W, H, dpr);
			const orb = orbPlacement(W, H, dpr, card);
			const covered = Math.max(0, card.right - (orb.cx - orb.r)) / (2 * orb.r);
			expect(covered).toBeLessThanOrEqual(1 / 3);
			// right of centre, at full strength
			expect(orb.cx).toBeGreaterThan(W / 2);
			expect(orb.r).toBeCloseTo(Math.min(W, H) * 0.37, 5);
			expect(orb.dim).toBe(1);
		}
	});

	test("a landscape frame under 720 px keeps the wide placement when narrowBelow is 0", () => {
		const [W, H, dpr] = [560 * 2, 350 * 2, 2] as const;
		const card = {
			left: 24 * dpr,
			top: 60 * dpr,
			right: 300 * dpr,
			bottom: 290 * dpr,
		};
		expect(orbPlacement(W, H, dpr, card).dim).toBeLessThan(1);
		const orb = orbPlacement(W, H, dpr, card, 0);
		expect(orb.dim).toBe(1);
		expect(orb.cx).toBeGreaterThan(W / 2);
		const covered = Math.max(0, card.right - (orb.cx - orb.r)) / (2 * orb.r);
		expect(covered).toBeLessThanOrEqual(1 / 3);
	});

	test("phone: the orb is centred behind the card at a lower strength", () => {
		const [W, H, dpr] = [390 * 3, 844 * 3, 3] as const;
		const card = cardFor(W, H, dpr);
		const orb = orbPlacement(W, H, dpr, card);
		expect(orb.cx).toBeCloseTo(W / 2, 5);
		expect(orb.cy).toBeCloseTo((card.top + card.bottom) / 2, 5);
		expect(orb.dim).toBeLessThan(1);
		// no cropped fragment: the whole orb stays on screen
		expect(orb.cx - orb.r).toBeGreaterThanOrEqual(0);
		expect(orb.cx + orb.r).toBeLessThanOrEqual(W);
		expect(orb.cy - orb.r).toBeGreaterThanOrEqual(0);
		expect(orb.cy + orb.r).toBeLessThanOrEqual(H);
	});

	test("no card falls back to the wide placement", () => {
		const wide = orbPlacement(1440, 900, 1, null);
		expect(wide).toEqual({
			cx: 1440 * 0.77,
			cy: 900 * 0.45,
			r: 900 * 0.37,
			dim: 1,
		});
		expect(orbPlacement(390, 844, 1, null).cx).toBeCloseTo(390 * 0.77, 5);
	});
});

describe("selectRamp", () => {
	const cov: [string, number][] = [
		[".", 0.02],
		[":", 0.05],
		["o", 0.1],
		["%", 0.16],
		["#", 0.22],
		["@", 0.3],
	];

	test("starts blank and climbs in ink order", () => {
		const ramp = selectRamp(cov, 4);
		expect(ramp[0]).toBe(" ");
		expect(ramp).toHaveLength(4);
		const ink = ramp.map((g) => cov.find((c) => c[0] === g)?.[1] ?? 0);
		expect([...ink].sort((a, b) => a - b)).toEqual(ink);
		expect(ramp.at(-1)).toBe("@");
	});

	test("never repeats a glyph, even when levels outnumber candidates", () => {
		const ramp = selectRamp(cov, 12);
		expect(new Set(ramp).size).toBe(ramp.length);
		expect(ramp.length).toBe(cov.length + 1);
	});
});

describe("dueForFrame", () => {
	test("gates to the interval with 1.5 ms of vsync slack", () => {
		const interval = 1000 / 24;
		expect(dueForFrame(16.7, 0, interval)).toBe(false);
		expect(dueForFrame(33.4, 0, interval)).toBe(false);
		expect(dueForFrame(40, 0, interval)).toBe(false);
		expect(dueForFrame(50, 0, interval)).toBe(true);
		// 60 Hz vsyncs land on every third frame: 20 fps.
		let last = 0;
		let drawn = 0;
		for (let t = 16.67; t <= 1000; t += 16.67) {
			if (dueForFrame(t, last, interval)) {
				last = t;
				drawn++;
			}
		}
		expect(drawn).toBeGreaterThanOrEqual(19);
		expect(drawn).toBeLessThanOrEqual(20);
	});
});

describe("selectRampVariants", () => {
	const coverage: [string, number][] = [
		[".", 0.05],
		["'", 0.06],
		[":", 0.1],
		[";", 0.12],
		["-", 0.14],
		["~", 0.16],
		["+", 0.2],
		["=", 0.22],
		["*", 0.3],
		["#", 0.34],
		["@", 0.4],
		["%", 0.38],
	];

	test("one variant is selectRamp", () => {
		expect(selectRampVariants(coverage, 6, 1)).toEqual(selectRamp(coverage, 6));
	});

	test("is level-major: blank variants first, then groups climbing by ink", () => {
		const ramp = selectRampVariants(coverage, 4, 3);
		expect(ramp).toHaveLength(12);
		expect(ramp.slice(0, 3)).toEqual([" ", " ", " "]);
		const ink = (g: string) => coverage.find(([c]) => c === g)?.[1] ?? 0;
		const mean = (level: number) =>
			ramp.slice(level * 3, level * 3 + 3).reduce((s, g) => s + ink(g), 0) / 3;
		expect(mean(1)).toBeLessThan(mean(2));
		expect(mean(2)).toBeLessThan(mean(3));
	});

	test("no glyph is used twice while the pool lasts", () => {
		const ramp = selectRampVariants(coverage, 4, 3).slice(3);
		expect(new Set(ramp).size).toBe(ramp.length);
	});

	test("a pool too small for another level ends the ramp early, in whole levels", () => {
		const ramp = selectRampVariants(coverage.slice(0, 5), 8, 2);
		expect(ramp.length % 2).toBe(0);
		expect(ramp.length).toBeLessThan(16);
	});

	test("a level the pool only half fills repeats its closest glyph", () => {
		const ramp = selectRampVariants(coverage.slice(0, 3), 3, 2);
		expect(ramp).toHaveLength(6);
		expect(ramp.slice(2).every((g) => g !== " ")).toBe(true);
	});
});

describe("contrast", () => {
	test("luminance and ratio match WCAG at the extremes", () => {
		expect(luminance([255, 255, 255])).toBeCloseTo(1, 6);
		expect(luminance([0, 0, 0])).toBe(0);
		expect(contrastRatio([255, 255, 255], [0, 0, 0])).toBeCloseTo(21, 6);
		expect(contrastRatio([9, 9, 9], [9, 9, 9])).toBe(1);
	});

	test("the ratio is symmetric", () => {
		expect(contrastRatio([10, 20, 30], [200, 210, 220])).toBeCloseTo(
			contrastRatio([200, 210, 220], [10, 20, 30]),
			9,
		);
	});

	test("maxQuietAlpha lands on the limit and not past it", () => {
		const ground: [number, number, number] = [6, 6, 17];
		const ink: [number, number, number] = [180, 187, 200];
		const a = maxQuietAlpha(ink, ground);
		expect(a).toBeGreaterThan(0.02);
		expect(a).toBeLessThan(0.15);
		expect(contrastRatio(mixRgb(ground, ink, a), ground)).toBeLessThanOrEqual(
			QUIET_MAX_RATIO,
		);
		expect(
			contrastRatio(mixRgb(ground, ink, a + 0.01), ground),
		).toBeGreaterThan(QUIET_MAX_RATIO);
	});

	test("a dimmer ink tolerates more opacity", () => {
		const ground: [number, number, number] = [6, 6, 17];
		expect(maxQuietAlpha([58, 64, 76], ground)).toBeGreaterThan(
			maxQuietAlpha([180, 187, 200], ground),
		);
	});
});
