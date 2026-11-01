import { durations, eases } from "@nebulactrl/tokens";
import { animate } from "motion/mini";
import type { AnimationPlaybackControls } from "motion/react";
import {
	type MouseEvent,
	type PointerEvent,
	type RefObject,
	useCallback,
	useEffect,
	useLayoutEffect,
	useRef,
} from "react";
import { bezier } from "#/lib/ease";
import { lastInputWasKeyboard } from "#/lib/input-modality";

/**
 * The sidebar's active-page highlight: one element that slides between the
 * rows, and the optimistic state behind it.
 *
 * It moves on `transform` alone (a Web Animation, `motion/mini`), so it keeps
 * its frame rate while the main thread renders the next page; Motion's
 * `layoutId` would run on the main thread and stall with it. Its size is set
 * once per target, never animated.
 *
 * It starts on the press, not when the router commits: `press` marks the row
 * `data-current` and moves the indicator in the same task as the pointerdown
 * (the routes of this app render in 20 to 90 ms), and the commit that follows
 * finds it already there. A navigation that never arrives (a press dragged
 * away) hands the highlight back after `PENDING_MS`. A row is highlighted by
 * `data-current`, which only this hook writes: React never touches it.
 */

/** How long a press waits for its navigation before the highlight returns. */
export const PENDING_MS = 1500;

/** A row's rectangle inside the scrolling content of the list that holds it. */
export interface Box {
	x: number;
	y: number;
	w: number;
	h: number;
}

export function sameBox(a: Box, b: Box): boolean {
	return (
		Math.abs(a.x - b.x) < 0.5 &&
		Math.abs(a.y - b.y) < 0.5 &&
		Math.abs(a.w - b.w) < 0.5 &&
		Math.abs(a.h - b.h) < 0.5
	);
}

export function boxTransform(box: Pick<Box, "x" | "y">): string {
	return `translate(${box.x}px, ${box.y}px)`;
}

/** The slide: the quick token, since navigating is something done all day. */
export const INDICATOR_SECONDS = durations.quick / 1000;

const EASE = bezier(eases.smoothOut);

function boxWithin(container: HTMLElement, row: HTMLElement): Box {
	const outer = container.getBoundingClientRect();
	const inner = row.getBoundingClientRect();
	return {
		x: inner.left - outer.left + container.scrollLeft,
		y: inner.top - outer.top + container.scrollTop,
		w: inner.width,
		h: inner.height,
	};
}

function currentOffset(el: HTMLElement): { x: number; y: number } {
	const transform = getComputedStyle(el).transform;
	if (!transform || transform === "none") return { x: 0, y: 0 };
	const matrix = new DOMMatrixReadOnly(transform);
	return { x: matrix.m41, y: matrix.m42 };
}

function prefersReducedMotion(): boolean {
	return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

/**
 * Wires `containerRef` (the scrolling list of nav links) and `indicatorRef`
 * (an absolutely positioned, `top-0 left-0` element inside it). `layoutKey`
 * changes when the rows change shape (the rail collapses, the drawer
 * opens): the indicator then jumps instead of sliding.
 */
export function useNavIndicator(
	containerRef: RefObject<HTMLElement | null>,
	indicatorRef: RefObject<HTMLElement | null>,
	layoutKey: string,
) {
	const pending = useRef<HTMLElement | null>(null);
	const timer = useRef<number | undefined>(undefined);
	const running = useRef<AnimationPlaybackControls | null>(null);
	const placed = useRef<{ row: HTMLElement; box: Box; key: string } | null>(
		null,
	);
	const keyRef = useRef(layoutKey);
	keyRef.current = layoutKey;

	const sync = useCallback(
		(slide: boolean) => {
			const container = containerRef.current;
			const indicator = indicatorRef.current;
			if (!(container && indicator)) return;
			const links = container.querySelectorAll<HTMLElement>("a[href]");
			// The routed page is the target unless a press is still waiting.
			const routed = container.querySelector<HTMLElement>(
				'a[data-status="active"]:not([data-nav-external])',
			);
			if (pending.current?.isConnected === false) pending.current = null;
			if (pending.current && pending.current === routed) {
				pending.current = null;
				window.clearTimeout(timer.current);
			}
			const target = pending.current ?? routed;
			for (const link of links) {
				if (link === target) link.setAttribute("data-current", "");
				else link.removeAttribute("data-current");
			}
			if (!target) {
				placed.current = null;
				indicator.style.opacity = "0";
				return;
			}
			const box = boxWithin(container, target);
			const before = placed.current;
			if (before && before.row === target && sameBox(before.box, box)) {
				return;
			}
			const key = keyRef.current;
			placed.current = { row: target, box, key };
			// `stop` commits where the running slide has got to, so what is read
			// next is exactly where the indicator is.
			running.current?.stop();
			running.current = null;
			indicator.style.width = `${box.w}px`;
			indicator.style.height = `${box.h}px`;
			indicator.style.opacity = "1";
			const to = boxTransform(box);
			const reveal = before === null;
			const moves =
				slide &&
				!reveal &&
				before.key === key &&
				!prefersReducedMotion() &&
				!lastInputWasKeyboard();
			if (!moves) {
				indicator.style.transform = to;
				return;
			}
			const from = currentOffset(indicator);
			running.current = animate(
				indicator,
				{ transform: [boxTransform(from), to] },
				{ duration: INDICATOR_SECONDS, ease: EASE },
			);
		},
		[containerRef, indicatorRef],
	);

	// Every commit: the route, the rail and the counts may all have moved a row.
	useLayoutEffect(() => {
		sync(true);
	});

	// A list that changes size (the window, the drawer) re-places it at once.
	useEffect(() => {
		const container = containerRef.current;
		if (!container || typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(() => sync(false));
		observer.observe(container);
		return () => {
			observer.disconnect();
			window.clearTimeout(timer.current);
		};
	}, [containerRef, sync]);

	const press = useCallback(
		(event: PointerEvent<HTMLElement> | MouseEvent<HTMLElement>) => {
			if (event.button !== 0) return;
			if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
				return;
			}
			if (!(event.target instanceof Element)) return;
			const link = event.target.closest<HTMLElement>("a[href]");
			const container = containerRef.current;
			if (!(link && container?.contains(link))) return;
			if (link.hasAttribute("data-nav-external")) return;
			if (pending.current === link) return;
			pending.current = link;
			window.clearTimeout(timer.current);
			timer.current = window.setTimeout(() => {
				pending.current = null;
				sync(true);
			}, PENDING_MS);
			sync(true);
		},
		[containerRef, sync],
	);

	return { press };
}
