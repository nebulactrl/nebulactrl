import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import type { AsciiEngine, AsciiOptions } from "./core";
import {
	createFakeGl,
	type FakeBrowser,
	FakeCanvas,
	installFakeBrowser,
} from "./fake-browser";
import { NEBULA_PALETTE } from "./palette";
import { createPlanet, planetTones } from "./planet";

let browser: FakeBrowser;
let engines: AsciiEngine[];

beforeEach(() => {
	browser = installFakeBrowser();
	engines = [];
});

afterEach(() => {
	for (const engine of engines) engine.destroy();
	browser.restore();
});

function make(
	canvas: FakeCanvas,
	options: Partial<Omit<AsciiOptions, "scene">> = {},
): AsciiEngine {
	const engine = createPlanet(canvas.asElement(), {
		palette: NEBULA_PALETTE,
		...options,
	});
	if (!engine) throw new Error("the planet did not start");
	engines.push(engine);
	return engine;
}

const draws = (canvas: FakeCanvas) => canvas.gl.called("drawArrays").length;

function pointerAt(x: number, y: number) {
	const event = new Event("pointermove");
	Object.assign(event, { clientX: x, clientY: y });
	browser.window.dispatchEvent(event);
}

describe("starting the Eclipse", () => {
	test("a browser without WebGL2 gets no planet", () => {
		const canvas = new FakeCanvas();
		canvas.hasWebgl2 = false;
		expect(
			createPlanet(canvas.asElement(), { palette: NEBULA_PALETTE }),
		).toBeNull();
	});

	test("a shader that does not compile gets no planet and a warning", () => {
		const warn = spyOn(console, "warn").mockImplementation(() => {});
		const canvas = new FakeCanvas({}, createFakeGl({ compiles: false }));
		expect(
			createPlanet(canvas.asElement(), { palette: NEBULA_PALETTE }),
		).toBeNull();
		expect(warn.mock.calls[0]?.[0]).toBe("ascii backdrop disabled:");
		warn.mockRestore();
	});

	test("paints its first frame once ready, with a cell of 2 CSS px, and reports ready", async () => {
		const statuses: string[] = [];
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas, { onStatus: (s) => statuses.push(s) });
		expect(statuses).toEqual([]);
		await engine.ready;
		expect(statuses).toEqual(["ready"]);
		expect(draws(canvas)).toBe(2);
		expect(canvas.gl.uniform("uGrid")).toEqual([400, 300]);
		expect(canvas.gl.uniform("uCell")).toEqual([2]);
		expect([canvas.width, canvas.height]).toEqual([800, 600]);
	});

	test("a retina canvas doubles the device pixels but keeps the grid", async () => {
		browser.window.devicePixelRatio = 2;
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		await make(canvas).ready;
		expect([canvas.width, canvas.height]).toEqual([1600, 1200]);
		expect(canvas.gl.uniform("uGrid")).toEqual([400, 300]);
		expect(canvas.gl.uniform("uCell")).toEqual([4]);
	});

	test("the tone uniforms come from the palette", async () => {
		const palette = {
			...NEBULA_PALETTE,
			accent: [200, 0, 0] as [number, number, number],
			accentInk: [255, 255, 0] as [number, number, number],
		};
		const canvas = new FakeCanvas();
		await make(canvas, { palette }).ready;
		const tones = planetTones(palette);
		expect(canvas.gl.uniform("uC1")).toEqual([...tones.c1]);
		expect(canvas.gl.uniform("uC2")).toEqual([1, 1, 0]);
	});

	test("a panel over the field moves the orb away from it", async () => {
		const bare = new FakeCanvas({ width: 1200, height: 700 });
		await make(bare).ready;
		const covered = new FakeCanvas({ width: 1200, height: 700 });
		await make(covered, {
			maskRect: () => ({ left: 700, top: 100, right: 1100, bottom: 600 }),
		}).ready;
		const orb = (c: FakeCanvas) => c.gl.uniform("uOrb") as number[];
		expect(orb(covered)).not.toEqual(orb(bare));
	});

	test("text drawn over the field (quiet rects) is re-read on every rebuild", async () => {
		const reads: number[] = [];
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas, {
			quietRects: () => {
				reads.push(reads.length);
				return [{ left: 20, top: 20, right: 200, bottom: 80 }];
			},
		});
		await engine.ready;
		const first = reads.length;
		expect(first).toBeGreaterThan(0);
		engine.resize();
		browser.advance(120);
		expect(reads.length).toBeGreaterThan(first);
	});

	test("a canvas with no size paints nothing and reports nothing", async () => {
		const statuses: string[] = [];
		const canvas = new FakeCanvas({ width: 0, height: 0 });
		await make(canvas, { onStatus: (s) => statuses.push(s) }).ready;
		expect(statuses).toEqual([]);
		expect(draws(canvas)).toBe(0);
	});
});

