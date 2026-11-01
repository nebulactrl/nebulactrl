import { describe, expect, test } from "bun:test";
import type { MapBounds } from "@nebulactrl/dither/backgrounds/geo";
import { MIN_LAT_SPAN, REGIONS } from "./model";
import { fitProjection, LAT_LIMITS, viewTransform } from "./projection";
import {
	axisRange,
	clampView,
	coverRange,
	enterFrom,
	FRAME_MS,
	FRICTION,
	isPinchWheel,
	keyAction,
	MIN_COVER,
	momentumStep,
	normaliseLon,
	panLimits,
	REST_VIEW,
	releaseVelocity,
	resist,
	resistView,
	rubberBand,
	STOP_SPEED,
	viewBounds,
	WHEEL_CAP,
	wheelFactor,
	wheelPixels,
	zoomAround,
	zoomModifier,
	zoomRange,
	zoomWindow,
} from "./view";

const europe = REGIONS[1].bounds;
const world = REGIONS[0].bounds;
const box = { width: 800, height: 500 };

function grid(bounds: MapBounds) {
	const p = fitProjection(bounds, box, 4);
	if (!p) throw new Error("no projection");
	return p;
}

const near = (a: number, b: number, digits = 6) =>
	expect(a).toBeCloseTo(b, digits);

describe("wheel delta normalisation", () => {
	test("pixels pass through, lines and pages scale up", () => {
		expect(wheelPixels({ deltaY: 7, deltaMode: 0 }, 400)).toBe(7);
		expect(wheelPixels({ deltaY: 3, deltaMode: 1 }, 400)).toBe(48);
		expect(wheelPixels({ deltaY: 1, deltaMode: 2 }, 400)).toBe(400);
		expect(wheelPixels({ deltaY: -2, deltaMode: 2 }, 300)).toBe(-600);
	});

	test("a firefox notch (3 lines) zooms like a chrome one (100px), near enough", () => {
		const line = wheelFactor(wheelPixels({ deltaY: 3, deltaMode: 1 }, 400));
		const pixel = wheelFactor(wheelPixels({ deltaY: 100, deltaMode: 0 }, 400));
		expect(Math.abs(Math.log(line) - Math.log(pixel))).toBeLessThan(0.15);
	});

	test("scrolling up zooms in, down zooms out, and opposite steps cancel", () => {
		expect(wheelFactor(-100)).toBeGreaterThan(1);
		expect(wheelFactor(100)).toBeLessThan(1);
		near(wheelFactor(-40) * wheelFactor(40), 1);
		near(wheelFactor(0), 1);
	});

	test("steps compose: two half notches are one notch", () => {
		near(wheelFactor(-50) * wheelFactor(-50), wheelFactor(-100));
	});

	test("one event never zooms past the cap, however big the page jump", () => {
		expect(wheelFactor(-100000)).toBe(wheelFactor(-WHEEL_CAP));
		expect(wheelFactor(100000)).toBe(wheelFactor(WHEEL_CAP));
	});

	test("a pinch counts for more per pixel than a notch", () => {
		expect(wheelFactor(-5, true)).toBeGreaterThan(wheelFactor(-5, false));
	});

	test("a ctrl wheel is a pinch only when small, in pixels and without the meta key", () => {
		const base = { ctrlKey: true, metaKey: false, deltaMode: 0, deltaY: -4.5 };
		expect(isPinchWheel(base)).toBe(true);
		expect(isPinchWheel({ ...base, deltaY: -100 })).toBe(false);
		expect(isPinchWheel({ ...base, deltaMode: 1 })).toBe(false);
		expect(isPinchWheel({ ...base, metaKey: true })).toBe(false);
		expect(isPinchWheel({ ...base, ctrlKey: false })).toBe(false);
	});
});

