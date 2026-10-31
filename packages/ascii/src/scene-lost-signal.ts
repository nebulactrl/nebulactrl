/**
 * The `lost-signal` scene alone. A bundle that imports this ships this scene's
 * field shader and no other (`engine.ts` ships all of them).
 */
import { type AsciiEngine, type AsciiOptions, createSceneEngine } from "./core";
import { LOST_SIGNAL } from "./lost-signal.glsl";

export type { AsciiEngine, AsciiOptions } from "./core";

export function createLostSignal(
	canvas: HTMLCanvasElement,
	opts: Omit<AsciiOptions, "scene">,
): AsciiEngine | null {
	return createSceneEngine(
		canvas,
		{ ...opts, scene: "lost-signal" },
		{ "lost-signal": LOST_SIGNAL },
	);
}
