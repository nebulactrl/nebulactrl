import { type RefObject, useLayoutEffect, useState } from "react";

/** How much of the strip's ends the fade covers, in px. */
const FADE_PX = 24;
/** Air kept between a scrolled-to tab and the strip's edge, in px. */
const TAB_MARGIN_PX = 32;

/** Which ends of a scrolling strip have more behind them. */
export interface EdgeFade {
	start: boolean;
	end: boolean;
}

const NO_FADE: EdgeFade = { start: false, end: false };

/** The ends that hide content: scrolled away from the start, short of the end. */
export function edgeFade(
	scrollLeft: number,
	clientWidth: number,
	scrollWidth: number,
): EdgeFade {
	return {
		start: scrollLeft > 1,
		end: scrollLeft + clientWidth < scrollWidth - 1,
	};
}

/**
 * A `mask-image` that fades the strip out where it continues, so a clipped
 * tab reads as "more this way" and not as a cut label. Undefined when
 * everything fits.
 */
export function fadeMask(fade: EdgeFade): string | undefined {
	if (!fade.start && !fade.end) return undefined;
	const from = fade.start ? `transparent 0, #000 ${FADE_PX}px` : "#000 0";
	const to = fade.end
		? `#000 calc(100% - ${FADE_PX}px), transparent 100%`
		: "#000 100%";
	return `linear-gradient(to right, ${from}, ${to})`;
}

/** The scroll position that brings a tab into view with air around it, or null if it already is. */
export function scrollTarget(
	scrollLeft: number,
	viewport: { left: number; width: number },
	tab: { left: number; width: number },
): number | null {
	const before = tab.left - viewport.left - TAB_MARGIN_PX;
	const after = tab.left + tab.width - (viewport.left + viewport.width);
	if (before < 0) return Math.max(0, scrollLeft + before);
	if (after + TAB_MARGIN_PX > 0) return scrollLeft + after + TAB_MARGIN_PX;
	return null;
}

/** Tracks which ends of a horizontally scrolling element hide content. */
export function useEdgeFade(ref: RefObject<HTMLElement | null>): EdgeFade {
	const [fade, setFade] = useState<EdgeFade>(NO_FADE);
	useLayoutEffect(() => {
		const element = ref.current;
		if (!element) return;
		const update = () => {
			const next = edgeFade(
				element.scrollLeft,
				element.clientWidth,
				element.scrollWidth,
			);
			setFade((current) =>
				current.start === next.start && current.end === next.end
					? current
					: next,
			);
		};
		update();
		element.addEventListener("scroll", update, { passive: true });
		const observer = new ResizeObserver(update);
		observer.observe(element);
		// Tabs come and go (a route service has fewer), which changes the width.
		for (const child of Array.from(element.children)) observer.observe(child);
		return () => {
			element.removeEventListener("scroll", update);
			observer.disconnect();
		};
	}, [ref]);
	return fade;
}
