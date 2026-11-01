import { type RefObject, useLayoutEffect } from "react";
import { scrollTarget } from "#/lib/hooks/use-edge-fade";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { prefersReducedMotion } from "#/lib/transitions";

export interface TabGlideOptions {
	/** The strip's current item, a direct child: `[role="tab"][data-active]`, `[aria-current="page"]`. */
	active: string;
	/** Every item of the strip, for resize tracking. */
	items: string;
	/** Attributes whose change moves the selection. */
	attributes: string[];
	/** A raised thumb behind the item (`pressed`) instead of a 2px bar under it. */
	thumb: boolean;
	/** Scroll the strip so the current item is in view when it changes. */
	scrolls: boolean;
	enabled: boolean;
}

/**
 * The glide for a tab strip: one indicator springs from item to item
 * (a bar under it, or a thumb behind it). Its first placement, a panel or
 * screen opening, lands without motion, and reduced motion never glides.
 * With `scrolls`, the current item is also brought into view, once per change
 * (a route change, a key press), never on every scroll.
 */
export function useTabGlide(
	listRef: RefObject<HTMLElement | null>,
	indicatorRef: RefObject<HTMLElement | null>,
	{
		active: activeSelector,
		items,
		attributes,
		thumb,
		scrolls,
		enabled,
	}: TabGlideOptions,
) {
	useLayoutEffect(() => {
		const list = listRef.current;
		const indicator = indicatorRef.current;
		if (!list || !indicator || !enabled) return;

		let placed = false;
		let scrolledTo: HTMLElement | null = null;
		let frame = 0;
		const sync = () => {
			cancelAnimationFrame(frame);
			frame = requestAnimationFrame(() => {
				const active = list.querySelector<HTMLElement>(
					`:scope > ${activeSelector}`,
				);
				if (!active) {
					indicator.style.opacity = "0";
					return;
				}
				// A key press moves the selection without gliding: the reader is
				// navigating by keyboard and the motion only makes them wait.
				// The glide itself is `.t-tabs-pill` (transitions.css); `none` lands the
				// pill without it, an empty value hands the clock back to the class.
				indicator.style.transition =
					placed && !prefersReducedMotion() && !lastInputWasKeyboard()
						? ""
						: "none";
				indicator.style.width = `${active.offsetWidth}px`;
				if (thumb) {
					indicator.style.height = `${active.offsetHeight}px`;
					indicator.style.transform = `translate(${active.offsetLeft}px, ${active.offsetTop}px)`;
				} else {
					indicator.style.height = "2px";
					indicator.style.transform = `translateX(${active.offsetLeft}px)`;
				}
				indicator.style.opacity = "1";
				placed = true;
				if (scrolls && active !== scrolledTo) {
					scrolledTo = active;
					const box = list.getBoundingClientRect();
					const tab = active.getBoundingClientRect();
					const target = scrollTarget(
						list.scrollLeft,
						{ left: box.left, width: list.clientWidth },
						{ left: tab.left, width: tab.width },
					);
					if (target !== null) list.scrollTo({ left: target });
				}
			});
		};

		const resize = new ResizeObserver(sync);
		const mutation = new MutationObserver(sync);
		resize.observe(list);
		for (const tab of list.querySelectorAll<HTMLElement>(items))
			resize.observe(tab);
		mutation.observe(list, {
			attributes: true,
			childList: true,
			subtree: true,
			attributeFilter: attributes,
		});
		list.addEventListener("scroll", sync, { passive: true });
		sync();
		return () => {
			cancelAnimationFrame(frame);
			resize.disconnect();
			mutation.disconnect();
			list.removeEventListener("scroll", sync);
		};
	}, [enabled, thumb, scrolls]);
}
