import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { BAND } from "./band.glsl";
import {
	type AsciiEngine,
	type AsciiOptions,
	createSceneEngine,
	DEFAULT_FONT_FAMILY,
	link,
} from "./core";
import { createAscii } from "./engine";
import {
	createFakeGl,
	type FakeBrowser,
	FakeCanvas,
	installFakeBrowser,
} from "./fake-browser";
import { NEBULA_PALETTE } from "./palette";
import { createBand } from "./scene-band";
import { resolveScene } from "./scenes";

const FIELDS = {
	band: "vec4 field(ivec2 c, vec2 p, float t){ return vec4(0.0); }",
};

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
	options: Partial<AsciiOptions> = {},
): AsciiEngine {
	const engine = createSceneEngine(
		canvas.asElement(),
		{ scene: "band", palette: NEBULA_PALETTE, ...options },
		FIELDS,
	);
	if (!engine) throw new Error("the engine did not start");
	engines.push(engine);
	return engine;
}

const draws = (canvas: FakeCanvas) => canvas.gl.called("drawArrays").length;

describe("starting", () => {
	test("asks for a transparent, non-antialiased low-power WebGL2 context", () => {
		const canvas = new FakeCanvas();
		make(canvas);
		expect(canvas.contextRequests[0]).toEqual([
			"webgl2",
			{
				alpha: true,
				premultipliedAlpha: true,
				antialias: false,
				depth: false,
				stencil: false,
				powerPreference: "low-power",
				preserveDrawingBuffer: false,
			},
		]);
	});

	test("a browser without WebGL2 gets no engine, so the page keeps its fallback", () => {
		const canvas = new FakeCanvas();
		canvas.hasWebgl2 = false;
		expect(
			createSceneEngine(
				canvas.asElement(),
				{ scene: "band", palette: NEBULA_PALETTE },
				FIELDS,
			),
		).toBeNull();
	});

	test("a scene this bundle does not ship gets no engine and a warning", () => {
		const warn = spyOn(console, "warn").mockImplementation(() => {});
		const canvas = new FakeCanvas();
		expect(
			createSceneEngine(
				canvas.asElement(),
				{ scene: "dome", palette: NEBULA_PALETTE },
				FIELDS,
			),
		).toBeNull();
		expect(warn).toHaveBeenCalledWith(
			'ascii scene "dome" is not in this bundle',
		);
		expect(canvas.contextRequests).toEqual([]);
		warn.mockRestore();
	});

	test("a shader that does not compile gets no engine and a warning that names the log", () => {
		const warn = spyOn(console, "warn").mockImplementation(() => {});
		const canvas = new FakeCanvas({}, createFakeGl({ compiles: false }));
		expect(
			createSceneEngine(
				canvas.asElement(),
				{ scene: "band", palette: NEBULA_PALETTE },
				FIELDS,
			),
		).toBeNull();
		expect(warn.mock.calls[0]?.[0]).toBe("ascii backdrop disabled:");
		expect(String(warn.mock.calls[0]?.[1])).toContain("fake compile error");
		warn.mockRestore();
	});

	test("the scene's own field body is spliced into the field shader", () => {
		const canvas = new FakeCanvas();
		make(canvas);
		const sources = canvas.gl
			.called("shaderSource")
			.map((args) => String(args[1]));
		expect(sources.some((s) => s.includes(FIELDS.band))).toBe(true);
		expect(sources.some((s) => s.includes("uniform sampler2D uAtlas"))).toBe(
			true,
		);
	});

	test("the first frame is painted when ready, and onStatus says so once", async () => {
		const statuses: string[] = [];
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas, { onStatus: (s) => statuses.push(s) });
		await engine.ready;
		expect(statuses).toEqual(["ready"]);
		// A field pass and a blit pass.
		expect(draws(canvas)).toBe(2);
	});

	test("the canvas backing store is sized in device pixels, capped by dprCap", async () => {
		browser.window.devicePixelRatio = 3;
		const canvas = new FakeCanvas({ width: 400, height: 200 });
		await make(canvas, { dprCap: 2 }).ready;
		expect([canvas.width, canvas.height]).toEqual([800, 400]);
		expect(canvas.gl.uniform("uRes")).toEqual([800, 400]);
	});

	test("text drawn over the field (quiet rects) is re-read on every rebuild", async () => {
		let reads = 0;
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas, {
			quietRects: () => {
				reads++;
				return [{ left: 20, top: 20, right: 200, bottom: 80 }];
			},
		});
		await engine.ready;
		const first = reads;
		expect(first).toBeGreaterThan(0);
		engine.resize();
		browser.advance(120);
		expect(reads).toBeGreaterThan(first);
	});

	test("a canvas with no size paints nothing and reports nothing", async () => {
		const statuses: string[] = [];
		const canvas = new FakeCanvas({ width: 0, height: 0 });
		await make(canvas, { onStatus: (s) => statuses.push(s) }).ready;
		expect(statuses).toEqual([]);
		expect(draws(canvas)).toBe(0);
	});

	test("the grid honours maxCells, given as a number or as a function of the canvas size", async () => {
		const small = new FakeCanvas({ width: 800, height: 600 });
		await make(small, { maxCells: 500 }).ready;
		const [cols, rows] = small.gl.uniform("uGrid") as number[];
		expect((cols as number) * (rows as number)).toBeLessThanOrEqual(500);

		const seen: Array<[number, number]> = [];
		const dynamic = new FakeCanvas({ width: 640, height: 480 });
		await make(dynamic, {
			maxCells: (w, h) => {
				seen.push([w, h]);
				return 300;
			},
		}).ready;
		expect(seen[0]).toEqual([640, 480]);
		const [c2, r2] = dynamic.gl.uniform("uGrid") as number[];
		expect((c2 as number) * (r2 as number)).toBeLessThanOrEqual(300);
	});

	test("the atlas holds one column per ramp glyph and extra, in three hues, in the requested font", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		await make(canvas, { fontFamily: "Fira Code" }).ready;
		// The calibration probe is 40 px square; the atlas is the widest canvas made.
		const atlas = [...browser.created].sort((a, b) => b.width - a.width)[0];
		expect(atlas).toBeDefined();
		const twoD = atlas?.twoD;
		expect(twoD?.font).toContain("Fira Code");
		// Three hues stacked: the atlas is three cell-heights tall.
		const grid = canvas.gl.uniform("uCell") as number[];
		expect(atlas?.height).toBe((grid[1] as number) * 3);
		expect((atlas?.width ?? 0) % (grid[0] as number)).toBe(0);
		expect(twoD?.filled.length).toBeGreaterThan(0);
	});

	test("the default font stack is a monospace one", () => {
		expect(DEFAULT_FONT_FAMILY).toContain("monospace");
	});

	test("without a 2D context the glyphs are taken in candidate order and the scene still starts", async () => {
		browser.document.has2d = false;
		const statuses: string[] = [];
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		await make(canvas, { onStatus: (s) => statuses.push(s) }).ready;
		expect(statuses).toEqual(["ready"]);
		expect(draws(canvas)).toBe(2);
	});

	test("waits for the font, and gives up after 2.5 s", async () => {
		const loading = Promise.withResolvers<void>();
		browser.document.fonts = { load: () => loading.promise };
		const statuses: string[] = [];
		const canvas = new FakeCanvas();
		const engine = make(canvas, { onStatus: (s) => statuses.push(s) });
		await Promise.resolve();
		expect(statuses).toEqual([]);
		expect(draws(canvas)).toBe(0);
		browser.advance(2499);
		await Promise.resolve();
		expect(draws(canvas)).toBe(0);
		browser.advance(1);
		await engine.ready;
		expect(statuses).toEqual(["ready"]);
		expect(draws(canvas)).toBe(2);
	});

	test("a font that fails to load still leaves the fallback stack", async () => {
		browser.document.fonts = {
			load: () => Promise.reject(new Error("no font")),
		};
		const statuses: string[] = [];
		const canvas = new FakeCanvas();
		await make(canvas, { onStatus: (s) => statuses.push(s) }).ready;
		expect(statuses).toEqual(["ready"]);
	});
});

