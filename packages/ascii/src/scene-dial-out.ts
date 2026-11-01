/**
 * The `dial-out` scene alone. A bundle that imports this ships this scene's
 * field shader and no other (`engine.ts` ships all of them).
 */
import { type AsciiEngine, type AsciiOptions, createSceneEngine } from "./core";
import { DIAL_OUT } from "./dial-out.glsl";

export type { AsciiEngine, AsciiOptions } from "./core";

export function createDialOut(
	canvas: HTMLCanvasElement,
	opts: Omit<AsciiOptions, "scene">,
): AsciiEngine | null {
	return createSceneEngine(
		canvas,
		{ ...opts, scene: "dial-out" },
		{ "dial-out": DIAL_OUT },
	);
}
