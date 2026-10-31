/**
 * The `eclipse` scene alone. A bundle that imports this ships this scene's
 * field shader and no other (`engine.ts` ships all of them).
 */
import { type AsciiEngine, type AsciiOptions, createSceneEngine } from "./core";
import { ECLIPSE } from "./eclipse.glsl";

export type { AsciiEngine, AsciiOptions } from "./core";

export function createEclipse(
	canvas: HTMLCanvasElement,
	opts: Omit<AsciiOptions, "scene">,
): AsciiEngine | null {
	return createSceneEngine(
		canvas,
		{ ...opts, scene: "eclipse" },
		{ eclipse: ECLIPSE },
	);
}
