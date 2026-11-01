import type { CanvasLayout } from "#/api/models";
import { isFullScreen } from "../inspector/geometry";

type Viewport = NonNullable<CanvasLayout["viewport"]>;

/**
 * The viewport the canvas opens with, or undefined to fit every service into
 * view. A saved viewport is used unless:
 * - it is the server's default for a layout nobody has panned yet ((0,0) at
 *   zoom 1), which would park the first nodes under the top-left toolbar; or
 * - the screen is a phone (under 760px, where the inspector is full-screen):
 *   the saved view is one a wider screen panned to, and on a phone it shows
 *   cards half off screen. The first paint there is fit-all.
 */
export function initialViewport(
	saved: CanvasLayout["viewport"],
	screenWidth: number,
): Viewport | undefined {
	if (!saved) return undefined;
	if (isFullScreen(screenWidth)) return undefined;
	if (saved.x === 0 && saved.y === 0 && saved.zoom === 1) return undefined;
	return saved;
}
