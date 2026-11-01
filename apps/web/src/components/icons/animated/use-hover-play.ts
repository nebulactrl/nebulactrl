import { useState } from "react";
import { useHoverCapable } from "#/lib/hooks/use-hover-capable";
import { shouldPlay } from "./playback";

/**
 * Plays an icon while a pointer rests on its control, for the control's
 * `onPointerEnter` / `onPointerLeave`. Never on keyboard focus (there is no
 * focus handler) and never because a route changed (nothing here reads
 * route state). Touch and pen-less devices without true hover never play.
 * Reduced motion is applied by the icon itself.
 */
export function useHoverPlay() {
	const hoverCapable = useHoverCapable();
	const [hovered, setHovered] = useState(false);
	return {
		playing:
			hovered &&
			shouldPlay("hover", {
				reducedMotion: false,
				keyboard: false,
				hoverCapable,
			}),
		handlers: {
			onPointerEnter: () => setHovered(true),
			onPointerLeave: () => setHovered(false),
		},
	} as const;
}
