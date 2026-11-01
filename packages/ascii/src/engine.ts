/**
 * The full engine: every scene (eclipse, dial-out, lost-signal, dome, band).
 * `createAscii(canvas, { scene })` draws one on a canvas. The Eclipse, the
 * default, is the dithered planet (planet.ts); the others are glyph scenes
 * (core.ts for the loop and budgets, shader.ts for the field contract,
 * scenes.ts for the specs).
 */
import { BAND } from "./band.glsl";
import {
	type AsciiEngine,
	type AsciiOptions,
	createSceneEngine,
	type SceneFields,
} from "./core";
import { DIAL_OUT } from "./dial-out.glsl";
import { DOME } from "./dome.glsl";
import { LOST_SIGNAL } from "./lost-signal.glsl";
import { createPlanet } from "./planet";
import { resolveScene } from "./scenes";

export type {
	AsciiEngine,
	AsciiOptions,
	AsciiPalette,
	AsciiStats,
	FocusPoint,
	RGB,
	SceneName,
} from "./core";
export { DEFAULT_FONT_FAMILY } from "./core";

const FIELDS: SceneFields = {
	"dial-out": DIAL_OUT,
	"lost-signal": LOST_SIGNAL,
	dome: DOME,
	band: BAND,
};

/**
 * Creates the backdrop on `canvas`. Returns `null` when WebGL2 is unavailable
 * or the shaders fail, so the caller keeps its non-GL floor.
 */
export function createAscii(
	canvas: HTMLCanvasElement,
	opts: AsciiOptions,
): AsciiEngine | null {
	return resolveScene(opts.scene).name === "eclipse"
		? createPlanet(canvas, opts)
		: createSceneEngine(canvas, opts, FIELDS);
}
