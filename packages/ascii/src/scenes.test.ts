import { describe, expect, test } from "bun:test";
import {
	contrastRatio,
	maxQuietAlpha,
	mixRgb,
	orbPlacement,
	PANEL_MASK,
	QUIET_MASK,
	QUIET_MAX_RATIO,
	type RGB,
} from "./layout";
import { NEBULA_PALETTE } from "./palette";
import {
	atlasColumns,
	BAND_LEAN,
	bandHalfWidth,
	DIAL_OUT_DEPTH,
	GAP_DRIFT,
	MAX_FPS,
	quietClearance,
	resolveScene,
	SCENE_NAMES,
	SCENES,
	sceneFps,
	scenePlacement,
} from "./scenes";

/** oklch(0.13 0.025 285): the site's void, in sRGB. */
const SITE_VOID: RGB = [6, 6, 17];

/** Every scene but the Eclipse. */
const OTHER_SCENES = ["dial-out", "lost-signal", "dome", "band"] as const;

describe("resolveScene", () => {
	test("every name resolves to its own spec", () => {
		for (const name of SCENE_NAMES) expect(resolveScene(name).name).toBe(name);
	});

	test("no name, or an unknown one, is the Eclipse", () => {
		expect(resolveScene(undefined).name).toBe("eclipse");
		expect(resolveScene("sunrise").name).toBe("eclipse");
		expect(resolveScene("toString").name).toBe("eclipse");
	});

	test("the Eclipse spec is pinned: 30 fps, the old panel and quiet masks, no glyphs", () => {
		const e = SCENES.eclipse;
		expect(e.fps).toBe(30);
		expect(e.panel).toEqual(PANEL_MASK);
		expect(e.quiet).toEqual(QUIET_MASK);
		expect(e.staticTime).toBe(14);
		expect(e.candidates).toBe("");
		expect(e.extras).toBe("");
		expect(e.levels).toBe(1);
	});
});

describe("sceneFps", () => {
	test("defaults to the scene's own rate", () => {
		expect(sceneFps(SCENES["dial-out"])).toBe(20);
		expect(sceneFps(SCENES["lost-signal"])).toBe(12);
		expect(sceneFps(SCENES.dome)).toBe(5);
		expect(sceneFps(SCENES.band)).toBe(10);
	});

	test("never above the scene's ceiling (24, the Eclipse 30) and never under 1", () => {
		expect(sceneFps(SCENES.dome, 60)).toBe(MAX_FPS);
		expect(sceneFps(SCENES.eclipse, 60)).toBe(30);
		expect(sceneFps(SCENES.eclipse)).toBe(30);
		expect(sceneFps(SCENES.eclipse, 0)).toBe(1);
		expect(sceneFps(SCENES.eclipse, -5)).toBe(1);
	});

	test("a caller may ask for less", () => {
		expect(sceneFps(SCENES["dial-out"], 8)).toBe(8);
	});

	test("a non-number falls back to the scene's rate", () => {
		expect(sceneFps(SCENES["lost-signal"], Number.NaN)).toBe(12);
	});
});

describe("scene specs", () => {
	test("glyph scene rates sit under the cap", () => {
		for (const name of OTHER_SCENES) {
			expect(SCENES[name].fps).toBeLessThanOrEqual(MAX_FPS);
		}
	});

	test("the other scenes' rates divide a 60 Hz display evenly, so they hit them", () => {
		for (const name of OTHER_SCENES) {
			expect(60 % SCENES[name].fps).toBe(0);
		}
	});

	test("the other scenes spell nothing: symbols and punctuation only", () => {
		for (const name of OTHER_SCENES) {
			const spec = SCENES[name];
			expect(spec.candidates + spec.extras).not.toMatch(/[A-Za-z0-9]/);
		}
	});

	test("candidates are enough to fill every level, and unique", () => {
		for (const name of OTHER_SCENES) {
			const spec = SCENES[name];
			const pool = Array.from(new Set(spec.candidates));
			expect(pool.length).toBe(spec.candidates.length);
			expect(pool.length).toBeGreaterThanOrEqual(
				(spec.levels - 1) * spec.variants,
			);
		}
	});

	test("the atlas holds the ramp then the extras", () => {
		expect(atlasColumns(SCENES.dome)).toBe(6 + 1);
		expect(atlasColumns(SCENES.band)).toBe(7 + 1);
		expect(atlasColumns(SCENES["lost-signal"])).toBe(7 * 3);
	});

	test("every extra fits the 7-bit glyph select the field writes", () => {
		for (const spec of Object.values(SCENES)) {
			expect(spec.extras.length).toBeLessThan(128);
			expect(spec.variants).toBeLessThan(128);
		}
	});

	test("alpha scenes hold text-safe contrast in their quiet region", () => {
		const { ink, accentInk } = NEBULA_PALETTE;
		// The brightest glyph any hue can draw: slate ink, or ink mixed to accent ink.
		for (const bright of [ink, mixRgb(ink, accentInk, 0.7), accentInk]) {
			const cap = maxQuietAlpha(bright, SITE_VOID);
			for (const spec of Object.values(SCENES)) {
				if (spec.maskMode !== "alpha") continue;
				expect(spec.quiet.min).toBeLessThanOrEqual(cap);
				const worst = mixRgb(SITE_VOID, bright, spec.quiet.min);
				expect(contrastRatio(worst, SITE_VOID)).toBeLessThanOrEqual(
					QUIET_MAX_RATIO,
				);
			}
		}
	});
});

