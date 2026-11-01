import { durations, eases } from "@nebulactrl/tokens";
import { animate } from "motion/mini";
import type { AnimationPlaybackControls } from "motion/react";
import { type RefObject, useLayoutEffect, useRef } from "react";
import { bezier } from "#/lib/ease";
import { sidebarAnimates } from "./use-sidebar";

/**
 * The sidebar's collapse and expand, with no per-frame layout.
 *
 * Animating the rail's width as a CSS transition would resize, every frame,
 * the sidebar and the whole content column beside it (the canvas, its
 * ResizeObserver, two halftone layers behind a `contrast(60)` filter) and
 * re-rasterize all of it. So the layout changes once, in the commit that
 * flips `collapsed`, and what the reader sees moving is a picture of it:
 *
 * - the content column (top bar and screen) is FLIPped: it is already at its
 *   final width and left edge, and slides from where it was with a
 *   `transform` animation;
 * - an expanding sidebar is revealed with a `clip-path` that sweeps out
 *   from the left edge, in step with the column's edge;
 * - a collapsing sidebar leaves a clone of its old self on top, swept
 *   shut and faded out the same way, over the rail that is already in place.
 *
 * All of it runs as Web Animations (`motion/mini`), off the main thread.
 * A toggle from the keyboard (⌘B) skips it; reduced motion keeps only a
 * short fade of the sidebar. A toggle during a toggle starts from wherever
 * the column is.
 */

const EASE = bezier(eases.smoothOut);

/**
 * How long the change runs, in seconds. Collapsing is the quicker of the
 * two: the reader is dismissing the labels, not waiting to read them.
 */
export function railDuration(collapsing: boolean, reduced: boolean): number {
	const ms = reduced
		? durations.quick
		: collapsing
			? Math.round(durations.fast * 0.8)
			: durations.fast;
	return ms / 1000;
}

/**
 * The column's keyframes: it starts `delta` px from rest, on top of wherever
 * it already was (`offset`, from an interrupted slide), and arrives at none.
 */
export function columnKeyframes(
	offset: number,
	delta: number,
): { transform: [string, string] } {
	return { transform: [`translateX(${offset + delta}px)`, "none"] };
}

/** A `clip-path` that hides the right-hand `px` of an element. */
export function clipFromRight(px: number): string {
	return `inset(0px ${px}px 0px 0px)`;
}

function prefersReducedMotion(): boolean {
	return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

/** The element's translation along x right now, read from the running animation. */
function offsetX(el: HTMLElement): number {
	const transform = getComputedStyle(el).transform;
	if (!transform || transform === "none") return 0;
	return new DOMMatrixReadOnly(transform).m41;
}

interface RailChange {
	/** Old size minus new size in px: positive when the rail shrank. */
	delta: number;
	collapsing: boolean;
	/** False for a keyboard toggle, and for the narrow drawer's breakpoint. */
	animated: boolean;
}

/**
 * Calls `onChange` from the layout effect of a commit in which `collapsed`
 * flipped: the DOM already holds the new layout, `measure` reads it, and the
 * size from the previous commit is the old one.
 */
function useRailChange(
	ref: RefObject<HTMLElement | null>,
	measure: (el: HTMLElement) => number,
	collapsed: boolean,
	narrow: boolean,
	onChange: (el: HTMLElement, change: RailChange) => void,
) {
	const last = useRef<{
		size: number;
		collapsed: boolean;
		narrow: boolean;
	} | null>(null);
	// Every commit: a window resize across the breakpoint moves the column
	// without flipping `collapsed`, and the next flip must start from there.
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		const before = last.current;
		const size = measure(el);
		last.current = { size, collapsed, narrow };
		if (!before || before.collapsed === collapsed || before.size === size) {
			return;
		}
		onChange(el, {
			delta: before.size - size,
			collapsing: collapsed,
			animated: !(narrow || before.narrow) && sidebarAnimates(),
		});
	});
}