describe("zoomAround", () => {
	const range = { min: 0.25, max: 8 };
	/** Where the point at unscaled `p` lands after the view. */
	const at = (
		v: { x: number; y: number; k: number },
		p: number,
		axis: "x" | "y",
	) => v[axis] + v.k * p;

	test("the point under the cursor stays under it", () => {
		const v = zoomAround(
			{ x: 30, y: -12, k: 1.5 },
			1.8,
			{ x: 210, y: 90 },
			range,
		);
		// What was under (210, 90) before: unscaled ((210 - 30) / 1.5, (90 + 12) / 1.5).
		near(at(v, (210 - 30) / 1.5, "x"), 210);
		near(at(v, (90 + 12) / 1.5, "y"), 90);
		near(v.k, 1.5 * 1.8);
	});

	test("zooming about the origin only scales", () => {
		const v = zoomAround({ x: 0, y: 0, k: 1 }, 2, { x: 0, y: 0 }, range);
		expect(v).toEqual({ x: 0, y: 0, k: 2 });
	});

	test("a factor of 1 changes nothing", () => {
		const start = { x: 12, y: -7, k: 2 };
		expect(zoomAround(start, 1, { x: 100, y: 40 }, range)).toEqual(start);
	});

	test("the scale stops at the range, and the point still holds there", () => {
		const v = zoomAround({ x: 0, y: 0, k: 7 }, 3, { x: 100, y: 50 }, range);
		expect(v.k).toBe(8);
		near(at(v, 100 / 7, "x"), 100);
		const out = zoomAround(
			{ x: 0, y: 0, k: 0.3 },
			0.1,
			{ x: 100, y: 50 },
			range,
		);
		expect(out.k).toBe(0.25);
	});

	test("at the limit a further wheel notch is a no-op", () => {
		const atMax = { x: 5, y: 9, k: 8 };
		expect(zoomAround(atMax, 1.25, { x: 60, y: 60 }, range)).toEqual(atMax);
	});
});

describe("zoomRange", () => {
	test("in to the buttons' limit, out to the widest the map shows", () => {
		const p = grid(europe);
		const { min, max } = zoomRange(p.bounds);
		const lat = p.bounds.north - p.bounds.south;
		near(lat / max, MIN_LAT_SPAN);
		expect(lat / min).toBeLessThanOrEqual(
			LAT_LIMITS.north - LAT_LIMITS.south + 1e-9,
		);
		expect(p.bounds.east - p.bounds.west).toBeLessThanOrEqual(
			(p.bounds.east - p.bounds.west) / min,
		);
	});

	test("the world cannot zoom out any further, but can zoom in", () => {
		const p = grid(world);
		const { min, max } = zoomRange(p.bounds);
		expect(min).toBe(1);
		expect(max).toBeGreaterThan(1);
	});

	test("one full turn of longitude is the limit even when the latitude has room", () => {
		const { min } = zoomRange({ north: 40, south: 0, west: -180, east: 180 });
		expect(min).toBe(1);
	});
});

describe("zoomWindow", () => {
	test("keeps the window's shape and centre", () => {
		const b = { north: 60, south: 30, west: 0, east: 48 };
		const z = zoomWindow(b, 1 / 1.6);
		near((z.east - z.west) / (z.north - z.south), 48 / 30);
		near((z.north + z.south) / 2, 45);
		near((z.east + z.west) / 2, 24);
		near(z.north - z.south, 30 / 1.6);
	});

	test("stops at the minimum span and inside the latitude limits", () => {
		const tiny = zoomWindow({ north: 10, south: 4, west: 0, east: 12 }, 0.1);
		near(tiny.north - tiny.south, MIN_LAT_SPAN);
		const wide = zoomWindow({ north: 84, south: 30, west: 0, east: 100 }, 5);
		expect(wide.north).toBeLessThanOrEqual(LAT_LIMITS.north + 1e-9);
		expect(wide.south).toBeGreaterThanOrEqual(LAT_LIMITS.south - 1e-9);
	});
});