describe("scenePlacement: eclipse", () => {
	test("is the orb placement, untouched", () => {
		const rect = { left: 100, top: 200, right: 700, bottom: 800 };
		for (const [W, H] of [
			[2880, 1800],
			[780, 1600],
		] as const) {
			const p = scenePlacement("eclipse", { W, H, dpr: 2, rect });
			expect(p.orb).toEqual(orbPlacement(W, H, 2, rect));
			expect(p.focus).toEqual([0, 0, 0, 0]);
		}
	});
});

describe("scenePlacement: dial-out", () => {
	const wide = { W: 2880, H: 960, dpr: 2 };
	const text = { left: 128, top: 192, right: 1250, bottom: 520 };

	test("without text the point sits right of centre, 40% down", () => {
		const { focus } = scenePlacement("dial-out", { ...wide, rect: null });
		expect(focus[0]).toBeCloseTo(0.74 * 2880);
		expect(focus[1]).toBeCloseTo(DIAL_OUT_DEPTH * 960);
	});

	test("it calls the free side of the panel", () => {
		const left = { left: 100, top: 200, right: 1100, bottom: 600 };
		const right = { left: 1800, top: 200, right: 2800, bottom: 600 };
		expect(
			scenePlacement("dial-out", { ...wide, rect: left }).focus[0],
		).toBeGreaterThan(1440);
		expect(
			scenePlacement("dial-out", { ...wide, rect: right }).focus[0],
		).toBeLessThan(1440);
	});

	test("quiet text is cleared by its dimmed zone and the halo, so the point is never dimmed", () => {
		const { focus } = scenePlacement("dial-out", {
			...wide,
			rect: null,
			quiet: [text],
		});
		expect(focus[0]).toBeGreaterThanOrEqual(
			text.right + quietClearance(wide.W, wide.dpr),
		);
		expect(focus[0]).toBeLessThanOrEqual(0.92 * wide.W);
	});

	test("a narrow canvas keeps the point on the right, behind the text at its quiet opacity", () => {
		const { focus } = scenePlacement("dial-out", {
			W: 780,
			H: 560,
			dpr: 2,
			rect: null,
			quiet: [{ left: 48, top: 96, right: 732, bottom: 420 }],
		});
		expect(focus[0]).toBeCloseTo(0.74 * 780);
		expect(focus[1]).toBeCloseTo(DIAL_OUT_DEPTH * 560);
	});

	test("an explicit point wins and is kept inside the canvas", () => {
		const { focus } = scenePlacement("dial-out", {
			...wide,
			rect: null,
			focus: { x: 2, y: -1 },
		});
		expect(focus[0]).toBeCloseTo(0.92 * 2880);
		expect(focus[1]).toBeCloseTo(0.08 * 960);
	});
});

describe("scenePlacement: dome", () => {
	const wide = { W: 2880, H: 960, dpr: 2 };

	test("the pole is above the top right edge", () => {
		const { focus } = scenePlacement("dome", { ...wide, rect: null });
		expect(focus[0]).toBeGreaterThan(wide.W);
		expect(focus[1]).toBeLessThan(0);
	});

	test("the accent star starts on the free side of the text, clear of its dimmed zone", () => {
		const text = { left: 128, top: 192, right: 1700, bottom: 520 };
		const { focus } = scenePlacement("dome", {
			...wide,
			rect: null,
			quiet: [text],
		});
		expect(focus[2]).toBeGreaterThanOrEqual(
			text.right + quietClearance(wide.W, wide.dpr),
		);
		expect(focus[2]).toBeLessThanOrEqual(0.94 * wide.W);
		expect(focus[3]).toBeCloseTo(0.3 * wide.H);
	});

	test("an explicit star position wins", () => {
		const { focus } = scenePlacement("dome", {
			...wide,
			rect: null,
			focus: { x: 0.5, y: 0.5 },
		});
		expect(focus[2]).toBeCloseTo(1440);
		expect(focus[3]).toBeCloseTo(480);
	});

	test("it has no orb", () => {
		expect(scenePlacement("dome", { ...wide, rect: null }).orb.dim).toBe(1);
	});
});