describe("static frames and reduced motion", () => {
	test("renderStatic paints one frame at the requested shader time and does not animate", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		const before = draws(canvas);
		engine.renderStatic(7.5);
		expect(draws(canvas)).toBe(before + 2);
		expect(canvas.gl.uniform("uT")).toEqual([7.5]);
		expect(browser.frames).toHaveLength(0);
	});

	test("start under reduced motion paints the scene's still instead of animating", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas, { reducedMotion: true, staticTime: 3 });
		await engine.ready;
		engine.start();
		expect(canvas.gl.uniform("uT")).toEqual([3]);
		expect(browser.frames).toHaveLength(0);
	});

	test("the scene's own still time is used when none is given", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas, { reducedMotion: true });
		await engine.ready;
		engine.start();
		expect(canvas.gl.uniform("uT")).toEqual([resolveScene("band").staticTime]);
	});

	test("without an explicit setting it follows prefers-reduced-motion live", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.start();
		expect(browser.frames).toHaveLength(1);
		const before = draws(canvas);
		browser.media("(prefers-reduced-motion: reduce)").set(true);
		expect(browser.frames).toHaveLength(0);
		expect(draws(canvas)).toBe(before + 2);
	});
});

describe("the animation loop", () => {
	test("draws a frame per due tick, advancing shader time by the elapsed seconds", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas, { fps: 10, staticTime: 5 });
		await engine.ready;
		const start = performance.now();
		engine.start();
		const base = draws(canvas);

		browser.frame(start + 100);
		expect(draws(canvas)).toBe(base + 2);
		expect(canvas.gl.uniform("uT")?.[0] as number).toBeCloseTo(5.1, 1);

		// 50 ms later is not due at 10 fps: the frame is requested again, nothing drawn.
		browser.frame(start + 150);
		expect(draws(canvas)).toBe(base + 2);
		expect(browser.frames).toHaveLength(1);

		browser.frame(start + 200);
		expect(draws(canvas)).toBe(base + 4);
	});

	test("a long pause does not make time jump: one tick advances at most a quarter second", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas, { fps: 10, staticTime: 0 });
		await engine.ready;
		const start = performance.now();
		engine.start();
		browser.frame(start + 60_000);
		expect(canvas.gl.uniform("uT")?.[0]).toBe(0.25);
	});

	test("start is idempotent and stop ends the loop", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.start();
		engine.start();
		expect(browser.frames).toHaveLength(1);
		engine.stop();
		expect(browser.frames).toHaveLength(0);
	});

	test("start before the first build waits for it", async () => {
		const loading = Promise.withResolvers<void>();
		browser.document.fonts = { load: () => loading.promise };
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		engine.start();
		expect(browser.frames).toHaveLength(0);
		loading.resolve();
		await engine.ready;
		expect(browser.frames).toHaveLength(1);
	});

	test("a hidden tab pauses the loop and coming back resumes it", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.start();
		browser.document.hide(true);
		expect(browser.frames).toHaveLength(0);
		browser.document.hide(false);
		expect(browser.frames).toHaveLength(1);
	});

	test("a canvas scrolled out of view pauses the loop and back in resumes it", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.start();
		const [view] = browser.intersectionObservers;
		expect(view?.observed).toEqual([canvas]);
		view?.callback([{ isIntersecting: false }]);
		expect(browser.frames).toHaveLength(0);
		view?.callback([{ isIntersecting: true }]);
		expect(browser.frames).toHaveLength(1);
	});
});

