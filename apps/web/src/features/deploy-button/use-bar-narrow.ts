import { type RefObject, useLayoutEffect, useState } from "react";

/** Under this bar width the production key is replaced by the icon stand-in. */
export const NARROW_BAR_PX = 760;

/** Whether a top bar `width` px wide has room for the guarded key. */
export function isNarrowBar(width: number): boolean {
	return width < NARROW_BAR_PX;
}

/**
 * Whether the top bar around `probe` is narrower than the guarded key needs.
 * The bar is the nearest `header`; its content width is what the `@container`
 * queries of its children see. JS and not a CSS `hidden` class: a control that
 * is only hidden is still in the document and a script can click it, so the
 * variant that does not fit must not be rendered at all.
 */
export function useBarNarrow(probe: RefObject<HTMLElement | null>): boolean {
	const [narrow, setNarrow] = useState(false);
	useLayoutEffect(() => {
		const bar = probe.current?.closest("header");
		if (!bar) return;
		const measure = (width: number) => setNarrow(isNarrowBar(width));
		measure(bar.getBoundingClientRect().width - paddingX(bar));
		const observer = new ResizeObserver((entries) => {
			const box = entries[0]?.contentRect;
			if (box) measure(box.width);
		});
		observer.observe(bar);
		return () => observer.disconnect();
	}, [probe]);
	return narrow;
}

function paddingX(element: Element): number {
	const style = getComputedStyle(element);
	return (
		Number.parseFloat(style.paddingLeft) +
		Number.parseFloat(style.paddingRight) +
		Number.parseFloat(style.borderLeftWidth) +
		Number.parseFloat(style.borderRightWidth)
	);
}