describe("pan clamp with rubber band", () => {
	test("content that fills the viewport can sit anywhere that keeps it covered", () => {
		// 0..300 of content at scale 2 is 600px in a 400px viewport.
		const covering = axisRange(0, 300, 2, 400);
		expect(covering.lo).toBe(-200);
		expect(covering.hi).toBeCloseTo(0, 9);
		expect(axisRange(-50, 250, 1, 200)).toEqual({ lo: -50, hi: 50 });
	});

	test("content smaller than the viewport is held centred", () => {
		const r = axisRange(0, 100, 1, 400);
		expect(r).toEqual({ lo: 150, hi: 150 });
	});

	test("inside the range an offset is drawn as given", () => {
		expect(resist(-80, { lo: -200, hi: 0 }, 400)).toBe(-80);
		expect(resist(0, { lo: -200, hi: 0 }, 400)).toBe(0);
		expect(resist(-200, { lo: -200, hi: 0 }, 400)).toBe(-200);
	});

	test("past an edge the map follows the pull in less and less", () => {
		const range = { lo: -200, hi: 0 };
		const a = resist(20, range, 400);
		const b = resist(40, range, 400);
		const c = resist(400, range, 400);
		expect(a).toBeGreaterThan(0);
		expect(a).toBeLessThan(20);
		expect(b).toBeGreaterThan(a);
		// Each extra pixel gives less than the one before.
		expect(b - a).toBeLessThan(a - 0);
		expect(c).toBeLessThan(400);
		// Symmetric on the far edge.
		near(resist(-220, range, 400), -200 - a);
	});

	test("the rubber band never reaches a viewport's length and starts near the stiffness slope", () => {
		expect(rubberBand(0, 400)).toBe(0);
		expect(rubberBand(-5, 400)).toBe(0);
		expect(rubberBand(1e9, 400)).toBeLessThan(400);
		expect(rubberBand(1, 400) / 1).toBeCloseTo(0.55, 1);
	});

	test("the vertical clamp keeps the grid inside the latitude limits", () => {
		const p = grid(europe);
		const up = clampView({ x: 0, y: 9999, k: 1 }, p);
		const down = clampView({ x: 0, y: -9999, k: 1 }, p);
		const top = viewBounds(p, up);
		const bottom = viewBounds(p, down);
		expect(top.north).toBeLessThanOrEqual(LAT_LIMITS.north + 1e-6);
		expect(bottom.south).toBeGreaterThanOrEqual(LAT_LIMITS.south - 1e-6);
	});

	test("a sideways drag keeps at least half of the box covered", () => {
		const p = grid(europe);
		const right = clampView({ x: 12345, y: 0, k: 1 }, p).x;
		const left = clampView({ x: -12345, y: 0, k: 1 }, p).x;
		// The grid spans 0..width: moved right it uncovers its left, and the
		// share still showing is what is left of the box.
		near(right, p.width * (1 - MIN_COVER));
		near(left, -p.width * (1 - MIN_COVER));
		expect((p.width - right) / p.width).toBeGreaterThanOrEqual(MIN_COVER);
		expect((p.width + left) / p.width).toBeGreaterThanOrEqual(MIN_COVER);
	});

	test("a drag inside the cover is left alone, and one past it is damped", () => {
		const p = grid(europe);
		const inside = p.width * 0.3;
		expect(clampView({ x: inside, y: 0, k: 1 }, p).x).toBe(inside);
		expect(resistView({ x: -inside, y: 0, k: 1 }, p).x).toBe(-inside);
		const limit = clampView({ x: 1e6, y: 0, k: 1 }, p).x;
		const soft = resistView({ x: limit + 40, y: 0, k: 1 }, p).x;
		expect(soft).toBeGreaterThan(limit);
		expect(soft).toBeLessThan(limit + 40);
	});

	test("zoomed in, the grid is wider than the box and can still go half a box", () => {
		const p = grid(europe);
		const { x } = panLimits(p, 3);
		near(x.hi, p.width * (1 - MIN_COVER));
		near(x.lo, p.width * MIN_COVER - p.width * 3);
	});

	test("coverRange: a grid smaller than the share stays whole in the box", () => {
		// 100px of content at k 0.5 is 50px: never worth less than all of it.
		const r = coverRange(100, 0.5, 400);
		expect(r).toEqual({ lo: 0, hi: 350 });
		expect(coverRange(400, 1, 400)).toEqual({ lo: -200, hi: 200 });
		expect(coverRange(400, 1, 400, 1)).toEqual({ lo: 0, hi: 0 });
	});

	test("a resisted view eases past the edge and a clamped one stops at it", () => {
		const p = grid(europe);
		const edge = clampView({ x: 0, y: 5000, k: 1 }, p).y;
		const soft = resistView({ x: 0, y: 5000, k: 1 }, p).y;
		expect(soft).toBeGreaterThan(edge);
		expect(soft).toBeLessThan(edge + p.height);
		expect(resistView({ x: 0, y: edge - 10, k: 1 }, p).y).toBe(edge - 10);
	});

	test("a map smaller than the box cannot be pushed off centre", () => {
		const p = grid(world);
		// k < 1 only arises past the zoom range, but the rule is the same.
		const a = clampView({ x: 0, y: 40, k: 0.5 }, p);
		const b = clampView({ x: 0, y: -40, k: 0.5 }, p);
		expect(a.y).toBe(b.y);
	});
});

