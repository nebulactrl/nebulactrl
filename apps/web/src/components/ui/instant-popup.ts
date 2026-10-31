import * as React from "react";
import { lastInputWasKeyboard } from "#/lib/input-modality";

/**
 * Ref callback for a popup (menu, popover) that marks it `data-instant` while
 * the keyboard is the reader's last input, so its enter/exit transition drops
 * to nothing: opening or closing by key is never animated. The mark follows
 * the input live (an arrow key while open, a click after), and the popup's
 * own classes read it (`data-instant:transition-none`).
 *
 * Also forwards the node to `forwardedRef`, so a caller's ref keeps working.
 */
export function useInstantPopupRef<T extends HTMLElement>(
	forwardedRef?: React.Ref<T>,
): React.RefCallback<T> {
	return React.useCallback(
		(node: T | null) => {
			if (typeof forwardedRef === "function") forwardedRef(node);
			else if (forwardedRef) forwardedRef.current = node;
			if (!node) return;

			// Listeners run after `input-modality`'s own capture listeners (installed
			// at import), so `lastInputWasKeyboard()` already reflects this event.
			const sync = () =>
				node.toggleAttribute("data-instant", lastInputWasKeyboard());
			sync();
			const events = ["keydown", "pointerdown", "mousedown", "touchstart"];
			for (const type of events) document.addEventListener(type, sync, true);
			return () => {
				for (const type of events)
					document.removeEventListener(type, sync, true);
				if (typeof forwardedRef === "function") forwardedRef(null);
				else if (forwardedRef) forwardedRef.current = null;
			};
		},
		[forwardedRef],
	);
}