describe("stats", () => {
	test("are absent unless asked for", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		expect(engine.stats()).toBeNull();
	});

	test("report frame cost and the grid once a few frames were drawn", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas, { stats: true, fps: 10 });
		await engine.ready;
		expect(engine.stats()).toBeNull();
		const start = performance.now();
		engine.start();
		for (let i = 1; i <= 4; i++) browser.frame(start + i * 100);
		const stats = engine.stats();
		expect(stats?.frames).toBe(4);
		const [cols, rows] = canvas.gl.uniform("uGrid") as number[];
		expect(stats?.grid).toBe(`${cols}x${rows}`);
		const [cw, ch] = canvas.gl.uniform("uCell") as number[];
		expect(stats?.cell).toBe(`${cw}x${ch}@1`);
		expect(stats?.maxMs).toBeGreaterThanOrEqual(stats?.meanMs ?? 0);
		expect(stats?.p95Ms).toBeLessThanOrEqual(stats?.maxMs ?? 0);
	});
});

describe("resizing", () => {
	test("a real size change rebuilds once, 120 ms after the last notice", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas);
		await engine.ready;
		const [observer] = browser.resizeObservers;
		expect(observer?.observed).toEqual([canvas]);

		canvas.resizeTo(400, 300);
		observer?.callback([]);
		browser.advance(100);
		observer?.callback([]);
		browser.advance(119);
		expect(canvas.width).toBe(800);
		browser.advance(1);
		expect([canvas.width, canvas.height]).toEqual([400, 300]);
		expect(canvas.gl.uniform("uRes")).toEqual([400, 300]);
		void engine;
	});

	test("a notice with an unchanged size does nothing", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		await make(canvas).ready;
		browser.resizeObservers[0]?.callback([]);
		expect(browser.pendingTimers()).toEqual([]);
	});

	test("resize() re-measures even when the observer stays silent", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas);
		await engine.ready;
		canvas.resizeTo(1000, 500);
		engine.resize();
		browser.advance(120);
		expect([canvas.width, canvas.height]).toEqual([1000, 500]);
	});

	test("a pixel-ratio change rebuilds at the new ratio", async () => {
		const canvas = new FakeCanvas({ width: 400, height: 300 });
		await make(canvas).ready;
		expect(canvas.width).toBe(400);
		browser.window.devicePixelRatio = 2;
		browser.media("(resolution: 1dppx)").dispatchEvent(new Event("change"));
		browser.advance(120);
		expect(canvas.width).toBe(800);
	});

	test("the panel mask is re-read on every rebuild", async () => {
		let reads = 0;
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas, {
			maskRect: () => {
				reads++;
				return { left: 100, top: 100, right: 300, bottom: 300 };
			},
		});
		await engine.ready;
		const first = reads;
		expect(first).toBeGreaterThan(0);
		engine.resize();
		browser.advance(120);
		expect(reads).toBeGreaterThan(first);
	});

	test("a resize to nothing then back paints again", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas);
		await engine.ready;
		canvas.resizeTo(0, 0);
		engine.resize();
		browser.advance(120);
		const before = draws(canvas);
		canvas.resizeTo(800, 600);
		engine.resize();
		browser.advance(120);
		expect(draws(canvas)).toBe(before + 2);
	});
});

