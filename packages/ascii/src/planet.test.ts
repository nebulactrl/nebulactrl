import { describe, expect, test } from "bun:test";
import { regionFromRect } from "./layout";
import { NEBULA_PALETTE } from "./palette";
import {
	buildPlanetMask,
	easeToward,
	PLANET_CELL_CSS_PX,
	planetExtent,
	planetGrid,
	planetOrb,
	planetTones,
	pointerFraction,
	RING_DUST,
	RING_INNER,
	RING_OUTER,
	RING_SQUASH,
	RING_TILT,
} from "./planet";
import { PLANET_BLIT_FRAG, PLANET_FIELD_FRAG } from "./planet.glsl";

describe("planetTones", () => {
	test("tone 2 is the accent ink, tone 1 the accent 55% toward black", () => {
		const { c1, c2 } = planetTones(NEBULA_PALETTE);
		const [r, g, b] = NEBULA_PALETTE.accentInk;
		expect(c2).toEqual([r / 255, g / 255, b / 255]);
		const [ar, ag, ab] = NEBULA_PALETTE.accent;
		expect(c1[0]).toBeCloseTo((ar * 0.45) / 255);
		expect(c1[1]).toBeCloseTo((ag * 0.45) / 255);
		expect(c1[2]).toBeCloseTo((ab * 0.45) / 255);
	});

	test("it reads the palette, not a hex", () => {
		const { c1, c2 } = planetTones({
			...NEBULA_PALETTE,
			accent: [200, 0, 0],
			accentInk: [255, 255, 0],
		});
		expect(c1[0]).toBeCloseTo((200 * 0.45) / 255);
		expect(c1[1]).toBe(0);
		expect(c2).toEqual([1, 1, 0]);
	});
});

describe("planetGrid", () => {
	test("a cell is 2 CSS px, whatever the pixel ratio", () => {
		expect(PLANET_CELL_CSS_PX).toBe(2);
		expect(planetGrid(1280, 720, 1)).toEqual({
			W: 1280,
			H: 720,
			dpr: 1,
			cell: 2,
			cols: 640,
			rows: 360,
		});
		const g = planetGrid(1280, 720, 2);
		expect(g.W).toBe(2560);
		expect(g.cell).toBe(4);
		expect(g.cols).toBe(640);
		expect(g.rows).toBe(360);
	});

	test("a fractional ratio keeps the cell at 2 CSS px and covers the canvas", () => {
		const g = planetGrid(1000, 600, 1.5);
		expect(g.cell).toBeCloseTo(3);
		expect(g.cols * g.cell).toBeGreaterThanOrEqual(g.W);
		expect((g.cols - 1) * g.cell).toBeLessThan(g.W);
		expect(g.rows * g.cell).toBeGreaterThanOrEqual(g.H);
	});

	test("an empty canvas is one cell, not zero", () => {
		const g = planetGrid(0, 0, 2);
		expect(g.W).toBe(1);
		expect(g.cols).toBe(1);
		expect(g.rows).toBe(1);
	});
});

describe("buildPlanetMask", () => {
	const grid = { cols: 4, rows: 2, cell: 10 };
	const text = regionFromRect({ left: 0, top: 0, right: 20, bottom: 20 }, 1, {
		min: 0,
		feather: 10,
		pad: 0,
	});

	test("no regions leave every cell fully on", () => {
		const out = buildPlanetMask(grid, [], [], new Uint8Array(16));
		expect(Array.from(out)).toEqual(new Array(16).fill(255));
	});

	test("text cells go to zero with a one-cell feather, row 0 at the top", () => {
		const out = buildPlanetMask(grid, [], [text], new Uint8Array(16));
		for (let row = 0; row < 2; row++) {
			const g = [0, 1, 2, 3].map((col) => out[(row * 4 + col) * 2 + 1]);
			expect(g).toEqual([0, 0, 128, 255]);
		}
		for (let i = 0; i < 16; i += 2) expect(out[i]).toBe(255);
	});

	test("the soft channel dims on its own", () => {
		const panel = regionFromRect(
			{ left: 30, top: 0, right: 40, bottom: 20 },
			1,
			{ min: 0.8, feather: 10, pad: 0 },
		);
		const out = buildPlanetMask(grid, [panel], [], new Uint8Array(16));
		expect(out[3 * 2]).toBe(Math.round(0.8 * 255));
		expect(out[0]).toBe(255);
		for (let i = 1; i < 16; i += 2) expect(out[i]).toBe(255);
	});
});

