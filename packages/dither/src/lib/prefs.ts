import {
	type RefObject,
	useEffect,
	useState,
	useSyncExternalStore,
} from "react";

/**
 * Reader hooks for the data-viz kit (charts, bars): the two media
 * preferences that change what is drawn, plus the size and visibility of the
 * element. Animated *backgrounds* use the shared clock in `motion.ts`
 * instead; these never run a frame loop.
 */

function mediaStore(query: string) {
	const subscribe = (onChange: () => void) => {
		const list = window.matchMedia(query);
		list.addEventListener("change", onChange);
		return () => list.removeEventListener("change", onChange);
	};
	const snapshot = () => window.matchMedia(query).matches;
	return { subscribe, snapshot };
}

const reduced = mediaStore("(prefers-reduced-motion: reduce)");
const forced = mediaStore("(forced-colors: active)");

/** `true` while the visitor asks for reduced motion. */
export function useReducedMotionPref(): boolean {
	return useSyncExternalStore(reduced.subscribe, reduced.snapshot, () => false);
}

/** `true` under Windows High Contrast / forced colours: draw solid fills. */
export function useForcedColors(): boolean {
	return useSyncExternalStore(forced.subscribe, forced.snapshot, () => false);
}

/**
 * The element's content width in whole px (null until measured). Updates
 * only when the integer width changes, so a chart re-renders on resize
 * steps, not on every sub-pixel.
 */
export function useElementWidth(ref: RefObject<Element | null>): number | null {
	const [width, setWidth] = useState<number | null>(null);
	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const observer = new ResizeObserver(([entry]) => {
			if (entry) setWidth(Math.floor(entry.contentRect.width));
		});
		observer.observe(el);
		return () => observer.disconnect();
	}, [ref]);
	return width;
}

/** Whether the element intersects the viewport (false until observed). */
export function useOnScreen(ref: RefObject<Element | null>): boolean {
	const [on, setOn] = useState(false);
	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const observer = new IntersectionObserver(([entry]) =>
			setOn(entry?.isIntersecting ?? false),
		);
		observer.observe(el);
		return () => observer.disconnect();
	}, [ref]);
	return on;
}

/** Below this cell size a pattern is mush: charts fall back to solid fills. */
export const MIN_DITHER_CELL = 1.5;