describe("losing the GL context", () => {
	test("reports lost, keeps the browser from discarding it for good, and stops painting", async () => {
		const statuses: string[] = [];
		const canvas = new FakeCanvas();
		const engine = make(canvas, { onStatus: (s) => statuses.push(s) });
		await engine.ready;
		engine.start();
		const event = new Event("webglcontextlost", { cancelable: true });
		canvas.dispatchEvent(event);
		expect(event.defaultPrevented).toBe(true);
		expect(statuses).toEqual(["ready", "lost"]);
		expect(browser.frames).toHaveLength(0);
		const before = draws(canvas);
		engine.renderStatic(1);
		expect(draws(canvas)).toBe(before);
	});

	test("a restored context is rebuilt and reported ready", async () => {
		const statuses: string[] = [];
		const canvas = new FakeCanvas();
		const engine = make(canvas, { onStatus: (s) => statuses.push(s) });
		await engine.ready;
		canvas.dispatchEvent(new Event("webglcontextlost", { cancelable: true }));
		const programsBefore = canvas.gl.called("createProgram").length;
		canvas.dispatchEvent(new Event("webglcontextrestored"));
		expect(canvas.gl.called("createProgram").length).toBe(programsBefore + 2);
		expect(statuses).toEqual(["ready", "lost", "ready"]);
		const before = draws(canvas);
		engine.start();
		expect(browser.frames).toHaveLength(1);
		expect(draws(canvas)).toBe(before);
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
		expect(warn.mock.calls[0]?.[0]).toBe("ascii backdrop could not recover:");
		warn.mockRestore();
	});
});