describe("the Eclipse's frames", () => {
	test("renderStatic paints the resting light at the requested time", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		const before = draws(canvas);
		engine.renderStatic(9);
		expect(draws(canvas)).toBe(before + 2);
		expect(canvas.gl.uniform("uT")).toEqual([9]);
		expect(canvas.gl.uniform("uCurW")).toEqual([0]);
		expect(browser.frames).toHaveLength(0);
	});

	test("under reduced motion start paints a still and the pointer is ignored", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas, { reducedMotion: true, staticTime: 4 });
		await engine.ready;
		engine.start();
		expect(canvas.gl.uniform("uT")).toEqual([4]);
		expect(browser.frames).toHaveLength(0);
		pointerAt(400, 300);
		engine.renderStatic(4);
		expect(canvas.gl.uniform("uCurW")).toEqual([0]);
	});

	test("animates at the Eclipse's 30 fps, so a 20 ms gap is skipped and a 34 ms one drawn", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas, { staticTime: 1 });
		await engine.ready;
		const start = performance.now();
		engine.start();
		const base = draws(canvas);
		browser.frame(start + 20);
		expect(draws(canvas)).toBe(base);
		browser.frame(start + 34);
		expect(draws(canvas)).toBe(base + 2);
		expect(canvas.gl.uniform("uT")?.[0] as number).toBeCloseTo(1.034, 2);
	});

	test("a cursor over the canvas lights the planet from there, easing toward it", async () => {
		const canvas = new FakeCanvas({ width: 1000, height: 500 });
		const engine = make(canvas);
		await engine.ready;
		const start = performance.now();
		engine.start();
		// x = 25 %, y = 75 % up.
		pointerAt(250, 125);
		browser.frame(start + 100);
		const [x, y] = canvas.gl.uniform("uCur") as number[];
		const [w] = canvas.gl.uniform("uCurW") as number[];
		// Eased from the resting light (0.7, 0.6) toward (0.25, 0.75), not snapped to it.
		expect(x).toBeLessThan(0.7);
		expect(x).toBeGreaterThan(0.25);
		expect(y).toBeGreaterThan(0.6);
		expect(y).toBeLessThan(0.75);
		expect(w).toBeGreaterThan(0);
		expect(w).toBeLessThan(1);
		for (let i = 2; i < 80; i++) {
			browser.frame(start + 100 + i * 40);
		}
		const [fx] = canvas.gl.uniform("uCur") as number[];
		expect(fx).toBeCloseTo(0.25, 1);
	});

	test("a cursor outside the canvas lets the light fade, and leaving the page does too", async () => {
		const canvas = new FakeCanvas({ width: 1000, height: 500 });
		const engine = make(canvas);
		await engine.ready;
		const start = performance.now();
		engine.start();
		pointerAt(500, 250);
		for (let i = 1; i <= 30; i++) browser.frame(start + i * 40);
		const lit = (canvas.gl.uniform("uCurW") as number[])[0] as number;
		expect(lit).toBeGreaterThan(0.5);

		pointerAt(5000, 5000);
		for (let i = 31; i <= 60; i++) browser.frame(start + i * 40);
		const faded = (canvas.gl.uniform("uCurW") as number[])[0] as number;
		expect(faded).toBeLessThan(lit);

		pointerAt(500, 250);
		for (let i = 61; i <= 90; i++) browser.frame(start + i * 40);
		browser.document.documentElement.dispatchEvent(new Event("pointerleave"));
		for (let i = 91; i <= 150; i++) browser.frame(start + i * 40);
		expect((canvas.gl.uniform("uCurW") as number[])[0] as number).toBeLessThan(
			0.1,
		);
	});

	test("a hidden tab or an off-screen canvas pauses the loop", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.start();
		browser.document.hide(true);
		expect(browser.frames).toHaveLength(0);
		browser.document.hide(false);
		expect(browser.frames).toHaveLength(1);
		browser.intersectionObservers[0]?.callback([{ isIntersecting: false }]);
		expect(browser.frames).toHaveLength(0);
		browser.intersectionObservers[0]?.callback([{ isIntersecting: true }]);
		expect(browser.frames).toHaveLength(1);
	});

	test("follows prefers-reduced-motion live", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.start();
		expect(browser.frames).toHaveLength(1);
		browser.media("(prefers-reduced-motion: reduce)").set(true);
		expect(browser.frames).toHaveLength(0);
	});

	test("stats report the frames drawn and the grid", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas, { stats: true });
		await engine.ready;
		expect(engine.stats()).toBeNull();
		const start = performance.now();
		engine.start();
		for (let i = 1; i <= 5; i++) browser.frame(start + i * 40);
		const stats = engine.stats();
		expect(stats?.frames).toBe(5);
		expect(stats?.grid).toBe("400x300");
		expect(stats?.cell).toBe("2x2@1");
	});
});

