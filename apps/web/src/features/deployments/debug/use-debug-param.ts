import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useEffect, useRef } from "react";
import { OPEN_OTHER_LAYER_SELECTOR } from "#/components/ui/sheet-dismiss";
import { CHORD_WINDOW_MS } from "#/features/canvas/inspector/use-tab-chords";

/**
 * The debug panel's state lives in the address: `&debug=<deploymentId>` next
 * to `?service=&tab=`, so a link or a reload lands on the same panel. Opening
 * adds a history entry (Back closes the panel); closing replaces it.
 */
export function useDebugParam(): {
	deploymentId: string | undefined;
	open(deploymentId: string): void;
	close(): void;
} {
	const search = useSearch({ strict: false }) as { debug?: unknown };
	const navigate = useNavigate();
	const deploymentId =
		typeof search.debug === "string" && search.debug !== ""
			? search.debug
			: undefined;

	const write = useCallback(
		(next: string | undefined, replace: boolean) => {
			void navigate({
				to: ".",
				search: (previous: Record<string, unknown>) => ({
					...previous,
					debug: next,
				}),
				replace,
			} as never);
		},
		[navigate],
	);
	const open = useCallback((id: string) => write(id, false), [write]);
	const close = useCallback(() => write(undefined, true), [write]);
	return { deploymentId, open, close };
}

function isTyping(target: EventTarget | null): boolean {
	if (!(target instanceof HTMLElement)) return false;
	return (
		target.isContentEditable ||
		target.closest("input, textarea, select, [contenteditable='true']") !== null
	);
}

/**
 * `d` runs `onPress` while the component is mounted (a deployment is
 * expanded). It stays out of the way of typing, modifier shortcuts, any open
 * dialog, menu or the debug panel itself, and of the `g d` tab chord.
 */
export function useDebugShortcut(onPress: () => void) {
	const latest = useRef(onPress);
	latest.current = onPress;
	useEffect(() => {
		let gAt = 0;
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.defaultPrevented || event.repeat) return;
			if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey)
				return;
			if (isTyping(event.target)) return;
			if (document.querySelector(OPEN_OTHER_LAYER_SELECTOR) !== null) return;
			const key = event.key.toLowerCase();
			if (key === "g") {
				gAt = event.timeStamp;
				return;
			}
			const chord = gAt !== 0 && event.timeStamp - gAt <= CHORD_WINDOW_MS;
			gAt = 0;
			if (key !== "d" || chord) return;
			event.preventDefault();
			latest.current();
		};
		document.addEventListener("keydown", onKeyDown);
		return () => document.removeEventListener("keydown", onKeyDown);
	}, []);
}