describe("easeToward", () => {
	test("one 60 Hz frame closes `rate` of the gap", () => {
		expect(easeToward(0, 1, 0.08, 1 / 60)).toBeCloseTo(0.08);
		expect(easeToward(10, 0, 0.06, 1 / 60)).toBeCloseTo(10 * 0.94);
	});

	test("two half frames are one frame, so the frame rate does not change the feel", () => {
		const half = easeToward(easeToward(0, 1, 0.08, 1 / 120), 1, 0.08, 1 / 120);
		expect(half).toBeCloseTo(easeToward(0, 1, 0.08, 1 / 60));
	});

	test("no time, no move; it never overshoots", () => {
		expect(easeToward(0.3, 1, 0.08, 0)).toBe(0.3);
		expect(easeToward(0, 1, 0.08, 100)).toBeLessThanOrEqual(1);
	});
});

describe("pointerFraction", () => {
	const rect = { left: 100, top: 50, width: 200, height: 100 };

	test("fractions of the rect with y up", () => {
		const out: [number, number] = [0, 0];
		expect(pointerFraction(rect, 200, 100, out)).toBe(true);
		expect(out).toEqual([0.5, 0.5]);
		expect(pointerFraction(rect, 100, 150, out)).toBe(true);
		expect(out).toEqual([0, 0]);
		expect(pointerFraction(rect, 300, 50, out)).toBe(true);
		expect(out).toEqual([1, 1]);
	});

	test("outside the rect it still reports where, and says false", () => {
		const out: [number, number] = [0, 0];
		expect(pointerFraction(rect, 50, 60, out)).toBe(false);
		expect(out[0]).toBeCloseTo(-0.25);
		expect(out[1]).toBeCloseTo(0.9);
	});

	test("a collapsed rect is never hit", () => {
		const out: [number, number] = [7, 7];
		expect(
			pointerFraction({ left: 0, top: 0, width: 0, height: 0 }, 0, 0, out),
		).toBe(false);
		expect(out).toEqual([7, 7]);
	});
});

describe("the planet shader", () => {
	const uniforms = (source: string, names: readonly string[]) => {
		for (const u of names) {
			expect(source).toMatch(new RegExp(`uniform [A-Za-z0-9]+ ${u};`));
		}
	};

	test("the field pass shades one cell per fragment into a tone index and reads the uniforms the engine sets", () => {
		expect(PLANET_FIELD_FRAG.startsWith("#version 300 es")).toBe(true);
		expect(PLANET_FIELD_FRAG).toContain("o = vec4(lv * 0.5, 0.0, 0.0, 1.0);");
		expect(PLANET_FIELD_FRAG).toContain("uGrid.y - 0.5 - gl_FragCoord.y");
		uniforms(PLANET_FIELD_FRAG, [
			"uRes",
			"uCell",
			"uGrid",
			"uT",
			"uOrb",
			"uDim",
			"uCur",
			"uCurW",
			"uMask",
		]);
	});

	test("the blit pass reads each cell's tone nearest-neighbour: tone 0 is transparent, tones 1 and 2 premultiplied palette colours", () => {
		expect(PLANET_BLIT_FRAG.startsWith("#version 300 es")).toBe(true);
		expect(PLANET_BLIT_FRAG).toContain("texelFetch(uField");
		expect(PLANET_BLIT_FRAG).toContain(
			"o = tone < 1 ? vec4(0.0) : vec4(tone < 2 ? uC1 : uC2, 1.0);",
		);
		uniforms(PLANET_BLIT_FRAG, [
			"uRes",
			"uCell",
			"uGrid",
			"uField",
			"uC1",
			"uC2",
		]);
	});

	test("the tone index survives the 8-bit target: 0, 1 and 2 round-trip through index / 2", () => {
		for (const tone of [0, 1, 2]) {
			const stored = Math.round(tone * 0.5 * 255) / 255;
			expect(Math.floor(stored * 2 + 0.5)).toBe(tone);
		}
	});

	test("it keeps the ring's tilt and squash, and its bands sit at the exported radii", () => {
		expect(PLANET_FIELD_FRAG).toContain("cos(-0.38)");
		expect(PLANET_FIELD_FRAG).toContain("dr.y * 3.4");
		expect(RING_TILT).toBe(0.38);
		expect(RING_SQUASH).toBe(3.4);
		expect(PLANET_FIELD_FRAG).toContain(`(rr - ${RING_INNER})`);
		expect(PLANET_FIELD_FRAG).toContain(`(rr - ${RING_OUTER})`);
		expect(PLANET_FIELD_FRAG).toContain(`(rr - ${RING_DUST})`);
	});

	test("the ring is soft: a bright inner band, a dimmer outer one, faint dust, and the planet shades its back half", () => {
		expect(PLANET_FIELD_FRAG).toMatch(
			/\* 0\.95 \+ exp\(.*\) \* 0\.55 \+ exp\(.*\) \* 0\.18;/,
		);
		expect(PLANET_FIELD_FRAG).toContain("ring *= mix(0.12, 1.0");
	});
});

