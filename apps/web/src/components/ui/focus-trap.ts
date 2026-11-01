import type * as React from "react";

const TABBABLE =
	'a[href],area[href],button,input:not([type="hidden"]),select,textarea,summary,[tabindex],[contenteditable=""],[contenteditable="true"]';

function isTabbable(element: HTMLElement, container: HTMLElement): boolean {
	if (element.closest("[data-base-ui-focus-guard]")) return false;
	if (element.tabIndex < 0) return false;
	if (
		(element instanceof HTMLButtonElement ||
			element instanceof HTMLInputElement ||
			element instanceof HTMLSelectElement ||
			element instanceof HTMLTextAreaElement) &&
		element.disabled
	) {
		return false;
	}
	// A control inside a closed disclosure, an `inert` or hidden subtree
	// cannot take focus, so Tab never lands on it.
	if (element.closest("[inert],[hidden]")) return false;
	if (!container.contains(element)) return false;
	return element.getClientRects().length > 0;
}

/**
 * The controls Tab visits inside `container`, in DOM order. A disabled
 * control is skipped, so a dialog whose last button is disabled still has a
 * real last stop.
 */
function tabbableWithin(container: HTMLElement): HTMLElement[] {
	return Array.from(container.querySelectorAll<HTMLElement>(TABBABLE)).filter(
		(element) => isTabbable(element, container),
	);
}

/**
 * Keeps Tab and Shift+Tab inside a modal popup: past the last control focus
 * wraps to the first, and back again. Put it on the popup's `onKeyDown`.
 *
 * The primitive's own focus guards stop at the last tabbable they know of;
 * this wraps on the controls that are enabled and visible right now, so a
 * disabled last button cannot let focus out to the page behind. Keys from a
 * portalled child (a select list, a menu) bubble through React but are not
 * inside the popup's DOM, and are left alone.
 */
export function trapTabKey(event: React.KeyboardEvent<HTMLElement>): void {
	if (event.key !== "Tab" || event.defaultPrevented) return;
	const popup = event.currentTarget;
	const target = event.target;
	if (!(target instanceof Node) || !popup.contains(target)) return;

	const stops = tabbableWithin(popup);
	if (stops.length === 0) {
		event.preventDefault();
		popup.focus();
		return;
	}
	const first = stops[0];
	const last = stops.at(-1);
	if (!first || !last) return;
	if (event.shiftKey && (target === first || target === popup)) {
		event.preventDefault();
		last.focus();
	} else if (!event.shiftKey && target === last) {
		event.preventDefault();
		first.focus();
	}
}