describe("momentum decay", () => {
	test("one frame moves the full speed and leaves friction's share", () => {
		const s = momentumStep({ x: 20, y: -10 }, FRAME_MS);
		near(s.delta.x, 20);
		near(s.delta.y, -10);
		near(s.velocity.x, 20 * FRICTION);
		near(s.velocity.y, -10 * FRICTION);
	});

	test("two half-frames cover what one frame does", () => {
		const whole = momentumStep({ x: 12, y: 0 }, FRAME_MS * 2);
		const a = momentumStep({ x: 12, y: 0 }, FRAME_MS);
		const b = momentumStep(a.velocity, FRAME_MS);
		near(whole.delta.x, a.delta.x + b.delta.x);
		near(whole.velocity.x, b.velocity.x);
	});

	test("the speed shrinks every frame and ends under the stop speed", () => {
		let v = { x: 30, y: 0 };
		let frames = 0;
		let travelled = 0;
		let last = Number.POSITIVE_INFINITY;
		while ((v.x !== 0 || v.y !== 0) && frames < 1000) {
			const s = momentumStep(v, FRAME_MS);
			travelled += s.delta.x;
			expect(Math.abs(v.x)).toBeLessThan(last);
			last = Math.abs(v.x);
			v = s.velocity;
			frames++;
		}
		expect(v).toEqual({ x: 0, y: 0 });
		expect(frames).toBeLessThan(120);
		// Geometric series: v / (1 - friction), less the tail that stopped it.
		expect(travelled).toBeLessThan(30 / (1 - FRICTION));
		expect(travelled).toBeGreaterThan(
			30 / (1 - FRICTION) - (2 * STOP_SPEED) / (1 - FRICTION),
		);
	});

	test("a velocity already under the stop speed stops at once", () => {
		const s = momentumStep({ x: STOP_SPEED * 0.9, y: 0 }, FRAME_MS);
		expect(s.velocity).toEqual({ x: 0, y: 0 });
	});

	test("release velocity is read from the last 100ms, in pixels a frame", () => {
		const samples = [
			{ t: 0, x: 0, y: 0 },
			{ t: 950, x: 500, y: 0 },
			{ t: 1000, x: 520, y: 10 },
			{ t: 1050, x: 560, y: 20 },
		];
		const v = releaseVelocity(samples, 1050);
		// 60px over 100ms between t=950 and t=1050, shown per 16.7ms frame.
		near(v.x, (60 / 100) * FRAME_MS, 3);
		near(v.y, (20 / 100) * FRAME_MS, 3);
	});

	test("a pause before release, or too few samples, carries nothing", () => {
		const moving = [
			{ t: 0, x: 0, y: 0 },
			{ t: 40, x: 80, y: 0 },
		];
		expect(releaseVelocity(moving, 400)).toEqual({ x: 0, y: 0 });
		expect(releaseVelocity(moving.slice(0, 1), 40)).toEqual({ x: 0, y: 0 });
		expect(releaseVelocity([], 40)).toEqual({ x: 0, y: 0 });
	});

	test("a wild flick is capped", () => {
		const v = releaseVelocity(
			[
				{ t: 0, x: 0, y: 0 },
				{ t: 10, x: 4000, y: 0 },
			],
			12,
		);
		near(Math.hypot(v.x, v.y), 60, 6);
	});
});