describe("planetExtent", () => {
	test("the dust band's outer edge decides the width, the sphere and its glow the height", () => {
		const { halfW, halfH } = planetExtent();
		expect(halfW).toBeGreaterThan(1.95);
		expect(halfW).toBeLessThan(2.1);
		expect(halfH).toBeCloseTo(1.12);
	});
});

describe("planetOrb", () => {
	const { halfW, halfH } = planetExtent();
	const wide = { W: 1440, H: 900, narrow: false };
	const copy = { left: 96, top: 200, right: 700, bottom: 700 };
	const overlap = 0.12 * 1440;

	test("the ring's far tip nearly reaches the edge and its near tip slides under the copy", () => {
		const o = planetOrb({ ...wide, rect: copy, quiet: [] });
		expect(o.cx - halfW * o.r).toBeCloseTo(copy.right - overlap);
		expect(o.cx + halfW * o.r).toBeCloseTo(0.99 * 1440);
		expect(o.cx - halfW * o.r).toBeLessThan(copy.right);
		expect(o.cy).toBeCloseTo(0.52 * 900);
		expect(o.cy - halfH * o.r).toBeGreaterThanOrEqual(0.06 * 900 - 1e-6);
		expect(o.cy + halfH * o.r).toBeLessThanOrEqual(0.94 * 900 + 1e-6);
		expect(o.r).toBeGreaterThan(0.16 * 900);
		expect(o.r).toBeLessThanOrEqual(0.26 * 900);
		expect(o.dim).toBe(1);
	});

	test("the planet is at most 26% of the height, however much room there is", () => {
		const o = planetOrb({
			...wide,
			rect: { left: 40, top: 200, right: 200, bottom: 700 },
			quiet: [],
		});
		expect(o.r).toBeCloseTo(0.26 * 900);
		expect(o.cx + halfW * o.r).toBeLessThanOrEqual(0.99 * 1440);
	});

	test("the quiet text counts as copy", () => {
		const quiet = { left: 96, top: 120, right: 780, bottom: 180 };
		const o = planetOrb({ ...wide, rect: copy, quiet: [quiet] });
		expect(o.cx - halfW * o.r).toBeCloseTo(780 - overlap);
	});

	test("copy on the right puts the planet on the left", () => {
		const right = { left: 700, top: 200, right: 1344, bottom: 700 };
		const o = planetOrb({ ...wide, rect: right, quiet: [] });
		expect(o.cx).toBeLessThan(700);
		expect(o.cx - halfW * o.r).toBeCloseTo(0.01 * 1440);
		expect(o.cx + halfW * o.r).toBeCloseTo(700 + overlap);
	});

	test("with no copy it takes the right side, ring fully in view", () => {
		const o = planetOrb({ ...wide, rect: null, quiet: [] });
		expect(o.cx - halfW * o.r).toBeGreaterThanOrEqual(0.4 * 1440 - 1e-6);
		expect(o.cx + halfW * o.r).toBeLessThanOrEqual(0.99 * 1440 + 1e-6);
	});

	test("the placement scales with the canvas, so a bigger pixel ratio gives the same picture", () => {
		const a = planetOrb({ ...wide, rect: copy, quiet: [] });
		const b = planetOrb({
			W: 2880,
			H: 1800,
			narrow: false,
			rect: { left: 192, top: 400, right: 1400, bottom: 1400 },
			quiet: [],
		});
		expect(b.r).toBeCloseTo(2 * a.r);
		expect(b.cx).toBeCloseTo(2 * a.cx);
		expect(b.cy).toBeCloseTo(2 * a.cy);
	});

	test("no free room: the planet keeps a floor of 16% of the height", () => {
		const o = planetOrb({
			...wide,
			rect: { left: 40, top: 100, right: 1300, bottom: 800 },
			quiet: [],
		});
		expect(o.r).toBeCloseTo(0.16 * 900);
	});

	describe("narrow", () => {
		const W = 1170;
		const H = 2532;
		const r = 0.34 * W;

		test("the planet sits under the panel, sized by the sphere, at full strength", () => {
			const rect = { left: 60, top: 900, right: 1110, bottom: 1700 };
			const o = planetOrb({ W, H, narrow: true, rect, quiet: [] });
			expect(o.cx).toBe(W / 2);
			expect(o.r).toBeCloseTo(r);
			expect(o.cy).toBeCloseTo(1700 + 0.9 * r);
			expect(o.dim).toBe(1);
			// The sphere's top is just under the panel; the ring tips may run off the sides.
			expect(o.cy - o.r).toBeCloseTo(rect.bottom - 0.1 * r);
			expect(o.cx - halfW * o.r).toBeLessThan(0);
			expect(o.cx + halfW * o.r).toBeGreaterThan(W);
		});

		test("it follows the panel's bottom edge, quiet text included, even with no panel", () => {
			const text = { left: 60, top: 100, right: 1110, bottom: 2300 };
			const o = planetOrb({ W, H, narrow: true, rect: null, quiet: [text] });
			expect(o.cy).toBeCloseTo(2300 + 0.9 * r);
			expect(o.dim).toBe(1);
			expect(
				planetOrb({ W, H, narrow: true, rect: null, quiet: [] }).cy,
			).toBeCloseTo(0.9 * r);
		});

		test("it stays while any of the sphere shows, and gives way once none would", () => {
			const shows = { left: 60, top: 300, right: 1110, bottom: 2560 };
			expect(
				planetOrb({ W, H, narrow: true, rect: shows, quiet: [] }).dim,
			).toBe(1);
			const hidden = { left: 60, top: 300, right: 1110, bottom: 2600 };
			expect(
				planetOrb({ W, H, narrow: true, rect: hidden, quiet: [] }).dim,
			).toBe(0.6);
		});

		test("a panel that reaches the bottom: behind it, the whole ring inside the margins, dim 0.6", () => {
			const rect = { left: 60, top: 1000, right: 1110, bottom: 2700 };
			const o = planetOrb({ W, H, narrow: true, rect, quiet: [] });
			expect(o.cx).toBe(W / 2);
			expect(o.dim).toBe(0.6);
			expect(o.cx - halfW * o.r).toBeGreaterThanOrEqual(0.04 * W - 1e-6);
			expect(o.cx + halfW * o.r).toBeLessThanOrEqual(0.96 * W + 1e-6);
			expect(o.cy).toBeCloseTo((rect.top + rect.bottom) / 2);
		});

		test("behind the panel, a panel near the top edge cannot push the planet out of view", () => {
			const rect = { left: 60, top: 0, right: 1110, bottom: 2700 };
			const o = planetOrb({ W, H, narrow: true, rect, quiet: [] });
			expect(o.dim).toBe(0.6);
			expect(o.cy - halfH * o.r).toBeGreaterThanOrEqual(0.08 * H - 1e-6);
			expect(o.cy + halfH * o.r).toBeLessThanOrEqual(0.92 * H + 1e-6);
		});
	});
});