describe("resizing and losing the context", () => {
	test("a real size change rebuilds the grid once after 120 ms", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		await make(canvas).ready;
		canvas.resizeTo(400, 200);
		browser.resizeObservers[0]?.callback([]);
		browser.advance(119);
		expect(canvas.gl.uniform("uGrid")).toEqual([400, 300]);
		browser.advance(1);
		expect(canvas.gl.uniform("uGrid")).toEqual([200, 100]);
	});

	test("an unchanged size does not rebuild", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		await make(canvas).ready;
		browser.resizeObservers[0]?.callback([]);
		expect(browser.pendingTimers()).toEqual([]);
	});

	test("a lost context reports lost and stops; a restored one rebuilds and reports ready", async () => {
		const statuses: string[] = [];
		const canvas = new FakeCanvas();
		const engine = make(canvas, { onStatus: (s) => statuses.push(s) });
		await engine.ready;
		engine.start();
		const lost = new Event("webglcontextlost", { cancelable: true });
		canvas.dispatchEvent(lost);
		expect(lost.defaultPrevented).toBe(true);
		expect(browser.frames).toHaveLength(0);
		const before = draws(canvas);
		engine.renderStatic(1);
		expect(draws(canvas)).toBe(before);

		canvas.dispatchEvent(new Event("webglcontextrestored"));
		expect(statuses).toEqual(["ready", "lost", "ready"]);
		expect(draws(canvas)).toBeGreaterThan(before);
	});

	test("a context that cannot be rebuilt stays lost", async () => {
		const warn = spyOn(console, "warn").mockImplementation(() => {});
		const statuses: string[] = [];
		const canvas = new FakeCanvas();
		await make(canvas, { onStatus: (s) => statuses.push(s) }).ready;
		canvas.dispatchEvent(new Event("webglcontextlost", { cancelable: true }));
		canvas.gl.setCompiles(false);
		canvas.dispatchEvent(new Event("webglcontextrestored"));
		expect(statuses).toEqual(["ready", "lost", "lost"]);
		warn.mockRestore();
	});

	test("destroy frees the GL objects, detaches every listener and is idempotent", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.start();
		engine.destroy();
		engine.destroy();
		expect(canvas.gl.called("deleteProgram")).toHaveLength(2);
		expect(canvas.gl.called("deleteTexture")).toHaveLength(2);
		expect(canvas.gl.called("deleteFramebuffer")).toHaveLength(1);
		expect(canvas.gl.called("loseContext")).toHaveLength(1);
		expect(browser.frames).toHaveLength(0);
		expect(browser.resizeObservers[0]?.disconnected).toBe(true);

		const before = draws(canvas);
		pointerAt(10, 10);
		browser.document.hide(false);
		engine.start();
		engine.renderStatic(3);
		expect(draws(canvas)).toBe(before);
	});

	test("destroyed before it was ready, it never paints", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		engine.destroy();
		await engine.ready;
		expect(draws(canvas)).toBe(0);
	});
});