/** The content column beside the rail: slides from its old left edge to its new one. */
export function useColumnSlide(
	ref: RefObject<HTMLElement | null>,
	collapsed: boolean,
	narrow: boolean,
) {
	const running = useRef<AnimationPlaybackControls | null>(null);
	useRailChange(
		ref,
		(el) => el.offsetLeft,
		collapsed,
		narrow,
		(el, { delta, collapsing, animated }) => {
			// `stop` commits the running animation's values, so what is read next
			// is exactly where the column is.
			running.current?.stop();
			running.current = null;
			const offset = offsetX(el);
			el.style.removeProperty("transform");
			if (!animated || prefersReducedMotion()) return;
			running.current = animate(el, columnKeyframes(offset, delta), {
				duration: railDuration(collapsing, false),
				ease: EASE,
			});
		},
	);
}

/** A clone of the sidebar as it was, taken before a pointer collapse changes it. */
let stagedGhost: HTMLElement | null = null;

/**
 * Photographs the sidebar for its collapse: call it just before the toggle,
 * and only for one that will animate. The clone is inert, id-free (the
 * sidebar's own id stays unique) and carries over the pixels of canvases.
 */
export function stageRailGhost(aside: HTMLElement | null) {
	stagedGhost?.remove();
	stagedGhost = null;
	if (!aside) return;
	const ghost = aside.cloneNode(true) as HTMLElement;
	ghost.removeAttribute("id");
	for (const node of ghost.querySelectorAll("[id]")) node.removeAttribute("id");
	ghost.setAttribute("aria-hidden", "true");
	ghost.setAttribute("inert", "");
	const copies = ghost.querySelectorAll("canvas");
	const sources = [...aside.querySelectorAll("canvas")];
	for (const [index, canvas] of sources.entries()) {
		copies[index]?.getContext("2d")?.drawImage(canvas, 0, 0);
	}
	Object.assign(ghost.style, {
		position: "absolute",
		top: "0",
		left: "0",
		height: "100%",
		width: `${aside.offsetWidth}px`,
		zIndex: "31",
		pointerEvents: "none",
	});
	stagedGhost = ghost;
}

/** The sidebar itself: revealed from the left edge, or covered by its old self sweeping shut. */
export function useRailReveal(
	ref: RefObject<HTMLElement | null>,
	collapsed: boolean,
	narrow: boolean,
) {
	const running = useRef<AnimationPlaybackControls | null>(null);
	const ghost = useRef<HTMLElement | null>(null);
	useRailChange(
		ref,
		(el) => el.offsetWidth,
		collapsed,
		narrow,
		(el, { delta, collapsing, animated }) => {
			running.current?.stop();
			running.current = null;
			el.style.removeProperty("clip-path");
			el.style.removeProperty("opacity");
			ghost.current?.remove();
			ghost.current = null;
			const staged = stagedGhost;
			stagedGhost = null;
			if (!animated) return;
			const reduced = prefersReducedMotion();
			const duration = railDuration(collapsing, reduced);
			if (reduced) {
				running.current = animate(el, { opacity: [0.4, 1] }, { duration });
				return;
			}
			if (collapsing) {
				const host = el.parentElement;
				if (!(staged && host)) return;
				host.append(staged);
				ghost.current = staged;
				const sweep = animate(
					staged,
					{
						clipPath: [clipFromRight(0), clipFromRight(delta)],
						opacity: [1, 0],
					},
					{ duration, ease: EASE },
				);
				running.current = sweep;
				void sweep.then(() => {
					if (ghost.current === staged) ghost.current = null;
					staged.remove();
				});
				return;
			}
			const sweep = animate(
				el,
				{ clipPath: [clipFromRight(-delta), clipFromRight(0)] },
				{ duration, ease: EASE },
			);
			running.current = sweep;
			void sweep.then(() => {
				if (running.current === sweep) el.style.removeProperty("clip-path");
			});
		},
	);
}
