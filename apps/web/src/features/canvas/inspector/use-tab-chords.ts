import { useEffect, useRef } from "react";
import { OPEN_OTHER_LAYER_SELECTOR } from "#/components/ui/sheet-dismiss";
import { type InspectorTab, tabForChordKey } from "./tab-ids";

/** How long `g` waits for its second key. */
export const CHORD_WINDOW_MS = 1000;

function isTyping(target: EventTarget | null): boolean {
	if (!(target instanceof HTMLElement)) return false;
	return (
		target.isContentEditable ||
		target.closest("input, textarea, select, [contenteditable='true']") !== null
	);
}

/**
 * Vim-style tab chords while the inspector is open: `g` then `o`, `d`, `v`,
 * `m`, `a`, `l` or `s`. They stay out of the way of typing, modifier
 * shortcuts and any dialog or menu that is open.
 */
export function useTabChords(
	enabled: boolean,
	onTab: (tab: InspectorTab) => void,
) {
	const onTabRef = useRef(onTab);
	onTabRef.current = onTab;
	useEffect(() => {
		if (!enabled) return;
		let armedAt = 0;
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.defaultPrevented || event.repeat) return;
			if (event.metaKey || event.ctrlKey || event.altKey) return;
			if (isTyping(event.target)) return;
			if (document.querySelector(OPEN_OTHER_LAYER_SELECTOR) !== null) return;

			const key = event.key.toLowerCase();
			const armed =
				armedAt !== 0 && event.timeStamp - armedAt <= CHORD_WINDOW_MS;
			if (armed) {
				armedAt = 0;
				const tab = tabForChordKey(key);
				if (tab) {
					event.preventDefault();
					onTabRef.current(tab);
					return;
				}
			}
			if (key === "g" && !event.shiftKey) armedAt = event.timeStamp;
		};
		document.addEventListener("keydown", onKeyDown);
		return () => document.removeEventListener("keydown", onKeyDown);
	}, [enabled]);
}
