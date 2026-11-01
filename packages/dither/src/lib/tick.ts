import { eases } from "@nebulactrl/tokens";
import { useCallback, useRef } from "react";

interface TickState {
	observer: MutationObserver;
	lastText: string;
}

function reducedMotion(): boolean {
	return (
		typeof window !== "undefined" &&
		typeof window.matchMedia === "function" &&
		window.matchMedia("(prefers-reduced-motion: reduce)").matches
	);
}

/**
 * Ref callback for a live counter or ticking metric: every time the element's
 * text changes, it rolls the new value up from below (420ms, `smoothOut`) and
 * clears a 3px blur (220ms, `out`). `<span ref={useTick()}>{count}</span>`
 * needs no dependency, because React already re-renders the text; a
 * `MutationObserver` only has to notice it happened. The first value never
 * animates, and nothing moves under `prefers-reduced-motion`.
 */
export function useTick() {
	const stateRef = useRef<TickState | null>(null);

	return useCallback((el: HTMLElement | null) => {
		stateRef.current?.observer.disconnect();
		stateRef.current = null;
		if (!el) return;

		const react = () => {
			const text = el.textContent ?? "";
			const state = stateRef.current;
			if (state && state.lastText !== text && !reducedMotion()) {
				el.animate([{ transform: "translateY(60%)" }, { transform: "none" }], {
					duration: 420,
					easing: eases.smoothOut,
				});
				el.animate(
					[
						{ opacity: 0, filter: "blur(3px)" },
						{ opacity: 1, filter: "blur(0px)" },
					],
					{ duration: 220, easing: eases.out },
				);
			}
			if (state) state.lastText = text;
		};

		const observer = new MutationObserver(react);
		stateRef.current = { observer, lastText: el.textContent ?? "" };
		observer.observe(el, {
			characterData: true,
			childList: true,
			subtree: true,
		});
	}, []);
}
