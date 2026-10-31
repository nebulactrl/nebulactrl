import type { AsciiEngine, AsciiOptions } from "@nebulactrl/ascii";
import { NEBULA_PALETTE } from "@nebulactrl/ascii/palette";
import { useEffect, useRef, useState } from "react";
import { textLines } from "./quiet-lines.ts";

/** One scene's creator, from its own entry so a page ships only the scenes it places. */
export type SceneCreator = (
	canvas: HTMLCanvasElement,
	opts: Omit<AsciiOptions, "scene">,
) => AsciiEngine | null;

/**
 * A threshold scene behind a page's own copy. The nearest `[data-ascii-scope]`
 * ancestor is the page's side of the contract:
 *   [data-ascii-quiet]  text drawn straight over the field: the scene keeps
 *                       it under 8% contrast. `="lines"` marks a paragraph:
 *                       each line is its own region, so a short last line
 *                       leaves the space beside it to the scene
 *   [data-ascii-panel]  the block the scene frames (lost signal opens its gap
 *                       around it)
 * The scope also gets `data-ascii="on"` once a frame is painted, and back to
 * "off" on context loss, so a halftone floor under the scene can stand down.
 *
 * The island hydrates when its box first scrolls into view, so the engine is
 * created on first view. It pauses itself off screen and in hidden tabs, paints
 * one still frame under reduced motion and is destroyed on unmount. The server
 * markup is an empty layer (opacity 0); the page's halftone floor is what shows
 * without script or WebGL2. Decoration: `aria-hidden`, no pointer events, gone
 * under forced colours.
 */
export function AsciiScene({
	scene,
	create,
}: {
	scene: string;
	create: SceneCreator;
}) {
	const layer = useRef<HTMLDivElement>(null);
	const canvas = useRef<HTMLCanvasElement>(null);
	const [status, setStatus] = useState<"on" | "off">("off");

	useEffect(() => {
		const el = canvas.current;
		const box = layer.current;
		if (!el || !box) return;
		const scope = box.closest<HTMLElement>("[data-ascii-scope]") ?? box;
		const live: {
			engine: AsciiEngine | null;
			watch: ResizeObserver | null;
			unfonts: (() => void) | null;
		} = { engine: null, watch: null, unfonts: null };
		const engine = create(el, {
			palette: NEBULA_PALETTE,
			maskRect: () =>
				scope.querySelector("[data-ascii-panel]")?.getBoundingClientRect() ??
				null,
			quietRects: () =>
				Array.from(scope.querySelectorAll<HTMLElement>("[data-ascii-quiet]"))
					.flatMap((node) =>
						node.dataset.asciiQuiet === "lines"
							? textLines(node)
							: [node.getBoundingClientRect()],
					)
					.filter((rect) => rect.right > rect.left),
			onStatus: (next) => {
				const on = next === "ready" ? "on" : "off";
				setStatus(on);
				// The page's own halftone floor reads this off the scope (`.ht-rim[data-ascii="on"]`).
				scope.dataset.ascii = on;
			},
		});
		if (engine) {
			live.engine = engine;
			engine.start();
			// Webfonts and wrapping move the copy after first paint: a loaded font
			// can change where a line ends without changing its box.
			const fonts =
				typeof document === "undefined" ? undefined : document.fonts;
			if (fonts) {
				const again = () => engine.resize();
				void fonts.ready.then(again);
				fonts.addEventListener("loadingdone", again);
				live.unfonts = () => fonts.removeEventListener("loadingdone", again);
			}
			if (typeof ResizeObserver === "function") {
				live.watch = new ResizeObserver(() => engine.resize());
				live.watch.observe(scope);
				for (const node of scope.querySelectorAll(
					"[data-ascii-quiet],[data-ascii-panel]",
				)) {
					live.watch.observe(node);
				}
			}
		}
		return () => {
			live.watch?.disconnect();
			live.unfonts?.();
			live.engine?.destroy();
			if (scope !== box) delete scope.dataset.ascii;
		};
	}, [create]);

	return (
		<div
			ref={layer}
			aria-hidden="true"
			data-ascii={status}
			data-scene={scene}
			className="ascii-scene pointer-events-none size-full opacity-0 data-[ascii=on]:opacity-100 motion-safe:transition-opacity motion-safe:duration-500 forced-colors:hidden"
		>
			<canvas ref={canvas} className="block size-full" />
		</div>
	);
}
