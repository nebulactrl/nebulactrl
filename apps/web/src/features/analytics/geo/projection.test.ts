import { describe, expect, test } from "bun:test";
import type { MapBounds } from "@nebulactrl/dither/backgrounds/geo";
import { REGIONS } from "./model";
import {
	fillAspect,
	fitProjection,
	inProjection,
	LAT_LIMITS,
	type Projection,
	projectPoint,
	transformCss,
	viewTransform,
} from "./projection";

const europe = REGIONS[1].bounds;
const world = REGIONS[0].bounds;

function must(p: Projection | null): Projection {
	if (!p) throw new Error("no projection");
	return p;
}

const contains = (outer: MapBounds, inner: MapBounds) =>
	outer.north >= inner.north - 1e-9 &&
	outer.south <= inner.south + 1e-9 &&
	outer.west <= inner.west + 1e-9 &&
	outer.east >= inner.east - 1e-9;

describe("fitProjection", () => {
	test("a tall box widens the region north and south and fills the box", () => {
		const box = { width: 800, height: 640 };
		const p = must(fitProjection(europe, box, 4));
		expect(contains(p.bounds, europe)).toBe(true);
		// Europe is 2:1; the box is 1.25:1, so it shows more land above and below.
		expect(p.bounds.north - p.bounds.south).toBeGreaterThan(
			europe.north - europe.south,
		);
		expect(p.bounds.east - p.bounds.west).toBeCloseTo(
			europe.east - europe.west,
			0,
		);
		expect(p.height).toBeGreaterThan(box.height - 4);
		expect(p.width).toBe(box.width);
	});

	test("a wide box shows more land east and west", () => {
		const p = must(fitProjection(europe, { width: 1200, height: 300 }, 4));
		expect(contains(p.bounds, europe)).toBe(true);
		expect(p.bounds.east - p.bounds.west).toBeGreaterThan(
			europe.east - europe.west,
		);
		expect(p.height).toBeGreaterThan(300 - 4);
	});

	test("cells are whole pixels and square, and the grid is centred", () => {
		const box = { width: 803, height: 641 };
		const p = must(fitProjection(europe, box, 4));
		expect(p.width).toBe(p.columns * 4);
		expect(p.height).toBe(p.rows * 4);
		expect(p.width).toBeLessThanOrEqual(box.width);
		expect(p.height).toBeLessThanOrEqual(box.height);
		// Square dots: pixels per degree match across and down.
		const perLon = p.width / (p.bounds.east - p.bounds.west);
		const perLat = p.height / (p.bounds.north - p.bounds.south);
		expect(perLon).toBeCloseTo(perLat, 9);
		expect(p.x).toBe(Math.floor((box.width - p.width) / 2));
		expect(p.y).toBe(Math.floor((box.height - p.height) / 2));
	});

	test("a bigger box adds dots, it does not make them bigger", () => {
		const small = must(fitProjection(europe, { width: 420, height: 210 }, 4));
		const large = must(fitProjection(europe, { width: 840, height: 420 }, 4));
		expect(small.pitch).toBe(large.pitch);
		expect(large.columns).toBeGreaterThan(small.columns * 1.9);
		// The same part of the world, give or take one column of rounding.
		expect(large.bounds.west).toBeCloseTo(small.bounds.west, 0);
		expect(large.bounds.east).toBeCloseTo(small.bounds.east, 0);
	});

	test("the world keeps to the latitude limits and is centred in a tall box", () => {
		const box = { width: 600, height: 900 };
		const p = must(fitProjection(world, box, 4));
		expect(p.bounds.north).toBeLessThanOrEqual(LAT_LIMITS.north + 1e-9);
		expect(p.bounds.south).toBeGreaterThanOrEqual(LAT_LIMITS.south - 1e-9);
		expect(contains(p.bounds, world)).toBe(true);
		expect(p.bounds.east - p.bounds.west).toBeCloseTo(360, 6);
		expect(p.height).toBeLessThan(box.height);
		expect(p.y).toBeGreaterThan(0);
	});

	test("a box of the region's own shape leaves it as it is", () => {
		const p = must(fitProjection(world, { width: 600, height: 600 / 2.68 }, 4));
		expect(p.bounds.north).toBeCloseTo(world.north, 0);
		expect(p.bounds.south).toBeCloseTo(world.south, 0);
	});

	test("no projection for a box smaller than a cell", () => {
		expect(fitProjection(europe, { width: 0, height: 100 }, 4)).toBeNull();
		expect(fitProjection(europe, { width: 3, height: 3 }, 4)).toBeNull();
	});
});

