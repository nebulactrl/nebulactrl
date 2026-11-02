import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import type { AsciiEngine } from "./core";
import { DIAL_OUT } from "./dial-out.glsl";
import { DOME } from "./dome.glsl";
import {
	type FakeBrowser,
	FakeCanvas,
	installFakeBrowser,
} from "./fake-browser";
import { LOST_SIGNAL } from "./lost-signal.glsl";
import { NEBULA_PALETTE } from "./palette";
import { createDialOut } from "./scene-dial-out";
import { createDome } from "./scene-dome";
import { createEclipse } from "./scene-eclipse";
import { createLostSignal } from "./scene-lost-signal";
import { resolveScene } from "./scenes";

let browser: FakeBrowser;
let engine: AsciiEngine | null;

beforeEach(() => {
	browser = installFakeBrowser();
	engine = null;
});

afterEach(() => {
	engine?.destroy();
	browser.restore();
});

const shaders = (canvas: FakeCanvas) =>
	canvas.gl.called("shaderSource").map((args) => String(args[1]));

// Each entry point ships exactly one scene, so a bundle built from it carries
// that scene's field shader and no other.
describe.each([
	["dial-out", createDialOut, DIAL_OUT, [DOME, LOST_SIGNAL]],
	["dome", createDome, DOME, [DIAL_OUT, LOST_SIGNAL]],
	["lost-signal", createLostSignal, LOST_SIGNAL, [DIAL_OUT, DOME]],
] as const)("the %s entry point", (name, create, own, others) => {
	test("draws its own scene and none of the others", async () => {
		const canvas = new FakeCanvas();
		engine = create(canvas.asElement(), { palette: NEBULA_PALETTE });
		await engine?.ready;
		const sources = shaders(canvas);
		expect(sources.some((s) => s.includes(own))).toBe(true);
		for (const other of others) {
			expect(sources.some((s) => s.includes(other))).toBe(false);
		}
		// The scene's still frame is the one painted for reduced motion.
		engine?.renderStatic(resolveScene(name).staticTime);
		expect(canvas.gl.uniform("uT")).toEqual([resolveScene(name).staticTime]);
	});

	test("starts nothing without WebGL2", () => {
		const canvas = new FakeCanvas();
		canvas.hasWebgl2 = false;
		expect(create(canvas.asElement(), { palette: NEBULA_PALETTE })).toBeNull();
	});
});

describe("the eclipse entry point", () => {
	test("is the dithered planet, not a glyph scene", async () => {
		const canvas = new FakeCanvas({ width: 400, height: 300 });
		engine = createEclipse(canvas.asElement(), { palette: NEBULA_PALETTE });
		await engine?.ready;
		expect(canvas.gl.uniform("uGrid")).toEqual([200, 150]);
		expect(canvas.gl.uniform("uAtlas")).toBeUndefined();
	});
});