describe("scenePlacement: band", () => {
	const wide = { W: 2880, H: 960, dpr: 2 };

	test("with no text the Milky Way's centre line starts at 80% and rises to the right at 45 degrees", () => {
		const [top, bottom, half] = scenePlacement("band", {
			...wide,
			rect: null,
		}).focus;
		expect(top).toBeCloseTo(0.8 * wide.W);
		expect(top - bottom).toBeCloseTo(BAND_LEAN * wide.H);
		expect(half).toBeCloseTo(bandHalfWidth(wide.W, wide.H));
	});

	test("at every line of text the centre line is a quiet clearance to its right", () => {
		const clear = quietClearance(wide.W, wide.dpr);
		// a paragraph that is long at the top and short at the bottom
		const lines = [
			{ left: 264, top: 0, right: 1500, bottom: 160 },
			{ left: 264, top: 160, right: 1400, bottom: 320 },
			{ left: 264, top: 320, right: 1510, bottom: 480 },
			{ left: 264, top: 480, right: 1000, bottom: 640 },
			{ left: 264, top: 640, right: 600, bottom: 800 },
		];
		const [top, bottom] = scenePlacement("band", {
			...wide,
			rect: null,
			quiet: lines,
		}).focus;
		const x = (y: number) => top - ((top - bottom) * y) / wide.H;
		for (const line of lines) {
			expect(x(line.bottom)).toBeGreaterThanOrEqual(line.right + clear - 1e-6);
			expect(x(line.top)).toBeGreaterThanOrEqual(line.right + clear - 1e-6);
		}
	});

	test("a short last line lets the river come in further left than one box over the whole paragraph would", () => {
		const lines = [
			{ left: 264, top: 0, right: 1500, bottom: 700 },
			{ left: 264, top: 700, right: 600, bottom: 800 },
		];
		const box = { left: 264, top: 0, right: 1500, bottom: 800 };
		const byLine = scenePlacement("band", {
			...wide,
			rect: null,
			quiet: lines,
		}).focus;
		const byBox = scenePlacement("band", {
			...wide,
			rect: null,
			quiet: [box],
		}).focus;
		expect(byLine[0]).toBeLessThan(byBox[0]);
	});

	test("the core never starts past half a half-width beyond the right edge", () => {
		const text = { left: 0, top: 0, right: 2800, bottom: 900 };
		const [top, , half] = scenePlacement("band", {
			...wide,
			rect: null,
			quiet: [text],
		}).focus;
		expect(top).toBeCloseTo(wide.W + 0.5 * half);
	});

	test("text below the canvas does not move the river", () => {
		const below = { left: 0, top: 2000, right: 2800, bottom: 2400 };
		const [top] = scenePlacement("band", {
			...wide,
			rect: null,
			quiet: [below],
		}).focus;
		expect(top).toBeCloseTo(0.8 * wide.W);
	});

	test("the half-width grows with the canvas", () => {
		expect(bandHalfWidth(2880, 960)).toBeGreaterThan(
			bandHalfWidth(780, 640) / 2,
		);
		expect(bandHalfWidth(2880, 960)).toBeGreaterThan(0);
	});
});

describe("scenePlacement: lost-signal", () => {
	const wide = { W: 2880, H: 960, dpr: 2 };

	test("without a panel the gap is centred", () => {
		const [cx, cy, rx, ry] = scenePlacement("lost-signal", {
			...wide,
			rect: null,
		}).focus;
		expect(cx).toBe(1440);
		expect(cy).toBe(480);
		expect(rx).toBeCloseTo(0.28 * 2880);
		expect(ry).toBeCloseTo(0.3 * 960);
	});

	test("the gap keeps the panel inside it, drift included", () => {
		const rect = { left: 940, top: 260, right: 1940, bottom: 700 };
		const [cx, cy, rx, ry] = scenePlacement("lost-signal", {
			...wide,
			rect,
		}).focus;
		const drift = GAP_DRIFT * Math.min(wide.W, wide.H);
		expect(cx - drift - rx).toBeLessThanOrEqual(rect.left - 0);
		expect(cx + drift + rx).toBeGreaterThanOrEqual(rect.right);
		// The panel stays inside the gap at every drift extreme.
		expect(cx - rx + drift).toBeLessThanOrEqual(rect.left);
		expect(cx + rx - drift).toBeGreaterThanOrEqual(rect.right);
		expect(cy - ry + 0.55 * drift).toBeLessThanOrEqual(rect.top);
		expect(cy + ry - 0.55 * drift).toBeGreaterThanOrEqual(rect.bottom);
	});

	test("a huge panel is clamped so noise still frames it", () => {
		const [cx, cy, rx, ry] = scenePlacement("lost-signal", {
			...wide,
			rect: { left: 0, top: 0, right: 2880, bottom: 960 },
		}).focus;
		expect(rx).toBeLessThanOrEqual(0.46 * 2880);
		expect(ry).toBeLessThanOrEqual(0.44 * 960);
		expect(cx).toBe(1440);
		expect(cy).toBe(480);
	});

	test("a panel near an edge keeps the gap inside the canvas", () => {
		const [cx, cy, rx, ry] = scenePlacement("lost-signal", {
			...wide,
			rect: { left: 0, top: 0, right: 600, bottom: 300 },
		}).focus;
		expect(cx - rx).toBeGreaterThanOrEqual(0);
		expect(cy - ry).toBeGreaterThanOrEqual(0);
		expect(cx + rx).toBeLessThanOrEqual(2880);
		expect(cy + ry).toBeLessThanOrEqual(960);
	});
});