describe("fillAspect", () => {
	test("a preferred shape the region can fill is kept", () => {
		expect(fillAspect(1.6, europe)).toBe(1.6);
	});

	test("a world map is not given a box taller than its latitudes", () => {
		const a = fillAspect(1.6, world);
		expect(a).toBeGreaterThan(1.6);
		const width = 358;
		const p = must(fitProjection(world, { width, height: width / a }, 4));
		// No empty band: the grid reaches both edges of the box, give or take a cell.
		expect(p.y).toBeLessThanOrEqual(4);
		expect(p.x).toBeLessThanOrEqual(4);
	});

	test("a box shape it returns always fills", () => {
		for (const bounds of [world, europe, REGIONS[5].bounds]) {
			for (const preferred of [0.8, 1.6, 3]) {
				const a = fillAspect(preferred, bounds);
				const box = { width: 600, height: 600 / a };
				const p = must(fitProjection(bounds, box, 4));
				expect(box.width - p.width).toBeLessThanOrEqual(8);
				expect(box.height - p.height).toBeLessThanOrEqual(8);
			}
		}
	});
});

describe("viewTransform", () => {
	const box = { width: 800, height: 600 };
	const from = must(fitProjection(world, box, 4));
	const to = must(fitProjection(europe, box, 4));

	test("is nothing between a projection and itself", () => {
		const t = viewTransform(to, to);
		expect(t.x).toBeCloseTo(0, 9);
		expect(t.y).toBeCloseTo(0, 9);
		expect(t.scaleX).toBeCloseTo(1, 9);
		expect(t.scaleY).toBeCloseTo(1, 9);
		expect(transformCss(t)).toContain("scale(1, 1)");
	});

	test("puts every place of `to` where `from` draws it", () => {
		const t = viewTransform(from, to);
		for (const at of [
			[50.1, 8.7],
			[40.4, -3.7],
			[60, 25],
		] as const) {
			const a = projectPoint(from, at);
			const b = projectPoint(to, at);
			expect(to.x + t.x + t.scaleX * b.x).toBeCloseTo(from.x + a.x, 6);
			expect(to.y + t.y + t.scaleY * b.y).toBeCloseTo(from.y + a.y, 6);
		}
	});

	test("goes the other way too", () => {
		const t = viewTransform(to, from);
		const at = [50.1, 8.7] as const;
		const a = projectPoint(to, at);
		const b = projectPoint(from, at);
		expect(from.x + t.x + t.scaleX * b.x).toBeCloseTo(to.x + a.x, 6);
		expect(from.y + t.y + t.scaleY * b.y).toBeCloseTo(to.y + a.y, 6);
	});

	test("pans the short way round the antimeridian", () => {
		const oceania = must(fitProjection(REGIONS[6].bounds, box, 4));
		const america = must(fitProjection(REGIONS[2].bounds, box, 4));
		const t = viewTransform(oceania, america);
		// Fiji (-17.7, 178) is 8° from the edge of both: the pan must stay under a screen or two.
		const at = [-17.7, 178] as const;
		const a = projectPoint(oceania, at);
		const b = projectPoint(america, at);
		expect(america.x + t.x + t.scaleX * b.x).toBeCloseTo(oceania.x + a.x, 6);
		expect(Math.abs(t.x)).toBeLessThan(box.width * 4);
	});
});

describe("inProjection", () => {
	test("is true for a place inside the grid and false outside", () => {
		const p = must(fitProjection(europe, { width: 800, height: 400 }, 4));
		expect(inProjection(p, [50, 8])).toBe(true);
		expect(inProjection(p, [-33.9, 151.2])).toBe(false);
	});
});
