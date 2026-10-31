/**
 * The `band` scene alone. A bundle that imports this ships this scene's
 * field shader and no other (`engine.ts` ships all of them).
 */

import { BAND } from "./band.glsl";
import { type AsciiEngine, type AsciiOptions, createSceneEngine } from "./core";

export type { AsciiEngine, AsciiOptions } from "./core";

export function createBand(
	canvas: HTMLCanvasElement,
	opts: Omit<AsciiOptions, "scene">,
): AsciiEngine | null {
	return createSceneEngine(canvas, { ...opts, scene: "band" }, { band: BAND });
}
