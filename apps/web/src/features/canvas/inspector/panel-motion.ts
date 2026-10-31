import { durations, eases } from "@nebulactrl/tokens";
import { animate } from "motion/mini";
import type { AnimationPlaybackControls } from "motion/react";
import { useCallback, useLayoutEffect, useRef } from "react";
import { bezier } from "#/lib/ease";

/**
 * The inspector's open and close, on `transform` and `opacity` only.
 *
 * Both are compositor properties, and the animation is a Web Animation
 * (`motion/mini`, with the full `transform` string rather than the `x`
 * shorthand), so it keeps its frame rate while the main thread mounts the
 * panel's content. It is interruptible: a close during the open, or an open
 * during the close, stops the running animation where it is and heads back
 * from that point, in time proportional to what is left to travel. A swap to
 * another service does not call for one at all: the panel stays open and
 * only its content changes.
 */

/** How far past the viewport edge the closed panel rests, in px (the gap it slides across). */
export const SLIDE_PAST_EDGE = 24;

/** The axis the panel travels on: x from the right edge, y from the bottom of a phone. */
export type SlideAxis = "x" | "y";
export type SlideState = "open" | "closed";

/** Where the panel is: its translation along the axis in px, and its opacity. */
export interface SlideFrame {
	offset: number;
	opacity: number;
}

export interface SlidePlan {
	from: SlideFrame;
	to: SlideFrame;
	/** Seconds, as `motion` takes them. */
	duration: number;
}

/** The translation at which the panel is off screen, in px. */
export function closedOffset(extent: number, axis: SlideAxis): number {
	return axis === "x" ? extent + SLIDE_PAST_EDGE : extent;
}

/**
 * The shortest an interrupted slide runs, as a share of the full duration:
 * a panel that is nearly there does not crawl the last pixels.
 */
const MIN_SHARE = 0.35;

/**
 * What to animate to take the panel from `from` to `state`.
 *
 * Opening takes `durations.fast` and closing `durations.quick`: it leaves
 * faster than it arrives. Under reduced motion the panel does not travel, it
 * cross-fades over `durations.quick` both ways. A slide that begins part-way
 * runs for the share of the full duration that is left to cover.
 */
export function planSlide(args: {
	state: SlideState;
	from: SlideFrame;
	extent: number;
	axis: SlideAxis;
	reduced: boolean;
}): SlidePlan {
	const { state, extent, axis, reduced } = args;
	const closed = closedOffset(extent, axis);
	const open = state === "open";
	const full = reduced
		? durations.quick
		: open
			? durations.fast
			: durations.quick;
	const to: SlideFrame = {
		offset: reduced || open ? 0 : closed,
		opacity: open ? 1 : 0,
	};
	const from: SlideFrame = {
		offset: reduced ? 0 : args.from.offset,
		opacity: args.from.opacity,
	};
	const travelled =
		reduced || closed === 0 ? 0 : Math.abs(to.offset - from.offset) / closed;
	const share = Math.max(
		MIN_SHARE,
		Math.min(1, Math.max(travelled, Math.abs(to.opacity - from.opacity))),
	);
	return { from, to, duration: (full * share) / 1000 };
}

/** The Web Animations keyframes for a plan; `transform` is left out under reduced motion. */
export function slideKeyframes(
	plan: SlidePlan,
	axis: SlideAxis,
	reduced: boolean,
): { opacity: [number, number]; transform?: [string, string] } {
	const move = axis === "x" ? "translateX" : "translateY";
	const opacity: [number, number] = [plan.from.opacity, plan.to.opacity];
	if (reduced) return { opacity };
	return {
		opacity,
		transform: [
			`${move}(${plan.from.offset}px)`,
			plan.to.offset === 0 ? "none" : `${move}(${plan.to.offset}px)`,
		],
	};
}

/** The panel's translation along `axis` right now, read from the running animation. */
function currentFrame(el: HTMLElement, axis: SlideAxis): SlideFrame {
	const style = getComputedStyle(el);
	const transform = style.transform;
	let offset = 0;
	if (transform && transform !== "none") {
		const matrix = new DOMMatrixReadOnly(transform);
		offset = axis === "x" ? matrix.m41 : matrix.m42;
	}
	return { offset, opacity: Number.parseFloat(style.opacity) };
}

const EASE = bezier(eases.smoothOut);

/**
 * Drives a panel's slide from `open`. The returned ref goes on the panel's
 * element: it slides in when it mounts open, and from then on follows
 * `open` (the panel stays mounted until its exit animation has finished,
 * which the dialog waits for).
 *
 * An element marked `data-instant` (the keyboard opened or closed it) gets
 * no animation: it is simply there, or gone.
 */
export function usePanelSlide(
	open: boolean,
	axis: SlideAxis,
	reduced: boolean,
): (el: HTMLElement | null) => void {
	const elRef = useRef<HTMLElement | null>(null);
	const running = useRef<AnimationPlaybackControls | null>(null);
	const driven = useRef<SlideState | null>(null);
	const latest = useRef({ axis, reduced });
	latest.current = { axis, reduced };

	const drive = useCallback(
		(el: HTMLElement, state: SlideState, fresh: boolean) => {
			if (driven.current === state) return;
			driven.current = state;
			// `stop` commits the running animation's values, so what is read next
			// is exactly where the panel is.
			running.current?.stop();
			running.current = null;
			if (el.hasAttribute("data-instant")) {
				el.style.removeProperty("transform");
				el.style.removeProperty("opacity");
				return;
			}
			const { axis, reduced } = latest.current;
			const extent = axis === "x" ? el.offsetWidth : el.offsetHeight;
			const from = fresh
				? { offset: closedOffset(extent, axis), opacity: 0 }
				: currentFrame(el, axis);
			const plan = planSlide({ state, from, extent, axis, reduced });
			running.current = animate(el, slideKeyframes(plan, axis, reduced), {
				duration: plan.duration,
				ease: EASE,
			});
		},
		[],
	);

	useLayoutEffect(() => {
		const el = elRef.current;
		if (el) drive(el, open ? "open" : "closed", false);
	}, [open, drive]);

	return useCallback(
		(el: HTMLElement | null) => {
			if (el) {
				elRef.current = el;
				drive(el, "open", true);
				return;
			}
			running.current?.stop();
			running.current = null;
			elRef.current = null;
			driven.current = null;
		},
		[drive],
	);
}
