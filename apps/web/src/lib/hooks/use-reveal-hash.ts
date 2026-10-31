import { useLocation } from "@tanstack/react-router";
import { useEffect } from "react";
import { prefersReducedMotion } from "#/lib/transitions";

/** How long to wait for an element whose queries have not drawn it yet. */
const GIVE_UP_MS = 6000;
/** Space left above the element so it does not sit under a sheet's own header. */
const SCROLL_MARGIN = "16px";

function reveal(element: Element) {
	if (!(element instanceof HTMLElement)) return;
	element.style.scrollMarginTop = SCROLL_MARGIN;
	const calm = prefersReducedMotion();
	element.scrollIntoView({
		block: "start",
		behavior: calm ? "auto" : "smooth",
	});
	if (calm) return;
	element.animate(
		[
			{
				backgroundColor:
					"color-mix(in oklab, var(--color-primary) 26%, transparent)",
			},
			{ backgroundColor: "transparent" },
		],
		{ duration: 1400, easing: "ease-out" },
	);
}

/**
 * Scrolls to, and flashes once, the element a URL hash points at.
 *
 * `find` turns the hash (without its "#") into a lookup for the element,
 * or undefined when the hash is not one this screen owns. The lookup runs
 * at once and then on every DOM change, because the element draws as its
 * data arrives; it gives up after a few seconds. Navigating to the same
 * hash again reveals again, since the router stamps each navigation with
 * its own key. `find` must be stable (module-level or memoised).
 */
export function useRevealHash(
	find: (hash: string) => (() => Element | null) | undefined,
): void {
	const navigation = useLocation({
		select: (location) =>
			`${location.hash}\u0000${location.state.__TSR_key ?? ""}`,
	});

	useEffect(() => {
		const [raw = ""] = navigation.split("\u0000");
		const lookup = find(raw.startsWith("#") ? raw.slice(1) : raw);
		if (!lookup) return;
		const found = lookup();
		if (found) {
			reveal(found);
			return;
		}
		const observer = new MutationObserver(() => {
			const late = lookup();
			if (!late) return;
			observer.disconnect();
			reveal(late);
		});
		observer.observe(document.body, { childList: true, subtree: true });
		const giveUp = window.setTimeout(() => observer.disconnect(), GIVE_UP_MS);
		return () => {
			observer.disconnect();
			window.clearTimeout(giveUp);
		};
	}, [navigation, find]);
}