describe("destroy", () => {
	test("frees every GL object, releases the context and stops listening", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.start();
		engine.destroy();
		expect(canvas.gl.called("deleteProgram")).toHaveLength(2);
		expect(canvas.gl.called("deleteTexture")).toHaveLength(3);
		expect(canvas.gl.called("deleteFramebuffer")).toHaveLength(1);
		expect(canvas.gl.called("deleteVertexArray")).toHaveLength(1);
		expect(canvas.gl.called("loseContext")).toHaveLength(1);
		expect(browser.frames).toHaveLength(0);
		expect(browser.resizeObservers[0]?.disconnected).toBe(true);
		expect(browser.intersectionObservers[0]?.disconnected).toBe(true);

		// Nothing the page does afterwards reaches the engine.
		const before = draws(canvas);
		browser.document.hide(false);
		canvas.dispatchEvent(new Event("webglcontextlost", { cancelable: true }));
		engine.start();
		engine.renderStatic(2);
		expect(draws(canvas)).toBe(before);
	});

	test("is safe to call twice", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		engine.destroy();
		engine.destroy();
		expect(canvas.gl.called("deleteProgram")).toHaveLength(2);
	});

	test("cancels a pending rebuild", async () => {
		const canvas = new FakeCanvas({ width: 800, height: 600 });
		const engine = make(canvas);
		await engine.ready;
		canvas.resizeTo(100, 100);
		engine.resize();
		engine.destroy();
		browser.advance(500);
		expect(canvas.width).toBe(800);
	});

	test("a destroyed-before-ready engine never paints", async () => {
		const loading = Promise.withResolvers<void>();
		browser.document.fonts = { load: () => loading.promise };
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		engine.destroy();
		loading.resolve();
		await engine.ready;
		expect(draws(canvas)).toBe(0);
	});

	test("a lost context is not released a second time", async () => {
		const canvas = new FakeCanvas();
		const engine = make(canvas);
		await engine.ready;
		canvas.dispatchEvent(new Event("webglcontextlost", { cancelable: true }));
		engine.destroy();
		expect(canvas.gl.called("deleteProgram")).toHaveLength(0);
	});
});

describe("the scene entry points", () => {
	test("createAscii starts a glyph scene through the full engine", async () => {
		const canvas = new FakeCanvas();
		const engine = createAscii(canvas.asElement(), {
			scene: "dome",
			palette: NEBULA_PALETTE,
		});
		expect(engine).not.toBeNull();
		await engine?.ready;
		expect(draws(canvas)).toBe(2);
		engine?.destroy();
	});

	test("createAscii hands the Eclipse to the planet renderer", async () => {
		const canvas = new FakeCanvas();
		const engine = createAscii(canvas.asElement(), { palette: NEBULA_PALETTE });
		await engine?.ready;
		// The planet draws one field pass and one blit, and has no glyph atlas.
		expect(canvas.gl.called("uniform3f").length).toBeGreaterThan(0);
		expect(canvas.gl.uniform("uC2")).toBeDefined();
		engine?.destroy();
	});

	test("createBand ships only the band scene", async () => {
		const canvas = new FakeCanvas();
		const engine = createBand(canvas.asElement(), { palette: NEBULA_PALETTE });
		await engine?.ready;
		const sources = canvas.gl
			.called("shaderSource")
			.map((args) => String(args[1]));
		expect(sources.some((s) => s.includes(BAND))).toBe(true);
		engine?.destroy();
	});

	test("link throws the driver's log when a program does not link", () => {
		const fake = createFakeGl();
		fake.gl.createShader(fake.gl.VERTEX_SHADER);
		fake.setCompiles(false);
		expect(() => link(fake.gl, "v", "f")).toThrow("fake compile error");
	});
});