describe("view to bounds", () => {
	test("a view at rest shows the projection's own bounds", () => {
		const p = grid(europe);
		const b = viewBounds(p, REST_VIEW);
		near(b.north, p.bounds.north);
		near(b.south, p.bounds.south);
		near(b.west, p.bounds.west);
		near(b.east, p.bounds.east);
	});

	test("dragging the map right shows land further west", () => {
		const p = grid(europe);
		const b = viewBounds(p, { x: p.width / 4, y: 0, k: 1 });
		const span = p.bounds.east - p.bounds.west;
		near(b.west, p.bounds.west - span / 4);
		near(b.east, p.bounds.east - span / 4);
	});

	test("dragging the map down shows land further north", () => {
		const p = grid(europe);
		const b = viewBounds(p, { x: 0, y: p.height / 2, k: 1 });
		const span = p.bounds.north - p.bounds.south;
		near(b.north, p.bounds.north + span / 2);
	});

	test("zoomed about a point, that point keeps its latitude and longitude", () => {
		const p = grid(europe);
		const anchor = { x: p.width * 0.3, y: p.height * 0.6 };
		const lonAt = (b: MapBounds, ax: number) =>
			b.west + (ax / p.width) * (b.east - b.west);
		const latAt = (b: MapBounds, ay: number) =>
			b.north - (ay / p.height) * (b.north - b.south);
		const v = zoomAround(REST_VIEW, 2.5, anchor, { min: 0.5, max: 10 });
		const b = viewBounds(p, v);
		near(lonAt(b, anchor.x), lonAt(p.bounds, anchor.x), 6);
		near(latAt(b, anchor.y), latAt(p.bounds, anchor.y), 6);
		near((b.east - b.west) * 2.5, p.bounds.east - p.bounds.west);
	});

	test("the west edge is wrapped into [-180, 180) however far it was dragged", () => {
		const b = normaliseLon({ north: 1, south: 0, west: 725, east: 800 });
		near(b.west, 5);
		near(b.east, 80);
		expect(normaliseLon(europe)).toBe(europe);
		const left = normaliseLon({ north: 1, south: 0, west: -200, east: -100 });
		near(left.west, 160);
	});
});

describe("the glide into a tab from a moved view", () => {
	test("at rest it is the plain view transform", () => {
		const from = grid(europe);
		const to = grid(world);
		const t = viewTransform(from, to);
		const e = enterFrom(REST_VIEW, t, from, to);
		near(e.x, t.x);
		near(e.y, t.y);
		near(e.scaleX, t.scaleX);
	});

	test("a place lands where the moved layer draws it", () => {
		const from = grid(europe);
		const to = grid(REGIONS[2].bounds);
		const t = viewTransform(from, to);
		const view = { x: 37, y: -21, k: 1.7 };
		const e = enterFrom(view, t, from, to);
		// An unscaled point of `to`, drawn by `to` after `e` and by `from` after `view`.
		const p = 123;
		const drawnByTo = to.x + e.x + e.scaleX * p;
		const pFrom = to.x + t.x - from.x + t.scaleX * p;
		const drawnByFrom = from.x + view.x + view.k * pFrom;
		near(drawnByTo, drawnByFrom);
	});
});

describe("keys", () => {
	test("arrows move the map the other way", () => {
		expect(keyAction("ArrowLeft")).toEqual({ type: "pan", x: 1, y: 0 });
		expect(keyAction("ArrowRight")).toEqual({ type: "pan", x: -1, y: 0 });
		expect(keyAction("ArrowUp")).toEqual({ type: "pan", x: 0, y: 1 });
		expect(keyAction("ArrowDown")).toEqual({ type: "pan", x: 0, y: -1 });
	});

	test("+ and = zoom in, - and _ zoom out, 0 fits, the rest are ignored", () => {
		for (const k of ["+", "="]) {
			const a = keyAction(k);
			expect(a?.type === "zoom" && a.factor > 1).toBe(true);
		}
		for (const k of ["-", "_"]) {
			const a = keyAction(k);
			expect(a?.type === "zoom" && a.factor < 1).toBe(true);
		}
		expect(keyAction("0")).toEqual({ type: "fit" });
		expect(keyAction("a")).toBeNull();
		expect(keyAction("Tab")).toBeNull();
	});
});

describe("zoom modifier", () => {
	test("command on Apple platforms, control elsewhere", () => {
		expect(zoomModifier("MacIntel").glyph).toBe("⌘");
		expect(zoomModifier("iPad").glyph).toBe("⌘");
		expect(zoomModifier("Win32")).toEqual({ glyph: "Ctrl", name: "Control" });
		expect(zoomModifier("Linux x86_64").glyph).toBe("Ctrl");
		expect(zoomModifier("").glyph).toBe("Ctrl");
	});
});
