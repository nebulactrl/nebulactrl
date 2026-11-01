import { eases } from "@nebulactrl/tokens";
import type { Transition } from "motion/react";
import { bezier } from "#/lib/ease";

/**
 * The console's one settle curve (`--ease-smooth-out`): an ease-out that
 * decelerates hard and lands without
 * overshoot. A CSS easing string for `Element.animate()` and
 * `transition-timing-function`; each caller picks its own duration.
 */
export const SPRING = eases.smoothOut;

/** `SPRING` as the cubic-bezier tuple `motion/react` and `motion/mini` take. */
export const SPRING_EASE = bezier(SPRING);

/**
 * `SPRING` for `motion/react`'s `transition` prop at 560ms, the duration
 * for a card, toast or portal move. A layout animation that needs a
 * different literal duration spreads this and overrides `duration`.
 */
export const SPRING_MOTION: Transition = {
	type: "tween",
	duration: 0.56,
	ease: SPRING_EASE,
};
